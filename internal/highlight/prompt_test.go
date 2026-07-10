package highlight

import "testing"

// TestParseClassification pins the tolerant JSON extraction: a fenced or
// prose-wrapped object parses, and garbage degrades to a zero score (a classify
// never crashes the worker — the moment is simply not confirmed).
func TestParseClassification(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantScore float64
		wantExc   string
	}{
		{
			name:      "bare object",
			in:        `{"score": 9.5, "excerpt": "nat 20", "reason": "crit"}`,
			wantScore: 9.5,
			wantExc:   "nat 20",
		},
		{
			name:      "fenced code block",
			in:        "```json\n{\"score\": 7.0, \"excerpt\": \"clutch\", \"reason\": \"save\"}\n```",
			wantScore: 7.0,
			wantExc:   "clutch",
		},
		{
			name:      "prose wrapped",
			in:        `Sure, here is my verdict: {"score": 3.5, "excerpt": "meh", "reason": "chatter"} hope that helps!`,
			wantScore: 3.5,
			wantExc:   "meh",
		},
		{name: "no json", in: "I could not decide.", wantScore: 0, wantExc: ""},
		{name: "malformed json", in: `{"score": not-a-number}`, wantScore: 0, wantExc: ""},
		{name: "empty", in: "", wantScore: 0, wantExc: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseClassification(tc.in)
			if got.score != tc.wantScore {
				t.Errorf("score = %v, want %v", got.score, tc.wantScore)
			}
			if got.excerpt != tc.wantExc {
				t.Errorf("excerpt = %q, want %q", got.excerpt, tc.wantExc)
			}
		})
	}
}
