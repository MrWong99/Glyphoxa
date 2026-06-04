package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
)

// fixedRand returns a *rand.Rand with a fixed seed so rolls are reproducible.
func fixedRand() *rand.Rand {
	return rand.New(rand.NewPCG(1, 2))
}

func TestDiceMetadata(t *testing.T) {
	d := NewDiceWithRand(fixedRand())
	if d.Name() != "dice" {
		t.Errorf("Name = %q, want dice", d.Name())
	}
	if !d.ReadOnly() {
		t.Error("dice must be read-only (ADR-0030)")
	}
	if !json.Valid(d.InputSchema()) {
		t.Error("InputSchema is not valid JSON")
	}
}

func TestDiceExecuteDeterministic(t *testing.T) {
	// Same seed → same rolls, twice.
	args := json.RawMessage(`{"count":3,"sides":6}`)
	got1, err := NewDiceWithRand(fixedRand()).Execute(context.Background(), args, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got2, err := NewDiceWithRand(fixedRand()).Execute(context.Background(), args, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got1 != got2 {
		t.Errorf("same seed gave different results: %q vs %q", got1, got2)
	}
	if !strings.HasPrefix(got1, "Rolled 3d6:") {
		t.Errorf("unexpected result %q", got1)
	}
}

func TestDiceExecuteSingleDie(t *testing.T) {
	got, err := NewDiceWithRand(fixedRand()).Execute(
		context.Background(), json.RawMessage(`{"count":1,"sides":20}`), nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.HasPrefix(got, "Rolled 1d20:") {
		t.Errorf("single-die phrasing wrong: %q", got)
	}
}

func TestDiceExecuteTotalInRange(t *testing.T) {
	// Every 4d6 total must land in [4,24]; sample a chunk of seeds and rolls.
	for seed := uint64(0); seed < 50; seed++ {
		d := NewDiceWithRand(rand.New(rand.NewPCG(seed, seed+1)))
		for range 20 {
			out, err := d.Execute(context.Background(), json.RawMessage(`{"count":4,"sides":6}`), nil)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			var total int
			i := strings.LastIndex(out, "total ")
			if i < 0 {
				t.Fatalf("cannot find total in %q", out)
			}
			if _, err := fmt.Sscanf(out[i:], "total %d).", &total); err != nil {
				t.Fatalf("cannot parse total from %q: %v", out, err)
			}
			if total < 4 || total > 24 {
				t.Errorf("4d6 total %d out of range, output %q", total, out)
			}
		}
	}
}

func TestDiceExecuteRejectsBadArgs(t *testing.T) {
	d := NewDiceWithRand(fixedRand())
	cases := []struct {
		name string
		args string
	}{
		{"not json", `not json`},
		{"count zero", `{"count":0,"sides":6}`},
		{"count too high", `{"count":101,"sides":6}`},
		{"sides one", `{"count":1,"sides":1}`},
		{"sides too high", `{"count":1,"sides":1001}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := d.Execute(context.Background(), json.RawMessage(tc.args), nil); err == nil {
				t.Errorf("expected error for args %q", tc.args)
			}
		})
	}
}

func TestDiceConcurrentRolls(t *testing.T) {
	// One shared Dice rolled in parallel — the documented execution model
	// (ADR-0030 inline-during-speculation + ADR-0025 parallel agents). Run
	// under -race to catch an unguarded *rand.Rand.
	d := NewDice()
	var wg sync.WaitGroup
	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := d.Execute(context.Background(), json.RawMessage(`{"count":5,"sides":20}`), nil); err != nil {
				t.Errorf("concurrent Execute: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestDiceExecuteHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewDiceWithRand(fixedRand()).Execute(ctx, json.RawMessage(`{"count":1,"sides":6}`), nil); err == nil {
		t.Error("expected error on canceled context")
	}
}
