package transcript

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// Transcript chunk writer (#104, ADR-0011). The chunker subscribes to the process
// voiceevent.Bus, folds the pipeline's events into a per-session open chunk of
// 3–6 utterances, and closes the chunk on whichever-first of five utterances,
// the window elapsing (with ≥2 utterances), or session end. A closed chunk is
// written with embedding NULL — the async embedding pipeline fills it later — and
// the NULL-embedding backlog gauge is refreshed from the store's COUNT after each
// write. This CHUNK grain (retrieval/Hot Context) is distinct from the per-line
// transcript_line grain (ADR-0040): the two are independent records of the same
// speech.
//
// Concurrency mirrors the relay (#74): the bus delivers project SYNCHRONOUSLY and
// must not block, so a closed chunk is teed onto a bounded queue drained by ONE
// writer goroutine, and an overflow drops + logs rather than ever calling the DB
// inline. FlushSession (the Manager's loop-exit hook) closes the open chunk and
// rides a flush barrier through the SAME queue, so FIFO guarantees every prior
// insert has landed when it returns.

const (
	// chunkQueue bounds the writer's backlog. Closes past this are dropped (logged)
	// rather than blocking the bus; a healthy DB drains far faster than a session
	// closes chunks.
	chunkQueue = 64
	// chunkWriteTimeout bounds one Insert+Count so a stalled DB can't wedge the
	// single writer goroutine indefinitely.
	chunkWriteTimeout = 5 * time.Second

	// defaultWindow / defaultMaxUtterances are the chunk-close thresholds (ADR-0011):
	// close at five utterances or sixty seconds, whichever first.
	defaultWindow        = 60 * time.Second
	defaultMaxUtterances = 5
)

// ChunkStore is the narrow persistence surface the chunker needs (#104):
// insert-with-NULL-embedding and the global NULL-embedding COUNT for the gauge.
// *storage.Store satisfies it; tests fake it.
type ChunkStore interface {
	InsertTranscriptChunk(ctx context.Context, c storage.TranscriptChunk) (uuid.UUID, error)
	CountUnembeddedChunks(ctx context.Context) (int, error)
}

// BacklogGauge receives the current NULL-embedding backlog after each write
// (Set-from-COUNT, never Inc/Dec — ADR-0032). *observe.PrometheusRecorder
// satisfies it; nil disables the gauge update.
type BacklogGauge interface {
	SetEmbeddingBacklog(n int)
}

// ChunkerConfig tunes the chunk-close thresholds. Zero values fall back to the
// ADR-0011 defaults (60s window, 5 utterances); tests shrink the window.
type ChunkerConfig struct {
	Window        time.Duration
	MaxUtterances int
}

// chunkTurn is the per-turn coalescing state: the routed target (name + agent id
// from AddressRouted) and, once the turn's first TTSInvoked opens its utterance,
// the open-chunk line whose text later sentences append to. closed marks a turn
// whose line was flushed into a now-closed chunk, so a late sentence is dropped
// (mirrors the relay's post-TurnEnded drop).
type chunkTurn struct {
	target voiceevent.AddressTarget
	line   *chunkLine
	closed bool
}

// chunkLine is one rendered utterance line ("Who: text") in an open chunk. It is a
// pointer so an Agent turn's later sentences append to the same line in place.
type chunkLine struct {
	text string
}

// openChunk accumulates the current session's utterances until a close condition
// fires.
type openChunk struct {
	startedAt  time.Time    // first utterance's event time — the row's started_at
	openedWall time.Time    // wall clock at open — the window-elapsed reference
	count      int          // utterance count (human STTFinal, or one per Agent turn)
	entries    []*chunkLine // rendered lines, in order
	turnIDs    []string     // Agent turns whose line lives in this chunk (for close-drop)
	agents     []uuid.UUID  // participated Agent ids, first-seen order
	agentSeen  map[uuid.UUID]struct{}
	timer      *time.Timer
}

// Chunker projects bus events into Transcript Chunks and writes them async. Safe
// for concurrent use: the bus callback, the window timer and FlushSession all take
// the same lock.
type Chunker struct {
	sessions Sessions
	store    ChunkStore
	gauge    BacklogGauge
	log      *slog.Logger
	window   time.Duration
	maxUtt   int

	mu               sync.Mutex
	activeID         string // current session id; "" at process start
	activeUUID       uuid.UUID
	activeCampaignID uuid.UUID
	open             *openChunk
	turns            map[string]*chunkTurn // per-session, reset on rollover

	// writeCh is the non-blocking queue draining into the single writer goroutine;
	// nil when persistence is disabled (store == nil).
	writeCh chan chunkOp
}

// chunkOp is one item on the writer queue: a chunk to insert, or a flush barrier.
type chunkOp struct {
	chunk *storage.TranscriptChunk
	flush *chunkFlush
}

// chunkFlush is the FlushSession barrier: the writer replies on result once every
// insert enqueued before it has landed (FIFO).
type chunkFlush struct {
	result chan error
}

// NewChunker subscribes to the bus once and returns a Chunker ready to fold
// events. The subscription lives for the process (single active session across
// reconnects/sessions, ADR-0039). A nil store disables writes (no writer
// goroutine); a nil gauge disables the backlog update.
func NewChunker(bus *voiceevent.Bus, sessions Sessions, store ChunkStore, gauge BacklogGauge, log *slog.Logger, cfg ChunkerConfig) *Chunker {
	if log == nil {
		log = slog.Default()
	}
	window := cfg.Window
	if window <= 0 {
		window = defaultWindow
	}
	maxUtt := cfg.MaxUtterances
	if maxUtt <= 0 {
		maxUtt = defaultMaxUtterances
	}
	c := &Chunker{
		sessions: sessions,
		store:    store,
		gauge:    gauge,
		log:      log,
		window:   window,
		maxUtt:   maxUtt,
		turns:    map[string]*chunkTurn{},
	}
	if store != nil {
		c.writeCh = make(chan chunkOp, chunkQueue)
		go c.writeLoop()
	}
	if bus != nil {
		bus.Subscribe(c.project)
	}
	return c
}

// project is the bus callback: it attributes the event to the active session
// (rolling over on a session change, one Snapshot per event like the relay) and
// folds it into the open chunk. It must not block (the bus delivers
// synchronously): the only outward call, closeChunk's enqueue, is non-blocking.
func (c *Chunker) project(e voiceevent.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()

	vs, active := c.sessions.Snapshot()
	if !active {
		return // no active session — drop the event (ADR-0039)
	}
	if vs.ID.String() != c.activeID {
		c.rollover(vs)
	}

	switch ev := e.(type) {
	case voiceevent.STTFinal:
		c.appendHuman(ev)
	case voiceevent.AddressRouted:
		c.turn(ev.TurnID).target = ev.Target
	case voiceevent.TTSInvoked:
		c.appendAgentSentence(ev)
	}
}

// rollover switches to the newly-active session vs. Any stale open chunk from the
// previous session is enqueue-flushed as a safety net (a session that ended
// without a FlushSession still persists its last chunk), BEFORE the active FKs are
// repointed so the flushed chunk keeps the OLD session's ids.
func (c *Chunker) rollover(vs storage.VoiceSession) {
	if c.open != nil {
		c.closeChunk(c.open)
	}
	c.activeID = vs.ID.String()
	c.activeUUID = vs.ID
	c.activeCampaignID = vs.CampaignID
	c.open = nil
	c.turns = map[string]*chunkTurn{}
}

// appendHuman folds one human utterance (one STTFinal, anonymous lane ADR-0039)
// into the open chunk and re-checks the close conditions.
func (c *Chunker) appendHuman(ev voiceevent.STTFinal) {
	oc := c.ensureOpen(ev.At)
	oc.entries = append(oc.entries, &chunkLine{text: "Player / DM: " + ev.Text})
	oc.count++
	c.afterAppend(oc)
}

// appendAgentSentence folds one TTS sentence into its turn's utterance: the first
// sentence of a turn opens a new utterance line (and records the Agent), later
// sentences append to that same line without bumping the utterance count. A
// sentence for a turn already flushed into a closed chunk is dropped + logged.
func (c *Chunker) appendAgentSentence(ev voiceevent.TTSInvoked) {
	t := c.turn(ev.TurnID)
	if t.closed {
		c.log.Warn("transcript: chunk sentence for a closed chunk, dropping", "turn", ev.TurnID)
		return
	}
	if t.line == nil {
		oc := c.ensureOpen(ev.At)
		t.line = &chunkLine{text: nameOr(t.target.Name, "NPC") + ": " + ev.Sentence}
		oc.entries = append(oc.entries, t.line)
		oc.turnIDs = append(oc.turnIDs, ev.TurnID)
		c.recordAgent(oc, t.target)
		oc.count++
		c.afterAppend(oc)
		return
	}
	if ev.Sentence == "" {
		return
	}
	if t.line.text != "" {
		t.line.text += " "
	}
	t.line.text += ev.Sentence
}

// recordAgent adds the turn's Agent to the chunk's participated set (deduped,
// first-seen order). AddressTarget.AgentID IS the DB UUID for a Character NPC
// (wirenpc/agentspec.go); the well-known "butler" route and any other non-UUID id
// is logged and skipped — a chunk with only such replies is left with no
// participants, which is the correct hard-filter behaviour for NPC-knowledge
// retrieval (ADR-0011).
func (c *Chunker) recordAgent(oc *openChunk, target voiceevent.AddressTarget) {
	id, err := uuid.Parse(target.AgentID)
	if err != nil {
		c.log.Warn("transcript: unparsable agent id on chunk turn, skipping participation", "agent_id", target.AgentID)
		return
	}
	if _, ok := oc.agentSeen[id]; ok {
		return
	}
	oc.agentSeen[id] = struct{}{}
	oc.agents = append(oc.agents, id)
}

// ensureOpen returns the open chunk, creating one (armed with the window timer)
// on first utterance. at is the utterance's event time, kept as the row's
// started_at; the wall clock at open is the window-elapsed reference.
func (c *Chunker) ensureOpen(at time.Time) *openChunk {
	if c.open == nil {
		oc := &openChunk{
			startedAt:  at,
			openedWall: time.Now(),
			agentSeen:  map[uuid.UUID]struct{}{},
		}
		oc.timer = time.AfterFunc(c.window, func() { c.onTimer(oc) })
		c.open = oc
	}
	return c.open
}

// afterAppend closes the open chunk when a count threshold is met: at MaxUtterances
// unconditionally, or once the window has elapsed with ≥2 utterances (a late
// utterance arriving after the timer already fired closes here immediately).
func (c *Chunker) afterAppend(oc *openChunk) {
	if oc.count >= c.maxUtt {
		c.closeChunk(oc)
		return
	}
	if oc.count >= 2 && time.Since(oc.openedWall) >= c.window {
		c.closeChunk(oc)
	}
}

// onTimer fires when the window elapses. With ≥2 utterances it closes the chunk;
// with a lone utterance it leaves the chunk open (the ONLY flush of a lone
// utterance is session end), so the next append re-checks and closes immediately.
func (c *Chunker) onTimer(oc *openChunk) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.open != oc {
		return // already closed / superseded
	}
	if oc.count >= 2 {
		c.closeChunk(oc)
	}
}

// closeChunk finalizes oc: it stops the timer, clears it as the open chunk, marks
// its Agent turns closed (so a late sentence drops), and tees the built chunk onto
// the writer queue. Idempotent-safe: a count-0 chunk (never happens in practice,
// as a chunk only opens on an utterance) is dropped without a write. Caller holds
// c.mu.
func (c *Chunker) closeChunk(oc *openChunk) {
	if oc.timer != nil {
		oc.timer.Stop()
	}
	if c.open == oc {
		c.open = nil
	}
	for _, id := range oc.turnIDs {
		if t := c.turns[id]; t != nil {
			t.closed = true
			t.line = nil
		}
	}
	if oc.count == 0 {
		return
	}
	texts := make([]string, len(oc.entries))
	for i, e := range oc.entries {
		texts[i] = e.text
	}
	chunk := storage.TranscriptChunk{
		CampaignID:            c.activeCampaignID,
		VoiceSessionID:        c.activeUUID,
		Content:               strings.Join(texts, "\n"),
		SpeakerDiscordUserIDs: []string{},
		ParticipatedAgentIDs:  oc.agents,
		StartedAt:             oc.startedAt,
	}
	c.enqueue(chunkOp{chunk: &chunk})
}

// enqueue tees an op onto the writer queue with a non-blocking send — the bus must
// not block, so an overflow drops + logs. No-op when persistence is disabled.
func (c *Chunker) enqueue(op chunkOp) {
	if c.writeCh == nil {
		return
	}
	select {
	case c.writeCh <- op:
	default:
		c.log.Warn("transcript: chunk queue full, dropping chunk")
	}
}

// turn returns the coalescing state for a turn id, creating it on first sight.
func (c *Chunker) turn(id string) *chunkTurn {
	t := c.turns[id]
	if t == nil {
		t = &chunkTurn{}
		c.turns[id] = t
	}
	return t
}

// writeLoop is the single writer goroutine: it serially inserts closed chunks and
// refreshes the backlog gauge, and services flush barriers. An insert/count
// failure is logged but does not stop the loop — durability is best-effort.
func (c *Chunker) writeLoop() {
	for op := range c.writeCh {
		switch {
		case op.chunk != nil:
			ctx, cancel := context.WithTimeout(context.Background(), chunkWriteTimeout)
			if _, err := c.store.InsertTranscriptChunk(ctx, *op.chunk); err != nil {
				c.log.Warn("transcript: insert chunk", "err", err)
				cancel()
				continue
			}
			n, err := c.store.CountUnembeddedChunks(ctx)
			cancel()
			if err != nil {
				c.log.Warn("transcript: count unembedded chunks", "err", err)
				continue
			}
			if c.gauge != nil {
				c.gauge.SetEmbeddingBacklog(n)
			}
		case op.flush != nil:
			op.flush.result <- nil // FIFO: every prior insert has landed
		}
	}
}

// FlushSession closes the session's open chunk (even a lone utterance — the ONLY
// place ADR-0011 flushes one) and drains the writer queue via a flush barrier,
// returning once every enqueued insert has landed (#104). The Manager calls it at
// every loop exit. It satisfies session.ChunkFinalizer. Persistence disabled (no
// store) or a mismatched session id is a no-op.
func (c *Chunker) FlushSession(ctx context.Context, sessionID uuid.UUID) error {
	c.mu.Lock()
	if c.open != nil && c.activeUUID == sessionID {
		c.closeChunk(c.open)
	}
	ch := c.writeCh
	c.mu.Unlock()

	if ch == nil {
		return nil
	}
	res := make(chan error, 1)
	select {
	case ch <- chunkOp{flush: &chunkFlush{result: res}}:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-res:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
