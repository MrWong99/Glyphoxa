package rpc_test

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/rpc"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// fakeKeyGate is a scripted platform-key entitlement (ADR-0054 seam (a)).
type fakeKeyGate struct {
	allowed bool
	err     error
	calls   int
}

func (g *fakeKeyGate) PlatformKeyAllowed(context.Context, uuid.UUID) (bool, error) {
	g.calls++
	return g.allowed, g.err
}

// listProvidedByPlatform lists the credentials of a server wired with gate, the
// central Bot token and a scripted env, keyed component → provided_by_platform.
func listProvidedByPlatform(t *testing.T, store *fakeProviderStore, gate *fakeKeyGate, botToken string, env map[string]string) map[string]bool {
	t.Helper()
	srv := rpc.NewProviderServer(store, testCipher(t), nil)
	if gate != nil {
		srv.SetKeyEntitlement(gate)
	}
	srv.SetEnvBotToken(botToken)
	srv.SetEnvLookupForTest(func(k string) string { return env[k] })
	client, _ := clientForServer(t, srv)
	resp, err := client.ListProviderConfigs(context.Background(), connect.NewRequest(&managementv1.ListProviderConfigsRequest{}))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	out := map[string]bool{}
	for _, c := range resp.Msg.GetCredentials() {
		// Two slots share the "llm" Component (#702); "llm" stays the Groq
		// slot these cases were written for, Anthropic gets its own key.
		key := c.GetComponent()
		if key == "llm" && c.GetProvider() != "groq" {
			key = "llm/" + c.GetProvider()
		}
		out[key] = c.GetProvidedByPlatform()
	}
	return out
}

// TestProviderList_ProvidedByPlatform pins W9: an unsaved slot the deployment
// serves — the Tenant is entitled to the Platform Keys AND the env key exists
// (or, for Discord, the central Bot token) — reads provided_by_platform so the
// screen says "Included", not "Key needed". A BYOK-plan Tenant, a missing env
// key, a failing entitlement read, and a saved own key all read false.
func TestProviderList_ProvidedByPlatform(t *testing.T) {
	t.Parallel()
	env := map[string]string{"GROQ_API_KEY": "gsk_platform", "ELEVENLABS_API_KEY": "el_platform"}

	t.Run("entitled tenant on platform keys", func(t *testing.T) {
		t.Parallel()
		gate := &fakeKeyGate{allowed: true}
		got := listProvidedByPlatform(t, newFakeProviderStore(), gate, "central-token", env)
		// image: the deployment has no GEMINI_API_KEY, so nothing provides it.
		want := map[string]bool{"discord": true, "llm": true, "tts": true, "chat_llm": true, "image": false}
		for comp, w := range want {
			if got[comp] != w {
				t.Errorf("%s provided_by_platform = %v, want %v", comp, got[comp], w)
			}
		}
		if gate.calls != 1 {
			t.Errorf("entitlement consulted %d times, want once per list", gate.calls)
		}
	})

	t.Run("BYOK plan tenant", func(t *testing.T) {
		t.Parallel()
		got := listProvidedByPlatform(t, newFakeProviderStore(), &fakeKeyGate{allowed: false}, "", env)
		for comp, v := range got {
			if v {
				t.Errorf("%s provided_by_platform = true for a BYOK-plan tenant with no central bot token", comp)
			}
		}
	})

	t.Run("self-host posture grants with no gate wired", func(t *testing.T) {
		t.Parallel()
		got := listProvidedByPlatform(t, newFakeProviderStore(), nil, "", env)
		if !got["llm"] || !got["tts"] || got["discord"] {
			t.Errorf("nil entitlement = everything allowed; got %v", got)
		}
	})

	t.Run("entitlement read error fails closed", func(t *testing.T) {
		t.Parallel()
		got := listProvidedByPlatform(t, newFakeProviderStore(), &fakeKeyGate{err: errors.New("db down")}, "", env)
		if got["llm"] || got["tts"] {
			t.Errorf("provided_by_platform true despite a failed entitlement read: %v", got)
		}
	})

	t.Run("own saved key wins", func(t *testing.T) {
		t.Parallel()
		store := newFakeProviderStore()
		store.configs["llm|groq"] = storage.ProviderConfig{Component: storage.ComponentLLM, Provider: "groq", CredentialsLast4: "abcd"}
		store.dep = &storage.DeploymentConfig{DiscordBotTokenLast4: "wxyz"}
		got := listProvidedByPlatform(t, store, &fakeKeyGate{allowed: true}, "central-token", env)
		if got["llm"] || got["discord"] {
			t.Errorf("a saved own key must not read provided_by_platform: %v", got)
		}
		if !got["tts"] {
			t.Errorf("unsaved tts should still be provided: %v", got)
		}
	})
}
