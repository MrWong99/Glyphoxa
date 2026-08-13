// Package portraitgen turns a Knowledge Graph entry's own prose into a portrait
// image, on demand (#590).
//
// It is the third consumer of the image seam #311 built for Session Highlights
// and #541 reused for generated maps, and it deliberately reuses that seam
// whole: the same [imagegen.Generator], the same tenant BYOK resolution, the
// same [spend.PriceOnly] metering ritual. A third image path with its own key
// handling and its own meter would be a third place to get ADR-0039 and
// ADR-0045 wrong.
//
// The lifecycle is mapgen's, not Highlight enrichment's: a synchronous call
// that writes NOTHING. The bytes go back to the GM as a draft they preview and
// then either apply — through the ordinary SetNodePortrait path, which is what
// puts them in the blob seam — or discard, in which case nothing anywhere ever
// knew about them. "Nothing hits the row before the GM applies" is true by
// construction rather than by discipline.
package portraitgen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/imagegen"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// ErrNotConfigured is returned when the tenant has no image provider key. It is
// an actionable refusal, not a failure: the GM is told to add a key.
//
// An ALIAS for imagegen.ErrNotConfigured — the one value the shared factory
// actually returns (see mapgen.ErrNotConfigured for the bug a second sentinel
// caused).
var ErrNotConfigured = imagegen.ErrNotConfigured

// GeneratorFactory builds the tenant's image generator and returns the model id
// to meter against. Structurally identical to highlight.GeneratorFactory and
// mapgen.GeneratorFactory; main.go wires ONE closure into all three — another
// copy would be another place for the ADR-0039 hybrid key policy to drift.
type GeneratorFactory func(ctx context.Context, tenantID uuid.UUID) (gen imagegen.Generator, model string, err error)

// SeedReader supplies the wiki context the prompt is built from.
// *storage.Store satisfies it.
//
// The read is PUBLIC-ONLY by construction (public Aspects only, gm_private
// entries refused) — a portrait is an artefact the GM shows the table, so
// seeding its prompt from GM secrets would launder them into a picture that
// cannot be un-seen (the MapSeedContext rationale, verbatim).
type SeedReader interface {
	PortraitSeedContext(ctx context.Context, campaignID, nodeID uuid.UUID) (storage.KGNode, error)
}

// Input is one generation request.
type Input struct {
	// NodeID is the entry the portrait depicts; its public prose and facts seed
	// the prompt. Always present — unlike a map, a portrait is OF something the
	// wiki knows.
	NodeID uuid.UUID
	// Prompt is the GM's optional extra direction ("weathered, mid-laugh").
	// Empty is fine: the entry's own prose is the prompt.
	Prompt string
}

// Result is a generated draft: bytes the caller hands to the GM, never to
// storage.
type Result struct {
	Data        []byte
	ContentType string
	Model       string
	// Prompt is the fully composed prompt that produced the image, returned so
	// the GM can see what was actually asked for rather than only what they
	// typed.
	Prompt string
}

// Engine generates portrait drafts.
type Engine struct {
	factory GeneratorFactory
	seeds   SeedReader
	rec     observe.StageRecorder
	log     *slog.Logger
}

// New builds the engine. A nil recorder discards metrics; a nil logger uses the
// default. A nil factory or seed reader is a wiring bug and panics at
// construction rather than at the GM's first click.
func New(factory GeneratorFactory, seeds SeedReader, rec observe.StageRecorder, log *slog.Logger) *Engine {
	if factory == nil {
		panic("portraitgen: New: nil generator factory")
	}
	if seeds == nil {
		panic("portraitgen: New: nil seed reader")
	}
	if rec == nil {
		rec = observe.Discard{}
	}
	if log == nil {
		log = slog.Default()
	}
	return &Engine{factory: factory, seeds: seeds, rec: rec, log: log}
}

// Generate produces one portrait draft and meters what it cost.
//
// It writes nothing: no row, no blob, no job. The only durable effect is the
// usage metering, which is deliberate — the tokens were spent whether or not
// the GM keeps the picture, and a discarded draft that billed nothing would
// make the ledger lie (the mapgen posture, ADR-0045/0046).
func (e *Engine) Generate(ctx context.Context, campaign storage.Campaign, in Input) (Result, error) {
	if in.NodeID == uuid.Nil {
		return Result{}, fmt.Errorf("portraitgen: node id must be set")
	}

	node, err := e.seeds.PortraitSeedContext(ctx, campaign.ID, in.NodeID)
	if errors.Is(err, storage.ErrNotFound) {
		return Result{}, fmt.Errorf("portraitgen: %w", storage.ErrNotFound)
	}
	if err != nil {
		return Result{}, fmt.Errorf("portraitgen: seed context: %w", err)
	}

	gen, model, err := e.factory(ctx, campaign.TenantID)
	if err != nil {
		// ErrNotConfigured and an entitlement refusal both travel intact — the
		// caller maps each onto an actionable code, and swallowing the latter
		// would spend the deployment's key on a tenant that is not entitled to it.
		return Result{}, err
	}

	full := buildPrompt(campaign, node, strings.TrimSpace(in.Prompt))
	res, err := gen.Generate(ctx, full)
	if err != nil {
		// ErrImageTooLarge is PERMANENT: the same prompt re-bills the same
		// oversize generation. It travels up unwrapped so the caller can refuse
		// rather than retry, and it is deliberately NOT metered here — the
		// provider already billed it, and this path has no token counts to price.
		return Result{}, err
	}

	// The metering ritual, identical to mapgen and Highlight enrichment
	// (ADR-0045/0046): PriceOnly tees a caps-free meter onto the production
	// recorder. Generation is off-session, so it is priced and attributed rather
	// than cap-gated.
	priced, estimatedUSD := spend.PriceOnly(e.rec, e.log)
	priced.LLMTokens(observe.ProviderGemini, model, res.PromptTokens, res.OutputTokens)
	e.log.Info("portraitgen: portrait generated",
		"campaign_id", campaign.ID,
		"tenant_id", campaign.TenantID,
		"node_id", in.NodeID,
		"model", model,
		"input_tokens", res.PromptTokens,
		"output_tokens", res.OutputTokens,
		"estimated_usd", estimatedUSD())

	return Result{Data: res.Data, ContentType: res.ContentType, Model: model, Prompt: full}, nil
}
