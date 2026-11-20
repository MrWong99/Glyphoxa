package orchestrator_test

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// These tests pin the consuming half of the STT stage's publish-everything
// contract (#434): an STTFinal whose text is empty or whitespace-only is "the
// recognizer authoritatively heard nothing" — a noise burst that crossed VAD
// onset (cough, breath, chair) — and must route NOWHERE. Zero AddressRouted /
// EnsembleRouted is the whole downstream story: the Replier wakes only on
// those events, so no route means no turn, no history/Hot-Context append, no
// LLM+TTS call, no spend — the Agent never speaks unprompted at a cough. The
// empty final itself stays on the bus (observability and Transcript semantics
// are unchanged).

// TestAddressDetector_EmptyFinal_NoSoleNPCRoute is the one-NPC roster case:
// the sole-active-NPC fallback (weight ≥ threshold with the production
// defaults) is the ambient heuristic that would otherwise route an empty
// utterance. Empty and whitespace-only finals must produce zero routes.
func TestAddressDetector_EmptyFinal_NoSoleNPCRoute(t *testing.T) {
	h := voicetest.New(t)
	butlerAg, bartAg, _ := scoringAgents()
	// Bart is the only non-AddressOnly Agent: the sole-NPC fallback is live.
	d := newScoringDetector(butlerAg, bartAg)
	t.Cleanup(d.Bind(t.Context(), h.Bus))

	for _, text := range []string{"", "   ", " \t\n"} {
		h.Bus.Publish(voiceevent.STTFinal{At: time.Now(), Text: text, TurnID: "T-noise"})
	}

	voicetest.AssertEventCount[voiceevent.AddressRouted](t, h, 0)
	voicetest.AssertEventCount[voiceevent.EnsembleRouted](t, h, 0)
}

// TestAddressDetector_EmptyFollowUp_NoLastAddressedRoute is the two-NPC
// roster case: after a normally-routed named turn, the last-addressed
// heuristic would route an empty follow-up back to the same NPC. The named
// utterance must route exactly as before (the non-empty path is untouched)
// and the empty follow-up must add nothing.
func TestAddressDetector_EmptyFollowUp_NoLastAddressedRoute(t *testing.T) {
	h := voicetest.New(t)
	butlerAg, bartAg, goblinAg := scoringAgents()
	d := newScoringDetector(butlerAg, bartAg, goblinAg)
	t.Cleanup(d.Bind(t.Context(), h.Bus))

	// A real, named utterance routes to Bart and makes him last-addressed.
	h.Bus.Publish(voiceevent.STTFinal{At: time.Now(), Text: "Bart, what's the special tonight?", TurnID: "T-1"})
	voicetest.AssertEvent(t, h,
		func(e voiceevent.AddressRouted) bool {
			return e.Target.AgentID == bartTarget.AgentID && e.TurnID == "T-1"
		},
		"address.routed → Bart for the named utterance (non-empty finals route exactly as before)",
	)

	// The cough that follows transcribes to nothing: no last-speaker route.
	h.Bus.Publish(voiceevent.STTFinal{At: time.Now(), Text: "  ", TurnID: "T-2"})

	voicetest.AssertEventCount[voiceevent.AddressRouted](t, h, 1) // still only the named one
	voicetest.AssertEventCount[voiceevent.EnsembleRouted](t, h, 0)
}

// logCapture is a slog.Handler recording every record at any level, so a test
// can assert WHICH level a line is emitted at.
type logCapture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (c *logCapture) Enabled(context.Context, slog.Level) bool { return true }
func (c *logCapture) WithAttrs([]slog.Attr) slog.Handler       { return c }
func (c *logCapture) WithGroup(string) slog.Handler            { return c }
func (c *logCapture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	c.records = append(c.records, r)
	c.mu.Unlock()
	return nil
}

// levels returns the level of every captured record whose message contains
// substr.
func (c *logCapture) levels(substr string) []slog.Level {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []slog.Level
	for _, r := range c.records {
		if strings.Contains(r.Message, substr) {
			out = append(out, r.Level)
		}
	}
	return out
}

// TestAddressDetector_NoRouteIsLoggedAtDebug pins the operator's view of "why
// did nobody answer": an empty final and an utterance that addressed no Agent
// each leave one Debug line. The empty-final line used to be Info, which at a
// busy table (a noise burst every few seconds) buried the log.
func TestAddressDetector_NoRouteIsLoggedAtDebug(t *testing.T) {
	capture := &logCapture{}
	prev := slog.Default()
	slog.SetDefault(slog.New(capture))
	t.Cleanup(func() { slog.SetDefault(prev) })

	h := voicetest.New(t)
	butlerAg, bartAg, goblinAg := scoringAgents()
	d := newScoringDetector(butlerAg, bartAg, goblinAg)
	t.Cleanup(d.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.STTFinal{At: time.Now(), Text: "  ", TurnID: "T-noise"})
	h.Bus.Publish(voiceevent.STTFinal{At: time.Now(), Text: "let us all roll for initiative", TurnID: "T-chat"})

	for _, msg := range []string{"empty STT final", "utterance routed nowhere"} {
		got := capture.levels(msg)
		if len(got) != 1 || got[0] != slog.LevelDebug {
			t.Errorf("log lines matching %q have levels %v, want exactly one at DEBUG", msg, got)
		}
	}
}
