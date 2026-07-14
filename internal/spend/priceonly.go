package spend

import (
	"log/slog"

	"github.com/MrWong99/Glyphoxa/internal/observe"
)

// PriceOnly returns a [observe.StageRecorder] that forwards every call to base
// and additionally prices the usage trio on a caps-free [Meter] — the shared
// off-session metering posture (#271/#272 gate posture): usage is recorded and
// priced for attribution, but nothing is cap-gated and AllowTurn is never
// consulted (ADR-0046 caps are live-session-only). estimatedUSD reads the
// running estimate for the caller's attribution log line.
//
// Off-session LLM/image call sites (the Recap engine, the Highlight enrichment
// job) ask for pricing in one call instead of hand-assembling the
// caps-free-meter + TeeUsage + Status ritual.
func PriceOnly(base observe.StageRecorder, log *slog.Logger) (rec observe.StageRecorder, estimatedUSD func() float64) {
	m := NewMeter(Caps{}, log, nil, nil)
	return observe.TeeUsage(base, m), func() float64 { return m.Status().EstimatedUSD }
}
