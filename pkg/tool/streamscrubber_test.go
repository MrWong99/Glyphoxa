package tool

import (
	"strings"
	"testing"
)

// collect runs a streamScrubber over the given deltas and returns everything it
// let through to the sink (Write emits + a final Flush).
func collect(t *testing.T, deltas []string) string {
	t.Helper()
	var got strings.Builder
	sc := &streamScrubber{out: func(s string) error { got.WriteString(s); return nil }}
	for _, d := range deltas {
		if err := sc.Write(d); err != nil {
			t.Fatalf("Write(%q): %v", d, err)
		}
	}
	if err := sc.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	return got.String()
}

// TestStreamScrubberSplitAtEveryIndex is the zero-leak guarantee: no matter WHERE
// the provider chunks the stream, the marked pseudo-call never reaches the sink,
// and the surrounding prose does. The whole message is split at every possible
// byte boundary.
func TestStreamScrubberSplitAtEveryIndex(t *testing.T) {
	full := `Rolling now. <function=dice {"count":1,"sides":20}</function>`
	wantPrefix := "Rolling now."
	for i := 0; i <= len(full); i++ {
		deltas := []string{full[:i], full[i:]}
		got := collect(t, deltas)
		if strings.Contains(got, "<function") || strings.Contains(got, "</function") {
			t.Fatalf("split at %d leaked the marker: %q", i, got)
		}
		if !strings.HasPrefix(got, "Rolling now.") {
			t.Fatalf("split at %d dropped prose: %q", i, got)
		}
		_ = wantPrefix
	}
}

// TestStreamScrubberSplitEveryTwoIndices exercises three-way chunking to catch
// state that survives across more than one boundary.
func TestStreamScrubberSplitEveryTwoIndices(t *testing.T) {
	full := `A <function=dice {"count":1,"sides":6}</function> B is prose.`
	for i := 0; i <= len(full); i++ {
		for j := i; j <= len(full); j++ {
			got := collect(t, []string{full[:i], full[i:j], full[j:]})
			if strings.Contains(got, "function=") {
				t.Fatalf("split (%d,%d) leaked: %q", i, j, got)
			}
		}
	}
}

// TestStreamScrubberLoneAngleBracketFlushed pins that a '<' that is NOT the start
// of a pseudo-call (e.g. "less-than") is emitted as prose, not eaten.
func TestStreamScrubberLoneAngleBracket(t *testing.T) {
	got := collect(t, []string{"3 < 5 and x<y in a <fancy> tag"})
	if got != "3 < 5 and x<y in a <fancy> tag" {
		t.Errorf("lone/partial angle brackets mangled: %q", got)
	}
}

// TestStreamScrubberDivergingPartialMarker pins that a run that starts like the
// marker but diverges ("<func...") is flushed as prose once it is clearly not
// "<function=".
func TestStreamScrubberDivergingPartialMarker(t *testing.T) {
	got := collect(t, []string{"<fun", "kytown"})
	if got != "<funkytown" {
		t.Errorf("diverging partial marker mangled: %q", got)
	}
}

// TestStreamScrubberHoldbackCap pins that a pathological unterminated "<function"
// prefix does not buffer unbounded — past the cap it is flushed as prose.
func TestStreamScrubberHoldbackCap(t *testing.T) {
	// A '<' followed by 5 KiB of 'f' can never complete "<function=", but it is a
	// prefix at each step for the first few bytes; the cap must release it.
	big := "<" + strings.Repeat("f", 5000)
	var got strings.Builder
	sc := &streamScrubber{out: func(s string) error { got.WriteString(s); return nil }}
	if err := sc.Write(big); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := sc.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got.Len() != len(big) {
		t.Errorf("cap should flush prose, emitted %d of %d bytes", got.Len(), len(big))
	}
}

func TestStreamScrubberPlainProsePassesThrough(t *testing.T) {
	got := collect(t, []string{"Hello ", "there, ", "friend."})
	if got != "Hello there, friend." {
		t.Errorf("plain prose altered: %q", got)
	}
}
