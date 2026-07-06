//go:build integration

// These tests stand up a real Postgres (testcontainers) to exercise the seed →
// DB → loadSeededNPCs → buildConversation round-trip, so they are tag-isolated
// behind `integration` (ADR-0033): the default `go test ./...` stays Docker-free
// per ADR-0021, and a dedicated `-tags=integration` CI job runs these with
// Docker. The runtime t.Skip on a missing Postgres remains for local
// convenience. The keyless wirenpc tests (address routing, npcVoice) live in
// wirenpc_test.go and stay in the default suite.
package wirenpc

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/storage/crypto"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
	ttseleven "github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
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

	specs, _, _, err := loadSeededNPCs(ctx, storage.New(pool))
	if err != nil {
		t.Fatalf("loadSeededNPCs: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("demo seed loaded %d Character NPCs, want 1", len(specs))
	}
	loaded := specs[0]
	want := hardcodedNPC()

	if loaded.name != want.name {
		t.Errorf("name = %q, want %q", loaded.name, want.name)
	}
	if loaded.persona != want.persona {
		t.Errorf("persona mismatch:\n got %q\nwant %q", loaded.persona, want.persona)
	}
	// Compare Voice field-by-field, decoding Settings semantically: the blob
	// round-trips through Postgres jsonb, which reserializes with whitespace, so a
	// raw-byte compare of the json.RawMessage spuriously fails even when the
	// settings are identical. Decode both to the typed Settings and compare those.
	if loaded.voice.ProviderID != want.voice.ProviderID ||
		loaded.voice.VoiceID != want.voice.VoiceID ||
		loaded.voice.Name != want.voice.Name ||
		loaded.voice.Language != want.voice.Language {
		t.Errorf("voice identity mismatch:\n got %+v\nwant %+v", loaded.voice, want.voice)
	}
	var gotSettings, wantSettings ttseleven.Settings
	if err := json.Unmarshal(loaded.voice.Settings, &gotSettings); err != nil {
		t.Fatalf("loaded voice Settings not valid JSON: %v", err)
	}
	if err := json.Unmarshal(want.voice.Settings, &wantSettings); err != nil {
		t.Fatalf("want voice Settings not valid JSON: %v", err)
	}
	if !reflect.DeepEqual(gotSettings, wantSettings) {
		t.Errorf("voice Settings mismatch:\n got %+v\nwant %+v", gotSettings, wantSettings)
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
	conv, roster, cleanup, err := buildConversation(voiceevent.NewBus(), slog.New(slog.NewTextHandler(io.Discard, nil)), specs, "", ttseleven.New(""), nil, providerKeys{}, false, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildConversation from DB-loaded NPC: %v", err)
	}
	defer cleanup()
	if conv == nil {
		t.Fatal("buildConversation returned a nil Conversation")
	}
	if roster == nil {
		t.Fatal("buildConversation returned a nil Roster")
	}

	// The assembled Roster's Matcher must route a named utterance to the loaded
	// Agent's identity (so the Persona the reply loop answers for and the Address
	// Detection target agree — the chain that makes the NPC speak).
	routed := roster.matcher.TargetMatch("Bart, do you have a room?")
	if len(routed) == 0 {
		t.Fatal("named utterance routed to nobody for the DB-loaded NPC")
	}
	if got := routed[0].Target.AgentID; got != loaded.agentID {
		t.Errorf("routed AgentID = %q, want loaded agentID %q (matcher/Persona disagree)", got, loaded.agentID)
	}
}

// TestLoadSeededNPCs_LoadsAllCharacterAgents pins the Stage-3 multi-NPC load
// (issue #49): loadSeededNPCs returns EVERY Character NPC in the campaign, not
// just one — the old loadSeededNPC hard-failed unless exactly one was present.
// Seed the demo (one NPC), insert a second Character NPC into the same campaign,
// and assert both come back and assemble into a Roster that routes each by name.
func TestLoadSeededNPCs_LoadsAllCharacterAgents(t *testing.T) {
	pool := startDB(t)
	ctx := context.Background()
	st := storage.New(pool)

	if err := SeedNPC(ctx, pool, testCipher(t), nil); err != nil {
		t.Fatalf("SeedNPC: %v", err)
	}

	tenant, err := st.FindTenantByName(ctx, SeedTenantName)
	if err != nil {
		t.Fatalf("FindTenantByName: %v", err)
	}
	campaign, err := st.FindCampaignByName(ctx, tenant.ID, SeedCampaignName)
	if err != nil {
		t.Fatalf("FindCampaignByName: %v", err)
	}

	// A second Character NPC in the same campaign — a Voice Session can host ≥2.
	if _, err := st.CreateAgent(ctx, storage.NewAgent{
		CampaignID:  campaign.ID,
		Role:        storage.AgentRoleCharacter,
		Name:        "Mira",
		Persona:     "You are Mira, the wandering bard.",
		AddressOnly: false,
		Aliases:     []string{"bard"},
	}); err != nil {
		t.Fatalf("CreateAgent (Mira): %v", err)
	}

	specs, _, _, err := loadSeededNPCs(ctx, st)
	if err != nil {
		t.Fatalf("loadSeededNPCs: %v", err)
	}
	if len(specs) != 2 {
		t.Fatalf("loadSeededNPCs returned %d NPCs, want 2 (Bart + Mira)", len(specs))
	}

	// The two NPCs must assemble into a Roster that routes each by name to its own
	// identity — the end-to-end multi-NPC acceptance bar.
	conv, roster, cleanup, err := buildConversation(voiceevent.NewBus(), slog.New(slog.NewTextHandler(io.Discard, nil)), specs, "", ttseleven.New(""), nil, providerKeys{}, false, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildConversation from 2 DB NPCs: %v", err)
	}
	defer cleanup()
	if conv == nil {
		t.Fatal("buildConversation returned a nil Conversation")
	}

	byName := map[string]string{} // display name -> agentID
	for _, s := range specs {
		byName[s.name] = s.agentID
	}
	for _, name := range []string{"Bart", "Mira"} {
		routed := roster.matcher.TargetMatch(name + ", a word?")
		if len(routed) == 0 {
			t.Fatalf("naming %q routed to nobody", name)
		}
		if got := routed[0].Target.AgentID; got != byName[name] {
			t.Errorf("naming %q routed to AgentID %q, want %q", name, got, byName[name])
		}
	}
}

// TestCampaignLanguageDrivesMatcherPhonetics (#199): the loaded campaign's
// language column reaches the assembled Roster's matcher, selecting the
// phonetic encoder. A 'de' campaign hosting "Jäger" must route "Yeager" — an
// EN-biased STT rendering that only Kölner Phonetik matches (both code 047;
// Double Metaphone separates them and the edit net is out of reach at
// Damerau-Levenshtein 3). Before #199 the matcher was hardcoded to "en" and
// this utterance routed to nobody. The seed's Bart keeps the lone-NPC
// fallback inert, so the route proves the name tier.
func TestCampaignLanguageDrivesMatcherPhonetics(t *testing.T) {
	pool := startDB(t)
	ctx := context.Background()
	st := storage.New(pool)

	if err := SeedNPC(ctx, pool, testCipher(t), nil); err != nil {
		t.Fatalf("SeedNPC: %v", err)
	}
	// The demo seed is an "en" campaign; the live German table runs 'de'.
	if _, err := pool.Exec(ctx, `UPDATE campaign SET language = 'de'`); err != nil {
		t.Fatalf("set campaign language: %v", err)
	}

	tenant, err := st.FindTenantByName(ctx, SeedTenantName)
	if err != nil {
		t.Fatalf("FindTenantByName: %v", err)
	}
	campaign, err := st.FindCampaignByName(ctx, tenant.ID, SeedCampaignName)
	if err != nil {
		t.Fatalf("FindCampaignByName: %v", err)
	}
	jaegerID, err := st.CreateAgent(ctx, storage.NewAgent{
		CampaignID: campaign.ID,
		Role:       storage.AgentRoleCharacter,
		Name:       "Jäger",
		Persona:    "You are Jäger, the huntsman.",
	})
	if err != nil {
		t.Fatalf("CreateAgent (Jäger): %v", err)
	}

	specs, _, loadedCampaign, err := loadSeededNPCs(ctx, st)
	if err != nil {
		t.Fatalf("loadSeededNPCs: %v", err)
	}
	if loadedCampaign.Language != "de" {
		t.Fatalf("loadSeededNPCs surfaced campaign language %q, want %q", loadedCampaign.Language, "de")
	}

	conv, roster, cleanup, err := buildConversation(voiceevent.NewBus(),
		slog.New(slog.NewTextHandler(io.Discard, nil)), specs, loadedCampaign.Language,
		ttseleven.New(""), nil, providerKeys{}, false, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildConversation: %v", err)
	}
	defer cleanup()
	if conv == nil {
		t.Fatal("buildConversation returned a nil Conversation")
	}

	routed := roster.matcher.TargetMatch("Yeager, wie läuft die Jagd?")
	if len(routed) == 0 {
		t.Fatal(`"Yeager" routed to nobody — campaign language did not reach the matcher`)
	}
	if got := routed[0].Target.AgentID; got != jaegerID.String() {
		t.Errorf(`"Yeager" routed to AgentID %q, want Jäger %q`, got, jaegerID.String())
	}
}

// TestLoadedNPC_DerivesTruncationAliases is the #197 bar on the DB-loaded path
// (AC: "derivation exercised on both hardcoded-NPC and DB-loaded paths"): a
// Character NPC loaded from Postgres derives its STT-truncation aliases through
// the same matcherAgent seam the hardcoded path uses, so an utterance opening
// with the STT truncation "Art" routes to the seeded "Bart". Two more NPCs keep
// the lone-NPC fallback inert, so the route proves the derived alias — not the
// fallback. It mirrors TestCampaignLanguageDrivesMatcherPhonetics' seed→load→
// buildConversation shape.
func TestLoadedNPC_DerivesTruncationAliases(t *testing.T) {
	pool := startDB(t)
	ctx := context.Background()
	st := storage.New(pool)

	if err := SeedNPC(ctx, pool, testCipher(t), nil); err != nil {
		t.Fatalf("SeedNPC: %v", err)
	}
	// The demo seed is an "en" campaign; the live German table runs 'de'.
	if _, err := pool.Exec(ctx, `UPDATE campaign SET language = 'de'`); err != nil {
		t.Fatalf("set campaign language: %v", err)
	}

	tenant, err := st.FindTenantByName(ctx, SeedTenantName)
	if err != nil {
		t.Fatalf("FindTenantByName: %v", err)
	}
	campaign, err := st.FindCampaignByName(ctx, tenant.ID, SeedCampaignName)
	if err != nil {
		t.Fatalf("FindCampaignByName: %v", err)
	}
	// Two more Character NPCs so a lone-NPC fallback cannot manufacture the route.
	for _, name := range []string{"Greta", "Marek"} {
		if _, err := st.CreateAgent(ctx, storage.NewAgent{
			CampaignID: campaign.ID,
			Role:       storage.AgentRoleCharacter,
			Name:       name,
			Persona:    "You are " + name + ".",
		}); err != nil {
			t.Fatalf("CreateAgent (%s): %v", name, err)
		}
	}

	specs, _, loadedCampaign, err := loadSeededNPCs(ctx, st)
	if err != nil {
		t.Fatalf("loadSeededNPCs: %v", err)
	}

	conv, roster, cleanup, err := buildConversation(voiceevent.NewBus(),
		slog.New(slog.NewTextHandler(io.Discard, nil)), specs, loadedCampaign.Language,
		ttseleven.New(""), nil, providerKeys{}, false, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildConversation: %v", err)
	}
	defer cleanup()
	if conv == nil {
		t.Fatal("buildConversation returned a nil Conversation")
	}

	var bartID string
	for _, s := range specs {
		if s.name == "Bart" {
			bartID = s.agentID
		}
	}
	if bartID == "" {
		t.Fatal("seeded Bart not found among loaded specs")
	}

	routed := roster.matcher.TargetMatch("Art, wie läuft das Geschäft heute Abend?")
	if len(routed) == 0 {
		t.Fatal(`"Art, …" routed to nobody — the DB-loaded NPC did not derive its truncation alias`)
	}
	if got := routed[0].Target.AgentID; got != bartID {
		t.Errorf(`"Art, …" routed to %q, want seeded Bart %q`, got, bartID)
	}
}

// TestLoadSeededNPCs_HydratesToolGrants is the #113 end-to-end bar over the real
// DB path: the seeded NPC's Tool Grants come from its tool_agent_grant rows, and
// removing the dice grant row makes the hydrated GrantSet declare no Tool at all
// (AC2/AC3) — the LLM is never shown dice, so the NPC cannot roll. dice is the
// only Tool the demo registry holds, so an empty Declarations() is exactly "the
// NPC can no longer roll".
func TestLoadSeededNPCs_HydratesToolGrants(t *testing.T) {
	pool := startDB(t)
	ctx := context.Background()
	st := storage.New(pool)

	if err := SeedNPC(ctx, pool, testCipher(t), nil); err != nil {
		t.Fatalf("SeedNPC: %v", err)
	}

	// Registry the live loop hydrates grants against (dice is the one v1.0 Tool).
	reg := tool.NewRegistry()
	reg.MustRegister(tool.NewDice())

	specs, _, _, err := loadSeededNPCs(ctx, st)
	if err != nil {
		t.Fatalf("loadSeededNPCs: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("loaded %d NPCs, want 1", len(specs))
	}
	bart := specs[0]

	// Seeded Bart hydrates a dice grant → its GrantSet declares dice to the LLM.
	if got := tool.NewGrantSet(reg, bart.grants...).Declarations(); len(got) != 1 || got[0].Name != "dice" {
		t.Fatalf("seeded NPC declared %+v, want exactly [dice]", got)
	}

	// Resolve Bart's Agent id (the spec.agentID is the UUID string) and remove
	// his dice grant row — the GM revoking the Tool (#117 owns the RPC; here we
	// hit storage directly).
	bartID, err := uuid.Parse(bart.agentID)
	if err != nil {
		t.Fatalf("parse agentID %q: %v", bart.agentID, err)
	}
	if err := st.DeleteToolGrant(ctx, bartID, "dice"); err != nil {
		t.Fatalf("DeleteToolGrant(dice): %v", err)
	}

	// Re-hydrate: with the row gone, the NPC is granted nothing → the LLM is
	// never shown a Tool, so it cannot roll.
	respec, _, _, err := loadSeededNPCs(ctx, st)
	if err != nil {
		t.Fatalf("loadSeededNPCs after revoke: %v", err)
	}
	if len(respec[0].grants) != 0 {
		t.Fatalf("after removing the dice grant row, NPC still has grants %+v, want none", respec[0].grants)
	}
	if got := tool.NewGrantSet(reg, respec[0].grants...).Declarations(); len(got) != 0 {
		t.Fatalf("after revoke the NPC declared %+v, want none (Tool never declared → cannot roll)", got)
	}
}

// TestBuildConversation_CleanupDoesNotDestroyEngine guards issue #44's reconnect
// loop against a regression that destroys the process-global Silero/ONNX
// environment. cleanup() must release only the per-cycle VAD session, NEVER the
// shared engine: silero.Engine wraps an ONNX environment initialised once and
// never re-initialised, so closing it would tear ONNX down for the whole process
// and every later reconnect's NewSession would fail — the NPC would go
// permanently deaf after the first Discord drop. Build → cleanup → build again
// (mimicking one reconnect cycle) must both succeed. Real Silero, so integration.
func TestBuildConversation_CleanupDoesNotDestroyEngine(t *testing.T) {
	npcs := []npcSpec{hardcodedNPC()}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	conv1, _, cleanup1, err := buildConversation(voiceevent.NewBus(), log, npcs, "", ttseleven.New(""), nil, providerKeys{}, false, nil, nil, nil)
	if err != nil {
		t.Fatalf("first buildConversation: %v", err)
	}
	if conv1 == nil {
		t.Fatal("first buildConversation returned a nil Conversation")
	}
	cleanup1() // end of reconnect cycle 1

	conv2, _, cleanup2, err := buildConversation(voiceevent.NewBus(), log, npcs, "", ttseleven.New(""), nil, providerKeys{}, false, nil, nil, nil)
	if err != nil {
		t.Fatalf("second buildConversation after cleanup: %v — cleanup destroyed the shared ONNX env?", err)
	}
	if conv2 == nil {
		t.Fatal("second buildConversation returned a nil Conversation")
	}
	cleanup2()
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
	campaign, err := st.FindCampaignByName(ctx, tenant.ID, SeedCampaignName)
	if err != nil {
		t.Fatalf("FindCampaignByName: %v", err)
	}
	chars, err := st.CharacterAgents(ctx, campaign.ID)
	if err != nil {
		t.Fatalf("CharacterAgents: %v", err)
	}
	if len(chars) != 1 {
		t.Fatalf("expected 1 Character NPC after two seeds, got %d", len(chars))
	}
}
