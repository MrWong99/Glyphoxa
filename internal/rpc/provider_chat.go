package rpc

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	managementv1 "github.com/MrWong99/Glyphoxa/gen/glyphoxa/management/v1"
	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
)

// This file is the Planning chat model choice on the ProviderService (ADR-0062
// amendment 2026-11-22). The chat model is a Tenant-level CHOICE — a provider
// and a model, stored as the Tenant's single chat_llm Provider Config — not a
// key slot: it reuses keys through a ladder (llmbuild.ChatKeyFor), so it has
// its own save and delete paths and its own read (effective_chat_llm), all
// resolved by the function the chat engine runs (llmbuild's SelectChat).

// chatSlot is the wire slot id of the Planning chat model choice.
const chatSlot = "chat"

// chatComponentLabel is the display component of the chat row's credential view.
const chatComponentLabel = string(storage.ComponentChatLLM)

// saveChatModel stores the Tenant's Planning chat model choice. provider must
// be one a chat can be put on (groq, anthropic). Three cases:
//
//   - a secret: it is sealed and stored on the chat row — a key for the chat
//     alone, which never becomes the Tenant's voice LLM. Any model is accepted
//     (the Tenant's own key, its own bill).
//   - no secret, the chat already on this provider: only the model changes; a
//     chat-only key stays byte-identical (the store never re-upserts ciphertext).
//   - no secret, the chat not on this provider: accepted when a key is
//     reachable — the Tenant's own key in that provider's LLM slot, or the
//     Platform Key (the deployment has it AND the plan includes it). Otherwise
//     InvalidArgument and nothing is written; there is no fallback to another
//     provider's key.
//
// Whenever the choice would run on the Platform Key the model must be priced
// (or empty, the provider's chat default): an unpriced model is metered at a
// flat fallback that can be far above or below its real rate.
func (s *ProviderServer) saveChatModel(
	ctx context.Context,
	tenantID uuid.UUID,
	provider, model, secret string,
) (*connect.Response[managementv1.SaveProviderConfigResponse], error) {
	if !llmbuild.IsChatProvider(provider) {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("the planning chat cannot run on provider %q (choose groq or anthropic)", provider))
	}

	choice := storage.ChatLLMChoice{TenantID: tenantID, Provider: provider, Model: model}
	if secret != "" {
		if !isSaved(crypto.Last4(secret)) {
			// Its last four ARE the keyless placeholder: the row would be read as
			// "no key of its own" while this path takes any model, bypassing the
			// priced-only rule of the platform rung. No real key is that short.
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("secret is not a valid key"))
		}
		if s.cipher == nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				errors.New("credential encryption is not configured ($GLYPHOXA_SECRET)"))
		}
		sealed, last4, err := s.seal(secret)
		if err != nil {
			return nil, internalErr(s.log, "SaveProviderConfig: seal chat key failed", err)
		}
		choice.Ciphertext, choice.Last4 = sealed, last4
	} else {
		configs, err := s.store.ListProviderConfigs(ctx, tenantID)
		if err != nil {
			return nil, internalErr(s.log, "SaveProviderConfig: read provider_config for chat save failed", err)
		}
		keySource, _, _ := llmbuild.ChatKeyFor(provider, configs)
		switch keySource {
		case llmbuild.ChatKeyChat:
			// The model was accepted against the chat row's own key: the save
			// must land on a row that still holds one. If a concurrent save
			// replaced it with a keyless row, the store refuses (ErrNotFound)
			// rather than leave a free-text model on the platform rung.
			choice.KeepKey = true
		case llmbuild.ChatKeyProvider:
			// The Tenant's own key for this provider: any model.
		default: // the platform rung
			if !chatOnProvider(configs, provider) && !s.platformKeyReachable(ctx, tenantID, provider) {
				return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
					"no %s key is available for the planning chat: paste a key for it, or save your own %s key as a voice model key",
					provider, provider))
			}
			if err := chatModelPriced(provider, model); err != nil {
				return nil, err
			}
		}
	}

	saved, err := s.store.SaveChatLLMChoice(ctx, choice)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, connect.NewError(connect.CodeAborted,
			errors.New("the planning chat key changed while saving; reload and try again"))
	}
	if err != nil {
		return nil, internalErr(s.log, "SaveProviderConfig: save chat choice failed", err)
	}
	// The provider-health cache is NOT busted: GetProviderHealth has no chat
	// entry (its LLM entry is the voice LLM, which a chat save never changes).
	return connect.NewResponse(&managementv1.SaveProviderConfigResponse{
		Credential: providerCredential(chatComponentLabel, provider, saved),
	}), nil
}

// chatOnProvider reports whether the Tenant's chat choice is already on provider.
func chatOnProvider(configs []storage.ProviderConfig, provider string) bool {
	sel := llmbuild.DeploymentDefault{}.SelectChat(nil, configs)
	return sel.Source == llmbuild.ChatSourceChoice && sel.Provider == provider
}

// chatModelPriced refuses a model without a price row for a chat choice that
// rides the Platform Key. The empty model is the provider's chat default,
// which is priced by test (llmbuild.TestDefaultChatModelsArePriced).
func chatModelPriced(provider, model string) error {
	if model == "" || spend.LLMPriced(observe.Provider(provider), model) {
		return nil
	}
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
		"model %q has no price on record and cannot run on the platform key: choose a listed model, or use your own %s key for the planning chat",
		model, provider))
}

// platformKeyReachable reports whether this Tenant can run provider on the
// Platform Key right now: the deployment has that provider's env key AND the
// Tenant's plan includes the Platform Keys — the two halves every call-time
// resolution needs (the adapter's env fallback, [llmbuild.ResolveKeyGated]).
func (s *ProviderServer) platformKeyReachable(ctx context.Context, tenantID uuid.UUID, provider string) bool {
	return s.platformKeyPresent(provider) && s.platformKeyGate(ctx, tenantID)(storage.ComponentChatLLM)
}

// platformKeyPresent reports whether the deployment has provider's Platform Key.
func (s *ProviderServer) platformKeyPresent(provider string) bool {
	env, ok := llmbuild.PlatformKeyEnv(provider)
	return ok && s.lookupEnv(env) != ""
}

// DeleteProviderConfig removes a stored choice. Slot "chat" removes the
// Tenant's Planning chat model choice, and a chat-only key stored with it,
// returning the chat to "same as voice"; the voice LLM slots are removed by
// deleteVoiceLLM (provider.go, ADR-0004 amendment 2026-11-26). Idempotent.
func (s *ProviderServer) DeleteProviderConfig(
	ctx context.Context,
	req *connect.Request[managementv1.DeleteProviderConfigRequest],
) (*connect.Response[managementv1.DeleteProviderConfigResponse], error) {
	tenantID, err := s.tenant(ctx)
	if err != nil {
		return nil, err
	}
	if providers, ok := voiceLLMDeleteSlot(req.Msg.GetSlot()); ok {
		return s.deleteVoiceLLM(ctx, tenantID, providers)
	}
	if req.Msg.GetSlot() != chatSlot {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("slot %q cannot be deleted (only %q, %q, %q or %q can)", req.Msg.GetSlot(),
				chatSlot, "groq", "anthropic", voiceLLMSlot))
	}
	if err := s.store.DeleteChatLLMConfig(ctx, tenantID); err != nil {
		return nil, internalErr(s.log, "DeleteProviderConfig: delete chat choice failed", err)
	}
	return connect.NewResponse(&managementv1.DeleteProviderConfigResponse{}), nil
}

// effectiveChatLLM reports the LLM the Planning chat answers with for the
// Tenant, from the resolution the engine runs (SelectChat with no Butler-bound
// row: the read is Tenant-scoped). platform is the per-call entitlement memo.
// key_source is NONE when the resolution lands on the Platform Key and the
// deployment lacks it or the plan excludes it — an exchange would be refused.
func (s *ProviderServer) effectiveChatLLM(configs []storage.ProviderConfig, platform func(storage.Component) bool) *managementv1.EffectiveChatLlm {
	wireKey := func(source llmbuild.ChatKeySource, provider string) managementv1.ChatLlmKeySource {
		switch source {
		case llmbuild.ChatKeyChat:
			return managementv1.ChatLlmKeySource_CHAT_LLM_KEY_SOURCE_CHAT_KEY
		case llmbuild.ChatKeyProvider:
			return managementv1.ChatLlmKeySource_CHAT_LLM_KEY_SOURCE_PROVIDER_KEY
		}
		if s.platformKeyPresent(provider) && platform(storage.ComponentChatLLM) {
			return managementv1.ChatLlmKeySource_CHAT_LLM_KEY_SOURCE_PLATFORM
		}
		return managementv1.ChatLlmKeySource_CHAT_LLM_KEY_SOURCE_NONE
	}

	sel := s.llmDefault.SelectChat(nil, configs)
	eff := &managementv1.EffectiveChatLlm{
		Provider:  sel.Provider,
		Model:     sel.Model,
		KeySource: wireKey(sel.KeySource, sel.Provider),
	}
	switch sel.Source {
	case llmbuild.ChatSourceChoice:
		eff.Source = managementv1.ChatLlmSource_CHAT_LLM_SOURCE_CHOICE
		eff.StoredModel = sel.StoredModel
	case llmbuild.ChatSourceVoice:
		eff.Source = managementv1.ChatLlmSource_CHAT_LLM_SOURCE_VOICE
	default:
		eff.Source = managementv1.ChatLlmSource_CHAT_LLM_SOURCE_PLATFORM_DEFAULT
	}
	if sel.KeySource != llmbuild.ChatKeyPlatform && sel.KeyConfig != nil {
		eff.KeyLast4 = sel.KeyConfig.CredentialsLast4
	}
	for _, provider := range llmbuild.ChatProviders {
		source, _, _ := llmbuild.ChatKeyFor(provider, configs)
		key := wireKey(source, provider)
		eff.Providers = append(eff.Providers, &managementv1.ChatProviderOption{
			Provider:     provider,
			Available:    key != managementv1.ChatLlmKeySource_CHAT_LLM_KEY_SOURCE_NONE,
			KeySource:    key,
			DefaultModel: llmbuild.DefaultChatModel(provider),
		})
	}
	return eff
}
