package orchestrator

// TurnGate is the live authoritative check of whether a NEW Agent turn may start
// (#130, ADR-0046). The per-session spend meter satisfies it: once the session's
// estimated spend crosses its soft cap, [TurnGate.AllowTurn] returns false and the
// [Replier] refuses new turns before taking the floor — in-flight turns finish and
// transcription is untouched (the gate lives at the replier, not the segmenter).
//
// It mirrors [MuteView]'s posture: the orchestrator keeps no spend state of its own
// and asks this view per route. A nil gate is the feature-off default.
type TurnGate interface {
	// AllowTurn reports whether a new turn may open. Spend is monotonic, so a false
	// result is permanent for the session — the replier needs a single pre-check, no
	// post-Take re-check (unlike the mute view, which can flip mid-turn).
	AllowTurn() bool
}

// The live turn gate is wired into the conversation as [Barge.Gate] (#130,
// #453): Register stores it on the [Replier], which refuses a route whose new
// turn the gate denies (beside the mute pre-check) before the floor is taken. A
// nil gate is the feature-off default — voice standalone / the benchmark are
// byte-for-byte unchanged.
