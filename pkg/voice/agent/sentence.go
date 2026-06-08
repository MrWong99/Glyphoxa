package agent

import (
	"strings"
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
// combinations like "?!") followed by whitespace or end-of-input. The trailing
// whitespace is consumed (not carried into the next sentence); the terminator
// itself stays with the sentence so TTS prosody is preserved. The splitter does
// not try to be linguistically perfect — an abbreviation ("Mr.") or a decimal
// ("3.5") that is immediately followed by a space will over-split, which for
// spoken TTS is benign (a slightly shorter first chunk), and chasing perfection
// here would add a lexicon the voice loop does not need. What it must guarantee
// is that no sentence is dropped or duplicated across the delta boundary, which
// the explicit carry buffer below provides.
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
// after the last terminator stays buffered for a later Push or Flush. An empty
// or whitespace-only sentence (e.g. a stray ".  ." run) is skipped rather than
// dispatched as a silent utterance.
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
		// A boundary requires whitespace or end-of-input after the terminator run.
		// Mid-token punctuation followed by a non-space (a decimal "3.5", a URL)
		// is not a boundary, so the sentence keeps growing.
		if j < len(runes) && !isSpace(runes[j]) {
			i = j
			continue
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

	// Re-buffer the unterminated remainder.
	s.buf.Reset()
	s.buf.WriteString(string(runes[start:]))
	return out
}

// Flush returns the trailing text the stream ended on without a terminator,
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

// isSpace reports whether r is whitespace that ends a sentence boundary.
func isSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}

// hasSpeakable reports whether s contains any letter or digit — i.e. something
// worth synthesizing. A run of only punctuation/whitespace is not dispatched, so
// the pump never speaks a silent or punctuation-only utterance.
func hasSpeakable(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}
