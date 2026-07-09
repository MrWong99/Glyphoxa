//go:build integration

package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/embeddings"
)

// The ANN retrieval tests (#119, ADR-0011) hand-place chunks in a tiny cosine
// space: the query is the first basis vector e0, and each chunk's leading
// components set a known angle to it. Cosine distance (<=>) is scale-invariant,
// so the vectors need no normalization for the ordering to be predictable:
//
//	e0=[1,0]         -> distance 0     (identical direction, nearest)
//	[0.9,0.1]        -> distance ~0.006
//	[0.5,0.5]        -> distance ~0.293 (45°)
//	e1=[0,1]         -> distance 1.0   (orthogonal, farthest)
//
// All other 766 dimensions are zero, so only the leading components matter.

// vec768 builds an embeddings.Dim-long vector from its leading components,
// zero-padded — the exact shape SetChunkEmbedding's ::vector(768) cast requires.
func vec768(lead ...float32) []float32 {
	v := make([]float32, embeddings.Dim)
	copy(v, lead)
	return v
}

// seedSearchChunk inserts one chunk (real write path) and, when vec is non-nil,
// embeds it via SetChunkEmbedding. A nil vec leaves the embedding NULL — the
// backlog state retrieval must skip.
func seedSearchChunk(t *testing.T, st *storage.Store, campaignID, vsID uuid.UUID, agents []uuid.UUID, vec []float32) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id, err := st.InsertTranscriptChunk(ctx, storage.TranscriptChunk{
		CampaignID:           campaignID,
		VoiceSessionID:       vsID,
		Content:              "chunk " + id36(agents),
		ParticipatedAgentIDs: agents,
		StartedAt:            time.Date(2026, 7, 5, 18, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("InsertTranscriptChunk: %v", err)
	}
	if vec != nil {
		if err := st.SetChunkEmbedding(ctx, id, vec, "test-model"); err != nil {
			t.Fatalf("SetChunkEmbedding: %v", err)
		}
	}
	return id
}

// id36 renders the agent set into the content so seeded rows are distinguishable
// in failure output; it carries no assertion weight.
func id36(agents []uuid.UUID) string {
	s := ""
	for _, a := range agents {
		s += a.String()[:8] + " "
	}
	return s
}

func matchIDs(matches []storage.ChunkMatch) []uuid.UUID {
	out := make([]uuid.UUID, len(matches))
	for i, m := range matches {
		out[i] = m.Chunk.ID
	}
	return out
}

func idSet(ids []uuid.UUID) map[uuid.UUID]bool {
	m := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}

func sameOrder(got, want []uuid.UUID) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestSearchChunksByCampaign_OrdersByCosineAndCapsAtK is AC1 + AC5(cap): seeded
// chunks with known embeddings come back ordered by cosine distance to the query
// (nearest first, distance monotone nondecreasing), and k caps the result at the
// k nearest.
func TestSearchChunksByCampaign_OrdersByCosineAndCapsAtK(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	vs, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("CreateVoiceSession: %v", err)
	}

	query := vec768(1, 0)
	// Small vectors at known angles PLUS one large-magnitude near-parallel vector:
	// `big` = [90,9] shares a near-zero angle with the query (cosine ranks it 2nd,
	// ~0.005) but its ~90 magnitude makes it the FARTHEST by L2 (~89.5). So the
	// exact cosine order below differs from the L2 order — it fails if <=> (cosine)
	// is ever swapped for <-> (L2), a mutant that every small-norm vector alone
	// (similar magnitudes → L2 order == cosine order) could not catch.
	nearest := seedSearchChunk(t, st, campaignID, vs.ID, nil, vec768(1, 0))  // cos 0
	big := seedSearchChunk(t, st, campaignID, vs.ID, nil, vec768(90, 9))     // cos ~0.005, L2 ~89.5
	near := seedSearchChunk(t, st, campaignID, vs.ID, nil, vec768(0.9, 0.1)) // cos ~0.006
	mid := seedSearchChunk(t, st, campaignID, vs.ID, nil, vec768(0.5, 0.5))  // cos ~0.293
	far := seedSearchChunk(t, st, campaignID, vs.ID, nil, vec768(0, 1))      // cos 1.0

	got, err := st.SearchChunksByCampaign(ctx, campaignID, query, 5)
	if err != nil {
		t.Fatalf("SearchChunksByCampaign: %v", err)
	}
	wantOrder := []uuid.UUID{nearest, big, near, mid, far}
	if !sameOrder(matchIDs(got), wantOrder) {
		t.Fatalf("order = %v, want cosine nearest-first %v (L2 would rank the big vector last)", matchIDs(got), wantOrder)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Distance < got[i-1].Distance {
			t.Errorf("distance not monotone: got[%d]=%g < got[%d]=%g", i, got[i].Distance, i-1, got[i-1].Distance)
		}
	}
	if got[0].Distance > 1e-4 {
		t.Errorf("nearest distance = %g, want ~0 (identical direction)", got[0].Distance)
	}
	if last := got[len(got)-1]; last.Distance < 0.99 {
		t.Errorf("farthest distance = %g, want ~1 (orthogonal)", last.Distance)
	}
	// The returned chunk carries its scanned fields, not a zero value.
	if got[0].Chunk.CampaignID != campaignID || got[0].Chunk.EmbeddingModel != "test-model" {
		t.Errorf("nearest chunk fields not populated: %+v", got[0].Chunk)
	}

	// k caps at the k nearest by cosine: k=2 -> {nearest, big} (big by angle, not L2).
	capped, err := st.SearchChunksByCampaign(ctx, campaignID, query, 2)
	if err != nil {
		t.Fatalf("SearchChunksByCampaign (k=2): %v", err)
	}
	if ids := matchIDs(capped); len(ids) != 2 || ids[0] != nearest || ids[1] != big {
		t.Fatalf("k=2 result = %v, want [%s %s] (the 2 nearest by cosine)", ids, nearest, big)
	}
}

// TestSearchChunksByAgent_ParticipationContainment is AC2: NPC-knowledge mode
// returns only chunks whose participated set CONTAINS the given agent, and a
// multi-agent chunk is returned for every one of its participants (containment,
// not equality). World-context mode ignores participants entirely.
func TestSearchChunksByAgent_ParticipationContainment(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	vs, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("CreateVoiceSession: %v", err)
	}

	agentA, agentB := uuid.New(), uuid.New()
	query := vec768(1, 0)
	c1 := seedSearchChunk(t, st, campaignID, vs.ID, []uuid.UUID{agentA}, vec768(1, 0))
	c2 := seedSearchChunk(t, st, campaignID, vs.ID, []uuid.UUID{agentA}, vec768(0.9, 0.1))
	c3 := seedSearchChunk(t, st, campaignID, vs.ID, []uuid.UUID{agentB}, vec768(0.5, 0.5))
	cAB := seedSearchChunk(t, st, campaignID, vs.ID, []uuid.UUID{agentA, agentB}, vec768(0, 1))

	byA, err := st.SearchChunksByAgent(ctx, campaignID, agentA, query, 10)
	if err != nil {
		t.Fatalf("SearchChunksByAgent(A): %v", err)
	}
	if set := idSet(matchIDs(byA)); len(set) != 3 || !set[c1] || !set[c2] || !set[cAB] {
		t.Errorf("ByAgent(A) = %v, want {c1,c2,cAB} (containment)", matchIDs(byA))
	}

	byB, err := st.SearchChunksByAgent(ctx, campaignID, agentB, query, 10)
	if err != nil {
		t.Fatalf("SearchChunksByAgent(B): %v", err)
	}
	if set := idSet(matchIDs(byB)); len(set) != 2 || !set[c3] || !set[cAB] {
		t.Errorf("ByAgent(B) = %v, want {c3,cAB} (containment)", matchIDs(byB))
	}

	// World-context mode ignores participants: all four chunks come back.
	world, err := st.SearchChunksByCampaign(ctx, campaignID, query, 10)
	if err != nil {
		t.Fatalf("SearchChunksByCampaign: %v", err)
	}
	if len(world) != 4 {
		t.Errorf("world-context returned %d chunks, want all 4 regardless of participant", len(world))
	}
}

// TestSearchChunks_ExcludesNullEmbeddingAndOtherCampaigns is AC3 + AC4 + the
// cross-campaign leak guard: a NULL-embedding chunk that WOULD be a perfect
// match is never returned by either mode, and a second campaign's identical
// vector never leaks into a query scoped to the first campaign.
func TestSearchChunks_ExcludesNullEmbeddingAndOtherCampaigns(t *testing.T) {
	dsn := startPostgres(t)
	pool, tenantID, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	vs, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("CreateVoiceSession: %v", err)
	}
	agent := uuid.New()
	query := vec768(1, 0)

	// A real, embedded match in campaign 1.
	real := seedSearchChunk(t, st, campaignID, vs.ID, []uuid.UUID{agent}, vec768(0.9, 0.1))
	// A chunk whose vector WOULD be the perfect match, but embedding is left NULL.
	nullMatch := seedSearchChunk(t, st, campaignID, vs.ID, []uuid.UUID{agent}, nil)

	// A second campaign with an identical-to-query embedded chunk.
	var campaign2 uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaign (tenant_id, name, system, language)
		 VALUES ($1, 'Other', 'dnd5e', 'en') RETURNING id`, tenantID).Scan(&campaign2); err != nil {
		t.Fatalf("insert campaign2: %v", err)
	}
	vs2, err := st.CreateVoiceSession(ctx, campaign2)
	if err != nil {
		t.Fatalf("CreateVoiceSession(campaign2): %v", err)
	}
	other := seedSearchChunk(t, st, campaign2, vs2.ID, []uuid.UUID{agent}, vec768(1, 0))

	assertAbsent := func(name string, matches []storage.ChunkMatch) {
		set := idSet(matchIDs(matches))
		if set[nullMatch] {
			t.Errorf("%s returned the NULL-embedding chunk %s", name, nullMatch)
		}
		if set[other] {
			t.Errorf("%s leaked cross-campaign chunk %s", name, other)
		}
		if !set[real] {
			t.Errorf("%s missing the real match %s", name, real)
		}
	}

	byCampaign, err := st.SearchChunksByCampaign(ctx, campaignID, query, 10)
	if err != nil {
		t.Fatalf("SearchChunksByCampaign: %v", err)
	}
	assertAbsent("world-context", byCampaign)

	byAgent, err := st.SearchChunksByAgent(ctx, campaignID, agent, query, 10)
	if err != nil {
		t.Fatalf("SearchChunksByAgent: %v", err)
	}
	assertAbsent("npc-knowledge", byAgent)
}

// TestSearchChunks_KMustBePositive is AC "capped to k": k<=0 is a caller bug and
// errors rather than silently defaulting — also covered Docker-free in the unit
// test, asserted here against a live Store for completeness.
func TestSearchChunks_KMustBePositive(t *testing.T) {
	dsn := startPostgres(t)
	pool, _, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	for _, k := range []int{0, -1} {
		if _, err := st.SearchChunksByCampaign(ctx, campaignID, vec768(1), k); err == nil {
			t.Errorf("SearchChunksByCampaign k=%d returned nil error", k)
		}
		if _, err := st.SearchChunksByAgent(ctx, campaignID, uuid.New(), vec768(1), k); err == nil {
			t.Errorf("SearchChunksByAgent k=%d returned nil error", k)
		}
	}
}
