package highlight

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
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

// highlightOwnerKind is the blob.Key owner-kind segment a Highlight's blobs live
// under (t/<tenant>/highlight/<id>/<name>): both its audio clip and its generated
// image. The orphan-image sweep scopes to this owner-kind.
const highlightOwnerKind = "highlight"

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

// enrichClaimTTL is how long a claim on a Highlight's image enrichment stays
// valid before it is reclaimable (#406). It MUST exceed the runner's per-handler
// lease deadline (ADR-0049 default 5m) so a live winner is never reclaimed
// mid-generation, while a crashed winner's claim eventually frees and a later
// retry (or boot re-enqueue) re-drives the enrichment.
const enrichClaimTTL = 10 * time.Minute

// EnrichStore is the storage surface the enrichment handler needs; *storage.Store
// satisfies it and tests fake it. GetHighlight is tenant-scoped; SetHighlightImage
// lands the result (ErrNotFound if the row was deleted meanwhile).
// TryClaimHighlightEnrich / ReleaseHighlightEnrichClaim implement the race-proof
// claim (#406): a conditional state transition so two concurrent enrich jobs for
// the same Highlight run the provider Generate AT MOST once.
type EnrichStore interface {
	GetHighlight(ctx context.Context, tenantID, id uuid.UUID) (storage.Highlight, error)
	SetHighlightImage(ctx context.Context, id uuid.UUID, imageKey, contentType string, sizeBytes int64) error
	// TryClaimHighlightEnrich atomically claims the enrichment of an imageless
	// Highlight: it reports true iff THIS caller won the claim (the row is still
	// imageless and no fresh claim within ttl is held). A false (no error) means a
	// concurrent worker holds the claim or the row was enriched meanwhile.
	TryClaimHighlightEnrich(ctx context.Context, id uuid.UUID, ttl time.Duration) (bool, error)
	// ReleaseHighlightEnrichClaim clears a claim so a retry (or a later
	// re-promotion) can re-claim without waiting out the ttl. Idempotent.
	ReleaseHighlightEnrichClaim(ctx context.Context, id uuid.UUID) error
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

		// Race-proof claim (#406): a conditional state transition so two concurrent
		// enrich jobs for the SAME Highlight run Generate AT MOST once. A false-no-error
		// means a live winner holds the claim (or the row was enriched between our read
		// and here): return a retryable error so this duplicate job re-checks after
		// backoff — the next attempt sees the winner's image_key and completes, or (if
		// the winner crashed) reclaims the stale claim after enrichClaimTTL. The marker
		// lives on a column that never reaches the wire (toProtoHighlight omits it), so
		// no sentinel leaks into an RPC response.
		claimed, err := store.TryClaimHighlightEnrich(ctx, p.HighlightID, enrichClaimTTL)
		if err != nil {
			return fmt.Errorf("highlight enrich: claim highlight %s: %w", p.HighlightID, err)
		}
		if !claimed {
			return fmt.Errorf("highlight enrich: %s claimed by a concurrent worker", p.HighlightID)
		}
		// From here we OWN the claim. Release it on every exit that does NOT land an
		// image so a fast retry (or a later re-promotion) can re-claim without waiting
		// out the ttl; a release failure is non-fatal (the ttl backs it up).
		//
		// The release runs on a fresh bounded ctx (#421): an error-path exit is
		// frequently reached BECAUSE the handler ctx was cancelled (lease timeout or
		// shutdown), and a release on that dead ctx would always fail — stranding the
		// claim for the full ttl and burning every retry against it. context.WithoutCancel
		// drops the parent's cancellation AND its deadline (Go: no Done, no Deadline), so
		// we re-impose our OWN 10s timeout — otherwise this becomes the handler's only
		// unbounded DB call and a hung conn would wedge the SERIAL job-runner drain across
		// every background job kind until restart (the voice-STT-wedge class).
		release := func() {
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			if rerr := store.ReleaseHighlightEnrichClaim(rctx, p.HighlightID); rerr != nil {
				log.Error("highlight enrich: release claim", "err", rerr, "highlight", p.HighlightID)
			}
		}

		gen, model, err := factory(ctx, p.TenantID)
		if errors.Is(err, ErrImageNotConfigured) {
			release()
			log.Info("highlight enrich: image generation not configured, leaving highlight without media",
				"highlight", p.HighlightID, "tenant", p.TenantID)
			return nil
		}
		if err != nil {
			release()
			return fmt.Errorf("highlight enrich: build generator: %w", err)
		}

		res, err := gen.Generate(ctx, buildImagePrompt(h.Excerpt, h.Reason))
		if errors.Is(err, imagegen.ErrImageTooLarge) {
			// PERMANENT: an oversize image can never be stored, and a retry only
			// re-bills the same generation. Log + return nil — the Highlight stays
			// intact without media (AC), no dead-letter churn.
			release()
			log.Warn("highlight enrich: generated image exceeds blob cap, leaving highlight without media",
				"highlight", p.HighlightID)
			return nil
		}
		if err != nil {
			// Provider error: return it so the runner retries / dead-letters. The row
			// is untouched — the Highlight keeps its clip and stays imageless (AC).
			release()
			return fmt.Errorf("highlight enrich: generate image: %w", err)
		}

		// Meter the spend (ADR-0045/0046): spend.PriceOnly tees a caps-free meter
		// onto the production recorder, pricing the image's tokens AND moving the
		// glyphoxa_voice_llm_tokens series. The enrichment job is off-session and
		// never cap-gated (the recap posture). Gemini bills the image as output
		// tokens → LLMTokens.
		priced, estimatedUSD := spend.PriceOnly(rec, log)
		priced.LLMTokens(observe.ProviderGemini, model, res.PromptTokens, res.OutputTokens)
		log.Info("highlight enrich: image generated",
			"highlight", p.HighlightID,
			"model", model,
			"input_tokens", res.PromptTokens,
			"output_tokens", res.OutputTokens,
			"estimated_usd", estimatedUSD(),
		)

		key, err := blob.Key(p.TenantID, highlightOwnerKind, p.HighlightID, imageBlobName)
		if err != nil {
			release()
			return fmt.Errorf("highlight enrich: build image key: %w", err)
		}
		if err := blobs.Put(ctx, key, res.ContentType, bytes.NewReader(res.Data), int64(len(res.Data))); err != nil {
			if errors.Is(err, blob.ErrTooLarge) {
				// Same PERMANENT posture as an oversize generation: never retryable.
				release()
				log.Warn("highlight enrich: image exceeds blob cap at Put, leaving highlight without media",
					"highlight", p.HighlightID)
				return nil
			}
			release()
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
			release()
			return fmt.Errorf("highlight enrich: record image on highlight: %w", err)
		}
		return nil
	}
}

// ReconcileStore is the storage surface the boot enrichment reconciliation sweep
// needs (#406); *storage.Store satisfies it and tests fake it. HighlightsExist is
// the membership half of the orphan-image anti-join (#421): the sweep enumerates
// image blobs through the blob seam and asks the store which of their embedded
// Highlight ids still have a row — the absent ones are the orphans.
type ReconcileStore interface {
	ListPromotedHighlightsNeedingEnrichment(ctx context.Context, enrichKind string) ([]storage.HighlightEnrichTarget, error)
	HighlightsExist(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]bool, error)
}

// SweepEnrichmentReconciliation is the boot-time enrichment backstop (#406, the
// pattern-sibling of SweepMissingCandidatePurges / ADR-0043 "rows are the source
// of truth, reconcile on boot"). Before serving, it does ONE sweep with two halves:
//
//   - (a) enqueue image enrichment for every PROMOTED Highlight left imageless with
//     no live enrich job — recovering a crash between promote-commit and the enqueue
//     (AC1). It complements, not replaces, PromoteHighlight's per-promotion enqueue.
//   - (b) drop every ORPHAN image blob — an image under the Highlight image
//     owner-kind whose Highlight row is gone (a delete-vs-enrich interleaving
//     that committed the image after the delete read the row imageless), closing the
//     window DeleteHighlight's re-read only shrinks (AC3). The blobs are enumerated
//     THROUGH the blob seam (blob.Store.List, #421) — never a direct query against a
//     backend table — so the sweep survives an S3 swap; the anti-join against live
//     rows (store.HighlightsExist) happens in Go. It touches ONLY the 'highlight'
//     owner-kind + 'image' name (ADR-0048), never a clip, another owner's blob, or a
//     live enrichment's in-flight blob (the row still exists).
//
// Both halves run even if one's list fails: a store-list error is collected and
// returned so boot logs it, but the sweep never aborts (AC4) — the caller (main.go)
// treats a returned error as loud-but-non-fatal, exactly like the purge backstop. A
// per-item enqueue/delete error logs and the sweep continues (the next boot retries).
func SweepEnrichmentReconciliation(ctx context.Context, store ReconcileStore, blobs blob.Store, enqueue JobEnqueuer, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	var errs []error

	// (a) Re-enqueue enrichment for imageless promoted Highlights.
	targets, err := store.ListPromotedHighlightsNeedingEnrichment(ctx, JobKindEnrichImage)
	if err != nil {
		errs = append(errs, fmt.Errorf("list promoted highlights needing enrichment: %w", err))
	} else {
		for _, t := range targets {
			payload := enrichPayload{HighlightID: t.HighlightID, TenantID: t.TenantID}
			if err := enqueue.Enqueue(ctx, JobKindEnrichImage, payload, time.Now()); err != nil {
				log.Error("highlight enrich sweep: enqueue backstop enrichment", "err", err, "highlight", t.HighlightID)
				continue
			}
		}
		if len(targets) > 0 {
			log.Warn("scheduled backstop image enrichment for imageless promoted highlights", "count", len(targets))
		}
	}

	// (b) Sweep orphaned image blobs. Enumerate every blob through the seam, keep
	// only the Highlight image blobs (owner-kind 'highlight', name 'image'), then
	// anti-join their embedded Highlight ids against the live rows in Go: a blob
	// whose id has no row is an orphan.
	allKeys, err := blobs.List(ctx, blob.AllKeysPrefix)
	if err != nil {
		errs = append(errs, fmt.Errorf("list blob keys: %w", err))
	} else {
		var imageKeys []string
		var ids []uuid.UUID
		for _, k := range allKeys {
			parts, perr := blob.ParseKey(k)
			if perr != nil || parts.OwnerKind != highlightOwnerKind || parts.Name != imageBlobName {
				continue
			}
			imageKeys = append(imageKeys, k)
			ids = append(ids, parts.OwnerID)
		}
		present, perr := store.HighlightsExist(ctx, ids)
		if perr != nil {
			errs = append(errs, fmt.Errorf("check highlight rows for orphan images: %w", perr))
		} else {
			swept := 0
			for i, k := range imageKeys {
				if present[ids[i]] {
					continue // the row still exists: a live or in-flight enrichment, not an orphan
				}
				if err := blobs.Delete(ctx, k); err != nil {
					log.Error("highlight enrich sweep: delete orphan image", "err", err, "key", k)
					continue
				}
				swept++
			}
			if swept > 0 {
				log.Warn("swept orphaned highlight image blobs", "count", swept)
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("highlight enrich sweep: %w", errors.Join(errs...))
	}
	return nil
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
