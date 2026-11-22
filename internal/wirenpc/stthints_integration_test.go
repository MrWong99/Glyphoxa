//go:build integration

// The DB half of the #701 wiring proof, against a real Postgres (testcontainers):
// a Campaign's NPCs and player characters, as saved, are what the session's first
// cycle is told to bias the recognizer towards. Tag-isolated behind `integration`
// (ADR-0033); the container harness (startDB, testCipher) lives in
// agentspec_test.go and the seed in credentials_integration_test.go.
package wirenpc

import (
	"reflect"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestRunFromDB_CampaignNamesReachTheSessionsKeyterms drives the PRODUCTION entry
// point up to its first connect-and-serve cycle. The saved NPC's name, the
// campaign's Butler and a saved player character's name AND alias must be on the
// keyterm list that cycle gets —
// and the character's name on the prompt's player list, which rides the same
// load. A RunFromDB that stopped handing the loaded characters on would otherwise
// leave every player name unbiased with the whole suite green.
func TestRunFromDB_CampaignNamesReachTheSessionsKeyterms(t *testing.T) {
	pool := startDB(t)
	st := storage.New(pool)
	cipher := testCipher(t)
	_, campaignID, _ := seedRealKeyNPC(t, t.Context(), st, cipher)
	if _, err := st.CreateCharacter(t.Context(), storage.NewCharacter{
		CampaignID:    campaignID,
		Name:          "Artusas",
		Aliases:       []string{"Artie"},
		DiscordUserID: "100000000000000001",
	}); err != nil {
		t.Fatalf("CreateCharacter: %v", err)
	}

	ctx, served := captureFirstCycle(t)
	err := RunFromDB(ctx, Config{CampaignID: campaignID, Guild: "1", Channel: "2", STTKeyterms: true}, pool, cipher)
	if err != nil {
		t.Fatalf("RunFromDB: %v", err)
	}
	cfg, ok := served()
	if !ok {
		t.Fatal("RunFromDB never reached a connect-and-serve cycle")
	}
	if got, want := cfg.sttHints.terms, []string{"Bart", "Glyphoxa", "Artusas", "Artie"}; !reflect.DeepEqual(got, want) {
		t.Errorf("the session's keyterms = %q, want %q", got, want)
	}
	if got, want := cfg.playerCharacters, []string{"Artusas"}; !reflect.DeepEqual(got, want) {
		t.Errorf("the session's player-character names = %q, want %q", got, want)
	}
}
