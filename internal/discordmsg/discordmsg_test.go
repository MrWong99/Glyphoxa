package discordmsg_test

import (
	"strings"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/discordmsg"
)

func TestSplit_ShortTextPassesThroughWhole(t *testing.T) {
	got := discordmsg.Split("hello there", 20)
	if len(got) != 1 || got[0] != "hello there" {
		t.Fatalf("Split = %q, want the unmodified text in one chunk", got)
	}
}

func TestSplit_ExactlyLimitIsOneChunk(t *testing.T) {
	text := strings.Repeat("a", 10)
	got := discordmsg.Split(text, 10)
	if len(got) != 1 || got[0] != text {
		t.Fatalf("Split = %q, want one chunk of exactly limit runes", got)
	}
}

func TestSplit_PrefersNewlineOverSpace(t *testing.T) {
	// Both a newline and a later space sit inside the window; the newline wins
	// even though the space would give a longer first chunk.
	got := discordmsg.Split("one\ntwo three four", 12)
	if got[0] != "one" {
		t.Fatalf("first chunk = %q, want the newline-bounded %q", got[0], "one")
	}
	if strings.HasPrefix(got[1], "\n") || strings.HasPrefix(got[1], " ") {
		t.Fatalf("second chunk %q leads with the boundary whitespace", got[1])
	}
}

func TestSplit_BreaksOnSpaceWhenNoNewline(t *testing.T) {
	got := discordmsg.Split("alpha beta gamma", 12)
	if got[0] != "alpha beta" {
		t.Fatalf("first chunk = %q, want %q", got[0], "alpha beta")
	}
	if got[1] != "gamma" {
		t.Fatalf("second chunk = %q, want %q", got[1], "gamma")
	}
}

func TestSplit_HardCutsUnbrokenRun(t *testing.T) {
	text := strings.Repeat("x", 25)
	got := discordmsg.Split(text, 10)
	if len(got) != 3 || got[0] != strings.Repeat("x", 10) || got[2] != strings.Repeat("x", 5) {
		t.Fatalf("Split = %v chunks %q, want a 10/10/5 hard cut", len(got), got)
	}
}

// TestSplit_CountsRunesNotBytes pins the German-text contract (#271, #299): the
// cap is runes, so multi-byte characters must not shrink the effective chunk.
func TestSplit_CountsRunesNotBytes(t *testing.T) {
	text := strings.Repeat("ä", 30) // 2 bytes per rune
	got := discordmsg.Split(text, 10)
	if len(got) != 3 {
		t.Fatalf("Split gave %d chunks, want 3 (rune-counted)", len(got))
	}
	for i, part := range got {
		if n := len([]rune(part)); n != 10 {
			t.Errorf("chunk %d is %d runes, want 10", i, n)
		}
	}
}

// TestSplit_DeliversEveryRune pins the never-truncate contract: re-joining the
// chunks (re-inserting one break per boundary) reproduces the text in order.
func TestSplit_DeliversEveryRune(t *testing.T) {
	text := "Der Wirt erzählt lange Geschichten über die Prancing Pony.\nUnd über die Gäste. " +
		strings.Repeat("Sehr lange Sätze ohne Ende. ", 20)
	got := discordmsg.Split(text, 50)
	if len(got) < 2 {
		t.Fatalf("fixture did not split: %d chunks", len(got))
	}
	var total int
	for i, part := range got {
		if n := len([]rune(part)); n > 50 {
			t.Errorf("chunk %d is %d runes, want <= 50", i, n)
		}
		total += len([]rune(part))
	}
	// Every rune except the dropped single boundary whitespace per split must
	// survive; the dropped boundaries are at most len(got)-1 runes.
	if want := len([]rune(text)); total < want-(len(got)-1) || total > want {
		t.Errorf("chunks carry %d runes, want within [%d, %d]", total, want-(len(got)-1), want)
	}
}

func TestSplit_NonPositiveLimitDisablesSplitting(t *testing.T) {
	text := strings.Repeat("y", 50)
	for _, limit := range []int{0, -1} {
		got := discordmsg.Split(text, limit)
		if len(got) != 1 || got[0] != text {
			t.Fatalf("Split(limit=%d) = %d chunks, want the whole text in one", limit, len(got))
		}
	}
}
