package llmbench

import (
	"regexp"
	"strings"

	"github.com/MrWong99/Glyphoxa/pkg/tool"
	ttseleven "github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
)

// Findings counts what the detectors found in one text. Each field is a count
// of occurrences (sentences, for English), so findings add up across turns.
type Findings struct {
	// ToolMarkup: pseudo tool calls and leaked markup — every shape the
	// production scrubber knows (pkg/tool), any other XML-like element, a bare
	// JSON object naming a Tool, and chat-template control tokens.
	ToolMarkup int `json:"tool_markup"`
	// StageDirections: narrated actions — asterisk- or parenthesis-wrapped, or
	// the NPC described in the third person. A bracketed action is counted
	// under UnknownTags.
	StageDirections int `json:"stage_directions"`
	// UnknownTags: square-bracket spans outside the audio-tag vocabulary the
	// TTS prompt offers ([ttseleven.AudioTags]) — an invented delivery tag
	// ("[softly]") or a stage direction in tag clothing ("[leans on the bar]").
	// The prompt itself warns that such a bracket may be read aloud. Closing
	// tags ("[/pause]") are counted under ToolMarkup, not here.
	UnknownTags int `json:"unknown_tags"`
	// Markdown: formatting a voice cannot speak (bold, bullets, headings, code).
	Markdown int `json:"markdown"`
	// English: sentences that read as English inside a German reply.
	English int `json:"english"`
	// ToolNarration: talk about tools, parameters or data sources instead of
	// in-character speech.
	ToolNarration int `json:"tool_narration"`
	// GenderSlip: a masculine form of address used for a female speaker.
	GenderSlip int `json:"gender_slip"`
}

// Add accumulates o into f.
func (f *Findings) Add(o Findings) {
	f.ToolMarkup += o.ToolMarkup
	f.StageDirections += o.StageDirections
	f.UnknownTags += o.UnknownTags
	f.Markdown += o.Markdown
	f.English += o.English
	f.ToolNarration += o.ToolNarration
	f.GenderSlip += o.GenderSlip
}

// Total is the sum of all findings.
func (f Findings) Total() int {
	return f.ToolMarkup + f.StageDirections + f.UnknownTags + f.Markdown + f.English + f.ToolNarration + f.GenderSlip
}

// DetectOptions is the per-turn context the detectors need.
type DetectOptions struct {
	// NPCName is the speaking NPC's name, for the third-person narration check.
	NPCName string
	// FemaleSpeaker marks a turn addressed by a female player character, which
	// arms the gender-address check.
	FemaleSpeaker bool
}

var (
	// anyElementRE matches an XML-like opening or closing element with a
	// name — what is left after the production scrub is still a leak.
	anyElementRE = regexp.MustCompile(`</?[A-Za-z_][\w:.-]*(\s[^<>]*)?/?>`)
	// toolJSONRE matches a bare JSON object naming one of the bench Tools.
	toolJSONRE = regexp.MustCompile(`\{\s*"(name|tool|function)"\s*:\s*"(dice|recap|kg_query|transcript_search|remember_knowledge)"`)

	// controlTokenRE matches chat-template control tokens that escaped into the
	// text: a closing bracket tag like "[/final]", or a pipe token.
	controlTokenRE = regexp.MustCompile(`\[/\w+\]|<\|[^|<>\n]{1,40}\|>`)

	// audioTagRE matches one bracketed span on one line. Its length is not
	// capped: a stage direction in brackets runs as long as the model likes
	// ("[wipes a tankard, glances at the door, then leans in …]"), and the
	// span is already bounded by the line and by the next bracket.
	audioTagRE = regexp.MustCompile(`\[[^\[\]\n]+\]`)
	// tagSizedRE matches a bracketed span short enough to be an audio tag. Only
	// these are stripped before the English and tool-narration checks (the tag
	// vocabulary is English by design); a longer span is the model talking, so
	// its English and tool talk still count there — it is an unknown tag too.
	tagSizedRE  = regexp.MustCompile(`\[[^\[\]\n]{1,40}\]`)
	boldRE      = regexp.MustCompile(`\*\*[^*\n]+\*\*|__[^_\n]+__`)
	asteriskRE  = regexp.MustCompile(`\*[^*\n]{2,120}\*`)
	parenActRE  = regexp.MustCompile(`(?i)\((lacht|grinst|nickt|seufzt|lächelt|zwinkert|schmunzelt|kichert|flüstert|räuspert|laughs|smiles|sighs|nods|grins|winks)[^)\n]{0,80}\)`)
	headingRE   = regexp.MustCompile(`(?m)^\s*#{1,6}\s+\S`)
	bulletRE    = regexp.MustCompile(`(?m)^\s*[-*•]\s+\S`)
	sentenceRE  = regexp.MustCompile(`[^.!?…\n]+[.!?…]*`)
	wordRE      = regexp.MustCompile(`[\p{L}']+`)
	narrationRE = regexp.MustCompile(`(?i)\b(tools?|werkzeuge?|funktion(en)?|function|parameter|json|kg_query|transcript_search|remember_knowledge|datenbank|wissensgraph|knowledge graph|api|transkript|suchfunktion|count|sides)\b`)
	genderRE    = regexp.MustCompile(`(?i)\bmein\s+(lieber|guter|bester|freund|herr|junge)\b|\b(junger mann|bursche|kumpel)\b`)
)

// knownAudioTags is the vocabulary the production TTS prompt lists, lower-case.
var knownAudioTags = wordList(ttseleven.AudioTags())

func wordList(words []string) map[string]bool {
	m := map[string]bool{}
	for _, w := range words {
		m[strings.ToLower(w)] = true
	}
	return m
}

// unknownTags counts the bracketed spans of text that are not a tag of the TTS
// vocabulary. Case and padding are forgiven; a closing tag is not counted (it
// is a control token, see controlTokenRE).
func unknownTags(text string) int {
	n := 0
	for _, m := range audioTagRE.FindAllString(text, -1) {
		tag := strings.ToLower(strings.TrimSpace(m[1 : len(m)-1]))
		if strings.HasPrefix(tag, "/") || knownAudioTags[tag] {
			continue
		}
		n++
	}
	return n
}

// thirdPersonVerbs are the action verbs a model uses when it narrates the NPC
// instead of speaking as her ("Flo lacht …", "Flo wirft …").
const thirdPersonVerbs = `wischt|lacht|grinst|nickt|wirft|schaut|lehnt|zwinkert|schüttelt|greift|stellt|beugt|hebt|zuckt|seufzt|lächelt|würfelt|blickt|kichert|stemmt|poliert|schmunzelt|runzelt|kramt|holt|zieht|nimmt|rollt|überlegt|denkt|sieht|geht|dreht|legt|schenkt|putzt|ruft|sagt|antwortet|rolls|laughs|smiles|nods|looks|wipes|grins`

// englishWords and germanWords are function words (plus the few task words the
// leaked reasoning uses) that tell the two languages apart cheaply. Words both
// languages share ("in", "was", "so", "will", "an", "not") are in neither set.
var (
	englishWords = wordSet("the and you your is are were with this that have has would what for i'll i'm let me need needs want wants roll of to a it they he she we do does should can which on about from but my just here there their look up")
	germanWords  = wordSet("der die das und ich du ist sind war nicht ein eine einen mit für auf was wir sie es dir den dem zu im noch schon mal na ja auch nur aber oder wenn dann hier da mein dein hat habe bin bist kann mir dich mich so wie von bei nach aus")
)

func wordSet(s string) map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(s) {
		m[w] = true
	}
	return m
}

// Detect runs every detector over text — a reply as the model wrote it, or as
// it would be spoken — and returns what it found.
func Detect(text string, opts DetectOptions) Findings {
	var f Findings

	// Tool markup: the production scrubber's shapes, then anything else that
	// still looks like an element or a tool-call object.
	clean, calls := tool.ExtractPseudoCalls(text)
	f.ToolMarkup = len(calls) + len(anyElementRE.FindAllString(clean, -1)) + len(toolJSONRE.FindAllString(clean, -1)) +
		len(controlTokenRE.FindAllString(clean, -1))

	// Markdown. Bold is counted here and removed before the asterisk check, so
	// "**wichtig**" is formatting, not a stage direction.
	f.Markdown = len(boldRE.FindAllString(text, -1)) + len(headingRE.FindAllString(text, -1)) +
		len(bulletRE.FindAllString(text, -1)) + strings.Count(text, "`")
	noBold := boldRE.ReplaceAllString(text, "")

	// Stage directions.
	f.StageDirections = len(asteriskRE.FindAllString(noBold, -1)) + len(parenActRE.FindAllString(text, -1))
	if opts.NPCName != "" {
		third := regexp.MustCompile(`\b` + regexp.QuoteMeta(opts.NPCName) + `\s+(` + thirdPersonVerbs + `)\b`)
		f.StageDirections += len(third.FindAllString(text, -1))
	}

	f.UnknownTags = unknownTags(text)

	// English leakage, per sentence, ignoring the bracketed audio tags (their
	// vocabulary is English by design).
	for _, sentence := range sentenceRE.FindAllString(tagSizedRE.ReplaceAllString(text, " "), -1) {
		en, de := 0, 0
		for _, w := range wordRE.FindAllString(strings.ToLower(sentence), -1) {
			if englishWords[w] {
				en++
			}
			if germanWords[w] {
				de++
			}
		}
		if en >= 3 && en > de {
			f.English++
		}
	}

	f.ToolNarration = len(narrationRE.FindAllString(tagSizedRE.ReplaceAllString(text, " "), -1))

	if opts.FemaleSpeaker {
		f.GenderSlip = len(genderRE.FindAllString(text, -1))
	}
	return f
}
