package orchestrator

import "context"

// pipelinedDispatchKey is the unexported context key marking a turn whose dispatch
// callback is PIPELINED (#626).
type pipelinedDispatchKey struct{}

// WithPipelinedDispatch marks ctx as a pipelined turn's producer context. The
// [Replier] installs it on the ctx it hands the producer ([StreamReplyFunc], the
// batch [ReplyFunc]) whenever the turn runs through the pre-synthesis pipeline.
func WithPipelinedDispatch(ctx context.Context) context.Context {
	return context.WithValue(ctx, pipelinedDispatchKey{}, true)
}

// PipelinedDispatch reports whether this turn's dispatch callback resolves
// ASYNCHRONOUSLY (#626): it returns as soon as the PREVIOUS sentence's outcome is
// known, while the sentence it was handed is still synthesizing or playing.
//
// A producer under such a ctx must not read a dispatch return as its sentence's
// outcome. It commits strictly through [Reply.OnDelivered] (fired at the ADR-0012
// commit point, possibly on a pipeline goroutine, so the hook must be safe there)
// and waits for every dispatched sentence's [Reply.OnResolved] before reading what
// it committed. A false result is the unpipelined contract every pre-#626 caller
// relies on: dispatch returns only once its own sentence has resolved, so the
// return value alone is a sound commit signal.
func PipelinedDispatch(ctx context.Context) bool {
	v, _ := ctx.Value(pipelinedDispatchKey{}).(bool)
	return v
}
