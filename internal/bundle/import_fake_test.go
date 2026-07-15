package bundle_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/bundle"
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// This file is the DB-free half of the import suite (#451): the same
// remap/merge/counting logic the integration tests prove against Postgres,
// exercised here through [bundle.TxRunner] over the in-memory fakeStore under
// plain `go test`. What stays integration-only is exactly the SQL-shaped rest:
// real rollback of a mid-bundle failure, FK/CHECK interactions, and the
// genuine trigger.

// seamVoiceJSON returns a canonical voice column value for the given provider
// and voice id, via the same mapper the importer validates with.
func seamVoiceJSON(t *testing.T, providerID, voiceID string) json.RawMessage {
	t.Helper()
	raw, err := storage.VoiceToJSON(tts.Voice{ProviderID: providerID, VoiceID: voiceID})
	if err != nil {
		t.Fatalf("VoiceToJSON: %v", err)
	}
	return raw
}

// remapBundle builds the reference bundle for the remap tests: a Butler, two
// character NPCs, an agent-linked NPC node plus a plain location node, one
// edge, one player Character, and a history session whose lines arrive out of
// seq order and whose chunk names both NPC refs.
func remapBundle(t *testing.T) *bundle.Bundle {
	t.Helper()
	started := time.Date(2026, 5, 1, 19, 0, 0, 0, time.UTC)
	ended := started.Add(3 * time.Hour)
	return &bundle.Bundle{
		FormatVersion: bundle.FormatVersion,
		ExportedAt:    started,
		Campaign: bundle.Campaign{
			Name: "Shadows over Innsmouth", System: "coc7e", Language: "de",
			Agents: []bundle.Agent{
				{
					ID: "butler-ref", Role: "butler", Name: "Majordomus", Title: "Haushofmeister",
					Persona: "stiff upper lip", AddressOnly: false, Aliases: []string{"Dom"},
					Grants: []bundle.Grant{{ToolName: "rules_lookup", Config: json.RawMessage(`{"system":"coc7e"}`)}},
				},
				{
					ID: "gesa-ref", Role: "character", Name: "Gesa",
					Voice:  seamVoiceJSON(t, "elevenlabs", "gesa-voice"),
					Grants: []bundle.Grant{{ToolName: "dice"}},
				},
				{ID: "bart-ref", Role: "character", Name: "Bart", Voice: seamVoiceJSON(t, "openai", "onyx")},
			},
			Nodes: []bundle.Node{
				{ID: "n-gesa", Type: "npc", Name: "Gesa", Body: "innkeeper", AgentID: "gesa-ref"},
				{ID: "n-inn", Type: "location", Name: "The Gilman House", GMPrivate: true},
			},
			Edges: []bundle.Edge{{From: "n-gesa", To: "n-inn", Type: "resides_in"}},
			Characters: []bundle.Character{
				{Name: "Frodo", Aliases: []string{"Ringbearer"}, DiscordUserID: "199999999999999999"},
			},
			History: &bundle.History{Sessions: []bundle.Session{{
				ID: "s1", StartedAt: started, EndedAt: &ended, Status: "ended", LineCount: 2,
				Lines: []bundle.Line{
					{LineID: "l2", Seq: 7, Who: "Gesa", Kind: "agent", TS: started.Add(time.Minute), Text: "Willkommen."},
					{LineID: "l1", Seq: 3, Who: "Frodo", Kind: "human", TS: started, Text: "Hallo?", SpeakerDiscordUserID: "199999999999999999"},
				},
				Chunks: []bundle.Chunk{{
					Content:               "Frodo: Hallo?\nGesa: Willkommen.",
					SpeakerDiscordUserIDs: []string{"199999999999999999"},
					ParticipatedAgentIDs:  []string{"gesa-ref", "bart-ref"},
					StartedAt:             started,
				}},
			}}},
		},
	}
}

// agentNamed / nodeNamed find a fake row by name within one campaign.
func agentNamed(t *testing.T, f *fakeStore, campaignID uuid.UUID, name string) storage.Agent {
	t.Helper()
	for _, a := range f.agents {
		if a.CampaignID == campaignID && a.Name == name {
			return a
		}
	}
	t.Fatalf("no agent %q in campaign %s", name, campaignID)
	return storage.Agent{}
}

func nodeNamed(t *testing.T, f *fakeStore, campaignID uuid.UUID, name string) storage.KGNode {
	t.Helper()
	for _, n := range f.nodes {
		if n.CampaignID == campaignID && n.Name == name {
			return n
		}
	}
	t.Fatalf("no node %q in campaign %s", name, campaignID)
	return storage.KGNode{}
}

// grantNames returns the tool names of an agent's grants in the seam's
// tool-name order.
func grantNames(t *testing.T, f *fakeStore, agentID uuid.UUID) []string {
	t.Helper()
	grants, err := f.ListToolGrants(context.Background(), agentID)
	if err != nil {
		t.Fatalf("ListToolGrants: %v", err)
	}
	names := make([]string, 0, len(grants))
	for _, g := range grants {
		names = append(names, g.ToolName)
	}
	return names
}

// TestImportFake_MintsAndRemapsEveryReference is the heart of ADR-0053 §4 over
// the fake: every entity lands under a minted UUID, and every intra-bundle
// reference — the node→agent link, both edge endpoints, and the chunk's
// participant refs — is remapped consistently onto those minted ids.
func TestImportFake_MintsAndRemapsEveryReference(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFakeStore()
	tenantID := uuid.New()

	res, err := bundle.Import(ctx, f, tenantID, remapBundle(t))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	// Counts feed the ServeImport response verbatim — all sections landed.
	if res.Agents != 3 || res.Nodes != 2 || res.Edges != 1 || res.Characters != 1 ||
		res.Sessions != 1 || res.Lines != 2 || res.Chunks != 1 || res.DroppedParticipantRefs != 0 {
		t.Fatalf("counts = %+v, want 3/2/1/1 agents/nodes/edges/characters, 1/2/1 sessions/lines/chunks, 0 dropped", res)
	}

	campaign, err := f.GetCampaign(ctx, res.CampaignID)
	if err != nil {
		t.Fatalf("campaign did not land: %v", err)
	}
	if campaign.TenantID != tenantID || campaign.Name != "Shadows over Innsmouth" ||
		campaign.System != "coc7e" || campaign.Language != "de" {
		t.Errorf("campaign = %+v, want tenant %s + bundle name/system/language", campaign, tenantID)
	}

	// Every bundle ref got a fresh, distinct minted id.
	for _, ref := range []string{"butler-ref", "gesa-ref", "bart-ref"} {
		if res.AgentIDs[ref] == uuid.Nil {
			t.Errorf("AgentIDs[%q] not minted", ref)
		}
	}
	seen := make(map[uuid.UUID]string, len(res.AgentIDs))
	for ref, id := range res.AgentIDs {
		if other, dup := seen[id]; dup {
			t.Errorf("minted agent ids collide: %q and %q both got %s", ref, other, id)
		}
		seen[id] = ref
	}

	// Node→agent link remapped onto the minted Gesa id; the plain node stays
	// unlinked.
	gesaNode := nodeNamed(t, f, res.CampaignID, "Gesa")
	if !gesaNode.AgentID.Valid || gesaNode.AgentID.UUID != res.AgentIDs["gesa-ref"] {
		t.Errorf("node agent link = %+v, want minted id %s", gesaNode.AgentID, res.AgentIDs["gesa-ref"])
	}
	if inn := nodeNamed(t, f, res.CampaignID, "The Gilman House"); inn.AgentID.Valid {
		t.Errorf("location node unexpectedly agent-linked: %+v", inn.AgentID)
	}

	// Edge endpoints remapped onto the minted node ids.
	if len(f.edges) != 1 {
		t.Fatalf("edges = %d, want 1", len(f.edges))
	}
	edge := f.edges[0]
	innNode := nodeNamed(t, f, res.CampaignID, "The Gilman House")
	if edge.FromNodeID != gesaNode.ID || edge.ToNodeID != innNode.ID {
		t.Errorf("edge = %s->%s, want %s->%s", edge.FromNodeID, edge.ToNodeID, gesaNode.ID, innNode.ID)
	}

	// Character travels verbatim (ADR-0053 §6: snowflake kept).
	if len(f.characters) != 1 || f.characters[0].DiscordUserID != "199999999999999999" {
		t.Errorf("characters = %+v, want Frodo's snowflake verbatim", f.characters)
	}

	// History: session minted, lines listed in seq order with replay keys
	// verbatim, chunk participants remapped in bundle order.
	if len(f.sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(f.sessions))
	}
	lines, err := f.ListTranscriptLines(ctx, f.sessions[0].ID)
	if err != nil {
		t.Fatalf("ListTranscriptLines: %v", err)
	}
	if len(lines) != 2 || lines[0].LineID != "l1" || lines[0].Seq != 3 ||
		lines[1].LineID != "l2" || lines[1].Seq != 7 {
		t.Errorf("lines out of replay order or keys rewritten: %+v", lines)
	}
	if lines[0].SpeakerDiscordUserID != "199999999999999999" {
		t.Errorf("speaker snowflake = %q, want verbatim", lines[0].SpeakerDiscordUserID)
	}
	if len(f.chunks) != 1 {
		t.Fatalf("chunks = %d, want 1", len(f.chunks))
	}
	wantParticipants := []uuid.UUID{res.AgentIDs["gesa-ref"], res.AgentIDs["bart-ref"]}
	if got := f.chunks[0].ParticipatedAgentIDs; len(got) != 2 ||
		got[0] != wantParticipants[0] || got[1] != wantParticipants[1] {
		t.Errorf("chunk participants = %v, want remapped %v", got, wantParticipants)
	}
}

// TestImportFake_TwiceMintsIndependentCampaigns: ADR-0053 §4 — the importer
// never dedups; the same bundle imported twice yields two fully independent
// Campaigns with disjoint minted ids.
func TestImportFake_TwiceMintsIndependentCampaigns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFakeStore()
	tenantID := uuid.New()

	first, err := bundle.Import(ctx, f, tenantID, remapBundle(t))
	if err != nil {
		t.Fatalf("first Import: %v", err)
	}
	second, err := bundle.Import(ctx, f, tenantID, remapBundle(t))
	if err != nil {
		t.Fatalf("second Import: %v", err)
	}

	if first.CampaignID == second.CampaignID {
		t.Fatalf("both imports landed in campaign %s", first.CampaignID)
	}
	for ref, id := range first.AgentIDs {
		if second.AgentIDs[ref] == id {
			t.Errorf("agent ref %q reused id %s across imports", ref, id)
		}
	}
	if len(f.campaigns) != 2 || len(f.agents) != 6 || len(f.nodes) != 4 ||
		len(f.characters) != 2 || len(f.sessions) != 2 {
		t.Errorf("fake holds %d/%d/%d/%d/%d campaigns/agents/nodes/characters/sessions, want 2/6/4/2/2",
			len(f.campaigns), len(f.agents), len(f.nodes), len(f.characters), len(f.sessions))
	}
}

// TestImportFake_RejectsDuplicateRefs: a repeated agent or node ref key would
// clobber the remap and bind references to the wrong row, so it is a hard
// error naming the offending ref.
func TestImportFake_RejectsDuplicateRefs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("agent", func(t *testing.T) {
		t.Parallel()
		b := remapBundle(t)
		b.Campaign.Agents = append(b.Campaign.Agents, bundle.Agent{ID: "gesa-ref", Role: "character", Name: "Impostor"})
		_, err := bundle.Import(ctx, newFakeStore(), uuid.New(), b)
		if err == nil || !strings.Contains(err.Error(), `duplicate agent ref "gesa-ref"`) {
			t.Fatalf("err = %v, want duplicate agent ref", err)
		}
	})
	t.Run("node", func(t *testing.T) {
		t.Parallel()
		b := remapBundle(t)
		b.Campaign.Nodes = append(b.Campaign.Nodes, bundle.Node{ID: "n-inn", Type: "location", Name: "Shadow Inn"})
		_, err := bundle.Import(ctx, newFakeStore(), uuid.New(), b)
		if err == nil || !strings.Contains(err.Error(), `duplicate node ref "n-inn"`) {
			t.Fatalf("err = %v, want duplicate node ref", err)
		}
	})
}

// TestImportFake_RejectsUnknownRefs: any cross-reference that resolves to no
// bundle entity — a node's agent link or either edge endpoint — aborts the
// import (all-or-nothing; only chunk participants are drop-not-fatal).
func TestImportFake_RejectsUnknownRefs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("node agent link", func(t *testing.T) {
		t.Parallel()
		b := remapBundle(t)
		b.Campaign.Nodes[0].AgentID = "nobody"
		_, err := bundle.Import(ctx, newFakeStore(), uuid.New(), b)
		if err == nil || !strings.Contains(err.Error(), `unknown agent "nobody"`) {
			t.Fatalf("err = %v, want unknown agent ref", err)
		}
	})
	t.Run("edge from", func(t *testing.T) {
		t.Parallel()
		b := remapBundle(t)
		b.Campaign.Edges[0].From = "n-ghost"
		_, err := bundle.Import(ctx, newFakeStore(), uuid.New(), b)
		if err == nil || !strings.Contains(err.Error(), `unknown from-node "n-ghost"`) {
			t.Fatalf("err = %v, want unknown from-node", err)
		}
	})
	t.Run("edge to", func(t *testing.T) {
		t.Parallel()
		b := remapBundle(t)
		b.Campaign.Edges[0].To = "n-ghost"
		_, err := bundle.Import(ctx, newFakeStore(), uuid.New(), b)
		if err == nil || !strings.Contains(err.Error(), `unknown to-node "n-ghost"`) {
			t.Fatalf("err = %v, want unknown to-node", err)
		}
	})
}

// TestImportFake_RejectsSecondButler: exactly one Butler per Campaign
// (ADR-0009) — a second one in the bundle is refused, never last-wins merged.
func TestImportFake_RejectsSecondButler(t *testing.T) {
	t.Parallel()
	b := remapBundle(t)
	b.Campaign.Agents = append(b.Campaign.Agents, bundle.Agent{ID: "butler2", Role: "butler", Name: "Zweiter"})
	_, err := bundle.Import(context.Background(), newFakeStore(), uuid.New(), b)
	if err == nil || !strings.Contains(err.Error(), "more than one butler") {
		t.Fatalf("err = %v, want more-than-one-butler refusal", err)
	}
}

// TestImportFake_ButlerMergesOntoTriggerRow: the ADR-0009 merge — the bundle's
// Butler UPDATEs the trigger-created row (same id, new editor fields), its
// grants replace the trigger defaults EXACTLY, and provider FKs land NULL
// (ADR-0053 §2). The address_only/role checks pin the SEAM contract the merge
// rides on — UpdateAgent force-keeps a Butler's address_only true and never
// changes agent_role — which the fake emulates here and storage proves for
// real (TestUpdateButlerKeepsAddressOnly; the bundle deliberately says false).
func TestImportFake_ButlerMergesOntoTriggerRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFakeStore()

	res, err := bundle.Import(ctx, f, uuid.New(), remapBundle(t))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	butler, err := f.GetButler(ctx, res.CampaignID)
	if err != nil {
		t.Fatalf("GetButler: %v", err)
	}
	if butler.ID != res.AgentIDs["butler-ref"] {
		t.Errorf("AgentIDs[butler-ref] = %s, want the trigger row %s", res.AgentIDs["butler-ref"], butler.ID)
	}
	if butler.Name != "Majordomus" || butler.Title != "Haushofmeister" || butler.Persona != "stiff upper lip" {
		t.Errorf("butler fields not merged: %+v", butler)
	}
	if len(butler.Aliases) != 1 || butler.Aliases[0] != "Dom" {
		t.Errorf("butler aliases = %v, want [Dom]", butler.Aliases)
	}
	if !butler.AddressOnly {
		t.Error("butler address_only unpinned — the UpdateAgent seam contract (ADR-0024) must keep it true against the bundle's false")
	}
	if butler.Role != storage.AgentRoleButler {
		t.Errorf("butler role changed to %q — the UpdateAgent seam contract never changes agent_role", butler.Role)
	}
	if butler.VoiceProviderConfigID.Valid || butler.LLMProviderConfigID.Valid {
		t.Errorf("butler provider FKs not NULL: %+v / %+v", butler.VoiceProviderConfigID, butler.LLMProviderConfigID)
	}

	// Grants replaced exactly: every trigger default gone, the bundle's one
	// grant present with its scope Config verbatim.
	if names := grantNames(t, f, butler.ID); len(names) != 1 || names[0] != "rules_lookup" {
		t.Fatalf("butler grants = %v, want exactly [rules_lookup]", names)
	}
	grants, _ := f.ListToolGrants(ctx, butler.ID)
	if string(grants[0].Config) != `{"system":"coc7e"}` {
		t.Errorf("grant config = %s, want verbatim scope blob", grants[0].Config)
	}
}

// TestImportFake_NoButlerKeepsTriggerDefaults: a bundle without a Butler
// leaves the trigger-created row and its default grant set standing.
func TestImportFake_NoButlerKeepsTriggerDefaults(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFakeStore()

	b := remapBundle(t)
	b.Campaign.Agents = b.Campaign.Agents[1:] // drop the Butler, keep the NPCs
	b.Campaign.History = nil                  // chunk participants reference NPCs only anyway
	res, err := bundle.Import(ctx, f, uuid.New(), b)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Agents != 2 {
		t.Errorf("Agents = %d, want 2 (the Butler is not counted when not in the bundle)", res.Agents)
	}

	butler, err := f.GetButler(ctx, res.CampaignID)
	if err != nil {
		t.Fatalf("GetButler: %v", err)
	}
	if butler.Name != "Glyphoxa" || !butler.AddressOnly {
		t.Errorf("trigger defaults disturbed: %+v", butler)
	}
	want := []string{"dice", "kg_query", "recap", "transcript_search"} // tool-name order
	got := grantNames(t, f, butler.ID)
	if len(got) != len(want) {
		t.Fatalf("butler grants = %v, want defaults %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("butler grants = %v, want defaults %v", got, want)
		}
	}
}

// TestImportFake_CharacterAgentsLandClean: character NPCs are created with
// provider FKs NULL (the exporter stripped bindings; the tenant-level fallback
// resolves providers later) and their grants carried verbatim.
func TestImportFake_CharacterAgentsLandClean(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFakeStore()

	res, err := bundle.Import(ctx, f, uuid.New(), remapBundle(t))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	gesa := agentNamed(t, f, res.CampaignID, "Gesa")
	if gesa.VoiceProviderConfigID.Valid || gesa.LLMProviderConfigID.Valid {
		t.Errorf("NPC provider FKs not NULL: %+v / %+v", gesa.VoiceProviderConfigID, gesa.LLMProviderConfigID)
	}
	if names := grantNames(t, f, gesa.ID); len(names) != 1 || names[0] != "dice" {
		t.Errorf("Gesa grants = %v, want [dice]", names)
	}
	voice, err := storage.VoiceFromJSON(gesa.Voice)
	if err != nil || voice.ProviderID != "elevenlabs" || voice.VoiceID != "gesa-voice" {
		t.Errorf("Gesa voice = %+v (err %v), want elevenlabs/gesa-voice", voice, err)
	}
}

// TestImportFake_InvalidVoiceFails: a voice that does not parse through the
// canonical mapper is a hard error naming the agent — never a silent NPC
// (#224) — for the Butler and character paths alike.
func TestImportFake_InvalidVoiceFails(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("butler", func(t *testing.T) {
		t.Parallel()
		b := remapBundle(t)
		b.Campaign.Agents[0].Voice = json.RawMessage(`"not an object"`)
		if _, err := bundle.Import(ctx, newFakeStore(), uuid.New(), b); err == nil ||
			!strings.Contains(err.Error(), "butler voice") {
			t.Fatalf("err = %v, want butler voice failure", err)
		}
	})
	t.Run("character", func(t *testing.T) {
		t.Parallel()
		b := remapBundle(t)
		b.Campaign.Agents[1].Voice = json.RawMessage(`"not an object"`)
		if _, err := bundle.Import(ctx, newFakeStore(), uuid.New(), b); err == nil ||
			!strings.Contains(err.Error(), `agent "Gesa" voice`) {
			t.Fatalf("err = %v, want Gesa voice failure", err)
		}
	})
}

// TestImportFake_DropsUnmappableParticipants: a chunk participant ref that
// maps to no imported Agent is dropped and counted — not fatal — while
// mappable refs in the same chunk still remap.
func TestImportFake_DropsUnmappableParticipants(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFakeStore()

	b := remapBundle(t)
	b.Campaign.History.Sessions[0].Chunks[0].ParticipatedAgentIDs = []string{"ghost", "gesa-ref", "phantom"}
	res, err := bundle.Import(ctx, f, uuid.New(), b)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.DroppedParticipantRefs != 2 {
		t.Errorf("DroppedParticipantRefs = %d, want 2", res.DroppedParticipantRefs)
	}
	if res.Chunks != 1 {
		t.Errorf("Chunks = %d, want 1 — a lossy chunk still lands", res.Chunks)
	}
	got := f.chunks[0].ParticipatedAgentIDs
	if len(got) != 1 || got[0] != res.AgentIDs["gesa-ref"] {
		t.Errorf("chunk participants = %v, want just the remapped gesa-ref", got)
	}
}

// TestImportFake_WarnsOnDroppedRefs is the #381 log contract, DB-free: one
// WARN on the request-scoped logger carrying campaign_id and count, emitted
// only after the import succeeded; a clean import stays silent.
func TestImportFake_WarnsOnDroppedRefs(t *testing.T) {
	t.Parallel()

	t.Run("lossy import warns once", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		ctx := observe.WithLogger(context.Background(),
			slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))

		b := remapBundle(t)
		b.Campaign.History.Sessions[0].Chunks[0].ParticipatedAgentIDs = []string{"ghost", "phantom"}
		res, err := bundle.Import(ctx, newFakeStore(), uuid.New(), b)
		if err != nil {
			t.Fatalf("Import: %v", err)
		}
		logged := buf.String()
		if !strings.Contains(logged, "level=WARN") ||
			!strings.Contains(logged, res.CampaignID.String()) ||
			!strings.Contains(logged, "count=2") {
			t.Errorf("warn missing/incomplete: %q", logged)
		}
	})
	t.Run("clean import is silent", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		ctx := observe.WithLogger(context.Background(),
			slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))

		if _, err := bundle.Import(ctx, newFakeStore(), uuid.New(), remapBundle(t)); err != nil {
			t.Fatalf("Import: %v", err)
		}
		if buf.Len() != 0 {
			t.Errorf("clean import emitted: %q", buf.String())
		}
	})
	t.Run("failed import is silent even with drops counted", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		ctx := observe.WithLogger(context.Background(),
			slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))

		// Drops are counted during the tx body; the commit then fails, so a
		// warn here would log a campaign_id that never persisted.
		b := remapBundle(t)
		b.Campaign.History.Sessions[0].Chunks[0].ParticipatedAgentIDs = []string{"ghost"}
		if _, err := bundle.Import(ctx, commitFailTx{newFakeStore()}, uuid.New(), b); err == nil {
			t.Fatal("Import succeeded through a failing commit")
		}
		if buf.Len() != 0 {
			t.Errorf("rolled-back import emitted: %q", buf.String())
		}
	})
}

// TestImportFake_CoercesSessionStatus: an imported Voice Session is never
// revivable — 'failed' keeps its distinct terminal state, anything else
// (including a stale 'running') lands 'ended', and a missing ended_at defaults
// to started_at so no imported row ever looks live.
func TestImportFake_CoercesSessionStatus(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	started := time.Date(2026, 5, 2, 19, 0, 0, 0, time.UTC)

	cases := []struct {
		name   string
		status string
		want   storage.VoiceSessionStatus
	}{
		{"running coerces to ended", "running", storage.VoiceSessionEnded},
		{"failed stays failed", "failed", storage.VoiceSessionFailed},
		{"ended stays ended", "ended", storage.VoiceSessionEnded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFakeStore()
			b := remapBundle(t)
			b.Campaign.History = &bundle.History{Sessions: []bundle.Session{{
				ID: "s1", StartedAt: started, Status: tc.status, // no EndedAt on purpose
			}}}
			if _, err := bundle.Import(ctx, f, uuid.New(), b); err != nil {
				t.Fatalf("Import: %v", err)
			}
			s := f.sessions[0]
			if s.Status != tc.want {
				t.Errorf("status = %q, want %q", s.Status, tc.want)
			}
			if s.EndedAt == nil || !s.EndedAt.Equal(started) {
				t.Errorf("ended_at = %v, want defaulted to started_at %v", s.EndedAt, started)
			}
		})
	}
}

// TestImportFake_DuplicateLineIDCoalesces pins the documented edge from
// import.go: two bundle Lines sharing a line_id COALESCE at the replay-key
// upsert — one row survives with the FIRST insert's seq and the LAST write's
// text — while res.Lines still counts both inputs.
func TestImportFake_DuplicateLineIDCoalesces(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFakeStore()
	started := time.Date(2026, 5, 3, 19, 0, 0, 0, time.UTC)

	b := remapBundle(t)
	b.Campaign.History.Sessions[0].Chunks = nil
	b.Campaign.History.Sessions[0].Lines = []bundle.Line{
		{LineID: "dup", Seq: 1, Who: "Gesa", Kind: "agent", TS: started, Text: "first"},
		{LineID: "dup", Seq: 9, Who: "Bart", Kind: "agent", TS: started, Text: "second"},
	}
	res, err := bundle.Import(ctx, f, uuid.New(), b)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Lines != 2 {
		t.Errorf("Lines = %d, want 2 — the count reports inputs, not rows", res.Lines)
	}
	lines, _ := f.ListTranscriptLines(ctx, f.sessions[0].ID)
	if len(lines) != 1 {
		t.Fatalf("rows = %d, want 1 coalesced", len(lines))
	}
	if lines[0].Seq != 1 || lines[0].Text != "second" || lines[0].Who != "Bart" {
		t.Errorf("coalesced line = %+v, want seq 1 (first insert) with last write's text/who", lines[0])
	}
}

// TestImportFake_NodeMayLinkButlerRef: mergeButler records the Butler's ref in
// AgentIDs precisely so a node can link to it — the consumer path of that
// remap entry.
func TestImportFake_NodeMayLinkButlerRef(t *testing.T) {
	t.Parallel()
	f := newFakeStore()
	b := remapBundle(t)
	b.Campaign.Nodes = append(b.Campaign.Nodes,
		bundle.Node{ID: "n-butler", Type: "npc", Name: "Majordomus", AgentID: "butler-ref"})

	res, err := bundle.Import(context.Background(), f, uuid.New(), b)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	node := nodeNamed(t, f, res.CampaignID, "Majordomus")
	if !node.AgentID.Valid || node.AgentID.UUID != res.AgentIDs["butler-ref"] {
		t.Errorf("node agent link = %+v, want the merged butler %s", node.AgentID, res.AgentIDs["butler-ref"])
	}
}

// TestImportFake_DuplicateGrantFails: an Agent grants a Tool at most once
// (ADR-0029, a seam contract) — a bundle granting the same tool twice to one
// agent aborts the import.
func TestImportFake_DuplicateGrantFails(t *testing.T) {
	t.Parallel()
	b := remapBundle(t)
	b.Campaign.Agents[1].Grants = append(b.Campaign.Agents[1].Grants, bundle.Grant{ToolName: "dice"})

	_, err := bundle.Import(context.Background(), newFakeStore(), uuid.New(), b)
	if err == nil || !strings.Contains(err.Error(), `create grant "dice"`) {
		t.Fatalf("err = %v, want duplicate-grant refusal", err)
	}
}

// TestImportFake_RefusesNewerVersionBeforeAnyWrite: the compatibility gate
// (ADR-0053 §7) runs before the transaction — a newer bundle is refused with
// both versions named and the store is never touched.
func TestImportFake_RefusesNewerVersionBeforeAnyWrite(t *testing.T) {
	t.Parallel()
	f := newFakeStore()
	b := remapBundle(t)
	b.FormatVersion = bundle.FormatVersion + 1

	_, err := bundle.Import(context.Background(), f, uuid.New(), b)
	if !errors.Is(err, bundle.ErrNewerFormat) {
		t.Fatalf("err = %v, want ErrNewerFormat", err)
	}
	wantMsg := fmt.Sprintf("bundle has format_version %d; this build supports %d",
		bundle.FormatVersion+1, bundle.FormatVersion)
	if !strings.Contains(err.Error(), wantMsg) {
		t.Errorf("refusal does not name both versions: %v, want %q", err, wantMsg)
	}
	if len(f.campaigns) != 0 || len(f.agents) != 0 {
		t.Errorf("store touched before the version gate: %d campaigns, %d agents", len(f.campaigns), len(f.agents))
	}
}

// TestImportFake_HistorylessBundleZeroCounts: no History section means a
// part-1 import exactly — zero session/line/chunk counts, nothing dropped.
func TestImportFake_HistorylessBundleZeroCounts(t *testing.T) {
	t.Parallel()
	f := newFakeStore()
	b := remapBundle(t)
	b.Campaign.History = nil

	res, err := bundle.Import(context.Background(), f, uuid.New(), b)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Sessions != 0 || res.Lines != 0 || res.Chunks != 0 || res.DroppedParticipantRefs != 0 {
		t.Errorf("history counts = %d/%d/%d/%d, want all zero", res.Sessions, res.Lines, res.Chunks, res.DroppedParticipantRefs)
	}
	if len(f.sessions) != 0 || len(f.lines) != 0 || len(f.chunks) != 0 {
		t.Errorf("history rows landed from a history-less bundle")
	}
}
