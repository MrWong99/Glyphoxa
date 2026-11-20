package recap

import (
	"context"
	"errors"

	"github.com/MrWong99/Glyphoxa/internal/observe"
)

// ErrSpendCapReached is returned by [Engine.Recap] when the caller's metering
// carries a gate that refuses new work (the live session crossed its spend cap,
// ADR-0046). No LLM call is made.
var ErrSpendCapReached = errors.New("recap: the session's spend cap has been reached")

// SpendGate is the caller's cap check: false once the caller may no longer start
// new paid work. The live session's spend meter satisfies it.
type SpendGate interface {
	AllowTurn() bool
}

// Metering is the CALLER's spend accounting, carried to the recap engine through
// the context ([WithMetering]). The engine is one shared process-wide value, but
// a recap run from inside a Voice Session (the Butler's recap Tool) or a planning
// chat exchange is that caller's spend: it must land on the caller's meter, hard
// cap and Usage Ledger, not only on the process recorder the engine was built
// with, where it would be invisible to all three.
type Metering struct {
	// Rec receives the recap's LLM usage INSTEAD of the engine's own metering.
	// It is expected to already forward to the production recorder AND to the
	// caller's Usage Ledger (the session's StageMetrics and the chat exchange's
	// recorder both do), so usage is never counted twice. nil means no caller
	// meters this recap: the engine then treats it as an off-session paid call
	// and opens its own gated, ledgered one (billing.OffSession).
	Rec observe.StageRecorder
	// Gate, when non-nil, is consulted before any LLM call; a refusal fails the
	// recap with [ErrSpendCapReached]. nil = no gate (the planning chat checks its
	// allowance before the exchange starts).
	Gate SpendGate
}

type meteringKey struct{}

// WithMetering returns a copy of ctx carrying the caller's [Metering], which
// every [Engine.Recap] descending from it charges.
func WithMetering(ctx context.Context, m Metering) context.Context {
	return context.WithValue(ctx, meteringKey{}, m)
}

// MeteringFrom returns the [Metering] installed by [WithMetering], or the zero
// value and false when the ctx carries none (an off-session recap: the slash
// command, the web recap), which the engine gates on the plan allowance and
// ledgers through billing.OffSession.
func MeteringFrom(ctx context.Context) (Metering, bool) {
	m, ok := ctx.Value(meteringKey{}).(Metering)
	return m, ok
}
