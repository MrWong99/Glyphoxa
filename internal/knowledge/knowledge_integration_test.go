//go:build integration

// Drives the #296 knowledge adapter against a real Postgres (testcontainers):
// storage's real SearchNodes / SearchTranscriptLines behind the neutral tool
// seams, proving the load-bearing gm_private DROP (SearchNodes returns private
// Nodes; SearchFacts must not), the campaign-scoping from the active session, and
// the no-session error. Tag-isolated behind `integration` (ADR-0033).

package knowledge_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/MrWong99/Glyphoxa/internal/knowledge"
	"github.com/MrWong99/Glyphoxa/internal/storage"
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
		t.Skipf("SKIPPED DB TEST — NO POSTGRES: could not start %s (is Docker running?). err: %v", pgImage, err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(container) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	return dsn
}

func seedCampaign(t *testing.T, dsn string) (*storage.Store, uuid.UUID) {
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
	return storage.New(pool), campaignID
}

// staticSession is a knowledge.Sessions returning a fixed active session (or
// none), standing in for the live *session.Manager.
type staticSession struct {
	sess storage.VoiceSession
	live bool
}

func (s staticSession) Snapshot() (storage.VoiceSession, bool) { return s.sess, s.live }

func TestAdapter_SearchFactsDropsGMPrivate_RealDB(t *testing.T) {
	dsn := startPostgres(t)
	store, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()

	// A public and a gm_private Node that both match the search term.
	if _, err := store.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: storage.KGNodeNPC, Name: "Duke Aldric", Body: "rules the city openly",
	}); err != nil {
		t.Fatalf("create public node: %v", err)
	}
	if _, err := store.CreateNode(ctx, storage.NewKGNode{
		CampaignID: campaignID, Type: storage.KGNodeFaction, Name: "Duke's Shadow Cabal", Body: "GM eyes only", GMPrivate: true,
	}); err != nil {
		t.Fatalf("create private node: %v", err)
	}

	adapter := knowledge.New(store, staticSession{sess: storage.VoiceSession{CampaignID: campaignID}, live: true})

	facts, err := adapter.SearchFacts(ctx, "duke", 10)
	if err != nil {
		t.Fatalf("SearchFacts: %v", err)
	}
	for _, f := range facts {
		if f.Name == "Duke's Shadow Cabal" {
			t.Fatalf("gm_private Node leaked through SearchFacts: %+v", facts)
		}
	}
	if len(facts) != 1 || facts[0].Name != "Duke Aldric" {
		t.Fatalf("facts = %+v, want only the public Duke Aldric", facts)
	}
}

func TestAdapter_SearchTranscriptCampaignScoped_RealDB(t *testing.T) {
	dsn := startPostgres(t)
	store, campaignID := seedCampaign(t, dsn)
	ctx := context.Background()

	sess, err := store.CreateVoiceSession(ctx, campaignID)
	if err != nil {
		t.Fatalf("create voice session: %v", err)
	}
	if err := store.UpsertTranscriptLine(ctx, storage.TranscriptLine{
		VoiceSessionID: sess.ID, CampaignID: campaignID, LineID: "l1", Seq: 1,
		Who: "Bart", Kind: "npc", TS: time.Now(), Text: "I remember your promise, traveler.",
	}); err != nil {
		t.Fatalf("upsert transcript line: %v", err)
	}

	adapter := knowledge.New(store, staticSession{sess: sess, live: true})

	hits, err := adapter.SearchTranscript(ctx, "promise", 10)
	if err != nil {
		t.Fatalf("SearchTranscript: %v", err)
	}
	if len(hits) != 1 || hits[0].Who != "Bart" || hits[0].Kind != "npc" {
		t.Fatalf("hits = %+v, want the one Bart line", hits)
	}

	// No active session → the campaign-scoped reads error, never read cross-campaign.
	idle := knowledge.New(store, staticSession{live: false})
	if _, err := idle.SearchTranscript(ctx, "promise", 10); !errors.Is(err, knowledge.ErrNoActiveSession) {
		t.Errorf("SearchTranscript idle = %v, want ErrNoActiveSession", err)
	}
	if _, err := idle.SearchFacts(ctx, "duke", 10); !errors.Is(err, knowledge.ErrNoActiveSession) {
		t.Errorf("SearchFacts idle = %v, want ErrNoActiveSession", err)
	}
}
