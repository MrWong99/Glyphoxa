package transcript

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// fakeChunkStore is an in-memory ChunkStore: it records the chunks it was asked
// to insert and models the NULL-embedding backlog as "one more per insert" — the
// real CountUnembeddedChunks over rows that all land embedding=NULL (#104).
type fakeChunkStore struct {
	mu         sync.Mutex
	chunks     []storage.TranscriptChunk
	countCalls int
}

func (f *fakeChunkStore) InsertTranscriptChunk(_ context.Context, c storage.TranscriptChunk) (uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chunks = append(f.chunks, c)
	return uuid.New(), nil
}

func (f *fakeChunkStore) CountUnembeddedChunks(_ context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.countCalls++
	return len(f.chunks), nil // every insert is a NULL-embedding row
}

func (f *fakeChunkStore) all() []storage.TranscriptChunk {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]storage.TranscriptChunk(nil), f.chunks...)
}

func (f *fakeChunkStore) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.chunks)
}

// fakeGauge records every SetEmbeddingBacklog call so the "Set-from-COUNT" path
// is observable.
type fakeGauge struct {
	mu   sync.Mutex
	sets []int
}

func (g *fakeGauge) SetEmbeddingBacklog(n int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sets = append(g.sets, n)
}

func (g *fakeGauge) snapshot() []int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]int(nil), g.sets...)
}

// capHandler records log messages so a drop-on-overflow is assertable.
type capHandler struct {
	mu   sync.Mutex
	msgs []string
}

func (c *capHandler) Enabled(context.Context, slog.Level) bool { return true }
func (c *capHandler) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, r.Message)
	return nil
}
func (c *capHandler) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *capHandler) WithGroup(string) slog.Handler      { return c }
func (c *capHandler) has(sub string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, m := range c.msgs {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}

func newChunker(t *testing.T, store ChunkStore, gauge BacklogGauge, cfg ChunkerConfig) (*voiceevent.Bus, *fakeSessions, *Chunker) {
	t.Helper()
	bus := voiceevent.NewBus()
	fs := &fakeSessions{id: uuid.New(), active: true}
	c := NewChunker(fwd(t, bus, fs), fs, store, gauge, slog.New(slog.DiscardHandler), cfg)
	return bus, fs, c
}

// liveChunker wires a chunker to a bus with one active session that carries a
// campaign id, so a test can assert campaign_id / voice_session_id on the row.
// The returned bus is the session bus tests publish on; events are stamped with
// vs.ID onto the process bus the chunker subscribes to (#487).
func liveChunker(t *testing.T, store ChunkStore, gauge BacklogGauge, cfg ChunkerConfig, vs storage.VoiceSession) (*voiceevent.Bus, *Chunker) {
	t.Helper()
	bus := voiceevent.NewBus()
	fs := &fakeSessions{id: vs.ID, campaign: vs.CampaignID, active: true}
	c := NewChunker(fwd(t, bus, fs), fs, store, gauge, slog.New(slog.DiscardHandler), cfg)
	return bus, c
}

// agentReply is the event sequence for one DELIVERED Agent sentence: dispatch
// (TTSInvoked) immediately followed by delivery (FirstAudio). The chunk grain
// commits only on delivery (ADR-0012).
func agentReply(bus *voiceevent.Bus, turnID, sentence string, at time.Time) {
	bus.Publish(voiceevent.TTSInvoked{At: at, Sentence: sentence, TurnID: turnID})
	bus.Publish(voiceevent.FirstAudio{At: at, TurnID: turnID})
}

// eventually polls fn until it returns true or the deadline passes.
func eventually(t *testing.T, within time.Duration, fn func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return fn()
}

// TestChunker_FiveUtterancesOneInsert is #104 rule CLOSE: a chunk closes at the
// fifth utterance into ONE insert whose content is the five lines in order — and
// the write happens on the auto-close path WITHOUT any FlushSession draining it.
// It also pins started_at = the first utterance's event time and the row's
// campaign/session FKs.
func TestChunker_FiveUtterancesOneInsert(t *testing.T) {
	store := &fakeChunkStore{}
	vs := storage.VoiceSession{ID: uuid.New(), CampaignID: uuid.New()}
	bus, _ := liveChunker(t, store, nil, ChunkerConfig{}, vs)

	for i := 1; i <= 5; i++ {
		bus.Publish(voiceevent.STTFinal{At: at(i), Text: "line" + string(rune('0'+i)), TurnID: "t"})
	}

	// The fifth utterance auto-closes the chunk and the async writer inserts it —
	// no FlushSession barrier involved.
	if !eventually(t, 2*time.Second, func() bool { return store.count() == 1 }) {
		t.Fatalf("five utterances did not auto-close into one insert: inserts = %d", store.count())
	}
	got := store.all()
	want := "Player / DM: line1\nPlayer / DM: line2\nPlayer / DM: line3\nPlayer / DM: line4\nPlayer / DM: line5"
	if got[0].Content != want {
		t.Errorf("content = %q,\nwant %q", got[0].Content, want)
	}
	if len(got[0].ParticipatedAgentIDs) != 0 {
		t.Errorf("participated = %v, want empty (human-only chunk)", got[0].ParticipatedAgentIDs)
	}
	if len(got[0].SpeakerDiscordUserIDs) != 0 {
		t.Errorf("speakers = %v, want empty (anonymous STT lane)", got[0].SpeakerDiscordUserIDs)
	}
	if !got[0].StartedAt.Equal(at(1)) {
		t.Errorf("started_at = %s, want the first utterance's event time %s", got[0].StartedAt, at(1))
	}
	if got[0].CampaignID != vs.CampaignID || got[0].VoiceSessionID != vs.ID {
		t.Errorf("row FKs = campaign %s / session %s, want %s / %s",
			got[0].CampaignID, got[0].VoiceSessionID, vs.CampaignID, vs.ID)
	}
}

// TestChunker_WindowClosesWithTwo is #104 rule CLOSE: with two utterances the
// window timer closes the chunk on its own (no fifth utterance, no session end).
func TestChunker_WindowClosesWithTwo(t *testing.T) {
	store := &fakeChunkStore{}
	bus, _, _ := newChunker(t, store, nil, ChunkerConfig{Window: 40 * time.Millisecond})

	bus.Publish(voiceevent.STTFinal{At: at(1), Text: "one", TurnID: "t"})
	bus.Publish(voiceevent.STTFinal{At: at(2), Text: "two", TurnID: "t"})

	if !eventually(t, 2*time.Second, func() bool { return store.count() == 1 }) {
		t.Fatalf("window did not close the 2-utterance chunk: inserts = %d", store.count())
	}
	if lines := strings.Count(store.all()[0].Content, "\n") + 1; lines != 2 {
		t.Errorf("closed chunk has %d lines, want 2", lines)
	}
}

// TestChunker_WindowKeepsLoneUtterance is #104 rule CLOSE + ADR-0011: a single
// utterance is NOT closed by the window (timer fire with count==1 keeps it open);
// it is flushed only at session end (FlushSession).
func TestChunker_WindowKeepsLoneUtterance(t *testing.T) {
	store := &fakeChunkStore{}
	bus, fs, c := newChunker(t, store, nil, ChunkerConfig{Window: 40 * time.Millisecond})

	bus.Publish(voiceevent.STTFinal{At: at(1), Text: "alone", TurnID: "t"})

	// Well past the window: the lone utterance must still be open (0 inserts).
	time.Sleep(150 * time.Millisecond)
	if n := store.count(); n != 0 {
		t.Fatalf("lone utterance closed by window: inserts = %d, want 0", n)
	}

	// Session end is the ONLY way a lone utterance flushes.
	if err := c.FlushSession(context.Background(), fs.id); err != nil {
		t.Fatalf("FlushSession: %v", err)
	}
	if n := store.count(); n != 1 {
		t.Fatalf("session end did not flush the lone utterance: inserts = %d, want 1", n)
	}
	if store.all()[0].Content != "Player / DM: alone" {
		t.Errorf("content = %q", store.all()[0].Content)
	}
}

// TestChunker_SecondUtteranceAfterWindowClosesImmediately is #104 rule CLOSE: a
// timer fire with count==1 leaves the chunk open; the next utterance re-checks
// elapsed and closes it immediately.
func TestChunker_SecondUtteranceAfterWindowClosesImmediately(t *testing.T) {
	store := &fakeChunkStore{}
	bus, _, _ := newChunker(t, store, nil, ChunkerConfig{Window: 40 * time.Millisecond})

	bus.Publish(voiceevent.STTFinal{At: at(1), Text: "one", TurnID: "t"})
	time.Sleep(120 * time.Millisecond) // timer fired with count==1, chunk stayed open
	if n := store.count(); n != 0 {
		t.Fatalf("chunk closed with a lone utterance: inserts = %d", n)
	}
	bus.Publish(voiceevent.STTFinal{At: at(2), Text: "two", TurnID: "t"})

	if !eventually(t, 2*time.Second, func() bool { return store.count() == 1 }) {
		t.Fatalf("second utterance past the window did not close immediately: inserts = %d", store.count())
	}
}

// TestChunker_AgentReplyCoalesces is #104 rule UTTERANCES: an Agent turn is ONE
// utterance no matter how many DELIVERED sentences it spans (each delivered on its
// FirstAudio, appended to one line), and participated_agent_ids carries exactly
// that Agent's DB UUID.
func TestChunker_AgentReplyCoalesces(t *testing.T) {
	store := &fakeChunkStore{}
	bus, fs, c := newChunker(t, store, nil, ChunkerConfig{})
	agentID := uuid.New()

	bus.Publish(voiceevent.STTFinal{At: at(1), Text: "Hello Bart", TurnID: "t1"})
	bus.Publish(voiceevent.AddressRouted{
		At: at(2), TurnID: "t1",
		Target: voiceevent.AddressTarget{AgentID: agentID.String(), AgentRole: "character", Name: "Bart"},
	})
	// Each sentence dispatched then delivered (TTSInvoked + FirstAudio interleaved).
	agentReply(bus, "t1", "Well met.", at(3))
	agentReply(bus, "t1", "Sit down.", at(4))
	agentReply(bus, "t1", "What'll it be?", at(5))
	bus.Publish(voiceevent.STTFinal{At: at(6), Text: "An ale", TurnID: "t2"})

	if err := c.FlushSession(context.Background(), fs.id); err != nil {
		t.Fatalf("FlushSession: %v", err)
	}

	got := store.all()
	if len(got) != 1 {
		t.Fatalf("inserts = %d, want 1", len(got))
	}
	want := "Player / DM: Hello Bart\nBart: Well met. Sit down. What'll it be?\nPlayer / DM: An ale"
	if got[0].Content != want {
		t.Errorf("content = %q,\nwant %q", got[0].Content, want)
	}
	if len(got[0].ParticipatedAgentIDs) != 1 || got[0].ParticipatedAgentIDs[0] != agentID {
		t.Errorf("participated = %v, want exactly [%s]", got[0].ParticipatedAgentIDs, agentID)
	}
}

// TestChunker_ChunkCarriesDistinctSpeakerSet is #278: a chunk whose human
// utterances came from two Speaker Lanes carries the DISTINCT speaker set (deduped,
// first-seen order), collected eagerly at append time (the events are gone by
// flush). An agent-only chunk keeps an empty set.
func TestChunker_ChunkCarriesDistinctSpeakerSet(t *testing.T) {
	store := &fakeChunkStore{}
	bus, fs, c := newChunker(t, store, nil, ChunkerConfig{})

	// Two lanes, one repeats — the set dedups to [111, 222] in first-seen order.
	bus.Publish(voiceevent.STTFinal{At: at(1), Text: "a", TurnID: "t", SpeakerID: "111"})
	bus.Publish(voiceevent.STTFinal{At: at(2), Text: "b", TurnID: "t", SpeakerID: "222"})
	bus.Publish(voiceevent.STTFinal{At: at(3), Text: "c", TurnID: "t", SpeakerID: "111"})

	if err := c.FlushSession(context.Background(), fs.id); err != nil {
		t.Fatalf("FlushSession: %v", err)
	}
	got := store.all()
	if len(got) != 1 {
		t.Fatalf("inserts = %d, want 1", len(got))
	}
	if want := []string{"111", "222"}; !equalStrs(got[0].SpeakerDiscordUserIDs, want) {
		t.Errorf("speakers = %v, want %v (distinct, first-seen order)", got[0].SpeakerDiscordUserIDs, want)
	}
}

// TestChunker_AgentOnlyChunkHasNoSpeakers is #278: an agent-only chunk carries an
// empty (non-nil) speaker set — unchanged from the pre-#278 anonymous behaviour.
func TestChunker_AgentOnlyChunkHasNoSpeakers(t *testing.T) {
	store := &fakeChunkStore{}
	bus, fs, c := newChunker(t, store, nil, ChunkerConfig{})
	agentID := uuid.New()

	bus.Publish(voiceevent.AddressRouted{
		At: at(1), TurnID: "t1",
		Target: voiceevent.AddressTarget{AgentID: agentID.String(), AgentRole: "character", Name: "Bart"},
	})
	agentReply(bus, "t1", "Well met.", at(2))

	if err := c.FlushSession(context.Background(), fs.id); err != nil {
		t.Fatalf("FlushSession: %v", err)
	}
	got := store.all()
	if len(got) != 1 {
		t.Fatalf("inserts = %d, want 1", len(got))
	}
	if len(got[0].SpeakerDiscordUserIDs) != 0 {
		t.Errorf("speakers = %v, want empty (agent-only chunk)", got[0].SpeakerDiscordUserIDs)
	}
	if got[0].SpeakerDiscordUserIDs == nil {
		t.Errorf("speakers is nil, want non-nil empty slice (scan contract)")
	}
}

// TestChunker_UnattributedUtteranceAbsentFromSpeakerSet is #278: an unattributed
// utterance (empty SpeakerID) is NEVER added to the chunk's speaker set.
func TestChunker_UnattributedUtteranceAbsentFromSpeakerSet(t *testing.T) {
	store := &fakeChunkStore{}
	bus, fs, c := newChunker(t, store, nil, ChunkerConfig{})

	bus.Publish(voiceevent.STTFinal{At: at(1), Text: "named", TurnID: "t", SpeakerID: "111"})
	bus.Publish(voiceevent.STTFinal{At: at(2), Text: "anon", TurnID: "t"}) // empty SpeakerID

	if err := c.FlushSession(context.Background(), fs.id); err != nil {
		t.Fatalf("FlushSession: %v", err)
	}
	got := store.all()
	if want := []string{"111"}; !equalStrs(got[0].SpeakerDiscordUserIDs, want) {
		t.Errorf("speakers = %v, want %v (empty SpeakerID excluded)", got[0].SpeakerDiscordUserIDs, want)
	}
}

func equalStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestChunker_DispatchedButNotDeliveredIsIgnored is #104 + ADR-0012: TTSInvoked is
// a dispatch attempt, not delivery. A sentence dispatched but never delivered (no
// FirstAudio — e.g. a Synthesize start-error) leaves the chunk unchanged and the
// Agent out of participated_agent_ids.
func TestChunker_DispatchedButNotDeliveredIsIgnored(t *testing.T) {
	store := &fakeChunkStore{}
	bus, fs, c := newChunker(t, store, nil, ChunkerConfig{})
	agentID := uuid.New()

	bus.Publish(voiceevent.STTFinal{At: at(1), Text: "hi", TurnID: "t1"})
	bus.Publish(voiceevent.AddressRouted{
		At: at(2), TurnID: "t1",
		Target: voiceevent.AddressTarget{AgentID: agentID.String(), AgentRole: "character", Name: "Bart"},
	})
	// Dispatched, but no FirstAudio ever follows: the room never heard it.
	bus.Publish(voiceevent.TTSInvoked{At: at(3), Sentence: "unheard", TurnID: "t1"})

	if err := c.FlushSession(context.Background(), fs.id); err != nil {
		t.Fatalf("FlushSession: %v", err)
	}
	got := store.all()
	if len(got) != 1 || got[0].Content != "Player / DM: hi" {
		t.Fatalf("content = %+v, want only the human line (undelivered agent sentence excluded)", got)
	}
	if len(got[0].ParticipatedAgentIDs) != 0 {
		t.Errorf("participated = %v, want empty (agent produced no audio)", got[0].ParticipatedAgentIDs)
	}
}

// TestChunker_UndeliveredTailDroppedOnBarge is #104 + ADR-0012: on a barge the
// turn ends carrying only its delivered sentences; the dispatched-but-undelivered
// tail is dropped, a late TTSInvoked after TurnEnded is dropped + logged, and a
// late FirstAudio after the buffer cleared is a no-op.
func TestChunker_UndeliveredTailDroppedOnBarge(t *testing.T) {
	store := &fakeChunkStore{}
	cap := &capHandler{}
	bus := voiceevent.NewBus()
	fs := &fakeSessions{id: uuid.New(), active: true}
	c := NewChunker(fwd(t, bus, fs), fs, store, nil, slog.New(cap), ChunkerConfig{})
	agentID := uuid.New()

	bus.Publish(voiceevent.AddressRouted{
		At: at(1), TurnID: "t1",
		Target: voiceevent.AddressTarget{AgentID: agentID.String(), AgentRole: "character", Name: "Bart"},
	})
	agentReply(bus, "t1", "Well met.", at(2))                                        // delivered
	agentReply(bus, "t1", "Sit.", at(3))                                             // delivered
	bus.Publish(voiceevent.TTSInvoked{At: at(4), Sentence: "Have a—", TurnID: "t1"}) // dispatched, not delivered
	bus.Publish(voiceevent.TurnEnded{At: at(5), TurnID: "t1", Reason: voiceevent.TurnEndBarge})
	bus.Publish(voiceevent.TTSInvoked{At: at(6), Sentence: "late", TurnID: "t1"}) // after end: dropped + logged
	bus.Publish(voiceevent.FirstAudio{At: at(7), TurnID: "t1"})                   // straggler: no pending -> no-op

	if err := c.FlushSession(context.Background(), fs.id); err != nil {
		t.Fatalf("FlushSession: %v", err)
	}
	got := store.all()
	if len(got) != 1 || got[0].Content != "Bart: Well met. Sit." {
		t.Fatalf("content = %+v, want only the two delivered sentences", got)
	}
	if len(got[0].ParticipatedAgentIDs) != 1 || got[0].ParticipatedAgentIDs[0] != agentID {
		t.Errorf("participated = %v, want [%s]", got[0].ParticipatedAgentIDs, agentID)
	}
	if !cap.has("after turn ended") {
		t.Errorf("late dispatch after TurnEnded was not logged")
	}
}

// TestChunker_SupersededTurnDropsUndeliveredTail (#443): a turn cut by a
// different-target floor supersession gets a TurnEnded{superseded}; the chunker
// must fold it exactly like a barge — the delivered prefix commits, the
// dispatched-but-undelivered tail drops on the terminal event (not a TTL sweep).
func TestChunker_SupersededTurnDropsUndeliveredTail(t *testing.T) {
	store := &fakeChunkStore{}
	bus, fs, c := newChunker(t, store, nil, ChunkerConfig{})
	agentID := uuid.New()

	bus.Publish(voiceevent.AddressRouted{
		At: at(1), TurnID: "t1",
		Target: voiceevent.AddressTarget{AgentID: agentID.String(), AgentRole: "character", Name: "Bart"},
	})
	agentReply(bus, "t1", "I shall.", at(2))                                          // delivered
	bus.Publish(voiceevent.TTSInvoked{At: at(3), Sentence: "And fur—", TurnID: "t1"}) // dispatched, not delivered
	bus.Publish(voiceevent.TurnEnded{At: at(4), TurnID: "t1", Reason: voiceevent.TurnEndSuperseded})

	if err := c.FlushSession(context.Background(), fs.id); err != nil {
		t.Fatalf("FlushSession: %v", err)
	}
	got := store.all()
	if len(got) != 1 || got[0].Content != "Bart: I shall." {
		t.Fatalf("content = %+v, want only the delivered sentence of the superseded turn", got)
	}
}

// TestChunker_ZeroDeliveredTurnLogsNothing is #104 + ADR-0012: a turn interrupted
// before its first sentence is delivered (no FirstAudio at all) contributes no
// utterance — a chunk never even opens for it.
func TestChunker_ZeroDeliveredTurnLogsNothing(t *testing.T) {
	store := &fakeChunkStore{}
	bus, fs, c := newChunker(t, store, nil, ChunkerConfig{})
	agentID := uuid.New()

	bus.Publish(voiceevent.AddressRouted{
		At: at(1), TurnID: "t1",
		Target: voiceevent.AddressTarget{AgentID: agentID.String(), AgentRole: "character", Name: "Bart"},
	})
	bus.Publish(voiceevent.TTSInvoked{At: at(2), Sentence: "cut off", TurnID: "t1"})
	bus.Publish(voiceevent.TurnEnded{At: at(3), TurnID: "t1", Reason: voiceevent.TurnEndBarge})

	if err := c.FlushSession(context.Background(), fs.id); err != nil {
		t.Fatalf("FlushSession: %v", err)
	}
	if n := store.count(); n != 0 {
		t.Fatalf("inserts = %d, want 0 (zero-delivered turn logs nothing)", n)
	}
}

// TestChunker_SupersededDispatchPurged is #104 + ADR-0012: dispatch is serial
// single-in-flight and FirstAudio(sN) precedes TTSInvoked(sN+1), so a sentence
// still pending when the NEXT dispatch arrives start-errored (never delivered). It
// is purged, not committed — the delivered sentence's text lands, not the unheard
// one, and the FirstAudio pairing does not shift.
func TestChunker_SupersededDispatchPurged(t *testing.T) {
	store := &fakeChunkStore{}
	bus, fs, c := newChunker(t, store, nil, ChunkerConfig{})
	agentID := uuid.New()

	bus.Publish(voiceevent.AddressRouted{
		At: at(1), TurnID: "t1",
		Target: voiceevent.AddressTarget{AgentID: agentID.String(), AgentRole: "character", Name: "Bart"},
	})
	bus.Publish(voiceevent.TTSInvoked{At: at(2), Sentence: "start-errored", TurnID: "t1"}) // no FirstAudio
	bus.Publish(voiceevent.TTSInvoked{At: at(3), Sentence: "delivered", TurnID: "t1"})
	bus.Publish(voiceevent.FirstAudio{At: at(3), TurnID: "t1"})

	if err := c.FlushSession(context.Background(), fs.id); err != nil {
		t.Fatalf("FlushSession: %v", err)
	}
	got := store.all()
	if len(got) != 1 || got[0].Content != "Bart: delivered" {
		t.Fatalf("content = %+v, want the delivered sentence only (superseded dead dispatch purged)", got)
	}
	if len(got[0].ParticipatedAgentIDs) != 1 || got[0].ParticipatedAgentIDs[0] != agentID {
		t.Errorf("participated = %v, want [%s]", got[0].ParticipatedAgentIDs, agentID)
	}
}

// TestChunker_RecoveredMidTurnErrorKeepsPairing is #104 + ADR-0012 — the reachable
// FIFO/purge pin (a depth-2 pending where BOTH commit cannot occur: serial
// single-in-flight dispatch means FirstAudio(sN) precedes TTSInvoked(sN+1), so
// pending never holds two undelivered sentences at once). A middle sentence that
// start-errors while the turn recovers (s1 delivered, s2 lost, s3 delivered) must
// commit only s1 and s3, in order, coalesced into the one utterance — never the
// unheard s2, and without shifting s3 onto s2's slot.
func TestChunker_RecoveredMidTurnErrorKeepsPairing(t *testing.T) {
	store := &fakeChunkStore{}
	bus, fs, c := newChunker(t, store, nil, ChunkerConfig{})
	agentID := uuid.New()

	bus.Publish(voiceevent.AddressRouted{
		At: at(1), TurnID: "t1",
		Target: voiceevent.AddressTarget{AgentID: agentID.String(), AgentRole: "character", Name: "Bart"},
	})
	agentReply(bus, "t1", "Well met.", at(2))                                     // delivered
	bus.Publish(voiceevent.TTSInvoked{At: at(3), Sentence: "lost", TurnID: "t1"}) // start-error, no FirstAudio
	agentReply(bus, "t1", "Sit.", at(4))                                          // recovers, delivered

	if err := c.FlushSession(context.Background(), fs.id); err != nil {
		t.Fatalf("FlushSession: %v", err)
	}
	got := store.all()
	if len(got) != 1 || got[0].Content != "Bart: Well met. Sit." {
		t.Fatalf("content = %+v, want only s1 + s3 in order (lost middle sentence excluded)", got)
	}
	if len(got[0].ParticipatedAgentIDs) != 1 || got[0].ParticipatedAgentIDs[0] != agentID {
		t.Errorf("participated = %v, want [%s]", got[0].ParticipatedAgentIDs, agentID)
	}
}

// TestChunker_ContinuationAcrossChunkClose is #104 rule 4: when a turn's first
// delivered sentence is the fifth utterance, the chunk closes with that sentence;
// the turn's next delivered sentence opens a CONTINUATION utterance in the next
// chunk, with the Agent in that new chunk's participated set and started_at at the
// continuation's delivery time.
func TestChunker_ContinuationAcrossChunkClose(t *testing.T) {
	store := &fakeChunkStore{}
	vs := storage.VoiceSession{ID: uuid.New(), CampaignID: uuid.New()}
	bus, c := liveChunker(t, store, nil, ChunkerConfig{}, vs) // default MaxUtterances = 5
	agentID := uuid.New()

	// Four human utterances fill slots 1–4.
	for i := 1; i <= 4; i++ {
		bus.Publish(voiceevent.STTFinal{At: at(i), Text: "h" + string(rune('0'+i)), TurnID: "h"})
	}
	bus.Publish(voiceevent.AddressRouted{
		At: at(5), TurnID: "t1",
		Target: voiceevent.AddressTarget{AgentID: agentID.String(), AgentRole: "character", Name: "Bart"},
	})
	agentReply(bus, "t1", "one", at(5)) // 5th utterance -> closes chunk 1
	agentReply(bus, "t1", "two", at(6)) // continuation -> opens chunk 2

	// chunk 1 auto-closed; FlushSession closes the continuation chunk 2 and drains.
	if err := c.FlushSession(context.Background(), vs.ID); err != nil {
		t.Fatalf("FlushSession: %v", err)
	}
	got := store.all()
	if len(got) != 2 {
		t.Fatalf("inserts = %d, want 2 (chunk + continuation)", len(got))
	}
	want1 := "Player / DM: h1\nPlayer / DM: h2\nPlayer / DM: h3\nPlayer / DM: h4\nBart: one"
	if got[0].Content != want1 {
		t.Errorf("chunk 1 content = %q,\nwant %q", got[0].Content, want1)
	}
	if len(got[0].ParticipatedAgentIDs) != 1 || got[0].ParticipatedAgentIDs[0] != agentID {
		t.Errorf("chunk 1 participated = %v, want [%s]", got[0].ParticipatedAgentIDs, agentID)
	}
	if got[1].Content != "Bart: two" {
		t.Errorf("continuation content = %q, want %q", got[1].Content, "Bart: two")
	}
	if len(got[1].ParticipatedAgentIDs) != 1 || got[1].ParticipatedAgentIDs[0] != agentID {
		t.Errorf("continuation participated = %v, want [%s] (agent in the new chunk too)", got[1].ParticipatedAgentIDs, agentID)
	}
	if !got[1].StartedAt.Equal(at(6)) {
		t.Errorf("continuation started_at = %s, want the continuation delivery time %s", got[1].StartedAt, at(6))
	}
}

// TestChunker_TwoSessionsNoCrossTalk is the #487 chunker isolation invariant:
// two live sessions' interleaved utterances fold into separate open chunks and
// persist under their OWN FKs — never mixed, never cross-attributed. (Replaces
// the old rollover-safety-net test: session end is now explicit per session, not
// inferred from a new session's id.)
func TestChunker_TwoSessionsNoCrossTalk(t *testing.T) {
	store := &fakeChunkStore{}
	sessA := storage.VoiceSession{ID: uuid.New(), CampaignID: uuid.New()}
	sessB := storage.VoiceSession{ID: uuid.New(), CampaignID: uuid.New()}
	// Two session buses bridged onto ONE process bus, each stamping its own id.
	busA, busB := voiceevent.NewBus(), voiceevent.NewBus()
	proc := voiceevent.NewBus()
	t.Cleanup(voiceevent.Forward(busA, proc, sessA.ID.String()))
	t.Cleanup(voiceevent.Forward(busB, proc, sessB.ID.String()))
	fs := newMultiSessions(sessA, sessB)
	c := NewChunker(proc, fs, store, nil, slog.New(slog.DiscardHandler), ChunkerConfig{})

	// Interleave A and B utterances.
	busA.Publish(voiceevent.STTFinal{At: at(1), Text: "a1", TurnID: "t"})
	busB.Publish(voiceevent.STTFinal{At: at(2), Text: "b1", TurnID: "t"})
	busA.Publish(voiceevent.STTFinal{At: at(3), Text: "a2", TurnID: "t"})
	busB.Publish(voiceevent.STTFinal{At: at(4), Text: "b2", TurnID: "t"})

	if err := c.FlushSession(context.Background(), sessA.ID); err != nil {
		t.Fatalf("FlushSession(A): %v", err)
	}
	if err := c.FlushSession(context.Background(), sessB.ID); err != nil {
		t.Fatalf("FlushSession(B): %v", err)
	}

	got := store.all()
	if len(got) != 2 {
		t.Fatalf("inserts = %d, want 2 (one chunk per session)", len(got))
	}
	byID := map[uuid.UUID]storage.TranscriptChunk{}
	for _, g := range got {
		byID[g.VoiceSessionID] = g
	}
	if a := byID[sessA.ID]; a.CampaignID != sessA.CampaignID || a.Content != "Player / DM: a1\nPlayer / DM: a2" {
		t.Errorf("session A chunk = %+v, want only a1/a2 under A's campaign (no B leakage)", a)
	}
	if b := byID[sessB.ID]; b.CampaignID != sessB.CampaignID || b.Content != "Player / DM: b1\nPlayer / DM: b2" {
		t.Errorf("session B chunk = %+v, want only b1/b2 under B's campaign (no A leakage)", b)
	}
}

// TestChunker_NonUUIDAgentIDSkippedAndLogged is #104 rule UTTERANCES: an
// AddressTarget.AgentID that is not a DB UUID (the well-known "butler" route, or
// any non-uuid) is skipped from participated_agent_ids and logged — the utterance
// still lands, but the chunk carries no participant for it.
func TestChunker_NonUUIDAgentIDSkippedAndLogged(t *testing.T) {
	store := &fakeChunkStore{}
	cap := &capHandler{}
	bus := voiceevent.NewBus()
	fs := &fakeSessions{id: uuid.New(), active: true}
	c := NewChunker(fwd(t, bus, fs), fs, store, nil, slog.New(cap), ChunkerConfig{})

	bus.Publish(voiceevent.AddressRouted{
		At: at(1), TurnID: "t1",
		Target: voiceevent.AddressTarget{AgentID: "butler", AgentRole: "butler", Name: "Butler"},
	})
	agentReply(bus, "t1", "At your service.", at(2))

	if err := c.FlushSession(context.Background(), fs.id); err != nil {
		t.Fatalf("FlushSession: %v", err)
	}
	got := store.all()
	if len(got) != 1 || got[0].Content != "Butler: At your service." {
		t.Fatalf("content = %+v, want the butler utterance", got)
	}
	if len(got[0].ParticipatedAgentIDs) != 0 {
		t.Errorf("participated = %v, want empty (non-uuid agent id skipped)", got[0].ParticipatedAgentIDs)
	}
	if !cap.has("unparsable agent id") {
		t.Errorf("non-uuid agent id skip was not logged")
	}
}

// TestChunker_HumanOnlyChunkEmptyParticipants is #104 AC: a chunk with no Agent
// reply has empty participated_agent_ids.
func TestChunker_HumanOnlyChunkEmptyParticipants(t *testing.T) {
	store := &fakeChunkStore{}
	bus, fs, c := newChunker(t, store, nil, ChunkerConfig{})

	bus.Publish(voiceevent.STTFinal{At: at(1), Text: "just me", TurnID: "t1"})
	if err := c.FlushSession(context.Background(), fs.id); err != nil {
		t.Fatalf("FlushSession: %v", err)
	}
	got := store.all()
	if len(got) != 1 || len(got[0].ParticipatedAgentIDs) != 0 {
		t.Fatalf("participated = %v, want empty", got)
	}
}

// TestChunker_GaugeSetFromCount is #104 AC: after each written chunk the gauge is
// Set to the store's live NULL-embedding count, rising by one per new chunk.
func TestChunker_GaugeSetFromCount(t *testing.T) {
	store := &fakeChunkStore{}
	gauge := &fakeGauge{}
	// MaxUtterances=1 so each STTFinal closes its own chunk.
	bus, fs, c := newChunker(t, store, gauge, ChunkerConfig{MaxUtterances: 1})

	bus.Publish(voiceevent.STTFinal{At: at(1), Text: "a", TurnID: "t1"})
	bus.Publish(voiceevent.STTFinal{At: at(2), Text: "b", TurnID: "t2"})
	if err := c.FlushSession(context.Background(), fs.id); err != nil {
		t.Fatalf("FlushSession: %v", err)
	}

	sets := gauge.snapshot()
	if len(sets) != 2 || sets[0] != 1 || sets[1] != 2 {
		t.Fatalf("gauge sets = %v, want [1 2] (rises by one per written chunk)", sets)
	}
}

// blockingChunkStore blocks every InsertTranscriptChunk on release, signalling
// the first entry on entered — so the writer goroutine can be pinned mid-insert
// and the bounded queue driven to overflow.
type blockingChunkStore struct {
	entered chan struct{}
	release chan struct{}
	mu      sync.Mutex
	inserts int
}

func (b *blockingChunkStore) InsertTranscriptChunk(_ context.Context, _ storage.TranscriptChunk) (uuid.UUID, error) {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-b.release
	b.mu.Lock()
	b.inserts++
	b.mu.Unlock()
	return uuid.New(), nil
}
func (b *blockingChunkStore) CountUnembeddedChunks(_ context.Context) (int, error) { return 0, nil }
func (b *blockingChunkStore) done() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.inserts
}

// TestChunker_BusCallbackNeverBlocks is #104 WRITE PATH (voiceevent.Bus contract):
// the bus callback must never block on the DB. With the single writer pinned
// mid-insert and the bounded queue full, further chunk closes are dropped + logged
// rather than blocking Publish — so only the in-flight + queued chunks (1 + cap)
// are ever written.
func TestChunker_BusCallbackNeverBlocks(t *testing.T) {
	store := &blockingChunkStore{entered: make(chan struct{}, 1), release: make(chan struct{})}
	cap := &capHandler{}
	bus := voiceevent.NewBus()
	fs := &fakeSessions{id: uuid.New(), active: true}
	// MaxUtterances=1: each STTFinal closes and enqueues its own chunk insert. The
	// chunker is driven entirely through the bus, so it needs no local handle.
	_ = NewChunker(fwd(t, bus, fs), fs, store, nil, slog.New(cap), ChunkerConfig{MaxUtterances: 1})

	// First close: the writer dequeues it and pins on release. entered confirms the
	// queue is drained to empty before we fill it, so the accepted count is exact.
	bus.Publish(voiceevent.STTFinal{At: at(0), Text: "0", TurnID: "t0"})
	select {
	case <-store.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("writer never reached InsertTranscriptChunk")
	}

	// Fill the bounded queue (chunkQueue) and then overflow it. Each Publish must
	// return promptly; a blocked send would hang the test (caught by go test).
	total := chunkQueue + 25
	done := make(chan struct{})
	go func() {
		for i := 1; i <= total; i++ {
			bus.Publish(voiceevent.STTFinal{At: at(i), Text: "x", TurnID: "t" + string(rune(i))})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a full-queue Publish blocked the bus callback")
	}

	if !cap.has("queue full") {
		t.Errorf("overflow was not logged")
	}

	// Release the writer; exactly 1 (in-flight) + chunkQueue accepted are written,
	// the rest dropped.
	close(store.release)
	want := 1 + chunkQueue
	if !eventually(t, 3*time.Second, func() bool { return store.done() == want }) {
		t.Fatalf("writer wrote %d chunks, want %d (in-flight + queue; overflow dropped)", store.done(), want)
	}
}

// TestChunker_LookaheadReaction_CommitsOnDelivery pins #375 at the chunk tier (happy):
// with F1+F2 the reaction's TTSInvoked (at release) is followed by its FirstAudio (after
// playback), so the reaction text is buffered THEN committed — it lands in the chunk.
func TestChunker_LookaheadReaction_CommitsOnDelivery(t *testing.T) {
	store := &fakeChunkStore{}
	bus, fs, c := newChunker(t, store, nil, ChunkerConfig{Window: time.Hour}) // no auto-close

	// Lead turn delivers.
	bus.Publish(voiceevent.AddressRouted{At: at(1), TurnID: "Te", Target: voiceevent.AddressTarget{AgentID: uuid.NewString(), AgentRole: "character", Name: "Bart"}})
	agentReply(bus, "Te", "Bart leads.", at(1))
	// Reaction: TTSInvoked (at release) THEN FirstAudio (after playback) — F1/F2 order.
	bus.Publish(voiceevent.TTSInvoked{At: at(2), Sentence: "I disagree.", TurnID: "rID"})
	bus.Publish(voiceevent.FirstAudio{At: at(2), TurnID: "rID"})

	if err := c.FlushSession(context.Background(), fs.id); err != nil {
		t.Fatalf("FlushSession: %v", err)
	}
	if store.count() != 1 {
		t.Fatalf("chunk inserts = %d, want 1", store.count())
	}
	content := store.all()[0].Content
	if !strings.Contains(content, "I disagree.") {
		t.Fatalf("chunk content = %q, want it to contain the delivered reaction text", content)
	}
}

// TestChunker_LookaheadReaction_DiscardCommitsNoReactionText pins #375's discard win at
// the chunk tier: a reaction that was pre-rendered but DISCARDED before playback emits
// NEITHER TTSInvoked{rID} (F1) NOR FirstAudio{rID} (F2), so no reaction text is ever
// buffered or committed — the chunk holds only the Lead's line.
func TestChunker_LookaheadReaction_DiscardCommitsNoReactionText(t *testing.T) {
	store := &fakeChunkStore{}
	bus, fs, c := newChunker(t, store, nil, ChunkerConfig{Window: time.Hour})

	bus.Publish(voiceevent.AddressRouted{At: at(1), TurnID: "Te", Target: voiceevent.AddressTarget{AgentID: uuid.NewString(), AgentRole: "character", Name: "Bart"}})
	agentReply(bus, "Te", "Bart leads.", at(1))
	// Barge before release: no TTSInvoked{rID}, no FirstAudio{rID} are ever published.

	if err := c.FlushSession(context.Background(), fs.id); err != nil {
		t.Fatalf("FlushSession: %v", err)
	}
	content := store.all()[0].Content
	if !strings.Contains(content, "Bart leads.") {
		t.Fatalf("chunk content = %q, want the Lead's line", content)
	}
	if strings.Contains(content, "I disagree.") {
		t.Fatalf("chunk content = %q, must NOT contain a discarded reaction's text", content)
	}
}

// TestChunker_MidStreamFailureRetractsCommittedSentence pins the #436 chunk-grain
// parity: a sentence whose FirstAudio fired but whose synthesis stream then died
// mid-delivery (TTSStreamFailed) is RETRACTED from the turn's line — the chunk
// records only the fully delivered sentences, agreeing with Agent history which
// omits the failed one (ADR-0012's never-record-unheard bias).
func TestChunker_MidStreamFailureRetractsCommittedSentence(t *testing.T) {
	store := &fakeChunkStore{}
	bus, fs, c := newChunker(t, store, nil, ChunkerConfig{})

	bus.Publish(voiceevent.STTFinal{At: at(1), Text: "Hello Bart", TurnID: "t1"})
	bus.Publish(voiceevent.AddressRouted{
		At: at(2), TurnID: "t1",
		Target: voiceevent.AddressTarget{AgentID: uuid.New().String(), AgentRole: "character", Name: "Bart"},
	})
	agentReply(bus, "t1", "Well met.", at(3))
	// The second sentence delivers its first chunk (FirstAudio commits it), then
	// the provider drops the stream — the room heard only a fragment.
	agentReply(bus, "t1", "Sit down and never leave this inn again.", at(4))
	bus.Publish(voiceevent.TTSStreamFailed{At: at(5), TurnID: "t1"})
	// The turn recovers with a third, fully delivered sentence (skip-and-continue).
	agentReply(bus, "t1", "What'll it be?", at(6))

	if err := c.FlushSession(context.Background(), fs.id); err != nil {
		t.Fatalf("FlushSession: %v", err)
	}
	got := store.all()
	if len(got) != 1 {
		t.Fatalf("inserts = %d, want 1", len(got))
	}
	want := "Player / DM: Hello Bart\nBart: Well met. What'll it be?"
	if got[0].Content != want {
		t.Errorf("content = %q,\nwant %q (the mid-stream-failed sentence retracted)", got[0].Content, want)
	}
}

// TestChunker_MidStreamFailureOnOpeningSentenceDropsLine pins the #436 zero-line
// case: when the turn's FIRST (and only committed) sentence fails mid-stream, the
// retraction leaves no delivered text — the whole line leaves the open chunk, no
// empty "Who:" artifact persists, and a later delivered sentence re-opens a line.
func TestChunker_MidStreamFailureOnOpeningSentenceDropsLine(t *testing.T) {
	store := &fakeChunkStore{}
	bus, fs, c := newChunker(t, store, nil, ChunkerConfig{})

	bus.Publish(voiceevent.STTFinal{At: at(1), Text: "Hello Bart", TurnID: "t1"})
	bus.Publish(voiceevent.AddressRouted{
		At: at(2), TurnID: "t1",
		Target: voiceevent.AddressTarget{AgentID: uuid.New().String(), AgentRole: "character", Name: "Bart"},
	})
	agentReply(bus, "t1", "This whole sentence dies mid-air.", at(3))
	bus.Publish(voiceevent.TTSStreamFailed{At: at(4), TurnID: "t1"})
	agentReply(bus, "t1", "Second try lands.", at(5))

	if err := c.FlushSession(context.Background(), fs.id); err != nil {
		t.Fatalf("FlushSession: %v", err)
	}
	got := store.all()
	if len(got) != 1 {
		t.Fatalf("inserts = %d, want 1", len(got))
	}
	want := "Player / DM: Hello Bart\nBart: Second try lands."
	if got[0].Content != want {
		t.Errorf("content = %q,\nwant %q (no empty line artifact, retry re-opens)", got[0].Content, want)
	}
}

// TestChunker_MidStreamFailureBeforeFirstAudioPurgesPending pins the #436
// no-audio case: a stream that dies BEFORE its first real chunk fired no
// FirstAudio, so the sentence still heads pending — the failure purges it so a
// straggler FirstAudio can never commit it later.
func TestChunker_MidStreamFailureBeforeFirstAudioPurgesPending(t *testing.T) {
	store := &fakeChunkStore{}
	bus, fs, c := newChunker(t, store, nil, ChunkerConfig{})

	bus.Publish(voiceevent.STTFinal{At: at(1), Text: "Hello Bart", TurnID: "t1"})
	bus.Publish(voiceevent.AddressRouted{
		At: at(2), TurnID: "t1",
		Target: voiceevent.AddressTarget{AgentID: uuid.New().String(), AgentRole: "character", Name: "Bart"},
	})
	// Dispatched but the stream died before any audio: TTSInvoked, no FirstAudio.
	bus.Publish(voiceevent.TTSInvoked{At: at(3), Sentence: "Never heard at all.", TurnID: "t1"})
	bus.Publish(voiceevent.TTSStreamFailed{At: at(4), TurnID: "t1"})
	// A straggler FirstAudio (e.g. racing teardown) must find nothing to commit.
	bus.Publish(voiceevent.FirstAudio{At: at(5), TurnID: "t1"})
	agentReply(bus, "t1", "Recovered.", at(6))

	if err := c.FlushSession(context.Background(), fs.id); err != nil {
		t.Fatalf("FlushSession: %v", err)
	}
	got := store.all()
	if len(got) != 1 {
		t.Fatalf("inserts = %d, want 1", len(got))
	}
	want := "Player / DM: Hello Bart\nBart: Recovered."
	if got[0].Content != want {
		t.Errorf("content = %q,\nwant %q (the undelivered pending sentence purged)", got[0].Content, want)
	}
}
