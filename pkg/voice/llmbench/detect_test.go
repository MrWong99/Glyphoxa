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
	a.Add(Findings{ToolMarkup: 1, Markdown: 3, GenderSlip: 1})
	if want := (Findings{ToolMarkup: 2, English: 2, Markdown: 3, GenderSlip: 1}); a != want {
		t.Errorf("Add = %+v, want %+v", a, want)
	}
	if a.Total() != 8 {
		t.Errorf("Total = %d, want 8", a.Total())
	}
}
