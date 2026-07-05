package presence

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/tool"
)

// seededDicePair returns two Dice sharing one deterministic PCG seed: rolling the
// same NdM on each yields identical output, so a test can compare the /roll
// handler's reply against the Dice Tool's exact string without advancing the
// roller under test.
func seededDicePair() (underTest, oracle *tool.Dice) {
	const s1, s2 = 0x9e3779b97f4a7c15, 0xbf58476d1ce4e5b9
	return tool.NewDiceWithRand(rand.New(rand.NewPCG(s1, s2))),
		tool.NewDiceWithRand(rand.New(rand.NewPCG(s1, s2)))
}

func runRoll(t *testing.T, dice *tool.Dice, expr string) *fakeResponder {
	t.Helper()
	cmd := RollCommand(dice)
	resp := &fakeResponder{}
	ic := &Interaction{guildID: testGuild, userID: strangerID, opts: fakeOpts{s: map[string]string{"dice": expr}}, resp: resp}
	if err := cmd.Handle(context.Background(), ic); err != nil {
		t.Fatalf("roll %q returned error %v; handler must own its reply and return nil", expr, err)
	}
	return resp
}

func oracleRoll(t *testing.T, oracle *tool.Dice, count, sides int) string {
	t.Helper()
	args, _ := json.Marshal(struct {
		Count int `json:"count"`
		Sides int `json:"sides"`
	}{count, sides})
	out, err := oracle.Execute(context.Background(), args, nil)
	if err != nil {
		t.Fatalf("oracle roll %dd%d: %v", count, sides, err)
	}
	return out
}

func TestRollValidPublicReply(t *testing.T) {
	underTest, oracle := seededDicePair()
	resp := runRoll(t, underTest, "2d6")

	want := oracleRoll(t, oracle, 2, 6)
	if len(resp.replies) != 1 {
		t.Fatalf("replies = %+v, want exactly one", resp.replies)
	}
	got := resp.replies[0]
	if got.ephemeral {
		t.Errorf("valid roll replied ephemerally; a result is public")
	}
	if got.content != want {
		t.Errorf("reply = %q, want the Dice Tool's exact string %q", got.content, want)
	}
}

func TestRollDefaultCountIsOne(t *testing.T) {
	underTest, oracle := seededDicePair()
	resp := runRoll(t, underTest, "d20")

	want := oracleRoll(t, oracle, 1, 20) // "d20" == "1d20"
	if len(resp.replies) != 1 || resp.replies[0].content != want {
		t.Fatalf("d20 reply = %+v, want %q (count defaults to 1)", resp.replies, want)
	}
}

func TestRollMalformedAndOutOfBoundsAreGraceful(t *testing.T) {
	for _, expr := range []string{"banana", "0d6", "2d1", "101d6", "", "d", "2d"} {
		underTest, _ := seededDicePair()
		resp := runRoll(t, underTest, expr)
		if len(resp.replies) != 1 {
			t.Fatalf("%q: replies = %+v, want one graceful reply", expr, resp.replies)
		}
		if !resp.replies[0].ephemeral {
			t.Errorf("%q: error reply must be ephemeral", expr)
		}
		if !strings.Contains(resp.replies[0].content, "NdM") {
			t.Errorf("%q: reply = %q, want an NdM usage hint", expr, resp.replies[0].content)
		}
	}
}
