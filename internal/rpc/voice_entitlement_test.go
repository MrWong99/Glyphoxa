package rpc

import (
	"context"
	"errors"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/auth"
	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/groq"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// scriptedEnt is a scripted PlatformKeyEntitlement recording which tenants it
// was consulted for (the recap-test precedent).
type scriptedEnt struct {
	mu        sync.Mutex
	allowed   bool
	err       error
	consulted []uuid.UUID
}

func (s *scriptedEnt) PlatformKeyAllowed(_ context.Context, tenantID uuid.UUID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.consulted = append(s.consulted, tenantID)
	return s.allowed, s.err
}

func (s *scriptedEnt) consultedFor() []uuid.UUID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]uuid.UUID(nil), s.consulted...)
}

// entitlementSrv builds a VoiceServer over store with ent wired and every live
// seam faked to succeed, so a failure can only come from key resolution.
func entitlementSrv(t *testing.T, store *fakeVoiceStore, ent llmbuild.PlatformKeyEntitlement) *VoiceServer {
	t.Helper()
	srv := NewVoiceServer(store, voiceTestCipher(t), nil)
	srv.SetKeyEntitlement(ent)
	srv.newLister = func(string) tts.VoiceLister { return &fakeLister{} }
	srv.newSynth = func(string) tts.Synthesizer {
		return &fakeSynth{chunks: []tts.AudioChunk{{PCM: make([]byte, 480), SampleRate: 24000, Channels: 1}}}
	}
	srv.listModels = func(context.Context, string) ([]string, error) { return []string{groq.DefaultModel}, nil }
	srv.pingLLM = func(context.Context, string) error { return nil }
	srv.pingImage = func(context.Context, string) error { return nil }
	srv.botTag = func(context.Context, string) (string, error) { return "Glyphoxa#4823", nil }
	return srv
}

// TestVoiceRPCs_EntitlementRefusedOnEnvFallback pins ADR-0054 seam (a) on the
// RPC tier (the closed phase-B gap): with a refusing entitlement, BOTH
// env-fallback shapes — no provider_config row at all, and the seeded "env"
// placeholder row — are refused with CodeFailedPrecondition wrapping
// ErrNoPlatformKeyEntitlement, on every provider-key RPC.
func TestVoiceRPCs_EntitlementRefusedOnEnvFallback(t *testing.T) {
	t.Parallel()
	stores := map[string]func() *fakeVoiceStore{
		"no config row": func() *fakeVoiceStore { return &fakeVoiceStore{} },
		"env placeholder row": func() *fakeVoiceStore {
			return &fakeVoiceStore{configs: map[storage.Component]storage.ProviderConfig{
				storage.ComponentLLM: {Component: storage.ComponentLLM, Provider: "groq", CredentialsLast4: "env"},
				storage.ComponentTTS: {Component: storage.ComponentTTS, Provider: "elevenlabs", CredentialsLast4: "env"},
			}}
		},
	}
	calls := map[string]func(srv *VoiceServer) error{
		"ListModels": func(srv *VoiceServer) error {
			_, err := srv.ListModels(tenantCtx(), connect.NewRequest(&managementv1.ListModelsRequest{Provider: "groq"}))
			return err
		},
		"ListVoices": func(srv *VoiceServer) error {
			_, err := srv.ListVoices(tenantCtx(), connect.NewRequest(&managementv1.ListVoicesRequest{}))
			return err
		},
		"PreviewVoice": func(srv *VoiceServer) error {
			_, err := srv.PreviewVoice(tenantCtx(), connect.NewRequest(&managementv1.PreviewVoiceRequest{VoiceId: "v-x"}))
			return err
		},
	}
	for storeName, mkStore := range stores {
		for rpcName, call := range calls {
			t.Run(storeName+"/"+rpcName, func(t *testing.T) {
				t.Parallel()
				srv := entitlementSrv(t, mkStore(), &scriptedEnt{allowed: false})
				err := call(srv)
				if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
					t.Fatalf("code = %v (err=%v), want CodeFailedPrecondition", got, err)
				}
				if !errors.Is(err, llmbuild.ErrNoPlatformKeyEntitlement) {
					t.Errorf("err = %v, want wrapping ErrNoPlatformKeyEntitlement", err)
				}
			})
		}
	}
}

// A real saved BYOK key NEVER consults the entitlement — the gate protects the
// deployment's Platform Keys, not the tenant's own.
func TestVoiceRPCs_EntitlementNotConsultedForRealKey(t *testing.T) {
	t.Parallel()
	cipher := voiceTestCipher(t)
	store := &fakeVoiceStore{configs: map[storage.Component]storage.ProviderConfig{
		storage.ComponentLLM: savedConfig(t, cipher, storage.ComponentLLM, "groq", "sk-groq-secret"),
	}}
	ent := &scriptedEnt{allowed: false}
	srv := NewVoiceServer(store, cipher, nil)
	srv.SetKeyEntitlement(ent)
	var gotKey string
	srv.listModels = func(_ context.Context, apiKey string) ([]string, error) {
		gotKey = apiKey
		return []string{groq.DefaultModel}, nil
	}
	if _, err := srv.ListModels(tenantCtx(), connect.NewRequest(&managementv1.ListModelsRequest{Provider: "groq"})); err != nil {
		t.Fatalf("ListModels with a real BYOK key must pass a refusing gate: %v", err)
	}
	if gotKey != "sk-groq-secret" {
		t.Errorf("seam key = %q, want the decrypted tenant key", gotKey)
	}
	if consulted := ent.consultedFor(); len(consulted) != 0 {
		t.Errorf("entitlement consulted for a real key: %v", consulted)
	}
}

// The entitlement is consulted for the CTX tenant — never some other id.
func TestVoiceRPCs_EntitlementConsultedForCtxTenant(t *testing.T) {
	t.Parallel()
	ent := &scriptedEnt{allowed: true}
	srv := entitlementSrv(t, &fakeVoiceStore{}, ent)
	tenantID := uuid.New()
	ctx := auth.WithTenant(context.Background(), tenantID)
	if _, err := srv.ListVoices(ctx, connect.NewRequest(&managementv1.ListVoicesRequest{})); err != nil {
		t.Fatalf("ListVoices with a granting gate: %v", err)
	}
	consulted := ent.consultedFor()
	if len(consulted) == 0 {
		t.Fatal("entitlement never consulted on the env-fallback path")
	}
	for _, id := range consulted {
		if id != tenantID {
			t.Errorf("consulted for %s, want ctx tenant %s", id, tenantID)
		}
	}
}

// An entitlement READ error fails closed as a generic internal error — never
// fail-open for availability.
func TestVoiceRPCs_EntitlementReadErrorFailsClosed(t *testing.T) {
	t.Parallel()
	ent := &scriptedEnt{err: errors.New("subs db down")}
	srv := entitlementSrv(t, &fakeVoiceStore{}, ent)
	_, err := srv.ListVoices(tenantCtx(), connect.NewRequest(&managementv1.ListVoicesRequest{}))
	if got := connect.CodeOf(err); got != connect.CodeInternal {
		t.Fatalf("code = %v, want CodeInternal (fail closed)", got)
	}
}

// An EnvFallbackAllowed gate (the allowlist posture) keeps today's env-fallback
// behavior byte-for-byte.
func TestVoiceRPCs_EnvFallbackAllowedGrants(t *testing.T) {
	t.Parallel()
	srv := entitlementSrv(t, &fakeVoiceStore{}, llmbuild.EnvFallbackAllowed{})
	var gotKey = "unset"
	srv.newLister = func(apiKey string) tts.VoiceLister {
		gotKey = apiKey
		return &fakeLister{}
	}
	if _, err := srv.ListVoices(tenantCtx(), connect.NewRequest(&managementv1.ListVoicesRequest{})); err != nil {
		t.Fatalf("ListVoices: %v", err)
	}
	if gotKey != "" {
		t.Errorf("granted env fallback should resolve to empty key, got %q", gotKey)
	}
}

// TestGetProviderHealth_EntitlementRefusedDegrades: a refusal degrades the
// provider-key rows (groq / elevenlabs / gemini) with the actionable message,
// while the Discord row — deployment infrastructure outside the entitlement —
// stays healthy.
func TestGetProviderHealth_EntitlementRefusedDegrades(t *testing.T) {
	t.Parallel()
	cipher := voiceTestCipher(t)
	store := &fakeVoiceStore{
		dep: &storage.DeploymentConfig{
			DiscordBotTokenLast4: "tok9", DiscordBotTokenCiphertext: mustSeal(t, cipher, "bot-token"),
		},
	}
	srv := NewVoiceServer(store, cipher, nil)
	srv.SetKeyEntitlement(&scriptedEnt{allowed: false})
	srv.newLister = func(string) tts.VoiceLister { return &fakeLister{} }
	srv.pingLLM = func(context.Context, string) error { return nil }
	srv.pingImage = func(context.Context, string) error { return nil }
	srv.botTag = func(context.Context, string) (string, error) { return "Glyphoxa#4823", nil }

	resp, err := srv.GetProviderHealth(tenantCtx(), connect.NewRequest(&managementv1.GetProviderHealthRequest{}))
	if err != nil {
		t.Fatalf("GetProviderHealth: %v", err)
	}
	byProvider := map[string]*managementv1.ProviderHealth{}
	for _, p := range resp.Msg.GetProviders() {
		byProvider[p.GetProvider()] = p
	}
	for _, prov := range []string{"groq", "elevenlabs", "gemini"} {
		p := byProvider[prov]
		if p == nil || p.GetStatus() != managementv1.HealthStatus_HEALTH_STATUS_DEGRADED {
			t.Errorf("%s = %+v, want degraded on an entitlement refusal", prov, p)
		}
	}
	if d := byProvider["discord"]; d == nil || d.GetStatus() != managementv1.HealthStatus_HEALTH_STATUS_HEALTHY {
		t.Errorf("discord = %+v, want healthy — the Bot token is outside the entitlement", byProvider["discord"])
	}
}
