package wirenpc

import (
	"reflect"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestSessionSTTTerms_PriorityOrder: the names the recognizer is biased towards
// are listed by how much a mishearing costs (#701) — the Cast NPCs' names and
// aliases (a misheard NPC name is an utterance that routes nowhere), the Butler,
// then the player characters. The provider keeps the head of the list.
func TestSessionSTTTerms_PriorityOrder(t *testing.T) {
	npcs := []npcSpec{
		{name: "Flo", aliases: []string{"Florian"}},
		{name: "Mira", aliases: []string{"the bard", "Mirabel"}},
		{name: "Glyphoxa", role: string(storage.AgentRoleButler), addressOnly: true},
	}
	players := []string{"Artusas", "Art", "Kira"}

	got := sessionSTTTerms(npcs, players, "de")
	want := []string{"Flo", "Florian", "Mira", "the bard", "Mirabel", "Glyphoxa", "Artusas", "Art", "Kira"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("terms = %q\n want %q", got, want)
	}
}

// TestSessionSTTTerms_ButlerComesAfterEveryCastNPC, wherever the loader put it.
func TestSessionSTTTerms_ButlerComesAfterEveryCastNPC(t *testing.T) {
	npcs := []npcSpec{
		{name: "Glyphoxa", role: string(storage.AgentRoleButler)},
		{name: "Flo"},
	}
	if got, want := sessionSTTTerms(npcs, nil, "de"), []string{"Flo", "Glyphoxa"}; !reflect.DeepEqual(got, want) {
		t.Errorf("terms = %q, want %q", got, want)
	}
}

// TestSessionSTTTerms_SkipsEverydayWordsOfTheCampaignLanguage: a keyterm rewrites
// its homophones to itself, so a name that is also an ordinary word would turn
// every use of that word into the spelling that routes as an exact name. Such a
// name is left to be recognised on its own merits.
func TestSessionSTTTerms_SkipsEverydayWordsOfTheCampaignLanguage(t *testing.T) {
	npcs := []npcSpec{
		{name: "Wolf", aliases: []string{"Karte", "Wolfram"}},
		{name: "Great"}, // an everyday word in English, a name in a German campaign
	}
	players := []string{"Rolle", "Der Wolf"}

	got := sessionSTTTerms(npcs, players, "de")
	want := []string{"Wolfram", "Great", "Der Wolf"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("terms = %q, want %q", got, want)
	}

	if got, want := sessionSTTTerms(npcs[:1], nil, "en"), []string{"Karte", "Wolfram"}; !reflect.DeepEqual(got, want) {
		t.Errorf("English campaign: terms = %q, want %q", got, want)
	}
}

// TestSessionSTTTerms_DropsBlanks: an empty alias is not a term.
func TestSessionSTTTerms_DropsBlanks(t *testing.T) {
	got := sessionSTTTerms([]npcSpec{{name: " Flo ", aliases: []string{"", "  "}}}, []string{""}, "")
	if want := []string{"Flo"}; !reflect.DeepEqual(got, want) {
		t.Errorf("terms = %q, want %q", got, want)
	}
}

// TestPlayerCharacterTerms_CarriesAliases: a player character's aliases are as
// much a name said at the table as its name, so both are offered — name first.
func TestPlayerCharacterTerms_CarriesAliases(t *testing.T) {
	chars := []storage.Character{
		{Name: "Artusas", Aliases: []string{"Art", "the paladin"}},
		{Name: "Kira"},
	}
	if got, want := playerCharacterTerms(chars), []string{"Artusas", "Art", "the paladin", "Kira"}; !reflect.DeepEqual(got, want) {
		t.Errorf("terms = %q, want %q", got, want)
	}
}
