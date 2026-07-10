package mixdown

import "time"

// TEMPORARY until #306 merges — these types MUST match the arch-e8 contract §A
// exactly. #304 is developed in parallel with #306 (owner of internal/tape);
// until that package lands on main we compile against this minimal local copy
// of the pinned Snapshot shape. At merge the orchestrator swaps these three
// types for `import ".../internal/tape"` — the field names and shapes here are
// the contract, so the swap is a type-alias/import change, not a rewrite.

// Frame is one ~20ms Opus payload stamped with its wall-clock arrival time.
type Frame struct {
	Opus []byte    // one ~20ms Opus payload
	At   time.Time // wall-clock: arrival (inbound) / pulled-to-wire (agent)
}

// LaneSnapshot is one speaker lane's frames, sorted ascending by At.
type LaneSnapshot struct {
	LaneID string
	Frames []Frame
}

// Snapshot is a consistent copy of the rollover tape between [From, To),
// with lanes sorted by LaneID.
type Snapshot struct {
	From, To time.Time
	Lanes    []LaneSnapshot
}
