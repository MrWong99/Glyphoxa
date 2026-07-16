package wirenpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/disgoorg/disgo/bot"
	"github.com/gorilla/websocket"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// TestRun_PublishesConnectingThenFailedOnFatalClient is #123 test-seq 3: when the
// standing-client provider yields a fatal rejection (a wrapped close 4004), Run
// publishes connection.state{connecting} BEFORE the provider is consulted, returns
// the classified *FatalError (no retry), and publishes a terminal
// connection.state{failed} carrying the readable reason on cfg.Bus. The provider
// error short-circuits before any Discord/VAD work, so the test needs no network.
func TestRun_PublishesConnectingThenFailedOnFatalClient(t *testing.T) {
	bus := voiceevent.NewBus()

	var mu sync.Mutex
	var states []voiceevent.ConnectionState
	var details []string
	voiceevent.On(bus, func(e voiceevent.ConnectionStateChanged) {
		mu.Lock()
		states = append(states, e.State)
		details = append(details, e.Detail)
		mu.Unlock()
	})

	fatal := fmt.Errorf("wirenpc: open gateway: %w",
		&websocket.CloseError{Code: 4004, Text: "Authentication failed"})

	connectingBeforeProvider := false
	cfg := Config{
		Guild:   "111222333",
		Channel: "444555666",
		Bus:     bus,
		Client: func(context.Context) (*bot.Client, error) {
			mu.Lock()
			connectingBeforeProvider = len(states) == 1 && states[0] == voiceevent.ConnectionConnecting
			mu.Unlock()
			return nil, fatal
		},
	}

	err := Run(context.Background(), cfg)

	var fe *FatalError
	if !errors.As(err, &fe) || fe.Reason != ReasonInvalidBotToken {
		t.Fatalf("Run returned %v, want *FatalError invalid_bot_token", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !connectingBeforeProvider {
		t.Errorf("connection.state{connecting} was not published before the client provider ran; states seen = %v", states)
	}
	if len(states) == 0 || states[len(states)-1] != voiceevent.ConnectionFailed {
		t.Fatalf("states = %v, want to end with failed", states)
	}
	if got := details[len(details)-1]; !strings.Contains(got, ReasonInvalidBotToken) {
		t.Errorf("failed detail = %q, want it to name %q", got, ReasonInvalidBotToken)
	}
}

// TestRun_NoBusFatalClientStillReturnsFatal is the env-only path (cfg.Bus nil): a
// fatal client rejection still returns the *FatalError — the connection.state
// publishes are simply nil-guarded no-ops. This pins the KNOWN BEHAVIOR CHANGE:
// env-only voice mode now EXITS on an invalid token instead of retrying forever.
func TestRun_NoBusFatalClientStillReturnsFatal(t *testing.T) {
	cfg := Config{
		Guild:   "111222333",
		Channel: "444555666",
		Client: func(context.Context) (*bot.Client, error) {
			return nil, &websocket.CloseError{Code: 4004, Text: "Authentication failed"}
		},
	}

	var fe *FatalError
	if err := Run(context.Background(), cfg); !errors.As(err, &fe) || fe.Reason != ReasonInvalidBotToken {
		t.Fatalf("Run (no bus) returned %v, want *FatalError invalid_bot_token", err)
	}
}
