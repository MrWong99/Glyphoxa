package llmbench

import "testing"

// lt keeps a literal tool-call element out of this file.
const lt = "<"

func TestDetect_CleanGermanReplyHasNoFindings(t *testing.T) {
	for _, text := range []string{
		"Na, Mira, was darf's sein? Der Eintopf ist frisch, und ein Zimmer kostet zwei Silber die Nacht.",
		"[chuckles] Eine Siebzehn! Das Glück ist heute auf deiner Seite, meine Liebe.",
		"Gellan Primwasser? Der war letzte Woche hier und hat nach dem alten Stollen gefragt.",
		"Du hast eine 4 und eine 6 gewürfelt, macht zusammen 10.",
	} {
		if f := Detect(text, DetectOptions{NPCName: "Flo", FemaleSpeaker: true}); f != (Findings{}) {
			t.Errorf("Detect(%q) = %+v, want no findings", text, f)
		}
	}
}

func TestDetect_ToolMarkup(t *testing.T) {
	for _, text := range []string{
		`Moment. ` + lt + `function=dice>{"sides":20}` + lt + `/function>`,
		"Moment. " + lt + `invoke name="dice">` + lt + "/invoke>",
		lt + "thinking>Sie will würfeln." + lt + "/thinking> Gern.",
		lt + "tool_call>{}" + lt + "/tool_call>",
		"Hier: " + lt + "result>17" + lt + "/result>", // an element the scrubber does not know
		`{"name": "dice", "arguments": {"count": 2, "sides": 6}}`,
		"Das kann ich nicht für die Chronik erledigen.[/final]", // a chat-template control token
		"Gern." + lt + "|end|>",
	} {
		if f := Detect(text, DetectOptions{NPCName: "Flo"}); f.ToolMarkup == 0 {
			t.Errorf("Detect(%q).ToolMarkup = 0, want the leak found", text)
		}
	}
}

func TestDetect_StageDirections(t *testing.T) {
	for _, text := range []string{
		"*wischt sich die Hände an der Schürze ab* Was darf's sein?",
		"Flo lacht laut auf und wischt sich die Hände an der Schürze ab.",
		"Na klar. Flo wirft zwei sechsseitige Würfel.",
		"(lacht) Das wüsstest du wohl gern.",
	} {
		if f := Detect(text, DetectOptions{NPCName: "Flo"}); f.StageDirections == 0 {
			t.Errorf("Detect(%q).StageDirections = 0, want the narrated action found", text)
		}
	}
	// A name in direct address or a self-introduction is not narration.
	for _, text := range []string{"Ich bin Flo, die Wirtin.", "Flo heiße ich, und das hier ist mein Haus."} {
		if f := Detect(text, DetectOptions{NPCName: "Flo"}); f.StageDirections != 0 {
			t.Errorf("Detect(%q).StageDirections = %d, want 0", text, f.StageDirections)
		}
	}
}

// TestDetect_UnknownTags pins the bracket check (#702 review): the TTS prompt
// offers a fixed tag vocabulary and warns that any other bracket may be read
// aloud. The models moved their stage directions into brackets — these are
// replies from the bench's own passes — and a detector that waved every
// "[...]" through as an audio tag reported none of them.
func TestDetect_UnknownTags(t *testing.T) {
	for text, want := range map[string]int{
		"[leans on the bar] Na, was darf's sein?":                1,
		"[nods approvingly] Gut gemacht. [scratches head] Hm.":   2,
		"[smiles] Schön, dass ihr da seid. [softly] Setzt euch.": 2,
		"[shrugs in voice] Keine Ahnung.":                        1,
		"[schmunzelt] Das wüsstest du wohl gern.":                1,
		"[singing, drawn out] Oh du schöne Gans.":                1,
		"[thoughtful] Lass mich überlegen.":                      1,
		"[chuckles] Eine Siebzehn! [long pause] Nicht schlecht.": 0,
		"[Pause] Na gut. [ clears throat ] Also.":                0, // case and padding are not a new tag
		"[cheerfully] Hallo![/cheerfully]":                       0, // a closing tag is counted as tool markup
		"Der Preis steht an der Tafel, zwei Silber [pro Nacht].": 1,
		// A stage direction longer than 40 characters is still one (#702
		// review: the span was capped at 40, so a long action slipped through).
		"[leans heavily on the polished oak bar and sighs] Na, was darf's sein?":                                            1,
		"[wipes a tankard with a rag, glances at the door, then leans in close to the table and lowers her voice] Hört zu.": 1,
		"Kein einziger Tag hier, nur Worte.": 0,
	} {
		if f := Detect(text, DetectOptions{NPCName: "Flo"}); f.UnknownTags != want {
			t.Errorf("Detect(%q).UnknownTags = %d, want %d", text, f.UnknownTags, want)
		}
	}
	// The closing tag is still a finding, in its own column.
	if f := Detect("[cheerfully] Hallo![/cheerfully]", DetectOptions{}); f.ToolMarkup != 1 {
		t.Errorf("closing tag: ToolMarkup = %d, want 1", f.ToolMarkup)
	}
}

func TestDetect_Markdown(t *testing.T) {
	for _, text := range []string{
		"Das ist **wichtig**, hör zu.",
		"Wir haben:\n- Eintopf\n- Brot",
		"# Speisekarte\nEintopf.",
		"Sag einfach `würfeln`.",
	} {
		if f := Detect(text, DetectOptions{NPCName: "Flo"}); f.Markdown == 0 {
			t.Errorf("Detect(%q).Markdown = 0, want markdown found", text)
		}
	}
}

func TestDetect_EnglishLeakage(t *testing.T) {
	for _, text := range []string{
		"I need to roll two six-sided dice for Flo.",
		"Brak wants a d20 roll and info on Gellan. I'll roll and look up both in parallel. Eine Zwölf!",
	} {
		if f := Detect(text, DetectOptions{NPCName: "Flo"}); f.English == 0 {
			t.Errorf("Detect(%q).English = 0, want the English sentence found", text)
		}
	}
	// A German reply with an English loanword or an audio tag is not leakage.
	for _, text := range []string{
		"[laughs] Das ist ja ein cooler Trick, den du da hast!",
		"Okay, okay, ich hol dir das Bier.",
	} {
		if f := Detect(text, DetectOptions{NPCName: "Flo"}); f.English != 0 {
			t.Errorf("Detect(%q).English = %d, want 0", text, f.English)
		}
	}
}

func TestDetect_ToolNarration(t *testing.T) {
	for _, text := range []string{
		"Das Ergebnis kommt aus dem Werkzeug.",
		"Also dice mit count 2 und sides 6.",
		"Ich schaue kurz in meiner Datenbank nach.",
		"Dafür habe ich leider kein Tool.",
	} {
		if f := Detect(text, DetectOptions{NPCName: "Flo"}); f.ToolNarration == 0 {
			t.Errorf("Detect(%q).ToolNarration = 0, want the tool talk found", text)
		}
	}
}

func TestDetect_GenderSlipOnlyForAFemaleSpeaker(t *testing.T) {
	text := "Na, mein Lieber, was darf's denn sein?"
	if f := Detect(text, DetectOptions{NPCName: "Flo", FemaleSpeaker: true}); f.GenderSlip != 1 {
		t.Errorf("female speaker: GenderSlip = %d, want 1", f.GenderSlip)
	}
	if f := Detect(text, DetectOptions{NPCName: "Flo"}); f.GenderSlip != 0 {
		t.Errorf("male speaker: GenderSlip = %d, want 0", f.GenderSlip)
	}
	if f := Detect("Na, meine Liebe, was darf's sein?", DetectOptions{NPCName: "Flo", FemaleSpeaker: true}); f.GenderSlip != 0 {
		t.Errorf("feminine address flagged: %+v", f)
	}
}

func TestFindings_AddAndTotal(t *testing.T) {
	a := Findings{ToolMarkup: 1, English: 2}
	a.Add(Findings{ToolMarkup: 1, Markdown: 3, GenderSlip: 1, UnknownTags: 2})
	if want := (Findings{ToolMarkup: 2, English: 2, Markdown: 3, GenderSlip: 1, UnknownTags: 2}); a != want {
		t.Errorf("Add = %+v, want %+v", a, want)
	}
	if a.Total() != 10 {
		t.Errorf("Total = %d, want 10", a.Total())
	}
}
