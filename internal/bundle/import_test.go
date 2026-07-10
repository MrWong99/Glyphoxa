//go:build integration

package bundle_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/bundle"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

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
	var linked bool
	for _, n := range nodes {
		if n.Type == storage.KGNodeNPC {
			if !n.AgentID.Valid || n.AgentID.UUID != dstBart.ID {
				t.Errorf("npc node agent link = %v, want remapped Bart %v", n.AgentID, dstBart.ID)
			}
			linked = true
		}
	}
	if !linked {
		t.Error("no npc node with remapped agent link")
	}

	edges, err := dst.ListEdges(ctx, res.CampaignID)
	if err != nil {
		t.Fatalf("ListEdges: %v", err)
	}
	if len(edges) != 1 {
		t.Errorf("edge count = %d, want 1", len(edges))
	}

	chars, err := dst.ListCharacters(ctx, res.CampaignID)
	if err != nil {
		t.Fatalf("ListCharacters: %v", err)
	}
	if len(chars) != 1 || chars[0].DiscordUserID != "123456789" {
		t.Fatalf("characters = %+v, want Frodo with verbatim discord id", chars)
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

// TestImportToleratesHistorySection is TEST 9: a bundle carrying a History section
// imports its part-1 domain grains fine and writes no session/line/chunk rows.
func TestImportToleratesHistorySection(t *testing.T) {
	ctx := context.Background()
	st, tid := freshTenant(t)

	bnd := &bundle.Bundle{
		FormatVersion: bundle.FormatVersion,
		Campaign: bundle.Campaign{
			Name: "Has History", System: "dnd5e", Language: "en",
			History: &bundle.History{Sessions: []bundle.Session{{
				ID: "s1", Status: "ended", LineCount: 1,
				Lines: []bundle.Line{{LineID: "l1", Seq: 1, Who: "Frodo", Kind: "human", Text: "hi"}},
			}}},
		},
	}
	res, err := bundle.Import(ctx, st, tid, bnd)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Sessions != 0 || res.Lines != 0 || res.Chunks != 0 {
		t.Errorf("part-1 counted history: sessions=%d lines=%d chunks=%d, want 0",
			res.Sessions, res.Lines, res.Chunks)
	}
	sessions, err := st.ListVoiceSessions(ctx, res.CampaignID, 100)
	if err != nil {
		t.Fatalf("ListVoiceSessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("part-1 wrote %d voice sessions, want 0", len(sessions))
	}
}
