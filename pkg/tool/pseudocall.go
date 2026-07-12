package tool

import (
	"encoding/json"
	"regexp"
	"strings"
)

// This file implements the pseudo-XML tool-call scrub + recovery (issue #410).
//
// Some LLMs (llama-3.3-70b on Groq, observed in live play) sometimes emit a
// tool call as MALFORMED TEXT content instead of a real provider tool_call —
// e.g. `<function=remember_knowledge>{"kind":"edge",...}</function>` — with no
// provider-side 400 (that path is #398). Left untouched, that string is spoken
// by TTS and persisted verbatim into the Transcript Line, and the tool never
// runs. [ExtractPseudoCalls] lifts these out of assistant text so the [Loop] can
// scrub them from the spoken/persisted text and recover the intent by executing
// the call as a real tool round.
//
// This package stays vendor- and metric-agnostic (ADR-0028): the scrub is pure
// text→text; the observability hook lives on [Loop] as a caller-supplied
// callback, not an observe/metrics import here.

const (
	pseudoMarker = "<function=" // opening literal of a pseudo-call
	pseudoClose  = "</function>"
)

// pseudoCallRE matches one well-formed `<function=NAME ...>...</function>`
// occurrence. The (?s) flag lets the middle span newlines; the non-greedy body
// stops at the first `</function>` (valid JSON never contains that literal).
var pseudoCallRE = regexp.MustCompile(`(?s)<function=(\w+)(.*?)</function>`)

// openerNameRE reads the tool name off an UNTERMINATED opener (a `<function=`
// with no closing `</function>`, e.g. a max_tokens-truncated call).
var openerNameRE = regexp.MustCompile(`^<function=(\w+)`)

// PseudoCall is one recovered pseudo-XML tool call: the Tool name and the parsed
// JSON arguments. Args is nil when the wrapper's arguments could not be parsed
// as JSON — or when the wrapper was unterminated — so the occurrence is stripped
// from spoken text but the [Loop] treats it as unrecoverable (logged + metered,
// never executed). Name is "" when even the name could not be read.
type PseudoCall struct {
	Name string
	Args json.RawMessage
}

// ExtractPseudoCalls scans text for pseudo-tool-call syntax, returns the text
// with every occurrence removed (clean speech/transcript text), and one
// [PseudoCall] per occurrence in order. It handles three shapes:
//
//   - well-formed `<function=…>…</function>` — parsed for recoverable args;
//   - an UNTERMINATED `<function=…` opener with no close (truncation / the model
//     forgetting the tag) — stripped from the opener to end of text, Args nil
//     (unrecoverable: the args are incomplete);
//   - orphan `</function>` closers left behind when a JSON string arg itself
//     contained the literal `</function>` — stripped so no garbage is spoken.
//
// Text with none of these is returned byte-identical with a nil slice. Excision
// joints are whitespace-collapsed locally (so "Los! <call>" → "Los!") while
// untouched prose — including newlines in Butler markdown — stays byte-identical.
// A whole-message pseudo-call yields clean == "".
func ExtractPseudoCalls(text string) (string, []PseudoCall) {
	locs := pseudoCallRE.FindAllStringSubmatchIndex(text, -1)

	var calls []PseudoCall

	// Prose segments between well-formed matches; each match becomes a PseudoCall.
	segs := make([]string, 0, len(locs)+1)
	prev := 0
	for _, m := range locs {
		segs = append(segs, text[prev:m[0]])
		prev = m[1]
		name := text[m[2]:m[3]]
		args, _ := parseArgs(text[m[4]:m[5]])
		calls = append(calls, PseudoCall{Name: name, Args: args})
	}
	segs = append(segs, text[prev:])

	// Join segments, collapsing whitespace only at the excision joints.
	clean := joinAtSeams(segs)

	// Unterminated opener: everything from the leftover `<function=` to end of
	// text is an incomplete call. Strip it (no recovery) and meter it.
	if idx := strings.Index(clean, pseudoMarker); idx >= 0 {
		name := ""
		if m := openerNameRE.FindStringSubmatch(clean[idx:]); m != nil {
			name = m[1]
		}
		clean = strings.TrimRight(clean[:idx], " \t\r\n")
		calls = append(calls, PseudoCall{Name: name, Args: nil})
	}

	// Orphan closers left by an inner literal </function> in a JSON string arg.
	clean = strings.ReplaceAll(clean, pseudoClose, "")

	if len(calls) == 0 {
		// Byte-identical identity for prose with nothing to scrub.
		return text, nil
	}
	return clean, calls
}

// joinAtSeams concatenates prose segments, trimming whitespace on both sides of
// each seam (where a pseudo-call was excised) and inserting a single space when
// both sides are non-empty. Interior bytes of each segment — including newlines —
// are preserved exactly.
func joinAtSeams(segs []string) string {
	clean := segs[0]
	for _, seg := range segs[1:] {
		l := strings.TrimRight(clean, " \t\r\n")
		r := strings.TrimLeft(seg, " \t\r\n")
		switch {
		case l == "":
			clean = r
		case r == "":
			clean = l
		default:
			clean = l + " " + r
		}
	}
	return clean
}

// parseArgs turns the raw middle of a pseudo-call wrapper into JSON args. It
// tolerates the delimiter noise the models emit around the object — a leading
// `(` or `>` and surrounding whitespace, a trailing `)` — then takes the span
// from the first `{` to the last `}` and validates it. A middle with no braces
// at all is a zero-argument call ("{}"). Anything that does not validate returns
// ok=false so the caller can strip-but-not-execute.
func parseArgs(middle string) (json.RawMessage, bool) {
	s := strings.TrimSpace(middle)
	s = strings.TrimLeft(s, "(> \t\r\n")
	s = strings.TrimRight(s, ") \t\r\n")

	i := strings.IndexByte(s, '{')
	j := strings.LastIndexByte(s, '}')
	if i < 0 && j < 0 {
		return json.RawMessage("{}"), true
	}
	if i < 0 || j < 0 || j < i {
		return nil, false
	}
	cand := s[i : j+1]
	if !json.Valid([]byte(cand)) {
		return nil, false
	}
	return json.RawMessage(cand), true
}
