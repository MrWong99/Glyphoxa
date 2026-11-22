package llmbench

import (
	"strings"
	"testing"
)

func names(cs []Candidate) string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return strings.Join(out, ",")
}

// TestCandidates_TableHasTheRequiredArms pins the arms #702 asks for.
func TestCandidates_TableHasTheRequiredArms(t *testing.T) {
	got := names(Candidates())
	for _, want := range []string{"groq-gpt-oss-120b", "haiku-5-5-adaptive-low", "haiku-5-5-thinking-off", "haiku-4-5", "groq-qwen3.8-27b"} {
		if !strings.Contains(got, want) {
			t.Errorf("candidate table %q lacks %q", got, want)
		}
	}
	for _, c := range Candidates() {
		if c.New == nil || c.New() == nil || c.Model == "" || c.Provider == "" || c.KeyEnv == "" {
			t.Errorf("candidate %q is incomplete: %+v", c.Name, c)
		}
	}
}

func TestSelectCandidates(t *testing.T) {
	all := Candidates()

	def, err := SelectCandidates(all, "")
	if err != nil || names(def) != "groq-gpt-oss-120b,haiku-5-5-adaptive-low,haiku-5-5-thinking-off,haiku-4-5" {
		t.Errorf(`"" selected %q (err %v), want the four non-optional arms`, names(def), err)
	}
	if every, _ := SelectCandidates(all, "all"); len(every) != len(all) {
		t.Errorf(`"all" selected %d arms, want %d`, len(every), len(all))
	}
	sub, err := SelectCandidates(all, " haiku-4-5 , groq-qwen3.8-27b ")
	if err != nil || names(sub) != "haiku-4-5,groq-qwen3.8-27b" {
		t.Errorf("subset selected %q (err %v)", names(sub), err)
	}
	if _, err := SelectCandidates(all, "haiku-9"); err == nil || !strings.Contains(err.Error(), "haiku-9") {
		t.Errorf("unknown name: err = %v, want it named", err)
	}
}

func TestMissingKeys(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "x")
	t.Setenv("ANTHROPIC_API_KEY", "")
	if got := strings.Join(MissingKeys(Candidates()), ","); got != "ANTHROPIC_API_KEY" {
		t.Errorf("MissingKeys = %q, want only the unset Anthropic variable, once", got)
	}
}
