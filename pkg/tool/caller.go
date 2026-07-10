package tool

import "context"

// callerKey is the unexported ctx key the caller identity travels under, so no
// other package can collide with or forge it.
type callerKey struct{}

// WithCaller stamps the calling Agent's id onto ctx so a scope-narrowing Tool
// handler can resolve the caller WITHOUT trusting the LLM's args (S2, ADR-0029).
// It is set ONCE per turn, in the agenttool Engine's Generate/GenerateStream
// path, from the Agent's own spec — the model never supplies it, so an
// own_node-scoped kg_query reads exactly the caller's neighbourhood and cannot
// be widened by clever arguments.
func WithCaller(ctx context.Context, agentID string) context.Context {
	return context.WithValue(ctx, callerKey{}, agentID)
}

// CallerID returns the Agent id stamped by [WithCaller], or "" if none is set
// (an unstamped ctx — a standalone bench turn, or a Persona with no persisted
// id). A handler that needs own-node scope treats "" as "no neighbourhood to
// scope to" and yields an empty result rather than falling back to a wider read.
func CallerID(ctx context.Context) string {
	id, _ := ctx.Value(callerKey{}).(string)
	return id
}
