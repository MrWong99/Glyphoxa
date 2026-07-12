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

// pseudoCallRE matches one `<function=NAME ...>...</function>` occurrence. The
// (?s) flag lets the middle span newlines; the non-greedy body stops at the
// first `</function>` (valid JSON never contains that literal).
var pseudoCallRE = regexp.MustCompile(`(?s)<function=(\w+)(.*?)</function>`)

// PseudoCall is one recovered pseudo-XML tool call: the Tool name and the parsed
// JSON arguments. Args is nil when the wrapper's arguments could not be parsed
// as JSON — the occurrence is still stripped from spoken text, but the [Loop]
// treats a nil-Args call as unrecoverable (logged + metered, never executed).
type PseudoCall struct {
	Name string
	Args json.RawMessage
}

// ExtractPseudoCalls scans text for `<function=…>…</function>` pseudo-tool-call
// syntax, returns the text with every occurrence removed (clean speech/transcript
// text), and one [PseudoCall] per occurrence in order. Text with no occurrence is
// returned unchanged with a nil slice (identity). A whole-message pseudo-call
// yields clean == "".
func ExtractPseudoCalls(text string) (string, []PseudoCall) {
	locs := pseudoCallRE.FindAllStringSubmatchIndex(text, -1)
	if len(locs) == 0 {
		return text, nil
	}

	calls := make([]PseudoCall, 0, len(locs))
	var b strings.Builder
	prev := 0
	for _, m := range locs {
		// m[0:2] full match, m[2:4] name group, m[4:6] middle group.
		b.WriteString(text[prev:m[0]])
		prev = m[1]

		name := text[m[2]:m[3]]
		args, _ := parseArgs(text[m[4]:m[5]])
		calls = append(calls, PseudoCall{Name: name, Args: args})
	}
	b.WriteString(text[prev:])

	return collapseSpace(b.String()), calls
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

// collapseSpace tidies the seam left where a pseudo-call was cut out of prose:
// runs of blank space (incl. newlines) collapse to a single space and the ends
// are trimmed, so "Los, Philipp! <call>" → "Los, Philipp!" rather than leaving a
// dangling double space.
func collapseSpace(s string) string {
	return strings.TrimSpace(spaceRunRE.ReplaceAllString(s, " "))
}

var spaceRunRE = regexp.MustCompile(`[ \t\r\n]+`)
