//go:build integration

package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/highlight"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestHighlight_SetSoundKind pins the Add-sound choice write (#312): promoted
// rows only, requested_at stamped for a kind and cleared for "", and the
// landed triad cleared on every re-run.
func TestHighlight_SetSoundKind(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	vs, _ := st.CreateVoiceSession(ctx, campaignID)

	// A candidate refuses the choice (sound is opt-in AFTER promotion).
	candID, _ := seedHighlight(t, st, tenantID, vs.ID, campaignID, storage.HighlightCandidate)
	if _, err := st.SetHighlightSoundKind(ctx, tenantID, candID, storage.SoundKindSting); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("set kind on candidate: want ErrNotFound, got %v", err)
	}

	id, _ := seedHighlight(t, st, tenantID, vs.ID, campaignID, storage.HighlightPromoted)
	h, err := st.SetHighlightSoundKind(ctx, tenantID, id, storage.SoundKindSting)
	if err != nil {
		t.Fatalf("set kind: %v", err)
	}
	if h.SoundKind != storage.SoundKindSting || h.SoundRequestedAt == nil {
		t.Fatalf("choice not recorded: kind=%q requestedAt=%v", h.SoundKind, h.SoundRequestedAt)
	}

	// A landed sound is cleared by a re-run (regeneration) …
	sndKey := "t/" + tenantID.String() + "/highlight/" + id.String() + "/sound"
	if err := st.SetHighlightSound(ctx, id, storage.SoundKindSting, sndKey, "audio/mpeg", 77); err != nil {
		t.Fatalf("land sound: %v", err)
	}
	h, err = st.SetHighlightSoundKind(ctx, tenantID, id, storage.SoundKindMusic)
	if err != nil {
		t.Fatalf("re-run set kind: %v", err)
	}
	if h.SoundKey != "" || h.SoundContentType != "" || h.SoundSizeBytes != 0 {
		t.Fatalf("re-run left the landed triad: %q/%q/%d", h.SoundKey, h.SoundContentType, h.SoundSizeBytes)
	}
	// … and "" clears the choice entirely.
	h, err = st.SetHighlightSoundKind(ctx, tenantID, id, "")
	if err != nil {
		t.Fatalf("clear kind: %v", err)
	}
	if h.SoundKind != "" || h.SoundRequestedAt != nil {
		t.Fatalf("clear left kind=%q requestedAt=%v", h.SoundKind, h.SoundRequestedAt)
	}
}

// TestHighlight_SetSound_ConditionalOnKind pins the job's conditional land: it
// writes only while the row's sound_kind still matches, so a stale job for a
// superseded choice misses (ErrNotFound) instead of landing the wrong asset.
func TestHighlight_SetSound_ConditionalOnKind(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	vs, _ := st.CreateVoiceSession(ctx, campaignID)
	id, _ := seedHighlight(t, st, tenantID, vs.ID, campaignID, storage.HighlightPromoted)
	if _, err := st.SetHighlightSoundKind(ctx, tenantID, id, storage.SoundKindMusic); err != nil {
		t.Fatalf("set kind: %v", err)
	}

	key := "t/" + tenantID.String() + "/highlight/" + id.String() + "/sound"
	// A stale sting job must miss the music request …
	if err := st.SetHighlightSound(ctx, id, storage.SoundKindSting, key, "audio/mpeg", 5); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("stale-kind land: want ErrNotFound, got %v", err)
	}
	// … the matching kind lands …
	if err := st.SetHighlightSound(ctx, id, storage.SoundKindMusic, key, "audio/mpeg", 5); err != nil {
		t.Fatalf("matching land: %v", err)
	}
	h, err := st.GetHighlight(ctx, tenantID, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if h.SoundKey != key || h.SoundContentType != "audio/mpeg" || h.SoundSizeBytes != 5 {
		t.Fatalf("sound not persisted: %q/%q/%d", h.SoundKey, h.SoundContentType, h.SoundSizeBytes)
	}
	// … and a missing row misses too.
	if err := st.SetHighlightSound(ctx, uuid.New(), storage.SoundKindMusic, key, "audio/mpeg", 5); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing-row land: want ErrNotFound, got %v", err)
	}
}

// TestTryClaimHighlightSoundEnrich_ConditionalTransition pins the sound claim
// (#312, the #406 pattern): claimable only while a sound is requested and
// unlanded; the first claim wins, a fresh second loses, release re-opens, a
// stale claim is reclaimable, and a landed row is unclaimable.
func TestTryClaimHighlightSoundEnrich_ConditionalTransition(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	vs, _ := st.CreateVoiceSession(ctx, campaignID)
	id, _ := seedHighlight(t, st, tenantID, vs.ID, campaignID, storage.HighlightPromoted)

	// No sound requested yet: unclaimable.
	won, err := st.TryClaimHighlightSoundEnrich(ctx, id, time.Hour)
	if err != nil || won {
		t.Fatalf("claim with no request: won=%v err=%v; want lost", won, err)
	}
	if _, err := st.SetHighlightSoundKind(ctx, tenantID, id, storage.SoundKindSting); err != nil {
		t.Fatalf("set kind: %v", err)
	}

	won, err = st.TryClaimHighlightSoundEnrich(ctx, id, time.Hour)
	if err != nil || !won {
		t.Fatalf("first claim: won=%v err=%v; want won", won, err)
	}
	won, err = st.TryClaimHighlightSoundEnrich(ctx, id, time.Hour)
	if err != nil || won {
		t.Fatalf("second claim: won=%v err=%v; want lost", won, err)
	}
	if err := st.ReleaseHighlightSoundEnrichClaim(ctx, id); err != nil {
		t.Fatalf("release: %v", err)
	}
	won, err = st.TryClaimHighlightSoundEnrich(ctx, id, time.Hour)
	if err != nil || !won {
		t.Fatalf("post-release claim: won=%v err=%v; want won", won, err)
	}
	won, err = st.TryClaimHighlightSoundEnrich(ctx, id, time.Nanosecond)
	if err != nil || !won {
		t.Fatalf("stale-claim reclaim: won=%v err=%v; want won", won, err)
	}

	key := "t/" + tenantID.String() + "/highlight/" + id.String() + "/sound"
	if err := st.SetHighlightSound(ctx, id, storage.SoundKindSting, key, "audio/mpeg", 9); err != nil {
		t.Fatalf("land sound: %v", err)
	}
	won, err = st.TryClaimHighlightSoundEnrich(ctx, id, time.Hour)
	if err != nil || won {
		t.Fatalf("claim on landed row: won=%v err=%v; want lost", won, err)
	}
}

// soundJobPayload marshals the sound job payload the reconciliation query
// matches on (highlight_id AND kind).
func soundJobPayload(t *testing.T, highlightID, tenantID uuid.UUID, kind string) []byte {
	t.Helper()
	b, err := highlight.MarshalEnrichSound(highlightID, tenantID, kind)
	if err != nil {
		t.Fatalf("marshal sound payload: %v", err)
	}
	return b
}

// TestListPromotedHighlightsNeedingSoundEnrichment pins the sound half of the
// boot sweep query (#312): only promoted, requested-but-unlanded rows with no
// live job for the SAME requested kind — a job for a superseded kind does not
// satisfy the current request.
func TestListPromotedHighlightsNeedingSoundEnrichment(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	vs, _ := st.CreateVoiceSession(ctx, campaignID)
	promote := func() uuid.UUID {
		id, _ := seedHighlight(t, st, tenantID, vs.ID, campaignID, storage.HighlightCandidate)
		if _, err := st.PromoteHighlight(ctx, tenantID, id); err != nil {
			t.Fatalf("promote: %v", err)
		}
		return id
	}
	request := func(id uuid.UUID, kind string) {
		if _, err := st.SetHighlightSoundKind(ctx, tenantID, id, kind); err != nil {
			t.Fatalf("set kind: %v", err)
		}
	}

	// (want) requested, unlanded, no job → a target carrying its kind.
	wantID := promote()
	request(wantID, storage.SoundKindMusic)

	// (want) requested music, but the only job is for the SUPERSEDED sting →
	// still a target: a kind-mismatched job never satisfies the request.
	kindChangedID := promote()
	request(kindChangedID, storage.SoundKindMusic)
	if _, err := st.EnqueueJob(ctx, highlight.JobKindEnrichSound, soundJobPayload(t, kindChangedID, tenantID, storage.SoundKindSting), 0); err != nil {
		t.Fatalf("enqueue stale-kind job: %v", err)
	}

	// requested with a live job of the SAME kind → excluded.
	hasJobID := promote()
	request(hasJobID, storage.SoundKindSting)
	if _, err := st.EnqueueJob(ctx, highlight.JobKindEnrichSound, soundJobPayload(t, hasJobID, tenantID, storage.SoundKindSting), 0); err != nil {
		t.Fatalf("enqueue matching job: %v", err)
	}

	// requested and already landed → excluded.
	landedID := promote()
	request(landedID, storage.SoundKindSting)
	key := "t/" + tenantID.String() + "/highlight/" + landedID.String() + "/sound"
	if err := st.SetHighlightSound(ctx, landedID, storage.SoundKindSting, key, "audio/mpeg", 3); err != nil {
		t.Fatalf("land sound: %v", err)
	}

	// promoted with no request → excluded.
	promote()

	// (want) re-requested the SAME kind after a job of that kind completed → a
	// target again: a 'done' job only satisfies the request it was enqueued for
	// (created_at >= sound_requested_at), so a regenerate whose enqueue was lost
	// is re-driven instead of being blocked by the earlier completion forever.
	reRequestedID := promote()
	request(reRequestedID, storage.SoundKindSting)
	doneJob, err := st.EnqueueJob(ctx, highlight.JobKindEnrichSound, soundJobPayload(t, reRequestedID, tenantID, storage.SoundKindSting), 0)
	if err != nil {
		t.Fatalf("enqueue done job: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE job SET status = 'done' WHERE id = $1`, doneJob); err != nil {
		t.Fatalf("mark job done: %v", err)
	}
	request(reRequestedID, storage.SoundKindSting) // regenerate: re-stamps sound_requested_at

	got, err := st.ListPromotedHighlightsNeedingSoundEnrichment(ctx, highlight.JobKindEnrichSound)
	if err != nil {
		t.Fatalf("list needing sound enrichment: %v", err)
	}
	byID := map[uuid.UUID]storage.HighlightSoundEnrichTarget{}
	for _, tgt := range got {
		byID[tgt.HighlightID] = tgt
	}
	if len(got) != 3 {
		t.Fatalf("want the 3 unsatisfied requests, got %+v", got)
	}
	if tgt, ok := byID[reRequestedID]; !ok || tgt.Kind != storage.SoundKindSting {
		t.Fatalf("re-requested target wrong (a stale 'done' job must not satisfy a newer request): %+v", byID[reRequestedID])
	}
	if tgt, ok := byID[wantID]; !ok || tgt.Kind != storage.SoundKindMusic || tgt.TenantID != tenantID {
		t.Fatalf("no-job target wrong: %+v", byID[wantID])
	}
	if tgt, ok := byID[kindChangedID]; !ok || tgt.Kind != storage.SoundKindMusic {
		t.Fatalf("kind-changed target wrong: %+v", byID[kindChangedID])
	}
}

// TestHighlight_CampaignClipKeySweep_IncludesSounds pins the third UNION arm:
// a landed sound key joins the campaign hard-delete blob sweep.
func TestHighlight_CampaignClipKeySweep_IncludesSounds(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	vs, _ := st.CreateVoiceSession(ctx, campaignID)
	id, clip := seedHighlight(t, st, tenantID, vs.ID, campaignID, storage.HighlightCandidate)
	if _, err := st.PromoteHighlight(ctx, tenantID, id); err != nil {
		t.Fatalf("promote: %v", err)
	}
	if _, err := st.SetHighlightSoundKind(ctx, tenantID, id, storage.SoundKindSting); err != nil {
		t.Fatalf("set kind: %v", err)
	}
	sndKey := "t/" + tenantID.String() + "/highlight/" + id.String() + "/sound"
	if err := st.SetHighlightSound(ctx, id, storage.SoundKindSting, sndKey, "audio/mpeg", 8); err != nil {
		t.Fatalf("land sound: %v", err)
	}

	keys, err := st.ListCampaignHighlightClipKeys(ctx, campaignID)
	if err != nil {
		t.Fatalf("list campaign keys: %v", err)
	}
	set := map[string]bool{}
	for _, k := range keys {
		set[k] = true
	}
	if !set[clip] || !set[sndKey] || len(keys) != 2 {
		t.Fatalf("campaign sweep keys mismatch: %v", keys)
	}
}
