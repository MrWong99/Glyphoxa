package presence

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/disgoorg/disgo/discord"

	"github.com/MrWong99/Glyphoxa/pkg/tool"
)

// RollCommand builds the /roll slash command (ADR-0010: anyone in the configured
// Guild). It parses an NdM dice expression and answers with the result of the
// built-in Dice Tool (pkg/tool.Dice) — the SAME code the Agent tool-use loop
// rolls with, never a re-implementation. A malformed expression or an
// out-of-bounds roll is answered with a graceful ephemeral hint, and the handler
// returns nil (a domain error, not an unexpected failure).
func RollCommand(dice *tool.Dice) Command {
	return Command{
		Path:        "roll",
		Description: "Roll dice, e.g. 2d6 or d20.",
		Options: []discord.ApplicationCommandOption{
			discord.ApplicationCommandOptionString{
				Name:        "dice",
				Description: "Dice expression like 2d6 or d20.",
				Required:    true,
			},
		},
		GMOnly: false,
		Handle: func(ctx context.Context, ic *Interaction) error {
			expr, _ := ic.String("dice")
			count, sides, err := parseDiceExpr(expr)
			if err != nil {
				return ic.ReplyEphemeral(rollHint(expr))
			}
			args, err := json.Marshal(struct {
				Count int `json:"count"`
				Sides int `json:"sides"`
			}{count, sides})
			if err != nil {
				return err // marshalling two ints cannot fail; propagate if it ever does
			}
			// The Dice Tool re-validates the bounds it declares (count 1..100,
			// sides 2..1000); an out-of-range NdM comes back as an error we turn
			// into the same graceful hint rather than a crash or silent drop.
			result, err := dice.Execute(ctx, args, nil)
			if err != nil {
				return ic.ReplyEphemeral(rollHint(expr))
			}
			return ic.Reply(result)
		},
	}
}

// rollHint is the graceful error reply for an unrollable expression.
func rollHint(expr string) string {
	return fmt.Sprintf("Can't roll %q — use NdM like 2d6 (N optional, e.g. d20).", expr)
}

// parseDiceExpr parses an "NdM" dice expression into (count, sides). N is
// optional and defaults to 1 (so "d20" == "1d20"); both parts, when present,
// must be non-negative integers. It validates only the SHAPE — numeric range
// bounds are the Dice Tool's job (single source of truth, ADR-0028), so "0d6" /
// "101d6" / "2d1" parse cleanly here and are rejected by dice.Execute.
func parseDiceExpr(expr string) (count, sides int, err error) {
	s := strings.ToLower(strings.TrimSpace(expr))
	left, right, found := strings.Cut(s, "d")
	if !found || right == "" {
		return 0, 0, fmt.Errorf("presence: %q is not an NdM dice expression", expr)
	}
	count = 1
	if left != "" {
		count, err = parseNonNegative(left)
		if err != nil {
			return 0, 0, fmt.Errorf("presence: dice count in %q: %w", expr, err)
		}
	}
	sides, err = parseNonNegative(right)
	if err != nil {
		return 0, 0, fmt.Errorf("presence: dice sides in %q: %w", expr, err)
	}
	return count, sides, nil
}

// parseNonNegative parses a base-10 non-negative integer. It rejects anything
// that is not purely digits BEFORE strconv.Atoi, because Atoi accepts a leading
// sign — so this is what makes "+2" / "-2" / "2x" malformed dice parts rather
// than sneaking through as a count.
func parseNonNegative(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("empty number")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("non-digit %q in %q", r, s)
		}
	}
	return strconv.Atoi(s)
}
