package wirenpc

import (
	"testing"
	"time"

	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// bargeMetrics counts BargeCancelled per guild; every other MetricsRecorder method
// is unreachable here, so the embedded nil interface never fires.
type bargeMetrics struct {
	gxvoice.MetricsRecorder
	guilds []string
}

func (m *bargeMetrics) BargeCancelled(guild string) { m.guilds = append(m.guilds, guild) }

// TestCountBargeCancels is A10: glyphoxa_voice_barge_cancels_total was registered
// but never incremented. A confirmed barge — the BargeIn reactor cutting an
// audibly speaking turn — now counts exactly once, and only while subscribed.
func TestCountBargeCancels(t *testing.T) {
	bus := voiceevent.NewBus()
	m := &bargeMetrics{}
	unsub := countBargeCancels(bus, m, "g1")

	floor := orchestrator.NewFloor()
	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), bus))
	ctx := voiceevent.WithTurnID(t.Context(), "t1")
	_, release, _ := floor.Take(ctx, "bart")
	defer release()
	bus.Publish(voiceevent.FirstOpus{At: time.Now(), TurnID: "t1"}) // audibly speaking
	bus.Publish(voiceevent.VADSpeechStart{At: time.Now()})          // instant barge (0 window)

	// Speech with nobody speaking is not a barge.
	bus.Publish(voiceevent.VADSpeechStart{At: time.Now()})

	if len(m.guilds) != 1 || m.guilds[0] != "g1" {
		t.Fatalf("BargeCancelled calls = %v, want exactly one for guild g1", m.guilds)
	}
	unsub()
	bus.Publish(voiceevent.BargeDetected{At: time.Now()})
	if len(m.guilds) != 1 {
		t.Fatalf("counted after unsubscribe: %v", m.guilds)
	}
	countBargeCancels(nil, m, "g1")()  // nil bus: no-op
	countBargeCancels(bus, nil, "g1")() // nil recorder: no-op
}
