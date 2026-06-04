package wirenpc

import (
	"context"
	"crypto/rand"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
)

// pgImage carries the pgvector extension the schema needs (ADR-0011).
const pgImage = "pgvector/pgvector:pg17"

// startDB spins up a pgvector container, applies the migrations, and returns a
// pool. It skips LOUDLY when Docker is unavailable so a green `go test` that
// never touched a DB can't be mistaken for real coverage. GLYPHOXA_TEST_DSN
// points at an external Postgres (with pgvector) to skip the container.
func startDB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := dsnFromEnvOrContainer(t)

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

func dsnFromEnvOrContainer(t *testing.T) string {
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
		t.Skipf("SKIPPED DB TEST — NO POSTGRES: could not start the %s container "+
			"(is Docker running?). The seed/load equivalence test was NOT run. Set "+
			"GLYPHOXA_TEST_DSN to run it without Docker. underlying error: %v", pgImage, err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(container) })
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	return dsn
}

func testCipher(t *testing.T) *crypto.Cipher {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand key: %v", err)
	}
	c, err := crypto.New(key)
	if err != nil {
		t.Fatalf("crypto.New: %v", err)
	}
	return c
}

// TestSeedThenLoadEquivalence is the task-#5 verification bar: a seeded DB must
// reproduce the in-code NPC. Seed → load → assert the loaded npcSpec matches the
// hardcoded one on every voiced field (persona, voice, aliases, name). AgentID
// is the only field that legitimately differs — in code it was the literal
// "bart"; from the DB it is the Agent's UUID. Both are valid stable identities;
// what matters is that the matcher and Persona share it (asserted separately).
func TestSeedThenLoadEquivalence(t *testing.T) {
	pool := startDB(t)
	ctx := context.Background()

	if err := SeedNPC(ctx, pool, testCipher(t), nil); err != nil {
		t.Fatalf("SeedNPC: %v", err)
	}

	loaded, err := loadSeededNPC(ctx, storage.New(pool))
	if err != nil {
		t.Fatalf("loadSeededNPC: %v", err)
	}
	want := hardcodedNPC()

	if loaded.name != want.name {
		t.Errorf("name = %q, want %q", loaded.name, want.name)
	}
	if loaded.persona != want.persona {
		t.Errorf("persona mismatch:\n got %q\nwant %q", loaded.persona, want.persona)
	}
	if !reflect.DeepEqual(loaded.voice, want.voice) {
		t.Errorf("voice mismatch:\n got %+v\nwant %+v", loaded.voice, want.voice)
	}
	if !reflect.DeepEqual(loaded.aliases, want.aliases) {
		t.Errorf("aliases = %v, want %v", loaded.aliases, want.aliases)
	}
	if loaded.agentID == "" {
		t.Error("loaded agentID is empty; address detection needs a stable identity")
	}

	// The DB-loaded spec must actually build a routable Conversation — not just
	// reconstruct the fields. buildConversation is the live loop's constructor;
	// assert it succeeds, and that the matcher routes a named utterance to the
	// loaded Agent's identity (so the Persona the reply loop answers for and the
	// Address Detection target agree — the chain that makes the NPC speak).
	conv, err := buildConversation(slog.New(slog.NewTextHandler(io.Discard, nil)), loaded)
	if err != nil {
		t.Fatalf("buildConversation from DB-loaded NPC: %v", err)
	}
	if conv == nil {
		t.Fatal("buildConversation returned a nil Conversation")
	}

	routed := npcMatcher(loaded).TargetMatch("Bart, do you have a room?")
	if len(routed) == 0 {
		t.Fatal("named utterance routed to nobody for the DB-loaded NPC")
	}
	if got := routed[0].Target.AgentID; got != loaded.agentID {
		t.Errorf("routed AgentID = %q, want loaded agentID %q (matcher/Persona disagree)", got, loaded.agentID)
	}
}

// TestSeedIdempotent asserts a second SeedNPC is a no-op (the slice re-seeds on
// every boot in some deploys; it must not duplicate or error).
func TestSeedIdempotent(t *testing.T) {
	pool := startDB(t)
	ctx := context.Background()
	cipher := testCipher(t)

	if err := SeedNPC(ctx, pool, cipher, nil); err != nil {
		t.Fatalf("first SeedNPC: %v", err)
	}
	if err := SeedNPC(ctx, pool, cipher, nil); err != nil {
		t.Fatalf("second SeedNPC (should be no-op): %v", err)
	}

	// Still exactly one Character NPC after two seeds.
	st := storage.New(pool)
	tenant, err := st.FindTenantByName(ctx, SeedTenantName)
	if err != nil {
		t.Fatalf("FindTenantByName: %v", err)
	}
	campaignID, err := st.FindCampaignByName(ctx, tenant.ID, SeedCampaignName)
	if err != nil {
		t.Fatalf("FindCampaignByName: %v", err)
	}
	chars, err := st.CharacterAgents(ctx, campaignID)
	if err != nil {
		t.Fatalf("CharacterAgents: %v", err)
	}
	if len(chars) != 1 {
		t.Fatalf("expected 1 Character NPC after two seeds, got %d", len(chars))
	}
}
