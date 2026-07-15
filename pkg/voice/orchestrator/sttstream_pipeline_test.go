package orchestrator_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/pkg/voice/address"
	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// scriptedStream is a deterministic [stt.Stream] for pipeline tests: it emits one
// scripted partial per successful Send (simulating provider partials during
// speech), can fail Send after a threshold (mid-utterance death), and resolves
// each Commit with a preset result.
type scriptedStream struct {
	partials  []string
	committed stt.CommitResult
	// failAfterSends > 0 makes Send fail (fatal transport) once that many sends have
	// succeeded — the mid-utterance websocket death. 0 never fails.
	failAfterSends int

	mu         sync.Mutex
	onPartial  func(string)
	partialIdx int
	sends      int
}

func (s *scriptedStream) Send(audio.Frame) error {
	s.mu.Lock()
	if s.failAfterSends > 0 && s.sends >= s.failAfterSends {
		s.mu.Unlock()
		return &stt.StreamError{Code: stt.CodeTransport, Fatal: true, Err: errors.New("scripted mid-utterance death")}
	}
	s.sends++
	var partial string
	var has bool
	if s.partialIdx < len(s.partials) {
		partial = s.partials[s.partialIdx]
		s.partialIdx++
		has = true
	}
	cb := s.onPartial
	s.mu.Unlock()
	if has && cb != nil {
		cb(partial) // provider emits an interim hypothesis as audio arrives
	}
	return nil
}

func (s *scriptedStream) Commit() (<-chan stt.CommitResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan stt.CommitResult, 1)
	ch <- s.committed
	return ch, nil
}

func (s *scriptedStream) Close() error { return nil }

// multiStreamRecognizer hands out its streams in order (one per OpenStream), wiring
// each session's OnPartial back onto the stream. A recognizer with a single stream
// models a stable session; several model reconnects across mid-session deaths.
type multiStreamRecognizer struct {
	mu      sync.Mutex
	streams []*scriptedStream
	idx     int
}

func (r *multiStreamRecognizer) OpenStream(_ context.Context, cfg stt.StreamConfig) (stt.Stream, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.idx >= len(r.streams) {
		return nil, &stt.StreamError{Code: stt.CodeTransport, Fatal: true, Err: errors.New("no more scripted streams")}
	}
	s := r.streams[r.idx]
	r.idx++
	s.onPartial = cfg.OnPartial
	return s, nil
}

func (r *multiStreamRecognizer) opens() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.idx
}

// metricSpy records STTRequest calls; other StageRecorder methods no-op via Discard.
type metricSpy struct {
	observe.Discard
	mu          sync.Mutex
	sttRequests int
}

func (s *metricSpy) STTRequest(observe.Provider, time.Duration) {
	s.mu.Lock()
	s.sttRequests++
	s.mu.Unlock()
}

func (s *metricSpy) requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sttRequests
}

// feedConv pushes n silent frames through the conversation's audio loop.
func feedConv(t *testing.T, conv *orchestrator.Conversation, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := conv.Feed(segFrame(t)); err != nil {
			t.Fatalf("frame %d: Feed: %v", i, err)
		}
	}
}

// partialsAndFinals splits the harness event log into the utterance's streamed
// partials and its authoritative finals.
func partialsAndFinals(h *voicetest.Harness) (partials []voiceevent.STTPartial, finals []voiceevent.STTFinal) {
	for _, e := range h.Events() {
		switch ev := e.(type) {
		case voiceevent.STTPartial:
			partials = append(partials, ev)
		case voiceevent.STTFinal:
			finals = append(finals, ev)
		}
	}
	return partials, finals
}

// TestConversation_StreamingUtterance_FinalFromCommit is the streaming happy path
// (TDD step 5): an utterance produces STTPartials during speech and an STTFinal
// from the manual commit at the local VAD endpoint — carrying a fresh TurnID, the
// speech-end time, and the utterance id its partials share — WITHOUT the batch
// recognizer being called at all.
func TestConversation_StreamingUtterance_FinalFromCommit(t *testing.T) {
	h := voicetest.New(t)

	// The batch recognizer is a spy that must stay untouched on the happy path.
	batch := &recordingRecognizer{}
	sttStage := orchestrator.NewSTT(h.Bus, batch)
	vadStage := orchestrator.NewVAD(h.Bus, &scriptedVAD{events: []vad.VADEventType{
		vad.VADSpeechStart, vad.VADSpeechContinue, vad.VADSpeechContinue, vad.VADSpeechEnd,
	}})
	stream := &scriptedStream{
		partials:  []string{"roll", "roll a", "roll a d20"},
		committed: stt.CommitResult{Transcript: stt.Transcript{Text: "roll a d20"}},
	}
	sm := orchestrator.NewStreamManager(&multiStreamRecognizer{streams: []*scriptedStream{stream}})

	conv := mustConversation(orchestrator.NewConversation(h.Bus, vadStage, sttStage, nil,
		orchestrator.WithStreamingSTT(sm)))
	t.Cleanup(conv.Register(t.Context()))
	if !sm.WaitStreamUp(2 * time.Second) {
		t.Fatal("the eager dial never brought a session up; utterance 1 could not stream")
	}

	var speechEnd time.Time
	voiceevent.On(h.Bus, func(e voiceevent.VADSpeechEnd) { speechEnd = e.At })

	feedConv(t, conv, 5) // start, continue, continue, end, (flush frame)
	if err := conv.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	partials, finals := partialsAndFinals(h)
	if len(partials) == 0 {
		t.Error("no STTPartial published during speech")
	}
	if len(finals) != 1 {
		t.Fatalf("%d STTFinal published, want exactly 1", len(finals))
	}
	f := finals[0]
	if f.Text != "roll a d20" {
		t.Errorf("STTFinal.Text = %q, want the committed text %q", f.Text, "roll a d20")
	}
	if f.TurnID == "" {
		t.Error("streamed STTFinal carried no TurnID")
	}
	if f.UtteranceID == "" {
		t.Error("streamed STTFinal carried no UtteranceID")
	}
	if !f.SpeechEndAt.Equal(speechEnd) || f.SpeechEndAt.IsZero() {
		t.Errorf("STTFinal.SpeechEndAt = %v, want the VAD speech-end %v", f.SpeechEndAt, speechEnd)
	}
	if partials[len(partials)-1].UtteranceID != f.UtteranceID {
		t.Error("partials and the final do not share the utterance id")
	}
	if got := len(batch.batches()); got != 0 {
		t.Errorf("batch recognizer called %d times; want 0 on the streaming happy path", got)
	}
}

// TestConversation_StreamingCommitError_FallsBackToBatch is TDD step 6: a commit
// that resolves with a provider error degrades to the batch recognizer, which
// receives EXACTLY the locally-buffered voiced frames (the pre-roll ring is never
// added, so cassette hashes stay stable) and produces the STTFinal. No utterance is
// lost.
func TestConversation_StreamingCommitError_FallsBackToBatch(t *testing.T) {
	h := voicetest.New(t)

	batch := &recordingRecognizer{} // returns "ok"
	sttStage := orchestrator.NewSTT(h.Bus, batch)
	// Two leading silences fill the pre-roll ring; the utterance is 3 voiced frames.
	vadStage := orchestrator.NewVAD(h.Bus, &scriptedVAD{events: []vad.VADEventType{
		vad.VADSilence, vad.VADSilence,
		vad.VADSpeechStart, vad.VADSpeechContinue, vad.VADSpeechContinue, vad.VADSpeechEnd,
	}})
	stream := &scriptedStream{
		committed: stt.CommitResult{Err: &stt.StreamError{Code: "commit_throttled", Fatal: false, Err: errors.New("throttled")}},
	}
	sm := orchestrator.NewStreamManager(&multiStreamRecognizer{streams: []*scriptedStream{stream}})

	conv := mustConversation(orchestrator.NewConversation(h.Bus, vadStage, sttStage, nil,
		orchestrator.WithStreamingSTT(sm)))
	t.Cleanup(conv.Register(t.Context()))
	if !sm.WaitStreamUp(2 * time.Second) {
		t.Fatal("eager dial never came up")
	}

	feedConv(t, conv, 7) // 2 silence + start,cont,cont,end + flush frame
	if err := conv.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	batches := batch.batches()
	if len(batches) != 1 {
		t.Fatalf("batch recognizer called %d times, want 1 (commit-error fallback)", len(batches))
	}
	if got := len(batches[0]); got != 3 {
		t.Errorf("batch segment had %d frames, want 3 (voiced only; pre-roll must not leak into the segment)", got)
	}
	_, finals := partialsAndFinals(h)
	if len(finals) != 1 || finals[0].Text != "ok" {
		t.Fatalf("STTFinal = %+v, want exactly 1 with the batch text %q", finals, "ok")
	}
	if finals[0].UtteranceID == "" {
		t.Error("a fallback STTFinal carried no UtteranceID; it should still join its partials")
	}
}

// TestConversation_MidUtteranceDeath_BatchThenReconnect is TDD step 7: the
// websocket dies mid-utterance; that utterance degrades to batch with its full
// segment intact (no transcription lost), and after the maintainer redials a later
// utterance streams again.
func TestConversation_MidUtteranceDeath_BatchThenReconnect(t *testing.T) {
	h := voicetest.New(t)

	batch := &recordingRecognizer{} // returns "ok"
	sttStage := orchestrator.NewSTT(h.Bus, batch)
	vadStage := orchestrator.NewVAD(h.Bus, &scriptedVAD{events: []vad.VADEventType{
		vad.VADSpeechStart, vad.VADSpeechContinue, vad.VADSpeechContinue, vad.VADSpeechEnd, // utterance 1: dies mid-stream
		vad.VADSpeechStart, vad.VADSpeechContinue, vad.VADSpeechEnd, // utterance 2: streams on the fresh session
	}})
	dying := &scriptedStream{failAfterSends: 2} // f0,f1 ok; f2 kills the session
	healthy := &scriptedStream{
		partials:  []string{"hi", "hi there"},
		committed: stt.CommitResult{Transcript: stt.Transcript{Text: "hi there"}},
	}
	rec := &multiStreamRecognizer{streams: []*scriptedStream{dying, healthy}}
	sm := orchestrator.NewStreamManager(rec, orchestrator.WithStreamBackoff(time.Millisecond, 10*time.Millisecond))

	conv := mustConversation(orchestrator.NewConversation(h.Bus, vadStage, sttStage, nil,
		orchestrator.WithStreamingSTT(sm)))
	t.Cleanup(conv.Register(t.Context()))
	if !sm.WaitStreamUp(2 * time.Second) {
		t.Fatal("eager dial never came up")
	}

	feedConv(t, conv, 4) // utterance 1: start, cont, cont(death), end(flush)
	// The session must re-establish after the mid-utterance death before utterance 2.
	if !sm.WaitStreamUp(2 * time.Second) {
		t.Fatal("the stream did not re-establish after the mid-utterance death")
	}
	if rec.opens() != 2 {
		t.Fatalf("recognizer opened %d sessions, want 2 (eager dial + one reconnect)", rec.opens())
	}
	feedConv(t, conv, 3) // utterance 2: start, cont, end
	if err := conv.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Utterance 1 fell to batch with its full 3-frame segment; utterance 2 streamed.
	batches := batch.batches()
	if len(batches) != 1 {
		t.Fatalf("batch recognizer called %d times, want 1 (only the dead utterance)", len(batches))
	}
	if got := len(batches[0]); got != 3 {
		t.Errorf("dead utterance's batch segment had %d frames, want 3 (full utterance, nothing lost)", got)
	}
	_, finals := partialsAndFinals(h)
	if len(finals) != 2 {
		t.Fatalf("%d STTFinal, want 2 (batch fallback + reconnected stream)", len(finals))
	}
	texts := map[string]bool{finals[0].Text: true, finals[1].Text: true}
	if !texts["ok"] || !texts["hi there"] {
		t.Errorf("STTFinal texts = %v, want {ok (batch), hi there (streamed)}", texts)
	}
}

// TestConversation_EmptyCommit_PublishesEmptyFinal is TDD step 9: an
// insufficient-audio commit (empty transcript, nil error) is a success, not a
// fallback — it publishes an empty STTFinal (batch parity), and the batch
// recognizer is not called.
func TestConversation_EmptyCommit_PublishesEmptyFinal(t *testing.T) {
	h := voicetest.New(t)

	batch := &recordingRecognizer{}
	sttStage := orchestrator.NewSTT(h.Bus, batch)
	vadStage := orchestrator.NewVAD(h.Bus, &scriptedVAD{events: []vad.VADEventType{
		vad.VADSpeechStart, vad.VADSpeechEnd,
	}})
	stream := &scriptedStream{committed: stt.CommitResult{Transcript: stt.Transcript{Text: ""}}}
	sm := orchestrator.NewStreamManager(&multiStreamRecognizer{streams: []*scriptedStream{stream}})

	conv := mustConversation(orchestrator.NewConversation(h.Bus, vadStage, sttStage, nil,
		orchestrator.WithStreamingSTT(sm)))
	t.Cleanup(conv.Register(t.Context()))
	if !sm.WaitStreamUp(2 * time.Second) {
		t.Fatal("eager dial never came up")
	}

	feedConv(t, conv, 3)
	if err := conv.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	_, finals := partialsAndFinals(h)
	if len(finals) != 1 || finals[0].Text != "" {
		t.Fatalf("STTFinal = %+v, want exactly 1 with empty text", finals)
	}
	if got := len(batch.batches()); got != 0 {
		t.Errorf("batch recognizer called %d times for an empty commit, want 0 (empty is a valid final)", got)
	}
}

// TestConversation_PartialsNeverRoute is TDD step 10's new assertion: partials are
// a live-view signal only — the address detector routes exactly one AddressRouted
// per STTFinal, and never on a partial. The routed text is the final's, not any
// interim hypothesis.
func TestConversation_PartialsNeverRoute(t *testing.T) {
	h := voicetest.New(t)

	sttStage := orchestrator.NewSTT(h.Bus, &recordingRecognizer{})
	vadStage := orchestrator.NewVAD(h.Bus, &scriptedVAD{events: []vad.VADEventType{
		vad.VADSpeechStart, vad.VADSpeechContinue, vad.VADSpeechContinue, vad.VADSpeechEnd,
	}})
	stream := &scriptedStream{
		partials:  []string{"bart", "bart hel", "bart hello"},
		committed: stt.CommitResult{Transcript: stt.Transcript{Text: "bart hello"}},
	}
	sm := orchestrator.NewStreamManager(&multiStreamRecognizer{streams: []*scriptedStream{stream}})
	detector := orchestrator.NewAddressDetector(
		address.NewMatcher(address.Config{Language: "en"},
			address.Agent{Target: voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character", Name: "Bart"}}),
	)

	conv := mustConversation(orchestrator.NewConversation(h.Bus, vadStage, sttStage, nil,
		orchestrator.WithStreamingSTT(sm),
		orchestrator.WithDetector(detector)))
	t.Cleanup(conv.Register(t.Context()))
	if !sm.WaitStreamUp(2 * time.Second) {
		t.Fatal("eager dial never came up")
	}

	feedConv(t, conv, 5)
	if err := conv.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	partials, finals := partialsAndFinals(h)
	if len(partials) < 2 {
		t.Fatalf("expected several partials during speech, got %d", len(partials))
	}
	if len(finals) != 1 {
		t.Fatalf("%d STTFinal, want 1", len(finals))
	}
	var routes []voiceevent.AddressRouted
	for _, e := range h.Events() {
		if r, ok := e.(voiceevent.AddressRouted); ok {
			routes = append(routes, r)
		}
	}
	if len(routes) != 1 {
		t.Fatalf("%d AddressRouted, want exactly 1 (only the final routes; partials never do)", len(routes))
	}
	if routes[0].Text != "bart hello" {
		t.Errorf("AddressRouted.Text = %q, want the final %q (never an interim partial)", routes[0].Text, "bart hello")
	}
}

// TestConversation_StreamedUtterance_RecordsOneSTTRequest is TDD step 11: a
// streamed utterance records exactly one stt_request span (the commit round-trip),
// and the STTFinal carries the speech-end time the response-latency subscriber
// derives its headline span from.
func TestConversation_StreamedUtterance_RecordsOneSTTRequest(t *testing.T) {
	h := voicetest.New(t)

	sttStage := orchestrator.NewSTT(h.Bus, &recordingRecognizer{})
	vadStage := orchestrator.NewVAD(h.Bus, &scriptedVAD{events: []vad.VADEventType{
		vad.VADSpeechStart, vad.VADSpeechContinue, vad.VADSpeechEnd,
	}})
	stream := &scriptedStream{committed: stt.CommitResult{Transcript: stt.Transcript{Text: "hi"}}}
	spy := &metricSpy{}
	sm := orchestrator.NewStreamManager(&multiStreamRecognizer{streams: []*scriptedStream{stream}},
		orchestrator.WithStreamMetrics(spy, observe.ProviderElevenLabs))

	conv := mustConversation(orchestrator.NewConversation(h.Bus, vadStage, sttStage, nil,
		orchestrator.WithStreamingSTT(sm)))
	t.Cleanup(conv.Register(t.Context()))
	if !sm.WaitStreamUp(2 * time.Second) {
		t.Fatal("eager dial never came up")
	}

	var speechEnd time.Time
	voiceevent.On(h.Bus, func(e voiceevent.VADSpeechEnd) { speechEnd = e.At })

	feedConv(t, conv, 4)
	if err := conv.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if got := spy.requests(); got != 1 {
		t.Errorf("stt_request recorded %d times, want exactly 1 per streamed utterance", got)
	}
	_, finals := partialsAndFinals(h)
	if len(finals) != 1 {
		t.Fatalf("%d STTFinal, want 1", len(finals))
	}
	if !finals[0].SpeechEndAt.Equal(speechEnd) || finals[0].SpeechEndAt.IsZero() {
		t.Errorf("STTFinal.SpeechEndAt = %v, want the VAD speech-end %v (response-latency anchor)", finals[0].SpeechEndAt, speechEnd)
	}
}

// TestConversation_NilStreamingSTT_IsBatchParity pins the byte-for-byte default:
// WithStreamingSTT(nil) is zero behaviour change — no partials, and the STTFinal
// carries an empty UtteranceID exactly like the no-streaming path.
func TestConversation_NilStreamingSTT_IsBatchParity(t *testing.T) {
	h := voicetest.New(t)

	sttStage := orchestrator.NewSTT(h.Bus, &recordingRecognizer{})
	vadStage := orchestrator.NewVAD(h.Bus, &scriptedVAD{events: []vad.VADEventType{
		vad.VADSpeechStart, vad.VADSpeechContinue, vad.VADSpeechEnd,
	}})

	conv := mustConversation(orchestrator.NewConversation(h.Bus, vadStage, sttStage, nil,
		orchestrator.WithStreamingSTT(nil))) // nil is the no-streaming default
	t.Cleanup(conv.Register(t.Context()))

	feedConv(t, conv, 4)
	if err := conv.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	partials, finals := partialsAndFinals(h)
	if len(partials) != 0 {
		t.Errorf("nil streaming published %d partials, want 0", len(partials))
	}
	if len(finals) != 1 {
		t.Fatalf("%d STTFinal, want 1", len(finals))
	}
	if finals[0].UtteranceID != "" {
		t.Errorf("batch-path STTFinal.UtteranceID = %q, want empty (byte-for-byte default)", finals[0].UtteranceID)
	}
}
