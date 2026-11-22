package tool

import (
	"context"
	"strings"
	"testing"
)

// The element shapes below are written through el()/end() rather than as string
// literals so this file never contains a literal tool-call element itself.

// ns is the namespace prefix some Claude builds put on their tool-call elements.
const ns = "antml" + ":"

// el renders an opening element: el("invoke", `name="dice"`) is the invoke
// opener with that attribute.
func el(name string, attrs ...string) string {
	s := "<" + name
	for _, a := range attrs {
		s += " " + a
	}
	return s + ">"
}

// end renders a closing element.
func end(name string) string { return "</" + name + ">" }

// claudeCall renders the function_calls / invoke / parameter block a Claude
// model writes when it emits a tool call as text, with prefix on every element
// name ("" or ns).
func claudeCall(prefix, toolName string) string {
	return el(prefix+"function_calls") + "\n" +
		el(prefix+"invoke", `name="`+toolName+`"`) + "\n" +
		el(prefix+"parameter", `name="sides"`) + "20" + end(prefix+"parameter") + "\n" +
		end(prefix+"invoke") + "\n" +
		end(prefix+"function_calls")
}

// leakRows are the Claude-shaped (and Hermes/Qwen-shaped) leaks that must never
// reach a voice (#702): each row is a model output and the prose that may be
// spoken from it.
func leakRows() []struct{ name, full, want string } {
	return []struct{ name, full, want string }{
		{"function_calls block", "Moment. " + claudeCall("", "dice") + " Eine Siebzehn!", "Moment. Eine Siebzehn!"},
		{"namespaced function_calls block", "Moment. " + claudeCall(ns, "dice") + " Eine Siebzehn!", "Moment. Eine Siebzehn!"},
		{"bare invoke block", "Ich würfle. " + el("invoke", `name="dice"`) + el("parameter", `name="sides"`) + "6" + end("parameter") + end("invoke"), "Ich würfle."},
		{"lone parameter element", "Also " + el("parameter", `name="query"`) + "Gellan" + end("parameter") + " gut.", "Also gut."},
		{"tool_use block", el("tool_use") + `{"name":"recap","input":{}}` + end("tool_use") + " Na dann.", "Na dann."},
		{"tool_call block", "Klar. " + el("tool_call") + `{"name":"dice","arguments":{"sides":20}}` + end("tool_call"), "Klar."},
		{"thinking block", el("thinking") + "The user wants a roll." + end("thinking") + " Na, was darf's sein?", "Na, was darf's sein?"},
		{"think block", el("think") + "\nSie fragt nach dem Preis.\n" + end("think") + "\nZwei Silber die Nacht.", "Zwei Silber die Nacht."},
		{"unknown namespaced element", "Hm. " + el(ns+"scratch") + "intern" + end(ns+"scratch") + " Ja.", "Hm. Ja."},
		{"unterminated block runs to the end", "Sofort. " + el("function_calls") + el("invoke", `name="dice"`), "Sofort."},
		{"orphan closers", "Gern." + end("invoke") + end(ns+"function_calls") + " Bitte.", "Gern. Bitte."},
	}
}

// leakMarkers are substrings that prove a leak survived.
func leaked(s string) bool {
	for _, name := range []string{"function_calls", "invoke", "tool_use", "tool_call", "parameter", "thinking", "think", ns} {
		if strings.Contains(s, "<"+name) || strings.Contains(s, "</"+name) || strings.Contains(s, ns) {
			return true
		}
	}
	return false
}

// TestExtractPseudoCalls_ClaudeShapesAreStripped pins the batch scrub for the
// leak shapes of #702: the element and everything inside it is removed, the
// prose around it is kept, and every occurrence is reported so the loop meters
// it (never executed: Args is nil).
func TestExtractPseudoCalls_ClaudeShapesAreStripped(t *testing.T) {
	for _, row := range leakRows() {
		clean, calls := ExtractPseudoCalls(row.full)
		if clean != row.want {
			t.Errorf("%s: clean = %q, want %q", row.name, clean, row.want)
		}
		if leaked(clean) {
			t.Errorf("%s: a leak marker survived: %q", row.name, clean)
		}
		if len(calls) == 0 {
			t.Errorf("%s: no occurrence reported, so the loop would not apply the scrub", row.name)
		}
		for _, c := range calls {
			if c.Args != nil {
				t.Errorf("%s: occurrence %+v is executable; these shapes are strip-only", row.name, c)
			}
		}
	}
}

// TestExtractPseudoCalls_ClaudeShapesNameTheTool pins that a stripped call
// still names its Tool where the block says so — the metric and the log line
// are only useful with it.
func TestExtractPseudoCalls_ClaudeShapesNameTheTool(t *testing.T) {
	for _, full := range []string{
		claudeCall("", "dice"),
		claudeCall(ns, "dice"),
		el("tool_call") + `{"name": "dice", "arguments": {}}` + end("tool_call"),
	} {
		_, calls := ExtractPseudoCalls(full)
		if len(calls) != 1 || calls[0].Name != "dice" {
			t.Errorf("%q: calls = %+v, want one occurrence naming dice", full, calls)
		}
	}
}

// TestExtractPseudoCalls_LookalikeProseIsUntouched pins that the wider scrub
// only fires on a real element: prose that merely contains an angle bracket or
// one of the words is returned byte-identical.
func TestExtractPseudoCalls_LookalikeProseIsUntouched(t *testing.T) {
	for _, text := range []string{
		"Ich denke, also bin ich. I think so.",
		"3 < 5 und 7 > 2, thinking about it.",
		"Der Preis ist <zwei Silber> pro Nacht.",
		"<thinker> ist kein Element, <invoker> auch nicht.",
		"Ein loses < am Ende <",
		"Am Ende ein halbes <thin",
	} {
		clean, calls := ExtractPseudoCalls(text)
		if clean != text || calls != nil {
			t.Errorf("ExtractPseudoCalls(%q) = (%q, %+v), want it untouched", text, clean, calls)
		}
	}
}

// TestExtractPseudoCalls_LlamaShapeStillRecovered pins that widening the scrub
// did not weaken the original detection: the llama function= shape is still
// parsed into an executable call.
func TestExtractPseudoCalls_LlamaShapeStillRecovered(t *testing.T) {
	clean, calls := ExtractPseudoCalls(`Los! <function=dice>{"sides":20}</function> ` + el("thinking") + "hm" + end("thinking"))
	if clean != "Los!" {
		t.Errorf("clean = %q, want %q", clean, "Los!")
	}
	if len(calls) != 2 || calls[0].Name != "dice" || string(calls[0].Args) != `{"sides":20}` {
		t.Errorf("calls = %+v, want the recoverable dice call first", calls)
	}
}

// TestStreamScrubber_ClaudeShapesNeverReachTheSink is the streaming half: for
// every leak shape, split at EVERY byte boundary, nothing of the element
// reaches TTS and the prose around it does.
func TestStreamScrubber_ClaudeShapesNeverReachTheSink(t *testing.T) {
	for _, row := range leakRows() {
		for i := 0; i <= len(row.full); i++ {
			got := collect(t, []string{row.full[:i], row.full[i:]})
			if leaked(got) {
				t.Fatalf("%s split at %d leaked: %q", row.name, i, got)
			}
			// The stream scrub does not collapse the whitespace around an excision
			// (the batch scrub does); compare on words.
			if strings.Join(strings.Fields(got), " ") != row.want {
				t.Fatalf("%s split at %d: visible = %q, want the words of %q", row.name, i, got, row.want)
			}
		}
	}
}

// TestStreamScrubber_LookalikeProsePassesThrough pins that prose with an angle
// bracket or a near-miss element name is delivered byte-identical, whatever the
// chunking.
func TestStreamScrubber_LookalikeProsePassesThrough(t *testing.T) {
	for _, text := range []string{
		"3 < 5 und 7 > 2, thinking about it.",
		"<thinker> ist kein Element, <invoker> auch nicht.",
		"Am Ende ein halbes <thin",
		"Ein loses < am Ende <",
	} {
		for i := 0; i <= len(text); i++ {
			if got := collect(t, []string{text[:i], text[i:]}); got != text {
				t.Fatalf("%q split at %d: visible = %q, want it unchanged", text, i, got)
			}
		}
	}
}

// TestRunStream_ClaudeShapedCallIsStrippedMeteredAndNotExecuted pins the loop
// outcome for a Claude-shaped text leak (#702): nothing of the block is
// streamed to the voice, the returned text is clean, the occurrence is metered
// as NOT recovered, and — unlike the llama shape — no tool round is run for it.
func TestRunStream_ClaudeShapedCallIsStrippedMeteredAndNotExecuted(t *testing.T) {
	rec := &pseudoRecorder{}
	p := &scriptedStreamingProvider{
		scriptedProvider: scriptedProvider{
			t: t,
			steps: []scriptStep{
				{reply: AssistantMessage{Text: "Ich würfle. " + claudeCall(ns, "dice") + " Mal sehen."}},
			},
		},
		chunk: 2,
	}
	loop := diceLoop(t, p)
	loop.OnPseudoCall = rec.hook

	var streamed strings.Builder
	final, err := loop.RunStream(context.Background(), []Message{{Role: RoleUser, Text: "roll d20"}},
		func(delta string) error { streamed.WriteString(delta); return nil })
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	if final != "Ich würfle. Mal sehen." {
		t.Errorf("final = %q, want the prose without the block", final)
	}
	if got := streamed.String(); leaked(got) || !strings.Contains(got, "Ich würfle.") || !strings.Contains(got, "Mal sehen.") {
		t.Errorf("streamed = %q, want the prose and nothing of the block", got)
	}
	if len(rec.names) != 1 || rec.names[0] != "dice" || rec.recovered[0] {
		t.Errorf("OnPseudoCall = %+v/%+v, want one (dice, not recovered)", rec.names, rec.recovered)
	}
	if p.calls != 1 {
		t.Errorf("provider called %d times, want 1: a stripped block runs no tool round", p.calls)
	}
}
