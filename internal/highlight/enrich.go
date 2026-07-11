package highlight

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/blob"
	"github.com/MrWong99/Glyphoxa/internal/imagegen"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// JobKindEnrichImage is the background-job kind that generates an AI image for a
// promoted Highlight and lands it on the row through the blob seam (#311, Epic 8,
// ADR-0004 amendment / ADR-0049). PromoteHighlight enqueues it; the handler is
// idempotent + at-least-once.
const JobKindEnrichImage = "highlight.enrich_image"

// imageBlobName is the blob.Key name segment for a Highlight's generated image
// (mirrors the clip's "clip.wav"). The key is t/<tenant>/highlight/<id>/image.
const imageBlobName = "image"

// excerptPromptLimit bounds the transcript excerpt folded into the image prompt
// (runes, not bytes) so a long moment cannot blow up the request.
const excerptPromptLimit = 1000

// ErrImageNotConfigured is the sentinel a [GeneratorFactory] returns when the
// tenant has no image provider key (no BYOK row AND no GEMINI_API_KEY). The
// handler treats it as a clean no-op: the Highlight stays intact without media
// (AC), no retry, no spend.
var ErrImageNotConfigured = errors.New("highlight: image generation is not configured")

// GeneratorFactory builds the tenant's image [imagegen.Generator] and returns the
// model id to meter against. It resolves the BYOK key under the hybrid policy
// (ADR-0039) and returns [ErrImageNotConfigured] when no key is available. main.go
// wires it from the store + cipher; tests fake it.
type GeneratorFactory func(ctx context.Context, tenantID uuid.UUID) (gen imagegen.Generator, model string, err error)

// enrichPayload is the enrichment job's payload: which Highlight to enrich and
// the tenant that owns it (the handler carries no ambient tenant, ADR-0049).
type enrichPayload struct {
	HighlightID uuid.UUID `json:"highlight_id"`
	TenantID    uuid.UUID `json:"tenant_id"`
}

// MarshalEnrichImage builds the JobKindEnrichImage payload PromoteHighlight
// enqueues. The RPC layer marshals it through its enqueuer seam.
func MarshalEnrichImage(highlightID, tenantID uuid.UUID) ([]byte, error) {
	return json.Marshal(enrichPayload{HighlightID: highlightID, TenantID: tenantID})
}

// EnrichStore is the storage surface the enrichment handler needs; *storage.Store
// satisfies it and tests fake it. GetHighlight is tenant-scoped; SetHighlightImage
// lands the result (ErrNotFound if the row was deleted meanwhile).
type EnrichStore interface {
	GetHighlight(ctx context.Context, tenantID, id uuid.UUID) (storage.Highlight, error)
	SetHighlightImage(ctx context.Context, id uuid.UUID, imageKey, contentType string, sizeBytes int64) error
}

// EnrichImageHandler builds the JobKindEnrichImage handler (ADR-0049). It is
// idempotent + at-least-once:
//
//   - the Highlight is gone (deleted before the job ran) → nil (done).
//   - the Highlight already has an image → nil (no double spend on a re-run).
//   - image generation is not configured for the tenant → log + nil (the
//     Highlight stays intact without media, AC — never a retry loop on a missing
//     key).
//   - otherwise generate → meter usage (caps-free spend meter teed onto the
//     production recorder; Gemini bills the image as output tokens, so it prices
//     through LLMTokens — no image-specific meter, #311) → store the image behind
//     the blob seam at the deterministic key → record it on the row. A row that
//     vanished between the read and the write compensates the orphaned blob and
//     returns nil (done). A provider or blob error returns so the runner retries /
//     dead-letters — the row is never mutated, so the Highlight is intact (AC).
func EnrichImageHandler(store EnrichStore, blobs blob.Store, factory GeneratorFactory, rec observe.StageRecorder, log *slog.Logger) func(context.Context, json.RawMessage) error {
	if log == nil {
		log = slog.Default()
	}
	if rec == nil {
		rec = observe.Discard{}
	}
	return func(ctx context.Context, payload json.RawMessage) error {
		var p enrichPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("highlight enrich: decode payload: %w", err)
		}

		h, err := store.GetHighlight(ctx, p.TenantID, p.HighlightID)
		if errors.Is(err, storage.ErrNotFound) {
			// The Highlight was deleted before enrichment ran: nothing to do.
			return nil
		}
		if err != nil {
			return fmt.Errorf("highlight enrich: load highlight %s: %w", p.HighlightID, err)
		}
		if h.ImageKey != "" {
			// Already enriched (a re-run of an at-least-once job): stop before any
			// spend so the same Highlight is never billed twice.
			return nil
		}

		gen, model, err := factory(ctx, p.TenantID)
		if errors.Is(err, ErrImageNotConfigured) {
			log.Info("highlight enrich: image generation not configured, leaving highlight without media",
				"highlight", p.HighlightID, "tenant", p.TenantID)
			return nil
		}
		if err != nil {
			return fmt.Errorf("highlight enrich: build generator: %w", err)
		}

		res, err := gen.Generate(ctx, buildImagePrompt(h.Excerpt, h.Reason))
		if err != nil {
			// Provider error: return it so the runner retries / dead-letters. The row
			// is untouched — the Highlight keeps its clip and stays imageless (AC).
			return fmt.Errorf("highlight enrich: generate image: %w", err)
		}

		// Meter the spend (ADR-0045/0046): a caps-free meter teed onto the production
		// recorder prices the image's tokens AND moves the glyphoxa_voice_llm_tokens
		// series. The enrichment job is off-session and never cap-gated (the recap
		// posture). Gemini bills the image as output tokens → LLMTokens.
		meter := spend.NewMeter(spend.Caps{}, log, nil, nil)
		observe.TeeUsage(rec, meter).LLMTokens(observe.ProviderGemini, model, res.PromptTokens, res.OutputTokens)
		log.Info("highlight enrich: image generated",
			"highlight", p.HighlightID,
			"model", model,
			"input_tokens", res.PromptTokens,
			"output_tokens", res.OutputTokens,
			"estimated_usd", meter.Status().EstimatedUSD,
		)

		key, err := blob.Key(p.TenantID, "highlight", p.HighlightID, imageBlobName)
		if err != nil {
			return fmt.Errorf("highlight enrich: build image key: %w", err)
		}
		if err := blobs.Put(ctx, key, res.ContentType, bytes.NewReader(res.Data), int64(len(res.Data))); err != nil {
			return fmt.Errorf("highlight enrich: store image: %w", err)
		}
		if err := store.SetHighlightImage(ctx, p.HighlightID, key, res.ContentType, int64(len(res.Data))); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				// The Highlight was deleted between the read and this write: the image
				// blob we just stored is now an orphan. Compensate through the seam
				// (ADR-0048) and treat the job as done — there is no row to enrich.
				if derr := blobs.Delete(ctx, key); derr != nil {
					log.Error("highlight enrich: compensate orphan image", "err", derr, "key", key)
				}
				return nil
			}
			return fmt.Errorf("highlight enrich: record image on highlight: %w", err)
		}
		return nil
	}
}

// buildImagePrompt renders the fixed image prompt from a Highlight's caption
// material (#311). Speaker IDs are deliberately NOT used. The excerpt is truncated
// to excerptPromptLimit runes.
func buildImagePrompt(excerpt, reason string) string {
	return fmt.Sprintf(
		"Illustrate this tabletop RPG moment as a single dramatic fantasy scene, no text or lettering in the image. Moment: %s. Why it is memorable: %s.",
		truncateRunes(excerpt, excerptPromptLimit), reason)
}

// truncateRunes returns s truncated to at most n runes.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	var b strings.Builder
	i := 0
	for _, r := range s {
		if i >= n {
			break
		}
		b.WriteRune(r)
		i++
	}
	return b.String()
}
