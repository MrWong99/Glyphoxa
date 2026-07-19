//go:build integration

// This drives the #133 edge-aware Hot Context round-trip against a real Postgres
// (testcontainers): storage.CreateNode + SetNodeAgent + CreateEdge → kgfacts over
// the real Store keyed by the Agent → an agent.Replier → the NPC's own Node and
// its edge-adjacent Nodes surface in the assembled system prompt; flipping
// gm_private removes a fact next turn; an unlinked Agent's prompt is byte-identical
// to the no-facts path; and two NPCs with different Edge neighbourhoods receive
// DIFFERENT fact sets (AC2, the epic's flagship, over the streaming reply path).
// Tag-isolated behind `integration` (ADR-0033).

package kgfacts_test

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/MrWong99/Glyphoxa/internal/kgfacts"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

const pgImage = "pgvector/pgvector:pg17"

// startPostgres spins up a pgvector container (or GLYPHOXA_TEST_DSN), skipping
// cleanly when Docker is unavailable — mirrors the storage harness.
func startPostgres(t *testing.T) string {
	t.Helper()
	if dsn := os.Getenv("GLYPHOXA_TEST_DSN"); dsn != "" {
		return dsn
	}
	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, pgImage,
		tcpostgres.WithDatabase("glyphoxa_test"),
		tcpostgres.WithUsername("glyphoxa"),
		tcpostgres.WithPassword("glyphoxa"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Skipf("SKIPPED DB TEST — NO POSTGRES: could not start %s (is Docker running?). "+
			"Set GLYPHOXA_TEST_DSN to run without Docker. err: %v", pgImage, err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(container) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	return dsn
}

func seedCampaign(t *testing.T, dsn string) (*pgxpool.Pool, uuid.UUID) {
	t.Helper()
	ctx := context.Background()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := storage.MigrateUp(ctx, db); err != nil {
		t.Fatalf("migrate up: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	var tenantID, campaignID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO tenant (name) VALUES ('Acme TTRPG') RETURNING id`).Scan(&tenantID); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaign (tenant_id, name, system, language)
		 VALUES ($1, 'Lost Mine', 'dnd5e', 'en') RETURNING id`, tenantID).Scan(&campaignID); err != nil {
		t.Fatalf("insert campaign: %v", err)
	}
	return pool, campaignID
}

// captureEngine is a minimal agent.Engine that records the assembled messages.
type captureEngine struct {
	reply    string
	captured []llm.Message
}

func (e *captureEngine) Generate(_ context.Context, msgs []llm.Message) (string, error) {
	e.captured = msgs
	return e.reply, nil
}

// nullSynth is a minimal tts.Synthesizer emitting no audio and no markup.
type nullSynth struct{}

func (nullSynth) Synthesize(context.Context, tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	ch := make(chan tts.AudioChunk)
	close(ch)
	return ch, nil
}
func (nullSynth) AudioMarkupPrompt(tts.Voice) string { return "" }

func routedTo(agentID, text string) voiceevent.AddressRouted {
	return voiceevent.AddressRouted{
		At:     time.Now(),
		Text:   text,
		Target: voiceevent.AddressTarget{AgentID: agentID, AgentRole: "character", Name: "NPC"},
	}
}

// captureStreamEngine is a minimal agent.StreamingEngine that records the assembled
// messages — the production reply path is streaming (ReplyStrategy.Stream), so the AC2
// divergence assertion exercises it rather than the batch fallback.
type captureStreamEngine struct {
	reply    string
	captured []llm.Message
}

func (e *captureStreamEngine) Generate(_ context.Context, msgs []llm.Message) (string, error) {
	e.captured = msgs
	return e.reply, nil
}

func (e *captureStreamEngine) GenerateStream(_ context.Context, msgs []llm.Message, onText func(string) error) (string, error) {
	e.captured = msgs
	if err := onText(e.reply); err != nil {
		return "", err
	}
	return e.reply, nil
}

// mkNode creates a Node with a body, failing the test on error.
func mkNode(t *testing.T, st *storage.Store, campID uuid.UUID, typ storage.KGNodeType, name, body string) storage.KGNode {
	t.Helper()
	n, err := st.CreateNode(context.Background(), storage.NewKGNode{
		CampaignID: campID, Type: typ, Name: name, Body: body,
	})
	if err != nil {
		t.Fatalf("CreateNode %s %q: %v", typ, name, err)
	}
	return n
}

// mkEdge creates a typed Edge between two same-Campaign Nodes, failing on error.
func mkEdge(t *testing.T, st *storage.Store, campID, from, to uuid.UUID, typ storage.KGEdgeType) {
	t.Helper()
	if _, err := st.CreateEdge(context.Background(), storage.NewKGEdge{
		CampaignID: campID, FromNodeID: from, ToNodeID: to, Type: typ,
	}); err != nil {
		t.Fatalf("CreateEdge %s: %v", typ, err)
	}
}

// linkAgent creates a Character Agent in the campaign and links it to the NPC Node,
// returning the Agent id (used as the Persona AgentID string).
func linkAgent(t *testing.T, st *storage.Store, campID, npcID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	agentID, err := st.CreateAgent(context.Background(), storage.NewAgent{
		CampaignID: campID, Role: storage.AgentRoleCharacter, Name: name,
	})
	if err != nil {
		t.Fatalf("CreateAgent %q: %v", name, err)
	}
	if _, err := st.SetNodeAgent(context.Background(), campID, npcID,
		uuid.NullUUID{UUID: agentID, Valid: true}); err != nil {
		t.Fatalf("SetNodeAgent %q: %v", name, err)
	}
	return agentID
}

// TestKGFacts_EndToEnd_Integration is #133's round-trip over a real Store: an NPC
// linked to its own Node with an edge-adjacent Location → the own Node's body AND
// the neighbour's body surface in the assembled prompt; flipping gm_private on the
// neighbour removes only that fact next turn (no cache); and an UNLINKED Agent's
// prompt is byte-identical to the no-facts path (the campaign-wide fallback is
// gone — an unlinked NPC injects nothing).
func TestKGFacts_EndToEnd_Integration(t *testing.T) {
	pool, campID := seedCampaign(t, startPostgres(t))
	// The turn ctx carries the session Identity the ctx-scoped Facts guard reads
	// (#487) — in production it descends from Manager.Start's run context.
	ctx := liveCtx(campID)
	st := storage.New(pool)

	rec := kgfacts.New(st, &fakeMetrics{}, nil, kgfacts.Config{})

	// runTurn drives one batch turn for the given Agent id and returns the assembled
	// system prompt.
	runTurn := func(agentID string, facts agent.FactsRecaller) string {
		eng := &captureEngine{reply: "Aye."}
		r := agent.NewReplier(agent.Config{
			Persona:     agent.Persona{AgentID: agentID, Markdown: "You are Bart.", Voice: tts.Voice{ProviderID: "elevenlabs", VoiceID: "v1"}},
			Engine:      eng,
			Synthesizer: nullSynth{},
			Facts:       facts,
		})
		r.Reply()(ctx, routedTo(agentID, "What do you know?"))
		if len(eng.captured) == 0 {
			t.Fatal("engine captured no messages")
		}
		return eng.captured[0].Text
	}

	// An unlinked Agent has an empty neighbourhood: its prompt must be byte-identical
	// with and without the recaller — the no-facts baseline.
	unlinked, err := st.CreateAgent(ctx, storage.NewAgent{
		CampaignID: campID, Role: storage.AgentRoleCharacter, Name: "Nobody",
	})
	if err != nil {
		t.Fatalf("CreateAgent unlinked: %v", err)
	}
	base := runTurn(unlinked.String(), nil)
	if empty := runTurn(unlinked.String(), rec); empty != base {
		t.Errorf("unlinked-agent prompt not byte-identical to the no-facts path:\n base=%q\n  rec=%q", base, empty)
	}

	// Link an NPC to its own Node, with a public Location neighbour.
	bart := mkNode(t, st, campID, storage.KGNodeNPC, "Bart the Innkeeper", "He keeps the peace.")
	bartAgent := linkAgent(t, st, campID, bart.ID, "Bart")
	inn := mkNode(t, st, campID, storage.KGNodeLocation, "The Prancing Pony", "It has never been robbed.")
	mkEdge(t, st, campID, bart.ID, inn.ID, storage.KGEdgeResidesIn)

	withFacts := runTurn(bartAgent.String(), rec)
	if !strings.Contains(withFacts, "He keeps the peace.") {
		t.Errorf("own Node body not injected into the prompt: %q", withFacts)
	}
	if !strings.Contains(withFacts, "It has never been robbed.") {
		t.Errorf("edge-adjacent Location body not injected into the prompt: %q", withFacts)
	}
	if !strings.Contains(withFacts, "## What you know about the world") {
		t.Errorf("facts block header missing: %q", withFacts)
	}
	if !strings.Contains(withFacts, "### Bart the Innkeeper (NPC)") {
		t.Errorf("own-node fact heading missing: %q", withFacts)
	}

	// Flip gm_private on the neighbour: it must vanish on the very next turn (no
	// cache), while the own Node's fact remains.
	if _, err := pool.Exec(ctx, `UPDATE kg_node SET gm_private = true WHERE id = $1`, inn.ID); err != nil {
		t.Fatalf("flip gm_private: %v", err)
	}
	hidden := runTurn(bartAgent.String(), rec)
	if strings.Contains(hidden, "It has never been robbed.") {
		t.Errorf("gm_private neighbour leaked into the prompt: %q", hidden)
	}
	if !strings.Contains(hidden, "He keeps the peace.") {
		t.Errorf("own Node fact wrongly dropped when only the neighbour went private: %q", hidden)
	}
}

// TestKGFacts_PerNPCDivergence_Integration is #133 AC2, the epic's flagship: two
// Character NPC Agents, each linked to its own NPC Node, each residing in a
// DIFFERENT Location, receive DIFFERENT fact sets in the SAME campaign — each
// prompt carries its OWN Location's body and NOT the other's. It drives the real
// streaming reply path (agent.Replier.ReplyStream) so the production wiring is
// what's under test.
func TestKGFacts_PerNPCDivergence_Integration(t *testing.T) {
	pool, campID := seedCampaign(t, startPostgres(t))
	ctx := liveCtx(campID) // #487: the turn ctx carries the session Identity
	st := storage.New(pool)

	rec := kgfacts.New(st, &fakeMetrics{}, nil, kgfacts.Config{})

	// runStream drives one streaming turn for the Agent and returns the assembled
	// system prompt captured by the streaming engine.
	runStream := func(agentID string) string {
		eng := &captureStreamEngine{reply: "Aye."}
		r := agent.NewReplier(agent.Config{
			Persona:     agent.Persona{AgentID: agentID, Markdown: "You are an NPC.", Voice: tts.Voice{ProviderID: "elevenlabs", VoiceID: "v1"}},
			Engine:      eng,
			Synthesizer: nullSynth{},
			Facts:       rec,
		})
		err := r.ReplyStream()(ctx, routedTo(agentID, "Where are you?"),
			func(orchestrator.Reply) error { return nil })
		if err != nil {
			t.Fatalf("ReplyStream: %v", err)
		}
		if len(eng.captured) == 0 {
			t.Fatal("streaming engine captured no messages")
		}
		return eng.captured[0].Text
	}

	// Bart resides in the Prancing Pony.
	bart := mkNode(t, st, campID, storage.KGNodeNPC, "Bart", "A gruff innkeeper.")
	bartAgent := linkAgent(t, st, campID, bart.ID, "Bart")
	pony := mkNode(t, st, campID, storage.KGNodeLocation, "The Prancing Pony", "A warm tavern by the river.")
	mkEdge(t, st, campID, bart.ID, pony.ID, storage.KGEdgeResidesIn)

	// Cyra resides in the Northern Keep.
	cyra := mkNode(t, st, campID, storage.KGNodeNPC, "Cyra", "A vigilant sentinel.")
	cyraAgent := linkAgent(t, st, campID, cyra.ID, "Cyra")
	keep := mkNode(t, st, campID, storage.KGNodeLocation, "The Northern Keep", "A cold fortress on the frontier.")
	mkEdge(t, st, campID, cyra.ID, keep.ID, storage.KGEdgeResidesIn)

	bartPrompt := runStream(bartAgent.String())
	if !strings.Contains(bartPrompt, "A warm tavern by the river.") {
		t.Errorf("Bart's prompt is missing his OWN Location body: %q", bartPrompt)
	}
	if strings.Contains(bartPrompt, "A cold fortress on the frontier.") {
		t.Errorf("Bart's prompt leaked Cyra's Location body: %q", bartPrompt)
	}

	cyraPrompt := runStream(cyraAgent.String())
	if !strings.Contains(cyraPrompt, "A cold fortress on the frontier.") {
		t.Errorf("Cyra's prompt is missing her OWN Location body: %q", cyraPrompt)
	}
	if strings.Contains(cyraPrompt, "A warm tavern by the river.") {
		t.Errorf("Cyra's prompt leaked Bart's Location body: %q", cyraPrompt)
	}
}
