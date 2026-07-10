// Package transcript is the SSE relay (ADR-0014 Hop-B): it bridges the
// in-process voiceevent.Bus to the browser's live Session screen. It subscribes
// to the bus ONCE at startup, projects the voice pipeline's events into
// human-readable transcript lines (ADR-0020 taxonomy; humans attributed to their
// Speaker Lane via SpeakerID since ADR-0050, resolved to Character/GM names by
// #281), keeps a per-session ~500-event ring buffer for Last-Event-ID replay,
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

	"github.com/MrWong99/Glyphoxa/internal/speaker"
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

// Kind classifies a transcript line's speaker. Humans resolve to player or gm from
// their Speaker Lane's SpeakerID (#281, ADR-0050): a GM-allowlisted snowflake lands
// in the gm lane, everyone else in player; an unattributed or resolver-off line
// stays in the anonymous player lane. Agent replies map to npc/butler.
type Kind string

const (
	KindGM     Kind = "gm"
	KindPlayer Kind = "player"
	KindNPC    Kind = "npc"
	KindButler Kind = "butler"
)

// kindFor maps a speaker role to its transcript Kind. role is the
// AddressTarget.AgentRole ("butler"/"character") for an Agent reply, or one of
// the human pseudo-roles "gm"/"player" for human STT. Human lines derive their
// Kind directly in resolveHuman (#281); this helper serves the Agent-reply path
// and still maps an explicit "gm"/"player" input for completeness.
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
	// SpeakerID is the Discord snowflake of the human who spoke this Line (#278,
	// ADR-0050), threaded from STTFinal for persistence + attribution (#281). Empty
	// for an unattributed utterance or an Agent reply; the omitempty keeps the
	// live-view wire byte-identical for the anonymous path. Who/Kind are resolved
	// from this snowflake at persist time (#281) — a display snapshot, never
	// re-resolved on replay.
	SpeakerID string `json:"speaker_id,omitempty"`
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

// muteFrame is the payload of a "mute" SSE frame (#211): one Agent's mute state
// flipped, so the web Voice panel patches that row without a reload. Deliberately
// minimal — the authoritative current set is GetSession.muted_agent_ids.
type muteFrame struct {
	AgentID string `json:"agent_id"`
	Muted   bool   `json:"muted"`
}

// spendcapFrame is the payload of a "spendcap" SSE frame (#130): the session's
// estimated spend crossed a cap, so the Session screen renders the spend-cap-reached
// state without a reload. Deliberately minimal (which cap) — the authoritative
// current state + estimated spend is GetSession (spend_cap_state / estimated_spend_usd).
type spendcapFrame struct {
	Level string `json:"level"`
}

// connectionFrame is the payload of a "connection" SSE frame (#123): the Voice
// Session's gateway connection state (connecting/connected/failed) and, on a fatal
// failure, the readable reason. The Session screen flips its badge from this live;
// the terminal reload truth for a failed session is GetSession (status + end_reason).
type connectionFrame struct {
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}

// View is the snapshot the JSON endpoint returns and the unit tests assert on:
// the current coalesced lines plus the derived status/typing and the live
// connection state (#123).
type View struct {
	Lines  []Line `json:"lines"`
	Status string `json:"status"`
	Typing Typing `json:"typing"`
	// Connection is the latest gateway connection state (connecting/connected/
	// failed) for the live session, "" before the first transition. It is the
	// live reload truth for a mid-session reconnect; a terminal failed session
	// reads its truth from GetSession instead.
	Connection string `json:"connection,omitempty"`
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

// SpeakerResolver resolves a Speaker Lane snowflake to a display name + GM flag
// for the live projection (#281). *speaker.Resolver satisfies it. Warm is the
// non-blocking async fill the relay calls on VADSpeechStart (~1.7s before STTFinal,
// so the name is cached by lookup time); Lookup is the cache-only read the
// synchronous STTFinal branch uses — it NEVER blocks (the bus callback must not).
// nil disables resolution, keeping the anonymous "Player / DM" lane byte-identical.
type SpeakerResolver interface {
	Warm(campaignID uuid.UUID, speakerID string)
	Lookup(campaignID uuid.UUID, speakerID string) speaker.Resolution
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
	store    LineStore       // persists projected lines (#74); nil disables persistence
	resolver SpeakerResolver // resolves SpeakerID → name/GM (#281); nil = anonymous lane
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
	connection       string // latest gateway connection state for the live session (#123); "" until the first transition
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

// SetResolver wires the speaker resolver (#281) after construction, so the many
// existing NewRelay call sites stay byte-identical (nil resolver = anonymous lane).
// Called once at boot before the relay serves any event; guarded by r.mu so it is
// safe against the subscribed bus callback.
func (r *Relay) SetResolver(res SpeakerResolver) {
	r.mu.Lock()
	r.resolver = res
	r.mu.Unlock()
}

// project is the bus callback: it attributes the event to the active session,
// rolling the buffer over on a session change, and folds the event into the
// transcript. It must not block (the bus delivers synchronously): all sends to
// live subscribers are non-blocking.
func (r *Relay) project(e voiceevent.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// ONE Snapshot serves both the id comparison and the rollover's UUID/campaign
	// capture (#149): the manager's mutex is independent of r.mu, so a second read
	// could see the session already ended and leave the FKs pointing at the
	// previous session (or uuid.Nil at process start).
	vs, active := r.sessions.Snapshot()
	if !active {
		return // no active session — drop the event (ADR-0039)
	}
	if vs.ID.String() != r.activeID {
		r.rollover(vs)
	}

	switch ev := e.(type) {
	case voiceevent.VADSpeechStart:
		// A human opened their mouth: warm the speaker resolver NOW (#281) so the
		// name is cached by the time STTFinal projects the line (~1.7s later, off the
		// bus). Warm never blocks. Then clear any stale "<NPC> is speaking…" the
		// moment speech starts (it fires BEFORE STTFinal), instead of waiting for
		// the next finalized line. Live-validated — a clean turn emits no
		// TurnEnded, so without this the last agent line keeps the label through
		// the following silence. Also correct for a barge (human over the Agent).
		if r.resolver != nil && ev.SpeakerID != "" {
			r.resolver.Warm(r.activeCampaignID, ev.SpeakerID)
		}
		r.setTyping(r.liveTyping())
	case voiceevent.STTFinal:
		// A human utterance — one line per STTFinal. who/Kind resolve from the
		// Speaker Lane's snowflake (#281): a mapped Character or guild display name
		// with the GM lane for allowlisted speakers, falling back to the
		// byte-identical anonymous "Player / DM" / KindPlayer label when the resolver
		// is off, the speaker is unattributed, or the name is unresolved.
		r.humanSeq++
		who, kind := r.resolveHuman(ev.SpeakerID)
		r.emitLine(Line{
			ID:        "u:" + strconv.FormatUint(r.humanSeq, 10),
			Who:       who,
			Kind:      kind,
			TS:        ev.At,
			Text:      ev.Text,
			SpeakerID: ev.SpeakerID, // #278: thread the Speaker Lane's snowflake through for persistence
		})
	case voiceevent.AddressRouted:
		// Records WHO answers this turn; no line yet (no text).
		t := r.turn(ev.TurnID)
		t.target = ev.Target
	case voiceevent.SpeakRequested:
		// A GM /say (#295): like AddressRouted it records WHO speaks this turn (no line
		// yet, no text). The Agent's Voice renders the text through the SAME TTSInvoked
		// path below, so the /say line is assembled and persisted exactly like an LLM
		// reply (ID "a:<turn>", NPC kind + pill) — no hand-crafted transcript row
		// (ADR-0012/0040).
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
	case voiceevent.MuteChanged:
		// One Agent's mute flipped (#211): forward a "mute" frame so the web Voice
		// panel tracks a Discord (or web) mute without a reload (AC5). It rides the
		// ring for Last-Event-ID replay; there is NO transcript-line change and NO
		// snapshot change — a mid-session reload reads the true state from GetSession.
		r.emit(Frame{Event: "mute", Data: mustJSON(muteFrame{AgentID: ev.AgentID, Muted: ev.Muted})})
	case voiceevent.SpendCapReached:
		// The session's estimated spend crossed a cap (#130): forward a "spendcap"
		// frame so the Session screen shows the spend-cap-reached state live. It rides
		// the ring for Last-Event-ID replay; there is NO transcript-line change — a
		// mid-session reload reads the authoritative state + estimate from GetSession
		// (spend_cap_state / estimated_spend_usd).
		r.emit(Frame{Event: "spendcap", Data: mustJSON(spendcapFrame{Level: string(ev.Level)})})
	case voiceevent.ConnectionStateChanged:
		// The gateway connection state moved (#123): forward a "connection" frame so
		// the Session screen flips connecting→connected, or to failed with its reason,
		// live and without a reload (AC3). It rides the ring for Last-Event-ID replay;
		// the live snapshot carries the state via View.Connection, while a terminal
		// failed session's reload truth is GetSession (status + end_reason).
		r.connection = string(ev.State)
		r.emit(Frame{Event: "connection", Data: mustJSON(connectionFrame{State: string(ev.State), Detail: ev.Detail})})
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

// rollover starts a fresh buffer for the newly-active session vs — the SAME
// snapshot the caller compared ids against (#149), so the typed FKs persistence
// needs can never belong to a different session than activeID — and seeds it
// with the initial live/listening status frame, so a client connecting
// mid-session replays a coherent state.
func (r *Relay) rollover(vs storage.VoiceSession) {
	r.activeID = vs.ID.String()
	r.activeUUID = vs.ID
	r.activeCampaignID = vs.CampaignID
	r.buf = nil
	r.lines = nil
	r.turns = map[string]*turn{}
	r.nextSeq = 0
	r.humanSeq = 0
	r.typing = r.liveTyping()
	r.connection = "" // a fresh session has no connection state until its first transition (#123)
	r.emit(Frame{Event: "status", Data: mustJSON(status{Status: "live", Typing: r.typing})})
}

// endSession emits the terminal `status: idle` frame when session id ends
// (#144). Called from Finalize — the Manager's loop-exit hook — so attached SSE
// subscribers learn the session died (self-exit included) instead of watching a
// silent stream; the frame rides the ring too, so a reconnect replays it.
func (r *Relay) endSession(id uuid.UUID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id.String() != r.activeID {
		// The relay never rolled over to this session (zero bus events) — there is
		// no buffer to close, and emitting here would inject a spurious idle frame
		// into the CURRENT session's stream.
		return
	}
	r.typing = Typing{}
	r.emit(Frame{Event: "status", Data: mustJSON(status{Status: "idle", Typing: Typing{}})})
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
		Lines:      copyLines(r.lines),
		Status:     "live",
		Typing:     r.typing,
		Connection: r.connection,
	}
}

// copyLines snapshots the ring's lines as a NON-NIL slice: the wire contract for
// View.Lines is a JSON array, never null — a zero-line live session must serve
// "lines":[] or the screen's .length read crashes (#248).
func copyLines(lines []Line) []Line {
	return append(make([]Line, 0, len(lines)), lines...)
}

// snapshot returns the initial-state View for id: the in-memory live state when id
// IS the active session, else the DB-persisted history with status "idle" (#74).
// Live behaviour is unchanged from View; the persisted path is the
// reconnect/reload history for an ended session.
func (r *Relay) snapshot(ctx context.Context, id string) View {
	r.mu.Lock()
	live := r.currentSessionID() == id && id == r.activeID
	if live {
		v := View{Lines: copyLines(r.lines), Status: "live", Typing: r.typing, Connection: r.connection}
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

// resolveHuman maps a human utterance's SpeakerID to its rendered who + Kind
// (#281). With no resolver or an empty SpeakerID it is the pre-#281 anonymous lane
// exactly ("Player / DM", KindPlayer). Otherwise a cache-only Lookup supplies the
// name (Character > guild display > "") and the GM flag: an allowlisted speaker
// lands in the KindGM lane even when unmapped (name falls back to the generic
// label), and a resolved name replaces the generic label. Caller holds r.mu.
func (r *Relay) resolveHuman(speakerID string) (string, Kind) {
	if r.resolver == nil || speakerID == "" {
		return "Player / DM", KindPlayer
	}
	res := r.resolver.Lookup(r.activeCampaignID, speakerID)
	kind := KindPlayer
	if res.GM {
		kind = KindGM
	}
	who := "Player / DM"
	if res.Name != "" {
		who = res.Name
	}
	return who, kind
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
