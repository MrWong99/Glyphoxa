package elevenlabs_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
)

// --- scripted Text-to-Dialogue websocket server (pattern: the STT adapter's
// newScriptedServer) ---
//
// Scripts run on the server's per-connection goroutine and MUST NOT call
// t.Fatal*; they do I/O only and every assertion runs on the test goroutine.

const (
	ttdPath     = "/v1/text-to-dialogue/stream-input"
	wsRecvWait  = 2 * time.Second
	wsQuietWait = 150 * time.Millisecond
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
	srv        *ttdServer
	n          int64 // 1-based ordinal of this connection
	keepAlives atomic.Int64
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
			c.keepAlives.Add(1)
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
