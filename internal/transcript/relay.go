// Package transcript is the SSE relay (ADR-0014 Hop-B): it bridges the
// in-process voiceevent.Bus to the browser's live Session screen. It subscribes
// to the bus ONCE at startup, projects the voice pipeline's events into
// human-readable transcript lines (ADR-0020 taxonomy, ADR-0039 anonymous human
// lane), keeps a per-session ~500-event ring buffer for Last-Event-ID replay,
// and serves two endpoints: a Server-Sent-Events live tail and a JSON snapshot.
//
// There is no DB write here: line persistence is issue #74. The buffer is the
// only state, scoped to the single active session (ADR-0039 single-operator) —
// a fresh buffer starts when the active session id changes.
package transcript

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

const (
	// ringCap bounds the per-session replay buffer (ADR-0014: ~500 events). A
	// reconnecting browser replays the frames after its Last-Event-ID; older
	// frames are dropped, so a very stale reconnect re-syncs from the snapshot.
	ringCap = 500
	// subBuffer sizes each live subscriber's channel. It MUST be <= ringCap so the
	// replay is lossless across a lagged drop: when a stalled reader's channel
	// overflows (subBuffer unsent frames) and is dropped, every frame it missed is
	// still within the ring's last ringCap, so its EventSource reconnect replays
	// them from Last-Event-ID with no gap. A reader keeping any reasonable pace
	// never overflows; an unbounded gap re-syncs from the snapshot.
	subBuffer = 256

	// listenLabel is the typing label while a session is live but no Agent is
	// mid-reply (the design's "Listening to the table…").
	listenLabel = "Listening to the table…"
)

// Kind classifies a transcript line's speaker. Per ADR-0039 the live projection
// only ever emits player/npc/butler — raw STT has no speaker id, so all humans
// share the anonymous "player" lane; gm is reserved for future SpeakerID work.
type Kind string

const (
	KindGM     Kind = "gm"
	KindPlayer Kind = "player"
	KindNPC    Kind = "npc"
	KindButler Kind = "butler"
)

// kindFor maps a speaker role to its transcript Kind. role is the
// AddressTarget.AgentRole ("butler"/"character") for an Agent reply, or one of
// the human pseudo-roles "gm"/"player" for human STT. The live projection only
// passes "player" for humans (ADR-0039); "gm" is reserved for future SpeakerID
// work, so an explicit gm input still maps to gm.
func kindFor(role string) Kind {
	switch role {
	case "butler":
		return KindButler
	case "gm":
		return KindGM
	case "player":
		return KindPlayer
	default: // "character" and any other Agent role
		return KindNPC
	}
}

// tagFor is the tiny uppercase pill the SPA renders beside an Agent's name —
// none for a human line.
func tagFor(k Kind) string {
	switch k {
	case KindButler:
		return "Butler"
	case KindNPC:
		return "NPC"
	default:
		return ""
	}
}

// Line is one rendered transcript line. ID is stable so the SPA upserts a turn's
// coalescing Agent reply in place; ts is the line's instant (RFC3339 on the
// wire).
type Line struct {
	ID   string    `json:"id"`
	Who  string    `json:"who"`
	Tag  string    `json:"tag,omitempty"`
	Kind Kind      `json:"kind"`
	TS   time.Time `json:"ts"`
	Text string    `json:"text"`
}

// Typing is the "is speaking" / "listening" indicator the SPA renders as the
// blinking dots + label. Derived server-side so it is unit-testable (ADR-0019).
type Typing struct {
	Active bool   `json:"active"`
	Label  string `json:"label"`
}

// status is the payload of a "status" frame and the status half of a snapshot:
// the live/idle session state plus the typing indicator.
type status struct {
	Status string `json:"status"` // "live" | "idle"
	Typing Typing `json:"typing"`
}

// View is the snapshot the JSON endpoint returns and the unit tests assert on:
// the current coalesced lines plus the derived status/typing.
type View struct {
	Lines  []Line `json:"lines"`
	Status string `json:"status"`
	Typing Typing `json:"typing"`
}

// Frame is one buffered SSE frame. Seq is the monotonic `id:` field a browser
// echoes back as Last-Event-ID; Event is the `event:` name; Data is the JSON
// `data:` payload (a Line or a status).
type Frame struct {
	Seq   uint64
	Event string
	Data  []byte
}

// Sessions is the narrow read the relay needs from the SessionManager: which
// voice session (if any) is currently active. *session.Manager satisfies it via
// Snapshot; tests fake it.
type Sessions interface {
	Snapshot() (storage.VoiceSession, bool)
}

// LineStore is the narrow persistence surface the relay needs (#74, ADR-0040):
// an incremental UPSERT of each projected Line, a list for replay-on-reload, and
// the authoritative count for the Stop summary. *storage.Store satisfies it;
// tests fake it. nil disables persistence (the live-only relay, e.g. unit tests).
type LineStore interface {
	UpsertTranscriptLine(ctx context.Context, l storage.TranscriptLine) error
	ListTranscriptLines(ctx context.Context, sessionID uuid.UUID) ([]storage.TranscriptLine, error)
	CountTranscriptLines(ctx context.Context, sessionID uuid.UUID) (int, error)
}

// turn holds the per-turn coalescing state: the routed target and the Agent's
// reply line whose text accumulates across the turn's TTSInvoked sentences.
// ended marks a finalized turn (TurnEnded seen) so a late TTSInvoked — which a
// barge can deliver AFTER the end — does not recreate the entry with a zero
// target and clobber the completed coalesced reply.
type turn struct {
	target voiceevent.AddressTarget
	line   *Line
	ended  bool
}

// Relay projects bus events into transcript lines and serves them over SSE +
// JSON. Safe for concurrent use: the bus callback, the HTTP handlers and View
// all take the same lock.
type Relay struct {
	sessions Sessions
	store    LineStore // persists projected lines (#74); nil disables persistence
	log      *slog.Logger

	mu       sync.Mutex
	activeID string // current session id; "" when idle
	// activeUUID / activeCampaignID mirror activeID as the typed FKs persistence
	// needs; captured from the active session's Snapshot at rollover.
	activeUUID       uuid.UUID
	activeCampaignID uuid.UUID
	buf              []Frame
	lines            []Line
	typing           Typing
	turns            map[string]*turn
	nextSeq          uint64
	humanSeq         uint64
	subs             map[*subscriber]struct{}

	// writeCh is the non-blocking queue draining into the single writer goroutine
	// (#74): emitLine tees each Line in here under r.mu, the bus contract forbids
	// blocking so the send drops on overflow, and Finalize sends a flush barrier.
	// nil when persistence is disabled (store == nil).
	writeCh chan writeOp

	// writeTimeout bounds each SSE frame write/flush (#148 Defect B): a client
	// that stops reading makes the blocked write fail instead of parking the
	// handler forever. Defaults to defaultWriteTimeout; tests shrink it.
	writeTimeout time.Duration

	// closing is closed by CloseStreams when the process begins its graceful
	// shutdown: every open SSE tail returns so the connections go idle and the
	// web tier's drain completes promptly (issue #138) — an SSE stream never
	// goes idle on its own and would otherwise stall shutdown for the full
	// grace period, only to be abandoned (not closed) at its expiry.
	closing   chan struct{}
	closeOnce sync.Once
}

// NewRelay subscribes to the bus once and returns a Relay ready to serve. The
// subscription lives for the process: the same bus persists across reconnect
// cycles AND across sessions (single active session, ADR-0039), so the relay
// sees every event without re-subscribing.
func NewRelay(bus *voiceevent.Bus, sessions Sessions, store LineStore, log *slog.Logger) *Relay {
	if log == nil {
		log = slog.Default()
	}
	r := &Relay{
		sessions:     sessions,
		store:        store,
		log:          log,
		turns:        map[string]*turn{},
		subs:         map[*subscriber]struct{}{},
		writeTimeout: defaultWriteTimeout,
		closing:      make(chan struct{}),
	}
	// One writer goroutine for the process drains the queue (#74). Only started
	// when persistence is enabled, so the live-only relay keeps its single-state
	// behaviour and unit tests with a nil store spawn nothing.
	if store != nil {
		r.writeCh = make(chan writeOp, persistQueue)
		go r.writeLoop()
	}
	bus.Subscribe(r.project)
	return r
}

// project is the bus callback: it attributes the event to the active session,
// rolling the buffer over on a session change, and folds the event into the
// transcript. It must not block (the bus delivers synchronously): all sends to
// live subscribers are non-blocking.
func (r *Relay) project(e voiceevent.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()

	id := r.currentSessionID()
	if id == "" {
		return // no active session — drop the event (ADR-0039)
	}
	if id != r.activeID {
		r.rollover(id)
	}

	switch ev := e.(type) {
	case voiceevent.VADSpeechStart:
		// A human opened their mouth: clear any stale "<NPC> is speaking…" the
		// moment speech starts (it fires BEFORE STTFinal), instead of waiting for
		// the next finalized line. Live-validated — a clean turn emits no
		// TurnEnded, so without this the last agent line keeps the label through
		// the following silence. Also correct for a barge (human over the Agent).
		r.setTyping(r.liveTyping())
	case voiceevent.STTFinal:
		// A human utterance — one line per STTFinal in the anonymous lane
		// (ADR-0039: raw STT carries no speaker id).
		r.humanSeq++
		r.emitLine(Line{
			ID:   "u:" + strconv.FormatUint(r.humanSeq, 10),
			Who:  "Player / DM",
			Kind: KindPlayer,
			TS:   ev.At,
			Text: ev.Text,
		})
	case voiceevent.AddressRouted:
		// Records WHO answers this turn; no line yet (no text).
		t := r.turn(ev.TurnID)
		t.target = ev.Target
	case voiceevent.TTSInvoked:
		// One sentence of the Agent's reply — coalesced into the turn's line.
		t := r.turn(ev.TurnID)
		if t.ended {
			// A barge can deliver a sentence after TurnEnded; ignore it so the
			// finalized reply is not clobbered (FIX 2). typing already cleared.
			return
		}
		if t.line == nil {
			k := kindFor(t.target.AgentRole)
			t.line = &Line{
				ID:   "a:" + ev.TurnID,
				Who:  nameOr(t.target.Name, "NPC"),
				Tag:  tagFor(k),
				Kind: k,
				TS:   ev.At,
				Text: ev.Sentence,
			}
		} else if ev.Sentence != "" {
			// Only separate with a space when there is preceding text — an empty
			// first sentence then a non-empty one must not leave a leading space.
			if t.line.Text != "" {
				t.line.Text += " "
			}
			t.line.Text += ev.Sentence
		}
		// emitLine derives typing from the line's kind (FIX 1), so a clean turn
		// (which emits NO TurnEnded) still shows "speaking" and a later human line
		// returns to listening — no standalone setTyping here.
		r.emitLine(*t.line)
	case voiceevent.TurnEnded:
		// Mark the turn finalized (keep the entry so a late sentence is dropped,
		// FIX 2) and fall back to listening — correct for a barge that cut the
		// Agent off mid-reply.
		r.turn(ev.TurnID).ended = true
		r.setTyping(r.liveTyping())
	}
}

// CloseStreams releases every open SSE tail — and makes any later ServeEvents
// return right after its replay — so the connections go idle and the web tier's
// graceful shutdown drains promptly (issue #138). Wired as the web server's
// RegisterOnShutdown hook; net/http then closes the idled connections. The
// browser's EventSource sees a clean stream end and reconnects into the
// restarted process. Idempotent and safe from any goroutine.
func (r *Relay) CloseStreams() {
	r.closeOnce.Do(func() { close(r.closing) })
}

// currentSessionID returns the active session's id, or "" when idle.
func (r *Relay) currentSessionID() string {
	vs, active := r.sessions.Snapshot()
	if !active {
		return ""
	}
	return vs.ID.String()
}

// rollover starts a fresh buffer for a newly-active session id and seeds it with
// the initial live/listening status frame, so a client connecting mid-session
// replays a coherent state.
func (r *Relay) rollover(id string) {
	r.activeID = id
	// Capture the typed session + campaign ids persistence needs as FKs (#74).
	// During a rollover the snapshot is active, so this is the freshly-active row.
	if vs, ok := r.sessions.Snapshot(); ok {
		r.activeUUID = vs.ID
		r.activeCampaignID = vs.CampaignID
	}
	r.buf = nil
	r.lines = nil
	r.turns = map[string]*turn{}
	r.nextSeq = 0
	r.humanSeq = 0
	r.typing = r.liveTyping()
	r.emit(Frame{Event: "status", Data: mustJSON(status{Status: "live", Typing: r.typing})})
}

// turn returns the coalescing state for a turn id, creating it on first sight.
func (r *Relay) turn(id string) *turn {
	t := r.turns[id]
	if t == nil {
		t = &turn{}
		r.turns[id] = t
	}
	return t
}

// liveTyping is the indicator while a session is live but no Agent is mid-reply.
func (r *Relay) liveTyping() Typing {
	return Typing{Active: true, Label: listenLabel}
}

// emitLine upserts the line into the current display state (replacing a turn's
// coalescing reply in place), emits a "line" frame, and derives the typing
// indicator from the line's kind (FIX 1). Deriving it from the LAST emitted line
// — Agent line => "<who> is speaking…", human line => listening — matches the
// design rule and is robust to a CLEAN turn, which emits no TurnEnded: without
// this the label would stick on "speaking" through the following silence and
// human turns.
func (r *Relay) emitLine(l Line) {
	replaced := false
	for i := range r.lines {
		if r.lines[i].ID == l.ID {
			r.lines[i] = l
			replaced = true
			break
		}
	}
	if !replaced {
		r.lines = append(r.lines, l)
	}
	r.emit(Frame{Event: "line", Data: mustJSON(l)})
	// emit assigned this line frame's seq to r.nextSeq; tee the line for durable
	// persistence with that seq as its ordering key, BEFORE the typing status
	// frame below bumps nextSeq again (#74).
	r.persist(l, r.nextSeq)

	switch l.Kind {
	case KindNPC, KindButler:
		r.setTyping(Typing{Active: true, Label: l.Who + " is speaking…"})
	default: // KindPlayer / KindGM — a human line means we are back to listening
		r.setTyping(r.liveTyping())
	}
}

// setTyping emits a "status" frame only when the typing indicator changes, so an
// idle session does not churn the stream.
func (r *Relay) setTyping(t Typing) {
	if t == r.typing {
		return
	}
	r.typing = t
	r.emit(Frame{Event: "status", Data: mustJSON(status{Status: "live", Typing: t})})
}

// emit assigns the next seq, appends to the ring (dropping the oldest past cap)
// and fans the frame out to live subscribers. Caller holds r.mu.
func (r *Relay) emit(f Frame) {
	r.nextSeq++
	f.Seq = r.nextSeq
	r.buf = append(r.buf, f)
	if len(r.buf) > ringCap {
		r.buf = append([]Frame(nil), r.buf[len(r.buf)-ringCap:]...)
	}
	r.push(f)
}

// View returns the current snapshot for id: the coalesced lines plus the derived
// status/typing. status is recomputed from the live manager state, so it reads
// idle the moment the session ends even though the buffer's last frame said live.
func (r *Relay) View(id string) View {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.currentSessionID() != id || id != r.activeID {
		return View{Lines: []Line{}, Status: "idle", Typing: Typing{}}
	}
	return View{
		Lines:  append([]Line(nil), r.lines...),
		Status: "live",
		Typing: r.typing,
	}
}

// snapshot returns the initial-state View for id: the in-memory live state when id
// IS the active session, else the DB-persisted history with status "idle" (#74).
// Live behaviour is unchanged from View; the persisted path is the
// reconnect/reload history for an ended session.
func (r *Relay) snapshot(ctx context.Context, id string) View {
	r.mu.Lock()
	live := r.currentSessionID() == id && id == r.activeID
	if live {
		v := View{Lines: append([]Line(nil), r.lines...), Status: "live", Typing: r.typing}
		r.mu.Unlock()
		return v
	}
	r.mu.Unlock()
	return r.persistedView(ctx, id)
}

// persistedView reads an ended session's lines from the store, ordered by seq, and
// returns them as an idle View (#74). A nil store, an unparseable id, or a read
// error degrades to the empty idle view (logged) so the screen still renders.
func (r *Relay) persistedView(ctx context.Context, id string) View {
	empty := View{Lines: []Line{}, Status: "idle", Typing: Typing{}}
	if r.store == nil {
		return empty
	}
	sid, err := uuid.Parse(id)
	if err != nil {
		return empty
	}
	rows, err := r.store.ListTranscriptLines(ctx, sid)
	if err != nil {
		r.log.Warn("transcript: load persisted snapshot", "err", err, "session", id)
		return empty
	}
	lines := make([]Line, 0, len(rows))
	for _, t := range rows {
		lines = append(lines, Line{
			ID:   t.LineID,
			Who:  t.Who,
			Tag:  t.Tag,
			Kind: Kind(t.Kind),
			TS:   t.TS,
			Text: t.Text,
		})
	}
	return View{Lines: lines, Status: "idle", Typing: Typing{}}
}

// Frames returns the buffered frames for id with Seq > afterSeq — the
// Last-Event-ID replay set. Empty when id is not the active session.
func (r *Relay) Frames(id string, afterSeq uint64) []Frame {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id != r.activeID {
		return nil
	}
	var out []Frame
	for _, f := range r.buf {
		if f.Seq > afterSeq {
			out = append(out, f)
		}
	}
	return out
}

// nameOr returns name, or def when name is empty.
func nameOr(name, def string) string {
	if name == "" {
		return def
	}
	return name
}

// mustJSON marshals v, panicking on a programming error (the relay's own fixed
// structs are always marshalable).
func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic("transcript: marshal frame: " + err.Error())
	}
	return b
}
