package recap

import (
	"strings"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

func TestRenderLine(t *testing.T) {
	tests := []struct {
		name string
		line storage.TranscriptLine
		want string
	}{
		{name: "with tag", line: storage.TranscriptLine{Who: "Bart", Tag: "npc", Text: "Well met."}, want: "Bart (npc): Well met.\n"},
		{name: "no tag omits parens", line: storage.TranscriptLine{Who: "GM", Tag: "", Text: "You enter the tavern."}, want: "GM: You enter the tavern.\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := renderLine(tt.line); got != tt.want {
				t.Errorf("renderLine() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSplitWindows(t *testing.T) {
	// Each rendered line "X: yyyy\n"; make lengths easy to reason about.
	lines := []storage.TranscriptLine{
		{Who: "A", Text: "one"},   // "A: one\n" = 7
		{Who: "B", Text: "two"},   // 7
		{Who: "C", Text: "three"}, // "C: three\n" = 9
	}

	t.Run("all fit one window", func(t *testing.T) {
		w := splitWindows(lines, 1000)
		if len(w) != 1 {
			t.Fatalf("got %d windows, want 1", len(w))
		}
		if len(w[0]) != 3 {
			t.Errorf("window has %d lines, want 3", len(w[0]))
		}
	})

	t.Run("splits on budget into whole lines", func(t *testing.T) {
		// Budget 14 fits two 7-char lines but not the third (9).
		w := splitWindows(lines, 14)
		if len(w) != 2 {
			t.Fatalf("got %d windows, want 2", len(w))
		}
		if len(w[0]) != 2 || len(w[1]) != 1 {
			t.Errorf("window sizes = %d,%d; want 2,1", len(w[0]), len(w[1]))
		}
	})

	t.Run("oversized single line gets its own window", func(t *testing.T) {
		big := storage.TranscriptLine{Who: "X", Text: strings.Repeat("z", 100)}
		w := splitWindows([]storage.TranscriptLine{big}, 10)
		if len(w) != 1 || len(w[0]) != 1 {
			t.Fatalf("oversized line: got %d windows, want 1 with 1 line", len(w))
		}
	})

	t.Run("no lines -> nil", func(t *testing.T) {
		if w := splitWindows(nil, 10); w != nil {
			t.Errorf("splitWindows(nil) = %v, want nil", w)
		}
	})
}
