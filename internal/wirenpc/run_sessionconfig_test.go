package wirenpc

import (
	"context"
	"log/slog"
	"reflect"
	"testing"

	"github.com/disgoorg/snowflake/v2"

	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// captureFirstCycle swaps the cycle seam for one that records the Config the
// first connect-and-serve cycle was handed and then ends the session, so a test
// can drive the REAL [Run] / [RunFromDB] up to the point where they would dial
// Discord. Run it with the returned context.
func captureFirstCycle(t *testing.T) (ctx context.Context, served func() (Config, bool)) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	orig := serveCycle
	t.Cleanup(func() { serveCycle = orig })
	var (
		got    Config
		cycles int
	)
	serveCycle = func(ctx context.Context, cfg Config, _, _ snowflake.ID, _ *slog.Logger, _ func(), _ *sessionOnce) error {
		got = cfg
		cycles++
		cancel()
		return ctx.Err()
	}
	return ctx, func() (Config, bool) { return got, cycles > 0 }
}

// TestRun_HandsEveryCycleTheResolvedSessionConfig closes the top of the #701
// wiring proof: the Config a cycle builds its pipeline from is the one [Run] put
// through [sessionConfig] — recognition hints resolved, player-character names
// derived. Without it both STT transports would silently send no keyterms for
// every session while the rest of the suite, which enters below Run, stayed
// green.
func TestRun_HandsEveryCycleTheResolvedSessionConfig(t *testing.T) {
	ctx, served := captureFirstCycle(t)
	err := Run(ctx, Config{
		Guild: "1", Channel: "2",
		STTKeyterms: true,
		npcs:        []npcSpec{{agentID: "flo", name: "Flo", aliases: []string{"Florian"}}},
		players:     []storage.Character{{Name: "Artusas", Aliases: []string{"Art"}}},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	cfg, ok := served()
	if !ok {
		t.Fatal("Run never reached a connect-and-serve cycle")
	}
	if got, want := cfg.sttHints.terms, []string{"Flo", "Florian", "Artusas", "Art"}; !reflect.DeepEqual(got, want) {
		t.Errorf("the cycle's keyterms = %q, want %q", got, want)
	}
	if got, want := cfg.playerCharacters, []string{"Artusas"}; !reflect.DeepEqual(got, want) {
		t.Errorf("the cycle's player-character names = %q, want %q", got, want)
	}
}

// TestRun_EnvOnlyPath_BiasesTowardsTheHardcodedNPC: with no roster loaded Run
// serves the hardcoded NPC, and its name is what the recognizer is biased to.
func TestRun_EnvOnlyPath_BiasesTowardsTheHardcodedNPC(t *testing.T) {
	ctx, served := captureFirstCycle(t)
	if err := Run(ctx, Config{Guild: "1", Channel: "2", STTKeyterms: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	cfg, _ := served()
	if len(cfg.npcs) != 1 {
		t.Fatalf("npcs = %d, want the one hardcoded NPC", len(cfg.npcs))
	}
	if got, want := cfg.sttHints.terms, []string{hardcodedNPC().name}; !reflect.DeepEqual(got[:1], want) {
		t.Errorf("keyterms = %q, want them to start with %q", got, want)
	}
}
