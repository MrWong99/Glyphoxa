package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
)

// Dice is the one built-in Tool v1.0 ships (ADR-0028): it rolls NdM — N dice of
// M sides each — and reports the rolls and their sum. It is read-only (ADR-0030)
// so the loop runs it inline during generation; the LLM needs the result to
// keep talking ("you rolled a 17").
//
// The roll source is injectable so tests pin the outcome: a tool-use loop test
// asserting the model's routing must not be flaky on a global RNG. Construct
// with [NewDice] for a seeded production roller or [NewDiceWithRand] for a test
// roller.
type Dice struct {
	rng *rand.Rand
}

// NewDice returns a Dice backed by a non-deterministic seeded source, for
// production use.
func NewDice() *Dice {
	return &Dice{rng: rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))}
}

// NewDiceWithRand returns a Dice that rolls from rng, for deterministic tests.
// rng must be non-nil.
func NewDiceWithRand(rng *rand.Rand) *Dice {
	if rng == nil {
		panic("tool: NewDiceWithRand: nil rng")
	}
	return &Dice{rng: rng}
}

// Name implements [Tool].
func (*Dice) Name() string { return "dice" }

// Description implements [Tool].
func (*Dice) Description() string {
	return "Roll dice: N dice of M sides each (NdM). Returns the individual rolls and their total."
}

// diceInputSchema is the JSON Schema declared to the LLM for [Dice]'s args.
var diceInputSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "count": {
      "type": "integer",
      "minimum": 1,
      "maximum": 100,
      "description": "Number of dice to roll (N)."
    },
    "sides": {
      "type": "integer",
      "minimum": 2,
      "maximum": 1000,
      "description": "Number of sides per die (M)."
    }
  },
  "required": ["count", "sides"],
  "additionalProperties": false
}`)

// InputSchema implements [Tool].
func (*Dice) InputSchema() json.RawMessage { return diceInputSchema }

// ReadOnly implements [Tool]: rolling dice mutates no state.
func (*Dice) ReadOnly() bool { return true }

// diceArgs is the decoded shape of [Dice]'s LLM-supplied arguments.
type diceArgs struct {
	Count int `json:"count"`
	Sides int `json:"sides"`
}

// Bounds mirror the input schema; re-validated in the handler because the LLM
// can emit anything (ADR-0029: never trust the model to enforce constraints).
const (
	maxDiceCount = 100
	maxDiceSides = 1000
)

// Execute implements [Tool]. It rolls args.Count dice of args.Sides sides and
// returns a human-readable line the LLM can speak. dice carries no grant scope,
// so grantConfig is ignored. The argument bounds are enforced here, not trusted
// from the model.
func (d *Dice) Execute(ctx context.Context, args json.RawMessage, _ any) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	var a diceArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("dice: invalid arguments: %w", err)
	}
	if a.Count < 1 || a.Count > maxDiceCount {
		return "", fmt.Errorf("dice: count must be between 1 and %d, got %d", maxDiceCount, a.Count)
	}
	if a.Sides < 2 || a.Sides > maxDiceSides {
		return "", fmt.Errorf("dice: sides must be between 2 and %d, got %d", maxDiceSides, a.Sides)
	}

	rolls := make([]int, a.Count)
	sum := 0
	for i := range rolls {
		r := d.rng.IntN(a.Sides) + 1 // 1..Sides
		rolls[i] = r
		sum += r
	}

	if a.Count == 1 {
		return fmt.Sprintf("Rolled 1d%d: %d.", a.Sides, sum), nil
	}
	return fmt.Sprintf("Rolled %dd%d: %v (total %d).", a.Count, a.Sides, rolls, sum), nil
}
