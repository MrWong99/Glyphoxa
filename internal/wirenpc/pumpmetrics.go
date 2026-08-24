package wirenpc

import (
	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/pkg/voice/wire"
)

// pumpRecorderOptions adds the playback-gap / look-ahead recorder to the pump when
// the configured StageRecorder can actually record them (#606). The pump's
// [wire.PumpRecorder] is deliberately NOT part of [observe.StageRecorder] — the gap
// is a pump measurement, not an orchestrator stage — so the concrete adapter is
// recovered by assertion: the Prometheus recorder satisfies it, [observe.Discard]
// and a nil (env-only) config do not, and then playback is unchanged.
func pumpRecorderOptions(rec observe.StageRecorder) []wire.PumpOption {
	pr, ok := rec.(wire.PumpRecorder)
	if !ok {
		return nil
	}
	return []wire.PumpOption{wire.WithPumpRecorder(pr)}
}
