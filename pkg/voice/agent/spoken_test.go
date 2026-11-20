package agent

import "testing"

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
	} {
		if got := scrubSpoken(in); got != in {
			t.Errorf("scrubSpoken(%q) = %q, want it unchanged", in, got)
		}
	}
}
