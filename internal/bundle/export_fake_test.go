package bundle_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/bundle"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// This file is the DB-free half of the export suite (#451): Export runs
// against the in-memory fakeStore through [bundle.ExportStore], so ref
// emission, history grouping, and voice normalization are proven under plain
// `go test`. SQL-shaped reads (real ordering collations, the raw-NULL chunk
// legacy shape) stay in the integration suite.

// seedFakeCampaign populates a fake with one campaign through the seam's own
// write methods — the auto-Butler plus a voiced NPC (linked to its KG node),
// a location, one edge, one player Character, and a terminal session with
// out-of-order lines and one chunk naming the NPC.
func seedFakeCampaign(t *testing.T, f *fakeStore) (campaignID, bartID uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	campaignID, err := f.CreateCampaign(ctx, storage.NewCampaign{
		TenantID: uuid.New(), Name: "Erebor Reclaimed", System: "dnd5e", Language: "en",
	})
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	bartID, err = f.CreateAgent(ctx, storage.NewAgent{
		CampaignID: campaignID, Role: storage.AgentRoleCharacter, Name: "Bart",
		Title: "Innkeeper", Persona: "gruff but fair",
		Voice: seamVoiceJSON(t, "elevenlabs", "bart-voice"), Aliases: []string{"Barkeep"},
	})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	if _, err := f.CreateToolGrant(ctx, storage.NewToolGrant{AgentID: bartID, ToolName: "dice"}); err != nil {
		t.Fatalf("CreateToolGrant: %v", err)
	}

	npc, err := f.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: "npc", Name: "Bart", Body: "keeps the inn",
	})
	if err != nil {
		t.Fatalf("CreateNode npc: %v", err)
	}
	if _, err := f.SetNodeAgent(ctx, campaignID, npc.ID, uuid.NullUUID{UUID: bartID, Valid: true}); err != nil {
		t.Fatalf("SetNodeAgent: %v", err)
	}
	loc, err := f.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: "location", Name: "The Prancing Pony", GMPrivate: true,
	})
	if err != nil {
		t.Fatalf("CreateNode location: %v", err)
	}
	if _, err := f.CreateEdge(ctx, storage.NewKGEdge{
		CampaignID: campaignID, FromNodeID: npc.ID, ToNodeID: loc.ID, Type: "resides_in",
	}); err != nil {
		t.Fatalf("CreateEdge: %v", err)
	}
	if _, err := f.CreateCharacter(ctx, storage.NewCharacter{
		CampaignID: campaignID, Name: "Frodo", Aliases: []string{"Ringbearer"},
		DiscordUserID: "199999999999999999",
	}); err != nil {
		t.Fatalf("CreateCharacter: %v", err)
	}

	started := time.Date(2026, 4, 20, 19, 0, 0, 0, time.UTC)
	ended := started.Add(2 * time.Hour)
	sid, err := f.ImportVoiceSession(ctx, storage.VoiceSession{
		CampaignID: campaignID, StartedAt: started, EndedAt: &ended,
		Status: storage.VoiceSessionEnded, LineCount: 2,
	})
	if err != nil {
		t.Fatalf("ImportVoiceSession: %v", err)
	}
	for _, l := range []storage.TranscriptLine{
		{VoiceSessionID: sid, CampaignID: campaignID, LineID: "l2", Seq: 5, Who: "Bart", Kind: "agent", TS: started.Add(time.Minute), Text: "Welcome back."},
		{VoiceSessionID: sid, CampaignID: campaignID, LineID: "l1", Seq: 2, Who: "Frodo", Kind: "human", TS: started, Text: "Hello!", SpeakerDiscordUserID: "199999999999999999"},
	} {
		if err := f.UpsertTranscriptLine(ctx, l); err != nil {
			t.Fatalf("UpsertTranscriptLine: %v", err)
		}
	}
	if _, err := f.InsertTranscriptChunk(ctx, storage.TranscriptChunk{
		CampaignID: campaignID, VoiceSessionID: sid,
		Content:               "Frodo: Hello!\nBart: Welcome back.",
		SpeakerDiscordUserIDs: []string{"199999999999999999"},
		ParticipatedAgentIDs:  []uuid.UUID{bartID},
		StartedAt:             started,
	}); err != nil {
		t.Fatalf("InsertTranscriptChunk: %v", err)
	}
	return campaignID, bartID
}

// TestExportFake_RefsAreSourceUUIDs: ADR-0053 §4 over the fake — every
// reference the bundle carries (agent ids, node ids, edge endpoints, chunk
// participants) is the source row's UUID string, wired consistently.
func TestExportFake_RefsAreSourceUUIDs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFakeStore()
	campaignID, bartID := seedFakeCampaign(t, f)

	b, err := bundle.Export(ctx, f, campaignID, bundle.ExportOptions{IncludeHistory: true})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if b.FormatVersion != bundle.FormatVersion || b.Campaign.Name != "Erebor Reclaimed" {
		t.Errorf("envelope = v%d %q, want v%d Erebor Reclaimed", b.FormatVersion, b.Campaign.Name, bundle.FormatVersion)
	}

	// Agents: Butler first (role ordering), then Bart; ids are row UUIDs.
	if len(b.Campaign.Agents) != 2 || b.Campaign.Agents[0].Role != "butler" || b.Campaign.Agents[1].Name != "Bart" {
		t.Fatalf("agents = %+v, want [butler Glyphoxa, character Bart]", b.Campaign.Agents)
	}
	if b.Campaign.Agents[1].ID != bartID.String() {
		t.Errorf("Bart ref = %q, want source uuid %s", b.Campaign.Agents[1].ID, bartID)
	}
	butler, _ := f.GetButler(ctx, campaignID)
	if b.Campaign.Agents[0].ID != butler.ID.String() {
		t.Errorf("butler ref = %q, want source uuid %s", b.Campaign.Agents[0].ID, butler.ID)
	}
	wantDefaults := []string{"dice", "kg_query", "recap", "transcript_search"}
	if got := b.Campaign.Agents[0].Grants; len(got) != len(wantDefaults) {
		t.Errorf("butler grants = %+v, want the trigger defaults %v", got, wantDefaults)
	} else {
		for i, g := range got {
			if g.ToolName != wantDefaults[i] {
				t.Errorf("butler grant[%d] = %q, want %q (tool-name order)", i, g.ToolName, wantDefaults[i])
			}
		}
	}

	// Nodes: node_type is a Postgres ENUM ordered by declaration, so 'npc'
	// sorts BEFORE 'location'; the NPC node references Bart, and both refs
	// are the fake's row UUIDs.
	if len(b.Campaign.Nodes) != 2 || b.Campaign.Nodes[0].Type != "npc" || b.Campaign.Nodes[1].Type != "location" {
		t.Fatalf("nodes = %+v, want [npc, location] in enum order", b.Campaign.Nodes)
	}
	npcNode, locNode := b.Campaign.Nodes[0], b.Campaign.Nodes[1]
	if npcNode.ID != nodeNamed(t, f, campaignID, "Bart").ID.String() ||
		locNode.ID != nodeNamed(t, f, campaignID, "The Prancing Pony").ID.String() {
		t.Errorf("node refs = %q/%q, want the source row uuids", npcNode.ID, locNode.ID)
	}
	if npcNode.AgentID != bartID.String() {
		t.Errorf("npc node agent link = %q, want %s", npcNode.AgentID, bartID)
	}
	if !locNode.GMPrivate {
		t.Error("gm_private flag lost on export")
	}

	// Edge endpoints are the node UUID strings.
	if len(b.Campaign.Edges) != 1 ||
		b.Campaign.Edges[0].From != npcNode.ID || b.Campaign.Edges[0].To != locNode.ID {
		t.Errorf("edge = %+v, want npc->location by node refs", b.Campaign.Edges)
	}

	// Character verbatim.
	if len(b.Campaign.Characters) != 1 || b.Campaign.Characters[0].DiscordUserID != "199999999999999999" {
		t.Errorf("characters = %+v, want Frodo's snowflake verbatim", b.Campaign.Characters)
	}

	// History: session ref is the row uuid, lines in seq order, chunk
	// participants are agent UUID strings.
	if b.Campaign.History == nil || len(b.Campaign.History.Sessions) != 1 {
		t.Fatalf("history = %+v, want one session", b.Campaign.History)
	}
	s := b.Campaign.History.Sessions[0]
	if s.ID != f.sessions[0].ID.String() {
		t.Errorf("session ref = %q, want source uuid %s", s.ID, f.sessions[0].ID)
	}
	if len(s.Lines) != 2 || s.Lines[0].LineID != "l1" || s.Lines[1].LineID != "l2" {
		t.Errorf("lines = %+v, want seq order l1,l2", s.Lines)
	}
	if len(s.Chunks) != 1 || len(s.Chunks[0].ParticipatedAgentIDs) != 1 ||
		s.Chunks[0].ParticipatedAgentIDs[0] != bartID.String() {
		t.Errorf("chunk participants = %+v, want [%s]", s.Chunks, bartID)
	}
}

// TestExportFake_HistoryFlagGates: the default export is a SETUP share —
// History stays nil even though the store holds sessions (ADR-0053 §1).
func TestExportFake_HistoryFlagGates(t *testing.T) {
	t.Parallel()
	f := newFakeStore()
	campaignID, _ := seedFakeCampaign(t, f)

	b, err := bundle.Export(context.Background(), f, campaignID, bundle.ExportOptions{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if b.Campaign.History != nil {
		t.Errorf("default export carries history: %+v", b.Campaign.History)
	}
}

// TestExportFake_SkipsSessionlessChunk: a chunk without a Voice Session
// binding (legacy NULL column shape) has no session to nest under and is
// skipped from the history payload rather than orphaned.
func TestExportFake_SkipsSessionlessChunk(t *testing.T) {
	t.Parallel()
	f := newFakeStore()
	campaignID, _ := seedFakeCampaign(t, f)
	// Injected directly: the seam's writer always binds a session, so the
	// legacy shape can only exist as a pre-seam row.
	f.chunks = append(f.chunks, storage.TranscriptChunk{
		ID: uuid.New(), CampaignID: campaignID, VoiceSessionID: uuid.Nil,
		Content: "orphaned", StartedAt: time.Date(2026, 4, 21, 19, 0, 0, 0, time.UTC),
	})

	b, err := bundle.Export(context.Background(), f, campaignID, bundle.ExportOptions{IncludeHistory: true})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	total := 0
	for _, s := range b.Campaign.History.Sessions {
		for _, c := range s.Chunks {
			total++
			if c.Content == "orphaned" {
				t.Error("sessionless chunk leaked into a session")
			}
		}
	}
	if total != 1 {
		t.Errorf("exported chunks = %d, want 1 (the session-bound one)", total)
	}
}

// TestExportFake_VoiceHandling: a '{}' default voice exports as absent, a real
// voice round-trips the canonical shape, and an unparsable column is a hard
// error (#224) — never a silently dropped voice.
func TestExportFake_VoiceHandling(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("zero voice exports absent", func(t *testing.T) {
		t.Parallel()
		f := newFakeStore()
		campaignID, _ := seedFakeCampaign(t, f)
		b, err := bundle.Export(ctx, f, campaignID, bundle.ExportOptions{})
		if err != nil {
			t.Fatalf("Export: %v", err)
		}
		if voice := b.Campaign.Agents[0].Voice; len(voice) != 0 { // the un-voiced Butler
			t.Errorf("butler voice = %s, want absent", voice)
		}
		got, err := storage.VoiceFromJSON(b.Campaign.Agents[1].Voice)
		if err != nil || got.ProviderID != "elevenlabs" || got.VoiceID != "bart-voice" {
			t.Errorf("Bart voice = %+v (err %v), want canonical elevenlabs/bart-voice", got, err)
		}
	})
	t.Run("unparsable voice column fails loudly", func(t *testing.T) {
		t.Parallel()
		f := newFakeStore()
		campaignID, _ := seedFakeCampaign(t, f)
		for i := range f.agents {
			if f.agents[i].Name == "Bart" {
				f.agents[i].Voice = json.RawMessage(`{corrupt`)
			}
		}
		if _, err := bundle.Export(ctx, f, campaignID, bundle.ExportOptions{}); err == nil ||
			!strings.Contains(err.Error(), "voice") {
			t.Fatalf("err = %v, want voice decode failure", err)
		}
	})
}

// TestExportFake_UnknownCampaign: an id the store has never seen surfaces the
// storage sentinel, which the HTTP layer maps to 404.
func TestExportFake_UnknownCampaign(t *testing.T) {
	t.Parallel()
	_, err := bundle.Export(context.Background(), newFakeStore(), uuid.New(), bundle.ExportOptions{})
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("err = %v, want storage.ErrNotFound", err)
	}
}

// normalizeRefs rewrites every UUID-valued ref in b to a stable name-derived
// key (agents/nodes by name, sessions by start time) and zeroes ExportedAt, so
// two exports of semantically identical campaigns compare byte-equal even
// though every minted UUID differs.
func normalizeRefs(t *testing.T, b *bundle.Bundle) {
	t.Helper()
	key := make(map[string]string)
	for i := range b.Campaign.Agents {
		a := &b.Campaign.Agents[i]
		key[a.ID] = "agent:" + a.Name
		a.ID = key[a.ID]
	}
	for i := range b.Campaign.Nodes {
		n := &b.Campaign.Nodes[i]
		key[n.ID] = "node:" + n.Name
		n.ID = key[n.ID]
		if n.AgentID != "" {
			n.AgentID = mustKey(t, key, n.AgentID)
		}
	}
	for i := range b.Campaign.Edges {
		e := &b.Campaign.Edges[i]
		e.From = mustKey(t, key, e.From)
		e.To = mustKey(t, key, e.To)
	}
	if b.Campaign.History != nil {
		for i := range b.Campaign.History.Sessions {
			s := &b.Campaign.History.Sessions[i]
			s.ID = "session:" + s.StartedAt.UTC().Format(time.RFC3339)
			for j := range s.Chunks {
				refs := s.Chunks[j].ParticipatedAgentIDs
				for k := range refs {
					refs[k] = mustKey(t, key, refs[k])
				}
			}
		}
	}
	b.ExportedAt = time.Time{}
}

func mustKey(t *testing.T, key map[string]string, ref string) string {
	t.Helper()
	mapped, ok := key[ref]
	if !ok {
		t.Fatalf("ref %q resolves to no exported entity", ref)
	}
	return mapped
}

// TestExportImportFake_RoundTrip is the seam-level parity proof: a campaign
// exported from one fake, imported into a second, and re-exported yields the
// SAME bundle modulo minted ids — names, personas, grants (including the
// Butler merge), KG wiring, characters, and history all survive the trip.
func TestExportImportFake_RoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	src := newFakeStore()
	campaignID, _ := seedFakeCampaign(t, src)
	original, err := bundle.Export(ctx, src, campaignID, bundle.ExportOptions{IncludeHistory: true})
	if err != nil {
		t.Fatalf("export source: %v", err)
	}

	dst := newFakeStore()
	res, err := bundle.Import(ctx, dst, uuid.New(), original)
	if err != nil {
		t.Fatalf("import into destination: %v", err)
	}
	roundTripped, err := bundle.Export(ctx, dst, res.CampaignID, bundle.ExportOptions{IncludeHistory: true})
	if err != nil {
		t.Fatalf("re-export destination: %v", err)
	}

	// Fresh mint before normalization: no destination ref of ANY kind (agent,
	// node, session) may equal a source ref.
	srcIDs := make(map[string]bool)
	for _, a := range original.Campaign.Agents {
		srcIDs[a.ID] = true
	}
	for _, n := range original.Campaign.Nodes {
		srcIDs[n.ID] = true
	}
	for _, s := range original.Campaign.History.Sessions {
		srcIDs[s.ID] = true
	}
	for _, a := range roundTripped.Campaign.Agents {
		if srcIDs[a.ID] {
			t.Errorf("agent ref %s survived the import un-minted", a.ID)
		}
	}
	for _, n := range roundTripped.Campaign.Nodes {
		if srcIDs[n.ID] {
			t.Errorf("node ref %s survived the import un-minted", n.ID)
		}
	}
	for _, s := range roundTripped.Campaign.History.Sessions {
		if srcIDs[s.ID] {
			t.Errorf("session ref %s survived the import un-minted", s.ID)
		}
	}

	normalizeRefs(t, original)
	normalizeRefs(t, roundTripped)
	want, err := json.MarshalIndent(original, "", "  ")
	if err != nil {
		t.Fatalf("marshal original: %v", err)
	}
	got, err := json.MarshalIndent(roundTripped, "", "  ")
	if err != nil {
		t.Fatalf("marshal round-tripped: %v", err)
	}
	if string(want) != string(got) {
		t.Errorf("round trip diverged:\n--- source export ---\n%s\n--- destination re-export ---\n%s", want, got)
	}
}
