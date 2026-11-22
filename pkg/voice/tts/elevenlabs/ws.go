package elevenlabs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// The Text-to-Dialogue websocket transport (#702, opt-in via [WithTransport]).
//
// Wire protocol, verified against the real API on 2026-11-22:
//   - Dial wss://…/v1/text-to-dialogue/stream-input?model_id=…&output_format=…
//     &language_code=… with the xi-api-key header. Only the eleven_v3 / eleven_v4
//     families are accepted.
//   - The first message registers {"voices":[id],"voice_settings":{…}}. Voice and
//     settings are fixed for the socket's life, so sockets are pooled by [wsKey].
//   - A sentence is {"inputs":[{"text":"<sentence> ","voice_id":id,"new_turn":true}],
//     "flush":true}. flush is mandatory (without it the server buffers ~40
//     characters and a short line never renders); the trailing space is there
//     because inputs are concatenated raw.
//   - The server answers {"audio":"<base64 pcm>"} frames and exactly one
//     {"is_final_audio_for_turn":true} per flush, in order. An error is a
//     {"message","error","code"} frame followed by close 1008.
//   - 20 s without a client message closes the socket; {"keep_alive":true} resets
//     that. An open socket holds one dialogue-session slot of the account's plan.
//   - There is no cancel message and a socket is strictly sequential: the only way
//     to abort a sentence is to close its socket.

const (
	wsDialoguePath = "/v1/text-to-dialogue/stream-input"

	// wsKeepAliveInterval is how often an open socket is sent keep_alive: half
	// the server's fixed 20 s inactivity limit. It is sent while a sentence is
	// streaming too, since playback back-pressure can stretch one sentence's
	// read past 20 s with no client message in between.
	wsKeepAliveInterval = 10 * time.Second

	// wsIdleTTL is how long a socket may sit unleased before it is closed, so a
	// silent table stops holding dialogue-session slots. [Client.Warm] (a speech
	// start) refreshes it.
	wsIdleTTL = 60 * time.Second

	// wsFirstAudioTimeout bounds the wait for a sentence's first frame; the
	// websocket twin of the HTTP transport's ResponseHeaderTimeout.
	wsFirstAudioTimeout = 5 * time.Second

	// wsDialTimeout bounds TCP + TLS + the websocket handshake, and one write.
	wsDialTimeout = 5 * time.Second

	// wsSocketsPerKey is the per-voice socket cap: the pipelined look-ahead
	// (#626) has sentence k+1 synthesizing while k is still being read, and a
	// socket is strictly sequential, so a voice needs a pair.
	wsSocketsPerKey = 2

	// wsSocketsTotal caps the sockets one client (one Voice Session) holds
	// across all voices: four NPC voices with their look-ahead pair open at
	// once. Past it an idle socket of another voice is closed to make room, and
	// when none is idle the sentence goes over HTTP.
	wsSocketsTotal = 8

	// wsWarmKeys is how many most-recently-used voices [Client.Warm] pre-dials.
	wsWarmKeys = 2
)

// wsTuning is the pool's timing and caps; production uses [defaultWSTuning].
type wsTuning struct {
	keepAlive  time.Duration
	idleTTL    time.Duration
	firstAudio time.Duration
	perKey     int
	total      int
}

func defaultWSTuning() wsTuning {
	return wsTuning{
		keepAlive:  wsKeepAliveInterval,
		idleTTL:    wsIdleTTL,
		firstAudio: wsFirstAudioTimeout,
		perKey:     wsSocketsPerKey,
		total:      wsSocketsTotal,
	}
}

// wsKey is everything a dialogue socket fixes at its handshake and first
// message: sentences may share a socket only when all of it matches.
type wsKey struct {
	voiceID  string
	model    string
	format   string
	language string
	seed     string
	// register is the JSON first message (voices, voice_settings,
	// pronunciation dictionaries).
	register string
}

func newWSKey(voiceID string, s Settings) (wsKey, error) {
	reg, err := json.Marshal(struct {
		Voices                          []string                         `json:"voices"`
		VoiceSettings                   *VoiceSettings                   `json:"voice_settings,omitempty"`
		PronunciationDictionaryLocators []PronunciationDictionaryLocator `json:"pronunciation_dictionary_locators,omitempty"`
	}{[]string{voiceID}, s.VoiceSettings, s.PronunciationDictionaryLocators})
	if err != nil {
		return wsKey{}, fmt.Errorf("marshal voice registration: %w", err)
	}
	k := wsKey{voiceID: voiceID, model: s.ModelID, format: s.OutputFormat, language: s.LanguageCode, register: string(reg)}
	if s.Seed != nil {
		k.seed = strconv.FormatInt(*s.Seed, 10)
	}
	return k, nil
}

// StreamError is an error frame a dialogue socket sent ({"message","error",
// "code"}); the server closes the socket right after it.
type StreamError struct {
	Code    int    // websocket close code the server announces, 1008 in practice
	Kind    string // machine-readable reason, e.g. "unsupported_language"
	Message string
}

func (e *StreamError) Error() string {
	return fmt.Sprintf("elevenlabs dialogue socket: %s (code %d): %s", e.Kind, e.Code, e.Message)
}

// wsStartError is a sentence that failed before any audio on a socket that was
// dialled for it: a dead connection or a first-audio timeout. It satisfies
// [net.Error] so the dispatch layer's retry policy treats it like any other
// transport failure and starts the sentence again.
type wsStartError struct {
	err     error
	timeout bool
}

func (e *wsStartError) Error() string   { return e.err.Error() }
func (e *wsStartError) Unwrap() error   { return e.err }
func (e *wsStartError) Timeout() bool   { return e.timeout }
func (e *wsStartError) Temporary() bool { return true }

var (
	errWSFirstAudioTimeout = errors.New("no audio from the dialogue socket in time")
	errWSIdle              = errors.New("dialogue socket closed: idle")
	errWSClosed            = errors.New("dialogue socket closed: client closed")
	errWSEvicted           = errors.New("dialogue socket closed: made room for another voice")
	errWSStray             = errors.New("dialogue socket sent a frame nobody asked for")
)

// WSStats counts what the websocket transport did since the client was built.
type WSStats struct {
	Dials     int64 // sockets opened (for a sentence or ahead of one)
	Reuses    int64 // sentences that rode an already-open socket
	Redials   int64 // sentences moved to a fresh socket because the pooled one was dead
	Fallbacks int64 // sentences sent over HTTP because the pool was at its cap
	Open      int   // sockets open right now
}

// WSStats reports the websocket transport's counters; all zero on an HTTP client.
func (c *Client) WSStats() WSStats {
	if c.ws == nil {
		return WSStats{}
	}
	p := c.ws
	p.mu.Lock()
	open := len(p.conns)
	p.mu.Unlock()
	return WSStats{Dials: p.dials.Load(), Reuses: p.reuses.Load(), Redials: p.redials.Load(), Fallbacks: p.fallbacks.Load(), Open: open}
}

// wsPool owns a client's dialogue sockets. A sentence takes an exclusive lease
// on an idle socket of its [wsKey] or dials one, and the lease returns to the
// pool only once the sentence's turn-final marker was read; a socket abandoned
// before that is closed, never reused, because the audio still queued on it
// would bleed into the next sentence.
type wsPool struct {
	client *Client
	tune   wsTuning

	ctx    context.Context // cancelled by close; bounds background dials
	cancel context.CancelFunc

	mu      sync.Mutex
	closed  bool
	conns   map[*wsConn]struct{} // every open socket, leased or idle
	idle    map[wsKey][]*wsConn  // unleased sockets, most recently used last
	open    map[wsKey]int        // sockets open or being dialled, per key
	dialing map[wsKey]int        // background pre-dials in flight, per key
	total   int                  // sum of open
	recent  []wsKey              // most recently used first, at most wsWarmKeys
	wg      sync.WaitGroup       // socket goroutines and background dials

	dials, reuses, redials, fallbacks atomic.Int64
}

func newWSPool(c *Client) *wsPool {
	ctx, cancel := context.WithCancel(context.Background())
	return &wsPool{
		client:  c,
		tune:    defaultWSTuning(),
		ctx:     ctx,
		cancel:  cancel,
		conns:   map[*wsConn]struct{}{},
		idle:    map[wsKey][]*wsConn{},
		open:    map[wsKey]int{},
		dialing: map[wsKey]int{},
	}
}

// acquire leases a socket for key: an idle one (reused=true) unless fresh is
// set, else a newly dialled one. A nil socket with a nil error means the pool
// has no room (or is closed) and the sentence should go over HTTP.
func (p *wsPool) acquire(ctx context.Context, key wsKey, fresh bool) (c *wsConn, reused bool, err error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, false, nil
	}
	p.touchLocked(key)
	if l := p.idle[key]; !fresh && len(l) > 0 {
		c = l[len(l)-1]
		p.idle[key] = l[:len(l)-1]
		c.leases.Add(1)
		c.leased.Store(true)
		p.mu.Unlock()
		p.reuses.Add(1)
		return c, true, nil
	}
	victim, ok := p.reserveLocked(key, true)
	p.mu.Unlock()
	if victim != nil {
		victim.shutdown(errWSEvicted)
	}
	if !ok {
		return nil, false, nil
	}
	c, err = p.dial(ctx, key, true)
	if err != nil {
		return nil, false, err
	}
	if c == nil { // closed while dialling
		return nil, false, nil
	}
	return c, false, nil
}

// touchLocked moves key to the front of the recently-used list.
func (p *wsPool) touchLocked(key wsKey) {
	out := append(make([]wsKey, 0, wsWarmKeys), key)
	for _, k := range p.recent {
		if k != key && len(out) < wsWarmKeys {
			out = append(out, k)
		}
	}
	p.recent = out
}

// reserveLocked books a slot for a socket about to be dialled for key. At the
// per-key cap it refuses. At the total cap it may (evict) close the longest-idle
// socket of any key to make room, returned for the caller to shut down once the
// lock is released; with nothing idle it refuses.
func (p *wsPool) reserveLocked(key wsKey, evict bool) (victim *wsConn, ok bool) {
	if p.open[key] >= p.tune.perKey {
		return nil, false
	}
	if p.total >= p.tune.total {
		if !evict {
			return nil, false
		}
		for _, l := range p.idle {
			for _, c := range l {
				if victim == nil || c.idleSince.Before(victim.idleSince) {
					victim = c
				}
			}
		}
		if victim == nil {
			return nil, false
		}
		p.dropLocked(victim)
	}
	p.open[key]++
	p.total++
	return victim, true
}

// dropLocked takes c out of the pool's books: the idle list, the open set and
// the slot counts.
func (p *wsPool) dropLocked(c *wsConn) {
	delete(p.conns, c)
	l := p.idle[c.key]
	for i, x := range l {
		if x == c {
			p.idle[c.key] = append(l[:i:i], l[i+1:]...)
			break
		}
	}
	if len(p.idle[c.key]) == 0 {
		delete(p.idle, c.key)
	}
	if c.counted {
		c.counted = false
		p.unreserveLocked(c.key)
	}
}

func (p *wsPool) unreserveLocked(key wsKey) {
	p.total--
	if p.open[key]--; p.open[key] <= 0 {
		delete(p.open, key)
	}
}

// dial opens and registers a socket for key against a slot already reserved,
// and starts its goroutines. leased sets the lease before the socket is
// visible. A nil socket with a nil error means the pool closed meanwhile.
func (p *wsPool) dial(ctx context.Context, key wsKey, leased bool) (*wsConn, error) {
	conn, err := p.client.dialDialogue(ctx, key)
	if err != nil {
		p.mu.Lock()
		p.unreserveLocked(key)
		p.mu.Unlock()
		return nil, err
	}
	p.dials.Add(1)
	c := &wsConn{pool: p, key: key, conn: conn, frames: make(chan wsFrame), done: make(chan struct{}), counted: true}
	c.leased.Store(leased)
	p.mu.Lock()
	if p.closed {
		p.unreserveLocked(key)
		p.mu.Unlock()
		_ = conn.Close()
		return nil, nil
	}
	p.conns[c] = struct{}{}
	if !leased {
		c.idleSince = time.Now()
		p.idle[key] = append(p.idle[key], c)
	}
	p.wg.Add(2)
	p.mu.Unlock()
	go c.readLoop()
	go c.keepLoop()
	return c, nil
}

// release returns a socket whose sentence finished cleanly to the idle list.
func (p *wsPool) release(c *wsConn) {
	p.mu.Lock()
	if _, live := p.conns[c]; !live || p.closed {
		p.mu.Unlock()
		return
	}
	c.leased.Store(false)
	c.idleSince = time.Now()
	p.idle[c.key] = append(p.idle[c.key], c)
	p.mu.Unlock()
}

// prewarm makes sure key has an idle socket: it refreshes the idle clock of the
// ones it has, or dials one in the background when it has none (and none is
// already on its way, and the caps allow). It never closes another voice's
// socket to make room.
func (p *wsPool) prewarm(key wsKey) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	if l := p.idle[key]; len(l) > 0 {
		now := time.Now()
		for _, c := range l {
			c.idleSince = now
		}
		return
	}
	if p.dialing[key] > 0 {
		return
	}
	if _, ok := p.reserveLocked(key, false); !ok {
		return
	}
	p.dialing[key]++
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ctx, cancel := context.WithTimeout(p.ctx, wsDialTimeout)
		defer cancel()
		_, _ = p.dial(ctx, key, false) // advisory: a failed pre-dial just means the sentence dials
		p.mu.Lock()
		if p.dialing[key]--; p.dialing[key] <= 0 {
			delete(p.dialing, key)
		}
		p.mu.Unlock()
	}()
}

// warm pre-dials the most recently used voices and reports whether it knew any.
func (p *wsPool) warm() bool {
	p.mu.Lock()
	keys := append([]wsKey(nil), p.recent...)
	p.mu.Unlock()
	for _, k := range keys {
		p.prewarm(k)
	}
	return len(keys) > 0
}

// close shuts every socket and waits for the pool's goroutines. Afterwards the
// pool leases nothing, so a late sentence goes over HTTP.
func (p *wsPool) close() {
	p.mu.Lock()
	p.closed = true
	conns := make([]*wsConn, 0, len(p.conns))
	for c := range p.conns {
		conns = append(conns, c)
	}
	p.mu.Unlock()
	p.cancel()
	for _, c := range conns {
		c.shutdown(errWSClosed)
	}
	p.wg.Wait()
}

// dialDialogue opens a dialogue socket for key and sends its registration.
func (c *Client) dialDialogue(ctx context.Context, key wsKey) (*websocket.Conn, error) {
	u, err := url.Parse(strings.TrimRight(c.baseURL, "/") + wsDialoguePath)
	if err != nil {
		return nil, fmt.Errorf("dialogue socket URL: %w", err)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	}
	q := url.Values{"model_id": {key.model}, "output_format": {key.format}}
	if key.language != "" {
		q.Set("language_code", key.language)
	}
	if key.seed != "" {
		q.Set("seed", key.seed)
	}
	u.RawQuery = q.Encode()

	dialer := websocket.Dialer{Proxy: http.ProxyFromEnvironment, HandshakeTimeout: wsDialTimeout}
	conn, resp, err := dialer.DialContext(ctx, u.String(), http.Header{"xi-api-key": {c.apiKey}})
	if err != nil {
		// A refused handshake carries an HTTP response: type it like any other
		// non-2xx start so the retry policy reads its status (429/5xx retry, a
		// 400/403 for a model or key the endpoint rejects does not).
		if resp != nil {
			defer resp.Body.Close()
			if errors.Is(err, websocket.ErrBadHandshake) {
				return nil, readErrorResponse(resp, "Synthesize")
			}
		}
		return nil, fmt.Errorf("dial dialogue socket: %w", err)
	}
	_ = conn.SetWriteDeadline(time.Now().Add(wsDialTimeout))
	if err := conn.WriteMessage(websocket.TextMessage, []byte(key.register)); err != nil {
		_ = conn.Close()
		return nil, &wsStartError{err: fmt.Errorf("register voice on dialogue socket: %w", err)}
	}
	return conn, nil
}

// wsFrame is one decoded server message a sentence cares about.
type wsFrame struct {
	pcm   []byte
	final bool
	lease uint64 // the socket's lease count when the frame was read
}

// wsConn is one dialogue socket. Its read loop hands frames to whoever holds the
// lease; its keep loop sends keep_alive and enforces the idle TTL.
type wsConn struct {
	pool *wsPool
	key  wsKey
	conn *websocket.Conn

	wmu sync.Mutex // one writer at a time (gorilla's rule)

	frames chan wsFrame  // read loop -> leaseholder, unbuffered: playback back-pressure reaches the socket
	done   chan struct{} // closed by shutdown
	once   sync.Once
	dead   error // why the socket went away; readable once done is closed

	leased atomic.Bool
	// leases counts the sentences this socket was handed to. A frame is stamped
	// with it on arrival, so one read under an earlier lease (audio after that
	// sentence's final marker) is recognised and never passed off as the next
	// sentence's.
	leases atomic.Uint64

	// Guarded by pool.mu.
	idleSince time.Time
	counted   bool // holds a slot in the pool's counts
}

// shutdown closes the socket (idempotent) and takes it out of the pool. It is
// the abort for a sentence in flight: closing is the only cancel the protocol has.
func (c *wsConn) shutdown(reason error) {
	c.once.Do(func() {
		c.dead = reason
		close(c.done)
		_ = c.conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(100*time.Millisecond))
		_ = c.conn.Close()
		c.pool.mu.Lock()
		c.pool.dropLocked(c)
		c.pool.mu.Unlock()
	})
}

func (c *wsConn) write(v any) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(wsDialTimeout))
	return c.conn.WriteJSON(v)
}

type wsInput struct {
	Text    string `json:"text"`
	VoiceID string `json:"voice_id"`
	NewTurn bool   `json:"new_turn"`
}

type wsSentence struct {
	Inputs []wsInput `json:"inputs"`
	Flush  bool      `json:"flush"`
}

// say submits one sentence. new_turn is always set: every call is a sentence on
// its own, as on HTTP, and a look-ahead pair alternates sentences between two
// sockets, so a socket never sees a reply's sentences in a row anyway.
func (c *wsConn) say(sentence string) error {
	return c.write(wsSentence{
		Inputs: []wsInput{{Text: sentence + " ", VoiceID: c.key.voiceID, NewTurn: true}},
		Flush:  true,
	})
}

// next waits for the leaseholder's next frame. timeout may be nil (no bound).
func (c *wsConn) next(ctx context.Context, timeout <-chan time.Time) (wsFrame, error) {
	select {
	case f := <-c.frames:
		if f.lease != c.leases.Load() {
			return wsFrame{}, errWSStray
		}
		return f, nil
	case <-c.done:
		return wsFrame{}, c.dead
	case <-ctx.Done():
		return wsFrame{}, ctx.Err()
	case <-timeout:
		return wsFrame{}, errWSFirstAudioTimeout
	}
}

func (c *wsConn) readLoop() {
	defer c.pool.wg.Done()
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			c.shutdown(fmt.Errorf("dialogue socket read: %w", err))
			return
		}
		var m struct {
			Audio   string `json:"audio"`
			Final   bool   `json:"is_final_audio_for_turn"`
			Error   string `json:"error"`
			Message string `json:"message"`
			Code    int    `json:"code"`
		}
		if err := json.Unmarshal(data, &m); err != nil {
			c.shutdown(fmt.Errorf("dialogue socket: undecodable frame: %w", err))
			return
		}
		f := wsFrame{lease: c.leases.Load()}
		switch {
		case m.Error != "":
			c.shutdown(&StreamError{Code: m.Code, Kind: m.Error, Message: m.Message})
			return
		case m.Audio != "":
			pcm, err := base64.StdEncoding.DecodeString(m.Audio)
			if err != nil {
				c.shutdown(fmt.Errorf("dialogue socket: audio frame is not base64: %w", err))
				return
			}
			f.pcm = pcm
		case m.Final:
			f.final = true
		default:
			continue // unknown keys are tolerated
		}
		if !c.leased.Load() {
			// Audio with no sentence outstanding: the socket's state is not what
			// the pool believes, so it must not serve another sentence.
			c.shutdown(errWSStray)
			return
		}
		select {
		case c.frames <- f:
		case <-c.done:
			return
		}
	}
}

func (c *wsConn) keepLoop() {
	defer c.pool.wg.Done()
	t := time.NewTicker(c.pool.tune.keepAlive)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
		}
		c.pool.mu.Lock()
		expired := !c.leased.Load() && time.Since(c.idleSince) >= c.pool.tune.idleTTL
		if expired {
			c.pool.dropLocked(c) // out of the idle list before the lock drops: no lease can take it now
		}
		c.pool.mu.Unlock()
		if expired {
			c.shutdown(errWSIdle)
			return
		}
		if err := c.write(struct {
			KeepAlive bool `json:"keep_alive"`
		}{true}); err != nil {
			c.shutdown(fmt.Errorf("dialogue socket keep-alive: %w", err))
			return
		}
	}
}

// errWSNoRoom tells [Client.Synthesize] the pool could not lease a socket (at
// its cap, or closed) and the sentence should go over HTTP.
var errWSNoRoom = errors.New("elevenlabs: no dialogue socket available")

// synthesizeWS renders one sentence over a pooled dialogue socket. Like the HTTP
// path it returns once the sentence has STARTED — the first frame arrived — so a
// failure before any audio is a returned (retryable) start error, never a
// terminal Err chunk on an empty stream. A pooled socket that turns out dead
// before its first frame is replaced by a fresh dial once, transparently.
func (c *Client) synthesizeWS(ctx context.Context, sentence string, key wsKey, sampleRate int) (<-chan tts.AudioChunk, error) {
	p := c.ws
	for attempt := 0; ; attempt++ {
		conn, reused, err := p.acquire(ctx, key, attempt > 0)
		if err != nil {
			return nil, fmt.Errorf("elevenlabs.Synthesize: %w", err)
		}
		if conn == nil {
			p.fallbacks.Add(1)
			return nil, errWSNoRoom
		}
		var first wsFrame
		err = conn.say(sentence)
		if err == nil {
			timer := time.NewTimer(p.tune.firstAudio)
			first, err = conn.next(ctx, timer.C)
			timer.Stop()
		}
		if err == nil {
			ch := make(chan tts.AudioChunk)
			go conn.stream(ctx, first, ch, sampleRate)
			return ch, nil
		}
		conn.shutdown(err)
		if ctx.Err() != nil {
			// Cut before the first audio: the socket is gone with its unread
			// sentence, so line up a replacement for the next one.
			p.prewarm(key)
			return nil, fmt.Errorf("elevenlabs.Synthesize: %w", ctx.Err())
		}
		if reused && attempt == 0 {
			p.redials.Add(1)
			continue
		}
		var se *StreamError
		if errors.As(err, &se) {
			return nil, fmt.Errorf("elevenlabs.Synthesize: %w", err)
		}
		return nil, &wsStartError{
			err:     fmt.Errorf("elevenlabs.Synthesize: %w", err),
			timeout: errors.Is(err, errWSFirstAudioTimeout),
		}
	}
}

// stream forwards a started sentence's frames to ch until its turn-final marker
// and closes ch. The lease goes back to the pool before the close, so a caller
// that saw the close finds the socket idle. A ctx cancel (barge) closes the
// socket with its unread audio and closes ch with no Err chunk; a socket that
// dies mid-sentence under a live ctx ends in a terminal Err chunk (#436).
func (c *wsConn) stream(ctx context.Context, f wsFrame, ch chan<- tts.AudioChunk, sampleRate int) {
	defer close(ch)
	var carry []byte // low byte of a sample split across two frames
	for {
		if f.final {
			c.pool.release(c)
			return
		}
		pcm := f.pcm
		if len(carry) > 0 {
			pcm = append(carry, pcm...)
			carry = nil
		}
		if len(pcm)%2 == 1 {
			carry = []byte{pcm[len(pcm)-1]}
			pcm = pcm[:len(pcm)-1]
		}
		if len(pcm) > 0 {
			select {
			case ch <- tts.AudioChunk{PCM: pcm, SampleRate: sampleRate, Channels: 1}:
			case <-ctx.Done():
				c.abandon(ctx)
				return
			}
		}
		var err error
		if f, err = c.next(ctx, nil); err != nil {
			if ctx.Err() != nil {
				c.abandon(ctx)
				return
			}
			c.shutdown(err)
			select {
			case ch <- tts.AudioChunk{Err: fmt.Errorf("elevenlabs.Synthesize: %w", err)}:
			case <-ctx.Done():
			}
			return
		}
	}
}

// abandon is a cut sentence's teardown: close its socket now and pre-dial the
// replacement, so the reply after a barge starts on a warm socket.
func (c *wsConn) abandon(ctx context.Context) {
	c.shutdown(ctx.Err())
	c.pool.prewarm(c.key)
}

// Close releases what the client holds: every dialogue socket of the websocket
// transport and the HTTP pool's idle connections. It waits for the sockets'
// goroutines. A sentence still streaming ends as a mid-stream failure (or
// silently, when its ctx is already cancelled). Safe to call more than once and
// on an HTTP-only client; a closed client still synthesizes, over HTTP.
func (c *Client) Close() error {
	if c.ws != nil {
		c.ws.close()
	}
	c.http.CloseIdleConnections()
	return nil
}

var _ io.Closer = (*Client)(nil)
