package tool

// BuiltinRegistry returns a Registry holding every built-in Tool v1.0 ships
// (ADR-0028: exactly one, `dice`). It is the single source of truth for "which
// Tools exist" — the live voice loop (wirenpc), the benchmark rig, and the grant
// editor RPC (#117) all build from it, so the Tools an operator can toggle on the
// Campaign screen are exactly the Tools a Voice Session can actually run. When a
// new built-in lands it is registered here once and appears everywhere.
//
// dice is registered with a non-deterministic production roller; tests that need
// a pinned roll construct their own Registry with [NewDiceWithRand].
func BuiltinRegistry() *Registry {
	r := NewRegistry()
	r.MustRegister(NewDice())
	return r
}
