package rpc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/discordinvite"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
)

// fakeInviteStore is a minimal providerStore for the ResolveGuildInvite tests:
// only GetDeploymentConfig carries behaviour (the Bot-token source); the rest of
// the interface is inert. The package rpc_test fakeProviderStore is unreachable
// from this internal (package rpc) test that overrides the unexported seam.
type fakeInviteStore struct {
	dep    storage.DeploymentConfig
	depSet bool
}

func (f *fakeInviteStore) GetDeploymentConfig(context.Context, uuid.UUID) (storage.DeploymentConfig, error) {
	if !f.depSet {
		return storage.DeploymentConfig{}, storage.ErrNotFound
	}
	return f.dep, nil
}

func (f *fakeInviteStore) ListProviderConfigs(context.Context, uuid.UUID) ([]storage.ProviderConfig, error) {
	return nil, nil
}

func (f *fakeInviteStore) GetProviderConfigByComponent(context.Context, uuid.UUID, storage.Component) (storage.ProviderConfig, error) {
	return storage.ProviderConfig{}, storage.ErrNotFound
}

func (f *fakeInviteStore) UpsertProviderConfigs(context.Context, []storage.NewProviderConfig) ([]storage.ProviderConfig, error) {
	return nil, nil
}

func (f *fakeInviteStore) SaveDiscordBotToken(context.Context, uuid.UUID, []byte, string) (storage.DeploymentConfig, error) {
	return storage.DeploymentConfig{}, nil
}

func (f *fakeInviteStore) SaveDiscordChannels(context.Context, uuid.UUID, string, string) (storage.DeploymentConfig, error) {
	return storage.DeploymentConfig{}, nil
}

func (f *fakeInviteStore) GetTenantSpendCaps(context.Context, uuid.UUID) (storage.SpendCaps, error) {
	return storage.SpendCaps{}, nil
}

func (f *fakeInviteStore) SetTenantSpendCaps(context.Context, uuid.UUID, storage.SpendCaps) error {
	return nil
}

// savedInviteStore builds a store holding a real Bot token sealed under a fresh
// cipher, returning both so the test decrypts the token the handler passes to
// the seam.
func savedInviteStore(t *testing.T) (*fakeInviteStore, *crypto.Cipher) {
	t.Helper()
	cipher := voiceTestCipher(t)
	ct, err := cipher.Seal([]byte("bot-secret-token"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	store := &fakeInviteStore{depSet: true, dep: storage.DeploymentConfig{
		DiscordBotTokenCiphertext: ct,
		DiscordBotTokenLast4:      crypto.Last4("bot-secret-token"),
	}}
	return store, cipher
}

// TestResolveGuildInvite_HappyPath_DecryptedTokenAndMapping pins that the handler
// decrypts the saved Bot token and passes it (with the bare code) to the resolver
// seam, then maps the seam's Resolved onto the wire response.
func TestResolveGuildInvite_HappyPath_DecryptedTokenAndMapping(t *testing.T) {
	t.Parallel()
	store, cipher := savedInviteStore(t)
	srv := NewProviderServer(store, cipher, nil)

	var gotToken, gotCode string
	srv.resolveInvite = func(_ context.Context, token, code string) (discordinvite.Resolved, error) {
		gotToken, gotCode = token, code
		return discordinvite.Resolved{
			Guild:         discordinvite.Guild{ID: "111", Name: "The Keep"},
			VoiceChannels: []discordinvite.VoiceChannel{{ID: "9", Name: "Lobby"}, {ID: "8", Name: "Tavern"}},
		}, nil
	}

	resp, err := srv.ResolveGuildInvite(tenantCtx(),
		connect.NewRequest(&managementv1.ResolveGuildInviteRequest{InviteCode: "abc123"}))
	if err != nil {
		t.Fatalf("ResolveGuildInvite: %v", err)
	}
	if gotToken != "bot-secret-token" {
		t.Errorf("seam token = %q, want the decrypted 'bot-secret-token'", gotToken)
	}
	if gotCode != "abc123" {
		t.Errorf("seam code = %q, want abc123", gotCode)
	}
	msg := resp.Msg
	if msg.GetGuildId() != "111" || msg.GetGuildName() != "The Keep" {
		t.Errorf("guild = %q/%q, want 111/The Keep", msg.GetGuildId(), msg.GetGuildName())
	}
	vc := msg.GetVoiceChannels()
	if len(vc) != 2 || vc[0].GetId() != "9" || vc[0].GetName() != "Lobby" || vc[1].GetId() != "8" {
		t.Errorf("voice channels = %+v, want [{9 Lobby} {8 Tavern}]", vc)
	}
}

// TestResolveGuildInvite_NoToken_FailedPrecondition_SeamNotCalled: with no saved
// token (no dep row, or the ENV placeholder), the RPC fails FailedPrecondition
// BEFORE touching the resolver — no live call fires without a real token.
func TestResolveGuildInvite_NoToken_FailedPrecondition_SeamNotCalled(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		store *fakeInviteStore
	}{
		{"no dep row", &fakeInviteStore{}},
		{"env placeholder", &fakeInviteStore{depSet: true, dep: storage.DeploymentConfig{DiscordBotTokenLast4: "env"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewProviderServer(tc.store, voiceTestCipher(t), nil)
			called := false
			srv.resolveInvite = func(context.Context, string, string) (discordinvite.Resolved, error) {
				called = true
				return discordinvite.Resolved{}, nil
			}
			_, err := srv.ResolveGuildInvite(tenantCtx(),
				connect.NewRequest(&managementv1.ResolveGuildInviteRequest{InviteCode: "abc123"}))
			if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
				t.Errorf("code = %v, want FailedPrecondition", got)
			}
			if called {
				t.Error("resolver seam called despite no saved token")
			}
		})
	}
}

// TestResolveGuildInvite_SavedTokenNilCipher_FailedPrecondition: a saved token
// with no cipher configured cannot be decrypted → FailedPrecondition, seam not
// called.
func TestResolveGuildInvite_SavedTokenNilCipher_FailedPrecondition(t *testing.T) {
	t.Parallel()
	store := &fakeInviteStore{depSet: true, dep: storage.DeploymentConfig{
		DiscordBotTokenLast4:      "abcd",
		DiscordBotTokenCiphertext: []byte("x"),
	}}
	srv := NewProviderServer(store, nil, nil) // nil cipher
	called := false
	srv.resolveInvite = func(context.Context, string, string) (discordinvite.Resolved, error) {
		called = true
		return discordinvite.Resolved{}, nil
	}
	_, err := srv.ResolveGuildInvite(tenantCtx(),
		connect.NewRequest(&managementv1.ResolveGuildInviteRequest{InviteCode: "abc123"}))
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Errorf("code = %v, want FailedPrecondition", got)
	}
	if called {
		t.Error("resolver seam called despite nil cipher")
	}
}

// TestResolveGuildInvite_SeamErrorMapping pins the sentinel → gRPC code mapping:
// ErrNotFound → NotFound, ErrNoAccess → FailedPrecondition, anything else →
// Internal (an opaque upstream error must not leak as a client-fault code).
func TestResolveGuildInvite_SeamErrorMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want connect.Code
	}{
		{"not found", discordinvite.ErrNotFound, connect.CodeNotFound},
		{"no access", discordinvite.ErrNoAccess, connect.CodeFailedPrecondition},
		{"opaque", errors.New("upstream 500"), connect.CodeInternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, cipher := savedInviteStore(t)
			srv := NewProviderServer(store, cipher, nil)
			srv.resolveInvite = func(context.Context, string, string) (discordinvite.Resolved, error) {
				return discordinvite.Resolved{}, tc.err
			}
			_, err := srv.ResolveGuildInvite(tenantCtx(),
				connect.NewRequest(&managementv1.ResolveGuildInviteRequest{InviteCode: "abc123"}))
			if got := connect.CodeOf(err); got != tc.want {
				t.Errorf("code = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestResolveGuildInvite_InternalError_LogRedactsCode: an opaque resolver error
// maps to Internal AND its log line must not carry the invite code. Transport
// failures wrap *url.Error whose text embeds the request URL — and thus the code,
// a join capability (ADR-0047). The op still logs; only the code is scrubbed.
func TestResolveGuildInvite_InternalError_LogRedactsCode(t *testing.T) {
	t.Parallel()
	store, cipher := savedInviteStore(t)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	srv := NewProviderServer(store, cipher, logger)

	const code = "s3cretJoinCode"
	// Mimic the *url.Error a live transport failure produces: its text carries the
	// full request URL, code included.
	srv.resolveInvite = func(context.Context, string, string) (discordinvite.Resolved, error) {
		return discordinvite.Resolved{}, fmt.Errorf(
			"discordinvite: GET /invites: Get %q: dial tcp: i/o timeout",
			"https://discord.com/api/v10/invites/"+code)
	}

	_, err := srv.ResolveGuildInvite(tenantCtx(),
		connect.NewRequest(&managementv1.ResolveGuildInviteRequest{InviteCode: code}))
	if got := connect.CodeOf(err); got != connect.CodeInternal {
		t.Fatalf("code = %v, want Internal", got)
	}
	logged := buf.String()
	if strings.Contains(logged, code) {
		t.Errorf("log leaked the invite code %q:\n%s", code, logged)
	}
	// Enough survives to diagnose: the operation and the failing REST call.
	if !strings.Contains(logged, "GET /invites") {
		t.Errorf("log dropped the diagnostic op, got:\n%s", logged)
	}
}

// TestResolveGuildInvite_BadCode_InvalidArgument: codes failing ^[A-Za-z0-9-]{2,64}$
// are rejected before any store read or seam call.
func TestResolveGuildInvite_BadCode_InvalidArgument(t *testing.T) {
	t.Parallel()
	store, cipher := savedInviteStore(t)
	for _, code := range []string{"", "a", "a b", "abc/def", strings.Repeat("a", 65)} {
		srv := NewProviderServer(store, cipher, nil)
		called := false
		srv.resolveInvite = func(context.Context, string, string) (discordinvite.Resolved, error) {
			called = true
			return discordinvite.Resolved{}, nil
		}
		_, err := srv.ResolveGuildInvite(tenantCtx(),
			connect.NewRequest(&managementv1.ResolveGuildInviteRequest{InviteCode: code}))
		if got := connect.CodeOf(err); got != connect.CodeInvalidArgument {
			t.Errorf("code %q: got %v, want InvalidArgument", code, got)
		}
		if called {
			t.Errorf("code %q: seam called for an invalid code", code)
		}
	}
}

// TestResolveGuildInvite_NoTenant_Unauthenticated: with no tenant in context the
// RPC is Unauthenticated, matching the other management reads.
func TestResolveGuildInvite_NoTenant_Unauthenticated(t *testing.T) {
	t.Parallel()
	store, cipher := savedInviteStore(t)
	srv := NewProviderServer(store, cipher, nil)
	_, err := srv.ResolveGuildInvite(context.Background(),
		connect.NewRequest(&managementv1.ResolveGuildInviteRequest{InviteCode: "abc123"}))
	if got := connect.CodeOf(err); got != connect.CodeUnauthenticated {
		t.Errorf("code = %v, want Unauthenticated", got)
	}
}
