// Package tape is the in-memory rollover tape (ADR-0051): a bounded per-Speaker
// ring buffer of recent Opus audio that the Session Highlights epic (#257) cuts
// clips from. It exists ONLY while a Voice Session runs with the tape armed, and
// everything in it is discarded at session end — only GM-promoted Highlights
// outlive the session.
//
// Consent is enforced at the tape boundary (ADR-0051 "not even transiently"): an
// inbound frame from a Speaker who has not consented never enters a ring, checked
// against a copy-on-write consent set BEFORE the frame is enqueued. Agent speech
// is synthetic and always captured. Revoking consent both stops future capture
// and clears that Speaker's ring immediately.
//
// The hot path adds ZERO latency to the live audio loop: [Tape.AppendInbound] and
// [Tape.AppendAgent] are non-blocking and allocation-free — they check consent
// (an atomic map load) and hand the frame to a buffered mailbox with a drop-oldest
// policy (the pkg/voice inboundDispatcher.send idiom). A single owner goroutine
// owns every per-lane ring, so the rings need no locks; [Tape.Snapshot] is a
// request/response over the same owner, yielding a consistent copy without ever
// stopping the world.
package tape

import (
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// AgentLaneID is the lane id of the outbound TTS (agent) audio. Agent speech is
// synthetic, produced on the playback path, and always on tape (ADR-0051), so it
// is never subject to the consent gate that guards inbound Speaker lanes.
const AgentLaneID = "agent"

// Window is the rollover tape's retention window (ADR-0051): the ring covers the
// most recent 120 seconds of each lane and drops older audio.
const Window = 120 * time.Second

// frameInterval is one Opus packet's duration (~20 ms); the per-lane ring holds
// window/frameInterval frames.
const frameInterval = 20 * time.Millisecond

// maxLanes caps the number of distinct lanes the tape will allocate rings for, a
// defensive bound against an unbounded set of Speaker ids (a returning user with a
// changed SSRC, a misbehaving client). Well above any real table's size.
const maxLanes = 16

// mailboxCap is the owner mailbox depth for append frames. Matched to the
// inboundDispatcher buffer: deep enough to absorb scheduling jitter, drop-oldest
// once full so the audio loop never blocks.
const mailboxCap = 512

// Frame is one ~20 ms Opus payload stamped with its wall-clock time: arrival time
// for inbound Speaker audio, pulled-to-wire time for agent audio. Opus aliases the
// caller's slice (gxvoice.Frame.Opus is already cloned by the inbound dispatcher),
// so callers must not mutate it after appending.
type Frame struct {
	Opus []byte
	At   time.Time
}

// LaneSnapshot is one lane's frames within a snapshot range, sorted ascending by
// At. LaneID is a Discord snowflake string for a Speaker lane, or [AgentLaneID].
type LaneSnapshot struct {
	LaneID string
	Frames []Frame
}

// Snapshot is a consistent copy of every lane over [From, To], its lanes sorted by
// LaneID. It is the input the mixdown slice (#304) assembles a clip from.
type Snapshot struct {
	From, To time.Time
	Lanes    []LaneSnapshot
}

// Tape is the running rollover tape. Construct it with [New]; feed it with
// [Tape.AppendInbound]/[Tape.AppendAgent]; cut clips with [Tape.Snapshot]; and
// discard it with [Tape.Close] at session end.
type Tape struct {
	capFrames int
	log       *slog.Logger

	// consent is the copy-on-write set of consented Speaker ids
	// (map[string]struct{}), swapped atomically by SetConsent and loaded by
	// AppendInbound before enqueue. consentMu serializes the read-modify-swap.
	consent   atomic.Value
	consentMu sync.Mutex

	// mailbox is the single ordered channel to the owner goroutine, carrying both
	// append frames and control requests (snapshot, revoke). One channel keeps a
	// Snapshot FIFO-ordered after the appends that precede it — the "request/response
	// over the same mailbox" contract — so a snapshot always observes prior frames.
	// Appends use a drop-oldest policy (never blocking the audio loop); control
	// messages block-send guarded by done and are never dropped.
	mailbox chan tapeMsg
	stop    chan struct{} // closed by Close to stop the owner
	done    chan struct{} // closed by the owner when it has exited
	once    sync.Once
	closed  atomic.Bool
}

// tapeMsg is one message to the owner: an append (isAppend), a snapshot request
// (snap != nil), or a lane clear on consent revoke (revoke). Exactly one applies.
type tapeMsg struct {
	isAppend   bool
	lane       string
	frame      Frame
	snap       *snapReq
	revoke     bool
	revokeLane string
}

type snapReq struct {
	from, to time.Time
	resp     chan Snapshot
}

// New builds a tape retaining window per lane, with consented seeded from the
// given Speaker ids (agent audio is always captured regardless). A nil logger
// discards logs. It starts the owner goroutine; call [Tape.Close] to stop it.
func New(window time.Duration, consented []string, log *slog.Logger) *Tape {
	if log == nil {
		log = slog.New(slog.NewTextHandler(discardWriter{}, nil))
	}
	capFrames := int(window / frameInterval)
	if capFrames < 1 {
		capFrames = 1
	}
	t := &Tape{
		capFrames: capFrames,
		log:       log,
		mailbox:   make(chan tapeMsg, mailboxCap),
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	set := make(map[string]struct{}, len(consented))
	for _, id := range consented {
		set[id] = struct{}{}
	}
	t.consent.Store(set)
	go t.run()
	return t
}

// AppendInbound captures one inbound Opus frame from userID at wall-clock at. It
// is non-blocking and allocation-free. Frames from a Speaker who has not consented
// are dropped BEFORE they touch any buffer (ADR-0051): the consent check happens
// here, on the caller's goroutine, so unconsented audio never enters even
// transiently. When the append mailbox is full the oldest queued frame is dropped.
func (t *Tape) AppendInbound(userID string, opus []byte, at time.Time) {
	set, _ := t.consent.Load().(map[string]struct{})
	if _, ok := set[userID]; !ok {
		return
	}
	t.enqueue(userID, Frame{Opus: opus, At: at})
}

// AppendAgent captures one outbound (agent TTS) Opus frame pulled to the wire at
// wall-clock at. Agent speech is always on tape (ADR-0051), so no consent check
// applies. Non-blocking and drop-oldest, like [Tape.AppendInbound].
func (t *Tape) AppendAgent(opus []byte, at time.Time) {
	t.enqueue(AgentLaneID, Frame{Opus: opus, At: at})
}

// enqueue hands lane+frame to the owner with a drop-oldest policy: if the mailbox
// is full, drop the oldest queued frame and retry, never blocking the audio loop
// (the pkg/voice inboundDispatcher.send idiom). After Close it is a no-op.
func (t *Tape) enqueue(lane string, f Frame) {
	if t.closed.Load() {
		return
	}
	m := tapeMsg{isAppend: true, lane: lane, frame: f}
	select {
	case t.mailbox <- m:
		return
	default:
	}
	// Mailbox full: drop the oldest queued frame to make room. If the oldest is a
	// control message (a rare snapshot/revoke behind 512 backlogged frames), do NOT
	// drop it — re-queue it and drop the incoming append instead, so a Snapshot is
	// never lost (which would strand its caller).
	select {
	case old := <-t.mailbox:
		if !old.isAppend {
			select {
			case t.mailbox <- old:
			default:
			}
			return
		}
	default:
	}
	select {
	case t.mailbox <- m:
	default:
	}
}

// SetConsent grants or revokes tape consent for one Speaker. Granting lets future
// frames from userID enter the tape; revoking both stops future capture and clears
// that Speaker's existing ring (ADR-0051: revocation is not just prospective). The
// consent set is swapped copy-on-write so [Tape.AppendInbound] never blocks on it.
func (t *Tape) SetConsent(userID string, granted bool) {
	t.consentMu.Lock()
	old, _ := t.consent.Load().(map[string]struct{})
	next := make(map[string]struct{}, len(old)+1)
	for id := range old {
		next[id] = struct{}{}
	}
	if granted {
		next[userID] = struct{}{}
	} else {
		delete(next, userID)
	}
	t.consent.Store(next)
	t.consentMu.Unlock()

	if !granted {
		// Clear any already-captured audio for the revoked Speaker. Ordered after
		// prior appends on the same mailbox; guarded by done so Close can't strand it.
		select {
		case t.mailbox <- tapeMsg{revoke: true, revokeLane: userID}:
		case <-t.done:
		}
	}
}

// Snapshot returns a consistent copy of every lane's frames over [from, to],
// serviced by the owner goroutine so it never races an append. After Close it
// returns an empty snapshot for the range.
func (t *Tape) Snapshot(from, to time.Time) Snapshot {
	req := &snapReq{from: from, to: to, resp: make(chan Snapshot, 1)}
	select {
	case t.mailbox <- tapeMsg{snap: req}:
	case <-t.done:
		return Snapshot{From: from, To: to}
	}
	select {
	case s := <-req.resp:
		return s
	case <-t.done:
		return Snapshot{From: from, To: to}
	}
}

// Close stops the owner goroutine and discards every ring. Appends after Close are
// no-ops. It is idempotent and blocks until the owner has exited.
func (t *Tape) Close() {
	t.closed.Store(true)
	t.once.Do(func() { close(t.stop) })
	<-t.done
}

// run is the single owner goroutine: it owns every per-lane ring, so no ring ever
// needs a lock. It drains the append mailbox and services control requests
// (snapshot, revoke) until Close. The stop arm is checked first on every wakeup so
// Close is prompt and a Snapshot blocked mid-flight unblocks via the done channel.
func (t *Tape) run() {
	defer close(t.done)
	rings := make(map[string]*ring)
	for {
		select {
		case <-t.stop:
			return
		default:
		}
		select {
		case <-t.stop:
			return
		case m := <-t.mailbox:
			switch {
			case m.isAppend:
				t.appendRing(rings, m.lane, m.frame)
			case m.snap != nil:
				m.snap.resp <- buildSnapshot(rings, m.snap.from, m.snap.to)
			case m.revoke:
				delete(rings, m.revokeLane)
			}
		}
	}
}

// appendRing routes one frame into its lane's ring, allocating the ring lazily and
// refusing new lanes past maxLanes.
func (t *Tape) appendRing(rings map[string]*ring, lane string, f Frame) {
	r := rings[lane]
	if r == nil {
		if len(rings) >= maxLanes {
			t.log.Warn("tape: lane cap reached, dropping frame", "lane", lane, "max", maxLanes)
			return
		}
		r = newRing(t.capFrames)
		rings[lane] = r
	}
	r.append(f)
}

// buildSnapshot copies every lane's in-range frames into a Snapshot, lanes sorted
// by LaneID and each lane's frames sorted ascending by At.
func buildSnapshot(rings map[string]*ring, from, to time.Time) Snapshot {
	s := Snapshot{From: from, To: to}
	for lane, r := range rings {
		frames := r.inRange(from, to)
		if len(frames) == 0 {
			continue
		}
		s.Lanes = append(s.Lanes, LaneSnapshot{LaneID: lane, Frames: frames})
	}
	sort.Slice(s.Lanes, func(i, j int) bool { return s.Lanes[i].LaneID < s.Lanes[j].LaneID })
	return s
}

// ring is a fixed-capacity circular buffer of frames owned solely by the owner
// goroutine. Its backing slice is preallocated so steady-state append never
// allocates; once full, append overwrites the oldest frame (drop-oldest).
type ring struct {
	buf   []Frame
	start int // index of the oldest frame
	n     int // number of frames currently held
}

func newRing(capFrames int) *ring {
	return &ring{buf: make([]Frame, capFrames)}
}

func (r *ring) append(f Frame) {
	if r.n < len(r.buf) {
		r.buf[(r.start+r.n)%len(r.buf)] = f
		r.n++
		return
	}
	r.buf[r.start] = f
	r.start = (r.start + 1) % len(r.buf)
}

// inRange returns a copy of the held frames whose At is within [from, to],
// sorted ascending by At.
func (r *ring) inRange(from, to time.Time) []Frame {
	var out []Frame
	for i := 0; i < r.n; i++ {
		f := r.buf[(r.start+i)%len(r.buf)]
		if f.At.Before(from) || f.At.After(to) {
			continue
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// discardWriter is the sink for the default no-op logger.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
