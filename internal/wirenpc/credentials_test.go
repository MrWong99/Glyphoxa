package wirenpc

import (
	"crypto/rand"
	"strings"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
)

// newUnitCipher builds a Cipher on a fresh random AES-256 key. Keyless and
// Docker-free, so the credential-bridge resolution logic is proven in the
// default suite (the DB round-trip lives in credentials_integration_test.go).
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

// sealedConfig builds a ProviderConfig holding a real (non-placeholder) key
// sealed with cipher — the BYOK web-app path (last4 is the key's, not "env").
func sealedConfig(t *testing.T, cipher *crypto.Cipher, key string) *storage.ProviderConfig {
	t.Helper()
	ct, err := cipher.Seal([]byte(key))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	return &storage.ProviderConfig{
		CredentialsCiphertext: ct,
		CredentialsLast4:      crypto.Last4(key),
	}
}

// TestResolveKey is the core AC1+AC2 proof: a real saved key reaches the caller
// DECRYPTED; the "env" placeholder (and an unbound config) falls back to "" so
// the adapter reads ENV; and a real key that cannot be decrypted surfaces a
// CLEAR error rather than a silent empty string.
func TestResolveKey(t *testing.T) {
	cipher := newUnitCipher(t)
	wrong := newUnitCipher(t) // a different key -> cannot open cipher's blobs

	const realKey = "sk-real-secret-key-1234"
	sealed := sealedConfig(t, cipher, realKey)

	// An env-placeholder row: it carries a (sealed) placeholder blob but its
	// last4 is the "env" marker, so the real key lives outside the DB.
	placeholder := sealedConfig(t, cipher, "placeholder: real key in keyring")
	placeholder.CredentialsLast4 = credPlaceholderLast4

	tests := []struct {
		name    string
		cipher  *crypto.Cipher
		cfg     *storage.ProviderConfig
		wantKey string
		wantErr string // substring; "" means no error expected
	}{
		{name: "nil config -> env fallback", cipher: cipher, cfg: nil, wantKey: ""},
		{name: "env placeholder -> env fallback", cipher: cipher, cfg: placeholder, wantKey: ""},
		{name: "real key + cipher -> decrypted", cipher: cipher, cfg: sealed, wantKey: realKey},
		{name: "real key + nil cipher -> clear error", cipher: nil, cfg: sealed, wantErr: "cipher"},
		{name: "real key + wrong cipher -> clear error", cipher: wrong, cfg: sealed, wantErr: "decrypt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveKey(tt.cipher, tt.cfg, storage.ComponentLLM)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("resolveKey() error = nil, want error containing %q — a real saved key must never resolve to a silent empty key (AC2)", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("resolveKey() error = %q, want substring %q", err, tt.wantErr)
				}
				if got != "" {
					t.Errorf("resolveKey() key = %q on error, want empty", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveKey() unexpected error: %v", err)
			}
			if got != tt.wantKey {
				t.Errorf("resolveKey() = %q, want %q", got, tt.wantKey)
			}
		})
	}
}

// TestResolveProviderKeys pins the per-component aggregation: each component
// resolves independently (real keys decrypted, an unbound component -> ""), and
// a single undecryptable component fails the WHOLE resolution clearly (AC2) so a
// broken key can never silently fall through to a half-configured session.
func TestResolveProviderKeys(t *testing.T) {
	cipher := newUnitCipher(t)
	llm := sealedConfig(t, cipher, "llm-key-aaaa")
	tts := sealedConfig(t, cipher, "tts-key-bbbb")

	// STT unbound -> env fallback ("") while LLM/TTS decrypt — the hybrid mix.
	keys, err := resolveProviderKeys(cipher, llm, tts, nil)
	if err != nil {
		t.Fatalf("resolveProviderKeys: %v", err)
	}
	if keys.llm != "llm-key-aaaa" {
		t.Errorf("keys.llm = %q, want decrypted %q", keys.llm, "llm-key-aaaa")
	}
	if keys.tts != "tts-key-bbbb" {
		t.Errorf("keys.tts = %q, want decrypted %q", keys.tts, "tts-key-bbbb")
	}
	if keys.stt != "" {
		t.Errorf("keys.stt = %q, want \"\" (unbound -> adapter ENV fallback)", keys.stt)
	}

	// A real key the cipher cannot open fails the whole resolution (AC2).
	if _, err := resolveProviderKeys(nil, llm, tts, nil); err == nil {
		t.Fatal("resolveProviderKeys(nil cipher, real keys) = nil error, want a clear failure (AC2)")
	}
}
