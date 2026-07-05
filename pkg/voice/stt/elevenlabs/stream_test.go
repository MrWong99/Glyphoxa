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

const recvTimeout = 2 * time.Second

// --- scripted websocket server harness (ADR-0021 deterministic posture) ---
//
// Scripts run in the server's per-connection goroutine and therefore MUST NOT
// call t.Fatal*/t.FailNow (those are legal only on the test goroutine). Scripts
// do I/O only and relay observations back to the test goroutine over channels;
// every assertion runs on the test goroutine.

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

func newScriptedServer(t *testing.T, script func(conn *websocket.Conn)) *scriptedServer {
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
		script(conn)
	}))
	return ss
}

// chunkMsg is a decoded client->server input_audio_chunk message.
type chunkMsg struct {
	pcm          []byte
	commit       bool
	sampleRate   int
	previousText string
	ok           bool // false once the connection is closed / a read fails
}

// readChunk reads and decodes one input_audio_chunk. It never touches *testing.T
// (it runs on the server goroutine); ok=false signals the connection is done.
func readChunk(conn *websocket.Conn) chunkMsg {
	_, data, err := conn.ReadMessage()
	if err != nil {
		return chunkMsg{}
	}
	var raw struct {
		MessageType  string `json:"message_type"`
		AudioBase64  string `json:"audio_base_64"`
		Commit       bool   `json:"commit"`
		SampleRate   int    `json:"sample_rate"`
		PreviousText string `json:"previous_text"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return chunkMsg{}
	}
	pcm, _ := base64.StdEncoding.DecodeString(raw.AudioBase64)
	return chunkMsg{
		pcm:          pcm,
		commit:       raw.Commit,
		sampleRate:   raw.SampleRate,
		previousText: raw.PreviousText,
		ok:           raw.MessageType == msgInputAudioChunkWire,
	}
}

const msgInputAudioChunkWire = "input_audio_chunk"

// readUntilCommit drains chunks (relaying each onto sink, if non-nil) until it
// sees commit:true. Returns false if the connection closed first.
func readUntilCommit(conn *websocket.Conn, sink chan<- chunkMsg) bool {
	for {
		c := readChunk(conn)
		if !c.ok {
			return false
		}
		if sink != nil {
			sink <- c
		}
		if c.commit {
			return true
		}
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
	t.Helper()
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

func openTestStream(t *testing.T, ss *scriptedServer, cfg stt.StreamConfig) stt.Stream {
	t.Helper()
	c := elevenlabs.New("test-key", elevenlabs.WithBaseURL(ss.URL))
	s, err := c.OpenStream(context.Background(), cfg)
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	return s
}

func recvChunk(t *testing.T, ch <-chan chunkMsg) chunkMsg {
	t.Helper()
	select {
	case c := <-ch:
		return c
	case <-time.After(recvTimeout):
		t.Fatal("timed out waiting for a chunk")
		return chunkMsg{}
	}
}

func recvCommit(t *testing.T, ch <-chan stt.CommitResult) stt.CommitResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(recvTimeout):
		t.Fatal("timed out waiting for commit result")
		return stt.CommitResult{}
	}
}

func asStreamError(t *testing.T, err error) *stt.StreamError {
	t.Helper()
	var se *stt.StreamError
	if !errors.As(err, &se) {
		t.Fatalf("error %v (%T) is not a *stt.StreamError", err, err)
	}
	return se
}

// echoUntilClose keeps a connection open, discarding client frames until close.
func echoUntilClose(conn *websocket.Conn) {
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

// --- Test 1: dial contract ---

func TestOpenStream_DialContract_PinsPathQueryAndKey(t *testing.T) {
	ss := newScriptedServer(t, echoUntilClose)
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
	ss := newScriptedServer(t, echoUntilClose)
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

// --- Test 11a: OpenStream rejects unsupported sample rate ---

func TestOpenStream_UnsupportedSampleRate_Errors(t *testing.T) {
	c := elevenlabs.New("k")
	_, err := c.OpenStream(context.Background(), stt.StreamConfig{SampleRate: 48000})
	if err == nil {
		t.Fatal("OpenStream with 48000 Hz returned nil error")
	}
}

// --- Test 3: Send aggregation to >=100ms chunks, order-preserving ---

func TestSend_AggregatesTo100msChunk_PreservesOrder(t *testing.T) {
	chunks := make(chan chunkMsg, 8)
	ss := newScriptedServer(t, func(conn *websocket.Conn) {
		for {
			c := readChunk(conn)
			if !c.ok {
				return
			}
			chunks <- c
		}
	})
	defer ss.Close()

	s := openTestStream(t, ss, stt.StreamConfig{SampleRate: 16000})
	defer s.Close()

	// Four 32 ms frames = 128 ms, crossing the 100 ms threshold on the fourth,
	// so they flush as ONE chunk in frame order.
	frames := []audio.Frame{rampFrame(t, 0), rampFrame(t, 1000), rampFrame(t, 2000), rampFrame(t, 3000)}
	for _, f := range frames {
		if err := s.Send(f); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}

	c := recvChunk(t, chunks)
	if c.commit {
		t.Error("aggregated audio chunk had commit=true, want false")
	}
	if c.sampleRate != 16000 {
		t.Errorf("sample_rate = %d, want 16000", c.sampleRate)
	}
	want := pcmOf(frames...)
	if len(c.pcm) != len(want) {
		t.Fatalf("chunk pcm len = %d, want %d (should be 4 frames concatenated)", len(c.pcm), len(want))
	}
	for i := range want {
		if c.pcm[i] != want[i] {
			t.Fatalf("chunk pcm differs at byte %d: got 0x%02x want 0x%02x", i, c.pcm[i], want[i])
		}
	}
}

// --- Test 4: partials arrive in order via OnPartial ---

func TestPartials_DeliveredInOrder(t *testing.T) {
	ss := newScriptedServer(t, func(conn *websocket.Conn) {
		_ = conn.WriteJSON(partialMsg("hel"))
		_ = conn.WriteJSON(partialMsg("hello wor"))
		echoUntilClose(conn)
	})
	defer ss.Close()

	partials := make(chan string, 8)
	c := elevenlabs.New("test-key", elevenlabs.WithBaseURL(ss.URL))
	s, err := c.OpenStream(context.Background(), stt.StreamConfig{
		SampleRate: 16000,
		OnPartial:  func(text string) { partials <- text },
	})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer s.Close()

	if got := recvString(t, partials); got != "hel" {
		t.Errorf("first partial = %q, want %q", got, "hel")
	}
	if got := recvString(t, partials); got != "hello wor" {
		t.Errorf("second partial = %q, want %q", got, "hello wor")
	}
}

func recvString(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case s := <-ch:
		return s
	case <-time.After(recvTimeout):
		t.Fatal("timed out waiting for a partial")
		return ""
	}
}

// --- Test 5: commit flushes pending audio then commit sentinel; resolves ---

func TestCommit_FlushesThenResolves_AfterPartials(t *testing.T) {
	seen := make(chan chunkMsg, 8)
	ss := newScriptedServer(t, func(conn *websocket.Conn) {
		if !readUntilCommit(conn, seen) {
			return
		}
		_ = conn.WriteJSON(partialMsg("interim"))
		_ = conn.WriteJSON(committedMsg("final text"))
		echoUntilClose(conn)
	})
	defer ss.Close()

	partials := make(chan string, 8)
	c := elevenlabs.New("test-key", elevenlabs.WithBaseURL(ss.URL))
	s, err := c.OpenStream(context.Background(), stt.StreamConfig{
		SampleRate: 16000,
		OnPartial:  func(text string) { partials <- text },
	})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer s.Close()

	// Two 32 ms frames = 64 ms < 100 ms, so nothing flushes until Commit.
	f0, f1 := rampFrame(t, 0), rampFrame(t, 500)
	if err := s.Send(f0); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := s.Send(f1); err != nil {
		t.Fatalf("Send: %v", err)
	}
	commitCh, err := s.Commit()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// Server sees the flushed remainder (commit=false) THEN the commit sentinel.
	audioChunk := recvChunk(t, seen)
	if audioChunk.commit {
		t.Error("first chunk had commit=true, want the flushed audio remainder")
	}
	if want := pcmOf(f0, f1); len(audioChunk.pcm) != len(want) {
		t.Errorf("remainder pcm len = %d, want %d", len(audioChunk.pcm), len(want))
	}
	commitChunk := recvChunk(t, seen)
	if !commitChunk.commit {
		t.Error("second chunk commit=false, want the manual commit sentinel")
	}
	if len(commitChunk.pcm) != 0 {
		t.Errorf("commit sentinel carried %d audio bytes, want 0", len(commitChunk.pcm))
	}

	res := recvCommit(t, commitCh)
	if res.Err != nil {
		t.Fatalf("commit resolved with error: %v", res.Err)
	}
	if res.Transcript.Text != "final text" {
		t.Errorf("committed text = %q, want %q", res.Transcript.Text, "final text")
	}
	// The partial was processed on the read pump before the committed frame, so
	// by the time the commit resolves it is already delivered.
	select {
	case p := <-partials:
		if p != "interim" {
			t.Errorf("partial = %q, want %q", p, "interim")
		}
	default:
		t.Error("expected the interim partial to have been delivered before commit resolution")
	}
}

func TestCommit_ZeroPartials_StillResolves(t *testing.T) {
	ss := newScriptedServer(t, func(conn *websocket.Conn) {
		if !readUntilCommit(conn, nil) {
			return
		}
		_ = conn.WriteJSON(committedMsg("just this"))
		echoUntilClose(conn)
	})
	defer ss.Close()

	s := openTestStream(t, ss, stt.StreamConfig{SampleRate: 16000})
	defer s.Close()

	commitCh, err := s.Commit()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	res := recvCommit(t, commitCh)
	if res.Err != nil {
		t.Fatalf("commit resolved with error: %v", res.Err)
	}
	if res.Transcript.Text != "just this" {
		t.Errorf("committed text = %q, want %q", res.Transcript.Text, "just this")
	}
}

// --- Test 6: two utterances in one session map FIFO ---

func TestTwoUtterances_OneSession_FIFO(t *testing.T) {
	ss := newScriptedServer(t, func(conn *websocket.Conn) {
		if !readUntilCommit(conn, nil) {
			return
		}
		_ = conn.WriteJSON(committedMsg("first"))
		if !readUntilCommit(conn, nil) {
			return
		}
		_ = conn.WriteJSON(committedMsg("second"))
		echoUntilClose(conn)
	})
	defer ss.Close()

	s := openTestStream(t, ss, stt.StreamConfig{SampleRate: 16000})
	defer s.Close()

	if err := s.Send(rampFrame(t, 0)); err != nil {
		t.Fatalf("Send 1: %v", err)
	}
	c1, err := s.Commit()
	if err != nil {
		t.Fatalf("Commit 1: %v", err)
	}
	if err := s.Send(rampFrame(t, 100)); err != nil {
		t.Fatalf("Send 2: %v", err)
	}
	c2, err := s.Commit()
	if err != nil {
		t.Fatalf("Commit 2: %v", err)
	}

	if got := recvCommit(t, c1).Transcript.Text; got != "first" {
		t.Errorf("commit 1 = %q, want first", got)
	}
	if got := recvCommit(t, c2).Transcript.Text; got != "second" {
		t.Errorf("commit 2 = %q, want second", got)
	}
}
