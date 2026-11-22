package llmbench

import (
	"slices"
	"testing"
)

// TestScripts_CoverTheRequiredGround pins the corpus contract of #702: at least
// 30 turns in total, and every behaviour the comparison is about is exercised.
func TestScripts_CoverTheRequiredGround(t *testing.T) {
	scripts := Scripts()

	total := 0
	tags := map[string]int{}
	for _, s := range scripts {
		if len(s.Turns) < 2 {
			t.Errorf("script %q has %d turns; history can only matter in a multi-turn script", s.Name, len(s.Turns))
		}
		for i, turn := range s.Turns {
			total++
			if turn.Text == "" || turn.Speaker == "" {
				t.Errorf("%s[%d]: empty text or speaker", s.Name, i)
			}
			if len(turn.Tags) == 0 {
				t.Errorf("%s[%d]: no category tag", s.Name, i)
			}
			for _, tag := range turn.Tags {
				tags[tag]++
			}
			if turn.Speaker == SpeakerMira && !slices.Contains(turn.Tags, TagFemalePC) {
				t.Errorf("%s[%d]: Mira's turn is not tagged %s", s.Name, i, TagFemalePC)
			}
		}
	}
	if total < 30 {
		t.Errorf("corpus has %d turns, want at least 30 per candidate", total)
	}
	for _, tag := range []string{
		TagSmallTalk, TagDiceSingle, TagDiceMulti, TagDiceSum, TagRecap,
		TagRememberAccept, TagRememberRefuse, TagKGLookup, TagTranscriptLookup,
		TagNoToolFits, TagReasoningBait, TagMustNotCall, TagFemalePC,
	} {
		if tags[tag] == 0 {
			t.Errorf("no turn covers %q", tag)
		}
	}
}

// TestScripts_ExpectationsNameRealTools pins that every expected or allowed
// Tool is one the bench actually grants.
func TestScripts_ExpectationsNameRealTools(t *testing.T) {
	granted := GrantedTools()
	for _, s := range Scripts() {
		for i, turn := range s.Turns {
			for _, name := range append(append([]string{}, turn.Expect...), turn.Allow...) {
				if !slices.Contains(granted, name) {
					t.Errorf("%s[%d]: tool %q is not granted (%v)", s.Name, i, name, granted)
				}
			}
		}
	}
}

func TestTurn_ToolsOK(t *testing.T) {
	for _, tc := range []struct {
		name          string
		expect, allow []string
		called        []string
		want          bool
	}{
		{"expected tool called", []string{"dice"}, nil, []string{"dice"}, true},
		{"expected tool called twice", []string{"dice"}, nil, []string{"dice", "dice"}, true},
		{"expected tool missing", []string{"dice"}, nil, nil, false},
		{"allowed extra tool", []string{"dice"}, []string{"kg_query"}, []string{"dice", "kg_query"}, true},
		{"unexpected extra tool", []string{"dice"}, nil, []string{"dice", "recap"}, false},
		{"no tool wanted, none called", nil, nil, nil, true},
		{"no tool wanted, one called", nil, nil, []string{"dice"}, false},
		{"optional tool not called", nil, []string{"remember_knowledge"}, nil, true},
		{"optional tool called", nil, []string{"remember_knowledge"}, []string{"remember_knowledge"}, true},
	} {
		turn := Turn{Expect: tc.expect, Allow: tc.allow}
		if got := turn.ToolsOK(tc.called); got != tc.want {
			t.Errorf("%s: ToolsOK(%v) = %v, want %v", tc.name, tc.called, got, tc.want)
		}
	}
}
