// Package planchat is the Butler planning chat engine (#592, ADR-0062): the
// Campaign's Butler (ADR-0009) reachable as a GM-only chat from the Campaign
// screen, with persisted planning threads. One Exchange runs one user message
// through the tool-armed LLM loop (pkg/tool) and streams the reply.
//
// The ADR-0062 decisions this package implements:
//
//   - Metering: each exchange opens one call on the off-session metering
//     seam ([billing.OffSession], the one path every off-session paid call
//     takes — #658), which prices it on a caps-free meter and flushes to the
//     Usage Ledger PER EXCHANGE with Tenant attribution; the thread/campaign
//     attribution rides a structured log line, matching the recap/assist
//     posture. Never cap-gated — ADR-0046's live caps are Voice-Session-only.
//   - Allowance gate: the same seam checks the ADR-0055 monthly allowance
//     BEFORE each exchange; a refusal is [ErrAllowanceExhausted], a clear
//     in-chat refusal, never a silent drop. BYOK tenants are metered
//     identically but their plan carries no included allowance, so the same
//     check passes them through (ADR-0055 scoping).
//   - Model: the chat-scoped slot — the Butler's chat_llm_provider_config_id,
//     else the tenant 'chat_llm' Provider Config, else the tenant 'llm'
//     config, else the Groq env default (ADR-0036) — so upgrading chat never
//     touches the voice loop's selection.
//   - Prompt: a stable prefix (Persona + chat-tool instructions) + thread
//     history; no roster, no volatile tail (see prompt.go).
//   - Tools: the chat belt (tools.go) hydrated from the Butler's CHAT-surface
//     grant rows — disjoint from the voice grants (ADR-0029/0062).
package planchat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/billing"
	"github.com/MrWong99/Glyphoxa/internal/knowledge"
	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/llmcall"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/recap"
	"github.com/MrWong99/Glyphoxa/internal/search"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/agenttool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
)

const (
	// chatMaxTokens bounds one reply. Prep chat wants room for prose (a recap
	// relay, a set of hooks) — four times the voice default, still bounded.
	chatMaxTokens = 4096
	// exchangeTimeout bounds one whole exchange (all tool rounds; a recap tool
	// call alone may take up to its own 45s child budget).
	exchangeTimeout = 180 * time.Second
	// autoTitleRunes is how much of the first user message seeds an unnamed
	// thread's title.
	autoTitleRunes = 60
	// MaxMessageRunes bounds one user chat message — a belt-and-braces spend
	// guard (the message seasons a money-spending LLM call) and a junk-input
	// gate, mirroring the assist prompt cap.
	MaxMessageRunes = 4000
)

// ErrAllowanceExhausted is returned when the tenant's ADR-0055 monthly
// allowance is spent: the exchange is refused BEFORE any provider call, and
// the RPC layer surfaces it as a clear in-chat refusal (ADR-0062).
//
// It is an ALIAS for the metering seam's sentinel, not a second value: every
// off-session surface refuses with the same error, and two distinct sentinels
// would compare unequal under errors.Is and silently kill one mapper's branch.
var ErrAllowanceExhausted = billing.ErrAllowanceExhausted

// Store is the narrow storage surface the engine needs; *storage.Store
// satisfies it.
type Store interface {
	GetButler(ctx context.Context, campaignID uuid.UUID) (storage.Agent, error)
	ListToolGrantsFor(ctx context.Context, agentID uuid.UUID, surface storage.GrantSurface) ([]storage.ToolGrant, error)
	GetProviderConfig(ctx context.Context, id uuid.UUID) (storage.ProviderConfig, error)
	GetProviderConfigByComponent(ctx context.Context, tenantID uuid.UUID, component storage.Component) (storage.ProviderConfig, error)
	GetPlanningThread(ctx context.Context, campaignID, id uuid.UUID) (storage.PlanningThread, error)
	ListPlanningMessages(ctx context.Context, campaignID, threadID uuid.UUID) ([]storage.PlanningMessage, error)
	AppendPlanningMessage(ctx context.Context, campaignID, threadID uuid.UUID, role, content string) (storage.PlanningMessage, error)
	RenamePlanningThread(ctx context.Context, campaignID, id uuid.UUID, title string) (storage.PlanningThread, error)
	ListNodeAppearances(ctx context.Context, campaignID, nodeID uuid.UUID, limit int) ([]storage.AppearanceHit, error)
}

// Searcher is the #591 retrieval surface the tool belt reads; *search.Engine
// satisfies it.
type Searcher interface {
	SearchNodes(ctx context.Context, campaignID uuid.UUID, access search.Access, query string, limit int) ([]storage.KGNode, error)
	SearchTranscripts(ctx context.Context, campaignID uuid.UUID, access search.Access, query string, limit int) (search.TranscriptResults, error)
}

// ProviderFactory builds the LLM provider for a resolved (providerID, key) —
// the cassette/test seam, defaulting to [llmbuild.New] (the recap pattern).
type ProviderFactory func(providerID, apiKey string) (llm.Provider, error)

// Events receives one exchange's stream: prose deltas as they arrive and one
// call per executed tool. The RPC layer maps them onto stream frames. Text's
// error aborts the exchange (a closed stream).
type Events interface {
	Text(delta string) error
	Tool(name string, isErr bool)
}

// Result is one successful exchange's outcome: the persisted rows and the
// thread (whose title this exchange may have auto-filled).
type Result struct {
	UserMessage      storage.PlanningMessage
	AssistantMessage storage.PlanningMessage
	Thread           storage.PlanningThread
}

// Engine runs planning chat exchanges. Build once at boot with [NewEngine];
// safe for concurrent use (its deps are; per-exchange state is local).
type Engine struct {
	store    Store
	cipher   *crypto.Cipher
	search   Searcher
	writer   tool.KGWriter       // knowledge.Adapter — campaign-stamped per exchange
	recapper tool.Recapper       // knowledge.RecapAdapter — same stamping
	metering *billing.OffSession // gates + ledgers every exchange (#658)
	metrics  observe.StageRecorder
	log      *slog.Logger

	keyEnt      llmbuild.PlatformKeyEntitlement // nil = everything allowed
	newProvider ProviderFactory
	maxRounds   int
}

// Option configures NewEngine.
type Option func(*Engine)

// WithKeyEntitlement wires the platform-key entitlement (ADR-0054/0055) the
// env-fallback key resolution consults; nil grants everything (self-host).
func WithKeyEntitlement(ent llmbuild.PlatformKeyEntitlement) Option {
	return func(e *Engine) { e.keyEnt = ent }
}

// WithProviderFactory overrides how the LLM provider is constructed (tests).
func WithProviderFactory(f ProviderFactory) Option {
	return func(e *Engine) { e.newProvider = f }
}

// NewEngine builds the chat engine. store, searcher, writer, recapper and
// metering are wiring requirements; metering is the off-session seam that
// carries the ADR-0055 allowance gate (wired for open admission mode at the
// composition root) and the Usage Ledger write. cipher may be nil (BYOK saves
// then fail loudly at key resolution, the keyless posture). metrics/log
// default to no-op/slog.Default.
func NewEngine(store Store, cipher *crypto.Cipher, searcher Searcher, writer tool.KGWriter, recapper tool.Recapper, metering *billing.OffSession, metrics observe.StageRecorder, log *slog.Logger, opts ...Option) *Engine {
	if store == nil || searcher == nil || writer == nil || recapper == nil || metering == nil {
		panic("planchat: NewEngine: nil store, searcher, writer, recapper, or metering")
	}
	if metrics == nil {
		metrics = observe.Discard{}
	}
	if log == nil {
		log = slog.Default()
	}
	e := &Engine{
		store:       store,
		cipher:      cipher,
		search:      searcher,
		writer:      writer,
		recapper:    recapper,
		metering:    metering,
		metrics:     metrics,
		log:         log,
		newProvider: llmbuild.New,
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// Exchange runs one chat exchange in thread threadID of campaign: gate →
// resolve provider → persist the user message (crash-safe before any provider
// call) → run the tool loop, streaming through ev → persist the reply. The
// ledger flush and the attribution log run on success AND failure, so metered
// tokens are never orphaned (the #272 review rule).
func (e *Engine) Exchange(ctx context.Context, tenantID uuid.UUID, campaign storage.Campaign, threadID uuid.UUID, text string, ev Events) (Result, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Result{}, fmt.Errorf("planchat: empty message")
	}
	if utf8.RuneCountInString(text) > MaxMessageRunes {
		return Result{}, fmt.Errorf("planchat: message is too long")
	}

	thread, err := e.store.GetPlanningThread(ctx, campaign.ID, threadID)
	if err != nil {
		return Result{}, fmt.Errorf("planchat: load thread: %w", err)
	}

	// Open the exchange's metered call. The ADR-0055 allowance gate runs inside
	// Begin, BEFORE anything spends or persists, and a read error fails closed
	// (the session-start precedent): when the gate cannot be evaluated, the
	// exchange must not silently spend platform money. The call's recorder is
	// the per-exchange caps-free meter TEE'd with the durable Usage Ledger
	// (ADR-0062: flush per exchange).
	call, err := e.metering.Begin(ctx, tenantID, e.metrics)
	if err != nil {
		if errors.Is(err, ErrAllowanceExhausted) {
			return Result{}, err
		}
		return Result{}, fmt.Errorf("planchat: allowance check: %w", err)
	}
	rec := call.Recorder()

	butler, err := e.store.GetButler(ctx, campaign.ID)
	if err != nil {
		return Result{}, fmt.Errorf("planchat: load butler: %w", err)
	}

	grantRows, err := e.store.ListToolGrantsFor(ctx, butler.ID, storage.GrantSurfaceChat)
	if err != nil {
		return Result{}, fmt.Errorf("planchat: load chat grants: %w", err)
	}

	cfg, component, err := e.resolveChatLLMConfig(ctx, tenantID, butler)
	if err != nil {
		return Result{}, err
	}
	key, err := llmbuild.ResolveKeyGated(ctx, e.keyEnt, tenantID, e.cipher, cfg, component)
	if err != nil {
		return Result{}, err
	}
	providerID, model := "", ""
	if cfg != nil {
		providerID, model = cfg.Provider, cfg.Model
	}
	provider, err := e.newProvider(providerID, key)
	if err != nil {
		return Result{}, fmt.Errorf("planchat: build provider: %w", err)
	}
	// An explicit model keeps the adapter's usage metering priced on a real
	// model id — the (groq, "") price-map miss the recap engine also guards
	// against (#272 review).
	if model == "" {
		model = llmbuild.DefaultModel(providerID)
	}

	history, err := e.store.ListPlanningMessages(ctx, campaign.ID, threadID)
	if err != nil {
		return Result{}, fmt.Errorf("planchat: load history: %w", err)
	}

	// Persist the user message BEFORE the provider call: a crash mid-exchange
	// keeps the GM's words (ADR-0062's crash-safe posture). A failed exchange
	// leaves it in the thread — honest history, and the retry is a fresh turn.
	userMsg, err := e.store.AppendPlanningMessage(ctx, campaign.ID, threadID, storage.PlanningRoleUser, text)
	if err != nil {
		return Result{}, fmt.Errorf("planchat: persist user message: %w", err)
	}
	if thread.Title == "" {
		if renamed, err := e.store.RenamePlanningThread(ctx, campaign.ID, threadID, autoTitle(text)); err == nil {
			thread = renamed
		} else {
			e.log.Warn("planchat: auto-title failed", "thread_id", threadID, "err", err)
		}
	}

	registry, err := e.chatTools(campaign.ID)
	if err != nil {
		return Result{}, err
	}
	grants := tool.NewGrantSet(registry, storage.GrantsFromRows(grantRows)...)

	adapter := agenttool.NewToolProvider(provider, model, chatMaxTokens,
		agenttool.WithMetrics(rec, llmcall.ProviderLabel(providerID)))
	loop := tool.NewLoop(adapter, grants)
	if e.maxRounds > 0 {
		loop.MaxRounds = e.maxRounds
	}
	loop.OnToolResult = func(_ context.Context, name, _ string, isErr bool) {
		ev.Tool(name, isErr)
	}

	// The tool ctx: campaign-stamped for the knowledge writer / recapper
	// (knowledge.WithCampaign — the off-session path), caller-stamped so
	// propose_knowledge attributes proposals to the Butler (ADR-0029), and
	// bounded by the exchange timeout.
	runCtx := knowledge.WithCampaign(ctx, campaign.ID)
	runCtx = tool.WithCaller(runCtx, butler.ID.String())
	// The recap Tool's own LLM calls are this exchange's spend: hand the shared
	// recap engine this exchange's recorder so they reach its Usage Ledger and
	// estimate instead of only the process recorder.
	runCtx = recap.WithMetering(runCtx, recap.Metering{Rec: rec})
	runCtx, cancel := context.WithTimeout(runCtx, exchangeTimeout)
	defer cancel()

	answer, runErr := loop.RunStream(runCtx, buildMessages(butler, history, text), ev.Text)

	// Flush + attribute on BOTH paths: usage may have accrued before a failure,
	// and losing it silently is the one thing the ledger may not do. Close
	// flushes detached from the (possibly cancelled/disconnected) exchange ctx
	// and logs a failure itself.
	flushErr := call.Close(ctx)
	e.log.Info("planchat: llm usage",
		"tenant_id", tenantID,
		"campaign_id", campaign.ID,
		"thread_id", threadID,
		"estimated_usd", call.EstimatedUSD(),
		"flushed", flushErr == nil,
		"ok", runErr == nil,
	)

	if runErr != nil {
		return Result{}, fmt.Errorf("planchat: exchange: %w", runErr)
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return Result{}, fmt.Errorf("planchat: exchange: %w", errEmptyAnswer)
	}

	asstMsg, err := e.store.AppendPlanningMessage(ctx, campaign.ID, threadID, storage.PlanningRoleAssistant, answer)
	if err != nil {
		return Result{}, fmt.Errorf("planchat: persist assistant message: %w", err)
	}
	return Result{UserMessage: userMsg, AssistantMessage: asstMsg, Thread: thread}, nil
}

// errEmptyAnswer marks a model that returned only whitespace — surfaced as a
// retryable failure, never persisted as an empty assistant turn.
var errEmptyAnswer = errors.New("the model returned an empty reply")

// resolveChatLLMConfig walks the ADR-0062 chat ladder: the Butler's chat slot →
// the tenant 'chat_llm' Provider Config → the tenant 'llm' config → nil (the
// Groq env default, ADR-0036). The returned component labels key-resolution
// errors and the entitlement check with the slot that actually resolved.
func (e *Engine) resolveChatLLMConfig(ctx context.Context, tenantID uuid.UUID, butler storage.Agent) (*storage.ProviderConfig, storage.Component, error) {
	if butler.ChatLLMProviderConfigID.Valid {
		pc, err := e.store.GetProviderConfig(ctx, butler.ChatLLMProviderConfigID.UUID)
		if err != nil && !errors.Is(err, storage.ErrNotFound) {
			return nil, "", fmt.Errorf("planchat: load butler chat provider config: %w", err)
		}
		if err == nil {
			return &pc, pc.Component, nil
		}
		// Config row gone (SET NULL race): fall through the ladder.
	}
	for _, component := range []storage.Component{storage.ComponentChatLLM, storage.ComponentLLM} {
		pc, err := e.store.GetProviderConfigByComponent(ctx, tenantID, component)
		switch {
		case errors.Is(err, storage.ErrNotFound):
			continue
		case err != nil:
			return nil, "", fmt.Errorf("planchat: load tenant %s provider config: %w", component, err)
		default:
			return &pc, component, nil
		}
	}
	return nil, storage.ComponentChatLLM, nil // env default (Groq, ADR-0036)
}

// autoTitle derives an unnamed thread's title from its first user message.
func autoTitle(text string) string {
	line := text
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSpace(line)
	if utf8.RuneCountInString(line) > autoTitleRunes {
		line = string([]rune(line)[:autoTitleRunes]) + "…"
	}
	return line
}

// Compile-time pins: *storage.Store satisfies every narrow store seam this
// package declares, so a drift in a store method fails THIS package's build
// instead of surfacing only at the composition root (CONTRIBUTING: interface
// assertions).
var (
	_ Store = (*storage.Store)(nil)
)
