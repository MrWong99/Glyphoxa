package voice

import (
	"bytes"
	"sync"
	"time"

	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
)

// rtpClockHz is the 48kHz clock the Discord voice RTP timestamp is sampled at;
// dividing a per-stream timestamp delta by it yields wall-clock duration.
const rtpClockHz = 48000

// Frame is one inbound Opus packet tagged with its speaker, the unit a Session
// hands STT. disgo resolves the RTP SSRC to a Discord User ID for us; an
// unrecognised SSRC (audio arriving before the speaking event) yields a zero
// UserID, which we pass through rather than buffer (latency budget).
type Frame struct {
	// UserID is the speaking Discord User, or zero if the SSRC is not yet known.
	UserID snowflake.ID
	// Opus is the raw Opus payload for one ~20ms window.
	Opus []byte
	// PTS is the presentation timestamp relative to the first frame seen from
	// this UserID, derived from the RTP timestamp. It is per-speaker, not
	// session-global, and resets if a speaker's SSRC changes.
	PTS time.Duration
	// Sequence is the RTP packet sequence number (widened from disgo's uint16).
	Sequence uint32
	// Silence is true when the packet is Discord's Opus silence frame.
	Silence bool
}

// inboundDispatcher implements disgo's voice.OpusFrameReceiver. disgo runs a
// receiver goroutine — which we do not own — that calls ReceiveOpusFrame; on
// [Session.Close], disgo cancels and then calls our Close. Because a receive
// can be in flight when Close runs, we guard the channel with an RWMutex:
// senders hold the read lock and check closed, Close takes the write lock so it
// waits out in-flight sends before closing the channel. This is the only safe
// way to close a channel a foreign goroutine still sends to.
type inboundDispatcher struct {
	guild   string
	metrics MetricsRecorder

	frames chan Frame

	mu     sync.RWMutex
	closed bool

	// basePTS tracks the first RTP timestamp seen per speaker so PTS is
	// per-stream relative. Guarded by ptsMu, independent of the channel lock.
	ptsMu   sync.Mutex
	basePTS map[snowflake.ID]uint32
}

func newInboundDispatcher(guild string, bufferSize int, metrics MetricsRecorder) *inboundDispatcher {
	return &inboundDispatcher{
		guild:   guild,
		metrics: metrics,
		frames:  make(chan Frame, bufferSize),
		basePTS: make(map[snowflake.ID]uint32),
	}
}

// inbound exposes the receive end as the package's read-only channel.
func (d *inboundDispatcher) inbound() <-chan Frame { return d.frames }

// ReceiveOpusFrame is called by disgo's receiver goroutine for every inbound
// packet. It applies a drop-oldest policy: when the buffer is full it discards
// the oldest queued frame to make room, favouring recency for VAD/STT.
func (d *inboundDispatcher) ReceiveOpusFrame(userID snowflake.ID, packet *voice.Packet) error {
	if packet == nil {
		return nil
	}
	frame := Frame{
		UserID:   userID,
		Opus:     bytes.Clone(packet.Opus), // disgo reuses its receive buffer
		PTS:      d.pts(userID, packet.Timestamp),
		Sequence: uint32(packet.Sequence),
		Silence:  bytes.Equal(packet.Opus, voice.SilenceAudioFrame),
	}

	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.closed {
		return nil
	}
	d.send(frame)
	return nil
}

// send enqueues frame, dropping the oldest queued frame if the buffer is full.
// The caller holds the read lock, so the channel cannot be closed underneath us
// and the drain-then-resend dance is safe.
func (d *inboundDispatcher) send(frame Frame) {
	select {
	case d.frames <- frame:
		return
	default:
	}
	// Buffer full: drop the oldest, then enqueue. Both ops may still lose a race
	// with a fast consumer, so re-try the send and only count a real drop.
	select {
	case <-d.frames:
		d.metrics.InboundFramesDropped(d.guild, 1)
	default:
	}
	select {
	case d.frames <- frame:
	default:
		// Consumer refilled the buffer between our drop and resend; drop the new
		// frame rather than block disgo's receiver goroutine.
		d.metrics.InboundFramesDropped(d.guild, 1)
	}
}

// pts returns the presentation timestamp for a packet relative to the first one
// seen from userID. The RTP timestamp starts at a random offset and wraps
// uint32, so absolute values are meaningless; per-stream relative deltas are
// not. Unsigned subtraction makes wrap-around handle itself.
func (d *inboundDispatcher) pts(userID snowflake.ID, ts uint32) time.Duration {
	d.ptsMu.Lock()
	defer d.ptsMu.Unlock()
	base, ok := d.basePTS[userID]
	if !ok {
		d.basePTS[userID] = ts
		return 0
	}
	delta := ts - base
	return time.Duration(delta) * time.Second / rtpClockHz
}

// CleanupUser drops per-speaker state when disgo reports a user left, so a
// returning user's PTS restarts from zero.
func (d *inboundDispatcher) CleanupUser(userID snowflake.ID) {
	d.ptsMu.Lock()
	delete(d.basePTS, userID)
	d.ptsMu.Unlock()
}

// Close is called by disgo when the receiver tears down. It takes the write
// lock to wait out any in-flight ReceiveOpusFrame, then closes the channel
// exactly once so a [Session]'s Inbound consumer observes a clean end.
func (d *inboundDispatcher) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	d.closed = true
	close(d.frames)
}
