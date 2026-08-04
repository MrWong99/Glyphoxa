// Package mapgen turns a GM's prompt into a map image, on demand (#541,
// ADR-0060, ADR-0004 amendment).
//
// It is the second consumer of the image seam #311 built for Session Highlights,
// and it deliberately reuses that seam whole: the same [imagegen.Generator], the
// same tenant BYOK resolution, the same [spend.PriceOnly] metering ritual. A
// second image path with its own key handling and its own meter would be two
// places to get ADR-0039 and ADR-0045 wrong.
//
// What is DIFFERENT from Highlight enrichment is the lifecycle, and it is the
// whole point of the slice: enrichment is a durable job that writes its result,
// while this is a synchronous call that writes NOTHING. The bytes go back to the
// GM as a draft they preview and then either save — through the ordinary
// CreateMap path, which is what puts them in the blob seam — or discard, in which
// case nothing anywhere ever knew about them. That is ADR-0049's own rule applied
// (a job is for work with a real durability need and nothing to re-trigger it; a
// button the operator clicks and waits on is synchronous), and it is what makes
// "nothing hits campaign_map before the GM applies" true by construction rather
// than by discipline.
package mapgen

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
// actually returns. Declaring a second one here made this whole branch dead in
// production while its unit test, which injected THIS sentinel through a fake
// factory, passed happily.
var ErrNotConfigured = imagegen.ErrNotConfigured

// GeneratorFactory builds the tenant's image generator and returns the model id
// to meter against. It is structurally identical to highlight.GeneratorFactory
// and main.go wires ONE closure into both — a second copy would be a second
// place for the ADR-0039 hybrid key policy to drift.
type GeneratorFactory func(ctx context.Context, tenantID uuid.UUID) (gen imagegen.Generator, model string, err error)

// SeedReader supplies the wiki context a location-seeded prompt is built from.
// *storage.Store satisfies it.
//
// The read is PUBLIC-ONLY by construction. A generated map is an artefact the GM
// shows the table, so seeding its prompt from gm_private prose or from the names
// of gm_private neighbours would launder a secret into a picture — and unlike a
// prompt, a picture cannot be un-seen or filtered later.
type SeedReader interface {
	MapSeedContext(ctx context.Context, campaignID, nodeID uuid.UUID) (storage.KGNode, []string, error)
}

// Input is one generation request.
type Input struct {
	// Prompt is the GM's own words. Always present.
	Prompt string
	// AnchorNodeID optionally seeds the prompt from a Location entry: its prose
	// and the names of what resides in it, so the picture matches the wiki instead
	// of inventing a second world.
	AnchorNodeID uuid.NullUUID
}

// Result is a generated draft: bytes the caller hands to the GM, never to storage.
type Result struct {
	Data        []byte
	ContentType string
	Model       string
	// Prompt is the fully composed prompt that produced the image, returned so the
	// GM can see what was actually asked for rather than only what they typed.
	Prompt string
}

// Engine generates map drafts.
type Engine struct {
	factory GeneratorFactory
	seeds   SeedReader
	// text backs the pin suggester (suggest.go); nil leaves suggestions
	// unavailable. Attached with WithTextCaller so the image path can be composed
	// without a text provider at all.
	text TextCaller
	rec  observe.StageRecorder
	log  *slog.Logger
}

// New builds the engine. A nil recorder discards metrics; a nil logger uses the
// default. A nil factory is a wiring bug and panics at construction rather than
// at the GM's first click.
func New(factory GeneratorFactory, seeds SeedReader, rec observe.StageRecorder, log *slog.Logger) *Engine {
	if factory == nil {
		panic("mapgen: New: nil generator factory")
	}
	if rec == nil {
		rec = observe.Discard{}
	}
	if log == nil {
		log = slog.Default()
	}
	return &Engine{factory: factory, seeds: seeds, rec: rec, log: log}
}

// Generate produces one map draft and meters what it cost.
//
// It writes nothing: no row, no blob, no job. The only durable effect is the
// usage metering, which is deliberate — the tokens were spent whether or not the
// GM keeps the picture, and a discarded draft that billed nothing would make the
// ledger lie.
func (e *Engine) Generate(ctx context.Context, campaign storage.Campaign, in Input) (Result, error) {
	prompt := strings.TrimSpace(in.Prompt)
	if prompt == "" {
		return Result{}, fmt.Errorf("mapgen: prompt must not be empty")
	}

	var seed seedContext
	if in.AnchorNodeID.Valid {
		if e.seeds == nil {
			return Result{}, fmt.Errorf("mapgen: seeding from an entry is unavailable in this mode")
		}
		node, residents, err := e.seeds.MapSeedContext(ctx, campaign.ID, in.AnchorNodeID.UUID)
		if errors.Is(err, storage.ErrNotFound) {
			return Result{}, fmt.Errorf("mapgen: %w", storage.ErrNotFound)
		}
		if err != nil {
			return Result{}, fmt.Errorf("mapgen: seed context: %w", err)
		}
		seed = seedContext{Name: node.Name, Body: node.Body, Residents: residents}
	}

	gen, model, err := e.factory(ctx, campaign.TenantID)
	if errors.Is(err, ErrNotConfigured) {
		return Result{}, err
	}
	if err != nil {
		// An entitlement refusal arrives here and must travel intact — the caller
		// maps it onto an actionable code, and swallowing it would spend the
		// deployment's key on a tenant that is not entitled to it.
		return Result{}, err
	}

	full := buildPrompt(campaign, prompt, seed)
	res, err := gen.Generate(ctx, full)
	if err != nil {
		// ErrImageTooLarge is PERMANENT: the same prompt re-bills the same oversize
		// generation. It travels up unwrapped so the caller can refuse rather than
		// retry, and it is deliberately NOT metered here — the provider already
		// billed it, and this path has no token counts to price.
		return Result{}, err
	}

	// The metering ritual, identical to Highlight enrichment (ADR-0045/0046):
	// PriceOnly tees a caps-free meter onto the production recorder, prices the
	// tokens and moves the same series. Generation is off-session, so it is priced
	// and attributed rather than cap-gated — ADR-0046's 2026-07-09 amendment scopes
	// the live cap to a running Voice Session, and pretending otherwise here would
	// invent a second gating rule in a slice that should not own one.
	priced, estimatedUSD := spend.PriceOnly(e.rec, e.log)
	priced.LLMTokens(observe.ProviderGemini, model, res.PromptTokens, res.OutputTokens)
	e.log.Info("mapgen: map image generated",
		"campaign_id", campaign.ID,
		"tenant_id", campaign.TenantID,
		"model", model,
		"input_tokens", res.PromptTokens,
		"output_tokens", res.OutputTokens,
		"estimated_usd", estimatedUSD())

	return Result{Data: res.Data, ContentType: res.ContentType, Model: model, Prompt: full}, nil
}
