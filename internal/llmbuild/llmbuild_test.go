package llmbuild

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/anthropic"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/gemini"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/groq"
)

func newUnitCipher(t *testing.T) *crypto.Cipher {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand key: %v", err)
	}
	c, err := crypto.New(key)
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	return c
}

func sealedConfig(t *testing.T, cipher *crypto.Cipher, provider, model, key string) *storage.ProviderConfig {
	t.Helper()
	ct, err := cipher.Seal([]byte(key))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return &storage.ProviderConfig{
		Provider:              provider,
		Model:                 model,
		CredentialsCiphertext: ct,
		CredentialsLast4:      crypto.Last4(key),
	}
}

// TestResolveKey is the parity port of wirenpc's credentials_test.go: the moved,
// exported resolver keeps identical semantics — nil cfg / "env" placeholder -> ""
// env fallback; a real key without a cipher or that cannot be decrypted is a CLEAR
// error, never a silent empty key (AC2, ADR-0039/0004).
func TestResolveKey(t *testing.T) {
	cipher := newUnitCipher(t)
	wrong := newUnitCipher(t)

	const realKey = "sk-real-secret-key-1234"
	sealed := sealedConfig(t, cipher, groq.ProviderID, "", realKey)

	placeholder := sealedConfig(t, cipher, groq.ProviderID, "", "placeholder: real key in keyring")
	placeholder.CredentialsLast4 = EnvPlaceholderLast4

	tests := []struct {
		name    string
		cipher  *crypto.Cipher
		cfg     *storage.ProviderConfig
		wantKey string
		wantErr string
	}{
		{name: "nil config -> env fallback", cipher: cipher, cfg: nil, wantKey: ""},
		{name: "env placeholder -> env fallback", cipher: cipher, cfg: placeholder, wantKey: ""},
		{name: "real key + cipher -> decrypted", cipher: cipher, cfg: sealed, wantKey: realKey},
		{name: "real key + nil cipher -> clear error", cipher: nil, cfg: sealed, wantErr: "cipher"},
		{name: "real key + wrong cipher -> clear error", cipher: wrong, cfg: sealed, wantErr: "decrypt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveKey(tt.cipher, tt.cfg, storage.ComponentLLM)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("ResolveKey() error = nil, want error containing %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("ResolveKey() error = %q, want substring %q", err, tt.wantErr)
				}
				if got != "" {
					t.Errorf("ResolveKey() key = %q on error, want empty", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveKey() unexpected error: %v", err)
			}
			if got != tt.wantKey {
				t.Errorf("ResolveKey() = %q, want %q", got, tt.wantKey)
			}
		})
	}
}

// TestNewDispatch pins provider selection off the provider_config.provider id: the
// three wired adapters, the empty-id default to Groq (ADR-0036), and a clear error
// naming an unknown id (never a silent hardwired groq).
func TestNewDispatch(t *testing.T) {
	tests := []struct {
		providerID string
		wantErr    string
	}{
		{providerID: "", wantErr: ""},
		{providerID: groq.ProviderID, wantErr: ""},
		{providerID: anthropic.ProviderID, wantErr: ""},
		{providerID: gemini.ProviderID, wantErr: ""},
		{providerID: "no-such-provider", wantErr: "no-such-provider"},
	}
	for _, tt := range tests {
		t.Run(tt.providerID, func(t *testing.T) {
			p, err := New(tt.providerID, "")
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("New(%q) error = nil, want error naming the id", tt.providerID)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("New(%q) error = %q, want substring %q", tt.providerID, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("New(%q) unexpected error: %v", tt.providerID, err)
			}
			if p == nil {
				t.Fatalf("New(%q) provider = nil", tt.providerID)
			}
		})
	}
}

// TestFromConfig proves the config carry: model comes from cfg.Model, a nil cfg
// resolves to the Groq default with an empty (env-fallback) key and empty model.
func TestFromConfig(t *testing.T) {
	cipher := newUnitCipher(t)

	t.Run("nil cfg -> groq default, empty model", func(t *testing.T) {
		p, model, err := FromConfig(cipher, nil)
		if err != nil {
			t.Fatalf("FromConfig(nil): %v", err)
		}
		if p == nil {
			t.Fatal("FromConfig(nil) provider = nil")
		}
		if model != "" {
			t.Errorf("FromConfig(nil) model = %q, want \"\"", model)
		}
	})

	t.Run("real cfg carries model", func(t *testing.T) {
		cfg := sealedConfig(t, cipher, anthropic.ProviderID, "claude-x", "sk-anthropic-key-9999")
		p, model, err := FromConfig(cipher, cfg)
		if err != nil {
			t.Fatalf("FromConfig(cfg): %v", err)
		}
		if p == nil {
			t.Fatal("FromConfig(cfg) provider = nil")
		}
		if model != "claude-x" {
			t.Errorf("FromConfig(cfg) model = %q, want %q", model, "claude-x")
		}
	})
}

// refuseAll is a scripted PlatformKeyEntitlement: refuse every tenant (an
// open-mode BYOK tenant with no platform subscription), optionally with an
// evaluation error.
type scriptedEntitlement struct {
	allowed bool
	err     error
	gotTen  uuid.UUID
}

func (s *scriptedEntitlement) PlatformKeyAllowed(_ context.Context, tenantID uuid.UUID) (bool, error) {
	s.gotTen = tenantID
	return s.allowed, s.err
}

// TestResolveKeyGated is the ADR-0054 entitlement seam (a) (ADR-0055): the ""
// env-fallback resolution — BOTH the nil-config path and the seeded "env"
// placeholder — is refused for a tenant without platform-key entitlement, while
// a real decrypted BYOK key passes regardless. A nil entitlement (self-host /
// allowlist posture) and an allowing one keep the ADR-0039 hybrid fallback
// byte-identical.
func TestResolveKeyGated(t *testing.T) {
	cipher := newUnitCipher(t)
	ctx := context.Background()
	tenant := uuid.New()

	const realKey = "sk-real-secret-key-1234"
	saved := sealedConfig(t, cipher, "groq", "m", realKey)
	envCfg := &storage.ProviderConfig{CredentialsLast4: EnvPlaceholderLast4}

	// nil entitlement = the allowlist posture: hybrid fallback untouched.
	if key, err := ResolveKeyGated(ctx, nil, tenant, cipher, nil, storage.ComponentLLM); err != nil || key != "" {
		t.Errorf("nil entitlement, nil cfg = (%q, %v), want (\"\", nil)", key, err)
	}

	// An allowing entitlement (platform-plan tenant): "" passes through so the
	// adapter spends the deployment's platform env key — that IS the platform path.
	allow := &scriptedEntitlement{allowed: true}
	if key, err := ResolveKeyGated(ctx, allow, tenant, cipher, envCfg, storage.ComponentTTS); err != nil || key != "" {
		t.Errorf("allowed entitlement, env cfg = (%q, %v), want (\"\", nil)", key, err)
	}
	if allow.gotTen != tenant {
		t.Errorf("entitlement consulted for tenant %s, want %s", allow.gotTen, tenant)
	}

	// A refusing entitlement: the nil-config hole AND the "env" placeholder are
	// both refused with a CLEAR error naming the component.
	refuse := &scriptedEntitlement{allowed: false}
	for name, cfg := range map[string]*storage.ProviderConfig{"nil config": nil, "env placeholder": envCfg} {
		_, err := ResolveKeyGated(ctx, refuse, tenant, cipher, cfg, storage.ComponentSTT)
		if !errors.Is(err, ErrNoPlatformKeyEntitlement) {
			t.Errorf("%s: err = %v, want ErrNoPlatformKeyEntitlement", name, err)
		}
		if err != nil && !strings.Contains(err.Error(), string(storage.ComponentSTT)) {
			t.Errorf("%s: err %q does not name the component", name, err)
		}
	}

	// A real saved BYOK key resolves normally even when the entitlement refuses:
	// the gate protects the deployment's platform keys, not the tenant's own.
	if key, err := ResolveKeyGated(ctx, refuse, tenant, cipher, saved, storage.ComponentLLM); err != nil || key != realKey {
		t.Errorf("refused entitlement, saved key = (%q, %v), want the decrypted key", key, err)
	}

	// The entitlement never runs when the key resolution itself errors (a saved
	// key with no cipher stays the ADR-0004 loud error).
	if _, err := ResolveKeyGated(ctx, refuse, tenant, nil, saved, storage.ComponentLLM); err == nil || errors.Is(err, ErrNoPlatformKeyEntitlement) {
		t.Errorf("saved key without cipher = %v, want the decrypt-unavailable error, not the entitlement's", err)
	}

	// An entitlement evaluation error fails CLOSED (never a silent platform-key
	// spend on a broken subscription read).
	broken := &scriptedEntitlement{err: errors.New("subscription read failed")}
	if _, err := ResolveKeyGated(ctx, broken, tenant, cipher, nil, storage.ComponentLLM); err == nil {
		t.Error("entitlement error must fail closed, got nil")
	}
}

// TestEnvFallbackAllowed pins the allowlist-posture entitlement: every tenant may
// ride the env fallback (ADR-0039 hybrid policy, self-host).
func TestEnvFallbackAllowed(t *testing.T) {
	ok, err := EnvFallbackAllowed{}.PlatformKeyAllowed(context.Background(), uuid.New())
	if !ok || err != nil {
		t.Errorf("EnvFallbackAllowed = (%v, %v), want (true, nil)", ok, err)
	}
}
