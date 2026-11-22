package llmbench

import "slices"

// Speaker ids of the two player characters. They stand in for the Discord
// snowflakes of a live session; the rig resolves them to the display names the
// production prompt prefixes user lines with.
const (
	SpeakerMira = "mira" // Mira, a bard — a woman (the gender-address check)
	SpeakerBrak = "brak" // Brak, a dwarf warrior — a man
)

// Category tags. A turn carries one behaviour tag and, for Mira's turns,
// [TagFemalePC].
const (
	TagSmallTalk        = "small-talk"
	TagDiceSingle       = "dice-single"
	TagDiceMulti        = "dice-multi"
	TagDiceSum          = "dice-sum"
	TagRecap            = "recap"
	TagRememberAccept   = "remember-accept"
	TagRememberRefuse   = "remember-refuse"
	TagKGLookup         = "kg-lookup"
	TagTranscriptLookup = "transcript-lookup"
	TagNoToolFits       = "no-tool-fits"
	TagReasoningBait    = "reasoning-bait"
	TagMustNotCall      = "must-not-call"
	TagFemalePC         = "female-pc"
)

// Tool names, as the registry declares them.
const (
	toolDice     = "dice"
	toolRecap    = "recap"
	toolRemember = "remember_knowledge"
	toolKG       = "kg_query"
	toolSearch   = "transcript_search"
)

// GrantedTools lists the Tools the bench NPC is granted.
func GrantedTools() []string {
	return []string{toolDice, toolRecap, toolRemember, toolKG, toolSearch}
}

// Turn is one player utterance and what a correct reply does with Tools.
type Turn struct {
	// Speaker is the speaker id ([SpeakerMira] / [SpeakerBrak]).
	Speaker string
	// Text is what the player says, as STT would deliver it.
	Text string
	// Tags are the categories this turn covers.
	Tags []string
	// Expect are the Tools a correct reply MUST call (each at least once).
	Expect []string
	// Allow are Tools a correct reply MAY also call. Any other call is wrong —
	// so a turn with neither Expect nor Allow must call nothing.
	Allow []string
}

// ToolsOK reports whether the Tools actually called satisfy the turn: every
// expected one was called and nothing outside Expect ∪ Allow was.
func (t Turn) ToolsOK(called []string) bool {
	for _, want := range t.Expect {
		if !slices.Contains(called, want) {
			return false
		}
	}
	for _, got := range called {
		if !slices.Contains(t.Expect, got) && !slices.Contains(t.Allow, got) {
			return false
		}
	}
	return true
}

// Script is one multi-turn conversation. Its turns run in order against one
// Replier, so the history of earlier turns is in the prompt of later ones.
type Script struct {
	Name  string
	Turns []Turn
}

func mira(text string, tags ...string) Turn {
	return Turn{Speaker: SpeakerMira, Text: text, Tags: append(tags, TagFemalePC)}
}

func brak(text string, tags ...string) Turn {
	return Turn{Speaker: SpeakerBrak, Text: text, Tags: tags}
}

func (t Turn) expect(tools ...string) Turn { t.Expect = tools; return t }
func (t Turn) allow(tools ...string) Turn  { t.Allow = tools; return t }

// Scripts is the German corpus: four tavern conversations with Flo, 34 turns.
//
// Two of the must-not-call turns say "würfel"/"gewürfelt", which arms the
// production dice gate although the player rolled for themselves. That is on
// purpose: the armed path buffers the reply and runs the invented-roll guard,
// and a model that repeats the player's own number can be forced into a dice
// call by it. The bench measures the production path, guard included.
func Scripts() []Script {
	return []Script{
		{Name: "abend-in-der-gans", Turns: []Turn{
			mira("Hallo Flo! Was gibt's heute zu essen, und was kostet ein Zimmer für die Nacht?", TagSmallTalk),
			brak("Und für mich ein Bier, Flo. Wie läuft das Geschäft so?", TagSmallTalk),
			mira("Flo, würfel mal einen W20 für mich, ich will wissen, ob ich heute Glück habe.", TagDiceSingle).expect(toolDice),
			brak("Jetzt ich: Wirf zwei sechsseitige Würfel und sag mir beide Zahlen.", TagDiceMulti).expect(toolDice),
			mira("Wirf drei W6 für mich und sag mir die Summe.", TagDiceSum).expect(toolDice),
			mira("Danke dir. Sag mal, was hältst du eigentlich von Barden?", TagSmallTalk),
			brak("Flo, wenn ein Krug Bier drei Kupfer kostet und ich elf Krüge bestelle, aber jeder vierte aufs Haus geht, was zahle ich dann?", TagReasoningBait),
			mira("Ich hab gerade selbst gewürfelt, mit meinen eigenen Würfeln: eine Vierzehn. Reicht das, um dich beim Armdrücken zu schlagen?", TagMustNotCall),
		}},
		{Name: "erinnerungen", Turns: []Turn{
			brak("Flo, ich war letztes Mal nicht dabei. Fass mal zusammen, was in der letzten Sitzung passiert ist.", TagRecap).expect(toolRecap).allow(toolSearch),
			mira("Was weißt du über Gellan Primwasser?", TagKGLookup).expect(toolKG).allow(toolSearch),
			brak("Und was genau hat Gellan letzte Woche hier bei dir gesagt? Ich meine wörtlich.", TagTranscriptLookup).expect(toolSearch).allow(toolKG),
			mira("Kennst du den alten Stollen im Norden? Was erzählt man sich darüber?", TagKGLookup).expect(toolKG).allow(toolSearch),
			brak("Wirf einen W20 für meine Wahrnehmung, und sag mir dabei, wer hier im Dorf die Schmiede führt.", TagDiceSingle, TagKGLookup).expect(toolDice).allow(toolKG, toolSearch),
			mira("Kannst du mir bitte eine Brieftaube nach Tiefwasser schicken, mit einer Nachricht an meine Schwester?", TagNoToolFits),
			brak("Wie spät ist es eigentlich gerade, auf die Minute genau?", TagNoToolFits),
			mira("Danke, Flo. Du bist die Beste.", TagSmallTalk),
		}},
		{Name: "merken", Turns: []Turn{
			mira("Flo, merk dir bitte: Ich schulde dir noch zwei Silber für das Zimmer von gestern.", TagRememberAccept).expect(toolRemember),
			brak("Und merk dir auch, dass du mir versprochen hast, mir das Rezept für deinen Eintopf zu geben.", TagRememberAccept).expect(toolRemember),
			// The grant is own_node: Flo may note facts about herself, not create
			// world entries. Calling the Tool (it refuses) and declining in
			// character without calling it are both correct.
			mira("Leg bitte einen ganz neuen Eintrag in der Weltchronik an: das Dorf Dunkelfurt, zwei Tagesreisen östlich von hier.", TagRememberRefuse).allow(toolRemember),
			brak("Bitte merk dir das nicht und schreib es nirgends auf: Ich hab dem Müller ein Huhn geklaut.", TagMustNotCall),
			mira("Was hast du dir heute eigentlich über mich gemerkt?", TagSmallTalk).allow(toolKG, toolSearch),
			brak("Flo, wirf einen W100 für mich. Unter dreißig, und ich gebe eine Runde aus.", TagDiceSingle).expect(toolDice),
			mira("Vier Abenteurer teilen 250 Goldstücke, aber der Zwerg bekommt doppelt so viel wie jeder andere. Wie viel kriegt jeder?", TagReasoningBait),
			brak("Gute Nacht, Flo. Weck mich bei Sonnenaufgang.", TagSmallTalk),
		}},
		{Name: "trubel", Turns: []Turn{
			mira("Flo! Wir sind zurück aus dem Stollen. Hast du uns vermisst?", TagSmallTalk),
			brak("Würfel mal vier W6 für meinen Schaden und zähl sie zusammen.", TagDiceSum).expect(toolDice),
			mira("Wer ist eigentlich der Fremde im roten Mantel, von dem alle reden?", TagKGLookup).expect(toolKG).allow(toolSearch),
			brak("Erinnerst du dich, was ich dir beim letzten Mal versprochen habe? Schau ruhig nach, was ich damals gesagt habe.", TagTranscriptLookup).expect(toolSearch).allow(toolKG),
			mira("Merk dir bitte: Am Markttag singe ich bei dir in der Taverne.", TagRememberAccept).expect(toolRemember),
			brak("Gib uns eine kurze Zusammenfassung der letzten beiden Sitzungen.", TagRecap).expect(toolRecap).allow(toolSearch),
			mira("Kannst du mir sagen, wie das Wetter morgen wird? Ich meine sicher, nicht geraten.", TagNoToolFits),
			brak("Ich hab gerade mit meinen eigenen Würfeln eine Drei gewürfelt. Lach mich ruhig aus.", TagMustNotCall),
			mira("Flo, sing mir zum Abschied zwei Zeilen von deinem Lieblingslied.", TagSmallTalk),
			brak("Was schulde ich dir insgesamt? Drei Bier zu je drei Kupfer, ein Eintopf für fünf, und sieben Kupfer hab ich schon angezahlt.", TagReasoningBait),
		}},
	}
}
