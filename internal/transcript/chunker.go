package transcript

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/busproject"
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
// Concurrency mirrors the relay (#74) through the SAME busproject scaffold
// (#447): the bus delivers project SYNCHRONOUSLY and must not block, so a
// closed chunk is teed onto a bounded queue drained by ONE writer goroutine,
// and an overflow drops + logs rather than ever calling the DB inline.
// FlushSession (the Manager's loop-exit hook) closes the open chunk and rides
// a flush barrier through the SAME queue, so FIFO guarantees every prior
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
	// lastCommitted is the sentence most recently committed to line by a
	// FirstAudio, kept so a TTSStreamFailed (#436) can retract exactly that
	// sentence — the stream died mid-delivery, so the room heard at most a
	// fragment and the chunk must not record the full text (ADR-0012 parity with
	// Agent history, which omits it). Cleared once retracted or superseded.
	lastCommitted string
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
// the same lock — c.mu, shared with the busproject scaffold that owns the
// subscription, the session rollover, and the turn map (#447).
type Chunker struct {
	store  ChunkStore
	gauge  BacklogGauge
	log    *slog.Logger
	window time.Duration
	maxUtt int

	resolver SpeakerResolver // resolves SpeakerID → name for the line prefix (#281); nil = generic label

	// proj is the shared projection scaffold (#447): it attributes every bus
	// event to the active session under c.mu (ONE Snapshot per event, like the
	// relay, #149), flushes a stale open chunk via finishSession BEFORE the FKs
	// repoint on a session change, and owns the per-turn coalescing map. The
	// chunker keeps only its fold rules (delivered-only, ADR-0012).
	proj *busproject.Projection[chunkTurn]

	mu   sync.Mutex
	open *openChunk

	// queue is the non-blocking write queue draining into the single writer
	// goroutine; nil when persistence is disabled (store == nil).
	queue *busproject.Queue[*storage.TranscriptChunk]
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
		store:  store,
		gauge:  gauge,
		log:    log,
		window: window,
		maxUtt: maxUtt,
	}
	if store != nil {
		c.queue = busproject.NewQueue(chunkQueue, c.writeChunk)
	}
	c.proj = busproject.New[chunkTurn](sessions, &c.mu, busproject.Hooks{
		Fold:          c.fold,
		FinishSession: c.finishSession,
		StartSession:  c.startSession,
	})
	c.proj.Subscribe(bus)
	return c
}

// SetResolver wires the speaker resolver (#281) after construction, so existing
// NewChunker call sites stay byte-identical (nil resolver = generic prefix). Called
// once at boot before the chunker folds any event; guarded by c.mu so it is safe
// against the subscribed bus callback.
func (c *Chunker) SetResolver(res SpeakerResolver) {
	c.mu.Lock()
	c.resolver = res
	c.mu.Unlock()
}

// fold applies one attributed event to the open chunk — the chunker's fold
// rules (ADR-0012: delivered-only, commit at FirstAudio). The scaffold has
// already taken the event's ONE session Snapshot (like the relay, #149) and
// rolled over on a session change; fold runs under c.mu and must not block
// (the bus delivers synchronously): the only outward call, closeChunk's
// enqueue, is non-blocking.
func (c *Chunker) fold(e voiceevent.Event) {
	switch ev := e.(type) {
	case voiceevent.STTFinal:
		c.appendHuman(ev)
	case voiceevent.AddressRouted:
		c.proj.Turn(ev.TurnID).target = ev.Target
	case voiceevent.TTSInvoked:
		c.bufferAgentSentence(ev)
	case voiceevent.FirstAudio:
		c.commitDelivered(ev)
	case voiceevent.TTSStreamFailed:
		c.abortSentence(ev)
	case voiceevent.TurnEnded:
		// The turn is finalized: drop its undelivered tail (ADR-0012 — the room
		// never heard those sentences) and refuse late dispatches.
		t := c.proj.Turn(ev.TurnID)
		t.ended = true
		t.pending = nil
	}
}

// finishSession is the scaffold's pre-rollover hook: any stale open chunk from
// the outgoing session is enqueue-flushed as a safety net (a session that ended
// without a FlushSession still persists its last chunk). It runs BEFORE the
// scaffold repoints the active FKs, so the flushed chunk keeps the OLD
// session's ids.
func (c *Chunker) finishSession() {
	if c.open != nil {
		c.closeChunk(c.open)
	}
}

// startSession is the scaffold's post-rollover hook: the scaffold has repointed
// the FKs and reset the turn map; clearing open here is defensive (finishSession's
// closeChunk already detached it).
func (c *Chunker) startSession() {
	c.open = nil
}

// appendHuman folds one human utterance (one STTFinal, attributed to its Speaker
// Lane via SpeakerID since #278/ADR-0050) into the open chunk and re-checks the
// close conditions. The line prefix resolves to the speaker's Character/guild name
// when available (#281), else the generic "Player / DM".
func (c *Chunker) appendHuman(ev voiceevent.STTFinal) {
	oc := c.ensureOpen(ev.At)
	oc.entries = append(oc.entries, &chunkLine{text: c.humanPrefix(ev.SpeakerID) + ": " + ev.Text})
	c.recordSpeaker(oc, ev.SpeakerID)
	oc.count++
	c.afterAppend(oc)
}

// humanPrefix is the "Who" prefix for a human chunk line (#281): the resolved
// Character (or guild) name when available, else the generic "Player / DM". It is
// cache-only (Lookup never blocks) and applies to NEW chunk content only —
// persisted chunks are immutable (ADR-0011). The relay warms the resolver on
// VADSpeechStart, so this shared cache is usually populated by the time a line
// folds. GM lane routing is a live-view concern; chunk content just uses names.
func (c *Chunker) humanPrefix(speakerID string) string {
	if c.resolver == nil || speakerID == "" {
		return "Player / DM"
	}
	if name := c.resolver.Lookup(c.proj.Session().CampaignID, speakerID).Name; name != "" {
		return name
	}
	return "Player / DM"
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
	t := c.proj.Turn(ev.TurnID)
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
	t := c.proj.Turn(ev.TurnID)
	if len(t.pending) == 0 {
		c.log.Debug("transcript: first audio with no pending sentence, ignoring", "turn", ev.TurnID)
		return
	}
	s := t.pending[0]
	t.pending = t.pending[1:]
	t.lastCommitted = s
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

// abortSentence handles a [voiceevent.TTSStreamFailed] (#436): the turn's
// in-flight sentence died mid-delivery, so the room heard at most a fragment and
// the chunk must NOT record its text — parity with Agent history, which omits it
// (ADR-0012's never-record-unheard bias). Two cases, disambiguated by the FIFO
// pairing state:
//
//   - The stream died BEFORE its first real chunk: no FirstAudio fired, so the
//     sentence still heads pending — drop it (it must never commit off a
//     straggler FirstAudio).
//   - FirstAudio fired and committed the sentence: retract exactly that trailing
//     sentence from the turn's line. A sentence that OPENED the line leaves it
//     with no delivered text at all, so the whole line is removed from the open
//     chunk (a later delivered sentence re-opens one). A line already detached
//     by a chunk close was persisted before the failure surfaced — logged and
//     accepted (the close raced the failure; rare by construction since the
//     dispatch is serial single-in-flight).
func (c *Chunker) abortSentence(ev voiceevent.TTSStreamFailed) {
	t := c.proj.Turn(ev.TurnID)
	if t.ended {
		return
	}
	if len(t.pending) > 0 {
		t.pending = t.pending[1:]
		return
	}
	s := t.lastCommitted
	t.lastCommitted = ""
	if s == "" {
		return // nothing committed for this turn yet — nothing to retract
	}
	if t.line == nil {
		c.log.Warn("transcript: mid-stream TTS failure after chunk close; truncated sentence already persisted",
			"turn", ev.TurnID)
		return
	}
	switch {
	case strings.HasSuffix(t.line.text, " "+s):
		// A later sentence of a multi-sentence line: trim just the failed tail.
		t.line.text = strings.TrimSuffix(t.line.text, " "+s)
	case strings.HasSuffix(t.line.text, ": "+s):
		// The failed sentence opened the line — no delivered text remains, so the
		// line leaves the open chunk entirely and the turn re-opens on a later
		// delivered sentence.
		c.removeOpenLine(t.line)
		t.line = nil
	default:
		c.log.Warn("transcript: mid-stream TTS failure did not match the committed tail, leaving line unchanged",
			"turn", ev.TurnID)
	}
}

// removeOpenLine deletes line from the open chunk's entries (pointer identity)
// and reverses its count contribution, so a retracted opening sentence (#436)
// neither persists an empty "Who:" line nor inflates the close thresholds. A
// line not in the open chunk (already detached by a close) is a no-op — the
// caller logs that case.
func (c *Chunker) removeOpenLine(line *chunkLine) {
	oc := c.open
	if oc == nil {
		return
	}
	for i, e := range oc.entries {
		if e == line {
			oc.entries = append(oc.entries[:i], oc.entries[i+1:]...)
			oc.count--
			return
		}
	}
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
		if t := c.proj.Lookup(id); t != nil {
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
	sess := c.proj.Session()
	chunk := storage.TranscriptChunk{
		CampaignID:            sess.CampaignID,
		VoiceSessionID:        sess.ID,
		Content:               strings.Join(texts, "\n"),
		SpeakerDiscordUserIDs: speakers,
		ParticipatedAgentIDs:  oc.agents,
		StartedAt:             oc.startedAt,
	}
	// The tee is non-blocking (the bus must not block), so an overflow drops +
	// logs; a nil queue (persistence disabled) accepts silently.
	if !c.queue.Enqueue(&chunk) {
		c.log.Warn("transcript: chunk queue full, dropping chunk")
	}
}

// writeChunk is the Chunker's flush sink — the write half of the single writer
// goroutine: one serial, bounded Insert+Count per closed chunk, refreshing the
// backlog gauge (Set-from-COUNT, ADR-0032). An insert/count failure is logged
// but does not stop the loop — durability is best-effort.
func (c *Chunker) writeChunk(chunk *storage.TranscriptChunk) {
	ctx, cancel := context.WithTimeout(context.Background(), chunkWriteTimeout)
	defer cancel()
	if _, err := c.store.InsertTranscriptChunk(ctx, *chunk); err != nil {
		c.log.Warn("transcript: insert chunk", "err", err)
		return
	}
	n, err := c.store.CountUnembeddedChunks(ctx)
	if err != nil {
		c.log.Warn("transcript: count unembedded chunks", "err", err)
		return
	}
	if c.gauge != nil {
		c.gauge.SetEmbeddingBacklog(n)
	}
}

// FlushSession closes the session's open chunk (even a lone utterance — the ONLY
// place ADR-0011 flushes one) and drains the writer queue via a flush barrier,
// returning once every enqueued insert has landed (#104). The Manager calls it at
// every loop exit. It satisfies session.ChunkFinalizer. Persistence disabled (no
// store) or a mismatched session id is a no-op.
func (c *Chunker) FlushSession(ctx context.Context, sessionID uuid.UUID) error {
	c.mu.Lock()
	if c.open != nil && c.proj.Session().ID == sessionID {
		c.closeChunk(c.open)
	}
	c.mu.Unlock()

	return c.queue.Flush(ctx, nil)
}
