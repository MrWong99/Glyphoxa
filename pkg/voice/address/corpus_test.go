package address_test

import (
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/address"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// The Address Detection evaluation corpus (ADR-0024 amendment, 2026-11-20).
//
// The roster is deliberately the WORST case for a fuzzy name matcher: short,
// common-sounding names whose phonetic codes and one-edit neighbours are
// everyday words ("Bart" ≈ Brot/bereit/part, "Mira" ≈ mehr/Meer/more, "Greta" ≈
// Karte/gerade/great, "Rolf" ≈ Wolf/roll). Before the amendment the fuzzy tiers
// alone were decisive, so this roster answered a large share of ordinary table
// talk — the owner's long-standing "the NPCs answer far too often".
//
// Every utterance is scored on a FRESH matcher (no continuation state), from a
// known speaker, with the Agents built the way production builds them
// (derived truncation aliases included). A negative that routes anywhere is a
// false positive; a positive that routes to the wrong Agent is both a false
// positive and a miss.

// corpusRoster builds the evaluation roster the way wirenpc's matcherAgent
// does: every Character NPC carries its derived STT-truncation aliases, and the
// Butler is AddressOnly.
func corpusRoster() []address.Agent {
	agents := []address.Agent{{
		Target:      voiceevent.AddressTarget{AgentID: "butler", AgentRole: voiceevent.AgentRoleButler, Name: "Glyphoxa"},
		AddressOnly: true,
	}}
	for _, name := range []string{"Bart", "Greta", "Mira", "Flo", "Tom", "Anna", "Rolf"} {
		agents = append(agents, address.Agent{
			Target:            voiceevent.AddressTarget{AgentID: name, AgentRole: voiceevent.AgentRoleCharacter, Name: name},
			TruncationAliases: address.DeriveTruncationAliases(name),
		})
	}
	return agents
}

// corpusRoute scores one utterance on a fresh matcher and returns the addressed
// AgentID ("" for nobody).
func corpusRoute(lang, utter string) string {
	m := address.NewMatcher(address.Config{Language: lang}, corpusRoster()...)
	got := m.TargetMatchFrom("player-1", utter)
	if len(got) == 0 {
		return ""
	}
	return got[0].Target.AgentID
}

// negativesDE is ordinary German table talk that addresses NO Agent. It mixes
// the auditor's live-shaped reproductions (phonetic collisions mid-sentence),
// vocative-SHAPED everyday words (single-word exclamations, sentence-initial
// words followed by a comma — the shapes a name-only vocative rule would still
// let through), and plain chatter.
var negativesDE = []string{
	// Phonetic / edit-distance collisions with the roster, mid-sentence.
	"Seid ihr bereit?",
	"Gib mir das Brot.",
	"Das Meer ist ruhig.",
	"Zeig mir mal die Karte.",
	"Ich will mehr davon.",
	"Der Pirat lacht.",
	"Die Mauer ist hoch.",
	"Das Brett ist morsch.",
	"Wir gehen an Bord.",
	"Das ist zu breit.",
	"Ich brauche einen Berater.",
	"Gerade eben.",
	"Der Grat ist schmal.",
	"Das Moor ist tief.",
	"Wir warten hier.",
	"Guten Morgen zusammen.",
	"Warte mal kurz.",
	"Wir machen eine Fahrt nach Norden.",
	"Der Barde spielt ein Lied.",
	"Am Start war alles gut.",
	"Das wird eine harte Nacht.",
	"Ich greife den Greif an.",
	"Mir ist kalt.",
	"Hat jemand noch Chips?",
	"Die Party war gut.",
	"Der Bär brüllt.",
	"Ich habe Durst.",
	"Der Wolf heult in der Ferne.",
	"Das ist eine Rolle Pergament.",
	"Wir spielen später noch Golf.",
	"Art und Weise.",
	"Was für eine Art von Bier ist das?",
	// Vocative-shaped everyday words: alone, or opening a clause before a comma.
	"Bereit?",
	"Brot?",
	"Mehr!",
	"Start!",
	"Party!",
	"Gerade.",
	"Warte, ich komme gleich.",
	"Karte, bitte.",
	"Hart, aber fair.",
	"Bitte, nach dir.",
	"Danke, das reicht.",
	"Moment, ich muss kurz nachdenken.",
	"Genau, so machen wir das.",
	"Also, ich würfle jetzt.",
	"So, woran erinnerst du dich?",
	"Gut, dann gehen wir weiter.",
	"Na gut, ich öffne die Tür.",
	"Ich würfle, Moment.",
	"Wer ist dran, Leute?",
	// Plain table talk.
	"Ich greife den Ork an.",
	"Wie viele Trefferpunkte habe ich noch?",
	"Kann ich einen Wahrnehmungswurf machen?",
	"Ich hole mir kurz was zu trinken.",
	"Wer ist als Nächstes dran?",
	"Das war eine kritische Zwanzig!",
	"Ich schleiche mich an der Wache vorbei.",
	"Hast du die Regeln dabei?",
	"Wir sollten erst rasten.",
	"Ich wirke einen Feuerball auf die Gruppe.",
	"Was steht auf dem Schild?",
	"Lass uns den linken Gang nehmen.",
	"Mein Charakter hat Angst vor Spinnen.",
	"Die Tür ist verschlossen.",
	"Kannst du mir die Würfel geben?",
	"Ich schaue mich im Raum um.",
	"Das habe ich nicht verstanden.",
	"Wir treffen uns morgen wieder.",
	"Hat jemand eine Fackel?",
	"Ich bin gleich wieder da.",
	"Wie spät ist es eigentlich?",
	"Der Drache fliegt über die Stadt.",
	"Wir brauchen einen besseren Plan.",
	"Ich nehme das Schwert und den Schild.",
	"Das macht zwölf Schaden.",
}

// negativesEN is the English counterpart of [negativesDE].
var negativesEN = []string{
	// Phonetic / edit-distance collisions with the roster, mid-sentence.
	"I want more of that.",
	"That is part of the plan.",
	"Look in the mirror.",
	"The mayor is here.",
	"It is a great day.",
	"A bird flew by.",
	"Let us start the fight.",
	"I rolled a great hit.",
	"We board the ship.",
	"Roll for initiative.",
	"The wolf is growling.",
	"He has a long beard.",
	"Pass me the bread.",
	"That was a bad start.",
	"I play the role of the healer.",
	"We meet at the barn.",
	"And a potion for me.",
	// Vocative-shaped everyday words.
	"Great!",
	"Great, let's go.",
	"Roll!",
	"More, please.",
	"Start!",
	"Party!",
	"Right, where were we?",
	"Okay, I attack the goblin.",
	"Wait, what did he say?",
	"Sorry, I missed that.",
	"So, what do you remember?",
	"Well, that went badly.",
	"Thanks, that helps.",
	"Whose turn is it, guys?",
	// Plain table talk.
	"I attack the orc with my sword.",
	"How many hit points do I have left?",
	"Can I make a perception check?",
	"I'll grab something to drink.",
	"Who is next in the order?",
	"That was a natural twenty!",
	"I sneak past the guard.",
	"Do you have the rulebook?",
	"We should take a long rest first.",
	"I cast fireball at the group.",
	"What does the sign say?",
	"Let's take the left corridor.",
	"My character is afraid of spiders.",
	"The door is locked.",
	"Can you hand me the dice?",
	"I look around the room.",
	"I did not catch that.",
	"See you all tomorrow.",
	"Does anyone have a torch?",
	"That is twelve damage.",
}

// corpusPositive is one genuine address and the Agent it must reach.
type corpusPositive struct {
	lang, utter, want string
}

// positives are genuine addresses: clean names in every vocative position, the
// existing binding corpora (#197 truncation, #198 collision, #199 German, #400
// Glyfoxa, #413 trailing vocative) re-expressed on this roster, and STT
// mishearings of the name in address position.
var positives = []corpusPositive{
	// Clean names, head vocative.
	{"de", "Bart, was gibt es heute zu essen?", "Bart"},
	{"de", "Greta, hast du Heiltränke?", "Greta"},
	{"de", "Mira, wo ist der Schlüssel?", "Mira"},
	{"de", "Flo, woran erinnerst du dich?", "Flo"},
	{"de", "Tom, komm mal her.", "Tom"},
	{"de", "Anna, was meinst du dazu?", "Anna"},
	{"de", "Rolf, öffne das Tor.", "Rolf"},
	{"de", "Glyphoxa, würfle einen W20.", "butler"},
	{"en", "Bart, what's the special tonight?", "Bart"},
	{"en", "Greta, do you sell healing potions?", "Greta"},
	{"en", "Mira, where is the key?", "Mira"},
	{"en", "Flo, what do you remember?", "Flo"},
	{"en", "Tom, come over here.", "Tom"},
	{"en", "Anna, what do you think?", "Anna"},
	{"en", "Rolf, open the gate.", "Rolf"},
	{"en", "Glyphoxa, roll a d20.", "butler"},
	// Clean names, trailing / inner vocative and bare call (#413 shape).
	{"de", "Was meinst du, Bart?", "Bart"},
	{"de", "Hast du das gesehen, Greta?", "Greta"},
	{"de", "Und du, Mira?", "Mira"},
	{"de", "Hey Tom, hörst du mich?", "Tom"},
	{"de", "Sag mal Rolf, was kostet das Zimmer?", "Rolf"},
	{"de", "Bart!", "Bart"},
	{"de", "Flo?", "Flo"},
	{"en", "What do you think, Anna?", "Anna"},
	{"en", "Hey Rolf, can you hear me?", "Rolf"},
	{"en", "Tell me, Greta, what is in the bottle?", "Greta"},
	// Clean name, no punctuation at all (an STT that does not punctuate).
	{"de", "bart was hast du im angebot", "Bart"},
	{"en", "mira where did you hide it", "Mira"},
	// #198: the exactly-heard name outranks a phonetic collision word.
	{"de", "Tom, was liegt gerade auf deinem Amboss?", "Tom"},
	{"de", "Anna, zeig mir mal die Karte.", "Anna"},
	// #400: the vocative-flagged Butler outranks a topic-mentioned Character.
	{"de", "So, Glyfoxa, was hat Bart über sein Gasthaus erzählt?", "butler"},
	{"de", "Glyfoxa, frag Bart, ob er kommt.", "butler"},
	{"en", "Glyfoxa, roll a d6", "butler"},
	// #197: the leading consonant dropped by STT.
	{"de", "Art, wie läuft das Geschäft heute Abend?", "Bart"},
	// STT mishearings of the name in address position.
	{"de", "Bard, was gibt es heute?", "Bart"},
	{"de", "Barth, hast du noch ein Zimmer frei?", "Bart"},
	{"de", "Bert, schenk mir ein Bier ein.", "Bart"},
	{"de", "Bahrd, was kostet das Zimmer?", "Bart"},
	{"de", "Grete, hast du Kräuter?", "Greta"},
	{"de", "Gretha, was kostet der Trank?", "Greta"},
	{"de", "Kreta, erzähl mir von deinen Kräutern.", "Greta"},
	{"de", "Miera, wo bist du?", "Mira"},
	{"de", "Hanna, was meinst du?", "Anna"},
	{"de", "Anne, hilf mir bitte.", "Anna"},
	{"de", "Ralf, öffne das Tor.", "Rolf"},
	{"de", "Rolph, lass uns rein.", "Rolf"},
	{"de", "Meera, was siehst du?", "Mira"},
	{"de", "Was sagst du dazu, Barth?", "Bart"},
	{"de", "Bard?", "Bart"},
	{"en", "bard, what is the special tonight?", "Bart"},
	{"en", "Barth, pour me an ale.", "Bart"},
	{"en", "Gretta, what do you sell?", "Greta"},
	{"en", "Meera, where are you going?", "Mira"},
	{"en", "Myra, tell me about the temple.", "Mira"},
	{"en", "Rolph, open up.", "Rolf"},
	{"en", "What about you, Gretta?", "Greta"},
}

// deliberateMisses are utterances that LOOK like they could be a misheard
// address but are decided to route NOWHERE, each with the reason. They are
// pinned so the decision is explicit and a later change that starts routing
// them has to argue with this table.
var deliberateMisses = []struct {
	lang, utter, why string
}{
	// The live case: "Flo, woran erinnerst du dich" heard as "So, woran …".
	// "So" is among the most frequent German AND English discourse openers; a
	// rule that hears "Flo" in it would wake Flo on a large share of all
	// sentences at the table. A three-rune name is exact-only by the rune
	// floor, and stays so. Inside a live exchange the speaker-scoped
	// continuation still carries it; cold, the speaker repeats the name.
	{"de", "So, woran erinnerst du dich?", "misheard 'Flo,' is indistinguishable from the discourse opener 'So,'"},
	{"en", "So, what do you remember?", "misheard 'Flo,' is indistinguishable from the discourse opener 'So,'"},
	// A misheard name with no vocative punctuation is not evidence of address:
	// without the bracket it is indistinguishable from the collision words the
	// negative corpus is full of ("Der Barde spielt", "It is a great day").
	{"en", "bard what is the special tonight", "fuzzy name without vocative punctuation"},
	// Below the four-rune floor a candidate is exact-only: "Ana" is one edit from
	// "Anna", but so are "an", "na" and half the function words from "Tom"/"Flo".
	{"en", "Ana, what do you think?", "three-rune candidate is exact-only (rune floor)"},
}

// knownFalsePositives are the residue: utterances that address nobody on this
// roster and STILL route, because they are indistinguishable — in shape and in
// spelling distance — from the misheard addresses the positives demand. They
// are pinned as currently routing so the limit is written down, and so a change
// that fixes one shows up as a failing pin to delete rather than as silence.
// They are NOT counted in [TestAddressCorpus]'s precision.
var knownFalsePositives = []struct {
	lang, utter, got, why string
}{
	// A vocative to somebody ELSE whose name/title is a near-homophone of an NPC:
	// a bard PC, a player called Hanna. Only renaming the NPC separates these.
	{"de", "Barde, sing uns ein Lied.", "Bart", "vocative to a near-homophone (phonetic, two edits)"},
	{"de", "Hanna, kommst du mit?", "Anna", "vocative to a player whose name is one edit from the NPC's"},
	// A rarer word that shares the name's phonetic code or sits one edit away,
	// in vocative shape. The everyday-word list is the lever for these.
	{"en", "Brat, give that back.", "Bart", "everyday word outside the frequency list"},
	{"de", "Kröte, igitt!", "Greta", "phonetic collision outside the frequency list, vocative shape"},
	// An EXACT name used as a topic or as the ordinary noun it also is. ADR-0024
	// treats a clean name as an address by design.
	{"de", "Der Bart muss ab.", "Bart", "exact name that is also a common noun"},
	{"en", "I already asked Tom about it.", "Tom", "exact name mentioned as a topic"},
}

// corpusScore runs the whole corpus and returns the confusion counts plus the
// offending utterances.
func corpusScore() (tp, fp, fn int, falsePositives, misses []string) {
	for lang, utters := range map[string][]string{"de": negativesDE, "en": negativesEN} {
		for _, u := range utters {
			if got := corpusRoute(lang, u); got != "" {
				fp++
				falsePositives = append(falsePositives, "["+lang+"] "+u+" -> "+got)
			}
		}
	}
	for _, p := range positives {
		switch got := corpusRoute(p.lang, p.utter); got {
		case p.want:
			tp++
		case "":
			fn++
			misses = append(misses, "["+p.lang+"] "+p.utter+" -> nobody, want "+p.want)
		default:
			fp++
			fn++
			misses = append(misses, "["+p.lang+"] "+p.utter+" -> "+got+", want "+p.want)
		}
	}
	return tp, fp, fn, falsePositives, misses
}

// TestAddressCorpus is the precision/recall gate. Precision is the binding
// half: NO negative may route anywhere. Recall is pinned at the level the
// amendment shipped with so a later tightening that silently drops genuine
// addresses fails here rather than at a table.
func TestAddressCorpus(t *testing.T) {
	if len(negativesDE) < 60 || len(negativesEN) < 40 {
		t.Fatalf("negative corpus too small: de=%d (want >=60), en=%d (want >=40)", len(negativesDE), len(negativesEN))
	}
	tp, fp, fn, falsePositives, misses := corpusScore()
	precision := float64(tp) / float64(tp+fp)
	recall := float64(tp) / float64(tp+fn)
	t.Logf("negatives: de=%d en=%d; positives: %d", len(negativesDE), len(negativesEN), len(positives))
	t.Logf("TP=%d FP=%d FN=%d precision=%.3f recall=%.3f", tp, fp, fn, precision, recall)
	for _, s := range falsePositives {
		t.Errorf("false positive: %s", s)
	}
	for _, s := range misses {
		t.Errorf("miss: %s", s)
	}
}

// TestAddressCorpus_KnownFalsePositives pins the documented residue.
func TestAddressCorpus_KnownFalsePositives(t *testing.T) {
	for _, c := range knownFalsePositives {
		if got := corpusRoute(c.lang, c.utter); got != c.got {
			t.Errorf("[%s] %q routed to %q, pinned as the known false positive %q (%s) — if this got fixed, delete the pin",
				c.lang, c.utter, got, c.got, c.why)
		}
	}
}

// TestAddressCorpus_DeliberateMisses pins the documented non-routes.
func TestAddressCorpus_DeliberateMisses(t *testing.T) {
	for _, c := range deliberateMisses {
		if got := corpusRoute(c.lang, c.utter); got != "" {
			t.Errorf("[%s] %q routed to %s; decided to route nowhere: %s", c.lang, c.utter, got, c.why)
		}
	}
}
