package tool

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestExtractPseudoCallsLiveVariants pins the three malformed shapes llama-3.3
// actually emitted in live play (issue #410): the space-separated form, the
// paren-wrapped form, and the bare-brace form. Each must be lifted out of the
// prose with its name + parseable JSON args, and stripped from the clean text.
func TestExtractPseudoCallsLiveVariants(t *testing.T) {
	cases := []struct {
		name     string
		in       string
		wantName string
		wantArgs string
	}{
		{
			name:     "space-separated",
			in:       `Rolling now. <function=dice {"count":1,"sides":20}</function>`,
			wantName: "dice",
			wantArgs: `{"count":1,"sides":20}`,
		},
		{
			name:     "paren-wrapped",
			in:       `Let me check. <function=kg_query({"query":"We-Wetter","limit":5})</function>`,
			wantName: "kg_query",
			wantArgs: `{"query":"We-Wetter","limit":5}`,
		},
		{
			name:     "bare-brace",
			in:       `Los, Philipp! <function=remember_knowledge>{"kind":"edge","relation":"knows","subject":"Gesa","target":"Philipp"}</function>`,
			wantName: "remember_knowledge",
			wantArgs: `{"kind":"edge","relation":"knows","subject":"Gesa","target":"Philipp"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clean, calls := ExtractPseudoCalls(tc.in)
			if strings.Contains(clean, "<function") {
				t.Errorf("clean text still contains the pseudo-call: %q", clean)
			}
			if len(calls) != 1 {
				t.Fatalf("got %d calls, want 1: %+v", len(calls), calls)
			}
			if calls[0].Name != tc.wantName {
				t.Errorf("name = %q, want %q", calls[0].Name, tc.wantName)
			}
			if !jsonEqual(t, calls[0].Args, tc.wantArgs) {
				t.Errorf("args = %s, want %s", calls[0].Args, tc.wantArgs)
			}
		})
	}
}

func TestExtractPseudoCallsPlainProseIsIdentity(t *testing.T) {
	in := "Just a normal sentence with no tool call in it at all."
	clean, calls := ExtractPseudoCalls(in)
	if clean != in {
		t.Errorf("clean = %q, want identity %q", clean, in)
	}
	if calls != nil {
		t.Errorf("plain prose yielded calls: %+v", calls)
	}
}

func TestExtractPseudoCallsWhitespaceAndNewlineLaced(t *testing.T) {
	in := "Ok.\n<function=dice\n  {\n  \"count\": 2,\n  \"sides\": 6\n}\n</function>"
	clean, calls := ExtractPseudoCalls(in)
	if strings.Contains(clean, "function") {
		t.Errorf("clean still has the call: %q", clean)
	}
	if len(calls) != 1 || calls[0].Name != "dice" {
		t.Fatalf("calls = %+v", calls)
	}
	if !jsonEqual(t, calls[0].Args, `{"count":2,"sides":6}`) {
		t.Errorf("args = %s", calls[0].Args)
	}
}

func TestExtractPseudoCallsMultiMatch(t *testing.T) {
	in := `First <function=dice {"count":1,"sides":4}</function> then <function=dice {"count":1,"sides":8}</function> done`
	clean, calls := ExtractPseudoCalls(in)
	if strings.Contains(clean, "<function") {
		t.Errorf("clean still has a call: %q", clean)
	}
	if len(calls) != 2 {
		t.Fatalf("got %d calls, want 2", len(calls))
	}
}

func TestExtractPseudoCallsNestedBraces(t *testing.T) {
	in := `<function=remember_knowledge>{"kind":"edge","payload":{"a":1,"b":{"c":2}}}</function>`
	clean, calls := ExtractPseudoCalls(in)
	if clean != "" {
		t.Errorf("whole-message call should leave empty clean, got %q", clean)
	}
	if len(calls) != 1 {
		t.Fatalf("got %d calls", len(calls))
	}
	if !jsonEqual(t, calls[0].Args, `{"kind":"edge","payload":{"a":1,"b":{"c":2}}}`) {
		t.Errorf("nested-brace args mangled: %s", calls[0].Args)
	}
}

// TestExtractPseudoCallsJunkStrippedNotExecutable pins that a matching
// <function=…> wrapper whose arguments are NOT valid JSON is still lifted out of
// the spoken text (strip-only) but carries nil Args so the Loop treats it as
// unrecoverable rather than executing garbage.
func TestExtractPseudoCallsJunkStrippedNotExecutable(t *testing.T) {
	in := `Hmm <function=dice {not json at all ]</function> ok`
	clean, calls := ExtractPseudoCalls(in)
	if strings.Contains(clean, "function") {
		t.Errorf("junk call not stripped from clean: %q", clean)
	}
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
	if calls[0].Args != nil {
		t.Errorf("junk args should be nil (unparseable), got %s", calls[0].Args)
	}
}

func TestExtractPseudoCallsNoArgsIsEmptyObject(t *testing.T) {
	in := `<function=recap></function>`
	_, calls := ExtractPseudoCalls(in)
	if len(calls) != 1 {
		t.Fatalf("got %d calls", len(calls))
	}
	if !jsonEqual(t, calls[0].Args, `{}`) {
		t.Errorf("no-brace args should default to {}, got %s", calls[0].Args)
	}
}

func jsonEqual(t *testing.T, raw json.RawMessage, want string) bool {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(raw, &a); err != nil {
		return false
	}
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatalf("bad want json: %v", err)
	}
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}
