package rpc

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/groq"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// fakeVoiceStore is an in-memory voiceStore: it returns canned per-component
// provider configs and a deployment config so the VoiceServer's key-resolution
// is unit-tested keyless and offline.
type fakeVoiceStore struct {
	configs map[storage.Component]storage.ProviderConfig
	dep     *storage.DeploymentConfig
}

func (f *fakeVoiceStore) GetProviderConfigByComponent(_ context.Context, _ uuid.UUID, c storage.Component) (storage.ProviderConfig, error) {
	cfg, ok := f.configs[c]
	if !ok {
		return storage.ProviderConfig{}, storage.ErrNotFound
	}
	return cfg, nil
}

func (f *fakeVoiceStore) GetDeploymentConfig(_ context.Context, _ uuid.UUID) (storage.DeploymentConfig, error) {
	if f.dep == nil {
		return storage.DeploymentConfig{}, storage.ErrNotFound
	}
	return *f.dep, nil
}

// fakeLister is a tts.VoiceLister returning a canned catalog and recording the
// api key it was built with, so a test can assert the resolved BYOK key flowed
// through.
type fakeLister struct {
	voices []tts.Voice
	err    error
}

func (f *fakeLister) ListVoices(context.Context) ([]tts.Voice, error) {
	return f.voices, f.err
}

// fakeSynth is a tts.Synthesizer that streams canned PCM chunks.
type fakeSynth struct {
	chunks []tts.AudioChunk
	err    error
}

func (f *fakeSynth) Synthesize(_ context.Context, _ tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	if f.err != nil {
		return nil, f.err
	}
	ch := make(chan tts.AudioChunk)
	go func() {
		defer close(ch)
		for _, c := range f.chunks {
			ch <- c
		}
	}()
	return ch, nil
}

func (f *fakeSynth) AudioMarkupPrompt(tts.Voice) string { return "x" }

func voiceTestCipher(t *testing.T) *crypto.Cipher {
	t.Helper()
	c, err := crypto.New(make([]byte, 32))
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	return c
}

// savedConfig builds a provider_config holding a real saved key sealed under
// cipher, so resolution decrypts it.
func savedConfig(t *testing.T, cipher *crypto.Cipher, comp storage.Component, provider, secret string) storage.ProviderConfig {
	t.Helper()
	ct, err := cipher.Seal([]byte(secret))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return storage.ProviderConfig{
		Component: comp, Provider: provider,
		CredentialsCiphertext: ct, CredentialsLast4: crypto.Last4(secret),
	}
}

func tenantCtx() context.Context {
	return auth.WithTenant(context.Background(), uuid.New())
}

func TestListModels_GroqAllowlist(t *testing.T) {
	t.Parallel()
	srv := NewVoiceServer(&fakeVoiceStore{}, nil, nil)
	resp, err := srv.ListModels(tenantCtx(), connect.NewRequest(&managementv1.ListModelsRequest{Provider: "groq"}))
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	got := resp.Msg.GetModels()
	if len(got) != len(groq.Models) {
		t.Fatalf("models = %v, want %v", got, groq.Models)
	}
	if got[0] != groq.DefaultModel {
		t.Errorf("first model = %q, want default %q", got[0], groq.DefaultModel)
	}
}

func TestListModels_UnknownProvider(t *testing.T) {
	t.Parallel()
	srv := NewVoiceServer(&fakeVoiceStore{}, nil, nil)
	_, err := srv.ListModels(tenantCtx(), connect.NewRequest(&managementv1.ListModelsRequest{Provider: "openai"}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("unknown provider code = %v, want InvalidArgument", connect.CodeOf(err))
	}
}

func TestListVoices_MapsAdapterVoicesWithDecryptedKey(t *testing.T) {
	t.Parallel()
	cipher := voiceTestCipher(t)
	store := &fakeVoiceStore{configs: map[storage.Component]storage.ProviderConfig{
		storage.ComponentTTS: savedConfig(t, cipher, storage.ComponentTTS, "elevenlabs", "real-eleven-key"),
	}}
	srv := NewVoiceServer(store, cipher, nil)

	var gotKey string
	srv.newLister = func(apiKey string) tts.VoiceLister {
		gotKey = apiKey
		return &fakeLister{voices: []tts.Voice{
			{ProviderID: "elevenlabs", VoiceID: "v-marcus", Name: "Marcus", Language: "en"},
			{ProviderID: "elevenlabs", VoiceID: "v-aria", Name: "Aria"},
		}}
	}

	resp, err := srv.ListVoices(tenantCtx(), connect.NewRequest(&managementv1.ListVoicesRequest{}))
	if err != nil {
		t.Fatalf("ListVoices: %v", err)
	}
	if gotKey != "real-eleven-key" {
		t.Errorf("lister built with key %q, want decrypted real-eleven-key", gotKey)
	}
	voices := resp.Msg.GetVoices()
	if len(voices) != 2 {
		t.Fatalf("voices = %d, want 2", len(voices))
	}
	if voices[0].GetVoiceId() != "v-marcus" || voices[0].GetName() != "Marcus" {
		t.Errorf("voice[0] = %+v", voices[0])
	}
	if voices[0].GetLabel() != "ElevenLabs · Marcus" {
		t.Errorf("voice[0] label = %q, want 'ElevenLabs · Marcus'", voices[0].GetLabel())
	}
}

func TestListVoices_EnvFallbackWhenNoSavedKey(t *testing.T) {
	t.Parallel()
	// Env-placeholder config -> "" key (the adapter reads its own env var).
	store := &fakeVoiceStore{configs: map[storage.Component]storage.ProviderConfig{
		storage.ComponentTTS: {Component: storage.ComponentTTS, Provider: "elevenlabs", CredentialsLast4: "env"},
	}}
	srv := NewVoiceServer(store, voiceTestCipher(t), nil)

	var gotKey = "unset"
	srv.newLister = func(apiKey string) tts.VoiceLister {
		gotKey = apiKey
		return &fakeLister{}
	}
	if _, err := srv.ListVoices(tenantCtx(), connect.NewRequest(&managementv1.ListVoicesRequest{})); err != nil {
		t.Fatalf("ListVoices: %v", err)
	}
	if gotKey != "" {
		t.Errorf("env-placeholder should resolve to empty key, got %q", gotKey)
	}
}

func TestPreviewVoice_ReturnsPlayableWav(t *testing.T) {
	t.Parallel()
	store := &fakeVoiceStore{}
	srv := NewVoiceServer(store, voiceTestCipher(t), nil)
	srv.newSynth = func(string) tts.Synthesizer {
		return &fakeSynth{chunks: []tts.AudioChunk{
			{PCM: make([]byte, 480), SampleRate: 24000, Channels: 1},
			{PCM: make([]byte, 480), SampleRate: 24000, Channels: 1},
		}}
	}

	resp, err := srv.PreviewVoice(tenantCtx(), connect.NewRequest(&managementv1.PreviewVoiceRequest{VoiceId: "v-marcus"}))
	if err != nil {
		t.Fatalf("PreviewVoice: %v", err)
	}
	audio := resp.Msg.GetAudio()
	if len(audio) < 44 || string(audio[0:4]) != "RIFF" || string(audio[8:12]) != "WAVE" {
		t.Fatalf("audio is not a WAV container (len=%d)", len(audio))
	}
	// 44-byte header + 960 PCM bytes.
	if len(audio) != 44+960 {
		t.Errorf("wav length = %d, want %d", len(audio), 44+960)
	}
	if resp.Msg.GetMimeType() != "audio/wav" {
		t.Errorf("mime = %q, want audio/wav", resp.Msg.GetMimeType())
	}
	if resp.Msg.GetSampleRate() != 24000 || resp.Msg.GetChannels() != 1 {
		t.Errorf("rate/channels = %d/%d", resp.Msg.GetSampleRate(), resp.Msg.GetChannels())
	}
}

func TestPreviewVoice_MissingVoiceID(t *testing.T) {
	t.Parallel()
	srv := NewVoiceServer(&fakeVoiceStore{}, voiceTestCipher(t), nil)
	_, err := srv.PreviewVoice(tenantCtx(), connect.NewRequest(&managementv1.PreviewVoiceRequest{}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("missing voice_id code = %v, want InvalidArgument", connect.CodeOf(err))
	}
}

func TestGetProviderHealth_AllHealthyResolvesBotTag(t *testing.T) {
	t.Parallel()
	cipher := voiceTestCipher(t)
	store := &fakeVoiceStore{
		configs: map[storage.Component]storage.ProviderConfig{
			storage.ComponentLLM: savedConfig(t, cipher, storage.ComponentLLM, "groq", "groq-key"),
			storage.ComponentTTS: savedConfig(t, cipher, storage.ComponentTTS, "elevenlabs", "eleven-key"),
		},
		dep: &storage.DeploymentConfig{
			DiscordBotTokenLast4: "tok9", DiscordBotTokenCiphertext: mustSeal(t, cipher, "bot-token"),
		},
	}
	srv := NewVoiceServer(store, cipher, nil)
	srv.newLister = func(string) tts.VoiceLister { return &fakeLister{} }
	srv.pingLLM = func(context.Context, string) error { return nil }
	srv.botTag = func(context.Context, string) (string, error) { return "Glyphoxa#4823", nil }

	resp, err := srv.GetProviderHealth(tenantCtx(), connect.NewRequest(&managementv1.GetProviderHealthRequest{}))
	if err != nil {
		t.Fatalf("GetProviderHealth: %v", err)
	}
	byProvider := map[string]*managementv1.ProviderHealth{}
	for _, p := range resp.Msg.GetProviders() {
		byProvider[p.GetProvider()] = p
	}
	for _, prov := range []string{"groq", "elevenlabs", "discord"} {
		p := byProvider[prov]
		if p == nil || p.GetStatus() != managementv1.HealthStatus_HEALTH_STATUS_HEALTHY {
			t.Errorf("%s not healthy: %+v", prov, p)
		}
	}
	if byProvider["discord"].GetBotTag() != "Glyphoxa#4823" {
		t.Errorf("discord bot tag = %q, want Glyphoxa#4823", byProvider["discord"].GetBotTag())
	}
}

func TestGetProviderHealth_DegradedOnFailures(t *testing.T) {
	t.Parallel()
	cipher := voiceTestCipher(t)
	store := &fakeVoiceStore{
		configs: map[storage.Component]storage.ProviderConfig{
			storage.ComponentLLM: savedConfig(t, cipher, storage.ComponentLLM, "groq", "groq-key"),
			storage.ComponentTTS: savedConfig(t, cipher, storage.ComponentTTS, "elevenlabs", "eleven-key"),
		},
		dep: &storage.DeploymentConfig{
			DiscordBotTokenLast4: "tok9", DiscordBotTokenCiphertext: mustSeal(t, cipher, "bot-token"),
		},
	}
	srv := NewVoiceServer(store, cipher, nil)
	srv.newLister = func(string) tts.VoiceLister { return &fakeLister{err: errors.New("401 unauthorized")} }
	srv.pingLLM = func(context.Context, string) error { return errors.New("groq down") }
	srv.botTag = func(context.Context, string) (string, error) { return "", errors.New("bad token") }

	resp, err := srv.GetProviderHealth(tenantCtx(), connect.NewRequest(&managementv1.GetProviderHealthRequest{}))
	if err != nil {
		t.Fatalf("GetProviderHealth: %v", err)
	}
	for _, p := range resp.Msg.GetProviders() {
		if p.GetStatus() != managementv1.HealthStatus_HEALTH_STATUS_DEGRADED {
			t.Errorf("%s should be degraded: %+v", p.GetProvider(), p)
		}
	}
}

func mustSeal(t *testing.T, c *crypto.Cipher, s string) []byte {
	t.Helper()
	ct, err := c.Seal([]byte(s))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return ct
}

// unauthenticated requests (no tenant in ctx) are rejected.
func TestVoiceRPCs_RequireTenant(t *testing.T) {
	t.Parallel()
	srv := NewVoiceServer(&fakeVoiceStore{}, voiceTestCipher(t), nil)
	if _, err := srv.ListVoices(context.Background(), connect.NewRequest(&managementv1.ListVoicesRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("ListVoices without tenant code = %v, want Unauthenticated", connect.CodeOf(err))
	}
}
