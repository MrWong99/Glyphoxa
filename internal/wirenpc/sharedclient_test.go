package wirenpc

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/snowflake/v2"
)

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

// TestAcquireClientSharedBorrows asserts the standing-client path (#102): the
// injected client is used as-is, reported NOT owned (so the cycle never closes
// the presence's client), and the disgo constructor is never touched.
func TestAcquireClientSharedBorrows(t *testing.T) {
	orig := newDiscordClient
	called := 0
	newDiscordClient = func(string, ...bot.ConfigOpt) (*bot.Client, error) {
		called++
		return nil, errors.New("constructor must not run on the shared path")
	}
	defer func() { newDiscordClient = orig }()

	sentinel := &bot.Client{}
	cfg := Config{Client: func(context.Context) (*bot.Client, error) { return sentinel, nil }}

	client, owned, err := acquireClient(context.Background(), cfg, discard())
	if err != nil {
		t.Fatalf("acquireClient(shared) err = %v", err)
	}
	if client != sentinel {
		t.Errorf("acquireClient returned %p, want the injected client %p", client, sentinel)
	}
	if owned {
		t.Error("shared client reported owned=true; the cycle must NOT close the presence's client")
	}
	if called != 0 {
		t.Errorf("disgo constructor ran %d times on the shared path, want 0", called)
	}
}

// TestAcquireClientSharedProviderError asserts a wait-state / rebuilding presence
// (provider error) fails the cycle so runWithReconnect backs off and retries.
func TestAcquireClientSharedProviderError(t *testing.T) {
	boom := errors.New("no token yet")
	cfg := Config{Client: func(context.Context) (*bot.Client, error) { return nil, boom }}

	if _, _, err := acquireClient(context.Background(), cfg, discard()); !errors.Is(err, boom) {
		t.Fatalf("acquireClient(shared error) = %v, want it to wrap %v", err, boom)
	}
}

// TestAcquireClientOwnedUsesConstructor asserts the per-cycle path (cfg.Client
// nil) still builds its own client via the disgo constructor seam.
func TestAcquireClientOwnedUsesConstructor(t *testing.T) {
	orig := newDiscordClient
	boom := errors.New("bad token")
	called := 0
	newDiscordClient = func(string, ...bot.ConfigOpt) (*bot.Client, error) {
		called++
		return nil, boom
	}
	defer func() { newDiscordClient = orig }()

	_, _, err := acquireClient(context.Background(), Config{Token: "x"}, discard())
	if called != 1 {
		t.Errorf("owned-path constructor calls = %d, want 1", called)
	}
	if !errors.Is(err, boom) {
		t.Errorf("owned-path error = %v, want it to wrap %v", err, boom)
	}
}

// TestConnectAndServeSharedClientErrorIsCycleError asserts connectAndServe
// surfaces the provider error (returning before it builds any pipeline), so the
// reconnect loop retries — the self-heal across presence rebuilds.
func TestConnectAndServeSharedClientErrorIsCycleError(t *testing.T) {
	boom := errors.New("presence rebuilding")
	cfg := Config{Client: func(context.Context) (*bot.Client, error) { return nil, boom }}

	connectedCalls := 0
	err := connectAndServe(context.Background(), cfg, snowflake.ID(1), snowflake.ID(2), discard(),
		func() { connectedCalls++ })
	if !errors.Is(err, boom) {
		t.Fatalf("connectAndServe = %v, want it to surface the provider error %v", err, boom)
	}
	if connectedCalls != 0 {
		t.Errorf("connected() fired %d times on a failed acquire, want 0", connectedCalls)
	}
}
