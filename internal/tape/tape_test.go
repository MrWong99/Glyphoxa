package tape

import (
	"sync"
	"testing"
	"time"
)

// base is a fixed wall-clock origin so tests can reason about At windows without
// touching the real clock.
var base = time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

// waitDrained blocks until the owner goroutine has processed every append enqueued
// so far, by round-tripping a Snapshot (serviced on the same owner after the
// appends it follows). It makes the async mailbox observable to assertions.
func drainedSnapshot(t *testing.T, tp *Tape, from, to time.Time) Snapshot {
	t.Helper()
	return tp.Snapshot(from, to)
}

func TestAppendSnapshotRoundTrip(t *testing.T) {
	tp := New(Window, []string{"alice"}, nil)
	defer tp.Close()

	tp.AppendInbound("alice", []byte{0x01}, base)
	tp.AppendInbound("alice", []byte{0x02}, base.Add(20*time.Millisecond))

	snap := drainedSnapshot(t, tp, base.Add(-time.Second), base.Add(time.Second))
	if len(snap.Lanes) != 1 {
		t.Fatalf("lanes = %d, want 1", len(snap.Lanes))
	}
	lane := snap.Lanes[0]
	if lane.LaneID != "alice" {
		t.Fatalf("lane id = %q, want alice", lane.LaneID)
	}
	if len(lane.Frames) != 2 {
		t.Fatalf("frames = %d, want 2", len(lane.Frames))
	}
	if lane.Frames[0].Opus[0] != 0x01 || lane.Frames[1].Opus[0] != 0x02 {
		t.Fatalf("frames out of order: %v", lane.Frames)
	}
}

func TestCloseDiscards(t *testing.T) {
	tp := New(Window, []string{"alice"}, nil)
	tp.AppendInbound("alice", []byte{0x01}, base)
	// Ensure the frame is processed before Close.
	drainedSnapshot(t, tp, base.Add(-time.Second), base.Add(time.Second))

	tp.Close()

	snap := tp.Snapshot(base.Add(-time.Second), base.Add(time.Second))
	if len(snap.Lanes) != 0 {
		t.Fatalf("after Close: lanes = %d, want 0", len(snap.Lanes))
	}
	// Appends after Close are no-ops and must not panic.
	tp.AppendInbound("alice", []byte{0x99}, base)
	tp.AppendAgent([]byte{0x99}, base)
	tp.Close() // idempotent
}

func TestRingBounded(t *testing.T) {
	// A 1-second window holds 50 frames (20ms each). Append 7000-scaled: use a
	// small window so the test is fast but the drop-oldest bound is exact.
	window := time.Second // 50 frames
	tp := New(window, []string{"alice"}, nil)
	defer tp.Close()

	total := 1050
	for i := 0; i < total; i++ {
		tp.AppendInbound("alice", []byte{byte(i)}, base.Add(time.Duration(i)*20*time.Millisecond))
	}
	// Wide range so every retained frame is in-range.
	snap := drainedSnapshot(t, tp, base, base.Add(time.Hour))
	if len(snap.Lanes) != 1 {
		t.Fatalf("lanes = %d, want 1", len(snap.Lanes))
	}
	got := len(snap.Lanes[0].Frames)
	want := int(window / frameInterval) // 50
	if got != want {
		t.Fatalf("retained frames = %d, want %d (bounded drop-oldest)", got, want)
	}
	// Oldest retained frame is total-want, i.e. the earliest survivors were dropped.
	first := snap.Lanes[0].Frames[0]
	wantFirstAt := base.Add(time.Duration(total-want) * 20 * time.Millisecond)
	if !first.At.Equal(wantFirstAt) {
		t.Fatalf("oldest retained At = %v, want %v (older dropped)", first.At, wantFirstAt)
	}
}

func TestUnconsentedNeverAppears(t *testing.T) {
	tp := New(Window, []string{"alice"}, nil)
	defer tp.Close()

	tp.AppendInbound("bob", []byte{0x01}, base) // bob never consented
	tp.AppendInbound("alice", []byte{0x02}, base)

	snap := drainedSnapshot(t, tp, base.Add(-time.Second), base.Add(time.Second))
	for _, lane := range snap.Lanes {
		if lane.LaneID == "bob" {
			t.Fatalf("unconsented speaker bob appeared in tape: %+v", lane)
		}
	}
	if len(snap.Lanes) != 1 || snap.Lanes[0].LaneID != "alice" {
		t.Fatalf("want only alice lane, got %+v", snap.Lanes)
	}
}

func TestSetConsentGrantThenCapture(t *testing.T) {
	tp := New(Window, nil, nil)
	defer tp.Close()

	tp.AppendInbound("carol", []byte{0x01}, base) // not yet consented -> dropped
	tp.SetConsent("carol", true)
	tp.AppendInbound("carol", []byte{0x02}, base.Add(20*time.Millisecond))

	snap := drainedSnapshot(t, tp, base.Add(-time.Second), base.Add(time.Second))
	if len(snap.Lanes) != 1 || len(snap.Lanes[0].Frames) != 1 {
		t.Fatalf("want 1 frame after grant, got %+v", snap.Lanes)
	}
	if snap.Lanes[0].Frames[0].Opus[0] != 0x02 {
		t.Fatalf("captured the wrong frame: %v", snap.Lanes[0].Frames[0].Opus)
	}
}

func TestSetConsentRevokeClearsLane(t *testing.T) {
	tp := New(Window, []string{"alice"}, nil)
	defer tp.Close()

	tp.AppendInbound("alice", []byte{0x01}, base)
	drainedSnapshot(t, tp, base, base.Add(time.Second)) // ensure captured

	tp.SetConsent("alice", false) // revoke clears the ring

	snap := tp.Snapshot(base.Add(-time.Second), base.Add(time.Second))
	if len(snap.Lanes) != 0 {
		t.Fatalf("after revoke: lanes = %d, want 0 (ring cleared)", len(snap.Lanes))
	}
	// And future frames from alice no longer captured.
	tp.AppendInbound("alice", []byte{0x02}, base.Add(40*time.Millisecond))
	snap = drainedSnapshot(t, tp, base.Add(-time.Second), base.Add(time.Second))
	if len(snap.Lanes) != 0 {
		t.Fatalf("after revoke: alice still captured: %+v", snap.Lanes)
	}
}

func TestAppendAgentAlwaysOn(t *testing.T) {
	// No consented speakers at all: agent audio is still captured (ADR-0051).
	tp := New(Window, nil, nil)
	defer tp.Close()

	tp.AppendAgent([]byte{0x01}, base)

	snap := drainedSnapshot(t, tp, base.Add(-time.Second), base.Add(time.Second))
	if len(snap.Lanes) != 1 || snap.Lanes[0].LaneID != AgentLaneID {
		t.Fatalf("agent lane missing: %+v", snap.Lanes)
	}
	if len(snap.Lanes[0].Frames) != 1 {
		t.Fatalf("agent frames = %d, want 1", len(snap.Lanes[0].Frames))
	}
}

func TestSnapshotConsistentUnderConcurrentAppends(t *testing.T) {
	tp := New(Window, []string{"alice", "bob"}, nil)
	defer tp.Close()

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for _, id := range []string{"alice", "bob"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				tp.AppendInbound(id, []byte{byte(i)}, base.Add(time.Duration(i)*time.Millisecond))
			}
		}(id)
	}
	// Take many snapshots concurrently; the race detector guards internal state.
	for i := 0; i < 200; i++ {
		snap := tp.Snapshot(base, base.Add(time.Hour))
		for _, lane := range snap.Lanes {
			// Frames must be sorted ascending — a torn read would violate this.
			for j := 1; j < len(lane.Frames); j++ {
				if lane.Frames[j].At.Before(lane.Frames[j-1].At) {
					t.Errorf("snapshot frames not sorted: lane %s", lane.LaneID)
				}
			}
		}
	}
	close(stop)
	wg.Wait()
}

func BenchmarkAppendInbound(b *testing.B) {
	tp := New(Window, []string{"alice"}, nil)
	defer tp.Close()
	// The owner goroutine drains the mailbox; steady-state append is enqueue (or,
	// under backlog, the drop-oldest path) — both must be allocation-free.
	opus := []byte{0x01, 0x02, 0x03}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tp.AppendInbound("alice", opus, base)
	}
}
