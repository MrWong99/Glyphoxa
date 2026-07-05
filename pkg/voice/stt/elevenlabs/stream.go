package elevenlabs

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
)

// Compile-time assertion: the ElevenLabs [Client] is a streaming recognizer in
// addition to the batch [stt.Recognizer] it already implements. The batch
// files (elevenlabs.go, transcribe.go) are untouched — same key, base URL, and
// ProviderID cover both surfaces (ADR-0004).
var _ stt.StreamingRecognizer = (*Client)(nil)

const (
	// StreamModel is the ElevenLabs Scribe v2 Realtime model — the websocket
	// sibling of the batch [Model] ("scribe_v2"). Only a live run can prove
	// this identifier against the provider.
	StreamModel = "scribe_v2_realtime"

	// AudioFormatPCM16000 is the realtime `audio_format` value for raw
	// little-endian signed-16-bit PCM at 16 kHz mono — the format [audio.Frame]
	// already carries, so no re-encoding is needed.
	AudioFormatPCM16000 = "pcm_16000"

	// streamPath is the realtime websocket endpoint path.
	streamPath = "/v1/speech-to-text/realtime"

	// supportedSampleRate is the only PCM rate the v1 realtime adapter accepts;
	// it matches audio_format=pcm_16000.
	supportedSampleRate = 16000

	// minChunkMs is the minimum audio duration aggregated into one
	// input_audio_chunk. ElevenLabs recommends 0.1–1s chunks; sending smaller
	// buffers adds websocket framing overhead for no benefit. Send flushes the
	// aggregation buffer the moment it crosses this threshold (no timers).
	minChunkMs = 100

	// writeQueueLen bounds the write pump's inbound queue. Send/Commit are
	// non-blocking: once the queue is full they surface a *stt.StreamError
	// instead of blocking the audio pump.
	writeQueueLen = 64

	// defaultPingInterval is how often the write pump sends a keepalive ping.
	defaultPingInterval = 20 * time.Second

	// writeWait bounds a single websocket write (frame or control).
	writeWait = 10 * time.Second

	// pongWait is the read deadline; it is refreshed on every message and pong.
	// Generously larger than defaultPingInterval so a healthy peer never trips it.
	pongWait = 60 * time.Second

	// msgInputAudioChunk is the sole client->server message type.
	msgInputAudioChunk = "input_audio_chunk"
)

// server->client message types.
const (
	msgSessionStarted    = "session_started"
	msgPartialTranscript = "partial_transcript"
	msgCommitted         = "committed_transcript"
	msgInsufficientAudio = "insufficient_audio_activity"
)

// codeSampleRate is the [stt.StreamError.Code] for a frame whose sample rate
// does not match the session's declared rate — a caller (not transport) error.
const codeSampleRate = "sample_rate_mismatch"

// recoverableErrorCodes are provider error-frame types after which the session
// stays usable: the failing operation is reported but Send/Commit continue to
// work. Every other error frame is treated as fatal (session dead).
var recoverableErrorCodes = map[string]bool{
	"commit_throttled":    true,
	"rate_limited":        true,
	"chunk_size_exceeded": true,
	"input_error":         true,
}

// OpenStream implements [stt.StreamingRecognizer]. It dials a fresh Scribe v2
// Realtime session honoring ADR-0042: manual commit strategy, sample rate
// declared, key drawn from the same ElevenLabs Provider Config as the batch
// adapter.
func (c *Client) OpenStream(ctx context.Context, cfg stt.StreamConfig) (stt.Stream, error) {
	return c.openStream(ctx, cfg, defaultPingInterval)
}

// openStream is OpenStream with the keepalive interval injected, so internal
// tests can shorten it without exporting a knob.
func (c *Client) openStream(ctx context.Context, cfg stt.StreamConfig, pingInterval time.Duration) (stt.Stream, error) {
	if cfg.SampleRate != supportedSampleRate {
		return nil, fmt.Errorf("elevenlabs.OpenStream: unsupported sample rate %d (only %d supported)", cfg.SampleRate, supportedSampleRate)
	}
	if c.apiKey == "" {
		return nil, fmt.Errorf("elevenlabs.OpenStream: missing API key (set %s or pass it to New)", APIKeyEnv)
	}

	u, err := streamURL(c.baseURL, cfg.Language)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs.OpenStream: %w", err)
	}

	hdr := http.Header{}
	hdr.Set("xi-api-key", c.apiKey)

	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, u, hdr)
	if err != nil {
		return nil, &stt.StreamError{Code: stt.CodeTransport, Fatal: true, Err: dialError(err, resp)}
	}

	s := &stream{
		conn:      conn,
		cfg:       cfg,
		writeCh:   make(chan wsWrite, writeQueueLen),
		stopCh:    make(chan struct{}),
		threshold: supportedSampleRate * minChunkMs / 1000 * 2, // bytes of s16le
	}
	s.wg.Add(3)
	go s.readPump()
	go s.writePump(pingInterval)
	go s.ctxWatch(ctx)
	return s, nil
}

// streamURL derives the realtime websocket URL from a batch base URL, mapping
// https->wss and http->ws so WithBaseURL(httptest.URL) works verbatim.
func streamURL(baseURL, language string) (string, error) {
	pu, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return "", fmt.Errorf("parse base URL %q: %w", baseURL, err)
	}
	switch pu.Scheme {
	case "https", "wss":
		pu.Scheme = "wss"
	case "http", "ws":
		pu.Scheme = "ws"
	default:
		return "", fmt.Errorf("unsupported base URL scheme %q", pu.Scheme)
	}
	pu.Path = streamPath
	q := url.Values{}
	q.Set("model_id", StreamModel)
	q.Set("audio_format", AudioFormatPCM16000)
	q.Set("commit_strategy", "manual")
	if language != "" {
		q.Set("language_code", language)
	}
	pu.RawQuery = q.Encode()
	return pu.String(), nil
}

func dialError(err error, resp *http.Response) error {
	if resp != nil {
		return fmt.Errorf("websocket dial: HTTP %d: %w", resp.StatusCode, err)
	}
	return fmt.Errorf("websocket dial: %w", err)
}

// wsWrite is one queued client->server write: either an audio chunk (commit
// false) or a manual commit sentinel (commit true, empty audio).
type wsWrite struct {
	audioB64 string
	commit   bool
}

func (w wsWrite) marshal(sampleRate int) ([]byte, error) {
	return json.Marshal(struct {
		MessageType string `json:"message_type"`
		AudioBase64 string `json:"audio_base_64"`
		Commit      bool   `json:"commit"`
		SampleRate  int    `json:"sample_rate"`
	}{
		MessageType: msgInputAudioChunk,
		AudioBase64: w.audioB64,
		Commit:      w.commit,
		SampleRate:  sampleRate,
	})
}

// stream is a live realtime session. One write pump owns every websocket write
// (gorilla's single-writer rule); one read pump routes server frames.
type stream struct {
	conn      *websocket.Conn
	cfg       stt.StreamConfig
	writeCh   chan wsWrite
	stopCh    chan struct{}
	threshold int // aggregation flush threshold in bytes
	wg        sync.WaitGroup

	closeOnce sync.Once
	deadErr   atomic.Pointer[stt.StreamError]

	// aggMu guards the Send/Commit audio aggregation buffer.
	aggMu sync.Mutex
	agg   []byte

	// mu guards the FIFO pending-commit queue, the auto-commit carryover text,
	// and the pendingClosed latch.
	mu            sync.Mutex
	pending       []chan stt.CommitResult
	autoText      string
	pendingClosed bool
}

// Send implements [stt.Stream].
func (s *stream) Send(frame audio.Frame) error {
	if de := s.deadErr.Load(); de != nil {
		return de
	}
	if frame.SampleRate() != s.cfg.SampleRate {
		return &stt.StreamError{
			Code:  codeSampleRate,
			Fatal: false,
			Err:   fmt.Errorf("frame sample rate %d != stream sample rate %d", frame.SampleRate(), s.cfg.SampleRate),
		}
	}

	s.aggMu.Lock()
	appendPCM16LE(&s.agg, frame.Samples())
	var chunk []byte
	if len(s.agg) >= s.threshold {
		chunk = s.agg
		s.agg = nil
	}
	s.aggMu.Unlock()

	if chunk == nil {
		return nil
	}
	return s.enqueue(wsWrite{audioB64: base64.StdEncoding.EncodeToString(chunk)})
}

// Commit implements [stt.Stream].
func (s *stream) Commit() (<-chan stt.CommitResult, error) {
	if de := s.deadErr.Load(); de != nil {
		return nil, de
	}

	s.aggMu.Lock()
	rem := s.agg
	s.agg = nil
	s.aggMu.Unlock()

	ch := make(chan stt.CommitResult, 1)
	s.mu.Lock()
	if s.pendingClosed {
		de := s.deadErr.Load()
		s.mu.Unlock()
		return nil, de
	}
	s.pending = append(s.pending, ch)
	s.mu.Unlock()

	if len(rem) > 0 {
		if err := s.enqueue(wsWrite{audioB64: base64.StdEncoding.EncodeToString(rem)}); err != nil {
			s.failCommit(ch, err)
			return ch, nil
		}
	}
	if err := s.enqueue(wsWrite{commit: true}); err != nil {
		s.failCommit(ch, err)
		return ch, nil
	}
	return ch, nil
}

// Close implements [stt.Stream].
func (s *stream) Close() error {
	s.shutdown(&stt.StreamError{Code: stt.CodeTransport, Fatal: true, Err: errStreamClosed})
	s.wg.Wait()
	return nil
}

var errStreamClosed = fmt.Errorf("stt stream closed")

// enqueue offers a write to the write pump without blocking. It returns the
// session's death error once dead, or a recoverable queue-full error.
func (s *stream) enqueue(w wsWrite) error {
	select {
	case s.writeCh <- w:
		return nil
	default:
		if de := s.deadErr.Load(); de != nil {
			return de
		}
		return &stt.StreamError{Code: stt.CodeTransport, Fatal: false, Err: fmt.Errorf("write queue full")}
	}
}

// shutdown tears the session down exactly once: it records the death cause,
// signals the pumps, and closes the connection to unblock the read pump.
func (s *stream) shutdown(cause *stt.StreamError) {
	s.closeOnce.Do(func() {
		s.deadErr.Store(cause)
		close(s.stopCh)
		_ = s.conn.Close()
	})
}

// ctxWatch mirrors context cancellation onto session teardown (cancel == Close).
func (s *stream) ctxWatch(ctx context.Context) {
	defer s.wg.Done()
	select {
	case <-ctx.Done():
		s.shutdown(&stt.StreamError{Code: stt.CodeTransport, Fatal: true, Err: ctx.Err()})
	case <-s.stopCh:
	}
}

// writePump owns every websocket write: audio chunks, commit sentinels, and
// keepalive pings.
func (s *stream) writePump(pingInterval time.Duration) {
	defer s.wg.Done()
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case w := <-s.writeCh:
			data, err := w.marshal(s.cfg.SampleRate)
			if err != nil {
				s.shutdown(transportErr(err))
				return
			}
			_ = s.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := s.conn.WriteMessage(websocket.TextMessage, data); err != nil {
				s.shutdown(transportErr(err))
				return
			}
		case <-ping.C:
			if err := s.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait)); err != nil {
				s.shutdown(transportErr(err))
				return
			}
		}
	}
}

// readPump routes server frames until the connection fails or is closed, then
// resolves any still-pending commits.
func (s *stream) readPump() {
	defer s.wg.Done()
	defer s.drainPending()

	_ = s.conn.SetReadDeadline(time.Now().Add(pongWait))
	s.conn.SetPongHandler(func(string) error {
		return s.conn.SetReadDeadline(time.Now().Add(pongWait))
	})

	for {
		_, data, err := s.conn.ReadMessage()
		if err != nil {
			s.shutdown(transportErr(err))
			return
		}
		_ = s.conn.SetReadDeadline(time.Now().Add(pongWait))

		var msg struct {
			MessageType string `json:"message_type"`
			Text        string `json:"text"`
			Error       string `json:"error"`
		}
		if err := json.Unmarshal(data, &msg); err != nil {
			continue // ignore malformed frames
		}

		switch msg.MessageType {
		case msgSessionStarted:
			// nothing to do; the session is live once the read pump runs.
		case msgPartialTranscript:
			if s.cfg.OnPartial != nil {
				s.cfg.OnPartial(msg.Text)
			}
		case msgCommitted:
			s.resolveCommitted(msg.Text)
		case msgInsufficientAudio:
			// Empty utterance: resolve like the batch adapter's empty text.
			s.resolveEmpty()
		default:
			se := classifyError(msg.MessageType, msg.Error)
			if se.Fatal {
				s.shutdown(se)
				return
			}
			s.resolveFront(stt.CommitResult{Err: se})
		}
	}
}

// resolveCommitted resolves the front pending commit with text, prepending any
// carried-over auto-commit text. With no pending commit the text is an
// unsolicited 90s auto-commit and is carried onto the next commit.
func (s *stream) resolveCommitted(text string) {
	s.mu.Lock()
	if len(s.pending) == 0 {
		s.autoText = spaceJoin(s.autoText, text)
		s.mu.Unlock()
		return
	}
	ch := s.pending[0]
	s.pending = s.pending[1:]
	full := spaceJoin(s.autoText, text)
	s.autoText = ""
	s.mu.Unlock()
	ch <- stt.CommitResult{Transcript: stt.Transcript{Text: full}}
}

// resolveEmpty resolves the front pending commit with empty (or carried-over)
// text and no error.
func (s *stream) resolveEmpty() {
	s.mu.Lock()
	if len(s.pending) == 0 {
		s.mu.Unlock()
		return
	}
	ch := s.pending[0]
	s.pending = s.pending[1:]
	full := s.autoText
	s.autoText = ""
	s.mu.Unlock()
	ch <- stt.CommitResult{Transcript: stt.Transcript{Text: full}}
}

// resolveFront resolves the front pending commit with res (a recoverable error).
func (s *stream) resolveFront(res stt.CommitResult) {
	s.mu.Lock()
	if len(s.pending) == 0 {
		s.mu.Unlock()
		return
	}
	ch := s.pending[0]
	s.pending = s.pending[1:]
	s.mu.Unlock()
	ch <- res
}

// drainPending resolves every still-pending commit with the death cause and
// latches the queue closed so no later Commit can register an orphan.
func (s *stream) drainPending() {
	de := s.deadErr.Load()
	if de == nil {
		de = transportErr(errStreamClosed)
	}
	s.mu.Lock()
	s.pendingClosed = true
	pend := s.pending
	s.pending = nil
	s.mu.Unlock()
	for _, ch := range pend {
		ch <- stt.CommitResult{Err: de}
	}
}

// failCommit resolves ch with err, but only if it is still pending — the read
// pump's drainPending may already have resolved it, and a channel resolves
// exactly once.
func (s *stream) failCommit(ch chan stt.CommitResult, err error) {
	s.mu.Lock()
	removed := false
	for i, c := range s.pending {
		if c == ch {
			s.pending = append(s.pending[:i], s.pending[i+1:]...)
			removed = true
			break
		}
	}
	s.mu.Unlock()
	if removed {
		ch <- stt.CommitResult{Err: err}
	}
}

func classifyError(code, detail string) *stt.StreamError {
	return &stt.StreamError{
		Code:  code,
		Fatal: !recoverableErrorCodes[code],
		Err:   fmt.Errorf("provider error %q: %s", code, detail),
	}
}

func transportErr(err error) *stt.StreamError {
	return &stt.StreamError{Code: stt.CodeTransport, Fatal: true, Err: err}
}

// appendPCM16LE appends the little-endian int16 byte encoding of samples to *b.
func appendPCM16LE(b *[]byte, samples []int16) {
	var buf [2]byte
	for _, s := range samples {
		binary.LittleEndian.PutUint16(buf[:], uint16(s))
		*b = append(*b, buf[:]...)
	}
}

func spaceJoin(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + " " + b
	}
}
