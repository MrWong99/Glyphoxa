package rpc

import (
	"context"
	"slices"
	"sort"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/anthropic"
)

// This file is the Planning chat half of the model catalog (ListModels with
// slot "chat", ADR-0062 amendment 2026-11-22): which key the list is fetched
// with and how it is ordered. The fetch, the priced-only cut on the Platform
// Key and the never-fail posture are ListModels' own, shared with voice.

// chatRecommendedModels are the ids listed right after a provider's chat
// default when the live catalog has them: the current Claude models, strongest
// everyday choice first. Claude Fable 5.2 is listed for a Tenant on its own
// key; it has no price row, so the priced-only cut removes it on the Platform
// Key. Groq has no curated order beyond its default.
var chatRecommendedModels = map[string][]string{
	anthropic.ProviderID: {anthropic.ChatModel, "claude-opus-5-5", anthropic.HaikuVoiceModel, "claude-fable-5-2"},
}

// anthropicChatFallbackModels is what the chat model select offers for
// Anthropic when the live catalog cannot be fetched. A FALLBACK, never the
// source, and every id in it must be priced (TestOfferedDefaultModelsArePriced):
// it is shown to Tenants on the Platform Key too.
var anthropicChatFallbackModels = []string{anthropic.ChatModel, anthropic.HaikuVoiceModel, "claude-haiku-4-5"}

// chatCatalog shapes a raw provider catalog for the chat model select: the
// provider's chat default first (even if the live list omits it), then the
// recommended ids the list actually has, then the rest sorted ascending,
// without repeats.
func chatCatalog(provider string, models []string) []string {
	rest := slices.Clone(models)
	sort.Strings(rest)
	var head []string
	for _, m := range chatRecommendedModels[provider] {
		if slices.Contains(models, m) {
			head = append(head, m)
		}
	}
	return pinFirst(llmbuild.DefaultChatModel(provider), append(head, rest...))
}

// chatFallbackCatalog is the chat catalog when the live list is unavailable:
// the provider's chat default, plus the priced Claude ids for Anthropic.
func chatFallbackCatalog(provider string) []string {
	var models []string
	if provider == anthropic.ProviderID {
		models = anthropicChatFallbackModels
	}
	return pinFirst(llmbuild.DefaultChatModel(provider), models)
}

// resolveChatSlotKey resolves the key a chat choice of provider would run on,
// through the chat key ladder ([llmbuild.ChatKeyFor]): the chat row's own key
// when the chat is on that provider, else the Tenant's key in that provider's
// LLM slot, else the Platform Key behind the entitlement ("" — gated like
// every other env fallback). It answers for a provider the Tenant has not
// chosen yet, so the select can list models before the choice is saved.
func (s *VoiceServer) resolveChatSlotKey(ctx context.Context, tenantID uuid.UUID, provider string) (string, error) {
	configs, err := s.store.ListProviderConfigs(ctx, tenantID)
	if err != nil {
		return "", internalErr(s.log, "resolveChatSlotKey: store read failed", err, "provider", provider)
	}
	_, cfg, component := llmbuild.ChatKeyFor(provider, configs)
	return s.resolveKey(ctx, tenantID, component, cfg)
}
