package rpc

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// These tests pin the Planning chat model catalog: ListModels with slot "chat"
// (ADR-0062 amendment 2026-11-22).

func chatModels(t *testing.T, srv *VoiceServer, provider string) []string {
	t.Helper()
	resp, err := srv.ListModels(tenantCtx(), connect.NewRequest(&managementv1.ListModelsRequest{Provider: provider, Slot: "chat"}))
	if err != nil {
		t.Fatalf("ListModels(%s, chat): %v", provider, err)
	}
	return resp.Msg.GetModels()
}

// liveAnthropic is a live Models API answer: unsorted, with a repeat.
var liveAnthropic = []string{
	"claude-haiku-4-5", "claude-opus-5-5", "claude-fable-5-2", "claude-opus-4-8",
	"claude-haiku-5-5", "claude-sonnet-5-5", "claude-sonnet-5", "claude-opus-5-5",
}

// The chat default is first, then the recommended current models the list
// actually has, then the rest sorted — no id twice.
func TestListModels_ChatPinsChatDefaultAndRecommended(t *testing.T) {
	t.Parallel()
	cipher := voiceTestCipher(t)
	store := &fakeVoiceStore{configs: map[storage.Component]storage.ProviderConfig{
		storage.ComponentLLM: savedConfig(t, cipher, storage.ComponentLLM, "anthropic", "sk-ant-secret"),
	}}
	srv := NewVoiceServer(store, cipher, nil)
	srv.listAnthropicModels = func(context.Context, string) ([]string, error) { return liveAnthropic, nil }

	want := []string{
		"claude-sonnet-5-5", "claude-opus-5-5", "claude-haiku-5-5", "claude-fable-5-2",
		"claude-haiku-4-5", "claude-opus-4-8", "claude-sonnet-5",
	}
	if got := chatModels(t, srv, "anthropic"); !reflect.DeepEqual(got, want) {
		t.Errorf("chat models = %v\nwant        %v", got, want)
	}

	// A recommended id the live list lacks is not invented; the default is.
	srv.listAnthropicModels = func(context.Context, string) ([]string, error) {
		return []string{"claude-haiku-5-5", "claude-haiku-4-5"}, nil
	}
	want = []string{"claude-sonnet-5-5", "claude-haiku-5-5", "claude-haiku-4-5"}
	if got := chatModels(t, srv, "anthropic"); !reflect.DeepEqual(got, want) {
		t.Errorf("chat models = %v, want %v", got, want)
	}
}

// The chat-only key on the chat row fetches the list when the chat is on that
// provider — not the voice slot's key.
func TestListModels_ChatUsesChatOwnKey(t *testing.T) {
	t.Parallel()
	cipher := voiceTestCipher(t)
	store := &fakeVoiceStore{configs: map[storage.Component]storage.ProviderConfig{
		storage.ComponentChatLLM: savedConfig(t, cipher, storage.ComponentChatLLM, "anthropic", "sk-ant-chat-only"),
		storage.ComponentLLM:     savedConfig(t, cipher, storage.ComponentLLM, "anthropic", "sk-ant-voice"),
	}}
	srv := NewVoiceServer(store, cipher, nil)
	var seen atomic.Value
	srv.listAnthropicModels = func(_ context.Context, apiKey string) ([]string, error) {
		seen.Store(apiKey)
		return nil, nil
	}
	chatModels(t, srv, "anthropic")
	if got, _ := seen.Load().(string); got != "sk-ant-chat-only" {
		t.Errorf("catalog key = %q, want the chat row's own key", got)
	}
}

// A provider the chat is NOT on (yet) lists with the Tenant's key in that
// provider's LLM slot — never with the chat-only key of another provider.
func TestListModels_ChatBorrowsProviderSlotKey(t *testing.T) {
	t.Parallel()
	cipher := voiceTestCipher(t)
	store := &fakeVoiceStore{
		configs: map[storage.Component]storage.ProviderConfig{
			storage.ComponentChatLLM: savedConfig(t, cipher, storage.ComponentChatLLM, "groq", "gsk-chat-only"),
			storage.ComponentLLM:     savedConfig(t, cipher, storage.ComponentLLM, "groq", "gsk-voice"),
		},
		older: []storage.ProviderConfig{savedConfig(t, cipher, storage.ComponentLLM, "anthropic", "sk-ant-own")},
	}
	srv := NewVoiceServer(store, cipher, nil)
	var seen atomic.Value
	srv.listAnthropicModels = func(_ context.Context, apiKey string) ([]string, error) {
		seen.Store(apiKey)
		return nil, nil
	}
	chatModels(t, srv, "anthropic")
	if got, _ := seen.Load().(string); got != "sk-ant-own" {
		t.Errorf("catalog key = %q, want the tenant's anthropic slot key", got)
	}
}

// On the Platform Key the chat catalog offers only priced models: what the
// screen lists is what a keyless chat choice may store.
func TestListModels_ChatPlatformKeyOffersOnlyPriced(t *testing.T) {
	t.Parallel()
	srv := NewVoiceServer(&fakeVoiceStore{}, nil, nil)
	srv.lookupEnv = platformKeysSet
	var seen atomic.Value
	srv.listAnthropicModels = func(_ context.Context, apiKey string) ([]string, error) {
		seen.Store(apiKey)
		return append([]string{"claude-opus-4-1", "claude-haiku-4-5-20991231"}, liveAnthropic...), nil
	}

	want := []string{
		"claude-sonnet-5-5", "claude-opus-5-5", "claude-haiku-5-5",
		"claude-haiku-4-5", "claude-opus-4-8", "claude-sonnet-5",
	}
	if got := chatModels(t, srv, "anthropic"); !reflect.DeepEqual(got, want) {
		t.Errorf("chat models = %v\nwant only the priced ids %v", got, want)
	}
	if got, _ := seen.Load().(string); got != "" {
		t.Errorf("catalog key = %q, want the platform key (empty)", got)
	}
}

// A failed listing degrades to the priced chat fallback, never an error and
// never an unpriced suggestion.
func TestListModels_ChatFailureFallsBackToPricedList(t *testing.T) {
	t.Parallel()
	srv := NewVoiceServer(&fakeVoiceStore{}, nil, nil)
	srv.lookupEnv = platformKeysSet
	srv.listAnthropicModels = func(context.Context, string) ([]string, error) {
		return nil, errors.New("anthropic unreachable")
	}
	srv.listModels = func(context.Context, string) ([]string, error) {
		return nil, errors.New("groq unreachable")
	}

	want := []string{"claude-sonnet-5-5", "claude-haiku-5-5", "claude-haiku-4-5"}
	if got := chatModels(t, srv, "anthropic"); !reflect.DeepEqual(got, want) {
		t.Errorf("anthropic chat fallback = %v, want %v", got, want)
	}
	if got := chatModels(t, srv, "groq"); !reflect.DeepEqual(got, []string{"openai/gpt-oss-120b"}) {
		t.Errorf("groq chat fallback = %v, want the chat default alone", got)
	}
}

// A Tenant whose plan excludes the Platform Key and that has no key for the
// provider still gets a model list (the chat card asks for a key next to it):
// the priced fallback, without calling the provider and without an RPC error.
func TestListModels_ChatNoReachableKeyGetsTheFallback(t *testing.T) {
	t.Parallel()
	srv := NewVoiceServer(&fakeVoiceStore{}, nil, nil)
	srv.lookupEnv = platformKeysSet
	srv.SetKeyEntitlement(&scriptedEnt{allowed: false})
	srv.listAnthropicModels = func(context.Context, string) ([]string, error) {
		t.Error("the provider was called for a tenant with no key it may use")
		return nil, nil
	}

	want := []string{"claude-sonnet-5-5", "claude-haiku-5-5", "claude-haiku-4-5"}
	if got := chatModels(t, srv, "anthropic"); !reflect.DeepEqual(got, want) {
		t.Errorf("chat models = %v, want the fallback %v", got, want)
	}

	// The VOICE catalog keeps refusing in that state, as before.
	_, err := srv.ListModels(tenantCtx(), connect.NewRequest(&managementv1.ListModelsRequest{Provider: "anthropic"}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("voice catalog without entitlement: err = %v, want FailedPrecondition", err)
	}
}

// Without a slot the request is the voice catalog, byte for byte what it was:
// the voice default pinned, no chat ordering.
func TestListModels_VoiceUnchangedWithoutSlot(t *testing.T) {
	t.Parallel()
	cipher := voiceTestCipher(t)
	store := &fakeVoiceStore{configs: map[storage.Component]storage.ProviderConfig{
		storage.ComponentLLM: savedConfig(t, cipher, storage.ComponentLLM, "anthropic", "sk-ant-secret"),
		// A chat choice exists; the voice catalog must not use its key.
		storage.ComponentChatLLM: savedConfig(t, cipher, storage.ComponentChatLLM, "anthropic", "sk-ant-chat-only"),
	}}
	srv := NewVoiceServer(store, cipher, nil)
	var seen atomic.Value
	srv.listAnthropicModels = func(_ context.Context, apiKey string) ([]string, error) {
		seen.Store(apiKey)
		return liveAnthropic, nil
	}

	resp, err := srv.ListModels(tenantCtx(), connect.NewRequest(&managementv1.ListModelsRequest{Provider: "anthropic"}))
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	want := []string{
		"claude-haiku-5-5", "claude-fable-5-2", "claude-haiku-4-5", "claude-opus-4-8",
		"claude-opus-5-5", "claude-sonnet-5", "claude-sonnet-5-5",
	}
	if got := resp.Msg.GetModels(); !reflect.DeepEqual(got, want) {
		t.Errorf("voice models = %v\nwant         %v", got, want)
	}
	if got, _ := seen.Load().(string); got != "sk-ant-secret" {
		t.Errorf("voice catalog key = %q, want the voice slot's key", got)
	}
}

func TestListModels_UnknownSlotRejected(t *testing.T) {
	t.Parallel()
	srv := NewVoiceServer(&fakeVoiceStore{}, nil, nil)
	_, err := srv.ListModels(tenantCtx(), connect.NewRequest(&managementv1.ListModelsRequest{Provider: "anthropic", Slot: "recap"}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("err = %v, want InvalidArgument", err)
	}
	// A chat catalog exists only for the providers a chat can be put on that
	// have a catalog at all.
	_, err = srv.ListModels(tenantCtx(), connect.NewRequest(&managementv1.ListModelsRequest{Provider: "gemini", Slot: "chat"}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("gemini chat catalog: err = %v, want InvalidArgument", err)
	}
}
