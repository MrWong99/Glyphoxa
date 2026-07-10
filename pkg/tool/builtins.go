package tool

// BuiltinRegistry returns a Registry holding every built-in Tool (ADR-0028):
// `dice`, plus the two read-only knowledge Tools `transcript_search` and
// `kg_query` (#296). It is the single source of truth for "which Tools exist" —
// the live voice loop (wirenpc), the benchmark rig, and the grant editor RPC
// (#117) all build from it, so the Tools an operator can toggle on the Campaign
// screen are exactly the Tools a Voice Session can actually run. When a new
// built-in lands it is registered here once and appears everywhere.
//
// deps injects the knowledge Tools' read sources (S1). A nil source leaves the
// Tool REGISTERED — the grant editor's catalog is identical in every mode — but
// its Execute reports it is unavailable, so a mode with no live retrieval (the
// standalone bench, the grant-editor RPC) still lists the full Tool set without
// panicking. dice is registered with a non-deterministic production roller; tests
// that need a pinned roll construct their own Registry with [NewDiceWithRand].
func BuiltinRegistry(deps Deps) *Registry {
	r := NewRegistry()
	r.MustRegister(NewDice())
	r.MustRegister(NewTranscriptSearch(deps.Transcripts))
	r.MustRegister(NewKGQuery(deps.KG))
	r.MustRegister(NewRememberKnowledge(deps.KGW))
	return r
}
