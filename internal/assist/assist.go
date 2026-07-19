// Package assist is the on-demand LLM campaign-creation helper (#479): from a
// short GM prompt it drafts a Character NPC's Persona, or a small set of linked
// Knowledge Graph entries (Nodes + Edges among them) as a PREVIEW the GM
// reviews before anything lands. It runs ONLY when the GM explicitly asks
// (a Generate button press) — it never volunteers suggestions — and it never
// persists anything itself: the persona draft goes back to the editor, the
// knowledge draft is applied by a separate GM-confirmed RPC.
//
// The LLM provider is resolved exactly like the recap engine (#271/#272): the
// Butler's llm_provider_config_id, else the Tenant 'llm' Provider Config, else
// the Groq env default (ADR-0036) — through [internal/llmbuild], with the env
// fallback gated by the platform-key entitlement (ADR-0054/0055). Usage is
// METERED via the shared [internal/llmcall] caller (attributed to the campaign
// via metrics + a structured log) but NEVER cap-gated — ADR-0046 caps are
// live-Voice-Session-only.
package assist

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/llmcall"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/groq"
)

const (
	// personaMaxTokens bounds a persona draft — a Persona is a few hundred words
	// of markdown, not an essay.
	personaMaxTokens = 1024
	// knowledgeMaxTokens bounds a knowledge draft's JSON: up to maxDraftNodes
	// entries with a few sentences of body each.
	knowledgeMaxTokens = 3072
	// maxDraftNodes caps how many entries one draft may hold; the LLM is told the
	// cap and any excess is truncated defensively (with its incident edges).
	maxDraftNodes = 12
	// maxDraftEdges caps a draft's edge list after validation.
	maxDraftEdges = 24
	// maxContextNames caps how many existing entry names season the knowledge
	// prompt (duplicate avoidance) so a huge wiki never blows the prompt budget.
	maxContextNames = 80
)

// ErrUnusableResponse is returned when the model's reply cannot be turned into
// a usable draft (no parseable JSON, or nothing valid left after filtering). A
// retry MAY succeed — the RPC layer maps it to CodeUnavailable.
var ErrUnusableResponse = errors.New("assist: the model response was not usable")

// Store is the read surface the engine needs; *storage.Store satisfies it.
type Store interface {
	GetButler(ctx context.Context, campaignID uuid.UUID) (storage.Agent, error)
	GetProviderConfig(ctx context.Context, id uuid.UUID) (storage.ProviderConfig, error)
	GetProviderConfigByComponent(ctx context.Context, tenantID uuid.UUID, component storage.Component) (storage.ProviderConfig, error)
	// ListNodes seasons the knowledge prompt with existing entry names so the
	// model links into fresh material instead of duplicating canon.
	ListNodes(ctx context.Context, campaignID uuid.UUID) ([]storage.KGNode, error)
}

// The concrete store satisfies Store with no thin wrappers.
var _ Store = (*storage.Store)(nil)

// PersonaInput carries the agent fields that season a persona draft.
type PersonaInput struct {
	// AgentName/AgentTitle are the editor's current values; either may be empty
	// (or a placeholder the GM has not renamed yet).
	AgentName  string
	AgentTitle string
	// Prompt is the GM's short description of the wanted character.
	Prompt string
}

// DraftNode is one drafted Knowledge Graph entry. Type is a kgvocab node-type
// value — already validated when the draft leaves the engine.
type DraftNode struct {
	Type      string
	Name      string
	Body      string
	GMPrivate bool
}

// DraftEdge is one drafted Edge between two DraftNodes of the same draft,
// referencing them by index into [Draft.Nodes]. Type is a kgvocab relation —
// already validated (vocabulary AND the ADR-0008 object-side matrix) when the
// draft leaves the engine.
type DraftEdge struct {
	FromIndex int
	ToIndex   int
	Type      string
}

// Draft is a validated knowledge draft: what the GM previews.
type Draft struct {
	Nodes []DraftNode
	Edges []DraftEdge
}

// ProviderFactory builds an [llm.Provider] from a provider id and API key — the
// cassette/test seam ([WithProviderFactory]); it defaults to [llmbuild.New].
type ProviderFactory func(providerID, apiKey string) (llm.Provider, error)

// Engine drafts campaign content on demand. Construct with [NewEngine].
type Engine struct {
	st          Store
	cipher      *crypto.Cipher
	metrics     observe.StageRecorder
	log         *slog.Logger
	newProvider ProviderFactory
	keyEnt      llmbuild.PlatformKeyEntitlement
}

// Option customises an [Engine].
type Option func(*Engine)

// WithProviderFactory overrides the LLM provider constructor — the seam a
// deterministic test injects to replay a canned completion. The default is
// [llmbuild.New].
func WithProviderFactory(f func(providerID, apiKey string) (llm.Provider, error)) Option {
	return func(e *Engine) { e.newProvider = f }
}

// WithKeyEntitlement gates the assist LLM-key env fallback behind the tenant's
// platform-key entitlement (ADR-0054 seam (a), ADR-0055), exactly like the
// recap engine. The default (unset) grants everything: the
// `allowlist`-Admission-Mode posture.
func WithKeyEntitlement(ent llmbuild.PlatformKeyEntitlement) Option {
	return func(e *Engine) { e.keyEnt = ent }
}

// NewEngine builds an assist Engine. cipher decrypts a BYOK LLM key (nil is
// fine for the env/default path); metrics receives usage (nil -> discard); log
// receives the attribution line (nil -> discard).
func NewEngine(st Store, cipher *crypto.Cipher, metrics observe.StageRecorder, log *slog.Logger, opts ...Option) *Engine {
	e := &Engine{
		st:          st,
		cipher:      cipher,
		metrics:     metrics,
		log:         log,
		newProvider: llmbuild.New,
	}
	if e.metrics == nil {
		e.metrics = observe.Discard{}
	}
	if e.log == nil {
		e.log = slog.New(slog.DiscardHandler)
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// GeneratePersona drafts a Persona (markdown) for a Character NPC from the GM's
// short prompt. The draft is returned for review in the editor — never saved.
func (e *Engine) GeneratePersona(ctx context.Context, campaign storage.Campaign, in PersonaInput) (persona string, err error) {
	caller, estimatedUSD, err := e.newCaller(ctx, campaign)
	if err != nil {
		return "", err
	}
	defer e.attribute("persona", campaign.ID, caller, estimatedUSD, &err)

	text, err := caller.Call(personaSystemPrompt(campaign), personaUserPrompt(in), personaMaxTokens)
	if err != nil {
		return "", err
	}
	persona = strings.TrimSpace(stripFences(text))
	if persona == "" {
		return "", ErrUnusableResponse
	}
	return persona, nil
}

// GenerateKnowledge drafts a set of linked Knowledge Graph entries from the
// GM's short prompt. The returned Draft is fully validated (vocabularies, index
// bounds, the ADR-0008 edge matrix) and is a PREVIEW: nothing is written here.
func (e *Engine) GenerateKnowledge(ctx context.Context, campaign storage.Campaign, prompt string) (d Draft, err error) {
	existing, err := e.st.ListNodes(ctx, campaign.ID)
	if err != nil {
		return Draft{}, fmt.Errorf("assist: list existing nodes: %w", err)
	}

	caller, estimatedUSD, err := e.newCaller(ctx, campaign)
	if err != nil {
		return Draft{}, err
	}
	defer e.attribute("knowledge", campaign.ID, caller, estimatedUSD, &err)

	text, err := caller.Call(knowledgeSystemPrompt(campaign, existing), prompt, knowledgeMaxTokens)
	if err != nil {
		return Draft{}, err
	}
	d, perr := parseDraft(text)
	if perr != nil {
		// The raw model text is logged (not returned) so a systematic prompt/parse
		// mismatch is diagnosable without leaking model output to the client.
		e.log.Warn("assist: unusable knowledge draft", "campaign_id", campaign.ID, "err", perr, "raw_len", len(text))
		return Draft{}, ErrUnusableResponse
	}
	return d, nil
}

// newCaller resolves the campaign's LLM provider (Butler config → tenant 'llm'
// config → gated env default) and wires the shared metered caller over the
// caps-free PriceOnly meter — the recap engine's exact posture.
func (e *Engine) newCaller(ctx context.Context, campaign storage.Campaign) (*llmcall.Caller, func() float64, error) {
	butler, err := e.st.GetButler(ctx, campaign.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("assist: load butler for campaign %s: %w", campaign.ID, err)
	}
	cfg, err := e.resolveLLMConfig(ctx, campaign.TenantID, butler)
	if err != nil {
		return nil, nil, err
	}
	key, err := llmbuild.ResolveKeyGated(ctx, e.keyEnt, campaign.TenantID, e.cipher, cfg, storage.ComponentLLM)
	if err != nil {
		return nil, nil, err
	}
	providerID, model := "", ""
	if cfg != nil {
		providerID, model = cfg.Provider, cfg.Model
	}
	provider, err := e.newProvider(providerID, key)
	if err != nil {
		return nil, nil, fmt.Errorf("assist: build llm provider: %w", err)
	}
	// The default path sends request model "" (the adapter fills the Groq
	// default) but prices on a real model so (groq, "") never misses the price
	// map (#272 review).
	priceModel := model
	if providerID == "" {
		priceModel = groq.DefaultModel
	}

	rec, estimatedUSD := spend.PriceOnly(e.metrics, e.log)
	return &llmcall.Caller{
		Ctx:        ctx,
		Provider:   provider,
		Model:      model,
		PriceModel: priceModel,
		Label:      llmcall.ProviderLabel(providerID),
		Rec:        rec,
		ErrPrefix:  "assist",
	}, estimatedUSD, nil
}

// attribute emits the usage attribution line — on success AND on a midway
// failure, so metered tokens are never orphaned (the #272 review rule).
func (e *Engine) attribute(feature string, campaignID uuid.UUID, c *llmcall.Caller, estimatedUSD func() float64, err *error) {
	e.log.Info("assist: llm usage",
		"feature", feature,
		"campaign_id", campaignID,
		"input_tokens", c.TotalIn,
		"output_tokens", c.TotalOut,
		"estimated_usd", estimatedUSD(),
		"ok", *err == nil,
	)
}

// resolveLLMConfig resolves the LLM Provider Config exactly like the recap
// engine (gate #271): the Butler's llm_provider_config_id if set, else the
// Tenant's 'llm' Provider Config, else nil (the Groq env default, ADR-0036).
func (e *Engine) resolveLLMConfig(ctx context.Context, tenantID uuid.UUID, butler storage.Agent) (*storage.ProviderConfig, error) {
	if butler.LLMProviderConfigID.Valid {
		pc, err := e.st.GetProviderConfig(ctx, butler.LLMProviderConfigID.UUID)
		if err != nil {
			return nil, fmt.Errorf("assist: load butler LLM provider config: %w", err)
		}
		return &pc, nil
	}
	pc, err := e.st.GetProviderConfigByComponent(ctx, tenantID, storage.ComponentLLM)
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return nil, nil // no config -> default Groq + env key
	case err != nil:
		return nil, fmt.Errorf("assist: load tenant LLM provider config: %w", err)
	default:
		return &pc, nil
	}
}
