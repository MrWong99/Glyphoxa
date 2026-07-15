package wirenpc

import (
	"log/slog"

	"github.com/MrWong99/Glyphoxa/internal/highlight"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/wire"
)

// buildHighlightDetector constructs the Session Highlights moment detector (#307)
// for this Voice Session cycle, or returns nil when highlights are off. It is armed
// ONLY when both the rollover tape (the clip source) and a trigger sink are wired —
// a detector with nothing to cut clips from, or nowhere to hand triggers, is inert
// by construction. It reuses the recap/wirenpc BYOK chain (llmbuild.New keyed off
// cfg.llmProviderID + cfg.keys.llm) for the classifier provider, snapshots via the
// tape, gates on the session spend gate, and meters on the shared StageRecorder the
// Manager already tees into the spend meter (ADR-0046). A provider-build failure is
// logged and degrades to no detector — a Highlight is best-effort and must never
// fail the session.
func buildHighlightDetector(cfg Config, bus *voiceevent.Bus, log *slog.Logger) *highlight.Detector {
	if cfg.Tape == nil || cfg.Highlights == nil {
		return nil
	}
	provider, err := newLLM(cfg.llmProviderID, cfg.keys.llm)
	if err != nil {
		log.Warn("highlight detector: build classifier provider; highlights disabled this session", "err", err)
		return nil
	}
	model := ""
	if len(cfg.npcs) > 0 {
		model = cfg.npcs[0].model
	}
	return highlight.NewDetector(
		bus,
		provider,
		model,
		cfg.Tape.Snapshot,
		cfg.Highlights,
		cfg.Gate,
		cfg.StageMetrics,
		log,
		highlight.Config{ProviderLabel: llmProviderLabel(cfg.llmProviderID)},
	)
}

// highlightPCMOptions wires the detector's decoded-PCM tap into the inbound
// pipeline (#307). A nil detector returns nothing (byte-identical loop). The tap is
// non-blocking (drops under load), so it adds no audio-loop latency (ADR-0020).
func highlightPCMOptions(d *highlight.Detector) []wire.Option {
	if d == nil {
		return nil
	}
	return []wire.Option{wire.WithPCMTap(d.PCMTap())}
}
