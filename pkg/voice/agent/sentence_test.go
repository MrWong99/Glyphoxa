package agent

import (
	"reflect"
	"sync"
	"testing"
	"time"
)

// feed pushes each delta through the splitter and returns every sentence emitted
// across all pushes plus the final Flush remainder appended (if non-empty), so a
// test can assert the full sentence sequence regardless of how the stream was
// chunked.
func feed(deltas ...string) []string {
	var s sentenceSplitter
	var out []string
	for _, d := range deltas {
		out = append(out, s.Push(d)...)
	}
	if tail := s.Flush(); tail != "" {
		out = append(out, tail)
	}
	return out
}

func TestSentenceSplitter_SplitsOnTerminators(t *testing.T) {
	got := feed("Aye, traveler. Two rooms upstairs! Need anything else?")
	want := []string{"Aye, traveler.", "Two rooms upstairs!", "Need anything else?"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sentences = %q, want %q", got, want)
	}
}

// TestSentenceSplitter_ChunkBoundaryInsensitive is the core streaming property:
// the SAME input split across arbitrary delta boundaries — even mid-word and
// mid-terminator — yields the SAME sentences, never dropping or duplicating one.
func TestSentenceSplitter_ChunkBoundaryInsensitive(t *testing.T) {
	want := []string{"First one.", "Second two!", "Third three?"}
	for _, deltas := range [][]string{
		{"First one. Second two! Third three?"},
		{"First ", "one. Second ", "two! Third ", "three?"},
		{"First one", ". Second two", "! Third three", "?"},
		{"F", "i", "r", "s", "t", " ", "o", "n", "e", ".", " ", "Second two! Third three?"},
	} {
		got := feed(deltas...)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("deltas %q → %q, want %q", deltas, got, want)
		}
	}
}

func TestSentenceSplitter_FlushYieldsUnterminatedTail(t *testing.T) {
	// The common case: the model's last sentence has no trailing terminator+space.
	got := feed("All done.", " No period here")
	want := []string{"All done.", "No period here"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sentences = %q, want %q", got, want)
	}
}

func TestSentenceSplitter_MultiTerminatorRun(t *testing.T) {
	got := feed("Really?! Yes... indeed.")
	want := []string{"Really?!", "Yes...", "indeed."}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sentences = %q, want %q", got, want)
	}
}

// TestSentenceSplitter_MidTokenPunctuationDoesNotSplit pins that a period not
// followed by whitespace — a decimal or an abbreviation mid-token — is NOT a
// boundary, so "3.5" and "v2.0" are not over-split.
func TestSentenceSplitter_MidTokenPunctuationDoesNotSplit(t *testing.T) {
	got := feed("You rolled a 3.5 on a d20. Lucky!")
	want := []string{"You rolled a 3.5 on a d20.", "Lucky!"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sentences = %q, want %q", got, want)
	}
}

// TestSentenceSplitter_TerminatorAtDeltaEndIsNotABoundary pins O4: the model's
// deltas are tokens, so a terminator that happens to be the last rune of the
// buffer says nothing about what follows it. Before the fix "3" "." "5 Gold" was
// spoken as "3." / "5 Gold.", and the second half of "?!" / "..." was dropped as
// an unspeakable sentence of its own.
func TestSentenceSplitter_TerminatorAtDeltaEndIsNotABoundary(t *testing.T) {
	for _, c := range []struct {
		deltas []string
		want   []string
	}{
		{[]string{"Das kostet 3", ".", "5 Gold. Und 1", ".", "000 Taler extra."},
			[]string{"Das kostet 3.5 Gold.", "Und 1.000 Taler extra."}},
		{[]string{"Das kostet 3.", "5 Gold. Und 1.", "000 Taler extra."},
			[]string{"Das kostet 3.5 Gold.", "Und 1.000 Taler extra."}},
		{[]string{"That costs 3", ".", "5 gold. Deal", "?"},
			[]string{"That costs 3.5 gold.", "Deal?"}},
		{[]string{"Wirklich", "?", "!", " Na gut", ".", ".", "."},
			[]string{"Wirklich?!", "Na gut..."}},
		{[]string{"Really", "?", "!", " Fine", ".", ".", ".", " Go on."},
			[]string{"Really?!", "Fine...", "Go on."}},
		{[]string{"\"Halt!", "\" rief er."},
			[]string{"\"Halt!\" rief er."}},
	} {
		if got := feed(c.deltas...); !reflect.DeepEqual(got, c.want) {
			t.Errorf("deltas %q → %q, want %q", c.deltas, got, c.want)
		}
	}
}

// TestSentenceSplitter_SentenceEmittedOnceFollowedByWhitespace pins that the
// buffered terminator costs no more than one token of latency: the sentence is
// released by the whitespace after it, without waiting for the next word.
func TestSentenceSplitter_SentenceEmittedOnceFollowedByWhitespace(t *testing.T) {
	var s sentenceSplitter
	if got := s.Push("Aye, traveler."); got != nil {
		t.Fatalf("terminator at buffer end emitted %q, want nothing yet", got)
	}
	if got := s.Push(" "); !reflect.DeepEqual(got, []string{"Aye, traveler."}) {
		t.Fatalf("after whitespace got %q, want the sentence", got)
	}
}

// TestSentenceSplitter_AbbreviationsOrdinalsAndInitials pins the other half of
// O4: a period that belongs to the token before it does not end the sentence.
// Splitting there is audible (the TTS closes an utterance on "Am dritten."),
// while a missed split only lengthens a chunk.
func TestSentenceSplitter_AbbreviationsOrdinalsAndInitials(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		// German (the auditor's sentence, plus the usual suspects).
		{"Am 3. Mai im 15. Jahrhundert traf ich Dr. Faust, z. B. in der Nr. 5.",
			[]string{"Am 3. Mai im 15. Jahrhundert traf ich Dr. Faust, z. B. in der Nr. 5."}},
		{"Das ist der 2. Versuch. Viel Glück!", []string{"Das ist der 2. Versuch.", "Viel Glück!"}},
		{"Prof. Albrecht bzw. sein Gehilfe wartet. Komm mit.", []string{"Prof. Albrecht bzw. sein Gehilfe wartet.", "Komm mit."}},
		{"Das heißt, d. h. wir gehen. Sofort.", []string{"Das heißt, d. h. wir gehen.", "Sofort."}},
		// A cardinal that ends its sentence still splits — the dice case.
		{"Du hast eine 14. Glückwunsch!", []string{"Du hast eine 14.", "Glückwunsch!"}},
		{"Hier ist dein Wurf: 14. Nicht schlecht.", []string{"Hier ist dein Wurf: 14.", "Nicht schlecht."}},
		// English.
		{"Mr. Smith arrives. Dr. Jones and Mrs. Hale follow.", []string{"Mr. Smith arrives.", "Dr. Jones and Mrs. Hale follow."}},
		{"It was J. R. R. Tolkien, e.g. in the Shire. Truly.", []string{"It was J. R. R. Tolkien, e.g. in the Shire.", "Truly."}},
		{"You rolled a 14. Nice!", []string{"You rolled a 14.", "Nice!"}},
		{"So do I. Anyway, onwards.", []string{"So do I.", "Anyway, onwards."}},
		{"No. I will not. Etc. is no excuse.", []string{"No.", "I will not.", "Etc.", "is no excuse."}},
		// A list number opening the reply or a line is not a sentence of its own.
		{"1. Geh nach Norden.\n2. Frag den Wirt.", []string{"1. Geh nach Norden.", "2. Frag den Wirt."}},
		{"Room B12. Second floor.", []string{"Room B12.", "Second floor."}},
	} {
		if got := feed(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("feed(%q) = %q, want %q", c.in, got, c.want)
		}
		// The same text one rune per delta must segment identically.
		var runes []string
		for _, r := range c.in {
			runes = append(runes, string(r))
		}
		if got := feed(runes...); !reflect.DeepEqual(got, c.want) {
			t.Errorf("feed(per-rune %q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSentenceSplitter_EmptyAndWhitespaceOnly(t *testing.T) {
	if got := feed(); got != nil {
		t.Errorf("no input → %q, want nil", got)
	}
	if got := feed("   ", "\n\t "); got != nil {
		t.Errorf("whitespace-only → %q, want nil (no silent utterances)", got)
	}
	// A stray terminator run with no words must not emit an empty sentence.
	if got := feed(" . . "); got != nil {
		t.Errorf("bare terminators → %q, want nil", got)
	}
}

func TestSentenceSplitter_TagOnlySentenceFiltered(t *testing.T) {
	// eleven_v3 audio/speaker tags ("[laughs]", "[Bart]") contain letters but
	// ElevenLabs strips speaker tags + emojis and then rejects empty input
	// (input_text_empty, HTTP 400) — which drops the whole reply. A sentence that
	// is only such a tag must not be dispatched, via a terminator or the Flush tail.
	if got := feed("[laughs]. "); got != nil {
		t.Errorf("tag-only sentence → %q, want nil (ElevenLabs strips it to empty)", got)
	}
	if got := feed("[laughs]"); got != nil {
		t.Errorf("tag-only Flush tail → %q, want nil", got)
	}
	// Real words plus a trailing tag: keep the words, drop the tag-only tail.
	if got := feed("Aye! [laughs]"); !reflect.DeepEqual(got, []string{"Aye!"}) {
		t.Errorf("words then tag → %q, want [\"Aye!\"]", got)
	}
	// A tag preceding real speech keeps the sentence (ElevenLabs voices the rest).
	if got := feed("[Bart] Hello there."); !reflect.DeepEqual(got, []string{"[Bart] Hello there."}) {
		t.Errorf("tag then words → %q, want [\"[Bart] Hello there.\"]", got)
	}
}

func TestSentenceSplitter_TrailingTerminatorNoSpace(t *testing.T) {
	// A terminator at end-of-input (no following space) still closes the sentence,
	// via Flush.
	got := feed("Done.")
	want := []string{"Done."}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sentences = %q, want %q", got, want)
	}
}

// TestSentenceSplitter_SettleSpeaksAFinishedSentence pins Settle: a buffer that
// ends on a real sentence end is released, one that ends mid-sentence or on an
// abbreviation is left alone.
func TestSentenceSplitter_SettleSpeaksAFinishedSentence(t *testing.T) {
	for _, c := range []struct{ buffered, want string }{
		{"Let me check.", "Let me check."},
		{"Wirklich?!", "Wirklich?!"},
		{"Du hast eine 14.", "Du hast eine 14."},
		{"Let me check", ""},
		{"Frag Dr.", ""},
		{"Wir treffen uns am 3.", ""},
		{"...", ""},
		{"", ""},
	} {
		var s sentenceSplitter
		if got := s.Push(c.buffered); got != nil {
			t.Fatalf("Push(%q) emitted %q, want it buffered", c.buffered, got)
		}
		if got := s.Settle(); got != c.want {
			t.Errorf("Settle after %q = %q, want %q", c.buffered, got, c.want)
		}
		// What Settle did not take is still there for Flush.
		if c.want == "" && hasSpeakable(c.buffered) {
			if got := s.Flush(); got != c.buffered {
				t.Errorf("Flush after a declined Settle(%q) = %q, want the buffer intact", c.buffered, got)
			}
		}
	}
}

// TestSentenceFeed_SettlesASentenceTheStreamPausesOn pins the idle flush: the
// model says "Let me check." and goes quiet (a tool round), so the sentence is
// spoken after sentenceIdleFlush instead of waiting — and gluing itself to —
// the next round's first delta.
func TestSentenceFeed_SettlesASentenceTheStreamPausesOn(t *testing.T) {
	var mu sync.Mutex
	var got []string
	first := make(chan struct{})
	feed := newSentenceFeed(func(s string) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, s)
		if len(got) == 1 {
			close(first)
		}
		return nil
	})
	if err := feed.Write("Let me check."); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first:
	case <-time.After(20 * sentenceIdleFlush):
		t.Fatal("the paused-on sentence was never settled")
	}
	if err := feed.Write("The door is locked. It"); err != nil {
		t.Fatal(err)
	}
	if err := feed.Close(true); err != nil {
		t.Fatal(err)
	}
	want := []string{"Let me check.", "The door is locked.", "It"}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("pushed %q, want %q", got, want)
	}
}

// TestSentenceFeed_ContinuousStreamIsNotSettledEarly pins that the idle flush
// does not bring O4 back: deltas arriving at token pace keep the terminator
// undecided, and a closed feed never pushes again.
func TestSentenceFeed_ContinuousStreamIsNotSettledEarly(t *testing.T) {
	var mu sync.Mutex
	var got []string
	feed := newSentenceFeed(func(s string) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, s)
		return nil
	})
	for _, d := range []string{"Das kostet 3", ".", "5 Gold. Wirklich", "?", "!"} {
		if err := feed.Write(d); err != nil {
			t.Fatal(err)
		}
	}
	if err := feed.Close(false); err != nil { // a cancelled generation: the tail is dropped
		t.Fatal(err)
	}
	time.Sleep(2 * sentenceIdleFlush) // an armed timer must not fire into a closed feed
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"Das kostet 3.5 Gold."}; !reflect.DeepEqual(got, want) {
		t.Errorf("pushed %q, want %q", got, want)
	}
}
