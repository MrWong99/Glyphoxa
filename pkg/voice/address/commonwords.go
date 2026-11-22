package address

import "strings"

// Everyday words: the third leg of the address-evidence rule (ADR-0024
// amendment, 2026-11-20; see [fuzzyIndex.score]).
//
// The fuzzy tiers exist to recover a NAME the recognizer misspelled ("Barth",
// "Gretta", "Meera"). When the recognizer instead produced a perfectly ordinary
// word of the Campaign Language — "great", "roll", "hart", "mehr" — the far
// likelier reading is that the speaker said that word, and a one-edit
// resemblance to an NPC's name is a coincidence. Vocative punctuation does not
// separate the two: "Great, let's go." and "Greta, let's go." have the same
// shape. So a single utterance token found here is never accepted as a misheard
// name. An EXACT match is untouched — an NPC really called "Wolf" or "Rose" is
// addressed by name like any other — and so is a multi-token join.
//
// The lists are frequency vocabulary (function words, the commonest verbs,
// adjectives and interjections) plus the handful of table-talk nouns every
// session is full of. Words below the fuzzy rune floor need not be listed: they are
// exact-only anyway. The lists are a precision aid, not a dictionary; a word
// missing here still needs the vocative bracket and a tight edit distance to
// count, and adding one costs nothing but a misheard name that happens to BE
// that word — which then needs the name said clearly, the same remedy as for
// any other mishearing.
//
// This list is also what stands between the phonetic tier and a short name's
// everyday homophones in vocative shape ("Mehr!", "Karte, bitte."): that tier
// is not distance-bounded (see [fuzzyIndex.similarity]), so a frequent word
// that shares a common name's consonant skeleton belongs here.
var commonWords = map[string]map[string]struct{}{
	"de": wordSet(strings.Fields(`
		aber alle allein allem allen aller alles also andere anderen anders auch
		bald beide beim bereit bereits besser bevor bier bisher bitte bleiben
		bleibt brauchen brauche braucht breit bringen brot
		dabei dafür daher damit danach danke dann daran darauf darf darum dass
		davon dazu dein deine deinem deinen deiner denn dessen deshalb dich dies
		diese diesem diesen dieser dieses doch dort drei durch dürfen
		eben echt eigentlich einem einen einer eines einfach einmal ende endlich
		erst erste ersten essen etwa etwas euch euer eure
		fahren fahrt fall falls fast fein fest feuer finde finden fort frage
		fragen frei früh früher fünf ganz ganze ganzen gar geben gegen gehen geht
		geld gell genau genug gerade gern gerne gibt ging glaube glauben gleich
		golf groß große großen gruppe gute guten guter gutes
		haben habt halb halt hallo hand hart harte hast hatte hatten haus heißt
		helfen heute hier hilfe hinter hoch holen hören
		ihnen ihre ihrem ihren ihrer immer irgendwie jede jedem jeden jeder jetzt
		kalt kann kannst karte karten kaum kein keine keinem keinen keiner klar
		klein kleine komm komme kommen kommt können könnte kopf kurz
		lang lange lass lassen läuft laut leben leicht leider letzte leute lieber
		liegt links los
		mache machen macht mal manchmal mann mauer meer mehr mein meine meinem
		meinen meiner meinst mensch mich minute mit moment morgen muss müssen
		musst nach nacht nah nehme nehmen nein nett neue neuen nicht nichts noch
		nummer nur oben oder offen ohne party paar plan platz
		raum recht rechts reden richtig rolle ruhe ruhig runde runter
		sache sagen sagt schau schauen schnell schon schön schwer sehen sehr seid
		sein seine seinem seinen seiner seit selbst sich sicher sieht sind soll
		sollen sollte sonst spät später spiel spielen stadt start statt steht
		stimmt stück stunde super
		tage teil tief toll trinken tür über überhaupt übrigens unser unsere
		unten unter viel viele vielleicht vier voll vom von vor vorbei vorher
		wann wäre warm warte warten warum was weg weil weit weiter welche welt
		wenig wenn werde werden wieder will willst wird wirklich wissen weiß
		wohl wolf wollen worte wurde würde würfel würfeln zahl zeit ziemlich
		zimmer zurück zusammen zwei zwischen
		bord braut brett garde garten gerät kraut kredit maurer moor parade pirat
		wort`)...),
	"en": wordSet(strings.Fields(`
		able about above after again against almost alone along already also
		always another anyone anything around away back bare barn beard because
		been before behind being best better between bird board both bread bring
		came card cards cart come comes could dark dart does doing done door down
		each else enough even ever every fact fall feel fell felt find fine fire
		first five four from full gave gets give gives goes going gold gone good
		great guess guys half hand hard have having hear heard help here high
		hold home hope into just keep kind knew know last late later least leave
		left less like line little long look looks made make makes many maybe
		mean might mind mine more most move much must name near need never next
		nice night none nothing once only open other over part party past place
		plan play please point pretty pull push quite rather real really rest
		right role roll room same says seem seen self shall should show side
		since some something soon sorry sort start still stop such sure take
		takes talk tell than thanks that their them then there these they thing
		things think this those though three through time today told took turn
		under until upon very wait walk want wants ward warm well went were what
		when where which while whole whose will with wolf word words work world
		worth would wrong yeah year years your
		bored garden grit guard mayor mirror moor pirate`)...),
}

// commonWordsFor returns the everyday-word list for lang, or nil when the
// package has none for it (the check is then off and the vocative bracket and
// edit bound carry the precision on their own). Unlike the backchannel
// vocabulary there is no cross-language fallback: an English list applied to
// another language's transcripts would only reject at random.
func commonWordsFor(lang string) map[string]struct{} {
	return commonWords[normalizeLang(lang)]
}

// IsCommonWord reports whether term is a single everyday word of lang per the
// list above. It is the read-only view of that list for callers outside Address
// Detection — the STT keyterm list (#701) leaves such a name out, because
// biasing the recognizer towards "Wolf" would make every spoken "Wolf" the
// spelling that routes as an exact name. A multi-word term is never an everyday
// word, and a language without a list has none.
//
// The list was built for the fuzzy tiers, which is the limit of this view: words
// below the fuzzy rune floor are not on it (see the note above).
func IsCommonWord(lang, term string) bool {
	tokens := tokenize(term)
	if len(tokens) != 1 {
		return false
	}
	_, ok := commonWordsFor(lang)[tokens[0]]
	return ok
}
