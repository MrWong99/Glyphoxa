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
// the window elapsing (with ≥2 utterances), or session end. An Agent utterance
// counts only DELIVERED speech (ADR-0012): a sentence enters the chunk on its
// FirstAudio (audio handed to the room), not on TTSInvoked (a dispatch attempt),
// so the undelivered tail of a barged / errored turn never reaches the transcript
// and a zero-delivered turn logs nothing. A closed chunk is
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
// from AddressRouted); pending, the sentences DISPATCHED (TTSInvoked) but not yet
// delivered, held FIFO and paired with FirstAudio by arrival order within the
// TurnID (event.go); line, the open-chunk utterance once the turn's first
// delivered sentence has opened it, which later delivered sentences append to; and
// ended (TurnEnded seen), after which a late TTSInvoked is dropped and the
// undelivered pending tail never commits (ADR-0012).
type chunkTurn struct {
	target  voiceevent.AddressTarget
	line    *chunkLine
	pending []string
	ended   bool
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
	turnIDs    []string     // Agent turns whose line lives in this chunk (detached on close)
	agents     []uuid.UUID  // participated Agent ids, first-seen order
	agentSeen  map[uuid.UUID]struct{}
	// speakers is the distinct Discord snowflakes of the humans who spoke in this
	// chunk (#278), collected EAGERLY at appendHuman — the STTFinal events are gone
	// by closeChunk. First-seen order; an empty SpeakerID (unattributed) is never
	// added. speakerSeen dedups.
	speakers    []string
	speakerSeen map[string]struct{}
	timer       *time.Timer
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
		c.bufferAgentSentence(ev)
	case voiceevent.FirstAudio:
		c.commitDelivered(ev)
	case voiceevent.TurnEnded:
		// The turn is finalized: drop its undelivered tail (ADR-0012 — the room
		// never heard those sentences) and refuse late dispatches.
		t := c.turn(ev.TurnID)
		t.ended = true
		t.pending = nil
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
	c.recordSpeaker(oc, ev.SpeakerID)
	oc.count++
	c.afterAppend(oc)
}

// recordSpeaker adds a human utterance's Speaker Lane snowflake to the chunk's
// distinct speaker set (deduped, first-seen order), collected eagerly since the
// event is gone by close (#278). An empty SpeakerID (unattributed, ADR-0050) is
// never added.
func (c *Chunker) recordSpeaker(oc *openChunk, speakerID string) {
	if speakerID == "" {
		return
	}
	if _, ok := oc.speakerSeen[speakerID]; ok {
		return
	}
	oc.speakerSeen[speakerID] = struct{}{}
	oc.speakers = append(oc.speakers, speakerID)
}

// bufferAgentSentence records one DISPATCHED sentence. TTSInvoked is a dispatch
// attempt, not delivery (ADR-0012 / event.go), so nothing enters the chunk here:
// the sentence is buffered and committed only when its FirstAudio confirms it
// reached the room. A sentence for a turn already ended (barge / tts_error) is
// dropped + logged.
//
// Dispatch is serial single-in-flight and FirstAudio(sN) is published inline
// before Dispatch(sN) returns (orchestrator.dispatchStream + wire/tee), so
// FirstAudio(sN) always precedes TTSInvoked(sN+1) on the bus. A sentence still
// pending when the NEXT dispatch arrives therefore start-errored and was never
// delivered — purge it (never commit unheard text, ADR-0012), else a mid-turn
// start-error the turn recovers from would commit the lost sentence AND shift the
// FirstAudio pairing of every later sentence by one.
func (c *Chunker) bufferAgentSentence(ev voiceevent.TTSInvoked) {
	t := c.turn(ev.TurnID)
	if t.ended {
		c.log.Warn("transcript: chunk sentence after turn ended, dropping", "turn", ev.TurnID)
		return
	}
	if n := len(t.pending); n > 0 {
		c.log.Warn("transcript: undelivered dispatched sentence superseded, dropping", "turn", ev.TurnID, "dropped", n)
		t.pending = t.pending[:0]
	}
	t.pending = append(t.pending, ev.Sentence)
}

// commitDelivered commits the next dispatched sentence now that its FirstAudio
// confirms it was delivered (ADR-0012: transcripts reflect what listeners actually
// heard). FirstAudio pairs FIFO with TTSInvoked by arrival order within the TurnID
// (event.go), so it pops pending's head. The first delivered sentence OPENS the
// turn's utterance (one utterance per Agent turn, records the Agent, bumps the
// count); later delivered sentences append to that same line. A FirstAudio with
// nothing pending — a straggler after TurnEnded cleared the buffer — is a no-op.
func (c *Chunker) commitDelivered(ev voiceevent.FirstAudio) {
	t := c.turn(ev.TurnID)
	if len(t.pending) == 0 {
		c.log.Debug("transcript: first audio with no pending sentence, ignoring", "turn", ev.TurnID)
		return
	}
	s := t.pending[0]
	t.pending = t.pending[1:]
	if t.line == nil {
		// First delivered sentence of this turn's current utterance: open it. After
		// a chunk close detached the line, this re-opens a CONTINUATION in the next
		// chunk (started_at = this delivery's time, Agent in the new chunk's set).
		oc := c.ensureOpen(ev.At)
		t.line = &chunkLine{text: nameOr(t.target.Name, "NPC") + ": " + s}
		oc.entries = append(oc.entries, t.line)
		oc.turnIDs = append(oc.turnIDs, ev.TurnID)
		c.recordAgent(oc, t.target)
		oc.count++
		c.afterAppend(oc)
		return
	}
	if s == "" {
		return
	}
	if t.line.text != "" {
		t.line.text += " "
	}
	t.line.text += s
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
			startedAt:   at,
			openedWall:  time.Now(),
			agentSeen:   map[uuid.UUID]struct{}{},
			speakerSeen: map[string]struct{}{},
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

// closeChunk finalizes oc: it stops the timer, clears it as the open chunk,
// DETACHES its Agent turns (line = nil) so a not-ended turn's next delivered
// sentence opens a continuation utterance in the next chunk, and tees the built
// chunk onto the writer queue. A count-0 chunk (a chunk only opens on a delivered
// or human utterance, so this is defensive) is dropped without a write. Caller
// holds c.mu.
func (c *Chunker) closeChunk(oc *openChunk) {
	if oc.timer != nil {
		oc.timer.Stop()
	}
	if c.open == oc {
		c.open = nil
	}
	for _, id := range oc.turnIDs {
		if t := c.turns[id]; t != nil {
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
	speakers := oc.speakers
	if speakers == nil {
		speakers = []string{} // scan contract: non-nil empty when no attributed speaker
	}
	chunk := storage.TranscriptChunk{
		CampaignID:            c.activeCampaignID,
		VoiceSessionID:        c.activeUUID,
		Content:               strings.Join(texts, "\n"),
		SpeakerDiscordUserIDs: speakers,
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
