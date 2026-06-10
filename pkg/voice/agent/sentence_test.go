package agent

import (
	"reflect"
	"testing"
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

// TestSentenceSplitter_AbbreviationOverSplitIsTolerated documents the known,
// benign limitation: "Mr. Smith" splits after "Mr." because a space follows. We
// pin it so the behavior is intentional, not accidental — it is harmless for TTS
// (a slightly shorter first chunk) and avoiding it would need an abbreviation
// lexicon the voice loop does not warrant.
func TestSentenceSplitter_AbbreviationOverSplitIsTolerated(t *testing.T) {
	got := feed("Mr. Smith arrives.")
	want := []string{"Mr.", "Smith arrives."}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sentences = %q, want %q (documented over-split)", got, want)
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
	// A terminator at end-of-input (no following space) still closes the sentence.
	got := feed("Done.")
	want := []string{"Done."}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sentences = %q, want %q", got, want)
	}
}
