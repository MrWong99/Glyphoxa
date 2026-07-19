//go:build integration

// This test stands up a real pgvector Postgres (testcontainers), embeds seeded
// Transcript Chunks through the real #116 backfill path (embedworker over an
// embeddingstest.Fixed provider), and drives a real Agent turn with the recaller
// wired in — the content-ful proof of AC1 (an NPC references a fact established in
// an earlier embedded chunk that it could not without retrieval) and AC2 (personal
// vs world framing). Tag-isolated behind `integration` (ADR-0021/0033); run with
// `go test -tags=integration ./internal/recall/`.

package recall

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

	"github.com/MrWong99/Glyphoxa/internal/embedworker"
	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/embeddings"
	"github.com/MrWong99/Glyphoxa/pkg/voice/embeddings/embeddingstest"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

const pgImage = "pgvector/pgvector:pg17"

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
			"Set GLYPHOXA_TEST_DSN to an external pgvector Postgres to run without Docker. err: %v", pgImage, err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(container) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	return dsn
}

// migratedPool migrates the schema up and returns an app pool.
func migratedPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	if err := storage.MigrateUp(context.Background(), db); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// recallVec768 builds an embeddings.Dim vector from its leading components,
// zero-padded to the vector(768) shape the storage layer requires.
func recallVec768(lead ...float32) []float32 {
	v := make([]float32, embeddings.Dim)
	copy(v, lead)
	return v
}

// captureEngine is a scripted [agent.Engine] that records the exact messages the
// loop assembled, so the test asserts on the real system prompt.
type captureEngine struct {
	reply string
	msgs  []llm.Message
}

func (e *captureEngine) Generate(_ context.Context, messages []llm.Message) (string, error) {
	e.msgs = append([]llm.Message(nil), messages...)
	return e.reply, nil
}

func (e *captureEngine) systemPrompt(t *testing.T) string {
	t.Helper()
	if len(e.msgs) == 0 {
		t.Fatal("engine was never called")
	}
	if e.msgs[0].Role != llm.RoleSystem {
		t.Fatalf("first message role = %q, want system", e.msgs[0].Role)
	}
	return e.msgs[0].Text
}

// silentSynth returns an empty audio-markup instruction, so the system prompt is
// Persona + the recalled memory block — nothing else to disentangle.
type silentSynth struct{}

func (silentSynth) Synthesize(context.Context, tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	ch := make(chan tts.AudioChunk)
	close(ch)
	return ch, nil
}
func (silentSynth) AudioMarkupPrompt(tts.Voice) string { return "" }

// TestRecall_Integration_FactLandsInCorrectSection is AC1 + AC2 end-to-end: a fact
// established in an embedded chunk the NPC participated in surfaces in the
// "personally witnessed" section, a campaign fact the NPC did NOT participate in
// surfaces only in the "second-hand" section, and with recall disabled neither
// reaches the prompt.
func TestRecall_Integration_FactLandsInCorrectSection(t *testing.T) {
	dsn := startPostgres(t)
	pool := migratedPool(t, dsn)
	ctx := context.Background()
	st := storage.New(pool)

	// Seed a tenant + campaign + voice session (FKs for chunks).
	var tenantID, campaignID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO tenant (name) VALUES ('Acme') RETURNING id`).Scan(&tenantID); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO campaign (tenant_id, name, system, language) VALUES ($1, 'Lost Mine', 'dnd5e', 'en') RETURNING id`,
		tenantID).Scan(&campaignID); err != nil {
		t.Fatalf("insert campaign: %v", err)
	}
	vs, err := st.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("CreateVoiceSession: %v", err)
	}
	// The turn ctx carries the session Identity Recall reads its Campaign from
	// (#487) — in production it descends from Manager.Start's run context.
	ctx = session.NewContext(ctx, session.Identity{SessionID: vs.ID, CampaignID: campaignID, TenantID: tenantID})

	targetAgent, otherAgent := uuid.New(), uuid.New()

	const (
		utterance = "do you remember the ruby dagger"
		rubyText  = "The ruby dagger was stolen from the vault."
		dragText  = "A dragon was seen near the northern pass."
		breadText = "The tavern serves warm bread each morning."
	)

	// A tiny cosine space keyed on the query direction e0.
	fixed := embeddingstest.Fixed{
		utterance: recallVec768(1, 0),
		rubyText:  recallVec768(0.95, 0.05),
		dragText:  recallVec768(0.9, 0.2),
		breadText: recallVec768(0, 1),
	}

	// Insert the chunks with embedding NULL (the real writer path).
	insertChunk := func(content string, agents []uuid.UUID) {
		if _, err := st.InsertTranscriptChunk(ctx, storage.TranscriptChunk{
			CampaignID:           campaignID,
			VoiceSessionID:       vs.ID,
			Content:              content,
			ParticipatedAgentIDs: agents,
			StartedAt:            time.Date(2026, 7, 5, 18, 0, 0, 0, time.UTC),
		}); err != nil {
			t.Fatalf("InsertTranscriptChunk %q: %v", content, err)
		}
	}
	insertChunk(rubyText, []uuid.UUID{targetAgent}) // NPC personally participated
	insertChunk(dragText, []uuid.UUID{otherAgent})  // campaign-wide only
	insertChunk(breadText, []uuid.UUID{otherAgent})

	// Embed them through the real #116 backfill worker over the Fixed provider.
	drainBacklog(t, st, fixed)

	bus := voiceevent.NewBus()
	recaller := New(fixed, st, fakeSessions{campaignID: campaignID, active: true}, bus, newFakeMetrics(),
		testLogger(), Config{})
	t.Cleanup(recaller.Close)

	// Recall ON: the fact must reach the prompt in the correct section.
	eng := &captureEngine{reply: "Aye, I recall it."}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: targetAgent.String(), Markdown: "You are the tavern keeper.", Voice: tts.Voice{ProviderID: "elevenlabs", VoiceID: "v"}},
		Engine:      eng,
		Synthesizer: silentSynth{},
		Memory:      recaller,
	})
	r.Reply()(ctx, addressRouted(targetAgent.String(), utterance))

	sys := eng.systemPrompt(t)
	if !strings.Contains(sys, rubyText) {
		t.Fatalf("AC1: established fact not recalled into the prompt:\n%s", sys)
	}
	iWitnessed := strings.Index(sys, "You personally witnessed:")
	iWorld := strings.Index(sys, "You may know these from around the campaign")
	if iWitnessed < 0 || iWorld < 0 || iWitnessed > iWorld {
		t.Fatalf("AC2: both memory sections must be present, witnessed before world context:\n%s", sys)
	}
	witnessed, world := sys[iWitnessed:iWorld], sys[iWorld:]
	if !strings.Contains(witnessed, rubyText) {
		t.Errorf("AC2: participated fact not in the witnessed section:\n%s", witnessed)
	}
	if strings.Contains(witnessed, dragText) {
		t.Errorf("AC2: a non-participated fact leaked into the witnessed section:\n%s", witnessed)
	}
	if !strings.Contains(world, dragText) {
		t.Errorf("AC2: campaign fact not framed as world context:\n%s", world)
	}
	// Dedup (finding 1a): the participated fact must appear ONLY in the witnessed
	// section, never duplicated into world context with contradictory framing.
	if strings.Contains(world, rubyText) {
		t.Errorf("AC2: participated fact duplicated into the world-context section:\n%s", world)
	}

	// Recall OFF (AC6): the same turn without a recaller never mentions the fact.
	engOff := &captureEngine{reply: "Hmm."}
	rOff := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: targetAgent.String(), Markdown: "You are the tavern keeper.", Voice: tts.Voice{ProviderID: "elevenlabs", VoiceID: "v"}},
		Engine:      engOff,
		Synthesizer: silentSynth{},
		// Memory nil.
	})
	rOff.Reply()(ctx, addressRouted(targetAgent.String(), utterance))
	if off := engOff.systemPrompt(t); strings.Contains(off, rubyText) || strings.Contains(off, "You personally witnessed:") {
		t.Errorf("AC6: with recall disabled the prompt must carry no memory:\n%s", off)
	}
}

// drainBacklog runs the real backfill worker over provider until the
// NULL-embedding backlog reaches zero, then stops it.
func drainBacklog(t *testing.T, st *storage.Store, provider embeddings.Provider) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go embedworker.New(st, provider, "test-model", nil, testLogger(),
		embedworker.Config{Interval: 10 * time.Millisecond, BatchSize: 16}).Run(ctx)

	deadline := time.Now().Add(15 * time.Second)
	for {
		n, err := st.CountUnembeddedChunks(context.Background())
		if err != nil {
			t.Fatalf("CountUnembeddedChunks: %v", err)
		}
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("backfill did not drain the embedding backlog in time (%d left)", n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func addressRouted(agentID, text string) voiceevent.AddressRouted {
	return voiceevent.AddressRouted{
		At:     time.Now(),
		Text:   text,
		Target: voiceevent.AddressTarget{AgentID: agentID, AgentRole: "character", Name: "Keeper"},
	}
}
