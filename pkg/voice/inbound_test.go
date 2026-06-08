package voice

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

// countingMetrics records dropped-frame counts for assertions.
type countingMetrics struct {
	dropped  atomic.Int64
	started  atomic.Int64
	finished atomic.Int64
}

func (m *countingMetrics) InboundFramesDropped(_ string, n int) { m.dropped.Add(int64(n)) }
func (m *countingMetrics) PlaybackStarted(string)               { m.started.Add(1) }
func (m *countingMetrics) PlaybackFinished(string, bool)        { m.finished.Add(1) }

func pkt(seq uint16, ts uint32, ssrc uint32, opus []byte) *voice.Packet {
	return &voice.Packet{Sequence: seq, Timestamp: ts, SSRC: ssrc, Opus: opus}
}

func TestInboundDispatcher(t *testing.T) {
	t.Run("frame carries userID, sequence, silence flag", func(t *testing.T) {
		d := newTestInboundDispatcher("g", 4, discardMetrics{})
		uid := snowflake.ID(42)
		_ = d.ReceiveOpusFrame(uid, pkt(7, 0, 1, []byte{0xDE, 0xAD}))
		_ = d.ReceiveOpusFrame(uid, pkt(8, 960, 1, voice.SilenceAudioFrame))

		got := <-d.inbound()
		if got.UserID != uid || got.Sequence != 7 || got.Silence {
			t.Fatalf("voiced frame wrong: %+v", got)
		}
		silence := <-d.inbound()
		if !silence.Silence {
			t.Fatalf("expected silence flag set: %+v", silence)
		}
	})

	t.Run("Opus payload is cloned, not aliased", func(t *testing.T) {
		d := newTestInboundDispatcher("g", 4, discardMetrics{})
		buf := []byte{0x01, 0x02}
		_ = d.ReceiveOpusFrame(1, pkt(1, 0, 1, buf))
		buf[0] = 0xFF // disgo reuses its receive buffer; our frame must be unaffected
		got := <-d.inbound()
		if got.Opus[0] != 0x01 {
			t.Fatalf("frame aliased disgo's buffer: %x", got.Opus)
		}
	})

	t.Run("PTS is per-speaker relative", func(t *testing.T) {
		d := newTestInboundDispatcher("g", 8, discardMetrics{})
		// First packet from a speaker is the zero point regardless of its
		// (random) RTP base; the next, 48000 ticks later, is exactly 1s.
		_ = d.ReceiveOpusFrame(1, pkt(1, 1_000_000, 1, []byte{0x01}))
		_ = d.ReceiveOpusFrame(1, pkt(2, 1_000_000+rtpClockHz, 1, []byte{0x02}))
		first := <-d.inbound()
		second := <-d.inbound()
		if first.PTS != 0 {
			t.Fatalf("first PTS got %v want 0", first.PTS)
		}
		if second.PTS != time.Second {
			t.Fatalf("second PTS got %v want 1s", second.PTS)
		}
	})

	t.Run("resolved UserID is carried non-zero per speaker", func(t *testing.T) {
		// Speaker attribution is load-bearing: Transcripts attribute by Discord
		// User and Address Detection's last-speaker step needs it (CONTEXT.md).
		// disgo hands ReceiveOpusFrame the SSRC-resolved snowflake; assert each
		// distinct speaker's frame carries exactly that non-zero ID.
		d := newTestInboundDispatcher("g", 8, discardMetrics{})
		alice, bob := snowflake.ID(111), snowflake.ID(222)
		_ = d.ReceiveOpusFrame(alice, pkt(1, 0, 10, []byte{0x01}))
		_ = d.ReceiveOpusFrame(bob, pkt(1, 0, 20, []byte{0x02}))

		seen := map[snowflake.ID][]byte{}
		for range 2 {
			f := <-d.inbound()
			if f.UserID == 0 {
				t.Fatalf("frame %x carried zero UserID; speaker attribution lost", f.Opus)
			}
			seen[f.UserID] = f.Opus
		}
		if string(seen[alice]) != "\x01" || string(seen[bob]) != "\x02" {
			t.Fatalf("per-speaker attribution wrong: %v", seen)
		}
	})

	t.Run("unknown SSRC yields zero UserID", func(t *testing.T) {
		d := newTestInboundDispatcher("g", 4, discardMetrics{})
		_ = d.ReceiveOpusFrame(0, pkt(1, 0, 999, []byte{0x01})) // disgo passes 0 when SSRC unknown
		got := <-d.inbound()
		if got.UserID != 0 {
			t.Fatalf("got UserID %d want 0", got.UserID)
		}
	})

	t.Run("drop-oldest keeps newest under a full buffer", func(t *testing.T) {
		m := &countingMetrics{}
		d := newTestInboundDispatcher("g", 2, m) // buffer of 2
		// Push 4 without draining: seq 1,2 fill, 3 evicts 1, 4 evicts 2.
		for seq := uint16(1); seq <= 4; seq++ {
			_ = d.ReceiveOpusFrame(1, pkt(seq, uint32(seq), 1, []byte{byte(seq)}))
		}
		var got []uint32
		for range 2 {
			got = append(got, (<-d.inbound()).Sequence)
		}
		if got[0] != 3 || got[1] != 4 {
			t.Fatalf("drop-oldest kept %v want [3 4]", got)
		}
		if m.dropped.Load() != 2 {
			t.Fatalf("dropped count got %d want 2", m.dropped.Load())
		}
	})

	t.Run("Close closes inbound channel and is idempotent", func(t *testing.T) {
		d := newTestInboundDispatcher("g", 4, discardMetrics{})
		d.Close()
		d.Close() // must not panic
		if _, ok := <-d.inbound(); ok {
			t.Fatal("inbound channel should be closed")
		}
	})

	t.Run("receive after Close is a safe no-op", func(t *testing.T) {
		d := newTestInboundDispatcher("g", 4, discardMetrics{})
		d.Close()
		if err := d.ReceiveOpusFrame(1, pkt(1, 0, 1, []byte{0x01})); err != nil {
			t.Fatalf("post-close receive err: %v", err)
		}
	})
}

// TestInboundDispatcherCloseRace interleaves a foreign sender goroutine (disgo's
// receiver) with Close, the exact send-on-closed-channel hazard the RWMutex
// guards. Run with -race; it must neither panic nor deadlock.
func TestInboundDispatcherCloseRace(t *testing.T) {
	for range 50 {
		d := newTestInboundDispatcher("g", 8, discardMetrics{})

		// Drain consumer so sends mostly succeed.
		drained := make(chan struct{})
		go func() {
			for range d.inbound() {
			}
			close(drained)
		}()

		var senders sync.WaitGroup
		for s := range 4 {
			senders.Add(1)
			go func(ssrc uint32) {
				defer senders.Done()
				for i := range 100 {
					_ = d.ReceiveOpusFrame(snowflake.ID(ssrc), pkt(uint16(i), uint32(i), ssrc, []byte{byte(i)}))
				}
			}(uint32(s))
		}

		time.Sleep(time.Millisecond)
		d.Close()
		senders.Wait()
		<-drained
	}
}
