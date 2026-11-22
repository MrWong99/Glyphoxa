package elevenlabs_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/goleak"

	"github.com/MrWong99/Glyphoxa/pkg/voice/providererr"
	"github.com/MrWong99/Glyphoxa/pkg/voice/retry"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
)

// --- scripted Text-to-Dialogue websocket server (pattern: the STT adapter's
// newScriptedServer) ---
//
// Scripts run on the server's per-connection goroutine and MUST NOT call
// t.Fatal*; they do I/O only and every assertion runs on the test goroutine.

const (
	ttdPath    = "/v1/text-to-dialogue/stream-input"
	wsRecvWait = 2 * time.Second
)

// ttdDial is what one websocket handshake carried.
type ttdDial struct {
	path   string
	query  map[string][]string
	apiKey string
}

type ttdServer struct {
	*httptest.Server
	upgrades  atomic.Int64 // websocket handshakes accepted
	open      atomic.Int64 // websocket connections currently open
	httpPosts atomic.Int64 // HTTP /stream syntheses served
	heads     atomic.Int64 // HTTP HEAD warms served
	keepAlive atomic.Int64 // keep_alive messages read, all sockets
	refuse    atomic.Int64 // when non-zero, the HTTP status every handshake is refused with
	refused   atomic.Int64 // handshakes refused

	mu         sync.Mutex
	dials      []ttdDial
	httpBodies [][]byte
	httpURLs   []string
}

// httpPCM is the audio the server's HTTP /stream path answers with, so a test
// can tell which transport served a sentence.
var httpPCM = []byte("HTTP-PCM")

func newTTDServer(t *testing.T, script func(c *ttdConn)) *ttdServer {
	t.Helper()
	s := &ttdServer{}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != ttdPath {
			if r.Method == http.MethodPost {
				body, _ := io.ReadAll(r.Body)
				s.mu.Lock()
				s.httpBodies = append(s.httpBodies, body)
				s.httpURLs = append(s.httpURLs, r.URL.String())
				s.mu.Unlock()
				s.httpPosts.Add(1)
				_, _ = w.Write(httpPCM)
			}
			if r.Method == http.MethodHead {
				s.heads.Add(1)
			}
			return
		}
		if status := s.refuse.Load(); status != 0 {
			s.refused.Add(1)
			http.Error(w, `{"detail":{"status":"too_many_concurrent_requests"}}`, int(status))
			return
		}
		s.mu.Lock()
		s.dials = append(s.dials, ttdDial{path: r.URL.Path, query: r.URL.Query(), apiKey: r.Header.Get("xi-api-key")})
		s.mu.Unlock()
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		s.upgrades.Add(1)
		s.open.Add(1)
		defer s.open.Add(-1)
		defer conn.Close()
		script(&ttdConn{Conn: conn, srv: s, n: s.upgrades.Load()})
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *ttdServer) dial(i int) ttdDial {
	s.mu.Lock()
	defer s.mu.Unlock()
	if i >= len(s.dials) {
		return ttdDial{}
	}
	return s.dials[i]
}

// ttdConn is the server side of one dialogue socket.
type ttdConn struct {
	*websocket.Conn
	srv *ttdServer
	n   int64 // 1-based ordinal of this connection
}

// ttdMsg is a decoded client message.
type ttdMsg struct {
	Voices        []string        `json:"voices"`
	VoiceSettings json.RawMessage `json:"voice_settings"`
	Inputs        []struct {
		Text    string `json:"text"`
		VoiceID string `json:"voice_id"`
		NewTurn bool   `json:"new_turn"`
	} `json:"inputs"`
	Flush     bool `json:"flush"`
	KeepAlive bool `json:"keep_alive"`
	raw       []byte
}

// read returns the next client message; ok is false once the connection is gone.
func (c *ttdConn) read() (ttdMsg, bool) {
	_, data, err := c.ReadMessage()
	if err != nil {
		return ttdMsg{}, false
	}
	var m ttdMsg
	if err := json.Unmarshal(data, &m); err != nil {
		return ttdMsg{}, false
	}
	m.raw = data
	return m, true
}

// sentence returns the next sentence message, counting (and skipping) keep-alives.
func (c *ttdConn) sentence() (ttdMsg, bool) {
	for {
		m, ok := c.read()
		if !ok {
			return ttdMsg{}, false
		}
		if m.KeepAlive {
			c.srv.keepAlive.Add(1)
			continue
		}
		return m, true
	}
}

func (c *ttdConn) audio(pcm []byte) {
	_ = c.WriteJSON(map[string]any{"audio": base64.StdEncoding.EncodeToString(pcm)})
}

func (c *ttdConn) final() { _ = c.WriteJSON(map[string]any{"is_final_audio_for_turn": true}) }

// fail sends a dialogue error frame and closes with 1008, as the real API does.
func (c *ttdConn) fail(code, msg string) {
	_ = c.WriteJSON(map[string]any{"message": msg, "error": code, "code": 1008})
	_ = c.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, code), time.Now().Add(time.Second))
}

// sentencePCM is the audio the echo script renders for a sentence: its text
// (space-padded to whole int16 samples), so a test can prove a call got its own
// audio and nobody else's.
func sentencePCM(text string) []byte {
	b := []byte(text)
	if len(b)%2 == 1 {
		b = append(b, ' ')
	}
	return b
}

// echoScript registers, then answers every sentence with its sentencePCM split
// into two audio frames followed by the turn-final marker.
func echoScript(c *ttdConn) {
	if _, ok := c.read(); !ok { // voices registration
		return
	}
	for {
		m, ok := c.sentence()
		if !ok {
			return
		}
		pcm := sentencePCM(m.Inputs[0].Text)
		half := (len(pcm) / 2) &^ 1
		c.audio(pcm[:half])
		c.audio(pcm[half:])
		c.final()
	}
}

func wsClient(t *testing.T, srv *ttdServer, opts ...elevenlabs.Option) *elevenlabs.Client {
	t.Helper()
	all := append([]elevenlabs.Option{elevenlabs.WithBaseURL(srv.URL), elevenlabs.WithTransport(elevenlabs.TransportWS)}, opts...)
	c := elevenlabs.New("k", all...)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func wsVoice(id string) tts.Voice { return elevenlabs.DefaultVoice(id, "de") }

// wsVoiceWith is wsVoice with its Settings edited.
func wsVoiceWith(t *testing.T, id string, edit func(*elevenlabs.Settings)) tts.Voice {
	t.Helper()
	v := wsVoice(id)
	var s elevenlabs.Settings
	if err := json.Unmarshal(v.Settings, &s); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	edit(&s)
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	v.Settings = raw
	return v
}

// collect drains a synthesis, returning its audio and terminal error (if any).
func collect(t *testing.T, ch <-chan tts.AudioChunk) (pcm []byte, chunks int, terminal error) {
	t.Helper()
	deadline := time.After(wsRecvWait)
	for {
		select {
		case chunk, ok := <-ch:
			if !ok {
				return pcm, chunks, terminal
			}
			if chunk.Err != nil {
				terminal = chunk.Err
				continue
			}
			chunks++
			if chunk.SampleRate != 48000 || chunk.Channels != 1 {
				t.Errorf("chunk format = %d Hz / %d ch, want 48000 / 1", chunk.SampleRate, chunk.Channels)
			}
			pcm = append(pcm, chunk.PCM...)
		case <-deadline:
			t.Fatalf("synthesis channel did not close within %v", wsRecvWait)
		}
	}
}

// drain reads a synthesis to its close and fails the test on a terminal error:
// for sentences whose audio the test does not look at.
func drain(t *testing.T, ch <-chan tts.AudioChunk) {
	t.Helper()
	if _, _, terminal := collect(t, ch); terminal != nil {
		t.Errorf("sentence ended in a terminal error: %v", terminal)
	}
}

func say(t *testing.T, c *elevenlabs.Client, ctx context.Context, voice tts.Voice, sentence string) <-chan tts.AudioChunk {
	t.Helper()
	ch, err := c.Synthesize(ctx, tts.SynthesizeRequest{Sentence: sentence, Voice: voice})
	if err != nil {
		t.Fatalf("Synthesize(%q): %v", sentence, err)
	}
	return ch
}

// eventually polls cond until it holds or wsRecvWait passes.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(wsRecvWait)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestWS_SentenceStreamsAudioAndClosesOnFinal is the tracer bullet for the
// opt-in websocket transport (#702): with TransportWS a v4 Turbo sentence goes
// over the Text-to-Dialogue socket — handshake query + key header, a voices
// registration, one flushed input — and the audio frames come back as PCM chunks
// on a channel that closes at the turn-final marker.
func TestWS_SentenceStreamsAudioAndClosesOnFinal(t *testing.T) {
	type seen struct{ reg, sentence ttdMsg }
	got := make(chan seen, 1)
	srv := newTTDServer(t, func(c *ttdConn) {
		reg, ok := c.read()
		if !ok {
			return
		}
		m, ok := c.sentence()
		if !ok {
			return
		}
		got <- seen{reg, m}
		c.audio([]byte{1, 2, 3, 4})
		c.audio([]byte{5, 6})
		c.final()
		c.sentence() // hold the socket open until the client closes it
	})
	c := wsClient(t, srv)

	// language_code comes from the voice's Settings, exactly as on HTTP.
	voice := wsVoiceWith(t, "voice-1", func(s *elevenlabs.Settings) { s.LanguageCode = "de" })
	pcm, chunks, terminal := collect(t, say(t, c, context.Background(), voice, "Willkommen, Reisender."))
	if terminal != nil {
		t.Fatalf("terminal error: %v", terminal)
	}
	if chunks != 2 || !bytes.Equal(pcm, []byte{1, 2, 3, 4, 5, 6}) {
		t.Errorf("audio = %v in %d chunks, want [1 2 3 4 5 6] in 2", pcm, chunks)
	}
	if n := srv.httpPosts.Load(); n != 0 {
		t.Errorf("HTTP syntheses = %d, want 0 (the sentence must ride the socket)", n)
	}

	d := srv.dial(0)
	if d.path != ttdPath || d.apiKey != "k" {
		t.Errorf("handshake path/key = %q / %q", d.path, d.apiKey)
	}
	for key, want := range map[string]string{"model_id": elevenlabs.ModelV4Turbo, "output_format": "pcm_48000", "language_code": "de"} {
		if v := d.query[key]; len(v) != 1 || v[0] != want {
			t.Errorf("handshake query %s = %v, want %q", key, v, want)
		}
	}

	s := <-got
	if len(s.reg.Voices) != 1 || s.reg.Voices[0] != "voice-1" {
		t.Errorf("registration voices = %v, want [voice-1]", s.reg.Voices)
	}
	var vs elevenlabs.VoiceSettings
	if err := json.Unmarshal(s.reg.VoiceSettings, &vs); err != nil || vs.Stability == nil || *vs.Stability != 0.5 {
		t.Errorf("registration voice_settings = %s (err %v), want the voice's stability 0.5", s.reg.VoiceSettings, err)
	}
	if len(s.sentence.Inputs) != 1 {
		t.Fatalf("sentence inputs = %d, want 1", len(s.sentence.Inputs))
	}
	in := s.sentence.Inputs[0]
	// Inputs are concatenated raw server-side, hence the trailing space; without
	// flush the server buffers ~40 chars and a short line never renders.
	if in.Text != "Willkommen, Reisender. " || in.VoiceID != "voice-1" || !in.NewTurn || !s.sentence.Flush {
		t.Errorf("sentence message = %s", s.sentence.raw)
	}
}

// TestWS_SequentialSentencesReuseOneSocket: a finished sentence returns its
// socket to the pool, so the next sentence of the same voice rides it warm
// (~100 ms first audio) instead of dialling (~210 ms).
func TestWS_SequentialSentencesReuseOneSocket(t *testing.T) {
	srv := newTTDServer(t, echoScript)
	c := wsClient(t, srv)
	voice := wsVoice("voice-1")
	for _, sentence := range []string{"Erster Satz.", "Zweiter Satz.", "Dritter Satz."} {
		pcm, _, terminal := collect(t, say(t, c, context.Background(), voice, sentence))
		if terminal != nil || !bytes.Equal(pcm, sentencePCM(sentence+" ")) {
			t.Errorf("%q: audio %q, terminal %v", sentence, pcm, terminal)
		}
	}
	if n := srv.upgrades.Load(); n != 1 {
		t.Errorf("sockets dialled = %d, want 1 for three sequential sentences", n)
	}
	if st := c.WSStats(); st.Dials != 1 || st.Reuses != 2 || st.Open != 1 {
		t.Errorf("stats = %+v, want 1 dial, 2 reuses, 1 open", st)
	}
}

// TestWS_LookAheadPairUsesTwoSocketsAndKeepsAudioApart: the pipelined
// look-ahead (#626) starts sentence k+1 while k is still being read under
// playback back-pressure. A dialogue socket is strictly sequential, so the pair
// needs two, and each call must get its own sentence's audio only.
func TestWS_LookAheadPairUsesTwoSocketsAndKeepsAudioApart(t *testing.T) {
	srv := newTTDServer(t, echoScript)
	c := wsClient(t, srv)
	voice := wsVoice("voice-1")

	chK := say(t, c, context.Background(), voice, "Satz k, noch nicht gelesen.")
	chNext := say(t, c, context.Background(), voice, "Satz k plus eins.") // k is unread: its socket is busy
	if n := srv.upgrades.Load(); n != 2 {
		t.Fatalf("sockets dialled = %d, want 2 for an overlapping pair", n)
	}
	pcmNext, _, errNext := collect(t, chNext)
	pcmK, _, errK := collect(t, chK)
	if errK != nil || errNext != nil {
		t.Fatalf("terminal errors: %v / %v", errK, errNext)
	}
	if !bytes.Equal(pcmK, sentencePCM("Satz k, noch nicht gelesen. ")) || !bytes.Equal(pcmNext, sentencePCM("Satz k plus eins. ")) {
		t.Errorf("audio crossed between the pair: k=%q next=%q", pcmK, pcmNext)
	}

	// The pair is reused, not grown.
	drain(t, say(t, c, context.Background(), voice, "Satz k plus zwei."))
	if n := srv.upgrades.Load(); n != 2 {
		t.Errorf("sockets dialled = %d after a third sentence, want still 2", n)
	}
}

// TestWS_CancelMidAudioClosesSocketSilentlyAndPreDials: a barge is a ctx cancel.
// The protocol has no cancel message, so the sentence's socket is closed at once
// (its unread audio must never reach the next sentence), the chunk channel closes
// with NO terminal Err chunk (the cut was the caller's), and a replacement is
// dialled in the background for the reply that follows.
func TestWS_CancelMidAudioClosesSocketSilentlyAndPreDials(t *testing.T) {
	cut := make(chan struct{})
	srv := newTTDServer(t, func(c *ttdConn) {
		if c.n > 1 {
			echoScript(c)
			return
		}
		c.read()
		c.sentence()
		c.audio([]byte{1, 2, 3, 4})
		c.read() // more audio would follow; the client goes away instead
		close(cut)
	})
	c := wsClient(t, srv)
	voice := wsVoice("voice-1")

	ctx, cancel := context.WithCancel(context.Background())
	ch := say(t, c, ctx, voice, "Ein langer Satz, der unterbrochen wird.")
	if first := <-ch; first.Err != nil || len(first.PCM) != 4 {
		t.Fatalf("first chunk = %+v", first)
	}
	cancel()
	if pcm, _, terminal := collect(t, ch); terminal != nil || len(pcm) != 0 {
		t.Errorf("after cancel: %d more bytes, terminal %v; want a silent close", len(pcm), terminal)
	}
	select {
	case <-cut:
	case <-time.After(wsRecvWait):
		t.Fatal("the cut sentence's socket was not closed")
	}

	eventually(t, "the replacement socket", func() bool { return srv.upgrades.Load() == 2 && c.WSStats().Open == 1 })
	next := "Die Antwort nach der Unterbrechung."
	pcm, _, terminal := collect(t, say(t, c, context.Background(), voice, next))
	if terminal != nil || !bytes.Equal(pcm, sentencePCM(next+" ")) {
		t.Errorf("next sentence: audio %q, terminal %v", pcm, terminal)
	}
	if n := srv.upgrades.Load(); n != 2 {
		t.Errorf("sockets dialled = %d, want 2 (the next sentence rides the pre-dialled one)", n)
	}
}

// TestWS_ErrorFrameMidSentenceIsTerminalErrChunk: an error frame after audio
// already flowed is a mid-stream failure (#436): a terminal chunk carrying the
// error, then the close, and the socket is gone.
func TestWS_ErrorFrameMidSentenceIsTerminalErrChunk(t *testing.T) {
	srv := newTTDServer(t, func(c *ttdConn) {
		c.read()
		c.sentence()
		c.audio([]byte{1, 2})
		c.fail("generation_failed", "boom")
	})
	c := wsClient(t, srv)
	pcm, _, terminal := collect(t, say(t, c, context.Background(), wsVoice("voice-1"), "Ein Satz."))
	var se *elevenlabs.StreamError
	if !errors.As(terminal, &se) || se.Kind != "generation_failed" || se.Code != 1008 {
		t.Fatalf("terminal error = %v, want the generation_failed stream error", terminal)
	}
	if !bytes.Equal(pcm, []byte{1, 2}) {
		t.Errorf("audio before the failure = %v, want [1 2]", pcm)
	}
	eventually(t, "the failed socket to leave the pool", func() bool { return c.WSStats().Open == 0 })
}

// TestWS_ErrorFrameBeforeAudioIsAStartError: a sentence the server refuses
// before any audio (an unsupported language, say) is a start failure like an
// HTTP 4xx: a returned error, no channel, and not retryable.
func TestWS_ErrorFrameBeforeAudioIsAStartError(t *testing.T) {
	srv := newTTDServer(t, func(c *ttdConn) {
		c.read()
		c.sentence()
		c.fail("unsupported_language", "Model 'eleven_v4_turbo' does not support language 'xx'.")
	})
	c := wsClient(t, srv)
	ch, err := c.Synthesize(context.Background(), tts.SynthesizeRequest{Sentence: "Ein Satz.", Voice: wsVoice("voice-1")})
	var se *elevenlabs.StreamError
	if ch != nil || !errors.As(err, &se) || se.Kind != "unsupported_language" {
		t.Fatalf("Synthesize = (%v, %v), want the unsupported_language start error", ch, err)
	}
	if retry.Retryable(err) {
		t.Error("a refused sentence must not be retried")
	}
}

// TestWS_DeadPooledSocketIsRedialledTransparently: a pooled socket can die
// between sentences without the client having noticed yet (the server's 20 s
// inactivity close racing a keep-alive, a NAT drop). The sentence that finds it
// dead — the socket closes, or answers with an error frame, before any audio —
// moves to a fresh dial once and the caller sees nothing.
func TestWS_DeadPooledSocketIsRedialledTransparently(t *testing.T) {
	for name, die := range map[string]func(c *ttdConn){
		"closed":      func(c *ttdConn) { _ = c.Close() },
		"error frame": func(c *ttdConn) { c.fail("input_timeout_exceeded", "No message received within 20s.") },
	} {
		t.Run(name, func(t *testing.T) {
			srv := newTTDServer(t, func(c *ttdConn) {
				if c.n > 1 {
					echoScript(c)
					return
				}
				c.read()
				m, _ := c.sentence()
				c.audio(sentencePCM(m.Inputs[0].Text))
				c.final()
				c.sentence() // the second sentence arrives on a socket that is about to die
				die(c)
			})
			c := wsClient(t, srv)
			voice := wsVoice("voice-1")
			drain(t, say(t, c, context.Background(), voice, "Erster Satz."))

			pcm, _, terminal := collect(t, say(t, c, context.Background(), voice, "Zweiter Satz."))
			if terminal != nil || !bytes.Equal(pcm, sentencePCM("Zweiter Satz. ")) {
				t.Errorf("second sentence: audio %q, terminal %v", pcm, terminal)
			}
			if st := c.WSStats(); st.Redials != 1 || st.Dials != 2 {
				t.Errorf("stats = %+v, want 1 redial over 2 dials", st)
			}
		})
	}
}

// TestWS_FirstAudioDeadlineIsARetryableStartError: a socket that accepts the
// sentence and then says nothing must not hold the reply goroutine. The wait
// for the first frame is bounded like the HTTP path's ResponseHeaderTimeout, and
// running out is a start error the dispatch layer's retry policy retries.
func TestWS_FirstAudioDeadlineIsARetryableStartError(t *testing.T) {
	srv := newTTDServer(t, func(c *ttdConn) {
		c.read()
		for {
			if _, ok := c.sentence(); !ok {
				return
			}
		}
	})
	c := wsClient(t, srv, elevenlabs.WithWSTuning(0, 0, 40*time.Millisecond, 0, 0))
	start := time.Now()
	ch, err := c.Synthesize(context.Background(), tts.SynthesizeRequest{Sentence: "Ein Satz.", Voice: wsVoice("voice-1")})
	if ch != nil || err == nil {
		t.Fatalf("Synthesize = (%v, %v), want a start error", ch, err)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Errorf("waited %v for the first frame, want the 40 ms deadline", waited)
	}
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() || !retry.Retryable(err) {
		t.Errorf("error %v: want a retryable timeout", err)
	}
	eventually(t, "the silent socket to be closed", func() bool { return srv.open.Load() == 0 })
}

// TestWS_FreshSocketDyingBeforeAudioIsARetryableStartError: with no pooled
// socket to blame, a connection lost before the first frame is surfaced as a
// retryable start error — never a terminal Err chunk on an empty stream.
func TestWS_FreshSocketDyingBeforeAudioIsARetryableStartError(t *testing.T) {
	srv := newTTDServer(t, func(c *ttdConn) {
		c.read()
		c.sentence()
	})
	c := wsClient(t, srv)
	ch, err := c.Synthesize(context.Background(), tts.SynthesizeRequest{Sentence: "Ein Satz.", Voice: wsVoice("voice-1")})
	if ch != nil || !retry.Retryable(err) {
		t.Fatalf("Synthesize = (%v, %v), want a retryable start error", ch, err)
	}
}

// TestWS_RefusedHandshakeIsATypedHTTPError: the handshake is where the API
// rejects a key or a model; the status must reach the retry policy as it does on
// HTTP, and the slot the failed dial had reserved must come back — else two
// failed dials would leave the voice on HTTP for good.
func TestWS_RefusedHandshakeIsATypedHTTPError(t *testing.T) {
	srv := newTTDServer(t, echoScript)
	c := wsClient(t, srv)
	voice := wsVoice("voice-1")

	srv.refuse.Store(http.StatusForbidden)
	_, err := c.Synthesize(context.Background(), tts.SynthesizeRequest{Sentence: "Ein Satz.", Voice: voice})
	var he *providererr.HTTPError
	if !errors.As(err, &he) || he.StatusCode != http.StatusForbidden || retry.Retryable(err) {
		t.Fatalf("error = %v, want a non-retryable HTTP 403", err)
	}

	srv.refuse.Store(http.StatusServiceUnavailable)
	for i := 0; i < 3; i++ { // more failed dials than the per-voice cap
		if _, err := c.Synthesize(context.Background(), tts.SynthesizeRequest{Sentence: "Ein Satz.", Voice: voice}); err == nil || !retry.Retryable(err) {
			t.Fatalf("error = %v, want a retryable HTTP 503", err)
		}
	}
	if st := c.WSStats(); st.Open != 0 || st.Fallbacks != 0 || srv.httpPosts.Load() != 0 {
		t.Errorf("stats = %+v, POSTs = %d; want nothing open and no HTTP fallback", st, srv.httpPosts.Load())
	}

	srv.refuse.Store(0)
	pcm, _, terminal := collect(t, say(t, c, context.Background(), voice, "Jetzt aber."))
	if terminal != nil || !bytes.Equal(pcm, sentencePCM("Jetzt aber. ")) {
		t.Errorf("after the refusals: audio %q, terminal %v; want the sentence on a socket (a failed dial must free its slot)", pcm, terminal)
	}
}

// TestWS_DialogueSessionCapFallsBackToHTTP: every open socket holds one of the
// account's dialogue-session slots, and the pool is shared by every Voice
// Session on the key. A handshake refused with 429 means the account is at that
// cap — which the HTTP path (its own concurrency pool) is not — so the sentence
// is spoken over HTTP instead of failing the turn, and the pool stops dialling
// for a while rather than paying a refused handshake per sentence.
func TestWS_DialogueSessionCapFallsBackToHTTP(t *testing.T) {
	srv := newTTDServer(t, echoScript)
	srv.refuse.Store(http.StatusTooManyRequests)
	c := wsClient(t, srv, elevenlabs.WithWSCapBackoff(80*time.Millisecond))
	ctx := context.Background()
	voice := wsVoice("voice-1")

	for _, sentence := range []string{"Erster Satz.", "Zweiter Satz."} {
		pcm, _, terminal := collect(t, say(t, c, ctx, voice, sentence))
		if terminal != nil || !bytes.Equal(pcm, httpPCM) {
			t.Fatalf("%q at the cap: audio %q, terminal %v; want the HTTP path's audio", sentence, pcm, terminal)
		}
	}
	if err := c.Warm(ctx); err != nil {
		t.Fatalf("Warm: %v", err)
	}
	if st := c.WSStats(); st.Fallbacks != 2 || st.Open != 0 || srv.refused.Load() != 1 {
		t.Errorf("stats = %+v, refused handshakes = %d; want 2 fallbacks and ONE handshake (dials held back)", st, srv.refused.Load())
	}

	// The cap clears and the hold runs out: sentences ride a socket again.
	srv.refuse.Store(0)
	eventually(t, "a sentence on a socket once the hold ran out", func() bool {
		pcm, _, terminal := collect(t, say(t, c, ctx, voice, "Wieder da."))
		if terminal != nil {
			t.Errorf("terminal error: %v", terminal)
		}
		return bytes.Equal(pcm, sentencePCM("Wieder da. "))
	})
}

// TestWS_SessionCapErrorFrameFallsBackToHTTP: the same cap, announced after the
// upgrade as an error frame before any audio.
func TestWS_SessionCapErrorFrameFallsBackToHTTP(t *testing.T) {
	srv := newTTDServer(t, func(c *ttdConn) {
		c.read()
		c.sentence()
		c.fail("too_many_concurrent_requests", "Too many concurrent dialogue sessions.")
	})
	c := wsClient(t, srv, elevenlabs.WithWSCapBackoff(time.Minute))
	for _, sentence := range []string{"Erster Satz.", "Zweiter Satz."} {
		pcm, _, terminal := collect(t, say(t, c, context.Background(), wsVoice("voice-1"), sentence))
		if terminal != nil || !bytes.Equal(pcm, httpPCM) {
			t.Fatalf("%q: audio %q, terminal %v; want the HTTP path's audio", sentence, pcm, terminal)
		}
	}
	if st := c.WSStats(); st.Fallbacks != 2 || srv.upgrades.Load() != 1 {
		t.Errorf("stats = %+v over %d sockets; want 2 fallbacks and one socket (dials held back)", st, srv.upgrades.Load())
	}
}

// TestWS_KeepAliveIsSentWhileIdle: the server closes a socket after 20 s without
// a client message, so a pooled socket is sent keep_alive on a timer.
func TestWS_KeepAliveIsSentWhileIdle(t *testing.T) {
	srv := newTTDServer(t, echoScript)
	c := wsClient(t, srv, elevenlabs.WithWSTuning(10*time.Millisecond, time.Minute, 0, 0, 0))
	drain(t, say(t, c, context.Background(), wsVoice("voice-1"), "Ein Satz."))
	eventually(t, "keep_alive messages on the idle socket", func() bool { return srv.keepAlive.Load() >= 3 })
	if n := srv.open.Load(); n != 1 {
		t.Errorf("open sockets = %d, want the idle one kept", n)
	}
}

// TestWS_KeepAliveIsSentWhileASentenceIsHeld: playback back-pressure can hold a
// look-ahead sentence on its socket for longer than the server's 20 s inactivity
// limit with no client message in between, so keep_alive goes out while a
// sentence is leased too. Here the server only finishes the sentence once it has
// seen keep-alives arrive during it.
func TestWS_KeepAliveIsSentWhileASentenceIsHeld(t *testing.T) {
	srv := newTTDServer(t, func(c *ttdConn) {
		c.read()
		m, ok := c.sentence()
		if !ok {
			return
		}
		c.audio(sentencePCM(m.Inputs[0].Text))
		for n := 0; n < 3; { // the sentence is still open: no final yet
			k, ok := c.read()
			if !ok {
				return
			}
			if k.KeepAlive {
				n++
			}
		}
		c.final()
		c.sentence()
	})
	c := wsClient(t, srv, elevenlabs.WithWSTuning(10*time.Millisecond, time.Minute, 0, 0, 0))
	pcm, _, terminal := collect(t, say(t, c, context.Background(), wsVoice("voice-1"), "Ein Satz."))
	if terminal != nil || !bytes.Equal(pcm, sentencePCM("Ein Satz. ")) {
		t.Errorf("audio %q, terminal %v; want the sentence finished once its keep-alives arrived", pcm, terminal)
	}
}

// TestWS_SampleSplitAcrossFramesStaysAligned: frames are not promised to end on
// a sample boundary. A dangling low byte is carried into the next frame, never
// dropped — dropping it would shift every sample after it.
func TestWS_SampleSplitAcrossFramesStaysAligned(t *testing.T) {
	srv := newTTDServer(t, func(c *ttdConn) {
		c.read()
		c.sentence()
		c.audio([]byte("ABC"))
		c.audio([]byte("DEF"))
		c.audio([]byte("GH"))
		c.final()
		c.sentence()
	})
	c := wsClient(t, srv)
	ch := say(t, c, context.Background(), wsVoice("voice-1"), "Ein Satz.")
	var got []string
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("terminal error: %v", chunk.Err)
		}
		got = append(got, string(chunk.PCM))
	}
	if want := []string{"AB", "CDEF", "GH"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("chunks = %q, want %q", got, want)
	}
}

// TestWS_CancelBeforeFirstAudioClosesSocketAndPreDials: a barge can land while
// the sentence is still waiting for its first frame. The call returns the ctx
// error, the socket (with the sentence the server is about to render) is closed,
// and a replacement is dialled for the reply that follows.
func TestWS_CancelBeforeFirstAudioClosesSocketAndPreDials(t *testing.T) {
	asked := make(chan struct{})
	srv := newTTDServer(t, func(c *ttdConn) {
		if c.n > 1 {
			echoScript(c)
			return
		}
		c.read()
		c.sentence()
		close(asked)
		c.sentence() // says nothing; returns when the client closes the socket
	})
	c := wsClient(t, srv)
	voice := wsVoice("voice-1")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-asked
		cancel()
	}()
	ch, err := c.Synthesize(ctx, tts.SynthesizeRequest{Sentence: "Wird nie gesprochen.", Voice: voice})
	if ch != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("Synthesize = (%v, %v), want the cancellation", ch, err)
	}
	eventually(t, "the cut socket closed and a replacement dialled", func() bool {
		return srv.upgrades.Load() == 2 && srv.open.Load() == 1 && c.WSStats().Open == 1
	})
	drain(t, say(t, c, context.Background(), voice, "Die Antwort."))
	if st := c.WSStats(); st.Dials != 2 || st.Reuses != 1 {
		t.Errorf("stats = %+v, want the reply on the pre-dialled socket", st)
	}
}

// TestWS_IdleSocketIsClosedAfterTTL: every open socket holds one of the
// account's dialogue-session slots, so a table that fell silent must give them
// back; the next sentence dials again.
func TestWS_IdleSocketIsClosedAfterTTL(t *testing.T) {
	srv := newTTDServer(t, echoScript)
	c := wsClient(t, srv, elevenlabs.WithWSTuning(10*time.Millisecond, 60*time.Millisecond, 0, 0, 0))
	voice := wsVoice("voice-1")
	drain(t, say(t, c, context.Background(), voice, "Ein Satz."))
	eventually(t, "the idle socket to be closed", func() bool { return srv.open.Load() == 0 && c.WSStats().Open == 0 })

	pcm, _, terminal := collect(t, say(t, c, context.Background(), voice, "Noch ein Satz."))
	if terminal != nil || !bytes.Equal(pcm, sentencePCM("Noch ein Satz. ")) {
		t.Errorf("after the TTL: audio %q, terminal %v", pcm, terminal)
	}
	if st := c.WSStats(); st.Dials != 2 || st.Redials != 0 {
		t.Errorf("stats = %+v, want a plain second dial", st)
	}
}

// TestWS_PerVoiceCapSendsTheThirdOverlapOverHTTP: a voice holds at most two
// sockets (the look-ahead pair). A third overlapping sentence — a preview, a
// stray overlap — does not open a third dialogue session; it goes over HTTP.
func TestWS_PerVoiceCapSendsTheThirdOverlapOverHTTP(t *testing.T) {
	srv := newTTDServer(t, echoScript)
	c := wsClient(t, srv)
	voice := wsVoice("voice-1")
	a := say(t, c, context.Background(), voice, "Eins.")
	b := say(t, c, context.Background(), voice, "Zwei.")
	third, _, terminal := collect(t, say(t, c, context.Background(), voice, "Drei."))
	if terminal != nil || !bytes.Equal(third, httpPCM) {
		t.Errorf("third overlapping sentence: audio %q, terminal %v; want the HTTP path's audio", third, terminal)
	}
	drain(t, a)
	drain(t, b)
	if st := c.WSStats(); srv.upgrades.Load() != 2 || st.Fallbacks != 1 {
		t.Errorf("sockets = %d, stats = %+v; want 2 sockets and 1 fallback", srv.upgrades.Load(), st)
	}
}

// TestWS_TotalCapEvictsAnIdleSocketElseFallsBack: across voices the client
// holds at most its total cap. A new voice takes the place of the longest-idle
// socket; with every socket busy the sentence goes over HTTP.
func TestWS_TotalCapEvictsAnIdleSocketElseFallsBack(t *testing.T) {
	srv := newTTDServer(t, echoScript)
	c := wsClient(t, srv, elevenlabs.WithWSTuning(0, 0, 0, 0, 2))
	ctx := context.Background()
	drain(t, say(t, c, ctx, wsVoice("voice-a"), "A."))
	drain(t, say(t, c, ctx, wsVoice("voice-b"), "B."))

	pcm, _, _ := collect(t, say(t, c, ctx, wsVoice("voice-c"), "C."))
	if !bytes.Equal(pcm, sentencePCM("C. ")) {
		t.Errorf("voice-c audio = %q, want it served over a socket", pcm)
	}
	eventually(t, "the evicted socket to close", func() bool { return srv.open.Load() == 2 })
	if st := c.WSStats(); st.Open != 2 || st.Dials != 3 {
		t.Errorf("stats = %+v, want 2 open after 3 dials", st)
	}
	// voice-a was the longest idle: it is the one that went.
	drain(t, say(t, c, ctx, wsVoice("voice-b"), "B again."))
	if st := c.WSStats(); st.Dials != 3 {
		t.Errorf("voice-b had to dial (stats %+v): the wrong socket was evicted", st)
	}

	// Both slots busy: no eviction possible, HTTP serves the newcomer.
	b := say(t, c, ctx, wsVoice("voice-b"), "B busy.")
	cc := say(t, c, ctx, wsVoice("voice-c"), "C busy.")
	pcm, _, _ = collect(t, say(t, c, ctx, wsVoice("voice-a"), "A."))
	if !bytes.Equal(pcm, httpPCM) {
		t.Errorf("voice-a audio = %q with the pool full, want the HTTP path's audio", pcm)
	}
	drain(t, b)
	drain(t, cc)
}

// TestWS_WarmPreDialsKnownVoices: a dialogue socket is bound to one voice, so
// Warm cannot open one before a voice has spoken — it warms the HTTP pool then,
// as on the HTTP transport. Once a voice is known, Warm lines up an idle socket
// for it (and sends no HEAD), so the reply to the utterance that just started
// begins on a warm socket.
func TestWS_WarmPreDialsKnownVoices(t *testing.T) {
	srv := newTTDServer(t, echoScript)
	c := wsClient(t, srv, elevenlabs.WithWSTuning(10*time.Millisecond, 50*time.Millisecond, 0, 0, 0))
	ctx := context.Background()

	if err := c.Warm(ctx); err != nil {
		t.Fatalf("Warm: %v", err)
	}
	if srv.upgrades.Load() != 0 || srv.heads.Load() != 1 {
		t.Fatalf("Warm with no known voice: %d sockets, %d HEADs; want 0 and 1", srv.upgrades.Load(), srv.heads.Load())
	}

	voice := wsVoice("voice-1")
	drain(t, say(t, c, ctx, voice, "Ein Satz."))
	eventually(t, "the idle socket to expire", func() bool { return srv.open.Load() == 0 })

	if err := c.Warm(ctx); err != nil {
		t.Fatalf("Warm: %v", err)
	}
	eventually(t, "the pre-dialled socket", func() bool { return srv.upgrades.Load() == 2 && c.WSStats().Open == 1 })
	if n := srv.heads.Load(); n != 1 {
		t.Errorf("HEADs = %d, want still 1 (a websocket-only client warms sockets)", n)
	}
	drain(t, say(t, c, ctx, voice, "Die Antwort."))
	if st := c.WSStats(); st.Dials != 2 || st.Reuses != 1 {
		t.Errorf("stats = %+v, want the reply on the pre-dialled socket", st)
	}
}

// TestWS_WarmKeepsAnIdleSocketPastItsTTL: a speech start means a reply is
// coming, so Warm restarts the idle clock of the sockets it would otherwise have
// to dial again a moment later.
func TestWS_WarmKeepsAnIdleSocketPastItsTTL(t *testing.T) {
	srv := newTTDServer(t, echoScript)
	c := wsClient(t, srv, elevenlabs.WithWSTuning(10*time.Millisecond, 120*time.Millisecond, 0, 0, 0))
	ctx := context.Background()
	drain(t, say(t, c, ctx, wsVoice("voice-1"), "Ein Satz."))
	for i := 0; i < 6; i++ { // 240 ms of warms, twice the TTL
		time.Sleep(40 * time.Millisecond)
		if err := c.Warm(ctx); err != nil {
			t.Fatalf("Warm: %v", err)
		}
	}
	if st := c.WSStats(); st.Open != 1 || st.Dials != 1 {
		t.Errorf("stats = %+v, want the one socket still open", st)
	}
}

// TestWS_WarmStopsHoldingAVoiceThatWentQuiet: Warm fires on every speech start
// at the table, whoever is speaking. A voice that has not said a sentence for a
// while must stop being kept warm — else players talking among themselves would
// hold its dialogue-session slots for the whole session — and Warm must not dial
// it again either.
func TestWS_WarmStopsHoldingAVoiceThatWentQuiet(t *testing.T) {
	srv := newTTDServer(t, echoScript)
	c := wsClient(t, srv, elevenlabs.WithWSTuning(10*time.Millisecond, 40*time.Millisecond, 0, 0, 0), elevenlabs.WithWSWarmFor(80*time.Millisecond))
	ctx := context.Background()
	drain(t, say(t, c, ctx, wsVoice("voice-1"), "Ein Satz."))
	warm := func() {
		t.Helper()
		if err := c.Warm(ctx); err != nil {
			t.Fatalf("Warm: %v", err)
		}
	}
	eventually(t, "the quiet voice's socket to be let go under steady warms", func() bool {
		warm()
		time.Sleep(10 * time.Millisecond)
		return c.WSStats().Open == 0 && srv.open.Load() == 0
	})
	heads := srv.heads.Load()
	for i := 0; i < 5; i++ {
		warm()
	}
	if st := c.WSStats(); st.Dials != 1 || st.Open != 0 {
		t.Errorf("stats = %+v, want no socket dialled for a voice that went quiet", st)
	}
	if n := srv.heads.Load(); n != heads+5 {
		t.Errorf("HEADs = %d, want %d (with no voice to warm, Warm warms the HTTP pool)", n, heads+5)
	}
}

// TestWS_WarmHoldsOneSocketPerVoice: the look-ahead pair leaves a voice with two
// idle sockets, but the reply to a speech start begins with ONE sentence, so
// Warm keeps one of them past its TTL and lets the other go.
func TestWS_WarmHoldsOneSocketPerVoice(t *testing.T) {
	srv := newTTDServer(t, echoScript)
	c := wsClient(t, srv, elevenlabs.WithWSTuning(10*time.Millisecond, 60*time.Millisecond, 0, 0, 0))
	ctx := context.Background()
	voice := wsVoice("voice-1")
	a := say(t, c, ctx, voice, "Eins.")
	b := say(t, c, ctx, voice, "Zwei.")
	drain(t, a)
	drain(t, b)
	if st := c.WSStats(); st.Open != 2 {
		t.Fatalf("stats = %+v, want the pair open", st)
	}
	warm := func() bool {
		if err := c.Warm(ctx); err != nil {
			t.Errorf("Warm: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
		return c.WSStats().Open == 1
	}
	eventually(t, "the pair to shrink to one socket under steady warms", warm)
	for i := 0; i < 12; i++ { // twice the TTL
		warm()
	}
	if st := c.WSStats(); st.Open != 1 || st.Dials != 2 {
		t.Errorf("stats = %+v, want one of the two sockets kept and none dialled", st)
	}
}

// TestWS_CloseTearsEverythingDown: Close shuts idle and leased sockets alike and
// leaves no goroutine behind; a sentence still streaming ends in a terminal Err
// chunk, and the server sees every connection go.
func TestWS_CloseTearsEverythingDown(t *testing.T) {
	baseline := goleak.IgnoreCurrent()
	srv := newTTDServer(t, func(c *ttdConn) {
		c.read()
		for {
			m, ok := c.sentence()
			if !ok {
				return
			}
			c.audio(sentencePCM(m.Inputs[0].Text))
			if m.Inputs[0].Text == "hängt " {
				continue // no final: this sentence is still streaming at Close
			}
			c.final()
		}
	})
	c := elevenlabs.New("k", elevenlabs.WithBaseURL(srv.URL), elevenlabs.WithTransport(elevenlabs.TransportWS),
		elevenlabs.WithWSTuning(5*time.Millisecond, time.Minute, 0, 0, 0))
	ctx := context.Background()
	drain(t, say(t, c, ctx, wsVoice("voice-a"), "A."))
	drain(t, say(t, c, ctx, wsVoice("voice-b"), "B."))
	hanging := say(t, c, ctx, wsVoice("voice-a"), "hängt") // leases voice-a's socket and never finishes
	if n := srv.open.Load(); n != 2 {
		t.Fatalf("open sockets = %d, want 2 before Close", n)
	}

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, _, terminal := collect(t, hanging); terminal == nil {
		t.Error("the sentence cut by Close ended without a terminal error")
	}
	eventually(t, "the server to see every socket closed", func() bool { return srv.open.Load() == 0 })
	if st := c.WSStats(); st.Open != 0 {
		t.Errorf("open sockets = %d after Close, want 0", st.Open)
	}
	if err := c.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}

	// A closed client leases no socket; a late sentence is served over HTTP.
	pcm, _, _ := collect(t, say(t, c, ctx, wsVoice("voice-a"), "Zu spät."))
	if !bytes.Equal(pcm, httpPCM) || srv.upgrades.Load() != 2 {
		t.Errorf("after Close: audio %q over %d sockets, want the HTTP path and no new socket", pcm, srv.upgrades.Load())
	}
	_ = c.Close()
	srv.Close()
	goleak.VerifyNone(t, baseline)
}

// TestTransport_HTTPIsTheDefaultAndItsRequestIsUnchanged pins the default: with
// no option, with TransportHTTP, and with TransportWS on a model the dialogue
// socket does not take, a sentence is the same POST /stream it always was —
// same URL, byte-identical body — and no socket is ever dialled.
func TestTransport_HTTPIsTheDefaultAndItsRequestIsUnchanged(t *testing.T) {
	const (
		wantURL  = "/v1/text-to-speech/voice-1/stream?output_format=pcm_48000"
		wantBody = `{"text":"Ein Satz.","model_id":"%s","voice_settings":{"stability":0.5,"similarity_boost":0.75,"use_speaker_boost":true}}`
	)
	flash := func(s *elevenlabs.Settings) { s.ModelID = "eleven_flash_v2_5" }
	for name, tc := range map[string]struct {
		opts  []elevenlabs.Option
		voice func(t *testing.T) tts.Voice
		model string
	}{
		"no option":      {nil, func(*testing.T) tts.Voice { return wsVoice("voice-1") }, elevenlabs.ModelV4Turbo},
		"http":           {[]elevenlabs.Option{elevenlabs.WithTransport(elevenlabs.TransportHTTP)}, func(*testing.T) tts.Voice { return wsVoice("voice-1") }, elevenlabs.ModelV4Turbo},
		"ws, flash v2.5": {[]elevenlabs.Option{elevenlabs.WithTransport(elevenlabs.TransportWS)}, func(t *testing.T) tts.Voice { return wsVoiceWith(t, "voice-1", flash) }, "eleven_flash_v2_5"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := newTTDServer(t, echoScript)
			c := elevenlabs.New("k", append([]elevenlabs.Option{elevenlabs.WithBaseURL(srv.URL)}, tc.opts...)...)
			defer c.Close()
			pcm, _, terminal := collect(t, say(t, c, context.Background(), tc.voice(t), "Ein Satz."))
			if terminal != nil || !bytes.Equal(pcm, httpPCM) {
				t.Fatalf("audio %q, terminal %v; want the HTTP path", pcm, terminal)
			}
			if n := srv.upgrades.Load(); n != 0 {
				t.Errorf("sockets dialled = %d, want 0", n)
			}
			srv.mu.Lock()
			defer srv.mu.Unlock()
			if len(srv.httpURLs) != 1 || srv.httpURLs[0] != wantURL {
				t.Errorf("request URLs = %v, want [%s]", srv.httpURLs, wantURL)
			}
			if want := []byte(fmt.Sprintf(wantBody, tc.model)); len(srv.httpBodies) != 1 || !bytes.Equal(srv.httpBodies[0], want) {
				t.Errorf("request body = %s\nwant         %s", srv.httpBodies, want)
			}
		})
	}
}

func TestParseTransport(t *testing.T) {
	for in, want := range map[string]elevenlabs.Transport{
		"": elevenlabs.TransportHTTP, "http": elevenlabs.TransportHTTP, " HTTP ": elevenlabs.TransportHTTP,
		"ws": elevenlabs.TransportWS, "WS": elevenlabs.TransportWS,
	} {
		if got, err := elevenlabs.ParseTransport(in); err != nil || got != want {
			t.Errorf("ParseTransport(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	// A typo must never switch the transport.
	if got, err := elevenlabs.ParseTransport("websocket"); err == nil || got != elevenlabs.TransportHTTP {
		t.Errorf("ParseTransport(websocket) = %q, %v; want http and an error", got, err)
	}
}

// TestWS_StrayFrameOnAnIdleSocketDropsIt: the pool's safety rests on a socket
// being clean between sentences. Should a server send audio after a sentence's
// final marker, once the sentence handed its socket back, that socket's state is
// not what the pool believes: it is closed, and the next sentence dials a fresh
// one and gets only its own audio.
func TestWS_StrayFrameOnAnIdleSocketDropsIt(t *testing.T) {
	stray := make(chan struct{})
	srv := newTTDServer(t, func(c *ttdConn) {
		if c.n > 1 {
			echoScript(c)
			return
		}
		c.read()
		m, _ := c.sentence()
		c.audio(sentencePCM(m.Inputs[0].Text))
		c.final()
		<-stray // the test saw the sentence end: the socket is back in the pool
		c.audio([]byte("STRAY!"))
		echoScript(c) // would answer a sentence wrongly sent on this socket
	})
	c := wsClient(t, srv)
	voice := wsVoice("voice-1")
	drain(t, say(t, c, context.Background(), voice, "Erster Satz."))
	close(stray)
	eventually(t, "the socket that got a stray frame to be closed", func() bool { return c.WSStats().Open == 0 && srv.open.Load() == 0 })

	pcm, _, terminal := collect(t, say(t, c, context.Background(), voice, "Zweiter Satz."))
	if terminal != nil || !bytes.Equal(pcm, sentencePCM("Zweiter Satz. ")) {
		t.Errorf("next sentence: audio %q, terminal %v; want only its own audio", pcm, terminal)
	}
	if st := c.WSStats(); st.Dials != 2 || st.Redials != 0 {
		t.Errorf("stats = %+v, want a plain second dial", st)
	}
}
