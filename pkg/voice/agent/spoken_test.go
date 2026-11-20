package agent

import (
	"context"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// TestScrubSpoken_RemovesMarkdownKeepsContent pins O12's delivery-path scrub
// against the shapes a chat-trained model actually emits into a voice turn.
func TestScrubSpoken_RemovesMarkdownKeepsContent(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"bold", "**Wichtig:** Der Schlüssel liegt im Keller.", "Wichtig: Der Schlüssel liegt im Keller."},
		{"italic", "Das war *wirklich* knapp.", "Das war wirklich knapp."},
		{"underscore emphasis", "That was _really_ close, __friend__.", "That was really close, friend."},
		{"bold italic", "***Halt!***", "Halt!"},
		{"roleplay action", "*lacht leise* Na gut.", "lacht leise Na gut."},
		{"emphasis cut by the sentence boundary, opener", "**Achtung!", "Achtung!"},
		{"emphasis cut by the sentence boundary, closer", "Gefahr im Verzug.**", "Gefahr im Verzug."},
		{"heading", "## Die Taverne", "Die Taverne"},
		{"bullets", "Du brauchst:\n- ein Schwert\n* einen Schild\n• einen Trank.", "Du brauchst:\nein Schwert\neinen Schild\neinen Trank."},
		{"numbered list keeps its numbers", "1. Geh nach Norden.", "1. Geh nach Norden."},
		{"blockquote", "> So sprach der König.", "So sprach der König."},
		{"bullet in front of a word, not a number", "- fünf Grad draußen.", "fünf Grad draußen."},
		{"quoted emphasis", "Er rief \"*Halt!*\" und rannte.", "Er rief \"Halt!\" und rannte."},
		{"inline code", "Sag `Mellon` und tritt ein.", "Sag Mellon und tritt ein."},
		{"code fence", "```json\nkein Code hier\n```", "kein Code hier"},
		{"audio tag with emphasis", "[cheerfully] **Hier** ist dein Wurf: Vierzehn.", "[cheerfully] Hier ist dein Wurf: Vierzehn."},
	} {
		if got := scrubSpoken(c.in); got != c.want {
			t.Errorf("%s: scrubSpoken(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// TestScrubSpoken_LeavesSpeechAlone pins the conservative half: audio tags, math
// and names that merely CONTAIN a markdown character pass through byte-identical.
func TestScrubSpoken_LeavesSpeechAlone(t *testing.T) {
	for _, in := range []string{
		"[cheerfully]Hier ist dein Wurf: Vierzehn.",
		"[whispers] Komm näher. [laughs]",
		"[cheerful ?][?] [....] Hallo.",
		"Das macht 3 * 4 = 12 Goldstücke.",
		"Zwei mal drei, also 2*3, ergibt sechs.",
		"Ever watched M*A*S*H?",
		"Die Datei heißt wurf_tabelle.",
		"Es sind -5 Grad, und Raum #1 ist frei.",
		"Der Pfad führt von A -> B.",
		"Halt - wer da?",
		"Rock'n'Roll + Met, das ist das Leben.",
		"Aye, traveler. Two rooms upstairs!",
		// Signs and comparisons in front of a number are not bullets or quotes (F6).
		"- 5 Grad draußen.",
		"+ 2 auf den Wurf.",
		"> 5 Gold?",
		// Asterisks that are neither an opener nor a closer of emphasis (F6).
		"Das sind 5*(2+1) Münzen.",
		"F*** off.",
		"What the f***!",
	} {
		if got := scrubSpoken(in); got != in {
			t.Errorf("scrubSpoken(%q) = %q, want it unchanged", in, got)
		}
	}
}

// TestDeliveredText_ScrubsBeforeStrippingTheSelfLabel pins F7: a speaker label
// hidden behind a list marker is only at the start of the sentence once the
// markdown is gone, so the scrub has to run first.
func TestDeliveredText_ScrubsBeforeStrippingTheSelfLabel(t *testing.T) {
	for in, want := range map[string]string{
		"- Mira: Hallo, Reisende.": "Hallo, Reisende.",
		"## Mira: Guten Abend.":    "Guten Abend.",
		"**Mira:** Na endlich.":    "Na endlich.",
		"> Mira: So ist es.":       "So ist es.",
	} {
		var spoken []string
		d := &deliveredText{selfName: "Mira", dispatch: func(rep orchestrator.Reply) error {
			spoken = append(spoken, rep.Sentence)
			return nil
		}}
		if err := d.emit(in); err != nil {
			t.Fatalf("emit(%q): %v", in, err)
		}
		if len(spoken) != 1 || spoken[0] != want || d.text() != want {
			t.Errorf("emit(%q) spoke %q and committed %q, want %q", in, spoken, d.text(), want)
		}
	}
}

// nopEngine satisfies [Engine] for tests that drive the history directly.
type nopEngine struct{}

func (nopEngine) Generate(context.Context, []llm.Message) (string, error) { return "", nil }

// nopSynth satisfies [tts.Synthesizer] for the same tests.
type nopSynth struct{}

func (nopSynth) Synthesize(context.Context, tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	ch := make(chan tts.AudioChunk)
	close(ch)
	return ch, nil
}
func (nopSynth) AudioMarkupPrompt(tts.Voice) string { return "" }

// TestCommitSpoken_AnchorTrimmedAway_DropsTheLateCommit pins F10: a turn whose
// user message has been trimmed out of the window while it was still speaking
// commits nothing — appended at the end, its reply would be filed under a
// question it does not answer.
func TestCommitSpoken_AnchorTrimmedAway_DropsTheLateCommit(t *testing.T) {
	r := NewReplier(Config{Engine: nopEngine{}, Synthesizer: nopSynth{}, HistoryTurns: 1})
	old := r.appendUser("question A")
	r.appendUser("question B")
	current := r.appendUser("question C") // 3 > 2*HistoryTurns: the window is cut back to [C]

	r.commitSpoken(old, "late answer to A")
	if hist := r.HistorySnapshot(); len(hist) != 1 || hist[0].Text != "question C" {
		t.Fatalf("history = %+v, want only [question C] (the late commit dropped)", hist)
	}
	r.commitSpoken(current, "answer to C")
	hist := r.HistorySnapshot()
	if len(hist) != 2 || hist[1].Role != llm.RoleAssistant || hist[1].Text != "answer to C" {
		t.Fatalf("history = %+v, want [question C, answer to C]", hist)
	}
}
