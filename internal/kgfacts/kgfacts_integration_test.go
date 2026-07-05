//go:build integration

// This drives the #126 AC5 end-to-end round-trip against a real Postgres
// (testcontainers): storage.CreateNode(Note) → kgfacts over the real Store +
// a fake Sessions → an agent.Replier with a capturing engine → the Note body
// appears in the assembled system prompt; flipping gm_private removes it next
// turn; and with no public Notes the prompt is byte-identical to the no-facts
// path. Tag-isolated behind `integration` (ADR-0033).

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
		Target: voiceevent.AddressTarget{AgentID: agentID, AgentRole: "character", Name: "Bart"},
	}
}

// TestKGFacts_EndToEnd_Integration is #126 AC5: the create → persist → surfaced-in-
// the-prompt round-trip over a real Store, plus the gm_private hide and the
// byte-identical no-facts guarantee.
func TestKGFacts_EndToEnd_Integration(t *testing.T) {
	pool, campID := seedCampaign(t, startPostgres(t))
	ctx := context.Background()
	st := storage.New(pool)

	rec := kgfacts.New(st, activeSessions(campID), &fakeMetrics{}, nil, kgfacts.Config{})

	// runTurn drives one batch turn and returns the assembled system prompt.
	runTurn := func(facts agent.FactsRecaller) string {
		eng := &captureEngine{reply: "Aye."}
		r := agent.NewReplier(agent.Config{
			Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: tts.Voice{ProviderID: "elevenlabs", VoiceID: "v1"}},
			Engine:      eng,
			Synthesizer: nullSynth{},
			Facts:       facts,
		})
		r.Reply()(ctx, routedTo("bart", "What do you know?"))
		if len(eng.captured) == 0 {
			t.Fatal("engine captured no messages")
		}
		return eng.captured[0].Text
	}

	// Baseline: no Facts recaller at all.
	base := runTurn(nil)

	// With the recaller but no Nodes yet: byte-identical to the baseline (the
	// reserved slot stays empty).
	if empty := runTurn(rec); empty != base {
		t.Errorf("no-notes prompt not byte-identical to the no-facts path:\n base=%q\n  rec=%q", base, empty)
	}

	// Create a gm-public Note; its body must surface in the assembled prompt.
	note, err := st.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campID, Type: storage.KGNodeNote,
		Name: "The Vault", Body: "It has never been opened.",
	})
	if err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	withFacts := runTurn(rec)
	if !strings.Contains(withFacts, "It has never been opened.") {
		t.Errorf("public Note body not injected into the prompt: %q", withFacts)
	}
	if !strings.Contains(withFacts, "## What you know about the world") {
		t.Errorf("facts block header missing: %q", withFacts)
	}
	if !strings.Contains(withFacts, "### The Vault (Note)") {
		t.Errorf("fact heading missing: %q", withFacts)
	}

	// Flip gm_private: the fact must vanish on the very next turn (no cache).
	if _, err := pool.Exec(ctx, `UPDATE kg_node SET gm_private = true WHERE id = $1`, note.ID); err != nil {
		t.Fatalf("flip gm_private: %v", err)
	}
	hidden := runTurn(rec)
	if strings.Contains(hidden, "It has never been opened.") {
		t.Errorf("gm_private Note leaked into the prompt: %q", hidden)
	}
	// With the sole Note now private, the prompt is byte-identical to the baseline.
	if hidden != base {
		t.Errorf("all-private prompt not byte-identical to the no-facts baseline:\n base=%q\n hidden=%q", base, hidden)
	}
}
