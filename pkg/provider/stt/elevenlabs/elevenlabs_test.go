package elevenlabs

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"testing"
	"time"

	"github.com/MrWong99/glyphoxa/pkg/provider/stt"
)

// ---- Constructor tests ----

func TestNew(t *testing.T) {
	t.Parallel()

	t.Run("EmptyAPIKey", func(t *testing.T) {
		t.Parallel()
		_, err := New("")
		if err == nil {
			t.Error("expected error for empty API key")
		}
	})

	t.Run("Defaults", func(t *testing.T) {
		t.Parallel()
		p, err := New("key")
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		assertEqual(t, "model", defaultModel, p.model)
		assertEqual(t, "language", defaultLanguage, p.language)
		if p.sampleRate != defaultSampleRate {
			t.Errorf("expected sampleRate %d, got %d", defaultSampleRate, p.sampleRate)
		}
		assertEqual(t, "baseURL", defaultBaseURL, p.baseURL)
	})

	t.Run("WithOptions", func(t *testing.T) {
		t.Parallel()
		p, err := New("key",
			WithModel("custom-model"),
			WithLanguage("de"),
			WithSampleRate(48000),
			WithBaseURL("wss://test.example.com/stt"),
		)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		assertEqual(t, "model", "custom-model", p.model)
		assertEqual(t, "language", "de", p.language)
		if p.sampleRate != 48000 {
			t.Errorf("expected sampleRate 48000, got %d", p.sampleRate)
		}
		assertEqual(t, "baseURL", "wss://test.example.com/stt", p.baseURL)
	})
}

// ---- URL builder tests ----

func TestBuildURL(t *testing.T) {
	t.Parallel()

	t.Run("Defaults", func(t *testing.T) {
		t.Parallel()
		p, err := New("test-key")
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		rawURL, err := p.buildURL(stt.StreamConfig{
			SampleRate: 16000,
			Language:   "en",
		})
		if err != nil {
			t.Fatalf("buildURL: %v", err)
		}

		u, err := url.Parse(rawURL)
		if err != nil {
			t.Fatalf("parse URL: %v", err)
		}
		q := u.Query()

		assertEqual(t, "model_id", "scribe_v2_realtime", q.Get("model_id"))
		assertEqual(t, "language_code", "en", q.Get("language_code"))
		assertEqual(t, "audio_format", "pcm_16000", q.Get("audio_format"))
		assertEqual(t, "include_timestamps", "true", q.Get("include_timestamps"))
		assertEqual(t, "commit_strategy", "manual", q.Get("commit_strategy"))
	})

	t.Run("LanguageOverriddenByCfg", func(t *testing.T) {
		t.Parallel()
		p, err := New("key", WithLanguage("en"))
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		rawURL, err := p.buildURL(stt.StreamConfig{Language: "fr", SampleRate: 16000})
		if err != nil {
			t.Fatalf("buildURL: %v", err)
		}

		u, _ := url.Parse(rawURL)
		assertEqual(t, "language_code", "fr", u.Query().Get("language_code"))
	})

	t.Run("CustomModel", func(t *testing.T) {
		t.Parallel()
		p, err := New("key", WithModel("scribe_v1"), WithSampleRate(48000))
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		rawURL, err := p.buildURL(stt.StreamConfig{})
		if err != nil {
			t.Fatalf("buildURL: %v", err)
		}

		u, _ := url.Parse(rawURL)
		q := u.Query()
		assertEqual(t, "model_id", "scribe_v1", q.Get("model_id"))
		assertEqual(t, "audio_format", "pcm_48000", q.Get("audio_format"))
	})

	t.Run("LanguageNormalization", func(t *testing.T) {
		t.Parallel()
		p, err := New("key")
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		rawURL, err := p.buildURL(stt.StreamConfig{Language: "en-US", SampleRate: 16000})
		if err != nil {
			t.Fatalf("buildURL: %v", err)
		}

		u, _ := url.Parse(rawURL)
		assertEqual(t, "language_code", "en", u.Query().Get("language_code"))
	})

	t.Run("CustomBaseURL", func(t *testing.T) {
		t.Parallel()
		p, err := New("key", WithBaseURL("wss://custom.host/v1/stt"))
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		rawURL, err := p.buildURL(stt.StreamConfig{SampleRate: 16000})
		if err != nil {
			t.Fatalf("buildURL: %v", err)
		}

		u, err := url.Parse(rawURL)
		if err != nil {
			t.Fatalf("parse URL: %v", err)
		}
		assertEqual(t, "host", "custom.host", u.Host)
	})
}

// ---- Response parser tests ----

func TestParseResponse(t *testing.T) {
	t.Parallel()

	t.Run("PartialTranscript", func(t *testing.T) {
		t.Parallel()
		raw := []byte(`{"message_type":"partial_transcript","text":"Hello wor"}`)

		tr, ok := parseResponse(raw)
		if !ok {
			t.Fatal("expected ok=true for partial_transcript")
		}
		if tr.IsFinal {
			t.Error("expected IsFinal=false for partial")
		}
		assertEqual(t, "text", "Hello wor", tr.Text)
	})

	t.Run("CommittedTranscript", func(t *testing.T) {
		t.Parallel()
		raw := []byte(`{"message_type":"committed_transcript","text":"Hello world","language_code":"en"}`)

		tr, ok := parseResponse(raw)
		if !ok {
			t.Fatal("expected ok=true for committed_transcript")
		}
		if !tr.IsFinal {
			t.Error("expected IsFinal=true for committed transcript")
		}
		assertEqual(t, "text", "Hello world", tr.Text)
		if len(tr.Words) != 0 {
			t.Errorf("expected 0 words for committed_transcript without timestamps, got %d", len(tr.Words))
		}
	})

	t.Run("CommittedTranscriptWithTimestamps", func(t *testing.T) {
		t.Parallel()
		raw := []byte(`{
			"message_type":"committed_transcript_with_timestamps",
			"text":"Hello world",
			"language_code":"en",
			"words":[
				{"text":"Hello","start":0.1,"end":0.5,"type":"word","speaker_id":"0","logprob":-0.05},
				{"text":" ","start":0.5,"end":0.6,"type":"spacing","speaker_id":"0","logprob":0},
				{"text":"world","start":0.6,"end":1.0,"type":"word","speaker_id":"0","logprob":-0.1}
			]
		}`)

		tr, ok := parseResponse(raw)
		if !ok {
			t.Fatal("expected ok=true")
		}
		if !tr.IsFinal {
			t.Error("expected IsFinal=true")
		}
		assertEqual(t, "text", "Hello world", tr.Text)
		if len(tr.Words) != 2 {
			t.Fatalf("expected 2 words (spacing filtered), got %d", len(tr.Words))
		}
		assertEqual(t, "word[0]", "Hello", tr.Words[0].Word)
		assertEqual(t, "word[1]", "world", tr.Words[1].Word)
	})

	t.Run("WordFiltering", func(t *testing.T) {
		t.Parallel()
		raw := []byte(`{
			"message_type":"committed_transcript_with_timestamps",
			"text":"A B",
			"words":[
				{"text":"A","start":0,"end":0.2,"type":"word","logprob":-0.01},
				{"text":" ","start":0.2,"end":0.3,"type":"spacing","logprob":0},
				{"text":"B","start":0.3,"end":0.5,"type":"word","logprob":-0.02}
			]
		}`)

		tr, ok := parseResponse(raw)
		if !ok {
			t.Fatal("expected ok=true")
		}
		if len(tr.Words) != 2 {
			t.Fatalf("expected 2 words (spacing filtered out), got %d", len(tr.Words))
		}
	})

	t.Run("ConfidenceMapping", func(t *testing.T) {
		t.Parallel()
		raw := []byte(`{
			"message_type":"committed_transcript_with_timestamps",
			"text":"test",
			"words":[{"text":"test","start":0,"end":0.5,"type":"word","logprob":-0.1}]
		}`)

		tr, ok := parseResponse(raw)
		if !ok {
			t.Fatal("expected ok=true")
		}
		if len(tr.Words) != 1 {
			t.Fatalf("expected 1 word, got %d", len(tr.Words))
		}
		expected := math.Exp(-0.1)
		if math.Abs(tr.Words[0].Confidence-expected) > 1e-9 {
			t.Errorf("expected confidence %f (math.Exp(-0.1)), got %f", expected, tr.Words[0].Confidence)
		}
	})

	t.Run("WordTimestamps", func(t *testing.T) {
		t.Parallel()
		raw := []byte(`{
			"message_type":"committed_transcript_with_timestamps",
			"text":"Hi",
			"words":[{"text":"Hi","start":0.1,"end":0.5,"type":"word","logprob":0}]
		}`)

		tr, ok := parseResponse(raw)
		if !ok {
			t.Fatal("expected ok=true")
		}
		wantStart := time.Duration(0.1 * float64(time.Second))
		wantEnd := time.Duration(0.5 * float64(time.Second))
		if tr.Words[0].Start != wantStart {
			t.Errorf("expected start %v, got %v", wantStart, tr.Words[0].Start)
		}
		if tr.Words[0].End != wantEnd {
			t.Errorf("expected end %v, got %v", wantEnd, tr.Words[0].End)
		}
	})

	t.Run("SessionStarted", func(t *testing.T) {
		t.Parallel()
		raw := []byte(`{"message_type":"session_started"}`)
		_, ok := parseResponse(raw)
		if ok {
			t.Error("expected ok=false for session_started")
		}
	})

	t.Run("ErrorMessage", func(t *testing.T) {
		t.Parallel()
		raw := []byte(`{"message_type":"transcription_error","error":"internal error"}`)
		_, ok := parseResponse(raw)
		if ok {
			t.Error("expected ok=false for error message")
		}
	})

	t.Run("FatalErrorMessage", func(t *testing.T) {
		t.Parallel()
		raw := []byte(`{"message_type":"auth_error","error":"invalid key"}`)
		_, ok := parseResponse(raw)
		if ok {
			t.Error("expected ok=false for fatal error message")
		}
	})

	t.Run("InvalidJSON", func(t *testing.T) {
		t.Parallel()
		_, ok := parseResponse([]byte(`{invalid`))
		if ok {
			t.Error("expected ok=false for invalid JSON")
		}
	})

	t.Run("UnknownMessageType", func(t *testing.T) {
		t.Parallel()
		raw := []byte(`{"message_type":"unknown_event","data":"stuff"}`)
		_, ok := parseResponse(raw)
		if ok {
			t.Error("expected ok=false for unknown message type")
		}
	})
}

// ---- Audio chunk message tests ----

func TestAudioChunkMessage(t *testing.T) {
	t.Parallel()

	t.Run("Normal", func(t *testing.T) {
		t.Parallel()
		pcm := []byte{0x01, 0x02, 0x03, 0x04}
		msg := audioChunkMessage{
			MessageType: "input_audio_chunk",
			AudioBase64: base64.StdEncoding.EncodeToString(pcm),
			SampleRate:  16000,
		}

		data, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		var parsed map[string]any
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}

		assertEqual(t, "message_type", "input_audio_chunk", parsed["message_type"].(string))
		assertEqual(t, "audio_base_64", base64.StdEncoding.EncodeToString(pcm), parsed["audio_base_64"].(string))
		if sr := parsed["sample_rate"].(float64); sr != 16000 {
			t.Errorf("expected sample_rate 16000, got %v", sr)
		}
		// commit should be omitted when false.
		if _, exists := parsed["commit"]; exists {
			t.Error("expected commit field to be omitted when false")
		}
	})

	t.Run("WithCommit", func(t *testing.T) {
		t.Parallel()
		msg := audioChunkMessage{
			MessageType: "input_audio_chunk",
			AudioBase64: "",
			Commit:      true,
			SampleRate:  16000,
		}

		data, err := json.Marshal(msg)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		var parsed map[string]any
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}

		commitVal, exists := parsed["commit"]
		if !exists {
			t.Fatal("expected commit field to be present")
		}
		if commitVal != true {
			t.Errorf("expected commit=true, got %v", commitVal)
		}
	})
}

// ---- SetKeywords test ----

func TestSetKeywords_ReturnsErrNotSupported(t *testing.T) {
	t.Parallel()
	s := &session{}
	err := s.SetKeywords([]stt.KeywordBoost{{Keyword: "Eldrinax", Boost: 5}})
	if err == nil {
		t.Fatal("expected error from SetKeywords, got nil")
	}
	if !errors.Is(err, stt.ErrNotSupported) {
		t.Fatalf("expected errors.Is(err, stt.ErrNotSupported), got %v", err)
	}
}

// ---- helpers ----

func assertEqual(t *testing.T, label, want, got string) {
	t.Helper()
	if want != got {
		t.Errorf("%s: want %q, got %q", label, want, got)
	}
}
