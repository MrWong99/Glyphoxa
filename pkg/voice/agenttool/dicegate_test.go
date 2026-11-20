package agenttool

import (
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
)

func TestNeedsDice(t *testing.T) {
	cases := []struct {
		name string
		lang string
		text string
		want bool
	}{
		// --- English: explicit die notation. ---
		{"en d20", "en", "Roll a d20 for me.", true},
		{"en NdM", "en", "I attack with 2d6 damage.", true},
		{"en d100", "en", "Give me a d100.", true},
		{"en d-percent", "en", "Roll d% for the table.", true},
		// Notation-only rows with NO keyword: prove the '%' branch bites on its own,
		// not via "roll" — the trailing-\b bug that made "d%"/"w%" dead notation.
		{"en d-percent notation only", "en", "Give me a d% please.", true},
		{"en w-percent notation only", "en", "hand me a w%", true},
		{"en bare die word", "en", "Throw the dice.", true},

		// --- English: ttrpg roll intent without explicit notation. ---
		{"en saving throw", "en", "Make a saving throw against the poison.", true},
		{"en initiative", "en", "Everyone roll initiative.", true},
		{"en check", "en", "Give me an ability check.", true},
		{"en to hit", "en", "What's my to hit?", true},
		// Inflections stay armed when they read as a roll; "rolling" as an
		// everyday adjective/verb no longer does (O5).
		{"en rolls", "en", "Everyone rolls initiative.", true},
		{"en rolling for", "en", "I'm rolling for stealth.", true},
		{"en rolling eyes", "en", "He keeps rolling his eyes.", false},

		// --- English: common named-check phrasings (#438) — must arm dice. ---
		{"en perception check", "en", "Make a perception check.", true},
		{"en insight check", "en", "Give me an insight check on the merchant.", true},
		{"en investigation check", "en", "I'd like an investigation check on the desk.", true},
		{"en wisdom saving throw", "en", "Make a wisdom saving throw.", true},

		// --- English: plain conversation — must NOT arm dice. ---
		{"en room price", "en", "How much for a room and a pint?", false},
		{"en greeting", "en", "Hello Bart, how are you?", false},
		{"en rumors", "en", "Heard any good rumors lately?", false},

		// --- English: false-positive guards. ---
		{"en model not d-something", "en", "Tell me about your business model.", false},
		{"en add not a die", "en", "Can you add another log to the fire?", false},
		{"en addled", "en", "The traveler looks addled.", false},
		// \b-anchoring: "die" substring inside "studied" no longer trips (#226).
		{"en studied not die", "en", "She studied the map.", false},
		{"en empty", "en", "", false},

		// --- German: the live failure — must arm dice (#226). ---
		{"de Würfelwerkzeug", "de", "Bart, benutze dein Würfelwerkzeug und würfle zwei sechsseitige Würfel.", true},
		// German article „die" must NOT trip the gate (#226).
		{"de article die", "de", "Erzähl mir die Geschichte von diesem Ort.", false},
		{"de w20 notation", "de", "würfle zwei w20", true},
		{"de bare notation", "de", "nimm zwei w20", true},
		{"de Probe", "de", "mach eine Probe", true},
		// German named checks are compounds on -probe, so the bare "probe" substring
		// covers them all (#438) — pinned here so a keyword-set edit cannot drop them.
		{"de Wahrnehmungsprobe", "de", "Mach eine Wahrnehmungsprobe.", true},
		{"de Motiv-erkennen Probe", "de", "Würfle eine Probe auf Motiv erkennen.", true},
		{"de Nachforschungen-Probe", "de", "Eine Nachforschungsprobe, bitte.", true},
		{"de Rettungswurf", "de", "mach einen Rettungswurf gegen das Gift", true},
		{"de Weisheitsrettungswurf", "de", "Mach einen Weisheitsrettungswurf.", true},
		{"de Initiative", "de", "alle würfeln Initiative", true},
		// werfen ("throw a die") verb family — imperative „wirf", „werfen" (#226).
		{"de wirf imperative", "de", "Wirf noch einmal!", true},
		{"de werfen", "de", "Kannst du nochmal werfen?", true},
		// d-percent + notation-only in German (no keyword): '%' branch bites.
		{"de d-percent notation only", "de", "gib mir ein d%", true},
		// German plain conversation — must NOT arm dice.
		{"de greeting", "de", "Hallo Bart, wie geht es dir?", false},
		{"de room", "de", "Was kostet ein Zimmer für die Nacht?", false},

		// --- Cross-language notation trips in any language (AC). ---
		{"de NdM notation", "de", "2d6 Schaden", true},
		{"en wN notation", "en", "roll 2w6", true},

		// --- Unknown/empty language behaves as en. ---
		{"unknown lang en keyword", "fr", "Roll a d20.", true},
		{"unknown lang de keyword ignored", "fr", "würfle zwei Würfel", false},
		{"empty lang en keyword", "", "Roll a d20.", true},
		{"empty lang notation", "", "2d6", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := needsDice(tc.lang, []llm.Message{{Role: llm.RoleUser, Text: tc.text}})
			if got != tc.want {
				t.Errorf("needsDice(%q, %q) = %v, want %v", tc.lang, tc.text, got, tc.want)
			}
		})
	}
}

// TestNeedsDice_Corpus is O5: the gate used to arm on bare substrings — and on
// the speaker-name prefix Hot Context puts on every user line — so everyday
// German/English talk buffered the whole turn (no streaming) and armed the
// invented-roll guard. Positive and negative utterances, each as the agent
// actually sees it ("<Name>: <text>").
func TestNeedsDice_Corpus(t *testing.T) {
	cases := []struct {
		lang, text string
		want       bool
	}{
		// Must arm.
		{"de", "Würfle einen W20", true},
		{"de", "Würfel mal für mich", true},
		{"de", "Mach eine Probe auf Wahrnehmung", true},
		{"de", "Mach mal eine Probe", true},
		{"de", "Leg eine Probe auf Heimlichkeit ab", true},
		{"de", "Eine Wahrnehmungsprobe, bitte", true},
		{"de", "Mach einen Rettungswurf gegen Gift", true},
		{"de", "Ein Angriffswurf gegen den Ork", true},
		{"de", "Guter Wurf! Nochmal.", true},
		{"de", "Alle würfeln Initiative", true},
		{"de", "Wirf noch einmal!", true},
		{"de", "Wirf die Würfel", true},
		{"de", "Kannst du nochmal werfen?", true},
		{"en", "roll for initiative", true},
		{"en", "I attack with advantage", true},
		{"en", "Roll it at disadvantage", true},
		{"en", "Throw the die for me", true},
		{"en", "Make a stealth check", true},
		{"en", "Can you roll?", true},
		// Review of #674: these must arm.
		{"de", "Mach einen Stärkewurf", true},
		{"de", "Weisheitswurf", true},
		{"de", "Wahrnehmungswurf", true},
		{"de", "Geschicklichkeitswurf", true},
		{"de", "Attributswurf", true},
		{"de", "Probewurf", true},
		{"de", "Wirf deinen Angriff", true},
		{"de", "Wirf deinen Schaden", true},
		{"de", "Wirf das nochmal", true},
		{"de", "Wirf einen für mich", true},
		{"de", "Wirf einen Zwanziger", true},
		{"de", "Wirf mir bitte auf Wahrnehmung", true},
		{"de", "Wirf einen Wahrnehmungscheck", true},
		{"de", "Ich möchte eine Probe ablegen", true},
		{"de", "Das erfordert eine Probe", true},
		{"de", "Brauche ich dafür eine Probe?", true},
		{"de", "Mach doch bitte mal eine Probe", true},
		{"en", "I'm rolling stealth", true},
		{"en", "Rolling perception", true},
		{"en", "Are you rolling yet?", true},
		{"en", "Okay Bart, roll for me: stealth", true},
		// Second review: werfen arms unless something is thrown somewhere.
		{"de", "Wirf Wahrnehmung für mich", true},
		{"de", "Wirf Stärke", true},
		{"de", "Wirf nochmal, Flo", true},
		{"de", "Wirf bitte für mich, Flo!", true},
		{"de", "Wirf für Gandalf", true},
		{"de", "Wirf mal für den Zwerg", true},
		{"de", "Werft Ihr bitte nochmal", true},
		{"de", "Werfen Sie bitte noch einmal", true},
		{"de", "Wirf einen Geschicklichkeitstest", true},

		// Must NOT arm.
		{"de", "Wirf mir den Schlüssel rüber", false},
		{"de", "Wirf mal den Schluessel rueber", false},
		{"de", "Was ist dein Vorwurf gegen mich?", false},
		{"de", "Darf ich eine Probe von deinem Wein haben?", false},
		{"de", "Zeit für eine Kostprobe", false},
		{"de", "Ich unterwerfe mich nicht", false},
		{"de", "Zeig mir den Entwurf", false},
		{"de", "Ein Maulwurf gräbt im Garten", false},
		{"de", "Deine Vorwürfe sind unfair", false},
		{"de", "Wirf ihn ins Feuer", false},
		{"de", "Wirf einen Blick auf die Karte", false},
		{"de", "Stell mich ruhig auf die Probe", false},
		{"de", "Der Rauswurf", false},
		{"de", "Sei nicht so unterwürfig", false},
		{"de", "Wir haben morgen Probe mit der Kapelle", false},
		{"de", "Der Gesetzentwurf", false},
		{"de", "Die Wache sieht müde aus", false},
		{"en", "Did the old king die here?", false},
		{"en", "I would rather diet than eat that", false},
		{"en", "Take advantage of the rolling hills", false},
		{"en", "What an advantage that would be", false},
		{"nl", "Wie is die man daar?", false},
	}
	for _, speaker := range []string{"", "Dieter: ", "Lena: ", "Wurf Würfelmann: "} {
		for _, tc := range cases {
			text := speaker + tc.text
			got := needsDice(tc.lang, []llm.Message{{Role: llm.RoleUser, Text: text}})
			if got != tc.want {
				t.Errorf("needsDice(%q, %q) = %v, want %v", tc.lang, text, got, tc.want)
			}
		}
	}
}

// TestStripSpeakerPrefix: the "<Name>: " attribution is removed, but content
// that merely contains ": " is never eaten.
func TestStripSpeakerPrefix(t *testing.T) {
	for in, want := range map[string]string{
		"Dieter: roll":                    "roll",
		"luk: roll":                       "roll",
		"Player / DM: roll":               "roll",
		"Sir Gregor von Hohenfels: roll":  "roll",
		"Okay Bart, roll for me: stealth": "Okay Bart, roll for me: stealth",
		"Okay roll for me: stealth":       "Okay roll for me: stealth",
		"no prefix here":                  "no prefix here",
	} {
		if got := stripSpeakerPrefix(in); got != want {
			t.Errorf("stripSpeakerPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestNeedsDice_LatestUserOnly proves the gate inspects only the most recent
// user message, not the whole history: an old dice turn must not arm a later
// plain turn, and the system prompt is ignored.
func TestNeedsDice_LatestUserOnly(t *testing.T) {
	msgs := []llm.Message{
		{Role: llm.RoleSystem, Text: "You are Bart. Roll dice when asked."}, // system mentions dice — ignored
		{Role: llm.RoleUser, Text: "Roll a d20."},                           // older user turn
		{Role: llm.RoleAssistant, Text: "You rolled a 14."},
		{Role: llm.RoleUser, Text: "Thanks, where's the bar?"}, // current: plain
	}
	if needsDice("en", msgs) {
		t.Error("needsDice must key on the latest user message (plain), not history or the system prompt")
	}
}

// TestGateLanguage covers the language-subtag normalization the gate applies
// before selecting a keyword table: a known primary subtag is kept, a region
// tag is stripped, and anything without a registered table degrades to "en"
// (mirroring the address matcher's fallback, ADR-0024).
func TestGateLanguage(t *testing.T) {
	cases := map[string]string{
		"de":    "de",
		"de-DE": "de",
		"DE":    "de",
		"en":    "en",
		"en-US": "en",
		"":      "en",
		"fr":    "en",
		"xx":    "en",
	}
	for in, want := range cases {
		if got := gateLanguage(in); got != want {
			t.Errorf("gateLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}
