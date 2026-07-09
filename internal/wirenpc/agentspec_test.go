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
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
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
	conv, roster, cleanup, err := buildConversation(voiceevent.NewBus(), slog.New(slog.NewTextHandler(io.Discard, nil)), specs, "", ttseleven.New(""), nil, providerKeys{}, "", false, nil, nil, nil, nil, nil)
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
	conv, roster, cleanup, err := buildConversation(voiceevent.NewBus(), slog.New(slog.NewTextHandler(io.Discard, nil)), specs, "", ttseleven.New(""), nil, providerKeys{}, "", false, nil, nil, nil, nil, nil)
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
		ttseleven.New(""), nil, providerKeys{}, "", false, nil, nil, nil, nil, nil)
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
		ttseleven.New(""), nil, providerKeys{}, "", false, nil, nil, nil, nil, nil)
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

	conv1, _, cleanup1, err := buildConversation(voiceevent.NewBus(), log, npcs, "", ttseleven.New(""), nil, providerKeys{}, "", false, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("first buildConversation: %v", err)
	}
	if conv1 == nil {
		t.Fatal("first buildConversation returned a nil Conversation")
	}
	cleanup1() // end of reconnect cycle 1

	conv2, _, cleanup2, err := buildConversation(voiceevent.NewBus(), log, npcs, "", ttseleven.New(""), nil, providerKeys{}, "", false, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("second buildConversation after cleanup: %v — cleanup destroyed the shared ONNX env?", err)
	}
	if conv2 == nil {
		t.Fatal("second buildConversation returned a nil Conversation")
	}
	cleanup2()
}

// alwaysButler is a TargetMatcher that routes every utterance to the Butler,
// isolating the gate test from the live fuzzy matcher's routing decisions.
type alwaysButler struct{}

func (alwaysButler) TargetMatch(text string) []voiceevent.AddressRouted {
	return []voiceevent.AddressRouted{{
		At:     time.Now(),
		Text:   text,
		Target: voiceevent.AddressTarget{AgentID: "butler", AgentRole: voiceevent.AgentRoleButler, Name: "Glyphoxa"},
	}}
}

// TestBuildConversation_ThreadsGMSpeakerIntoButlerGate proves the wiring half of
// issue #280: Config.GMSpeaker flows into the AddressDetector's Butler GM-address
// gate (ADR-0024/ADR-0050). It captures the DetectorOptions buildConversation
// passes to newAddressDetector, then applies them to a detector over a
// Butler-always matcher (isolating the gate from the live fuzzy matcher). A
// Butler route then publishes only for the allowlisted GM SpeakerID and is
// dropped for a non-GM one — so the gate is armed with GMSpeaker, not nil. Real
// Silero, so integration.
func TestBuildConversation_ThreadsGMSpeakerIntoButlerGate(t *testing.T) {
	var capturedOpts []orchestrator.DetectorOption
	orig := newAddressDetector
	newAddressDetector = func(m orchestrator.TargetMatcher, opts ...orchestrator.DetectorOption) *orchestrator.AddressDetector {
		capturedOpts = opts
		return orig(m, opts...)
	}
	t.Cleanup(func() { newAddressDetector = orig })

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	gmSpeaker := func(id string) bool { return id == "gm-1" }
	_, _, cleanup, err := buildConversation(voiceevent.NewBus(), log, []npcSpec{hardcodedNPC()}, "",
		ttseleven.New(""), nil, providerKeys{}, "", false, nil, nil, nil, nil, gmSpeaker)
	if err != nil {
		t.Fatalf("buildConversation with GMSpeaker: %v", err)
	}
	t.Cleanup(cleanup)
	if len(capturedOpts) == 0 {
		t.Fatal("buildConversation passed no DetectorOptions — GM gate not threaded")
	}

	// Apply the captured options to a Butler-always matcher and drive the gate.
	probe := orig(alwaysButler{}, capturedOpts...)
	bus := voiceevent.NewBus()
	t.Cleanup(probe.Bind(context.Background(), bus))
	var routed []voiceevent.AddressRouted
	voiceevent.On(bus, func(e voiceevent.AddressRouted) { routed = append(routed, e) })

	bus.Publish(voiceevent.STTFinal{At: time.Now(), Text: "help", SpeakerID: "player-9"}) // non-GM → dropped
	bus.Publish(voiceevent.STTFinal{At: time.Now(), Text: "help", SpeakerID: "gm-1"})     // GM → published

	if len(routed) != 1 {
		t.Fatalf("Butler routes published = %d, want 1 (GM only; non-GM dropped): %+v", len(routed), routed)
	}
	if routed[0].Target.AgentRole != "butler" {
		t.Fatalf("published route AgentRole = %q, want butler", routed[0].Target.AgentRole)
	}
}

// TestLoadSeededNPCs_ModelFromBoundLLMConfig is the #227 AC1 bar: a non-default
// model saved on the Agent's bound LLM provider_config threads through
// loadSeededNPCs onto the npcSpec, so the live engine runs it instead of the
// hardcoded default. Update the bound row's model and assert the loaded spec
// carries it.
func TestLoadSeededNPCs_ModelFromBoundLLMConfig(t *testing.T) {
	pool := startDB(t)
	ctx := context.Background()

	if err := SeedNPC(ctx, pool, testCipher(t), nil); err != nil {
		t.Fatalf("SeedNPC: %v", err)
	}
	const want = "meta-llama/llama-4-scout-17b-16e-instruct"
	if _, err := pool.Exec(ctx, `UPDATE provider_config SET model = $1 WHERE component = 'llm'`, want); err != nil {
		t.Fatalf("set llm model: %v", err)
	}

	specs, _, _, err := loadSeededNPCs(ctx, storage.New(pool))
	if err != nil {
		t.Fatalf("loadSeededNPCs: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("loaded %d NPCs, want 1", len(specs))
	}
	if specs[0].model != want {
		t.Errorf("spec.model = %q, want %q (configured model must thread through)", specs[0].model, want)
	}
}

// TestLoadSeededNPCs_ModelFallsBackToTenantConfig pins the LOAD-BEARING fallback
// (#227): a web-created Character NPC has NO LLMProviderConfigID, so its model
// must come from the tenant-level LLM provider_config the Configuration screen
// writes. Without this fallback the fix would miss the operator's own NPCs.
func TestLoadSeededNPCs_ModelFallsBackToTenantConfig(t *testing.T) {
	pool := startDB(t)
	ctx := context.Background()
	st := storage.New(pool)

	if err := SeedNPC(ctx, pool, testCipher(t), nil); err != nil {
		t.Fatalf("SeedNPC: %v", err)
	}
	const want = "tenant-fallback-model"
	if _, err := pool.Exec(ctx, `UPDATE provider_config SET model = $1 WHERE component = 'llm'`, want); err != nil {
		t.Fatalf("set tenant llm model: %v", err)
	}

	tenant, err := st.FindTenantByName(ctx, SeedTenantName)
	if err != nil {
		t.Fatalf("FindTenantByName: %v", err)
	}
	campaign, err := st.FindCampaignByName(ctx, tenant.ID, SeedCampaignName)
	if err != nil {
		t.Fatalf("FindCampaignByName: %v", err)
	}
	// A web-created NPC: bound to no LLM provider_config (LLMProviderConfigID
	// omitted → NULL), exactly what the Campaign editor writes.
	if _, err := st.CreateAgent(ctx, storage.NewAgent{
		CampaignID: campaign.ID,
		Role:       storage.AgentRoleCharacter,
		Name:       "WebNPC",
		Persona:    "You are a web-created NPC.",
	}); err != nil {
		t.Fatalf("CreateAgent (WebNPC): %v", err)
	}

	specs, _, _, err := loadSeededNPCs(ctx, st)
	if err != nil {
		t.Fatalf("loadSeededNPCs: %v", err)
	}
	var web *npcSpec
	for i := range specs {
		if specs[i].name == "WebNPC" {
			web = &specs[i]
		}
	}
	if web == nil {
		t.Fatal("WebNPC not among loaded specs")
	}
	if web.model != want {
		t.Errorf("web NPC model = %q, want tenant fallback %q", web.model, want)
	}
}

// TestLoadSeededNPCs_NoLLMConfigYieldsEmptyModel pins the empty-model edge: a
// campaign with no LLM provider_config at all leaves spec.model == "" so the
// adapter default applies (#227 AC2, no defaulting duplicated in wirenpc).
func TestLoadSeededNPCs_NoLLMConfigYieldsEmptyModel(t *testing.T) {
	pool := startDB(t)
	ctx := context.Background()
	st := storage.New(pool)

	// Build the demo tenant/campaign by hand WITHOUT any provider_config rows, so
	// both the bound-config and tenant-fallback lookups miss.
	tenantID, err := st.CreateTenant(ctx, SeedTenantName)
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	campaignID, err := st.CreateCampaign(ctx, storage.NewCampaign{
		TenantID: tenantID,
		Name:     SeedCampaignName,
		System:   "dnd5e",
		Language: "en",
	})
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	if _, err := st.CreateAgent(ctx, storage.NewAgent{
		CampaignID: campaignID,
		Role:       storage.AgentRoleCharacter,
		Name:       "Bart",
		Persona:    BartPersona,
	}); err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}

	specs, _, _, err := loadSeededNPCs(ctx, st)
	if err != nil {
		t.Fatalf("loadSeededNPCs: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("loaded %d NPCs, want 1", len(specs))
	}
	if specs[0].model != "" {
		t.Errorf("spec.model = %q, want empty (no config → adapter default)", specs[0].model)
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

// TestResolveNPCModel_BoundConfigOutranksTenantFallback pins the precedence
// pure-functionally (#227 review finding): a resolver that ignored the bound
// config and always returned the tenant fallback would pass the DB-backed
// tests, whose single provider_config row is simultaneously both sources.
func TestResolveNPCModel_BoundConfigOutranksTenantFallback(t *testing.T) {
	t.Parallel()
	bound := &storage.ProviderConfig{Model: "bound-model"}
	if got := resolveNPCModel(bound, "tenant-model"); got != "bound-model" {
		t.Errorf("bound + tenant = %q, want bound-model", got)
	}
	if got := resolveNPCModel(&storage.ProviderConfig{}, "tenant-model"); got != "tenant-model" {
		t.Errorf("empty bound model + tenant = %q, want tenant-model", got)
	}
	if got := resolveNPCModel(nil, "tenant-model"); got != "tenant-model" {
		t.Errorf("nil bound + tenant = %q, want tenant-model", got)
	}
	if got := resolveNPCModel(nil, ""); got != "" {
		t.Errorf("nil bound + no tenant = %q, want empty (adapter default)", got)
	}
}

// TestLoadCampaignNPCs_ScopesToBoundCampaign is #323 acceptance (a): the
// campaign-scoped loader voices the BOUND Active Campaign's roster and Language,
// not the hardcoded seed. Seed the demo (Glyphoxa Demo / The Prancing Pony /
// Bart), create a SECOND active campaign X (its own tenant) with a Character NPC
// Yara and a distinct Language, then load X — the result must contain Yara (not
// Bart) and carry X's Language.
func TestLoadCampaignNPCs_ScopesToBoundCampaign(t *testing.T) {
	pool := startDB(t)
	ctx := context.Background()
	st := storage.New(pool)

	// The seed roster is the trap: a campaign-blind loader would return Bart / "en".
	if err := SeedNPC(ctx, pool, testCipher(t), nil); err != nil {
		t.Fatalf("SeedNPC: %v", err)
	}

	tenantX, err := st.CreateTenant(ctx, "Operator X")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	campX, err := st.CreateCampaign(ctx, storage.NewCampaign{
		TenantID: tenantX,
		Name:     "Campaign X",
		System:   "dnd5e",
		Language: "fr", // distinct from the seed's "en" so a leak is visible
	})
	if err != nil {
		t.Fatalf("CreateCampaign (X): %v", err)
	}
	if _, err := st.CreateAgent(ctx, storage.NewAgent{
		CampaignID:  campX,
		Role:        storage.AgentRoleCharacter,
		Name:        "Yara",
		Persona:     "You are Yara, the seer of Campaign X.",
		AddressOnly: false,
		Aliases:     []string{"seer"},
	}); err != nil {
		t.Fatalf("CreateAgent (Yara): %v", err)
	}

	specs, _, loadedCampaign, err := loadCampaignNPCs(ctx, st, campX)
	if err != nil {
		t.Fatalf("loadCampaignNPCs(X): %v", err)
	}
	if loadedCampaign.Language != "fr" {
		t.Errorf("loaded campaign Language = %q, want X's %q", loadedCampaign.Language, "fr")
	}
	names := map[string]bool{}
	for _, s := range specs {
		names[s.name] = true
	}
	if !names["Yara"] {
		t.Errorf("loaded roster %v does not contain Yara — the bound campaign was ignored", names)
	}
	if names["Bart"] {
		t.Errorf("loaded roster %v contains the seed NPC Bart — the loader voiced the seed campaign", names)
	}
}

// TestLoadCampaignNPCs_FreshDBNoSeed is #323 acceptance (b): on a fresh, UNSEEDED
// install (the tenant is named "Glyphoxa", not "Glyphoxa Demo"), a session for a
// non-seed campaign loads THAT campaign's roster and must NOT hard-fail with
// "find tenant: not found" — the seed-name resolution that broke unseeded installs.
func TestLoadCampaignNPCs_FreshDBNoSeed(t *testing.T) {
	pool := startDB(t)
	ctx := context.Background()
	st := storage.New(pool)

	// A fresh install: a "Glyphoxa" tenant + one campaign, no seed. The seed-name
	// loader's FindTenantByName("Glyphoxa Demo") would ErrNotFound here.
	tenantID, err := st.CreateTenant(ctx, "Glyphoxa")
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	campID, err := st.CreateCampaign(ctx, storage.NewCampaign{
		TenantID: tenantID,
		Name:     "My First Campaign",
		System:   "dnd5e",
		Language: "en",
	})
	if err != nil {
		t.Fatalf("CreateCampaign: %v", err)
	}
	if _, err := st.CreateAgent(ctx, storage.NewAgent{
		CampaignID: campID,
		Role:       storage.AgentRoleCharacter,
		Name:       "Wren",
		Persona:    "You are Wren, the first NPC of a self-made campaign.",
	}); err != nil {
		t.Fatalf("CreateAgent (Wren): %v", err)
	}

	specs, _, loadedCampaign, err := loadCampaignNPCs(ctx, st, campID)
	if err != nil {
		t.Fatalf("loadCampaignNPCs on a fresh unseeded DB errored (must not): %v", err)
	}
	if loadedCampaign.ID != campID {
		t.Errorf("loaded campaign ID = %s, want %s", loadedCampaign.ID, campID)
	}
	if len(specs) != 1 || specs[0].name != "Wren" {
		t.Errorf("loaded roster = %+v, want the single Character NPC Wren", specs)
	}
}

// loadSeededNPCs resolves the demo seed Tenant/Campaign BY NAME and hydrates its
// roster. It is a TEST-ONLY helper (the runtime path is the campaign-scoped
// loadCampaignNPCs, #323): the seed constants live on the `glyphoxa seed` command
// and these tests only, never in the voice loop. Kept here so the many seed→load
// integration tests below read unchanged.
func loadSeededNPCs(ctx context.Context, st *storage.Store) ([]npcSpec, storage.LoadedAgent, storage.Campaign, error) {
	tenant, err := st.FindTenantByName(ctx, SeedTenantName)
	if err != nil {
		return nil, storage.LoadedAgent{}, storage.Campaign{}, err
	}
	campaign, err := st.FindCampaignByName(ctx, tenant.ID, SeedCampaignName)
	if err != nil {
		return nil, storage.LoadedAgent{}, storage.Campaign{}, err
	}
	return loadCampaignRoster(ctx, st, campaign)
}
