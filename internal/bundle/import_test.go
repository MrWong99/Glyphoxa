//go:build integration

package bundle_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/bundle"
	"github.com/MrWong99/Glyphoxa/internal/embedworker"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/embeddings/embeddingstest"
)

// discardLogger swallows the embedworker's log output in the drain test.
func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// freshTenant migrates a new DB and returns a Store + a fresh tenant to import
// into — the "second database" the round-trip acceptance criterion demands.
func freshTenant(t *testing.T) (*storage.Store, uuid.UUID) {
	t.Helper()
	pool := migratedPool(t)
	st := storage.New(pool)
	tid, err := st.CreateTenant(context.Background(), "Import Target")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	return st, tid
}

// TestImportRefusesNewerVersion is TEST 3: a bundle written by a newer build is
// refused (ErrNewerFormat) before any DB work — no campaign lands.
func TestImportRefusesNewerVersion(t *testing.T) {
	ctx := context.Background()
	st, tid := freshTenant(t)

	b := &bundle.Bundle{
		FormatVersion: bundle.FormatVersion + 1,
		Campaign:      bundle.Campaign{Name: "From The Future", System: "dnd5e", Language: "en"},
	}
	_, err := bundle.Import(ctx, st, tid, b)
	if !errors.Is(err, bundle.ErrNewerFormat) {
		t.Fatalf("Import err = %v, want ErrNewerFormat", err)
	}
	if _, err := st.FindCampaignByName(ctx, tid, "From The Future"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("refused import still wrote a campaign: %v", err)
	}
}

// TestImportMinimalCampaign is TEST 4: a hand-written campaign-only bundle creates
// the campaign row and the trigger-created Butler with its default dice grant.
func TestImportMinimalCampaign(t *testing.T) {
	ctx := context.Background()
	st, tid := freshTenant(t)

	b := &bundle.Bundle{
		FormatVersion: bundle.FormatVersion,
		Campaign:      bundle.Campaign{Name: "Bare Campaign", System: "dnd5e", Language: "en"},
	}
	res, err := bundle.Import(ctx, st, tid, b)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.CampaignID == uuid.Nil {
		t.Fatal("result CampaignID unset")
	}
	if res.Name != "Bare Campaign" {
		t.Errorf("result Name = %q", res.Name)
	}

	butler, err := st.GetButler(ctx, res.CampaignID)
	if err != nil {
		t.Fatalf("GetButler: %v", err)
	}
	grants, err := st.ListToolGrants(ctx, butler.ID)
	if err != nil {
		t.Fatalf("ListToolGrants: %v", err)
	}
	if len(grants) != 1 || grants[0].ToolName != "dice" {
		t.Fatalf("trigger butler grants = %+v, want single dice", grants)
	}
}

// TestImportButlerMerge is TEST 5: a bundle Butler with persona/voice/grants
// produces exactly ONE Butler row, its editable fields UPDATEd, its grants equal
// to the bundle's exactly (the trigger dice replaced, not duplicated),
// address_only pinned true, and provider FKs NULL.
func TestImportButlerMerge(t *testing.T) {
	ctx := context.Background()
	st, tid := freshTenant(t)

	voice := json.RawMessage(`{"ProviderID":"elevenlabs","VoiceID":"butler-voice","Name":"Jeeves"}`)
	b := &bundle.Bundle{
		FormatVersion: bundle.FormatVersion,
		Campaign: bundle.Campaign{
			Name: "Butler Merge", System: "dnd5e", Language: "en",
			Agents: []bundle.Agent{{
				ID: "butler-ref", Role: "butler", Name: "Alfred", Title: "The Butler",
				Persona: "Impeccably dry.", Voice: voice, AddressOnly: true,
				Aliases: []string{"Al"},
				Grants:  []bundle.Grant{{ToolName: "rules_lookup"}},
			}},
		},
	}
	res, err := bundle.Import(ctx, st, tid, b)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Agents != 1 {
		t.Errorf("result Agents = %d, want 1", res.Agents)
	}

	agents, err := st.ListAgents(ctx, res.CampaignID)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	butlers := 0
	for _, a := range agents {
		if a.Role == storage.AgentRoleButler {
			butlers++
		}
	}
	if butlers != 1 {
		t.Fatalf("butler count = %d, want exactly 1", butlers)
	}

	butler, err := st.GetButler(ctx, res.CampaignID)
	if err != nil {
		t.Fatalf("GetButler: %v", err)
	}
	if butler.Name != "Alfred" || butler.Title != "The Butler" || butler.Persona != "Impeccably dry." {
		t.Errorf("butler fields not merged: %+v", butler)
	}
	if !butler.AddressOnly {
		t.Error("butler address_only not pinned true")
	}
	if butler.VoiceProviderConfigID.Valid || butler.LLMProviderConfigID.Valid {
		t.Error("butler provider FKs not NULL")
	}
	if id, ok := res.AgentIDs["butler-ref"]; !ok || id != butler.ID {
		t.Errorf("AgentIDs[butler-ref] = %v, want %v", id, butler.ID)
	}

	grants, err := st.ListToolGrants(ctx, butler.ID)
	if err != nil {
		t.Fatalf("ListToolGrants: %v", err)
	}
	if len(grants) != 1 || grants[0].ToolName != "rules_lookup" {
		t.Fatalf("butler grants = %+v, want single rules_lookup (dice replaced)", grants)
	}
}

// TestImportRoundTrip is TEST 6: export a seeded campaign (Butler + Bart + grants
// + KG + PC) and import it into a SECOND database under a fresh tenant. The domain
// grains are reproduced, the node↔agent link is remapped, every minted UUID
// differs from the source, and the Character's discord_user_id is verbatim.
func TestImportRoundTrip(t *testing.T) {
	ctx := context.Background()
	src, srcCID, srcBartID := seededCampaign(t)

	b, err := bundle.Export(ctx, src, srcCID, bundle.ExportOptions{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	dst, tid := freshTenant(t)
	res, err := bundle.Import(ctx, dst, tid, b)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	if res.CampaignID == srcCID {
		t.Error("imported campaign id equals source (not minted fresh)")
	}
	if res.Agents != len(b.Campaign.Agents) {
		t.Errorf("result Agents = %d, want %d", res.Agents, len(b.Campaign.Agents))
	}

	agents, err := dst.ListAgents(ctx, res.CampaignID)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	var dstBart storage.Agent
	var haveButler bool
	for _, a := range agents {
		if a.ID == srcBartID {
			t.Error("imported Bart reused source UUID")
		}
		switch a.Role {
		case storage.AgentRoleButler:
			haveButler = true
			if a.VoiceProviderConfigID.Valid || a.LLMProviderConfigID.Valid {
				t.Error("imported butler carries provider FKs")
			}
		case storage.AgentRoleCharacter:
			if a.Name == "Bart" {
				dstBart = a
			}
		}
	}
	if !haveButler {
		t.Fatal("imported campaign has no butler")
	}
	if dstBart.ID == uuid.Nil {
		t.Fatal("imported campaign missing Bart")
	}
	if dstBart.VoiceProviderConfigID.Valid || dstBart.LLMProviderConfigID.Valid {
		t.Error("imported Bart carries provider FKs; want NULL")
	}

	bartGrants, err := dst.ListToolGrants(ctx, dstBart.ID)
	if err != nil {
		t.Fatalf("ListToolGrants(Bart): %v", err)
	}
	if len(bartGrants) != 1 || bartGrants[0].ToolName != "dice" {
		t.Errorf("Bart grants = %+v, want single dice", bartGrants)
	}

	nodes, err := dst.ListNodes(ctx, res.CampaignID)
	if err != nil {
		t.Fatalf("ListNodes: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("node count = %d, want 2", len(nodes))
	}
	var npcNode, locNode storage.KGNode
	for _, n := range nodes {
		switch n.Type {
		case storage.KGNodeNPC:
			npcNode = n
			if !n.AgentID.Valid || n.AgentID.UUID != dstBart.ID {
				t.Errorf("npc node agent link = %v, want remapped Bart %v", n.AgentID, dstBart.ID)
			}
			if n.Name != "Barliman" || !n.GMPrivate {
				t.Errorf("npc node name/gm_private not round-tripped: %+v", n)
			}
		case storage.KGNodeLocation:
			locNode = n
			if n.Name != "The Prancing Pony" || n.Body != "A cozy inn." {
				t.Errorf("location node name/body not round-tripped: %+v", n)
			}
		}
	}
	if npcNode.ID == uuid.Nil || locNode.ID == uuid.Nil {
		t.Fatal("round-trip lost the npc or location node")
	}

	edges, err := dst.ListEdges(ctx, res.CampaignID)
	if err != nil {
		t.Fatalf("ListEdges: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("edge count = %d, want 1", len(edges))
	}
	// Edge content: the resides_in edge points npc -> location, direction preserved,
	// endpoints remapped to the fresh node ids.
	e := edges[0]
	if e.Type != storage.KGEdgeResidesIn || e.FromNodeID != npcNode.ID || e.ToNodeID != locNode.ID {
		t.Errorf("edge = %+v, want resides_in npc(%s)->loc(%s)", e, npcNode.ID, locNode.ID)
	}

	chars, err := dst.ListCharacters(ctx, res.CampaignID)
	if err != nil {
		t.Fatalf("ListCharacters: %v", err)
	}
	if len(chars) != 1 {
		t.Fatalf("character count = %d, want 1", len(chars))
	}
	if chars[0].Name != "Frodo" || chars[0].DiscordUserID != "123456789" ||
		len(chars[0].Aliases) != 1 || chars[0].Aliases[0] != "Mr. Underhill" {
		t.Fatalf("character not round-tripped: %+v", chars[0])
	}
}

// TestImportTwiceMakesTwoCampaigns is TEST 7: the same bundle imported twice
// yields two independent campaigns (ADR-0053 §4 always-mint).
func TestImportTwiceMakesTwoCampaigns(t *testing.T) {
	ctx := context.Background()
	src, srcCID, _ := seededCampaign(t)
	b, err := bundle.Export(ctx, src, srcCID, bundle.ExportOptions{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	dst, tid := freshTenant(t)
	first, err := bundle.Import(ctx, dst, tid, b)
	if err != nil {
		t.Fatalf("Import #1: %v", err)
	}
	second, err := bundle.Import(ctx, dst, tid, b)
	if err != nil {
		t.Fatalf("Import #2: %v", err)
	}
	if first.CampaignID == second.CampaignID {
		t.Fatal("two imports produced one campaign; want two independent")
	}
}

// TestImportMidBundleFailureRollsBack is TEST 8: an edge referencing an unknown
// node aborts the import and leaves ZERO rows (single transaction).
func TestImportMidBundleFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	st, tid := freshTenant(t)

	b := &bundle.Bundle{
		FormatVersion: bundle.FormatVersion,
		Campaign: bundle.Campaign{
			Name: "Doomed Import", System: "dnd5e", Language: "en",
			Nodes: []bundle.Node{{ID: "n1", Type: "location", Name: "Somewhere"}},
			Edges: []bundle.Edge{{From: "n1", To: "ghost", Type: "resides_in"}},
		},
	}
	if _, err := bundle.Import(ctx, st, tid, b); err == nil {
		t.Fatal("Import with unknown edge ref succeeded; want error")
	}
	if _, err := st.FindCampaignByName(ctx, tid, "Doomed Import"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("failed import left a campaign behind: %v", err)
	}
}

// TestImportRejectsSecondButler proves a bundle carrying TWO Butlers is refused
// (rollback), never last-wins: a Campaign has exactly one Butler (types.go), so a
// second would silently overwrite the first's fields/grants and inflate the
// reported Agents count above what the DB holds.
func TestImportRejectsSecondButler(t *testing.T) {
	ctx := context.Background()
	st, tid := freshTenant(t)

	b := &bundle.Bundle{
		FormatVersion: bundle.FormatVersion,
		Campaign: bundle.Campaign{
			Name: "Two Butlers", System: "dnd5e", Language: "en",
			Agents: []bundle.Agent{
				{ID: "b1", Role: "butler", Name: "Alfred"},
				{ID: "b2", Role: "butler", Name: "Jeeves"},
			},
		},
	}
	if _, err := bundle.Import(ctx, st, tid, b); err == nil {
		t.Fatal("Import with two butlers succeeded; want error")
	}
	if _, err := st.FindCampaignByName(ctx, tid, "Two Butlers"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("rejected two-butler import left a campaign: %v", err)
	}
}

// TestImportRejectsDuplicateNodeRef proves two nodes sharing a ref key abort the
// import (rollback): a silent clobber of the remap map would bind edges/links to
// the wrong node.
func TestImportRejectsDuplicateNodeRef(t *testing.T) {
	ctx := context.Background()
	st, tid := freshTenant(t)

	b := &bundle.Bundle{
		FormatVersion: bundle.FormatVersion,
		Campaign: bundle.Campaign{
			Name: "Dup Node", System: "dnd5e", Language: "en",
			Nodes: []bundle.Node{
				{ID: "n1", Type: "location", Name: "First"},
				{ID: "n1", Type: "location", Name: "Second"},
			},
		},
	}
	if _, err := bundle.Import(ctx, st, tid, b); err == nil {
		t.Fatal("Import with duplicate node ref succeeded; want error")
	}
	if _, err := st.FindCampaignByName(ctx, tid, "Dup Node"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("rejected dup-node import left a campaign: %v", err)
	}
}

// TestImportRejectsDuplicateAgentRef proves two Character Agents sharing a ref key
// abort the import (rollback): a clobbered agent remap would link a node to the
// wrong Agent.
func TestImportRejectsDuplicateAgentRef(t *testing.T) {
	ctx := context.Background()
	st, tid := freshTenant(t)

	b := &bundle.Bundle{
		FormatVersion: bundle.FormatVersion,
		Campaign: bundle.Campaign{
			Name: "Dup Agent", System: "dnd5e", Language: "en",
			Agents: []bundle.Agent{
				{ID: "a1", Role: "character", Name: "Bart"},
				{ID: "a1", Role: "character", Name: "Barty"},
			},
		},
	}
	if _, err := bundle.Import(ctx, st, tid, b); err == nil {
		t.Fatal("Import with duplicate agent ref succeeded; want error")
	}
	if _, err := st.FindCampaignByName(ctx, tid, "Dup Agent"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("rejected dup-agent import left a campaign: %v", err)
	}
}

// TestImportSessionWithLines is #292 TEST 2: a bundle carrying one Session with
// two Lines writes a terminal Voice Session, and its Lines render in seq order
// with line_id/seq VERBATIM (the ADR-0040 replay keys, never "fixed") and the
// speaker snowflake verbatim. Lines are supplied OUT of seq order to prove the
// stored seq drives replay ordering, not bundle order.
func TestImportSessionWithLines(t *testing.T) {
	ctx := context.Background()
	st, tid := freshTenant(t)

	base := time.Date(2026, 6, 1, 20, 0, 0, 0, time.UTC)
	ended := base.Add(time.Hour)
	bnd := &bundle.Bundle{
		FormatVersion: bundle.FormatVersion,
		Campaign: bundle.Campaign{
			Name: "One Session", System: "dnd5e", Language: "en",
			History: &bundle.History{Sessions: []bundle.Session{{
				ID: "s1", StartedAt: base, EndedAt: &ended, Status: "ended", LineCount: 2,
				Lines: []bundle.Line{
					{LineID: "l2", Seq: 2, Who: "Bart", Kind: "agent", TS: base.Add(time.Second), Text: "second"},
					{LineID: "l1", Seq: 1, Who: "Frodo", Kind: "human", TS: base, Text: "first", SpeakerDiscordUserID: "123456789"},
				},
			}}},
		},
	}
	res, err := bundle.Import(ctx, st, tid, bnd)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Sessions != 1 || res.Lines != 2 {
		t.Fatalf("counts sessions=%d lines=%d, want 1/2", res.Sessions, res.Lines)
	}

	sessions, err := st.ListVoiceSessions(ctx, res.CampaignID, 100)
	if err != nil {
		t.Fatalf("ListVoiceSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("voice sessions = %d, want 1", len(sessions))
	}
	vs := sessions[0]
	if vs.Status != storage.VoiceSessionEnded || vs.EndedAt == nil {
		t.Errorf("imported session not terminal: %+v", vs)
	}
	if vs.LineCount != 2 {
		t.Errorf("line_count = %d, want 2 (verbatim)", vs.LineCount)
	}

	lines, err := st.ListTranscriptLines(ctx, vs.ID)
	if err != nil {
		t.Fatalf("ListTranscriptLines: %v", err)
	}
	if len(lines) != 2 || lines[0].LineID != "l1" || lines[1].LineID != "l2" {
		t.Fatalf("lines not in seq order (line_id/seq verbatim): %+v", lines)
	}
	if lines[0].Seq != 1 || lines[1].Seq != 2 {
		t.Errorf("seq not verbatim: %+v", lines)
	}
	if lines[0].SpeakerDiscordUserID != "123456789" {
		t.Errorf("speaker snowflake = %q, want verbatim 123456789", lines[0].SpeakerDiscordUserID)
	}
	if lines[1].SpeakerDiscordUserID != "" {
		t.Errorf("agent line speaker = %q, want empty", lines[1].SpeakerDiscordUserID)
	}
}

// TestImportCoercesNonTerminalSession is #292 TEST 3: a Session exported while
// still 'running' (a source that crashed mid-session) imports as terminal
// 'ended' with ended_at defaulted to started_at — no imported row ever looks live.
func TestImportCoercesNonTerminalSession(t *testing.T) {
	ctx := context.Background()
	st, tid := freshTenant(t)

	base := time.Date(2026, 6, 3, 20, 0, 0, 0, time.UTC)
	bnd := &bundle.Bundle{
		FormatVersion: bundle.FormatVersion,
		Campaign: bundle.Campaign{
			Name: "Running Session", System: "dnd5e", Language: "en",
			History: &bundle.History{Sessions: []bundle.Session{{
				ID: "s1", StartedAt: base, Status: "running", LineCount: 0,
			}}},
		},
	}
	res, err := bundle.Import(ctx, st, tid, bnd)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	sessions, err := st.ListVoiceSessions(ctx, res.CampaignID, 100)
	if err != nil {
		t.Fatalf("ListVoiceSessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sessions))
	}
	vs := sessions[0]
	if vs.Status != storage.VoiceSessionEnded {
		t.Errorf("status = %q, want ended (coerced from running)", vs.Status)
	}
	if vs.EndedAt == nil || !vs.EndedAt.Equal(base) {
		t.Errorf("ended_at = %v, want started_at %v", vs.EndedAt, base)
	}
}

// TestImportChunksRemapAndEmbedNull is #292 TEST 4: a Chunk's participated refs
// are remapped to minted Agent ids; an unmappable ref is DROPPED and counted in
// DroppedParticipantRefs (not fatal); and every imported Chunk lands with
// embedding NULL + embedding_model ” so the embedworker regenerates it —
// CountUnembeddedChunks == the imported chunk count.
func TestImportChunksRemapAndEmbedNull(t *testing.T) {
	ctx := context.Background()
	st, tid := freshTenant(t)

	base := time.Date(2026, 6, 4, 20, 0, 0, 0, time.UTC)
	ended := base.Add(time.Hour)
	bnd := &bundle.Bundle{
		FormatVersion: bundle.FormatVersion,
		Campaign: bundle.Campaign{
			Name: "Chunky", System: "dnd5e", Language: "en",
			Agents: []bundle.Agent{{ID: "a1", Role: "character", Name: "Bart"}},
			History: &bundle.History{Sessions: []bundle.Session{{
				ID: "s1", StartedAt: base, EndedAt: &ended, Status: "ended", LineCount: 0,
				Chunks: []bundle.Chunk{{
					Content:               "the dragon spoke of gold",
					SpeakerDiscordUserIDs: []string{"111", "222"},
					ParticipatedAgentIDs:  []string{"a1", "ghost"},
					StartedAt:             base,
				}},
			}}},
		},
	}
	res, err := bundle.Import(ctx, st, tid, bnd)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Chunks != 1 {
		t.Fatalf("Chunks = %d, want 1", res.Chunks)
	}
	if res.DroppedParticipantRefs != 1 {
		t.Errorf("DroppedParticipantRefs = %d, want 1 (ghost dropped)", res.DroppedParticipantRefs)
	}

	bartID := res.AgentIDs["a1"]
	if bartID == uuid.Nil {
		t.Fatal("a1 not remapped")
	}

	// Every imported chunk is unembedded (embedding NULL by construction).
	n, err := st.CountUnembeddedChunks(ctx)
	if err != nil {
		t.Fatalf("CountUnembeddedChunks: %v", err)
	}
	if n != 1 {
		t.Errorf("unembedded chunks = %d, want 1 (import never writes vectors)", n)
	}

	chunks, err := st.ListUnembeddedChunks(ctx, 10)
	if err != nil {
		t.Fatalf("ListUnembeddedChunks: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("chunks = %d, want 1", len(chunks))
	}
	c := chunks[0]
	if len(c.ParticipatedAgentIDs) != 1 || c.ParticipatedAgentIDs[0] != bartID {
		t.Errorf("participated = %v, want [remapped Bart %s] (ghost dropped)", c.ParticipatedAgentIDs, bartID)
	}
	if c.EmbeddingModel != "" {
		t.Errorf("embedding_model = %q, want '' (embedworker stamps it)", c.EmbeddingModel)
	}
	if len(c.SpeakerDiscordUserIDs) != 2 || c.SpeakerDiscordUserIDs[0] != "111" || c.SpeakerDiscordUserIDs[1] != "222" {
		t.Errorf("speaker snowflakes = %v, want verbatim [111 222]", c.SpeakerDiscordUserIDs)
	}
}

// TestImportedChunksEmbedAndRecall is #292 TEST 5 — the recall acceptance
// criterion: imported Chunks land embedding NULL, the real embedworker (with a
// deterministic fake provider) drains the backlog to zero, and afterwards
// SearchChunksByAgent for the REMAPPED agent finds the imported content by
// nearest-vector. The importer itself never writes a vector — recall emerges
// only because the async worker regenerated the embeddings (ADR-0011).
func TestImportedChunksEmbedAndRecall(t *testing.T) {
	ctx := context.Background()
	st, tid := freshTenant(t)

	base := time.Date(2026, 6, 5, 20, 0, 0, 0, time.UTC)
	ended := base.Add(time.Hour)
	const recallText = "the innkeeper mentioned a hidden cellar beneath the Prancing Pony"
	bnd := &bundle.Bundle{
		FormatVersion: bundle.FormatVersion,
		Campaign: bundle.Campaign{
			Name: "Recall", System: "dnd5e", Language: "en",
			Agents: []bundle.Agent{{ID: "a1", Role: "character", Name: "Bart"}},
			History: &bundle.History{Sessions: []bundle.Session{{
				ID: "s1", StartedAt: base, EndedAt: &ended, Status: "ended", LineCount: 0,
				Chunks: []bundle.Chunk{{
					Content:              recallText,
					ParticipatedAgentIDs: []string{"a1"},
					StartedAt:            base,
				}},
			}}},
		},
	}
	res, err := bundle.Import(ctx, st, tid, bnd)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	bartID := res.AgentIDs["a1"]

	// Before the worker runs, recall finds nothing (embedding still NULL).
	prov := embeddingstest.Deterministic{}
	qvec, err := prov.Embed(ctx, []string{recallText})
	if err != nil {
		t.Fatalf("embed query: %v", err)
	}
	pre, err := st.SearchChunksByAgent(ctx, res.CampaignID, bartID, qvec[0], 5)
	if err != nil {
		t.Fatalf("SearchChunksByAgent (pre): %v", err)
	}
	if len(pre) != 0 {
		t.Fatalf("recall before embedding = %d hits, want 0 (embedding NULL)", len(pre))
	}

	// Drain the backlog with the real worker + deterministic provider.
	w := embedworker.New(st, prov, "test-model", nil, discardLogger(), embedworker.Config{Interval: 10 * time.Millisecond})
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { w.Run(runCtx); close(done) }()

	deadline := time.Now().Add(15 * time.Second)
	for {
		n, err := st.CountUnembeddedChunks(ctx)
		if err != nil {
			t.Fatalf("CountUnembeddedChunks: %v", err)
		}
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("embed backlog never drained to 0")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done

	// Recall now finds the imported content for the remapped agent.
	hits, err := st.SearchChunksByAgent(ctx, res.CampaignID, bartID, qvec[0], 5)
	if err != nil {
		t.Fatalf("SearchChunksByAgent (post): %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("recall after embedding found nothing; imported history not searchable")
	}
	if hits[0].Chunk.Content != recallText {
		t.Errorf("nearest chunk = %q, want the imported content", hits[0].Chunk.Content)
	}
}

// TestImportRoundTripWithHistory is #292 TEST 6: a seeded campaign with two Voice
// Sessions (lines + a chunk) exported WITH history and imported into a fresh
// database reproduces both sessions, their lines replay in seq order, and the
// chunk's participated ref lands on the remapped destination agent.
func TestImportRoundTripWithHistory(t *testing.T) {
	ctx := context.Background()
	src, srcCID, srcBartID := seededCampaign(t)

	base := time.Now().UTC().Truncate(time.Millisecond)
	// Session 1: two lines (inserted out of order) + one chunk participated by Bart.
	vs1, err := src.CreateVoiceSession(ctx, srcCID)
	if err != nil {
		t.Fatalf("CreateVoiceSession 1: %v", err)
	}
	if err := src.UpsertTranscriptLine(ctx, storage.TranscriptLine{
		VoiceSessionID: vs1.ID, CampaignID: srcCID, LineID: "l2", Seq: 2,
		Who: "Bart", Kind: "agent", TS: base.Add(time.Second), Text: "second",
	}); err != nil {
		t.Fatalf("Upsert l2: %v", err)
	}
	if err := src.UpsertTranscriptLine(ctx, storage.TranscriptLine{
		VoiceSessionID: vs1.ID, CampaignID: srcCID, LineID: "l1", Seq: 1,
		Who: "Frodo", Kind: "human", TS: base, Text: "first", SpeakerDiscordUserID: "123456789",
	}); err != nil {
		t.Fatalf("Upsert l1: %v", err)
	}
	if _, err := src.InsertTranscriptChunk(ctx, storage.TranscriptChunk{
		CampaignID: srcCID, VoiceSessionID: vs1.ID, Content: "first\nsecond",
		SpeakerDiscordUserIDs: []string{"123456789"},
		ParticipatedAgentIDs:  []uuid.UUID{srcBartID},
		StartedAt:             base,
	}); err != nil {
		t.Fatalf("InsertTranscriptChunk: %v", err)
	}
	if _, err := src.EndVoiceSession(ctx, vs1.ID, 2); err != nil {
		t.Fatalf("EndVoiceSession 1: %v", err)
	}
	// Session 2: one line.
	vs2, err := src.CreateVoiceSession(ctx, srcCID)
	if err != nil {
		t.Fatalf("CreateVoiceSession 2: %v", err)
	}
	if err := src.UpsertTranscriptLine(ctx, storage.TranscriptLine{
		VoiceSessionID: vs2.ID, CampaignID: srcCID, LineID: "m1", Seq: 1,
		Who: "Bart", Kind: "agent", TS: base.Add(time.Hour), Text: "later",
	}); err != nil {
		t.Fatalf("Upsert m1: %v", err)
	}
	if _, err := src.EndVoiceSession(ctx, vs2.ID, 1); err != nil {
		t.Fatalf("EndVoiceSession 2: %v", err)
	}

	b, err := bundle.Export(ctx, src, srcCID, bundle.ExportOptions{IncludeHistory: true})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	dst, tid := freshTenant(t)
	res, err := bundle.Import(ctx, dst, tid, b)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Sessions != 2 {
		t.Errorf("Sessions = %d, want 2", res.Sessions)
	}
	if res.Lines != 3 {
		t.Errorf("Lines = %d, want 3", res.Lines)
	}
	if res.Chunks != 1 {
		t.Errorf("Chunks = %d, want 1", res.Chunks)
	}

	// Locate the destination Bart to assert chunk remap.
	var dstBartID uuid.UUID
	dstAgents, err := dst.ListAgents(ctx, res.CampaignID)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	for _, a := range dstAgents {
		if a.Name == "Bart" {
			dstBartID = a.ID
		}
	}

	sessions, err := dst.ListVoiceSessions(ctx, res.CampaignID, 100)
	if err != nil {
		t.Fatalf("ListVoiceSessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("imported sessions = %d, want 2", len(sessions))
	}
	// Find the two-line session and assert replay order.
	var twoLine uuid.UUID
	for _, s := range sessions {
		lines, err := dst.ListTranscriptLines(ctx, s.ID)
		if err != nil {
			t.Fatalf("ListTranscriptLines: %v", err)
		}
		if len(lines) == 2 {
			twoLine = s.ID
			if lines[0].LineID != "l1" || lines[1].LineID != "l2" {
				t.Errorf("replay order = %v, want [l1 l2]", []string{lines[0].LineID, lines[1].LineID})
			}
		}
	}
	if twoLine == uuid.Nil {
		t.Fatal("imported history lost the two-line session")
	}

	chunks, err := dst.ListTranscriptChunks(ctx, res.CampaignID, false)
	if err != nil {
		t.Fatalf("ListTranscriptChunks: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("imported chunks = %d, want 1", len(chunks))
	}
	if got := chunks[0].ParticipatedAgentIDs; len(got) != 1 || got[0] != dstBartID {
		t.Errorf("chunk participated = %v, want remapped dst Bart %s", got, dstBartID)
	}
}

// TestImportHistoryRollsBackWithPart1 is #292 TEST 8: a part-1 failure (an edge
// referencing an unknown node) in a bundle that ALSO carries history rolls back
// the whole import — the history rows never persist either (one transaction,
// ADR-0049). No campaign and no orphaned voice session survive.
func TestImportHistoryRollsBackWithPart1(t *testing.T) {
	ctx := context.Background()
	st, tid := freshTenant(t)

	base := time.Date(2026, 6, 6, 20, 0, 0, 0, time.UTC)
	ended := base.Add(time.Hour)
	bnd := &bundle.Bundle{
		FormatVersion: bundle.FormatVersion,
		Campaign: bundle.Campaign{
			Name: "Doomed History", System: "dnd5e", Language: "en",
			Nodes: []bundle.Node{{ID: "n1", Type: "location", Name: "Somewhere"}},
			Edges: []bundle.Edge{{From: "n1", To: "ghost", Type: "resides_in"}},
			History: &bundle.History{Sessions: []bundle.Session{{
				ID: "s1", StartedAt: base, EndedAt: &ended, Status: "ended", LineCount: 1,
				Lines: []bundle.Line{{LineID: "l1", Seq: 1, Who: "Frodo", Kind: "human", TS: base, Text: "hi"}},
			}}},
		},
	}
	if _, err := bundle.Import(ctx, st, tid, bnd); err == nil {
		t.Fatal("Import with bad edge succeeded; want error")
	}
	if _, err := st.FindCampaignByName(ctx, tid, "Doomed History"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("rolled-back import left a campaign: %v", err)
	}
}

// TestImportWarnsOnDroppedParticipantRefs (#381): when a chunk carries a
// participant ref that maps to no imported Agent, the importer emits a single
// slog.Warn on the context logger (ADR-0032 request-scoped) carrying campaign_id
// and the dropped count — so a lossy import is visible in the logs, not silent.
func TestImportWarnsOnDroppedParticipantRefs(t *testing.T) {
	ctx := context.Background()
	st, tid := freshTenant(t)

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	ctx = observe.WithLogger(ctx, log)

	base := time.Date(2026, 6, 8, 20, 0, 0, 0, time.UTC)
	ended := base.Add(time.Hour)
	bnd := &bundle.Bundle{
		FormatVersion: bundle.FormatVersion,
		Campaign: bundle.Campaign{
			Name: "Warns", System: "dnd5e", Language: "en",
			Agents: []bundle.Agent{{ID: "a1", Role: "character", Name: "Bart"}},
			History: &bundle.History{Sessions: []bundle.Session{{
				ID: "s1", StartedAt: base, EndedAt: &ended, Status: "ended", LineCount: 0,
				Chunks: []bundle.Chunk{{
					Content:              "the dragon spoke of gold",
					ParticipatedAgentIDs: []string{"a1", "ghost", "phantom"},
					StartedAt:            base,
				}},
			}}},
		},
	}
	res, err := bundle.Import(ctx, st, tid, bnd)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.DroppedParticipantRefs != 2 {
		t.Fatalf("DroppedParticipantRefs = %d, want 2", res.DroppedParticipantRefs)
	}
	logged := buf.String()
	if !strings.Contains(logged, "level=WARN") {
		t.Errorf("no WARN emitted for dropped refs: %q", logged)
	}
	if !strings.Contains(logged, res.CampaignID.String()) {
		t.Errorf("warn missing campaign_id %s: %q", res.CampaignID, logged)
	}
	if !strings.Contains(logged, "count=2") {
		t.Errorf("warn missing count=2: %q", logged)
	}
}

// TestImportSilentWhenNoDroppedRefs (#381): a clean import (every participant ref
// maps) emits NO dropped-refs warning — the log line is gated on count > 0.
func TestImportSilentWhenNoDroppedRefs(t *testing.T) {
	ctx := context.Background()
	st, tid := freshTenant(t)

	var buf bytes.Buffer
	ctx = observe.WithLogger(ctx, slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))

	base := time.Date(2026, 6, 9, 20, 0, 0, 0, time.UTC)
	ended := base.Add(time.Hour)
	bnd := &bundle.Bundle{
		FormatVersion: bundle.FormatVersion,
		Campaign: bundle.Campaign{
			Name: "Clean", System: "dnd5e", Language: "en",
			Agents: []bundle.Agent{{ID: "a1", Role: "character", Name: "Bart"}},
			History: &bundle.History{Sessions: []bundle.Session{{
				ID: "s1", StartedAt: base, EndedAt: &ended, Status: "ended", LineCount: 0,
				Chunks: []bundle.Chunk{{
					Content:              "the dragon spoke of gold",
					ParticipatedAgentIDs: []string{"a1"},
					StartedAt:            base,
				}},
			}}},
		},
	}
	if _, err := bundle.Import(ctx, st, tid, bnd); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("clean import emitted a warning: %q", buf.String())
	}
}

// TestImportHistorylessBundleUnchanged is #292 TEST 7 (regression): a bundle with
// NO History section imports its part-1 domain grains and reports zero history
// counts — identical to part 1.
func TestImportHistorylessBundleUnchanged(t *testing.T) {
	ctx := context.Background()
	st, tid := freshTenant(t)

	bnd := &bundle.Bundle{
		FormatVersion: bundle.FormatVersion,
		Campaign:      bundle.Campaign{Name: "No History", System: "dnd5e", Language: "en"},
	}
	res, err := bundle.Import(ctx, st, tid, bnd)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Sessions != 0 || res.Lines != 0 || res.Chunks != 0 || res.DroppedParticipantRefs != 0 {
		t.Errorf("history-less bundle counted history: %+v", res)
	}
	sessions, err := st.ListVoiceSessions(ctx, res.CampaignID, 100)
	if err != nil {
		t.Fatalf("ListVoiceSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("history-less bundle wrote %d voice sessions, want 0", len(sessions))
	}
}
