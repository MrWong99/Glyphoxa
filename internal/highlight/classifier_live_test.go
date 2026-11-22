//go:build live

// Live check of the Session Highlight classifier on the Anthropic voice model
// (#702). Excluded from the default keyless suite by the `live` tag: it makes
// PAID calls and only runs with `go test -tags=live` and ANTHROPIC_API_KEY set
// (an API key, or the `ant` CLI's OAuth access token).
package highlight

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm/anthropic"
)

// TestLive_ClassifierFitsItsBudgetOnTheHaikuVoiceModel checks the one request
// in the tree that gives the voice model a small completion budget. A Voice
// Session on an Anthropic key classifies highlights with the session's voice
// model, claude-haiku-5-5, for which the adapter turns adaptive thinking on —
// and thinking tokens share max_tokens (256 here). If the model spent the
// budget thinking, the reply would end at max_tokens with no JSON and the
// detector, which is best-effort, would silently score nothing for the whole
// session. The request is the production one ([buildRequest]); every reply
// must end normally and parse.
func TestLive_ClassifierFitsItsBudgetOnTheHaikuVoiceModel(t *testing.T) {
	if os.Getenv(anthropic.APIKeyEnv) == "" {
		t.Skipf("%s not set", anthropic.APIKeyEnv)
	}
	windows := map[string][]finalLine{
		"quiet": {
			{speaker: "1", text: "Ich nehme noch ein Bier, Flo."},
			{speaker: "2", text: "Wann brechen wir morgen auf?"},
			{speaker: "1", text: "Bei Sonnenaufgang, würde ich sagen."},
		},
		"dramatic": {
			{speaker: "1", text: "Ich stoße die Tür auf und stürme mit gezogenem Schwert in den Thronsaal!"},
			{speaker: "2", text: "Warte, nein! Das ist eine Falle, der Boden gibt nach!"},
			{speaker: "3", text: "Natürliche Zwanzig! Ich packe ihn am Kragen, bevor er fällt!"},
			{speaker: "1", text: "Oh mein Gott, das war knapp. Der König ist der Verräter, ich wusste es!"},
			{speaker: "2", text: "Er hat die ganze Zeit gelogen. Alles, was wir wussten, war falsch."},
		},
	}
	client := anthropic.New("")
	const runs = 4
	for name, lines := range windows {
		for run := 1; run <= runs; run++ {
			req := buildRequest(anthropic.HaikuVoiceModel, lines, "Audio: lane 1 peak 0.82, lane 2 peak 0.64, overlap 0.3s.")
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			ch, err := client.Complete(ctx, req)
			if err != nil {
				cancel()
				t.Fatalf("%s #%d: Complete: %v", name, run, err)
			}
			var text strings.Builder
			stop, out := "", 0
			for ev := range ch {
				switch ev.Type {
				case llm.EventText:
					text.WriteString(ev.Text)
				case llm.EventUsage:
					out = ev.Usage.OutputTokens
				case llm.EventDone:
					stop = ev.StopReason
				case llm.EventError:
					t.Errorf("%s #%d: stream error: %s", name, run, ev.Err)
				}
			}
			cancel()
			c, ok := parseClassification(text.String())
			t.Logf("%s #%d: stop=%s output_tokens=%d (budget %d) parsed=%v %+v", name, run, stop, out, req.MaxTokens, ok, c)
			if stop != "end_turn" {
				t.Errorf("%s #%d: stop_reason = %q, want end_turn (max_tokens means thinking ate the budget)", name, run, stop)
			}
			if !ok {
				t.Errorf("%s #%d: reply does not parse as a classification: %q", name, run, text.String())
			}
		}
	}
}
