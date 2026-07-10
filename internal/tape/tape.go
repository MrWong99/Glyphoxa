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

	// appends is the frame mailbox to the owner goroutine. It carries ONLY appends,
	// so the drop-oldest policy (a full mailbox pops its oldest frame to make room —
	// the pkg/voice inboundDispatcher.send idiom, run by the appending goroutine) can
	// never discard a control request. Never blocks the audio loop.
	//
	// ctrl is a SEPARATE channel for control requests (snapshot, revoke), drained
	// ONLY by the owner and never dropped. Keeping control off the frame mailbox is
	// what fixes the deadlock: with one shared channel an appender's drop-oldest pop
	// could evict a Snapshot request, stranding its caller forever. Ordering (a
	// Snapshot must observe the appends that precede it) is preserved by the owner
	// draining every pending frame from appends BEFORE it services a control request.
	appends chan laneFrame
	ctrl    chan ctrlMsg
	stop    chan struct{} // closed by Close to stop the owner
	done    chan struct{} // closed by the owner when it has exited
	once    sync.Once
	closed  atomic.Bool

	// warnedLanes records lane ids already warned about at the maxLanes cap, so the
	// warning fires once per over-cap lane rather than once per dropped frame (50/s
	// per speaker). Owner-goroutine-only, so it needs no lock.
	warnedLanes map[string]struct{}
}

// laneFrame is one append: a frame plus the lane it belongs to.
type laneFrame struct {
	lane  string
	frame Frame
}

// ctrlMsg is one control request to the owner: a snapshot (snap != nil), a lane
// clear on consent revoke (revoke), or an authoritative consent reconcile
// (reconcile). Exactly one applies.
type ctrlMsg struct {
	snap       *snapReq
	revoke     bool
	revokeLane string
	// reconcile, with allowed set, drops every non-agent lane whose id is not in
	// allowed — clearing the buffered audio of any Speaker who lost consent since
	// the last reconcile (the authoritative store-backed reseed, #306).
	reconcile bool
	allowed   map[string]struct{}
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
		appends:   make(chan laneFrame, mailboxCap),
		ctrl:      make(chan ctrlMsg),
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
	m := laneFrame{lane: lane, frame: f}
	select {
	case t.appends <- m:
		return
	default:
	}
	// Mailbox full: drop the oldest queued frame to make room, then retry. The
	// appends channel carries ONLY frames, so the popped item is always a frame —
	// this can never evict a control request (that hazard is why control lives on a
	// separate channel).
	select {
	case <-t.appends:
	default:
	}
	select {
	case t.appends <- m:
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
		// Clear any already-captured audio for the revoked Speaker. Delivered on the
		// owner-drained ctrl channel (never dropped); guarded by done so Close can't
		// strand it. The owner drains pending appends before applying it, so a frame
		// enqueued before this revoke is captured-then-cleared, never orphaned.
		select {
		case t.ctrl <- ctrlMsg{revoke: true, revokeLane: userID}:
		case <-t.done:
		}
	}
}

// ReconcileConsent replaces the consent set with exactly consented (agent audio is
// always captured regardless) and clears the buffered audio of any Speaker who is
// no longer in the set. It is the authoritative reseed the live wiring drives from
// the durable consent rows (#306, ADR-0051): applied at each Voice Session cycle
// start and on every consent-change event, so a consent grant/revoke that landed
// while nothing was subscribed (a reconnect gap) — or two racing events that
// published out of order — always converges the tape to the persisted truth,
// exactly as the mute wiring re-reads its authoritative view.
func (t *Tape) ReconcileConsent(consented []string) {
	set := make(map[string]struct{}, len(consented))
	for _, id := range consented {
		set[id] = struct{}{}
	}
	t.consentMu.Lock()
	t.consent.Store(set)
	t.consentMu.Unlock()

	// Clear any buffered audio for Speakers no longer consented. Delivered on the
	// owner-drained ctrl channel; guarded by done so Close can't strand it.
	select {
	case t.ctrl <- ctrlMsg{reconcile: true, allowed: set}:
	case <-t.done:
	}
}

// Snapshot returns a consistent copy of every lane's frames over [from, to],
// serviced by the owner goroutine so it never races an append. After Close it
// returns an empty snapshot for the range.
func (t *Tape) Snapshot(from, to time.Time) Snapshot {
	req := &snapReq{from: from, to: to, resp: make(chan Snapshot, 1)}
	select {
	case t.ctrl <- ctrlMsg{snap: req}:
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
// needs a lock. It drains the frame mailbox and services control requests
// (snapshot, revoke) until Close. The stop arm is checked first on every wakeup so
// Close is prompt and a Snapshot blocked mid-flight unblocks via the done channel.
//
// Before servicing any control request it drains every pending frame from the
// mailbox, so a Snapshot always observes the appends enqueued before it (the
// ordering the caller expects) and never blocks appends: the snapshot reply rides
// a buffered response channel, so the owner returns to draining at once.
func (t *Tape) run() {
	defer close(t.done)
	rings := make(map[string]*ring)
	t.warnedLanes = make(map[string]struct{})
	for {
		select {
		case <-t.stop:
			return
		default:
		}
		select {
		case <-t.stop:
			return
		case lf := <-t.appends:
			t.appendRing(rings, lf.lane, lf.frame)
		case c := <-t.ctrl:
			t.drainAppends(rings)
			switch {
			case c.snap != nil:
				c.snap.resp <- buildSnapshot(rings, c.snap.from, c.snap.to)
			case c.revoke:
				delete(rings, c.revokeLane)
			case c.reconcile:
				for lane := range rings {
					if lane == AgentLaneID {
						continue
					}
					if _, ok := c.allowed[lane]; !ok {
						delete(rings, lane)
					}
				}
			}
		}
	}
}

// drainAppends applies every frame currently queued in the mailbox to its ring,
// non-blocking. The owner calls it before each control request so a Snapshot
// observes all prior appends and a revoke clears after them.
func (t *Tape) drainAppends(rings map[string]*ring) {
	for {
		select {
		case lf := <-t.appends:
			t.appendRing(rings, lf.lane, lf.frame)
		default:
			return
		}
	}
}

// appendRing routes one frame into its lane's ring, allocating the ring lazily and
// refusing new lanes past maxLanes.
//
// It re-checks consent for non-agent lanes against the current set before storing
// (ADR-0051): AppendInbound already gates at enqueue, but a revoke can land between
// that check and this apply — the appender loaded a stale "consented" set, then
// consent was revoked and the lane cleared, and only now does the buffered frame
// arrive. Re-reading here (one atomic load, on the owner goroutine) drops that
// straggler so a revoked Speaker's audio can never re-enter the ring.
func (t *Tape) appendRing(rings map[string]*ring, lane string, f Frame) {
	if lane != AgentLaneID {
		set, _ := t.consent.Load().(map[string]struct{})
		if _, ok := set[lane]; !ok {
			return // consent revoked between enqueue and apply — drop the straggler
		}
	}
	r := rings[lane]
	if r == nil {
		if len(rings) >= maxLanes {
			if _, warned := t.warnedLanes[lane]; !warned {
				t.warnedLanes[lane] = struct{}{}
				t.log.Warn("tape: lane cap reached, dropping frames for lane", "lane", lane, "max", maxLanes)
			}
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
