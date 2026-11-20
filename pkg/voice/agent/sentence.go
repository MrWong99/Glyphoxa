package agent

import (
	"strings"
	"sync"
	"time"
	"unicode"
)

// sentenceSplitter segments a streamed reply into speakable sentences as text
// deltas arrive, so the voice loop can dispatch sentence N to TTS while the LLM
// is still generating sentence N+1 (B1, the highest-leverage latency fix). It is
// the streaming counterpart to "split the whole completion at the end": fed the
// model's incremental [llm.EventText] deltas, it emits each sentence the moment
// its terminator arrives, and [sentenceSplitter.Flush] yields whatever trailing
// text never got a terminator (the common case — models rarely end on a period
// followed by more output).
//
// A "sentence" boundary is a run of terminal punctuation (. ! ? … and their
// combinations like "?!" or "...") followed by whitespace. The trailing
// whitespace is consumed (not carried into the next sentence); the terminator
// run stays with the sentence so TTS prosody is preserved.
//
// The boundary is decided only once the rune AFTER the terminator run has
// arrived. A run that is the last thing in the buffer stays buffered: the model's
// deltas are tokens, so "3" "." "5 Gold" must not be spoken as "3." / "5 Gold.",
// and "?" "!" must not lose its second half. The cost is that a sentence is
// emitted when the next delta arrives rather than on its own last token, which
// is one token of latency; the reply's final sentence comes from
// [sentenceSplitter.Flush] as it always did, and a stream that PAUSES on a
// finished sentence can be settled by [sentenceFeed]'s idle flush.
//
// A single period is additionally NOT a boundary after (see [classifyDot]):
// a known abbreviation ("Dr. Faust", "bzw."), a single-letter initial ("z. B.",
// "J. R. R. Tolkien" — but not the tail of a contraction, "Das war's."), a
// German ordinal ("am 3. Mai", "der 2. Versuch") or a list number opening a
// line ("1. Geh nach Norden"). Splitting there is audible — the TTS ends an
// utterance on "Am dritten." — whereas a missed split only makes one chunk a
// little longer. It is a short lexicon, not a linguistic model.
//
// A sentence that never terminates is cut anyway once it passes
// [maxSentenceRunes], on whitespace: one TTS request must stay bounded whatever
// the model does.
//
// Not safe for concurrent use: one splitter drives one turn on one goroutine.
type sentenceSplitter struct {
	buf strings.Builder
}

// isTerminator reports whether r ends a sentence.
func isTerminator(r rune) bool {
	switch r {
	case '.', '!', '?', '…':
		return true
	default:
		return false
	}
}

// Push appends delta to the pending buffer and returns every complete sentence
// the buffer now holds, in order, with surrounding whitespace trimmed. Text
// after the last decided boundary — including a terminator run the buffer ends
// on — stays buffered for a later Push or Flush. An empty or whitespace-only
// sentence (e.g. a stray ".  ." run) is skipped rather than dispatched as a
// silent utterance.
func (s *sentenceSplitter) Push(delta string) []string {
	s.buf.WriteString(delta)
	text := s.buf.String()

	var out []string
	start := 0 // rune index where the current pending sentence begins
	i := 0
	runes := []rune(text)

	for i < len(runes) {
		if !isTerminator(runes[i]) {
			i++
			continue
		}
		// Consume a run of terminators ("?!", "...").
		j := i
		for j < len(runes) && isTerminator(runes[j]) {
			j++
		}
		// The run ends the buffer: undecided until the next delta shows what
		// follows it (more terminators, a digit, a closing quote, whitespace).
		if j == len(runes) {
			break
		}
		// A boundary requires whitespace after the terminator run. Mid-token
		// punctuation followed by a non-space (a decimal "3.5", a URL) is not a
		// boundary, so the sentence keeps growing.
		if !isSpace(runes[j]) {
			i = j
			continue
		}
		// An abbreviation, initial, ordinal or list number: the period belongs
		// to the token before it, not to the sentence.
		if j-i == 1 && runes[i] == '.' {
			kind := classifyDot(runes[start:i])
			if kind == dotNeedsNextWord {
				// "die 20." is an ordinal only in front of the thing it counts
				// ("die 20. Runde"), so the word after it decides — and that
				// word must have arrived completely.
				word, complete := nextWord(runes[j:])
				if !complete {
					break
				}
				kind = dotEndsSentence
				if ordinalNouns[strings.ToLower(word)] {
					kind = dotInsideSentence
				}
			}
			if kind == dotInsideSentence {
				i = j
				continue
			}
		}
		// Emit text[start:endOfTerminators], trimmed. Then skip the whitespace.
		// Skip a run with no speakable content (a stray ".  ." with no letters or
		// digits) so the pump never receives a silent utterance.
		sentence := strings.TrimSpace(string(runes[start:j]))
		if hasSpeakable(sentence) {
			out = append(out, sentence)
		}
		for j < len(runes) && isSpace(runes[j]) {
			j++
		}
		start = j
		i = j
	}

	// An unterminated run-on: cut it on whitespace so no single sentence — one
	// TTS request, one history line — grows without bound.
	for len(runes)-start > maxSentenceRunes {
		cut := start + maxSentenceRunes
		for k := cut; k > start+maxSentenceRunes/2; k-- {
			if isSpace(runes[k]) {
				cut = k
				break
			}
		}
		sentence := strings.TrimSpace(string(runes[start:cut]))
		if hasSpeakable(sentence) {
			out = append(out, sentence)
		}
		for cut < len(runes) && isSpace(runes[cut]) {
			cut++
		}
		start = cut
	}

	// Re-buffer the undecided remainder.
	s.buf.Reset()
	s.buf.WriteString(string(runes[start:]))
	return out
}

// dotAbbreviations are the lower-cased tokens whose trailing period is part of
// the word (German and English titles and connectives that are followed by more
// of the same sentence). Abbreviations that usually END a sentence ("etc.",
// "usw.") and ones that are also ordinary words ("No.") are deliberately absent.
var dotAbbreviations = map[string]bool{
	"dr": true, "prof": true, "hr": true, "fr": true, "nr": true, "st": true,
	"bzw": true, "ca": true, "evtl": true, "ggf": true, "inkl": true, "vgl": true,
	"bzgl": true, "sog": true, "str": true, "chr": true,
	"mr": true, "mrs": true, "ms": true, "vs": true, "jr": true, "sr": true,
	"mt": true, "lt": true, "col": true, "capt": true, "sgt": true,
}

// maxSentenceRunes is the longest sentence the splitter emits. A model that
// stops punctuating would otherwise hand TTS one request of unbounded length;
// past this many runes the text is cut at the last whitespace. It is far longer
// than a spoken sentence (roughly half a minute of speech), so ordinary replies
// never reach it.
const maxSentenceRunes = 400

// ordinalLeads are the lower-cased German words a dotted number is ALWAYS an
// ordinal after ("am 3. Mai", "im 15. Jahrhundert"). English writes no dotted
// ordinals and none of these words precedes a sentence-final number there, so
// the rule needs no language switch.
var ordinalLeads = map[string]bool{
	"am": true, "im": true, "vom": true, "zum": true, "zur": true, "beim": true,
	"den": true, "dem": true, "des": true, "ab": true, "seit": true,
	"jeden": true, "jedes": true, "jedem": true,
}

// ambiguousOrdinalLeads precede an ordinal ("der 2. Versuch", "bis 3. Mai") as
// readily as a cardinal that ends its sentence ("Ich würfle die 20.", "Warte bis
// 10."). After one of them the number is an ordinal only when the next word is
// one of [ordinalNouns].
var ambiguousOrdinalLeads = map[string]bool{"der": true, "die": true, "das": true, "bis": true}

// ordinalNouns are the lower-cased words that follow a German ordinal: the
// months and the things one counts by rank.
var ordinalNouns = map[string]bool{
	"januar": true, "februar": true, "märz": true, "april": true, "mai": true, "juni": true,
	"juli": true, "august": true, "september": true, "oktober": true, "november": true, "dezember": true,
	"jahrhundert": true, "jahrtausend": true, "jahr": true, "monat": true, "woche": true, "tag": true,
	"stunde": true, "versuch": true, "mal": true, "runde": true, "kapitel": true, "akt": true,
	"stock": true, "etage": true, "ebene": true, "stufe": true, "grad": true, "klasse": true,
	"reihe": true, "platz": true, "tür": true, "haus": true,
}

// dotKind is what a single period means for the sentence it stands in.
type dotKind int

const (
	dotEndsSentence   dotKind = iota // a boundary (given whitespace after it)
	dotInsideSentence                // part of the token before it: abbreviation, initial, ordinal
	dotNeedsNextWord                 // an ordinal only if one of [ordinalNouns] follows
)

// classifyDot decides what a single period following before (the pending
// sentence up to the period) is. See the [sentenceSplitter] doc for the cases.
func classifyDot(before []rune) dotKind {
	// The token directly in front of the period: a run of letters or digits.
	end := len(before)
	begin := end
	for begin > 0 && (unicode.IsLetter(before[begin-1]) || unicode.IsDigit(before[begin-1])) {
		begin--
	}
	token := before[begin:end]
	if len(token) == 0 {
		return dotEndsSentence
	}
	if allDigits(token) {
		// Only a free-standing number can be an ordinal or a list number
		// ("Raum B12." is neither).
		if begin > 0 && !isSpace(before[begin-1]) {
			return dotEndsSentence
		}
		// What precedes the number on its own line.
		lead := before[:begin]
		for k := len(lead) - 1; k >= 0; k-- {
			if lead[k] == '\n' {
				lead = lead[k+1:]
				break
			}
		}
		words := strings.Fields(string(lead))
		if len(words) == 0 {
			return dotInsideSentence // a list number opening the sentence or a line
		}
		switch last := strings.ToLower(words[len(words)-1]); {
		case ordinalLeads[last]:
			return dotInsideSentence
		case ambiguousOrdinalLeads[last]:
			return dotNeedsNextWord
		}
		return dotEndsSentence
	}
	// The tail of a contraction or possessive ("don't.", "war's.", "Bob’s.") is
	// the end of a word, never an initial or an abbreviation.
	if begin > 0 && isApostrophe(before[begin-1]) {
		return dotEndsSentence
	}
	if len(token) == 1 {
		// An initial or a spelled-out abbreviation ("z. B.", "e.g."). The English
		// pronoun is the one single letter that routinely ends a sentence.
		if token[0] == 'I' {
			return dotEndsSentence
		}
		return dotInsideSentence
	}
	if dotAbbreviations[strings.ToLower(string(token))] {
		return dotInsideSentence
	}
	return dotEndsSentence
}

// isApostrophe reports whether r is one of the apostrophes a contraction is
// written with.
func isApostrophe(r rune) bool { return r == '\'' || r == '’' || r == 'ʼ' }

// nextWord returns the first word of after (leading whitespace skipped) and
// whether it is complete — followed by something that is not a letter. A word
// the buffer ends on may still be growing.
func nextWord(after []rune) (word string, complete bool) {
	i := 0
	for i < len(after) && isSpace(after[i]) {
		i++
	}
	j := i
	for j < len(after) && unicode.IsLetter(after[j]) {
		j++
	}
	return string(after[i:j]), j < len(after)
}

// allDigits reports whether every rune of token is a digit.
func allDigits(token []rune) bool {
	for _, r := range token {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// Flush returns the trailing text the stream ended on — an unterminated
// sentence, or one whose terminator run was the very last thing generated —
// trimmed, or "" if nothing remains. Call it once after the last [Push]. It is
// the common end-of-completion case: a reply whose final sentence the model did
// not punctuate-then-continue.
func (s *sentenceSplitter) Flush() string {
	rest := strings.TrimSpace(s.buf.String())
	s.buf.Reset()
	if !hasSpeakable(rest) {
		return ""
	}
	return rest
}

// Settle returns the buffered text as a sentence if it ends on a terminator run
// that would be a boundary were whitespace to follow, or "" (leaving the buffer
// untouched) otherwise. It is the answer to "the stream has gone quiet": what
// follows the terminator is no longer one token away, so the undecided sentence
// is decided in favour of speaking it.
func (s *sentenceSplitter) Settle() string {
	runes := []rune(s.buf.String())
	end := len(runes)
	i := end
	for i > 0 && isTerminator(runes[i-1]) {
		i--
	}
	// A dot whose meaning hangs on the next word is settled as a sentence end:
	// the quiet stream is the evidence that no "Runde" is one token away.
	if i == end || (end-i == 1 && runes[i] == '.' && classifyDot(runes[:i]) == dotInsideSentence) {
		return ""
	}
	sentence := strings.TrimSpace(string(runes))
	if !hasSpeakable(sentence) {
		return ""
	}
	s.buf.Reset()
	return sentence
}

// DefaultSentenceIdleFlush is the [Config.SentenceIdleFlush] the live wiring
// uses: how long a streamed reply may sit on a finished-looking sentence before
// it is spoken without waiting for the next delta. Tokens of one generation
// arrive milliseconds apart, so a gap this long is a real pause — a stalled
// stream, or a slow tool round the model announced with "Let me check." — and
// holding the sentence back would add the whole pause to its latency.
const DefaultSentenceIdleFlush = 300 * time.Millisecond

// sentenceFeed drives a [sentenceSplitter] from a live stream: it pushes every
// sentence the deltas complete to push, and — when built with a positive idle
// interval — settles a sentence the stream pauses on. The timer is why it
// carries a lock; the splitter itself is single-goroutine.
type sentenceFeed struct {
	push     func(sentence string) error
	interval time.Duration // idle flush interval; <= 0 disables it (no timer at all)

	mu     sync.Mutex
	split  sentenceSplitter
	idle   *time.Timer
	due    time.Time // when the stream will have been quiet for interval since the last Write
	closed bool
	err    error // a push error from the idle flush, surfaced by the next Write/Close
}

// newSentenceFeed builds a feed delivering sentences to push. push is called
// with the feed's lock held, from the writer's goroutine or the idle timer's,
// never concurrently, and must not block. interval <= 0 turns the idle flush
// off: the feed is then a pure function of its deltas, with no wall clock in it.
func newSentenceFeed(push func(sentence string) error, interval time.Duration) *sentenceFeed {
	f := &sentenceFeed{push: push, interval: interval}
	if interval > 0 {
		f.idle = time.AfterFunc(interval, f.onIdle)
		f.idle.Stop()
	}
	return f
}

// Write feeds one delta, pushing the sentences it completes. It returns the
// first push error (the caller stops generating).
func (f *sentenceFeed) Write(delta string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	for _, sentence := range f.split.Push(delta) {
		if err := f.push(sentence); err != nil {
			f.err = err
			return err
		}
	}
	if f.idle != nil {
		// Stop cannot recall a callback that has already fired and is waiting
		// for this lock, so the callback checks due itself.
		f.due = time.Now().Add(f.interval)
		f.idle.Reset(f.interval)
	}
	return nil
}

// onIdle is the timer callback. It settles the pending sentence only if the
// stream really has been quiet for the whole interval: a callback that fired
// just as a new delta arrived finds due moved into the future and re-arms for
// the remainder instead — otherwise a delta gap of about one interval would
// split "3." from "5 Gold" after all.
func (f *sentenceFeed) onIdle() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || f.err != nil {
		return
	}
	if rest := time.Until(f.due); rest > 0 {
		f.idle.Reset(rest)
		return
	}
	if sentence := f.split.Settle(); sentence != "" {
		f.err = f.push(sentence)
	}
}

// Close ends the feed; nothing is pushed after it returns. With flush set (a
// generation that completed) the trailing sentence is pushed first; without it
// (a failed or cancelled generation, which ended mid-sentence) the tail is
// dropped. It returns a push error the idle flush or the tail ran into.
func (f *sentenceFeed) Close(flush bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	if f.idle != nil {
		f.idle.Stop()
	}
	if f.err == nil && flush {
		if tail := f.split.Flush(); tail != "" {
			f.err = f.push(tail)
		}
	}
	return f.err
}

// isSpace reports whether r is whitespace that ends a sentence boundary.
func isSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}

// hasSpeakable reports whether s contains any letter or digit OUTSIDE a bracketed
// tag — i.e. something the TTS will actually voice. A run of only
// punctuation/whitespace, only emojis, or only bracketed audio/speaker tags
// (eleven_v3 markup like "[laughs]" or "[Bart]") is not dispatched: ElevenLabs
// strips speaker tags and emojis and then rejects the request if nothing
// remains ("input_text_empty", HTTP 400), so a tag-only sentence would drop the
// whole reply. Letters/digits inside "[...]" are therefore not counted as
// speakable; a sentence with real words plus a tag ("Aye! [laughs]") still
// qualifies on the words outside the brackets.
func hasSpeakable(s string) bool {
	depth := 0
	for _, r := range s {
		switch r {
		case '[':
			depth++
		case ']':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
				return true
			}
		}
	}
	return false
}
