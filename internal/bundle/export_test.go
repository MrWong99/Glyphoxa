//go:build integration

package bundle_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/bundle"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/wirenpc"
)

// seededCampaign runs the demo seed (Butler + Bart + dice grants + provider
// configs) then adds KG content (a location node, an NPC node linked to Bart, an
// edge between them) and one PC Character. It returns the store, the campaign id,
// and Bart's agent id so callers can assert node↔agent links.
func seededCampaign(t *testing.T) (*storage.Store, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	pool := migratedPool(t)
	if err := wirenpc.SeedNPC(ctx, pool, testCipher(t), nil); err != nil {
		t.Fatalf("SeedNPC: %v", err)
	}
	st := storage.New(pool)

	tenant, err := st.FindTenantByName(ctx, wirenpc.SeedTenantName)
	if err != nil {
		t.Fatalf("FindTenantByName: %v", err)
	}
	campaign, err := st.FindCampaignByName(ctx, tenant.ID, wirenpc.SeedCampaignName)
	if err != nil {
		t.Fatalf("FindCampaignByName: %v", err)
	}
	cid := campaign.ID

	agents, err := st.ListAgents(ctx, cid)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	var bartID uuid.UUID
	for _, a := range agents {
		if a.Role == storage.AgentRoleCharacter && a.Name == "Bart" {
			bartID = a.ID
		}
	}
	if bartID == uuid.Nil {
		t.Fatal("seed did not create Bart")
	}

	loc, err := st.CreateNode(ctx, storage.NewKGNode{
		CampaignID: cid, Type: storage.KGNodeLocation, Name: "The Prancing Pony", Body: "A cozy inn.",
	})
	if err != nil {
		t.Fatalf("CreateNode location: %v", err)
	}
	npcNode, err := st.CreateNode(ctx, storage.NewKGNode{
		CampaignID: cid, Type: storage.KGNodeNPC, Name: "Barliman", GMPrivate: true,
	})
	if err != nil {
		t.Fatalf("CreateNode npc: %v", err)
	}
	if _, err := st.SetNodeAgent(ctx, cid, npcNode.ID, uuid.NullUUID{UUID: bartID, Valid: true}); err != nil {
		t.Fatalf("SetNodeAgent: %v", err)
	}
	if _, err := st.CreateEdge(ctx, storage.NewKGEdge{
		CampaignID: cid, FromNodeID: npcNode.ID, ToNodeID: loc.ID, Type: storage.KGEdgeResidesIn,
	}); err != nil {
		t.Fatalf("CreateEdge: %v", err)
	}
	if _, err := st.CreateCharacter(ctx, storage.NewCharacter{
		CampaignID: cid, Name: "Frodo", DiscordUserID: "123456789", Aliases: []string{"Mr. Underhill"},
	}); err != nil {
		t.Fatalf("CreateCharacter: %v", err)
	}
	return st, cid, bartID
}

// TestExportDemoCampaign is TEST 1: the seeded demo exports Butler + Bart with
// dice grants, KG nodes/edges with source-UUID refs, and no History by default.
func TestExportDemoCampaign(t *testing.T) {
	ctx := context.Background()
	st, cid, bartID := seededCampaign(t)

	b, err := bundle.Export(ctx, st, cid, bundle.ExportOptions{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	if b.FormatVersion != bundle.FormatVersion {
		t.Errorf("FormatVersion = %d, want %d", b.FormatVersion, bundle.FormatVersion)
	}
	if b.Campaign.History != nil {
		t.Errorf("default export has History; want nil")
	}
	if b.Campaign.Name != wirenpc.SeedCampaignName {
		t.Errorf("Name = %q, want %q", b.Campaign.Name, wirenpc.SeedCampaignName)
	}

	var butler, bart *bundle.Agent
	for i := range b.Campaign.Agents {
		switch b.Campaign.Agents[i].Role {
		case "butler":
			butler = &b.Campaign.Agents[i]
		case "character":
			if b.Campaign.Agents[i].Name == "Bart" {
				bart = &b.Campaign.Agents[i]
			}
		}
	}
	if butler == nil {
		t.Fatal("bundle missing Butler agent")
	}
	if bart == nil {
		t.Fatal("bundle missing Bart agent")
	}
	if bart.ID != bartID.String() {
		t.Errorf("Bart ref = %q, want source UUID %q", bart.ID, bartID.String())
	}
	if !hasGrant(bart.Grants, "dice") {
		t.Errorf("Bart missing dice grant, got %+v", bart.Grants)
	}
	if !hasGrant(butler.Grants, "dice") {
		t.Errorf("Butler missing dice grant, got %+v", butler.Grants)
	}

	// KG: an NPC node linked to Bart, a location node, one edge with UUID refs.
	nodeIDs := map[string]bool{}
	var linkedToBart bool
	for _, n := range b.Campaign.Nodes {
		if _, err := uuid.Parse(n.ID); err != nil {
			t.Errorf("node ref %q is not a source UUID", n.ID)
		}
		nodeIDs[n.ID] = true
		if n.AgentID == bartID.String() {
			linkedToBart = true
		}
	}
	if !linkedToBart {
		t.Errorf("no node links to Bart's agent id %s", bartID)
	}
	if len(b.Campaign.Edges) != 1 {
		t.Fatalf("edges = %d, want 1", len(b.Campaign.Edges))
	}
	e := b.Campaign.Edges[0]
	if !nodeIDs[e.From] || !nodeIDs[e.To] {
		t.Errorf("edge endpoints %q->%q are not node refs", e.From, e.To)
	}

	if len(b.Campaign.Characters) != 1 || b.Campaign.Characters[0].Name != "Frodo" {
		t.Errorf("characters = %+v, want one Frodo", b.Campaign.Characters)
	}
}

// TestExportHistoryFlag is TEST 3: IncludeHistory omits/includes transcript
// sections, sessions nest lines (seq order) and chunks grouped by session, and
// chunk participated refs are agent UUID strings.
func TestExportHistoryFlag(t *testing.T) {
	ctx := context.Background()
	st, cid, bartID := seededCampaign(t)

	vs, err := st.CreateVoiceSession(ctx, cid)
	if err != nil {
		t.Fatalf("CreateVoiceSession: %v", err)
	}
	base := time.Now().UTC().Truncate(time.Millisecond)
	// Insert two lines OUT of seq order to prove the export orders by seq.
	if err := st.UpsertTranscriptLine(ctx, storage.TranscriptLine{
		VoiceSessionID: vs.ID, CampaignID: cid, LineID: "l2", Seq: 2,
		Who: "Bart", Kind: "agent", TS: base.Add(time.Second), Text: "second",
	}); err != nil {
		t.Fatalf("UpsertTranscriptLine l2: %v", err)
	}
	if err := st.UpsertTranscriptLine(ctx, storage.TranscriptLine{
		VoiceSessionID: vs.ID, CampaignID: cid, LineID: "l1", Seq: 1,
		Who: "Frodo", Kind: "human", TS: base, Text: "first", SpeakerDiscordUserID: "123456789",
	}); err != nil {
		t.Fatalf("UpsertTranscriptLine l1: %v", err)
	}
	if _, err := st.InsertTranscriptChunk(ctx, storage.TranscriptChunk{
		CampaignID: cid, VoiceSessionID: vs.ID, Content: "first\nsecond",
		SpeakerDiscordUserIDs: []string{"123456789"},
		ParticipatedAgentIDs:  []uuid.UUID{bartID},
		StartedAt:             base,
	}); err != nil {
		t.Fatalf("InsertTranscriptChunk: %v", err)
	}

	// IncludeHistory=false omits sessions even though transcript rows exist.
	noHist, err := bundle.Export(ctx, st, cid, bundle.ExportOptions{IncludeHistory: false})
	if err != nil {
		t.Fatalf("Export no-history: %v", err)
	}
	if noHist.Campaign.History != nil {
		t.Errorf("IncludeHistory=false still nested History")
	}

	hist, err := bundle.Export(ctx, st, cid, bundle.ExportOptions{IncludeHistory: true})
	if err != nil {
		t.Fatalf("Export history: %v", err)
	}
	if hist.Campaign.History == nil || len(hist.Campaign.History.Sessions) != 1 {
		t.Fatalf("history sessions = %+v, want 1", hist.Campaign.History)
	}
	s := hist.Campaign.History.Sessions[0]
	if s.ID != vs.ID.String() {
		t.Errorf("session ref %q, want %q", s.ID, vs.ID)
	}
	if len(s.Lines) != 2 || s.Lines[0].LineID != "l1" || s.Lines[1].LineID != "l2" {
		t.Errorf("lines not in seq order: %+v", s.Lines)
	}
	if len(s.Chunks) != 1 {
		t.Fatalf("chunks = %d, want 1", len(s.Chunks))
	}
	if got := s.Chunks[0].ParticipatedAgentIDs; len(got) != 1 || got[0] != bartID.String() {
		t.Errorf("chunk participated refs = %v, want [%s]", got, bartID)
	}
}

// TestExportVoiceRoundTrip is TEST 4: Bart's seeded canonical voice round-trips
// through VoiceFromJSON, and no provider-config FK id appears in the JSON.
func TestExportVoiceRoundTrip(t *testing.T) {
	ctx := context.Background()
	st, cid, _ := seededCampaign(t)

	// Collect the provider-config FK ids that must NOT leak into the bundle.
	agents, err := st.ListAgents(ctx, cid)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	var fkIDs []string
	var bartVoice []byte
	for _, a := range agents {
		if a.VoiceProviderConfigID.Valid {
			fkIDs = append(fkIDs, a.VoiceProviderConfigID.UUID.String())
		}
		if a.LLMProviderConfigID.Valid {
			fkIDs = append(fkIDs, a.LLMProviderConfigID.UUID.String())
		}
		if a.Name == "Bart" {
			bartVoice = a.Voice
		}
	}
	if len(fkIDs) == 0 {
		t.Fatal("seed produced no provider FK ids to check exclusion against")
	}

	b, err := bundle.Export(ctx, st, cid, bundle.ExportOptions{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Bart's exported voice must decode to the same canonical Voice the column holds.
	var bart *bundle.Agent
	for i := range b.Campaign.Agents {
		if b.Campaign.Agents[i].Name == "Bart" {
			bart = &b.Campaign.Agents[i]
		}
	}
	if bart == nil || len(bart.Voice) == 0 {
		t.Fatal("Bart has no exported voice")
	}
	wantVoice, err := storage.VoiceFromJSON(bartVoice)
	if err != nil {
		t.Fatalf("decode seeded voice: %v", err)
	}
	gotVoice, err := storage.VoiceFromJSON(bart.Voice)
	if err != nil {
		t.Fatalf("decode exported voice: %v", err)
	}
	if gotVoice.ProviderID != wantVoice.ProviderID || gotVoice.VoiceID != wantVoice.VoiceID {
		t.Errorf("voice round-trip mismatch: got %+v want %+v", gotVoice, wantVoice)
	}

	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}
	for _, id := range fkIDs {
		if bytes.Contains(raw, []byte(id)) {
			t.Errorf("provider FK id %s leaked into bundle JSON", id)
		}
	}
}

// TestExportHistorySkipsSessionlessChunk pins the contract that a Transcript
// Chunk with a NULL voice_session_id (the ADR-0011 nullable SEAM — reads back as
// uuid.Nil) is skipped from the history payload rather than orphaned under a nil
// map key. The FK forbids a non-null non-existent session, so a raw NULL insert
// is the only way to reach the guard; no storage writer produces it.
func TestExportHistorySkipsSessionlessChunk(t *testing.T) {
	ctx := context.Background()
	pool := migratedPool(t)
	if err := wirenpc.SeedNPC(ctx, pool, testCipher(t), nil); err != nil {
		t.Fatalf("SeedNPC: %v", err)
	}
	st := storage.New(pool)
	tenant, err := st.FindTenantByName(ctx, wirenpc.SeedTenantName)
	if err != nil {
		t.Fatalf("FindTenantByName: %v", err)
	}
	campaign, err := st.FindCampaignByName(ctx, tenant.ID, wirenpc.SeedCampaignName)
	if err != nil {
		t.Fatalf("FindCampaignByName: %v", err)
	}
	cid := campaign.ID

	vs, err := st.CreateVoiceSession(ctx, cid)
	if err != nil {
		t.Fatalf("CreateVoiceSession: %v", err)
	}
	base := time.Now().UTC()
	if _, err := st.InsertTranscriptChunk(ctx, storage.TranscriptChunk{
		CampaignID: cid, VoiceSessionID: vs.ID, Content: "bound", StartedAt: base,
	}); err != nil {
		t.Fatalf("InsertTranscriptChunk bound: %v", err)
	}
	// Raw NULL voice_session_id insert — unreachable through the storage writer,
	// which always binds a session; the FK only forbids a non-null orphan.
	if _, err := pool.Exec(ctx,
		`INSERT INTO transcript_chunk
		   (campaign_id, voice_session_id, content, speaker_discord_user_ids, participated_agent_ids, started_at)
		 VALUES ($1, NULL, $2, '{}', '{}', $3)`,
		cid, "orphan", base); err != nil {
		t.Fatalf("raw null-session chunk insert: %v", err)
	}

	b, err := bundle.Export(ctx, st, cid, bundle.ExportOptions{IncludeHistory: true})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if b.Campaign.History == nil || len(b.Campaign.History.Sessions) != 1 {
		t.Fatalf("history sessions = %+v, want 1", b.Campaign.History)
	}
	var total int
	for _, s := range b.Campaign.History.Sessions {
		for _, c := range s.Chunks {
			total++
			if c.Content == "orphan" {
				t.Errorf("sessionless chunk leaked into history")
			}
		}
	}
	if total != 1 {
		t.Errorf("exported chunks = %d, want 1 (orphan skipped)", total)
	}
}

func hasGrant(gs []bundle.Grant, tool string) bool {
	for _, g := range gs {
		if g.ToolName == tool {
			return true
		}
	}
	return false
}
