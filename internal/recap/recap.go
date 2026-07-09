// Package recap is the one-shot Voice Session summarization service (#272, E3):
// given one or more Voice Session ids it renders their Transcript Lines (ADR-0040)
// and asks the campaign's Butler-flavoured LLM to produce a coherent recap. It is a
// plain service — NO Tool registration (ADR-0028/0029); Epic 7 wraps it later.
//
// Decisions (gate #271): the LLM provider is resolved from the Butler's
// llm_provider_config_id, falling back to the Tenant 'llm' Provider Config, then to
// the Groq env default (ADR-0036) — via the shared [internal/llmbuild] helper.
// Recaps are REGENERATED per request and never persisted (no schema touch). A short
// session is one Butler-flavoured call; an over-budget one is map-reduced over
// windows of whole Lines. Usage is METERED (attributed to the recapped Voice Session
// ids via metrics + a structured log) but NEVER cap-gated — recap never reads a
// tenant cap nor calls AllowTurn (ADR-0046 is live-session-only).
package recap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/llmbuild"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
)

const (
	// singleCallBudgetChars is the conservative per-session rendered-character ceiling
	// under which a whole session is recapped in ONE Butler-flavoured call. Above it,
	// the session is map-reduced over windows (gate #271 long-session strategy).
	singleCallBudgetChars = 24_000
	// windowChars caps one map window's rendered characters; windows hold WHOLE Lines.
	windowChars = 20_000
	// maxWindows bounds the map fan-out; a transcript needing more fails loudly rather
	// than launching an unbounded number of calls.
	maxWindows = 32

	mapMaxTokens    = 512
	reduceMaxTokens = 1024
	singleMaxTokens = 1024

	sessionHeaderTimeFormat = "2006-01-02 15:04"
)

var (
	// ErrNoTranscript is returned when a single-session recap has no Lines, or every
	// session in a multi-session recap is empty — there is nothing to summarize.
	ErrNoTranscript = errors.New("recap: no transcript lines to summarize")
	// ErrMixedCampaigns is returned when the requested sessions do not all belong to
	// one Campaign — a recap is scoped to a single campaign's Butler and Language.
	ErrMixedCampaigns = errors.New("recap: sessions span multiple campaigns")
	// ErrTranscriptTooLong is returned when a session's transcript needs more than
	// maxWindows map windows — beyond the bounded single-pass map-reduce.
	ErrTranscriptTooLong = errors.New("recap: transcript exceeds the maximum window count")
)

// Store is the read surface the engine needs; *storage.Store satisfies it (all six
// methods already exist on the concrete store — no wrapper needed).
type Store interface {
	GetVoiceSession(ctx context.Context, id uuid.UUID) (storage.VoiceSession, error)
	ListTranscriptLines(ctx context.Context, sessionID uuid.UUID) ([]storage.TranscriptLine, error)
	GetCampaign(ctx context.Context, id uuid.UUID) (storage.Campaign, error)
	GetButler(ctx context.Context, campaignID uuid.UUID) (storage.Agent, error)
	GetProviderConfig(ctx context.Context, id uuid.UUID) (storage.ProviderConfig, error)
	GetProviderConfigByComponent(ctx context.Context, tenantID uuid.UUID, component storage.Component) (storage.ProviderConfig, error)
}

// The concrete store satisfies Store with no thin wrappers (#272 contract).
var _ Store = (*storage.Store)(nil)

// Result is a completed recap. Text is the recap prose; SessionIDs are the recapped
// sessions in chronological (started_at) order; Windowed reports whether any session
// took the map-reduce path.
type Result struct {
	Text       string
	SessionIDs []uuid.UUID
	Windowed   bool
}

// ProviderFactory builds an [llm.Provider] from a provider id and API key — the
// cassette seam ([WithProviderFactory]); it defaults to [llmbuild.New].
type ProviderFactory func(providerID, apiKey string) (llm.Provider, error)

// Engine summarizes Voice Sessions. Construct with [NewEngine].
type Engine struct {
	st          Store
	cipher      *crypto.Cipher
	metrics     observe.StageRecorder
	log         *slog.Logger
	newProvider ProviderFactory
}

// Option customises an [Engine].
type Option func(*Engine)

// WithProviderFactory overrides the LLM provider constructor — the cassette seam a
// deterministic test injects to replay a recorded completion (ADR-0021). The default
// is [llmbuild.New].
func WithProviderFactory(f func(providerID, apiKey string) (llm.Provider, error)) Option {
	return func(e *Engine) { e.newProvider = f }
}

// NewEngine builds a recap Engine. cipher decrypts a BYOK LLM key (nil is fine for
// the env/default path); metrics receives usage (nil -> discard); log receives the
// attribution line (nil -> discard).
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

// Recap summarizes the given Voice Sessions. The sessions must share one Campaign
// (else [ErrMixedCampaigns]); they are recapped in chronological order, each
// independently, and joined with per-session headers when there is more than one. A
// single empty session — or all-empty in a multi-session set — yields
// [ErrNoTranscript].
func (e *Engine) Recap(ctx context.Context, sessionIDs []uuid.UUID) (Result, error) {
	if len(sessionIDs) == 0 {
		return Result{}, ErrNoTranscript
	}

	sessions := make([]storage.VoiceSession, 0, len(sessionIDs))
	for _, id := range sessionIDs {
		vs, err := e.st.GetVoiceSession(ctx, id)
		if err != nil {
			return Result{}, fmt.Errorf("recap: load voice session %s: %w", id, err)
		}
		sessions = append(sessions, vs)
	}

	campaignID := sessions[0].CampaignID
	for _, vs := range sessions[1:] {
		if vs.CampaignID != campaignID {
			return Result{}, ErrMixedCampaigns
		}
	}
	// Chronological order: multi-session join and the reported SessionIDs both use it.
	sort.SliceStable(sessions, func(i, j int) bool {
		return sessions[i].StartedAt.Before(sessions[j].StartedAt)
	})

	campaign, err := e.st.GetCampaign(ctx, campaignID)
	if err != nil {
		return Result{}, fmt.Errorf("recap: load campaign %s: %w", campaignID, err)
	}
	butler, err := e.st.GetButler(ctx, campaignID)
	if err != nil {
		return Result{}, fmt.Errorf("recap: load butler for campaign %s: %w", campaignID, err)
	}

	cfg, err := e.resolveLLMConfig(ctx, campaign.TenantID, butler)
	if err != nil {
		return Result{}, err
	}
	key, err := llmbuild.ResolveKey(e.cipher, cfg, storage.ComponentLLM)
	if err != nil {
		return Result{}, err
	}
	providerID, model := "", ""
	if cfg != nil {
		providerID, model = cfg.Provider, cfg.Model
	}
	provider, err := e.newProvider(providerID, key)
	if err != nil {
		return Result{}, fmt.Errorf("recap: build llm provider: %w", err)
	}

	// Metering (gate #271 posture): a caps-free meter teed alongside the production
	// recorder — usage is recorded and priced, but the meter has NO caps and the
	// engine never reads one or calls AllowTurn (ADR-0046 is live-session-only).
	meter := spend.NewMeter(spend.Caps{}, e.log, nil, nil)
	caller := &llmCaller{
		ctx:      ctx,
		provider: provider,
		model:    model,
		label:    providerLabel(providerID),
		rec:      observe.TeeUsage(e.metrics, meter),
	}

	butlerSys := butlerSystemPrompt(butler.Persona, campaign.Language)
	neutralSys := neutralSystemPrompt(campaign.Language)

	chronIDs := make([]uuid.UUID, len(sessions))
	for i, vs := range sessions {
		chronIDs[i] = vs.ID
	}
	multi := len(sessions) > 1

	var parts []string
	anyWindowed := false
	produced := false
	for _, vs := range sessions {
		lines, err := e.st.ListTranscriptLines(ctx, vs.ID)
		if err != nil {
			return Result{}, fmt.Errorf("recap: load transcript for session %s: %w", vs.ID, err)
		}
		header := "**Session " + vs.StartedAt.UTC().Format(sessionHeaderTimeFormat) + " UTC**"
		if len(lines) == 0 {
			if !multi {
				return Result{}, ErrNoTranscript
			}
			// A zero-line session in a multi-set is skipped with a note, not dropped —
			// the reader sees the gap rather than a silently missing header.
			parts = append(parts, header+"\n_(no transcript lines recorded for this session)_")
			continue
		}
		text, windowed, err := e.recapSession(caller, butlerSys, neutralSys, lines)
		if err != nil {
			return Result{}, err
		}
		produced = true
		anyWindowed = anyWindowed || windowed
		if multi {
			parts = append(parts, header+"\n"+text)
		} else {
			parts = append(parts, text)
		}
	}
	if !produced {
		return Result{}, ErrNoTranscript
	}

	status := meter.Status()
	e.log.Info("recap: llm usage",
		"voice_session_ids", chronIDs,
		"input_tokens", caller.totalIn,
		"output_tokens", caller.totalOut,
		"estimated_usd", status.EstimatedUSD,
	)

	return Result{
		Text:       strings.Join(parts, "\n\n"),
		SessionIDs: chronIDs,
		Windowed:   anyWindowed,
	}, nil
}

// recapSession recaps ONE session's Lines. At or under the single-call budget it is
// one Butler-flavoured call; above it, a map-reduce: each window of whole Lines is
// mapped with a NEUTRAL factual prompt, then the ordered partials are reduced by one
// Butler-flavoured call. Windowed reports which path ran.
func (e *Engine) recapSession(c *llmCaller, butlerSys, neutralSys string, lines []storage.TranscriptLine) (text string, windowed bool, err error) {
	rendered := renderLines(lines)
	if utf8.RuneCountInString(rendered) <= singleCallBudgetChars {
		text, err = c.call(butlerSys, rendered, singleMaxTokens)
		return text, false, err
	}

	windows := splitWindows(lines, windowChars)
	if len(windows) > maxWindows {
		return "", false, ErrTranscriptTooLong
	}
	partials := make([]string, 0, len(windows))
	for _, w := range windows {
		p, err := c.call(neutralSys, renderLines(w), mapMaxTokens)
		if err != nil {
			return "", false, err
		}
		partials = append(partials, p)
	}
	reduced, err := c.call(butlerSys, strings.Join(partials, "\n\n"), reduceMaxTokens)
	return reduced, true, err
}

// resolveLLMConfig resolves the LLM Provider Config for the recap (gate #271):
// the Butler's llm_provider_config_id if set, else the Tenant's 'llm' Provider
// Config, else nil (the Groq env default, ADR-0036).
func (e *Engine) resolveLLMConfig(ctx context.Context, tenantID uuid.UUID, butler storage.Agent) (*storage.ProviderConfig, error) {
	if butler.LLMProviderConfigID.Valid {
		pc, err := e.st.GetProviderConfig(ctx, butler.LLMProviderConfigID.UUID)
		if err != nil {
			return nil, fmt.Errorf("recap: load butler LLM provider config: %w", err)
		}
		return &pc, nil
	}
	pc, err := e.st.GetProviderConfigByComponent(ctx, tenantID, storage.ComponentLLM)
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return nil, nil // no config -> default Groq + env key
	case err != nil:
		return nil, fmt.Errorf("recap: load tenant LLM provider config: %w", err)
	default:
		return &pc, nil
	}
}

// providerLabel maps a provider_config id to the bounded [observe.Provider] metric
// label; the empty id (default) is Groq (ADR-0036). The three wired ids equal their
// observe constants, so the cast is exact.
func providerLabel(providerID string) observe.Provider {
	if providerID == "" {
		return observe.ProviderGroq
	}
	return observe.Provider(providerID)
}
