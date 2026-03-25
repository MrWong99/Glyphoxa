package deepgram

import (
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/MrWong99/glyphoxa/pkg/provider/stt"
)

// ---- URL / query-param tests ----

func TestBuildURL_Defaults(t *testing.T) {
	p, err := New("test-key")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cfg := stt.StreamConfig{
		SampleRate: 16000,
		Channels:   1,
		Language:   "en",
	}

	rawURL, err := p.buildURL(cfg)
	if err != nil {
		t.Fatalf("buildURL: %v", err)
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	q := u.Query()

	assertEqual(t, "model", "nova-3", q.Get("model"))
	assertEqual(t, "language", "en", q.Get("language"))
	assertEqual(t, "punctuate", "true", q.Get("punctuate"))
	assertEqual(t, "interim_results", "true", q.Get("interim_results"))
	assertEqual(t, "sample_rate", "16000", q.Get("sample_rate"))
	assertEqual(t, "channels", "1", q.Get("channels"))
}

func TestBuildURL_CustomModel(t *testing.T) {
	p, err := New("key", WithModel("base"), WithLanguage("de-DE"), WithSampleRate(48000))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rawURL, err := p.buildURL(stt.StreamConfig{})
	if err != nil {
		t.Fatalf("buildURL: %v", err)
	}

	u, _ := url.Parse(rawURL)
	q := u.Query()

	assertEqual(t, "model", "base", q.Get("model"))
	assertEqual(t, "language", "de-DE", q.Get("language"))
	assertEqual(t, "sample_rate", "48000", q.Get("sample_rate"))
}

func TestBuildURL_LanguageOverridenByCfg(t *testing.T) {
	// cfg.Language should take precedence over the provider-level default.
	p, err := New("key", WithLanguage("en"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rawURL, err := p.buildURL(stt.StreamConfig{Language: "fr-FR", SampleRate: 16000})
	if err != nil {
		t.Fatalf("buildURL: %v", err)
	}

	u, _ := url.Parse(rawURL)
	assertEqual(t, "language", "fr-FR", u.Query().Get("language"))
}

func TestBuildURL_Keywords(t *testing.T) {
	p, err := New("key")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cfg := stt.StreamConfig{
		SampleRate: 16000,
		Keywords: []stt.KeywordBoost{
			{Keyword: "Eldrinax", Boost: 5},
			{Keyword: "Zorrath", Boost: 3.5},
		},
	}

	rawURL, err := p.buildURL(cfg)
	if err != nil {
		t.Fatalf("buildURL: %v", err)
	}

	u, _ := url.Parse(rawURL)
	kws := u.Query()["keywords"]
	if len(kws) != 2 {
		t.Fatalf("expected 2 keywords, got %d: %v", len(kws), kws)
	}

	// Both keywords should be present (order may vary).
	found := map[string]bool{}
	for _, kw := range kws {
		found[kw] = true
	}
	if !found["Eldrinax:5"] {
		t.Errorf("expected keyword 'Eldrinax:5', got %v", kws)
	}
	if !found["Zorrath:3.5"] {
		t.Errorf("expected keyword 'Zorrath:3.5', got %v", kws)
	}
}

func TestBuildURL_NoKeywords(t *testing.T) {
	p, err := New("key")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rawURL, err := p.buildURL(stt.StreamConfig{SampleRate: 16000})
	if err != nil {
		t.Fatalf("buildURL: %v", err)
	}

	u, _ := url.Parse(rawURL)
	if _, ok := u.Query()["keywords"]; ok {
		t.Error("expected no 'keywords' param when none provided")
	}
}

// ---- JSON parsing tests ----

func TestParseDeepgramResponse_Final(t *testing.T) {
	raw := []byte(`{
		"type": "Results",
		"is_final": true,
		"channel": {
			"alternatives": [{
				"transcript": "Hello world",
				"confidence": 0.95,
				"words": [
					{"word": "Hello", "start": 0.1, "end": 0.5, "confidence": 0.97},
					{"word": "world", "start": 0.6, "end": 1.0, "confidence": 0.93}
				]
			}]
		}
	}`)

	tr, ok := parseDeepgramResponse(raw)
	if !ok {
		t.Fatal("expected ok=true for valid Results message")
	}

	if !tr.IsFinal {
		t.Error("expected IsFinal=true")
	}
	assertEqual(t, "text", "Hello world", tr.Text)
	if tr.Confidence != 0.95 {
		t.Errorf("expected confidence 0.95, got %f", tr.Confidence)
	}
	if len(tr.Words) != 2 {
		t.Fatalf("expected 2 words, got %d", len(tr.Words))
	}
	assertEqual(t, "word[0]", "Hello", tr.Words[0].Word)
	if tr.Words[0].Start != time.Duration(0.1*float64(time.Second)) {
		t.Errorf("unexpected start: %v", tr.Words[0].Start)
	}
}

func TestParseDeepgramResponse_Partial(t *testing.T) {
	raw := []byte(`{
		"type": "Results",
		"is_final": false,
		"channel": {
			"alternatives": [{
				"transcript": "Hello",
				"confidence": 0.7,
				"words": []
			}]
		}
	}`)

	tr, ok := parseDeepgramResponse(raw)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if tr.IsFinal {
		t.Error("expected IsFinal=false for partial result")
	}
	assertEqual(t, "text", "Hello", tr.Text)
}

func TestParseDeepgramResponse_NonResultsType(t *testing.T) {
	raw := []byte(`{"type":"Metadata","request_id":"abc"}`)
	_, ok := parseDeepgramResponse(raw)
	if ok {
		t.Error("expected ok=false for non-Results message")
	}
}

func TestParseDeepgramResponse_EmptyAlternatives(t *testing.T) {
	raw := []byte(`{"type":"Results","is_final":true,"channel":{"alternatives":[]}}`)
	_, ok := parseDeepgramResponse(raw)
	if ok {
		t.Error("expected ok=false when alternatives is empty")
	}
}

func TestParseDeepgramResponse_InvalidJSON(t *testing.T) {
	_, ok := parseDeepgramResponse([]byte(`{invalid`))
	if ok {
		t.Error("expected ok=false for invalid JSON")
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
	assertEqual(t, "model", defaultModel, p.model)
	assertEqual(t, "language", defaultLanguage, p.language)
	if p.sampleRate != defaultSampleRate {
		t.Errorf("expected sampleRate %d, got %d", defaultSampleRate, p.sampleRate)
	}
}

// ---- keyword support ----

func TestSetKeywords_ReturnsErrNotSupported(t *testing.T) {
	t.Parallel()
	// SetKeywords only touches kwMu and keywords — no websocket needed.
	s := &session{}
	err := s.SetKeywords([]stt.KeywordBoost{{Keyword: "Eldrinax", Boost: 5}})
	if err == nil {
		t.Fatal("expected error from SetKeywords, got nil")
	}
	if !errors.Is(err, stt.ErrNotSupported) {
		t.Fatalf("expected errors.Is(err, stt.ErrNotSupported), got %v", err)
	}
}

// ---- session channel & SendAudio tests ----

func TestSession_Partials_Returns_Channel(t *testing.T) {
	t.Parallel()

	s := &session{
		partials: make(chan stt.Transcript, 1),
		finals:   make(chan stt.Transcript, 1),
		done:     make(chan struct{}),
	}

	ch := s.Partials()
	if ch == nil {
		t.Fatal("Partials() returned nil channel")
	}
	// Write a value and read it back.
	s.partials <- stt.Transcript{Text: "hello"}
	got := <-ch
	if got.Text != "hello" {
		t.Errorf("Partials: got %q, want %q", got.Text, "hello")
	}
}

func TestSession_Finals_Returns_Channel(t *testing.T) {
	t.Parallel()

	s := &session{
		partials: make(chan stt.Transcript, 1),
		finals:   make(chan stt.Transcript, 1),
		done:     make(chan struct{}),
	}

	ch := s.Finals()
	if ch == nil {
		t.Fatal("Finals() returned nil channel")
	}
	// Write a value and read it back.
	s.finals <- stt.Transcript{Text: "final text", IsFinal: true}
	got := <-ch
	if got.Text != "final text" {
		t.Errorf("Finals: got %q, want %q", got.Text, "final text")
	}
	if !got.IsFinal {
		t.Error("Finals: expected IsFinal=true")
	}
}

func TestSendAudio_WhenOpen(t *testing.T) {
	t.Parallel()

	s := &session{
		audio: make(chan []byte, 2),
		done:  make(chan struct{}),
	}

	chunk := []byte{0x01, 0x02, 0x03}
	if err := s.SendAudio(chunk); err != nil {
		t.Fatalf("SendAudio: unexpected error: %v", err)
	}
	// Read back the chunk.
	got := <-s.audio
	if len(got) != 3 || got[0] != 0x01 {
		t.Errorf("SendAudio: got %v, want %v", got, chunk)
	}
}

func TestSendAudio_WhenClosed(t *testing.T) {
	t.Parallel()

	s := &session{
		audio: make(chan []byte, 2),
		done:  make(chan struct{}),
	}
	close(s.done) // simulate closed session

	err := s.SendAudio([]byte{0x01})
	if err == nil {
		t.Fatal("SendAudio after close: expected error, got nil")
	}
}

func TestSendAudio_BlocksUntilClosed(t *testing.T) {
	t.Parallel()

	// Unbuffered audio channel — will block.
	s := &session{
		audio: make(chan []byte),
		done:  make(chan struct{}),
	}

	// Close done so the blocking select branch fires.
	close(s.done)
	err := s.SendAudio([]byte{0x01})
	if err == nil {
		t.Fatal("expected error when session is closed and audio channel full")
	}
}

// ---- buildURL additional edge cases ----

func TestBuildURL_ZeroChannels(t *testing.T) {
	t.Parallel()

	p, err := New("key")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rawURL, err := p.buildURL(stt.StreamConfig{SampleRate: 16000, Channels: 0})
	if err != nil {
		t.Fatalf("buildURL: %v", err)
	}

	u, _ := url.Parse(rawURL)
	// channels param should not be present when Channels == 0
	if _, ok := u.Query()["channels"]; ok {
		t.Error("expected no 'channels' param when Channels is 0")
	}
}

func TestBuildURL_SampleRateOverrideByCfg(t *testing.T) {
	t.Parallel()

	p, err := New("key", WithSampleRate(8000))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// cfg.SampleRate should override provider-level default.
	rawURL, err := p.buildURL(stt.StreamConfig{SampleRate: 44100})
	if err != nil {
		t.Fatalf("buildURL: %v", err)
	}

	u, _ := url.Parse(rawURL)
	if got := u.Query().Get("sample_rate"); got != "44100" {
		t.Errorf("sample_rate = %q, want %q", got, "44100")
	}
}

func TestBuildURL_ProviderDefaultSampleRate(t *testing.T) {
	t.Parallel()

	p, err := New("key", WithSampleRate(22050))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// cfg.SampleRate is 0, so provider's 22050 should be used.
	rawURL, err := p.buildURL(stt.StreamConfig{SampleRate: 0})
	if err != nil {
		t.Fatalf("buildURL: %v", err)
	}

	u, _ := url.Parse(rawURL)
	if got := u.Query().Get("sample_rate"); got != "22050" {
		t.Errorf("sample_rate = %q, want %q", got, "22050")
	}
}

// ---- New with all options ----

func TestNew_AllOptions(t *testing.T) {
	t.Parallel()

	p, err := New("key",
		WithModel("enhanced"),
		WithLanguage("ja"),
		WithSampleRate(48000),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.model != "enhanced" {
		t.Errorf("model = %q, want %q", p.model, "enhanced")
	}
	if p.language != "ja" {
		t.Errorf("language = %q, want %q", p.language, "ja")
	}
	if p.sampleRate != 48000 {
		t.Errorf("sampleRate = %d, want %d", p.sampleRate, 48000)
	}
}

// ---- parseDeepgramResponse edge cases ----

func TestParseDeepgramResponse_MultipleWords(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"type": "Results",
		"is_final": true,
		"channel": {
			"alternatives": [{
				"transcript": "The quick brown fox",
				"confidence": 0.88,
				"words": [
					{"word": "The", "start": 0.0, "end": 0.2, "confidence": 0.9},
					{"word": "quick", "start": 0.3, "end": 0.5, "confidence": 0.85},
					{"word": "brown", "start": 0.6, "end": 0.8, "confidence": 0.9},
					{"word": "fox", "start": 0.9, "end": 1.1, "confidence": 0.88}
				]
			}]
		}
	}`)

	tr, ok := parseDeepgramResponse(raw)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if len(tr.Words) != 4 {
		t.Fatalf("expected 4 words, got %d", len(tr.Words))
	}
	if tr.Words[3].Word != "fox" {
		t.Errorf("word[3] = %q, want %q", tr.Words[3].Word, "fox")
	}
	if tr.Words[1].End != time.Duration(0.5*float64(time.Second)) {
		t.Errorf("word[1].End = %v, unexpected", tr.Words[1].End)
	}
}

func TestParseDeepgramResponse_NoWords(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"type": "Results",
		"is_final": true,
		"channel": {
			"alternatives": [{
				"transcript": "hello",
				"confidence": 0.99
			}]
		}
	}`)

	tr, ok := parseDeepgramResponse(raw)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if len(tr.Words) != 0 {
		t.Errorf("expected 0 words, got %d", len(tr.Words))
	}
	if tr.Text != "hello" {
		t.Errorf("text = %q, want %q", tr.Text, "hello")
	}
}

// ---- helpers ----

func assertEqual(t *testing.T, label, want, got string) {
	t.Helper()
	if want != got {
		t.Errorf("%s: want %q, got %q", label, want, got)
	}
}
