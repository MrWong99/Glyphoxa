package elevenlabs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
