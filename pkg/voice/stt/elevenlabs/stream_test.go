package elevenlabs_test

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt/elevenlabs"
)

// Compile-time assertion: [elevenlabs.Client] satisfies the streaming surface
// exactly as it already satisfies [stt.Recognizer], and the batch adapter is
// untouched.
var _ stt.StreamingRecognizer = (*elevenlabs.Client)(nil)

// --- scripted websocket server harness (ADR-0021 deterministic posture) ---

// scriptedServer is an httptest server that upgrades every connection to a
// websocket and runs a per-connection script. It records the upgrade request
// so dial-contract assertions can inspect path, query, and headers, and counts
// upgrades so the reconnect test can prove a fresh session.
type scriptedServer struct {
	*httptest.Server
	upgrades atomic.Int64
	lastReq  atomic.Pointer[dialInfo]
}

type dialInfo struct {
	path   string
	query  map[string][]string
	apiKey string
}

// newScriptedServer starts a server whose every websocket connection is driven
// by script. The caller owns shutdown via defer srv.Close() so goroutine-leak
// tests can order the close before the leak assertion.
func newScriptedServer(t *testing.T, script func(t *testing.T, conn *websocket.Conn)) *scriptedServer {
	t.Helper()
	ss := &scriptedServer{}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	ss.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ss.upgrades.Add(1)
		ss.lastReq.Store(&dialInfo{
			path:   r.URL.Path,
			query:  r.URL.Query(),
			apiKey: r.Header.Get("xi-api-key"),
		})
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		script(t, conn)
	}))
	return ss
}

// chunkMsg is a decoded client->server input_audio_chunk message.
type chunkMsg struct {
	pcm          []byte
	commit       bool
	sampleRate   int
	previousText string
}

// readChunk reads one input_audio_chunk from the client and decodes its base64
// audio into raw PCM bytes.
func readChunk(t *testing.T, conn *websocket.Conn) chunkMsg {
	t.Helper()
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("readChunk: ReadMessage: %v", err)
	}
	var raw struct {
		MessageType  string `json:"message_type"`
		AudioBase64  string `json:"audio_base_64"`
		Commit       bool   `json:"commit"`
		SampleRate   int    `json:"sample_rate"`
		PreviousText string `json:"previous_text"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("readChunk: unmarshal %q: %v", data, err)
	}
	if raw.MessageType != "input_audio_chunk" {
		t.Fatalf("readChunk: message_type = %q, want input_audio_chunk", raw.MessageType)
	}
	pcm, err := base64.StdEncoding.DecodeString(raw.AudioBase64)
	if err != nil {
		t.Fatalf("readChunk: base64 decode: %v", err)
	}
	return chunkMsg{pcm: pcm, commit: raw.Commit, sampleRate: raw.SampleRate, previousText: raw.PreviousText}
}

func sendJSON(t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	if err := conn.WriteJSON(v); err != nil {
		t.Fatalf("sendJSON: %v", err)
	}
}

func partialMsg(text string) map[string]any {
	return map[string]any{"message_type": "partial_transcript", "text": text}
}

func committedMsg(text string) map[string]any {
	return map[string]any{"message_type": "committed_transcript", "text": text}
}

func errorMsg(code, detail string) map[string]any {
	return map[string]any{"message_type": code, "error": detail}
}

// frame16k is one 16 kHz / 32 ms mono frame (512 samples) filled with samples.
func frame16k(t *testing.T, samples []int16) audio.Frame {
	t.Helper()
	f, err := audio.NewFrame(samples, 16000, 32)
	if err != nil {
		t.Fatalf("audio.NewFrame: %v", err)
	}
	return f
}

// rampFrame builds a 512-sample frame whose values are start..start+511, so a
// concatenated PCM stream is order-sensitive.
func rampFrame(t *testing.T, start int) audio.Frame {
	s := make([]int16, 512)
	for i := range s {
		s[i] = int16(start + i)
	}
	return frame16k(t, s)
}

func pcmOf(frames ...audio.Frame) []byte {
	var out []byte
	var buf [2]byte
	for _, f := range frames {
		for _, s := range f.Samples() {
			binary.LittleEndian.PutUint16(buf[:], uint16(s))
			out = append(out, buf[:]...)
		}
	}
	return out
}

// openTestStream dials the scripted server with a working key.
func openTestStream(t *testing.T, ss *scriptedServer, cfg stt.StreamConfig) stt.Stream {
	t.Helper()
	c := elevenlabs.New("test-key", elevenlabs.WithBaseURL(ss.URL))
	s, err := c.OpenStream(context.Background(), cfg)
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	return s
}

// --- Test 1: dial contract ---

func TestOpenStream_DialContract_PinsPathQueryAndKey(t *testing.T) {
	ss := newScriptedServer(t, func(t *testing.T, conn *websocket.Conn) {
		// keep the connection open until the client closes it
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	defer ss.Close()

	c := elevenlabs.New("expected-key", elevenlabs.WithBaseURL(ss.URL))
	s, err := c.OpenStream(context.Background(), stt.StreamConfig{SampleRate: 16000})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer s.Close()

	info := ss.lastReq.Load()
	if info == nil {
		t.Fatal("server recorded no upgrade request")
	}
	if info.path != "/v1/speech-to-text/realtime" {
		t.Errorf("path = %q, want /v1/speech-to-text/realtime", info.path)
	}
	if got := info.query["model_id"]; len(got) != 1 || got[0] != "scribe_v2_realtime" {
		t.Errorf("model_id = %v, want [scribe_v2_realtime]", got)
	}
	if got := info.query["audio_format"]; len(got) != 1 || got[0] != "pcm_16000" {
		t.Errorf("audio_format = %v, want [pcm_16000]", got)
	}
	if got := info.query["commit_strategy"]; len(got) != 1 || got[0] != "manual" {
		t.Errorf("commit_strategy = %v, want [manual]", got)
	}
	if _, ok := info.query["language_code"]; ok {
		t.Errorf("language_code present but Language was empty: %v", info.query["language_code"])
	}
	if info.apiKey != "expected-key" {
		t.Errorf("xi-api-key = %q, want expected-key", info.apiKey)
	}
}

func TestOpenStream_DialContract_LanguageCode(t *testing.T) {
	ss := newScriptedServer(t, func(t *testing.T, conn *websocket.Conn) {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
	defer ss.Close()

	s := openTestStream(t, ss, stt.StreamConfig{SampleRate: 16000, Language: "de"})
	defer s.Close()

	info := ss.lastReq.Load()
	if got := info.query["language_code"]; len(got) != 1 || got[0] != "de" {
		t.Errorf("language_code = %v, want [de]", got)
	}
}

// --- Test 2: missing key ---

func TestOpenStream_NoKey_NoEnv_Errors(t *testing.T) {
	t.Setenv("ELEVENLABS_API_KEY", "")
	c := elevenlabs.New("")
	_, err := c.OpenStream(context.Background(), stt.StreamConfig{SampleRate: 16000})
	if err == nil {
		t.Fatal("OpenStream without API key returned nil error")
	}
	if !strings.Contains(err.Error(), "missing API key") {
		t.Errorf("error %q does not mention missing API key", err)
	}
}

// --- Test 11 (part): OpenStream rejects unsupported sample rate ---

func TestOpenStream_UnsupportedSampleRate_Errors(t *testing.T) {
	c := elevenlabs.New("k")
	_, err := c.OpenStream(context.Background(), stt.StreamConfig{SampleRate: 48000})
	if err == nil {
		t.Fatal("OpenStream with 48000 Hz returned nil error")
	}
}

// --- helper for typed-error assertions ---

func asStreamError(t *testing.T, err error) *stt.StreamError {
	t.Helper()
	var se *stt.StreamError
	if !errors.As(err, &se) {
		t.Fatalf("error %v (%T) is not a *stt.StreamError", err, err)
	}
	return se
}

// silence keeps unused harness helpers referenced until later tests use them.
var _ = sync.Mutex{}
var _ = time.Second
var _ = asStreamError
