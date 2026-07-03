package rpc

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

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

// healthyStore returns a fakeVoiceStore with a saved key for every component
// plus a deployment bot token, so all three health checks reach their seams.
func healthyStore(t *testing.T, cipher *crypto.Cipher) *fakeVoiceStore {
	t.Helper()
	return &fakeVoiceStore{
		configs: map[storage.Component]storage.ProviderConfig{
			storage.ComponentLLM: savedConfig(t, cipher, storage.ComponentLLM, "groq", "groq-key"),
			storage.ComponentTTS: savedConfig(t, cipher, storage.ComponentTTS, "elevenlabs", "eleven-key"),
		},
		dep: &storage.DeploymentConfig{
			DiscordBotTokenLast4: "tok9", DiscordBotTokenCiphertext: mustSeal(t, cipher, "bot-token"),
		},
	}
}

// TestGetProviderHealth_ChecksRunConcurrently pins #150: the three provider
// checks run in parallel, so the RPC's worst case is the slowest single check
// (~checkDelay), not the sum (~3×checkDelay). Sequential checks would take
// ~360ms and fail the <300ms bound.
func TestGetProviderHealth_ChecksRunConcurrently(t *testing.T) {
	t.Parallel()
	const checkDelay = 120 * time.Millisecond

	cipher := voiceTestCipher(t)
	srv := NewVoiceServer(healthyStore(t, cipher), cipher, nil)
	srv.newLister = func(string) tts.VoiceLister { return &slowLister{delay: checkDelay} }
	srv.pingLLM = func(ctx context.Context, _ string) error {
		sleepCtx(ctx, checkDelay)
		return nil
	}
	srv.botTag = func(ctx context.Context, _ string) (string, error) {
		sleepCtx(ctx, checkDelay)
		return "Glyphoxa#4823", nil
	}

	start := time.Now()
	resp, err := srv.GetProviderHealth(tenantCtx(), connect.NewRequest(&managementv1.GetProviderHealthRequest{}))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("GetProviderHealth: %v", err)
	}
	if got := len(resp.Msg.GetProviders()); got != 3 {
		t.Fatalf("providers = %d, want 3", got)
	}
	if elapsed < checkDelay {
		t.Errorf("elapsed %v < one check's delay %v — checks were skipped, not run", elapsed, checkDelay)
	}
	if elapsed >= 300*time.Millisecond {
		t.Errorf("elapsed %v suggests sequential checks; concurrent should be ~%v", elapsed, checkDelay)
	}
}

// slowLister blocks ~delay before returning an empty healthy catalog.
type slowLister struct{ delay time.Duration }

func (s *slowLister) ListVoices(ctx context.Context) ([]tts.Voice, error) {
	sleepCtx(ctx, s.delay)
	return nil, nil
}

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}

// countingHealthSeams wires all three provider seams to atomic counters so a
// test can pin how often the vendors were actually touched.
type countingHealthSeams struct {
	lister, llm, discord atomic.Int64
}

func (c *countingHealthSeams) wire(srv *VoiceServer) {
	srv.newLister = func(string) tts.VoiceLister {
		c.lister.Add(1)
		return &fakeLister{}
	}
	srv.pingLLM = func(context.Context, string) error {
		c.llm.Add(1)
		return nil
	}
	srv.botTag = func(context.Context, string) (string, error) {
		c.discord.Add(1)
		return "Glyphoxa#4823", nil
	}
}

func (c *countingHealthSeams) counts() [3]int64 {
	return [3]int64{c.lister.Load(), c.llm.Load(), c.discord.Load()}
}

// TestGetProviderHealth_CachedWithinTTL pins #150's server-side TTL cache: two
// health calls within the TTL touch each vendor exactly once (the second is
// served from cache), and after the TTL expires the vendors are probed again.
func TestGetProviderHealth_CachedWithinTTL(t *testing.T) {
	t.Parallel()
	cipher := voiceTestCipher(t)
	srv := NewVoiceServer(healthyStore(t, cipher), cipher, nil)
	var seams countingHealthSeams
	seams.wire(srv)

	// A controllable clock so the test advances past the TTL without sleeping.
	now := time.Now()
	srv.now = func() time.Time { return now }

	ctx := tenantCtx() // ONE tenant: the cache is keyed per tenant
	req := func() *managementv1.GetProviderHealthResponse {
		t.Helper()
		resp, err := srv.GetProviderHealth(ctx, connect.NewRequest(&managementv1.GetProviderHealthRequest{}))
		if err != nil {
			t.Fatalf("GetProviderHealth: %v", err)
		}
		return resp.Msg
	}

	first := req()
	if got := seams.counts(); got != [3]int64{1, 1, 1} {
		t.Fatalf("counts after first call = %v, want each vendor touched once", got)
	}

	second := req()
	if got := seams.counts(); got != [3]int64{1, 1, 1} {
		t.Errorf("counts after second call within TTL = %v, want still 1 each (served from cache)", got)
	}
	if len(second.GetProviders()) != len(first.GetProviders()) {
		t.Errorf("cached response shape differs: %v vs %v", second, first)
	}

	// Advance past the TTL: the next call probes the vendors again.
	now = now.Add(healthCacheTTL + time.Second)
	req()
	if got := seams.counts(); got != [3]int64{2, 2, 2} {
		t.Errorf("counts after TTL expiry = %v, want each vendor probed again (2)", got)
	}
}

// fakeSessions is an activeSessionSource whose Snapshot reports a live voice
// session iff active is true.
type fakeSessions struct{ active bool }

func (f *fakeSessions) Snapshot() (storage.VoiceSession, bool) {
	return storage.VoiceSession{}, f.active
}

// TestGetProviderHealth_ActiveSessionSkipsDiscordProbe pins #150: while a voice
// session is active, the Discord check short-circuits to healthy WITHOUT
// touching Discord — the live session (on the same token) IS the health signal,
// and a probe would race its reconnects for the per-token IDENTIFY budget.
func TestGetProviderHealth_ActiveSessionSkipsDiscordProbe(t *testing.T) {
	t.Parallel()
	cipher := voiceTestCipher(t)
	srv := NewVoiceServer(healthyStore(t, cipher), cipher, nil)
	var seams countingHealthSeams
	seams.wire(srv)
	srv.SetSessions(&fakeSessions{active: true})

	resp, err := srv.GetProviderHealth(tenantCtx(), connect.NewRequest(&managementv1.GetProviderHealthRequest{}))
	if err != nil {
		t.Fatalf("GetProviderHealth: %v", err)
	}
	if got := seams.discord.Load(); got != 0 {
		t.Errorf("discord probed %d times during an active session, want 0", got)
	}
	for _, p := range resp.Msg.GetProviders() {
		if p.GetProvider() == "discord" && p.GetStatus() != managementv1.HealthStatus_HEALTH_STATUS_HEALTHY {
			t.Errorf("discord should report healthy during an active session: %+v", p)
		}
	}
	// The other two providers are still probed for real.
	if got := seams.llm.Load(); got != 1 {
		t.Errorf("llm probes = %d, want 1", got)
	}
	if got := seams.lister.Load(); got != 1 {
		t.Errorf("tts probes = %d, want 1", got)
	}
}

// blockingVoiceStore blocks every read until release is closed, IGNORING ctx —
// the worst-case wedged dependency. After release it delegates to inner.
type blockingVoiceStore struct {
	release chan struct{}
	inner   *fakeVoiceStore
}

func (b *blockingVoiceStore) GetProviderConfigByComponent(ctx context.Context, id uuid.UUID, c storage.Component) (storage.ProviderConfig, error) {
	<-b.release
	return b.inner.GetProviderConfigByComponent(ctx, id, c)
}

func (b *blockingVoiceStore) GetDeploymentConfig(ctx context.Context, id uuid.UUID) (storage.DeploymentConfig, error) {
	<-b.release
	return b.inner.GetDeploymentConfig(ctx, id)
}

// TestGetProviderHealth_HungStoreDoesNotWedgeTenant pins the probe's hard
// deadline: the WHOLE probe — store reads included — is bounded, the per-tenant
// cache entry lock is released on timeout, and a timed-out probe is NOT cached
// (the next call after the store recovers probes fresh and reports healthy).
// Pre-fix a hung store read under context.WithoutCancel blocked the entry lock
// forever, wedging every later health call for the tenant.
func TestGetProviderHealth_HungStoreDoesNotWedgeTenant(t *testing.T) {
	t.Parallel()
	cipher := voiceTestCipher(t)
	bs := &blockingVoiceStore{release: make(chan struct{}), inner: healthyStore(t, cipher)}
	srv := NewVoiceServer(bs, cipher, nil)
	var seams countingHealthSeams
	seams.wire(srv)
	srv.probeTimeout = 50 * time.Millisecond

	ctx := tenantCtx()
	call := func(label string) *managementv1.GetProviderHealthResponse {
		t.Helper()
		done := make(chan *managementv1.GetProviderHealthResponse, 1)
		go func() {
			resp, err := srv.GetProviderHealth(ctx, connect.NewRequest(&managementv1.GetProviderHealthRequest{}))
			if err != nil {
				t.Errorf("%s: GetProviderHealth: %v", label, err)
				done <- nil
				return
			}
			done <- resp.Msg
		}()
		select {
		case msg := <-done:
			return msg
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: GetProviderHealth wedged >2s on a hung store read", label)
			return nil
		}
	}

	first := call("first")
	if first == nil {
		return
	}
	for _, p := range first.GetProviders() {
		if p.GetStatus() != managementv1.HealthStatus_HEALTH_STATUS_DEGRADED {
			t.Errorf("first call: %s should be degraded while the store hangs: %+v", p.GetProvider(), p)
		}
	}

	// The entry lock must be free again: a second call also returns within bound.
	call("second")

	// A timed-out probe must not be cached: once the store recovers, the next
	// call probes fresh and reports healthy.
	close(bs.release)
	third := call("third")
	if third == nil {
		return
	}
	for _, p := range third.GetProviders() {
		if p.GetStatus() != managementv1.HealthStatus_HEALTH_STATUS_HEALTHY {
			t.Errorf("after store recovery %s should be healthy: %+v", p.GetProvider(), p)
		}
	}
}

// TestGetProviderHealth_ActiveSessionKeepsLastKnownBotTag pins that the
// active-session short-circuit does not blank the Configuration screen's
// "Connected as X#NNNN" row: the tag from the last successful probe is
// returned with the short-circuited healthy result.
func TestGetProviderHealth_ActiveSessionKeepsLastKnownBotTag(t *testing.T) {
	t.Parallel()
	cipher := voiceTestCipher(t)
	srv := NewVoiceServer(healthyStore(t, cipher), cipher, nil)
	var seams countingHealthSeams
	seams.wire(srv)
	sessions := &fakeSessions{}
	srv.SetSessions(sessions)

	now := time.Now()
	srv.now = func() time.Time { return now }
	ctx := tenantCtx()

	discordOf := func(label string) *managementv1.ProviderHealth {
		t.Helper()
		resp, err := srv.GetProviderHealth(ctx, connect.NewRequest(&managementv1.GetProviderHealthRequest{}))
		if err != nil {
			t.Fatalf("%s: GetProviderHealth: %v", label, err)
		}
		for _, p := range resp.Msg.GetProviders() {
			if p.GetProvider() == "discord" {
				return p
			}
		}
		t.Fatalf("%s: no discord slot", label)
		return nil
	}

	// No session yet: the probe runs and resolves the tag.
	if got := discordOf("probe").GetBotTag(); got != "Glyphoxa#4823" {
		t.Fatalf("probed tag = %q, want Glyphoxa#4823", got)
	}

	// Session starts; cache expires. The short-circuit must not blank the tag.
	sessions.active = true
	now = now.Add(healthCacheTTL + time.Second)
	p := discordOf("short-circuit")
	if p.GetStatus() != managementv1.HealthStatus_HEALTH_STATUS_HEALTHY {
		t.Errorf("discord should be healthy during an active session: %+v", p)
	}
	if got := p.GetBotTag(); got != "Glyphoxa#4823" {
		t.Errorf("short-circuit tag = %q, want the last-known Glyphoxa#4823", got)
	}
	if got := seams.discord.Load(); got != 1 {
		t.Errorf("discord probes = %d, want 1 (short-circuit must not touch Discord)", got)
	}
}

// TestSaveCredentials_InvalidateHealthCache pins #150's cache-busting: after
// the operator saves a new provider key or Discord settings, the next health
// call probes the vendors fresh instead of serving a stale (possibly Degraded)
// cached result for up to the TTL — which would imply the new key is also bad.
func TestSaveCredentials_InvalidateHealthCache(t *testing.T) {
	t.Parallel()
	cipher := voiceTestCipher(t)
	voiceSrv := NewVoiceServer(healthyStore(t, cipher), cipher, nil)
	var seams countingHealthSeams
	seams.wire(voiceSrv)
	now := time.Now()
	voiceSrv.now = func() time.Time { return now } // TTL never expires on its own

	providerSrv := NewProviderServer(&stubProviderStore{}, cipher, nil)
	providerSrv.SetHealthInvalidator(voiceSrv.InvalidateHealth)

	ctx := tenantCtx() // same tenant drives saves and health calls
	health := func(label string) {
		t.Helper()
		if _, err := voiceSrv.GetProviderHealth(ctx, connect.NewRequest(&managementv1.GetProviderHealthRequest{})); err != nil {
			t.Fatalf("%s: GetProviderHealth: %v", label, err)
		}
	}

	health("initial")
	if got := seams.counts(); got != [3]int64{1, 1, 1} {
		t.Fatalf("counts after initial call = %v, want 1 each", got)
	}

	// Saving a provider key busts the tenant's cache: the next call probes.
	if _, err := providerSrv.SaveProviderConfig(ctx, connect.NewRequest(&managementv1.SaveProviderConfigRequest{
		Provider: "groq", Secret: "new-groq-key",
	})); err != nil {
		t.Fatalf("SaveProviderConfig: %v", err)
	}
	health("after key save")
	if got := seams.counts(); got != [3]int64{2, 2, 2} {
		t.Errorf("counts after key save = %v, want 2 each (cache busted)", got)
	}

	// Saving Discord settings busts it too.
	if _, err := providerSrv.SaveDiscordSettings(ctx, connect.NewRequest(&managementv1.SaveDiscordSettingsRequest{
		GuildId: ptr("g1"), VoiceChannelId: ptr("c1"),
	})); err != nil {
		t.Fatalf("SaveDiscordSettings: %v", err)
	}
	health("after discord save")
	if got := seams.counts(); got != [3]int64{3, 3, 3} {
		t.Errorf("counts after discord save = %v, want 3 each (cache busted)", got)
	}
}

// stubProviderStore is the minimal providerStore for the invalidation test:
// saves succeed with canned rows (provider_test.go's richer fake lives in the
// external rpc_test package and is out of reach here).
type stubProviderStore struct{}

func (stubProviderStore) ListProviderConfigs(context.Context, uuid.UUID) ([]storage.ProviderConfig, error) {
	return nil, nil
}

func (stubProviderStore) UpsertProviderConfigs(_ context.Context, configs []storage.NewProviderConfig) ([]storage.ProviderConfig, error) {
	out := make([]storage.ProviderConfig, len(configs))
	for i, n := range configs {
		out[i] = storage.ProviderConfig{
			Component: n.Component, Provider: n.Provider,
			CredentialsCiphertext: n.CredentialsCiphertext, CredentialsLast4: n.CredentialsLast4,
		}
	}
	return out, nil
}

func (stubProviderStore) GetDeploymentConfig(context.Context, uuid.UUID) (storage.DeploymentConfig, error) {
	return storage.DeploymentConfig{}, storage.ErrNotFound
}

func (stubProviderStore) SaveDiscordBotToken(context.Context, uuid.UUID, []byte, string) (storage.DeploymentConfig, error) {
	return storage.DeploymentConfig{}, nil
}

func (stubProviderStore) SaveDiscordChannels(_ context.Context, _ uuid.UUID, guildID, voiceChannelID string) (storage.DeploymentConfig, error) {
	return storage.DeploymentConfig{GuildID: guildID, VoiceChannelID: voiceChannelID}, nil
}

// ptr returns a pointer to v, for proto3 optional scalar fields.
func ptr[T any](v T) *T { return &v }
