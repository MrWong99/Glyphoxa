package tool

import (
	"regexp"
	"strings"
)

// This file widens the pseudo-call scrub (#410) to the element shapes other
// model families write when a tool call or their reasoning leaks into visible
// text (#702): Claude's function_calls / invoke / parameter block (with or
// without its namespace prefix), the tool_use and tool_call wrappers (the
// latter is the Hermes/Qwen shape), and thinking / think reasoning blocks.
//
// Unlike the llama `<function=…>` shape these are STRIP-ONLY: the element and
// everything inside it is removed from what is spoken and persisted, and the
// occurrence is reported (so it is logged and metered), but it is never parsed
// into an executable call. Recovering one would mean trusting argument typing
// the wrapper does not carry (Claude's parameter values are untyped strings).
//
// The matcher is shared by the batch scrub ([ExtractPseudoCalls]) and the
// stream scrub ([streamScrubber]) so the two cannot drift.

// leakElements are the element names whose blocks never reach a voice.
var leakElements = []string{
	"function_calls", "invoke", "parameter", // Claude's tool-call block
	"tool_use", "tool_call", // JSON-in-element wrappers (Hermes/Qwen)
	"thinking", "think", // leaked reasoning
}

// leakNamespace is the prefix some Claude builds put on their element names.
// ANY element carrying it is internal markup, whatever its name.
const leakNamespace = "antml:"

// maxLeakNameLen bounds an unknown namespaced element name, and with it how
// much trailing text the stream scrubber may hold back waiting for one to end.
const maxLeakNameLen = 40

// elementMatch is how far a text starting at '<' matches a leak element.
type elementMatch int

const (
	elementNone    elementMatch = iota // not a leak element: ordinary prose
	elementPartial                     // could still become one with more input
	elementFull                        // is one
)

// matchLeakOpener classifies s, which starts with '<', as the opening tag of a
// leak element. On [elementFull] name is the element's bare name (namespace
// stripped). An opener is the name followed by '>', '/', or whitespace — so
// "<thinker>" or "<invoker>" is prose.
func matchLeakOpener(s string) (name string, m elementMatch) {
	rest := s[1:]
	if strings.HasPrefix(rest, "/") {
		return "", elementNone // a closer; see matchLeakCloser
	}
	namespaced := false
	switch {
	case strings.HasPrefix(rest, leakNamespace):
		rest, namespaced = rest[len(leakNamespace):], true
	case strings.HasPrefix(leakNamespace, rest):
		return "", elementPartial // "<", "<a", … "<antml": may become the prefix
	}

	partial := false
	for _, el := range leakElements {
		switch {
		case len(rest) > len(el) && strings.HasPrefix(rest, el):
			if isElementBoundary(rest[len(el)]) {
				return el, elementFull
			}
		case strings.HasPrefix(el, rest):
			partial = true // rest is the name so far (or all of it, boundary pending)
		}
	}
	if partial {
		return "", elementPartial
	}
	if namespaced {
		// An element we do not know by name, but the namespace marks it internal.
		n := wordLen(rest)
		switch {
		case n == 0 || n > maxLeakNameLen:
			return "", elementNone
		case n == len(rest):
			return "", elementPartial
		case isElementBoundary(rest[n]):
			return rest[:n], elementFull
		}
	}
	return "", elementNone
}

// matchLeakCloser classifies s, which starts with '<', as the closing tag of a
// leak element; on [elementFull] n is the closer's byte length.
func matchLeakCloser(s string) (n int, m elementMatch) {
	if len(s) < 2 {
		return 0, elementPartial // "<": may become "</…"
	}
	if s[1] != '/' {
		return 0, elementNone
	}
	rest := s[2:]
	consumed := 2
	namespaced := false
	switch {
	case strings.HasPrefix(rest, leakNamespace):
		rest, namespaced = rest[len(leakNamespace):], true
		consumed += len(leakNamespace)
	case strings.HasPrefix(leakNamespace, rest):
		return 0, elementPartial
	}

	partial := false
	for _, el := range leakElements {
		closer := el + ">"
		switch {
		case strings.HasPrefix(rest, closer):
			return consumed + len(closer), elementFull
		case strings.HasPrefix(closer, rest):
			partial = true
		}
	}
	if partial {
		return 0, elementPartial
	}
	if namespaced {
		n := wordLen(rest)
		switch {
		case n == 0 || n > maxLeakNameLen:
			return 0, elementNone
		case n == len(rest):
			return 0, elementPartial
		case rest[n] == '>':
			return consumed + n + 1, elementFull
		}
	}
	return 0, elementNone
}

// leakClosers are the closing tags that end a swallowed element named name:
// the bare one and the namespaced one (a model is not always consistent).
func leakClosers(name string) []string {
	return []string{"</" + name + ">", "</" + leakNamespace + name + ">"}
}

// isElementBoundary reports whether c may follow an element name in a tag.
func isElementBoundary(c byte) bool {
	return c == '>' || c == '/' || c == ' ' || c == '\t' || c == '\r' || c == '\n'
}

// wordLen is the length of the leading run of element-name bytes in s.
func wordLen(s string) int {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '_' && c != '-' && (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return i
		}
	}
	return len(s)
}

// indexFirst returns the earliest occurrence of any marker in s and that
// marker's length; idx is -1 when none occurs.
func indexFirst(s string, markers []string) (idx, n int) {
	idx = -1
	for _, m := range markers {
		if k := strings.Index(s, m); k >= 0 && (idx < 0 || k < idx) {
			idx, n = k, len(m)
		}
	}
	return idx, n
}

// leakToolNameRE reads the Tool a stripped block named: the name attribute of
// an invoke element, or the "name" member of a tool_use / tool_call JSON body.
var leakToolNameRE = regexp.MustCompile(`invoke\s+name="([\w-]+)"|"name"\s*:\s*"([\w-]+)"`)

// leakToolName returns the Tool name a stripped block carries, "" if it names
// none (a reasoning block, a lone parameter).
func leakToolName(block string) string {
	m := leakToolNameRE.FindStringSubmatch(block)
	if m == nil {
		return ""
	}
	if m[1] != "" {
		return m[1]
	}
	return m[2]
}

// stripLeakElements removes every leak element (through its closer, or to the
// end of the text when it is never closed) and every orphan closer from text.
// It returns the cleaned text and one un-executable [PseudoCall] per removal;
// text without any is returned unchanged with a nil slice.
func stripLeakElements(text string) (string, []PseudoCall) {
	if !strings.Contains(text, "<") {
		return text, nil
	}
	var calls []PseudoCall
	clean := text
	for from := 0; from < len(clean); {
		i := strings.IndexByte(clean[from:], '<')
		if i < 0 {
			break
		}
		i += from

		if n, m := matchLeakCloser(clean[i:]); m == elementFull {
			// An orphan closer: its opener was cut off, or sat in an earlier round.
			clean = clean[:i] + clean[i+n:]
			calls = append(calls, PseudoCall{})
			from = i
			continue
		}
		name, m := matchLeakOpener(clean[i:])
		if m != elementFull {
			from = i + 1 // prose (a partial tag at the very end is prose too)
			continue
		}
		block := clean[i:]
		after := ""
		if k, n := indexFirst(block, leakClosers(name)); k >= 0 {
			block, after = block[:k+n], block[k+n:]
		}
		calls = append(calls, PseudoCall{Name: leakToolName(block)})
		before := clean[:i]
		clean = joinAtSeams([]string{before, after})
		// Rescan from the seam: joinAtSeams only trims whitespace there.
		from = len(strings.TrimRight(before, " \t\r\n"))
	}
	if len(calls) == 0 {
		return text, nil
	}
	return clean, calls
}
