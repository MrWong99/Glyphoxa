package wirenpc

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// TestSessionSTTTerms_PriorityOrder: the names the recognizer is biased towards
// are listed by how much a mishearing costs (#701). A misheard NPC or Butler
// NAME is an utterance that routes nowhere, so every one of those comes before
// any alias; the player characters, whose names route nothing, come last. The
// provider keeps the head of the list.
func TestSessionSTTTerms_PriorityOrder(t *testing.T) {
	npcs := []npcSpec{
		{name: "Flo", aliases: []string{"Florian"}},
		{name: "Glyphoxa", role: string(storage.AgentRoleButler), addressOnly: true, aliases: []string{"Glyph"}},
		{name: "Mira", aliases: []string{"the bard", "Mirabel"}},
	}
	players := []storage.Character{
		{Name: "Artusas", Aliases: []string{"Art", "the paladin"}},
		{Name: "Kira"},
	}

	got := sessionSTTTerms(npcs, players, "de")
	want := []string{
		"Flo", "Mira", "Glyphoxa", // the names that route: Cast, then Butler
		"Florian", "the bard", "Mirabel", "Glyph", // their aliases, same order
		"Artusas", "Kira", // player-character names
		"Art", "the paladin", // and aliases
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("terms = %q\n want %q", got, want)
	}
}

// TestResolveSTTHints_ABigCastKeepsEveryNameThatRoutes: at the provider's 50-term
// cap it is aliases that are cut, never the name of an NPC late in the roster or
// the Butler's (#701 review: 25 NPCs with two aliases each used to push NPCs
// 17..25 and the Butler off the list while the first NPC's third spelling stayed).
func TestResolveSTTHints_ABigCastKeepsEveryNameThatRoutes(t *testing.T) {
	var npcs []npcSpec
	var names []string
	for i := range 25 {
		name := fmt.Sprintf("Npc%02d", i)
		names = append(names, name)
		npcs = append(npcs, npcSpec{name: name, aliases: []string{name + "a", name + "b"}})
	}
	npcs = append(npcs, npcSpec{name: "Glyphoxa", role: string(storage.AgentRoleButler)})
	names = append(names, "Glyphoxa")

	hints, dropped := resolveSTTHints(Config{STTKeyterms: true, npcs: npcs,
		players: []storage.Character{{Name: "Artusas"}}})
	if len(hints.terms) != 50 {
		t.Fatalf("sent %d terms, want the provider's cap of 50", len(hints.terms))
	}
	if got := hints.terms[:len(names)]; !reflect.DeepEqual(got, names) {
		t.Errorf("the head of the list must be every NPC name and the Butler's, got %q", got)
	}
	if want := 26 + 50 + 1 - 50; dropped != want {
		t.Errorf("dropped = %d, want %d", dropped, want)
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
	players := []storage.Character{{Name: "Rolle", Aliases: []string{"Der Wolf"}}}

	got := sessionSTTTerms(npcs, players, "de")
	want := []string{"Great", "Wolfram", "Der Wolf"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("terms = %q, want %q", got, want)
	}

	if got, want := sessionSTTTerms(npcs[:1], nil, "en"), []string{"Karte", "Wolfram"}; !reflect.DeepEqual(got, want) {
		t.Errorf("English campaign: terms = %q, want %q", got, want)
	}
}

// TestSessionSTTTerms_DropsBlanks: an empty alias is not a term.
func TestSessionSTTTerms_DropsBlanks(t *testing.T) {
	got := sessionSTTTerms([]npcSpec{{name: " Flo ", aliases: []string{"", "  "}}}, []storage.Character{{Name: ""}}, "")
	if want := []string{"Flo"}; !reflect.DeepEqual(got, want) {
		t.Errorf("terms = %q, want %q", got, want)
	}
}

// TestSTTLanguageCode: the recognizer takes a bare primary subtag as
// language_code, whatever shape the Campaign Language was saved in.
func TestSTTLanguageCode(t *testing.T) {
	for in, want := range map[string]string{
		"de":    "de",
		"de-DE": "de",
		"DE_de": "de",
		" en ":  "en",
		"en-US": "en",
		"":      "",
		"   ":   "",
	} {
		if got := sttLanguageCode(in); got != want {
			t.Errorf("sttLanguageCode(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestResolveSTTHints_LanguageHintIsThePrimarySubtag: a campaign saved as
// "de-DE" pins the recognizer to "de" — a regional tag is not sent.
func TestResolveSTTHints_LanguageHintIsThePrimarySubtag(t *testing.T) {
	hints, _ := resolveSTTHints(Config{STTLanguageHint: true, language: "de-DE"})
	if hints.language != "de" {
		t.Errorf("language hint = %q, want de", hints.language)
	}
}
