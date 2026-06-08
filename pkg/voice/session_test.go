package voice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"

	"github.com/MrWong99/Glyphoxa/pkg/voice/mock"
)

// openSession opens a session over a fresh fake conn and returns both.
func openSession(t *testing.T) (*Session, *mock.Conn) {
	t.Helper()
	fm := mock.NewManager()
	m := newTestManager(fm)
	sess, err := m.Open(context.Background(), testGuild, testChannel)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	conn, ok := fm.Conn(testGuild)
	if !ok {
		t.Fatal("fake conn not recorded")
	}
	return sess, conn
}

func TestSessionStateTransitions(t *testing.T) {
	sess, _ := openSession(t)
	if sess.State() != Ready {
		t.Fatalf("state got %v want Ready", sess.State())
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if sess.State() != Closed {
		t.Fatalf("state got %v want Closed", sess.State())
	}
}

func TestSessionDoubleCloseSafe(t *testing.T) {
	sess, conn := openSession(t)
	if err := sess.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if !conn.Closed() {
		t.Fatal("underlying conn should be closed")
	}
}

func TestSessionInboundClosesAfterClose(t *testing.T) {
	sess, conn := openSession(t)
	// Deliver a frame through the fake receiver, drain it, then close.
	_ = conn.PushPacket(snowflake.ID(5), &voice.Packet{Sequence: 1, Opus: []byte{0x01}})
	if got := <-sess.Inbound(); got.UserID != snowflake.ID(5) {
		t.Fatalf("inbound frame UserID got %d want 5", got.UserID)
	}
	sess.Close()
	select {
	case _, ok := <-sess.Inbound():
		if ok {
			t.Fatal("inbound should be drained and closed")
		}
	case <-time.After(time.Second):
		t.Fatal("inbound channel did not close after Session.Close")
	}
}

func TestSessionPlayStreamsFrames(t *testing.T) {
	sess, conn := openSession(t)
	defer sess.Close()

	pb, err := sess.Play(context.Background(), OpusReader(bytesReader(framed([]byte{0x01}, []byte{0x02}))))
	if err != nil {
		t.Fatalf("Play: %v", err)
	}
	// Pull frames as disgo's sender would; the provider drains the source.
	got := pullAll(t, conn)
	if len(got) != 2 {
		t.Fatalf("pulled %d frames want 2", len(got))
	}
	select {
	case <-pb.Done():
	case <-time.After(time.Second):
		t.Fatal("playback did not finish after source EOF")
	}
	if err := pb.Err(); err != nil {
		t.Fatalf("clean playback Err got %v want nil", err)
	}
}

func TestSessionPlayInterruptsCurrent(t *testing.T) {
	sess, conn := openSession(t)
	defer sess.Close()

	// First playback has an effectively endless source.
	first, err := sess.Play(context.Background(), endlessSource{})
	if err != nil {
		t.Fatalf("first Play: %v", err)
	}
	_, _ = conn.PullFrame() // let it take the floor

	second, err := sess.Play(context.Background(), OpusReader(bytesReader(framed([]byte{0xAA}))))
	if err != nil {
		t.Fatalf("second Play: %v", err)
	}
	select {
	case <-first.Done():
	case <-time.After(time.Second):
		t.Fatal("first playback not interrupted by second Play")
	}
	if err := first.Err(); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("interrupted playback Err got %v want ErrInterrupted", err)
	}

	// The second playback is now active and drains to a clean finish.
	pullAll(t, conn)
	select {
	case <-second.Done():
	case <-time.After(time.Second):
		t.Fatal("second playback did not finish")
	}
	if err := second.Err(); err != nil {
		t.Fatalf("second playback Err got %v want nil", err)
	}
}

func TestSessionPlayAfterCloseFails(t *testing.T) {
	sess, _ := openSession(t)
	sess.Close()
	if _, err := sess.Play(context.Background(), endlessSource{}); err == nil {
		t.Fatal("Play on closed session should fail")
	}
}

func TestSessionPlayMetricsCountedOnce(t *testing.T) {
	// Every playback — interrupted or clean — records exactly one
	// PlaybackFinished. Regression guard for the double-count when Play both
	// Stopped the displaced playback and recorded Finished itself.
	m := &countingMetrics{}
	fm := mock.NewManager()
	mgr := newTestManager(fm, WithMetrics(m), WithDave(false))
	sess, err := mgr.Open(context.Background(), testGuild, testChannel)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	conn, _ := fm.Conn(testGuild)

	first, _ := sess.Play(context.Background(), endlessSource{})
	second, _ := sess.Play(context.Background(), endlessSource{}) // interrupts first
	<-first.Done()
	third, _ := sess.Play(context.Background(), OpusReader(bytesReader(framed([]byte{0x01})))) // interrupts second
	<-second.Done()
	pullAll(t, conn) // drain third to clean EOF
	<-third.Done()

	// PlaybackFinished is recorded by each playback's accounting goroutine just
	// after its Done closes, so poll briefly for the count to settle rather than
	// race the goroutine. Started is synchronous in Play, so it is exact now.
	if got := m.started.Load(); got != 3 {
		t.Fatalf("PlaybackStarted count got %d want 3", got)
	}
	if !eventually(func() bool { return m.finished.Load() == 3 }) {
		t.Fatalf("PlaybackFinished count got %d want 3 (one per playback)", m.finished.Load())
	}
	sess.Close()
}

func TestSessionPlayNilSourceFails(t *testing.T) {
	sess, _ := openSession(t)
	defer sess.Close()
	if _, err := sess.Play(context.Background(), nil); err == nil {
		t.Fatal("Play with nil Source should fail")
	}
}

// eventually polls cond up to ~1s, returning true as soon as it holds.
func eventually(cond func() bool) bool {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return cond()
}

// endlessSource yields a frame forever (until ctx is cancelled).
type endlessSource struct{}

func (endlessSource) NextFrame(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []byte{0x7F}, nil
}

// pullAll pulls frames from conn until the provider returns silence (the
// post-EOF idle state), mimicking disgo's sender loop without its 20ms sleeps.
func pullAll(t *testing.T, conn *mock.Conn) [][]byte {
	t.Helper()
	var frames [][]byte
	for range 100 {
		frame, err := conn.PullFrame()
		if err != nil {
			t.Fatalf("PullFrame: %v", err)
		}
		if frame == nil {
			return frames
		}
		frames = append(frames, frame)
	}
	t.Fatal("pullAll did not reach idle within 100 pulls")
	return nil
}
