package elevenlabs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MrWong99/glyphoxa/pkg/provider/tts"
	"github.com/coder/websocket"
)

// ---- WebSocket message construction ----

func TestBuildWSMessage_WithVoiceSettings(t *testing.T) {
	vs := &voiceSettings{Stability: 0.5, SimilarityBoost: 0.75}
	data, err := buildWSMessage("Hello there", vs)
	if err != nil {
		t.Fatalf("buildWSMessage: %v", err)
	}

	var msg textMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Text != "Hello there" {
		t.Errorf("expected text 'Hello there', got %q", msg.Text)
	}
	if msg.VoiceSettings == nil {
		t.Fatal("expected non-nil voice settings")
	}
	if msg.VoiceSettings.Stability != 0.5 {
		t.Errorf("expected stability 0.5, got %f", msg.VoiceSettings.Stability)
	}
	if msg.VoiceSettings.SimilarityBoost != 0.75 {
		t.Errorf("expected similarity_boost 0.75, got %f", msg.VoiceSettings.SimilarityBoost)
	}
}

func TestBuildWSMessage_WithoutVoiceSettings(t *testing.T) {
	data, err := buildWSMessage("Flush", nil)
	if err != nil {
		t.Fatalf("buildWSMessage: %v", err)
	}

	var msg textMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if msg.Text != "Flush" {
		t.Errorf("expected text 'Flush', got %q", msg.Text)
	}
	if msg.VoiceSettings != nil {
		t.Error("expected nil voice_settings when omitempty")
	}
}

func TestBuildWSMessage_FlushCommand(t *testing.T) {
	// ElevenLabs flush = {"text":""} with no other fields.
	data, err := buildWSMessage("", nil)
	if err != nil {
		t.Fatalf("buildWSMessage: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal flush: %v", err)
	}
	textVal, ok := raw["text"]
	if !ok {
		t.Fatal("expected 'text' field in flush message")
	}
	if string(textVal) != `""` {
		t.Errorf("expected empty string for text, got %s", textVal)
	}
	if _, exists := raw["voice_settings"]; exists {
		t.Error("flush message should not contain voice_settings")
	}
}

// ---- URL construction ----

func TestBuildURLForVoice(t *testing.T) {
	url := buildURLForVoice("voice-abc123", "eleven_flash_v2_5")
	if !strings.Contains(url, "voice-abc123") {
		t.Errorf("URL should contain voice ID, got: %s", url)
	}
	if !strings.Contains(url, "eleven_flash_v2_5") {
		t.Errorf("URL should contain model ID, got: %s", url)
	}
	if !strings.HasPrefix(url, "wss://") {
		t.Errorf("URL should be a WebSocket URL, got: %s", url)
	}
}

// ---- Voice list response parsing ----

func TestParseVoicesResponse_Success(t *testing.T) {
	raw := []byte(`{
		"voices": [
			{
				"voice_id": "abc123",
				"name": "Rachel",
				"category": "premade",
				"labels": {"gender": "female", "accent": "american"}
			},
			{
				"voice_id": "def456",
				"name": "Adam",
				"category": "premade",
				"labels": {"gender": "male"}
			}
		]
	}`)

	profiles, err := parseVoicesResponse(raw)
	if err != nil {
		t.Fatalf("parseVoicesResponse: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("expected 2 profiles, got %d", len(profiles))
	}

	rachel := profiles[0]
	if rachel.ID != "abc123" {
		t.Errorf("expected ID 'abc123', got %q", rachel.ID)
	}
	if rachel.Name != "Rachel" {
		t.Errorf("expected Name 'Rachel', got %q", rachel.Name)
	}
	if rachel.Provider != "elevenlabs" {
		t.Errorf("expected Provider 'elevenlabs', got %q", rachel.Provider)
	}
	if rachel.Metadata["gender"] != "female" {
		t.Errorf("expected gender 'female', got %q", rachel.Metadata["gender"])
	}
	if rachel.Metadata["category"] != "premade" {
		t.Errorf("expected category 'premade', got %q", rachel.Metadata["category"])
	}

	adam := profiles[1]
	if adam.ID != "def456" {
		t.Errorf("expected ID 'def456', got %q", adam.ID)
	}
}

func TestParseVoicesResponse_Empty(t *testing.T) {
	raw := []byte(`{"voices":[]}`)
	profiles, err := parseVoicesResponse(raw)
	if err != nil {
		t.Fatalf("parseVoicesResponse: %v", err)
	}
	if len(profiles) != 0 {
		t.Errorf("expected 0 profiles, got %d", len(profiles))
	}
}

func TestParseVoicesResponse_InvalidJSON(t *testing.T) {
	_, err := parseVoicesResponse([]byte(`{invalid`))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestParseVoicesResponse_NoLabels(t *testing.T) {
	raw := []byte(`{
		"voices": [
			{"voice_id": "x1", "name": "Ghost", "category": "", "labels": null}
		]
	}`)
	profiles, err := parseVoicesResponse(raw)
	if err != nil {
		t.Fatalf("parseVoicesResponse: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("expected 1 profile, got %d", len(profiles))
	}
	// category is empty, so it should not appear in metadata.
	if _, ok := profiles[0].Metadata["category"]; ok {
		t.Error("expected no 'category' key in metadata when category is empty")
	}
}

// ---- Constructor tests ----

func TestNew_EmptyAPIKey(t *testing.T) {
	_, err := New("")
	if err == nil {
		t.Error("expected error for empty API key")
	}
}

func TestNew_Defaults(t *testing.T) {
	p, err := New("key")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.model != defaultModel {
		t.Errorf("expected model %q, got %q", defaultModel, p.model)
	}
	if p.outputFormat != defaultOutputFmt {
		t.Errorf("expected outputFormat %q, got %q", defaultOutputFmt, p.outputFormat)
	}
}

func TestNew_WithOptions(t *testing.T) {
	p, err := New("key", WithModel("eleven_multilingual_v2"), WithOutputFormat("pcm_24000"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.model != "eleven_multilingual_v2" {
		t.Errorf("expected model 'eleven_multilingual_v2', got %q", p.model)
	}
	if p.outputFormat != "pcm_24000" {
		t.Errorf("expected outputFormat 'pcm_24000', got %q", p.outputFormat)
	}
}

// ---- CloneVoice tests ----

func TestCloneVoice_NilSamples(t *testing.T) {
	t.Parallel()
	p, err := New("test-key")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = p.CloneVoice(context.Background(), nil)
	if err == nil {
		t.Error("expected error for nil samples")
	}
}

func TestCloneVoice_EmptySamples(t *testing.T) {
	t.Parallel()
	p, err := New("test-key")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = p.CloneVoice(context.Background(), [][]byte{})
	if err == nil {
		t.Error("expected error for empty samples")
	}
}

func TestCloneVoice_Success(t *testing.T) {
	t.Parallel()

	srv := newCloneVoiceServer(t, http.StatusOK, `{"voice_id":"cloned-123","requires_verification":false}`,
		func(t *testing.T, r *http.Request) {
			t.Helper()
			if r.Method != http.MethodPost {
				t.Errorf("expected POST, got %s", r.Method)
			}
			if got := r.Header.Get("xi-api-key"); got != "test-key" {
				t.Errorf("expected xi-api-key 'test-key', got %q", got)
			}
			if err := r.ParseMultipartForm(10 << 20); err != nil {
				t.Fatalf("ParseMultipartForm: %v", err)
			}
			name := r.FormValue("name")
			if !strings.HasPrefix(name, "glyphoxa-clone-") {
				t.Errorf("expected name to start with 'glyphoxa-clone-', got %q", name)
			}
			files := r.MultipartForm.File["files"]
			if len(files) != 2 {
				t.Fatalf("expected 2 files, got %d", len(files))
			}
			if files[0].Filename != "sample_0.wav" {
				t.Errorf("expected filename 'sample_0.wav', got %q", files[0].Filename)
			}
			if files[1].Filename != "sample_1.wav" {
				t.Errorf("expected filename 'sample_1.wav', got %q", files[1].Filename)
			}
		})
	defer srv.Close()

	p := newProviderWithServer(t, "test-key", srv)
	profile, err := p.CloneVoice(context.Background(), [][]byte{
		[]byte("audio-data-0"),
		[]byte("audio-data-1"),
	})
	if err != nil {
		t.Fatalf("CloneVoice: %v", err)
	}
	if profile.ID != "cloned-123" {
		t.Errorf("expected ID 'cloned-123', got %q", profile.ID)
	}
	if profile.Provider != "elevenlabs" {
		t.Errorf("expected Provider 'elevenlabs', got %q", profile.Provider)
	}
	if !strings.HasPrefix(profile.Name, "glyphoxa-clone-") {
		t.Errorf("expected Name to start with 'glyphoxa-clone-', got %q", profile.Name)
	}
}

func TestCloneVoiceWithOptions_CustomName(t *testing.T) {
	t.Parallel()

	srv := newCloneVoiceServer(t, http.StatusOK, `{"voice_id":"v42","requires_verification":false}`,
		func(t *testing.T, r *http.Request) {
			t.Helper()
			if err := r.ParseMultipartForm(10 << 20); err != nil {
				t.Fatalf("ParseMultipartForm: %v", err)
			}
			if got := r.FormValue("name"); got != "my-custom-voice" {
				t.Errorf("expected name 'my-custom-voice', got %q", got)
			}
		})
	defer srv.Close()

	p := newProviderWithServer(t, "test-key", srv)
	profile, err := p.CloneVoiceWithOptions(
		context.Background(),
		[][]byte{[]byte("sample")},
		WithCloneName("my-custom-voice"),
	)
	if err != nil {
		t.Fatalf("CloneVoiceWithOptions: %v", err)
	}
	if profile.Name != "my-custom-voice" {
		t.Errorf("expected Name 'my-custom-voice', got %q", profile.Name)
	}
}

func TestCloneVoiceWithOptions_AllOptions(t *testing.T) {
	t.Parallel()

	srv := newCloneVoiceServer(t, http.StatusOK, `{"voice_id":"v99","requires_verification":false}`,
		func(t *testing.T, r *http.Request) {
			t.Helper()
			if err := r.ParseMultipartForm(10 << 20); err != nil {
				t.Fatalf("ParseMultipartForm: %v", err)
			}
			if got := r.FormValue("name"); got != "full-options-voice" {
				t.Errorf("expected name 'full-options-voice', got %q", got)
			}
			if got := r.FormValue("remove_background_noise"); got != "true" {
				t.Errorf("expected remove_background_noise 'true', got %q", got)
			}
			if got := r.FormValue("description"); got != "a test voice" {
				t.Errorf("expected description 'a test voice', got %q", got)
			}
			labelsRaw := r.FormValue("labels")
			if labelsRaw == "" {
				t.Error("expected labels field to be present")
			}
			var labels map[string]string
			if err := json.Unmarshal([]byte(labelsRaw), &labels); err != nil {
				t.Fatalf("unmarshal labels: %v", err)
			}
			if labels["language"] != "en" {
				t.Errorf("expected labels[language]='en', got %q", labels["language"])
			}
			if labels["accent"] != "british" {
				t.Errorf("expected labels[accent]='british', got %q", labels["accent"])
			}
		})
	defer srv.Close()

	p := newProviderWithServer(t, "test-key", srv)
	_, err := p.CloneVoiceWithOptions(
		context.Background(),
		[][]byte{[]byte("sample-data")},
		WithCloneName("full-options-voice"),
		WithRemoveBackgroundNoise(true),
		WithDescription("a test voice"),
		WithLabels(map[string]string{"language": "en", "accent": "british"}),
	)
	if err != nil {
		t.Fatalf("CloneVoiceWithOptions: %v", err)
	}
}

func TestCloneVoice_APIError(t *testing.T) {
	t.Parallel()

	srv := newCloneVoiceServer(t, http.StatusBadRequest, `{"detail":"invalid file format"}`, nil)
	defer srv.Close()

	p := newProviderWithServer(t, "test-key", srv)
	_, err := p.CloneVoice(context.Background(), [][]byte{[]byte("bad-data")})
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("expected error to mention status 400, got: %v", err)
	}
}

// ---- test helpers ----

// newCloneVoiceServer starts an httptest.Server that handles POST /v1/voices/add.
// It responds with the given status and body. If validate is non-nil it is called
// to perform extra assertions on the incoming request.
func newCloneVoiceServer(t *testing.T, status int, body string, validate func(*testing.T, *http.Request)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if validate != nil {
			validate(t, r)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
}

// newProviderWithServer creates an ElevenLabs Provider whose addVoiceURL points
// to the given test server, bypassing the real ElevenLabs API.
func newProviderWithServer(t *testing.T, apiKey string, srv *httptest.Server) *Provider {
	t.Helper()
	p, err := New(apiKey)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p.addVoiceURL = srv.URL + "/v1/voices/add"
	return p
}

// ---- Connection pool tests ----

// newWSEchoServer creates an httptest.Server that upgrades to WebSocket,
// reads BOI + text messages, and responds with a single audio chunk followed
// by an isFinal marker. Suitable for pool and synthesis integration tests.
func newWSEchoServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "done")
		conn.SetReadLimit(1 << 20)

		ctx := r.Context()
		for {
			_, msg, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var tm textMessage
			if err := json.Unmarshal(msg, &tm); err != nil {
				continue
			}
			// Flush command (empty text) → send audio + isFinal, then return.
			if tm.Text == "" {
				pcm := []byte("fake-pcm-data")
				resp, _ := json.Marshal(audioResponse{
					Audio: base64.StdEncoding.EncodeToString(pcm),
				})
				_ = conn.Write(ctx, websocket.MessageText, resp)
				final, _ := json.Marshal(audioResponse{IsFinal: true})
				_ = conn.Write(ctx, websocket.MessageText, final)
				return
			}
		}
	}))
}

// newIdleWSServer creates an httptest.Server that accepts WebSocket connections
// and holds them open until the peer closes. Useful for pool-only tests where
// no actual synthesis traffic is needed.
func newIdleWSServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		// Block until the peer closes the connection.
		for {
			_, _, err := conn.Read(r.Context())
			if err != nil {
				return
			}
		}
	}))
}

// testDialFunc returns a dialFunc that connects to srv and an atomic counter
// that records how many times it was called.
func testDialFunc(t *testing.T, srv *httptest.Server) (func(ctx context.Context, url string) (*websocket.Conn, error), *atomic.Int64) {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	var count atomic.Int64
	return func(ctx context.Context, _ string) (*websocket.Conn, error) {
		count.Add(1)
		conn, _, err := websocket.Dial(ctx, wsURL, nil)
		return conn, err
	}, &count
}

func TestPoolTakeWarm_EmptyPool(t *testing.T) {
	t.Parallel()
	p, err := New("test-key")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if conn := p.takeWarm("voice-1"); conn != nil {
		t.Error("expected nil from empty pool")
	}
}

func TestPoolPutAndTake(t *testing.T) {
	t.Parallel()
	srv := newIdleWSServer(t)
	defer srv.Close()

	p, err := New("test-key")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	dial, _ := testDialFunc(t, srv)
	conn, err := dial(context.Background(), "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	if !p.putWarm("voice-1", conn) {
		t.Fatal("putWarm returned false on empty pool")
	}

	got := p.takeWarm("voice-1")
	if got == nil {
		t.Fatal("expected non-nil connection from pool")
	}

	// Pool should now be empty.
	if second := p.takeWarm("voice-1"); second != nil {
		t.Error("expected nil after taking the only connection")
	}
}

func TestPoolPutWarm_RejectsWhenFull(t *testing.T) {
	t.Parallel()
	srv := newIdleWSServer(t)
	defer srv.Close()

	p, err := New("test-key", WithPoolSize(1))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	dial, _ := testDialFunc(t, srv)

	c1, _ := dial(context.Background(), "")
	c2, _ := dial(context.Background(), "")

	if !p.putWarm("v1", c1) {
		t.Fatal("first putWarm should succeed")
	}
	if p.putWarm("v1", c2) {
		t.Error("second putWarm should fail when pool is full")
	}
	c2.Close(websocket.StatusNormalClosure, "test")
}

func TestPoolTakeWarm_EvictsStale(t *testing.T) {
	t.Parallel()
	srv := newIdleWSServer(t)
	defer srv.Close()

	p, err := New("test-key", WithMaxIdleTime(1*time.Millisecond))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	dial, _ := testDialFunc(t, srv)
	conn, _ := dial(context.Background(), "")
	p.putWarm("v1", conn)

	// Wait for the connection to become stale.
	time.Sleep(5 * time.Millisecond)

	if got := p.takeWarm("v1"); got != nil {
		t.Error("expected nil for stale connection")
		got.Close(websocket.StatusNormalClosure, "test")
	}
}

func TestPoolClose(t *testing.T) {
	t.Parallel()
	srv := newIdleWSServer(t)
	defer srv.Close()

	p, err := New("test-key", WithPoolSize(2))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	dial, _ := testDialFunc(t, srv)
	c1, _ := dial(context.Background(), "")
	c2, _ := dial(context.Background(), "")
	p.putWarm("v1", c1)
	p.putWarm("v1", c2)

	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Pool should be empty and closed.
	if got := p.takeWarm("v1"); got != nil {
		t.Error("expected nil from closed pool")
	}

	// Double close is safe.
	if err := p.Close(); err != nil {
		t.Fatalf("double Close: %v", err)
	}
}

func TestWarm(t *testing.T) {
	t.Parallel()
	srv := newIdleWSServer(t)
	defer srv.Close()

	p, err := New("test-key")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	dial, count := testDialFunc(t, srv)
	p.dialFunc = dial

	voices := []tts.VoiceProfile{
		{ID: "voice-a", Name: "A"},
		{ID: "voice-b", Name: "B"},
		{ID: "", Name: "Empty"}, // should be skipped
	}

	if err := p.Warm(context.Background(), voices...); err != nil {
		t.Fatalf("Warm: %v", err)
	}

	if got := count.Load(); got != 2 {
		t.Errorf("expected 2 dials (skipping empty ID), got %d", got)
	}

	// Both voices should have warm connections.
	if c := p.takeWarm("voice-a"); c == nil {
		t.Error("expected warm connection for voice-a")
	}
	if c := p.takeWarm("voice-b"); c == nil {
		t.Error("expected warm connection for voice-b")
	}
}

func TestDialAhead(t *testing.T) {
	t.Parallel()
	srv := newIdleWSServer(t)
	defer srv.Close()

	p, err := New("test-key")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	dial, count := testDialFunc(t, srv)
	p.dialFunc = dial

	p.dialAhead("voice-1")
	p.wg.Wait() // wait for the background goroutine

	if got := count.Load(); got != 1 {
		t.Errorf("expected 1 dial-ahead call, got %d", got)
	}

	if c := p.takeWarm("voice-1"); c == nil {
		t.Error("expected warm connection from dial-ahead")
	}
}

func TestDialAhead_SkipsWhenPoolDisabled(t *testing.T) {
	t.Parallel()

	p, err := New("test-key", WithPoolSize(0))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var count atomic.Int64
	p.dialFunc = func(_ context.Context, _ string) (*websocket.Conn, error) {
		count.Add(1)
		return nil, fmt.Errorf("should not be called")
	}

	p.dialAhead("voice-1")
	p.wg.Wait()

	if got := count.Load(); got != 0 {
		t.Errorf("expected 0 dials when pool disabled, got %d", got)
	}
}

func TestSynthesizeStream_UsesWarmConnection(t *testing.T) {
	t.Parallel()
	srv := newWSEchoServer(t)
	defer srv.Close()

	p, err := New("test-key")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	dial, count := testDialFunc(t, srv)
	p.dialFunc = dial

	// Pre-warm a connection.
	if err := p.Warm(context.Background(), tts.VoiceProfile{ID: "voice-1"}); err != nil {
		t.Fatalf("Warm: %v", err)
	}
	warmDials := count.Load()

	// SynthesizeStream should use the warm connection, not dial a new one.
	textCh := make(chan string, 1)
	textCh <- "Hello world."
	close(textCh)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	audioCh, err := p.SynthesizeStream(ctx, textCh, tts.VoiceProfile{ID: "voice-1"})
	if err != nil {
		t.Fatalf("SynthesizeStream: %v", err)
	}

	// Drain audio.
	var chunks int
	for range audioCh {
		chunks++
	}
	if chunks == 0 {
		t.Error("expected at least one audio chunk")
	}

	// Wait for dial-ahead to complete.
	p.wg.Wait()

	// The warm dial + the dial-ahead = warmDials + 1.
	// SynthesizeStream itself should NOT have triggered a fresh dial.
	finalDials := count.Load()
	dialsDuringSynth := finalDials - warmDials
	if dialsDuringSynth != 1 {
		// 1 dial = the dial-ahead only (not a fresh dial for the synthesis itself)
		t.Errorf("expected 1 dial during synthesis (dial-ahead only), got %d", dialsDuringSynth)
	}
}

func TestSynthesizeStream_FallsBackOnFreshDial(t *testing.T) {
	t.Parallel()
	srv := newWSEchoServer(t)
	defer srv.Close()

	p, err := New("test-key")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	dial, count := testDialFunc(t, srv)
	p.dialFunc = dial

	// No warm connection — should dial fresh.
	textCh := make(chan string, 1)
	textCh <- "Hello."
	close(textCh)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	audioCh, err := p.SynthesizeStream(ctx, textCh, tts.VoiceProfile{ID: "voice-1"})
	if err != nil {
		t.Fatalf("SynthesizeStream: %v", err)
	}

	// Drain audio.
	for range audioCh {
	}

	p.wg.Wait()

	// 1 fresh dial + 1 dial-ahead = 2 total.
	if got := count.Load(); got != 2 {
		t.Errorf("expected 2 dials (fresh + dial-ahead), got %d", got)
	}
}

func TestWithPoolSize(t *testing.T) {
	t.Parallel()
	p, err := New("test-key", WithPoolSize(3))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.poolSize != 3 {
		t.Errorf("expected poolSize 3, got %d", p.poolSize)
	}
}

func TestWithMaxIdleTime(t *testing.T) {
	t.Parallel()
	p, err := New("test-key", WithMaxIdleTime(10*time.Second))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.maxIdle != 10*time.Second {
		t.Errorf("expected maxIdle 10s, got %v", p.maxIdle)
	}
}

func TestWarmerInterfaceAssertion(t *testing.T) {
	t.Parallel()
	p, err := New("test-key")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Provider should satisfy tts.Warmer.
	var provider tts.Provider = p
	if _, ok := provider.(tts.Warmer); !ok {
		t.Error("Provider does not implement tts.Warmer")
	}
}
