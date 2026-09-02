package wirenpc

import (
	"context"
	"errors"
	"testing"
	"time"

	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
)

// blockingOpen models disgo's conn.Open on a channel Discord never answers: it
// returns only when its ctx ends.
func blockingOpen(ctx context.Context) (*gxvoice.Session, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestJoinVoiceChannelTimesOut pins the join bound: a join Discord never answers
// ends within the timeout as errJoinTimeout instead of hanging the cycle.
func TestJoinVoiceChannelTimesOut(t *testing.T) {
	t.Parallel()
	start := time.Now()
	_, err := joinVoiceChannel(context.Background(), blockingOpen, 50*time.Millisecond)
	if !errors.Is(err, errJoinTimeout) {
		t.Fatalf("err = %v, want errJoinTimeout", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want it to keep wrapping DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("join took %v, want ~50ms", elapsed)
	}
}

// TestJoinVoiceChannelOuterCancelIsNotATimeout: a GM Stop during the join keeps
// surfacing as the plain cancellation the reconnect loop classifies.
func TestJoinVoiceChannelOuterCancelIsNotATimeout(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := joinVoiceChannel(ctx, blockingOpen, time.Minute)
	if errors.Is(err, errJoinTimeout) {
		t.Fatalf("err = %v, want a plain cancellation, not errJoinTimeout", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
