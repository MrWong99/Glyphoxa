package recall

import "testing"

// TestNormalize_VariantsMatch pins the self-heal key (ADR-0042): case,
// trailing/embedded punctuation and whitespace jitter all fold to the same
// comparison form, so a speculated partial matches its STTFinal.
func TestNormalize_VariantsMatch(t *testing.T) {
	canonical := "do you remember the ruby dagger"
	variants := []string{
		"Do you remember the Ruby Dagger?",
		"do you remember the ruby dagger",
		"  DO   you remember the ruby dagger!!!  ",
		"Do you remember the ruby dagger...",
		"do you, remember the ruby dagger",
	}
	for _, v := range variants {
		if got := normalize(v); got != canonical {
			t.Errorf("normalize(%q) = %q, want %q", v, got, canonical)
		}
	}
}

// TestNormalize_DifferentWordsDiffer pins that distinct utterances do NOT
// collapse together — a mismatch must fall back to inline retrieval, not reuse a
// stale prefetch.
func TestNormalize_DifferentWordsDiffer(t *testing.T) {
	if normalize("the ruby dagger") == normalize("the golden crown") {
		t.Error("distinct utterances must normalize differently")
	}
	if normalize("") != "" {
		t.Errorf("normalize(empty) = %q, want empty", normalize(""))
	}
	if normalize("!!! ??? ...") != "" {
		t.Errorf("normalize(punct-only) = %q, want empty", normalize("!!! ??? ..."))
	}
}
