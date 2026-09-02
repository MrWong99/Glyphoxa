package highlight

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/billing"
	"github.com/MrWong99/Glyphoxa/internal/blob"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/spend"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/providererr"
	"github.com/MrWong99/Glyphoxa/pkg/voice/soundgen"
)

// JobKindEnrichSound is the background-job kind that generates the GM-chosen
// sound asset — a Sting or a Music track — for a promoted Highlight and lands
// it on the row through the blob seam (#312, Epic 8, ADR-0004 amendment /
// ADR-0049). Unlike image enrichment it is OPT-IN: SetHighlightSound (the RPC)
// enqueues it on the GM's explicit "Add sound" action, never PromoteHighlight.
// The handler is idempotent + at-least-once.
const JobKindEnrichSound = "highlight.enrich_sound"

// soundBlobName is the blob.Key name segment for a Highlight's generated sound
// (beside "clip.wav" and "image"): t/<tenant>/highlight/<id>/sound — the #312
// decision's attach-as-separate-blob path. One name for both kinds: a kind
// change overwrites in place, so a Highlight never owns two sound blobs.
const soundBlobName = "sound"

// ErrSoundNotConfigured is the sentinel a [SoundGeneratorFactory] returns when
// the tenant has no usable sound key: no `tts` Provider Config, a
// non-ElevenLabs tts provider, or no resolvable key (ADR-0004 amendment). The
// handler treats it as a clean no-op — the Highlight stays intact without
// media, no retry, no spend. An ALIAS for soundgen.ErrNotConfigured, not a
// second value (the imagegen #541 lesson).
var ErrSoundNotConfigured = soundgen.ErrNotConfigured

// SoundGeneratorFactory builds the tenant's [soundgen.Generator]. It resolves
// the tenant's `tts` Provider Config key iff the provider is ElevenLabs
// (ADR-0004 amendment) and returns [ErrSoundNotConfigured] otherwise. main.go
// wires it from the store + cipher; tests fake it. No model id accompanies it
// (unlike the image factory): the SFX and Music model ids are per-call facts
// the adapter reports on each [soundgen.Result].
type SoundGeneratorFactory func(ctx context.Context, tenantID uuid.UUID) (soundgen.Generator, error)

// soundEnrichPayload is the sound job's payload: which Highlight, its tenant,
// and WHICH kind the GM asked for at enqueue time. Kind rides in the payload
// (unlike the image job) so a stale job for a superseded choice self-detects:
// the handler compares it against the row's current sound_kind.
type soundEnrichPayload struct {
	HighlightID uuid.UUID `json:"highlight_id"`
	TenantID    uuid.UUID `json:"tenant_id"`
	Kind        string    `json:"kind"`
}

// MarshalEnrichSound builds the JobKindEnrichSound payload the
// SetHighlightSound RPC enqueues.
func MarshalEnrichSound(highlightID, tenantID uuid.UUID, kind string) ([]byte, error) {
	return json.Marshal(soundEnrichPayload{HighlightID: highlightID, TenantID: tenantID, Kind: kind})
}

// Sting/Music duration bounds (#312). The sting matches the clip so the web UI
// can layer it client-side (attach-decision: zero DSP), capped at the
// endpoint's 22s sting contract; the Music track is a bounded "full track" —
// long enough to be one (30s floor), capped for spend predictability.
const (
	stingMinDuration = 2 * time.Second
	stingMaxDuration = 22 * time.Second
	musicMinDuration = 30 * time.Second
	musicMaxDuration = 60 * time.Second
)

// soundRequestFor derives the generation request from the Highlight: the
// prompt from its caption material (speaker IDs deliberately excluded, the
// image posture) and the duration from its clip range, clamped per kind.
func soundRequestFor(kind string, h storage.Highlight) soundgen.Request {
	clip := h.EndsAt.Sub(h.StartsAt)
	switch kind {
	case storage.SoundKindMusic:
		return soundgen.Request{
			Prompt:   buildMusicPrompt(h.Excerpt, h.Reason),
			Duration: clampDuration(clip, musicMinDuration, musicMaxDuration),
		}
	default: // storage.SoundKindSting
		return soundgen.Request{
			Prompt:   buildStingPrompt(h.Excerpt, h.Reason),
			Duration: clampDuration(clip, stingMinDuration, stingMaxDuration),
		}
	}
}

// buildStingPrompt renders the sound-effects prompt from a Highlight's caption
// material (#312). The excerpt is truncated to excerptPromptLimit runes, like
// the image prompt.
func buildStingPrompt(excerpt, reason string) string {
	return fmt.Sprintf(
		"A cinematic sound-effect sting for a tabletop RPG highlight reel, matching this moment: %s. Why it is memorable: %s.",
		truncateRunes(excerpt, excerptPromptLimit), reason)
}

// buildMusicPrompt renders the Music composition prompt from a Highlight's
// caption material (#312). Instrumental is enforced adapter-side; the excerpt
// steers mood, not lyrics.
func buildMusicPrompt(excerpt, reason string) string {
	return fmt.Sprintf(
		"An instrumental cinematic theme for a tabletop RPG highlight, capturing the mood of this moment: %s. Why it is memorable: %s.",
		truncateRunes(excerpt, excerptPromptLimit), reason)
}

// clampDuration bounds d to [min, max].
func clampDuration(d, min, max time.Duration) time.Duration {
	if d < min {
		return min
	}
	if d > max {
		return max
	}
	return d
}

// SoundEnrichStore is the storage surface the sound-generation handler needs;
// *storage.Store satisfies it and tests fake it. The claim pair mirrors the
// image enrichment's (#406); SetHighlightSound is the CONDITIONAL land (misses
// when the row is gone OR the GM changed the choice mid-generation); AddUsage
// is the per-generation Usage Ledger flush (ADR-0004 amendment: attribution
// under the SFX/Music model id).
type SoundEnrichStore interface {
	GetHighlight(ctx context.Context, tenantID, id uuid.UUID) (storage.Highlight, error)
	SetHighlightSound(ctx context.Context, id uuid.UUID, kind, soundKey, contentType string, sizeBytes int64) error
	TryClaimHighlightSoundEnrich(ctx context.Context, id uuid.UUID, ttl time.Duration) (bool, error)
	ReleaseHighlightSoundEnrichClaim(ctx context.Context, id uuid.UUID) error
	AddUsage(ctx context.Context, rows []storage.UsageRow) error
}

// EnrichSoundHandler builds the JobKindEnrichSound handler (ADR-0049). It is
// idempotent + at-least-once, the EnrichImageHandler decision ladder adapted
// for an opt-in, re-runnable action:
//
//   - the Highlight is gone → nil (done).
//   - the row's sound_kind no longer matches the payload's (the GM changed or
//     cleared the choice; the newer RPC enqueued its own job) → nil.
//   - the requested sound already landed → nil (no double spend on a re-run).
//   - sound generation is not configured for the tenant → log + nil (the
//     Highlight stays intact without media — never a retry loop on a missing
//     key; ADR-0004 amendment's clean no-op).
//   - a provider failure → the error is returned (retry / dead-letter)
//     whatever its status — dead-lettering is what lets the boot sweep re-run
//     the generation once a bad key is fixed (the EnrichImageHandler
//     rationale) — but a rejected key and an exhausted quota are LOGGED apart.
//   - otherwise generate → flush a per-generation Usage Ledger row under the
//     result's model id (SFX and Music separately, ADR-0004 amendment) →
//     store the audio behind the blob seam at the deterministic key →
//     conditionally record it on the row. A conditional-land miss re-reads:
//     a GONE row compensates the orphaned blob; a superseded kind leaves the
//     blob for the newer job to overwrite (deleting it could destroy that
//     job's already-landed asset). Errors never mutate the row — the
//     Highlight keeps its clip and stays soundless (AC).
func EnrichSoundHandler(store SoundEnrichStore, blobs blob.Store, factory SoundGeneratorFactory, log *slog.Logger) func(context.Context, json.RawMessage) error {
	if log == nil {
		log = slog.Default()
	}
	return func(ctx context.Context, payload json.RawMessage) error {
		var p soundEnrichPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("highlight sound enrich: decode payload: %w", err)
		}

		h, err := store.GetHighlight(ctx, p.TenantID, p.HighlightID)
		if errors.Is(err, storage.ErrNotFound) {
			return nil // deleted before the generation ran: nothing to do
		}
		if err != nil {
			return fmt.Errorf("highlight sound enrich: load highlight %s: %w", p.HighlightID, err)
		}
		if h.SoundKind != p.Kind {
			// The GM changed (or cleared) the choice after this job was enqueued;
			// the newer SetHighlightSound call enqueued its own job. Done.
			return nil
		}
		if h.SoundKey != "" {
			// Already landed (a re-run of an at-least-once job): stop before any
			// spend so the same request is never billed twice.
			return nil
		}

		// Race-proof claim (the #406 pattern): two concurrent jobs for the SAME
		// Highlight run Generate AT MOST once, and — because the blob name is
		// shared between kinds — the claim also serializes blob writes across a
		// choice change. A false-no-error returns a retryable error so this
		// duplicate re-checks after backoff.
		claimed, err := store.TryClaimHighlightSoundEnrich(ctx, p.HighlightID, enrichClaimTTL)
		if err != nil {
			return fmt.Errorf("highlight sound enrich: claim highlight %s: %w", p.HighlightID, err)
		}
		if !claimed {
			return fmt.Errorf("highlight sound enrich: %s claimed by a concurrent worker", p.HighlightID)
		}
		// From here we OWN the claim; release it on every exit that does not land
		// a sound. Fresh bounded ctx (#421): an error exit is often reached
		// BECAUSE the handler ctx died, and a release on that dead ctx would
		// strand the claim for the full ttl.
		release := func() {
			rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			if rerr := store.ReleaseHighlightSoundEnrichClaim(rctx, p.HighlightID); rerr != nil {
				log.Error("highlight sound enrich: release claim", "err", rerr, "highlight", p.HighlightID)
			}
		}

		gen, err := factory(ctx, p.TenantID)
		if errors.Is(err, ErrSoundNotConfigured) {
			release()
			log.Info("highlight sound enrich: sound generation not configured, leaving highlight without sound",
				"highlight", p.HighlightID, "tenant", p.TenantID)
			return nil
		}
		if err != nil {
			release()
			return fmt.Errorf("highlight sound enrich: build generator: %w", err)
		}

		req := soundRequestFor(p.Kind, h)
		var res soundgen.Result
		if p.Kind == storage.SoundKindMusic {
			res, err = gen.ComposeMusic(ctx, req)
		} else {
			res, err = gen.GenerateSting(ctx, req)
		}
		if err != nil {
			// Provider error: return it so the runner retries / dead-letters — the
			// row is untouched, the Highlight keeps its clip and stays soundless
			// (AC). Every failure takes this exit, including a rejected key: the
			// boot sweep treats 'dead' as absent and 'done' as satisfied, so
			// dead-lettering is precisely what re-drives these once the key is
			// fixed (the EnrichImageHandler rationale in full). Only the DIAGNOSIS
			// varies by status.
			release()
			var httpErr *providererr.HTTPError
			if errors.As(err, &httpErr) {
				switch httpErr.StatusCode {
				case http.StatusUnauthorized, http.StatusForbidden:
					log.Error("highlight sound enrich: provider rejected the configured key",
						"err", err, "highlight", p.HighlightID, "tenant", p.TenantID)
				case http.StatusTooManyRequests:
					log.Warn("highlight sound enrich: provider out of quota or rate-limited",
						"err", err, "highlight", p.HighlightID, "tenant", p.TenantID)
				}
			}
			return fmt.Errorf("highlight sound enrich: generate %s: %w", p.Kind, err)
		}

		// Meter the spend (ADR-0004/0046 amendments): a per-generation Usage
		// Ledger flush attributes the estimate to the tenant under the SFX/Music
		// model id — off-session, never cap-gated (the recap posture). A flush
		// failure logs and continues: attribution only, never a gate, and failing
		// the job here would re-bill the generation on retry.
		estimated := spend.EstimateSoundUSD(observe.ProviderElevenLabs, res.Model, req.Duration)
		ledger := billing.NewLedger(p.TenantID, nil)
		ledger.SoundGeneration(observe.ProviderElevenLabs, res.Model, req.Duration, res.CharacterCost)
		if ferr := ledger.Flush(ctx, store.AddUsage); ferr != nil {
			log.Error("highlight sound enrich: flush usage ledger", "err", ferr, "highlight", p.HighlightID)
		}
		log.Info("highlight sound enrich: sound generated",
			"highlight", p.HighlightID,
			"kind", p.Kind,
			"model", res.Model,
			"requested_duration", req.Duration,
			"character_cost", res.CharacterCost,
			"estimated_usd", estimated,
		)

		key, err := blob.Key(p.TenantID, highlightOwnerKind, p.HighlightID, soundBlobName)
		if err != nil {
			release()
			return fmt.Errorf("highlight sound enrich: build sound key: %w", err)
		}
		if err := blobs.Put(ctx, key, res.ContentType, bytes.NewReader(res.Data), int64(len(res.Data))); err != nil {
			if errors.Is(err, blob.ErrTooLarge) {
				// PERMANENT: an oversize asset can never be stored, and a retry only
				// re-bills the same generation. The Highlight stays intact.
				release()
				log.Warn("highlight sound enrich: generated sound exceeds blob cap, leaving highlight without sound",
					"highlight", p.HighlightID)
				return nil
			}
			release()
			return fmt.Errorf("highlight sound enrich: store sound: %w", err)
		}
		if err := store.SetHighlightSound(ctx, p.HighlightID, p.Kind, key, res.ContentType, int64(len(res.Data))); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				// The conditional land missed: the row is gone OR the GM changed the
				// choice mid-generation. Disambiguate — ONLY a gone row compensates
				// the blob; on a superseded kind the newer job (which cannot claim
				// until we release) will overwrite the shared blob name itself, and
				// deleting here could destroy an asset it already landed in the
				// claim-expired double-generate window.
				if _, gerr := store.GetHighlight(ctx, p.TenantID, p.HighlightID); errors.Is(gerr, storage.ErrNotFound) {
					if derr := blobs.Delete(ctx, key); derr != nil {
						log.Error("highlight sound enrich: compensate orphan sound", "err", derr, "key", key)
					}
					return nil
				}
				release()
				log.Info("highlight sound enrich: choice changed mid-generation, leaving land to the newer job",
					"highlight", p.HighlightID, "kind", p.Kind)
				return nil
			}
			release()
			return fmt.Errorf("highlight sound enrich: record sound on highlight: %w", err)
		}
		// Release the claim on SUCCESS too — a deliberate departure from the
		// image handler, which never re-runs (ImageKey blocks forever). A sound
		// is re-runnable (regeneration / choice change), and a claim left
		// stamped by a success would block the NEXT request's job for the full
		// ttl — long enough for its retries to dead-letter. Safe to clear: the
		// claim conditional refuses while sound_key is set, so releasing here
		// reopens nothing until a new request clears the key.
		release()
		return nil
	}
}

// Compile-time pins: *storage.Store satisfies every narrow store seam this
// package declares, so a drift in a store method fails THIS package's build
// instead of surfacing only at the composition root (CONTRIBUTING: interface
// assertions).
var (
	_ SoundEnrichStore = (*storage.Store)(nil)
)
