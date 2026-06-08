package agent_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// fakeProvider is a deterministic [llm.Provider] for the loop tests: it records
// every [llm.Request] it is handed and replays a scripted text completion (or a
// preset error). Keyless — the Provider interface is the seam the orchestrator
// reply-path tests stub at one layer up (stt_test.go), mirrored here.
type fakeProvider struct {
	mu       sync.Mutex
	requests []llm.Request

	reply string // text streamed back, split into per-word EventText deltas
	err   error  // returned from Complete when non-nil
}

func (f *fakeProvider) Complete(_ context.Context, req llm.Request) (<-chan llm.StreamEvent, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()

	if f.err != nil {
		return nil, f.err
	}
	ch := make(chan llm.StreamEvent)
	go func() {
		defer close(ch)
		// Stream word-by-word so the loop's accumulation across multiple
		// EventText deltas is exercised, not just a single chunk.
		words := strings.Fields(f.reply)
		for i, w := range words {
			text := w
			if i < len(words)-1 {
				text += " "
			}
			ch <- llm.StreamEvent{Type: llm.EventText, Text: text}
		}
		ch <- llm.StreamEvent{Type: llm.EventDone, StopReason: "end_turn"}
	}()
	return ch, nil
}

func (f *fakeProvider) lastRequest(t *testing.T) llm.Request {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("provider was never called")
	}
	return f.requests[len(f.requests)-1]
}

func (f *fakeProvider) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// sentinelMarkup is the audio-markup instruction the stub Synthesizer returns,
// so the test can assert the loop folds [tts.Synthesizer.AudioMarkupPrompt] into
// the system prompt (the ADR-0022 requirement).
const sentinelMarkup = "MARKUP-INSTRUCTION-SENTINEL"

// stubSynth is a [tts.Synthesizer] that returns a fixed markup string; the loop
// never synthesizes audio, so Synthesize is a stub.
type stubSynth struct{}

func (stubSynth) Synthesize(context.Context, tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	ch := make(chan tts.AudioChunk)
	close(ch)
	return ch, nil
}
func (stubSynth) AudioMarkupPrompt(tts.Voice) string { return sentinelMarkup }

func testVoice() tts.Voice {
	return tts.Voice{ProviderID: "elevenlabs", VoiceID: "v1", Name: "Bart"}
}

func routed(agentID, text string) voiceevent.AddressRouted {
	return voiceevent.AddressRouted{
		At:     time.Now(),
		Text:   text,
		Target: voiceevent.AddressTarget{AgentID: agentID, AgentRole: "character", Name: "Bart"},
	}
}

// TestReply_NotAddressed_ReturnsNilAndSkipsProvider pins the AgentID gate: a
// route targeting a different Agent yields no reply and never calls the LLM, so
// many Agents' loops can share one bus (the Ensemble Turn building block).
func TestReply_NotAddressed_ReturnsNilAndSkipsProvider(t *testing.T) {
	prov := &fakeProvider{reply: "should not be produced"}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Provider:    prov,
		Synthesizer: stubSynth{},
	})

	got := r.Reply()(routed("someone-else", "Hello there."))
	if got != nil {
		t.Errorf("reply for unaddressed route = %+v, want nil", got)
	}
	if prov.callCount() != 0 {
		t.Errorf("provider called %d times for unaddressed route, want 0", prov.callCount())
	}
}

// TestReply_Addressed_AssemblesSystemPromptAndAccumulatesText pins the core of
// the Agent loop: a route to this Agent assembles a system prompt carrying the
// Persona AND the audio-markup instruction (ordered Persona-then-markup), the
// utterance becomes the user message, the streamed deltas accumulate into one
// trimmed reply, and the reply carries the Agent's Voice.
func TestReply_Addressed_AssemblesSystemPromptAndAccumulatesText(t *testing.T) {
	prov := &fakeProvider{reply: "Welcome to the Prancing Pony, traveler."}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart, the innkeeper.", Voice: testVoice()},
		Provider:    prov,
		Synthesizer: stubSynth{},
	})

	got := r.Reply()(routed("bart", "Hello, innkeeper."))
	if len(got) != 1 {
		t.Fatalf("got %d replies, want 1", len(got))
	}
	if got[0].Sentence != "Welcome to the Prancing Pony, traveler." {
		t.Errorf("reply sentence = %q, want accumulated streamed text", got[0].Sentence)
	}
	// tts.Voice carries json.RawMessage, so compare the identifying fields.
	if v := got[0].Voice; v.ProviderID != "elevenlabs" || v.VoiceID != "v1" {
		t.Errorf("reply voice = %+v, want the Persona's voice", v)
	}

	req := prov.lastRequest(t)
	if len(req.Messages) < 2 {
		t.Fatalf("request has %d messages, want >=2 (system + user)", len(req.Messages))
	}
	sys := req.Messages[0]
	if sys.Role != llm.RoleSystem {
		t.Fatalf("first message role = %q, want system", sys.Role)
	}
	if !strings.Contains(sys.Text, "You are Bart, the innkeeper.") {
		t.Errorf("system prompt missing Persona text: %q", sys.Text)
	}
	if !strings.Contains(sys.Text, sentinelMarkup) {
		t.Errorf("system prompt missing audio-markup instruction (ADR-0022): %q", sys.Text)
	}
	if strings.Index(sys.Text, "You are Bart") > strings.Index(sys.Text, sentinelMarkup) {
		t.Errorf("system prompt order: Persona must precede markup: %q", sys.Text)
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role != llm.RoleUser || last.Text != "Hello, innkeeper." {
		t.Errorf("last message = {%q, %q}, want user utterance", last.Role, last.Text)
	}
}

// TestReply_MultiTurn_CarriesHistory pins that the recent Transcript lives in
// the loop: a second turn's request includes the first turn's user message and
// the first assistant reply, because a [orchestrator.ReplyFunc] sees only the
// current utterance.
func TestReply_MultiTurn_CarriesHistory(t *testing.T) {
	prov := &fakeProvider{reply: "Aye."}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Provider:    prov,
		Synthesizer: stubSynth{},
	})
	reply := r.Reply()

	reply(routed("bart", "Do you have rooms?"))
	reply(routed("bart", "And a meal?"))

	req := prov.lastRequest(t)
	// Expect: system, user "Do you have rooms?", assistant "Aye.", user "And a meal?".
	var roles []llm.Role
	var texts []string
	for _, m := range req.Messages {
		roles = append(roles, m.Role)
		texts = append(texts, m.Text)
	}
	wantContains := []string{"Do you have rooms?", "Aye.", "And a meal?"}
	joined := strings.Join(texts, "|")
	for _, w := range wantContains {
		if !strings.Contains(joined, w) {
			t.Errorf("second-turn request missing history element %q; messages: %v", w, texts)
		}
	}
	// The first reply must have been recorded as an assistant turn before turn 2.
	var sawAssistant bool
	for _, role := range roles {
		if role == llm.RoleAssistant {
			sawAssistant = true
		}
	}
	if !sawAssistant {
		t.Errorf("second-turn request has no assistant turn; roles: %v", roles)
	}
}

// TestReply_HistoryTurns_BoundsTranscript pins the Hot Context bound: with
// HistoryTurns set, only the most recent turns are carried; the oldest user
// utterance drops out once the bound is exceeded.
func TestReply_HistoryTurns_BoundsTranscript(t *testing.T) {
	prov := &fakeProvider{reply: "ok"}
	r := agent.NewReplier(agent.Config{
		Persona:      agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Provider:     prov,
		Synthesizer:  stubSynth{},
		HistoryTurns: 2, // keep at most 2 messages of recent Transcript
	})
	reply := r.Reply()

	reply(routed("bart", "first-utterance"))
	reply(routed("bart", "second-utterance"))
	reply(routed("bart", "third-utterance"))

	req := prov.lastRequest(t)
	var joined string
	for _, m := range req.Messages {
		joined += m.Text + "|"
	}
	if strings.Contains(joined, "first-utterance") {
		t.Errorf("oldest utterance should have been dropped by HistoryTurns bound; messages: %q", joined)
	}
	if !strings.Contains(joined, "third-utterance") {
		t.Errorf("most recent utterance must be present; messages: %q", joined)
	}
}

// TestReply_ProviderError_ReturnsNilAndReportsError pins the no-error seam: a
// [orchestrator.ReplyFunc] cannot return an error, so a failed completion yields
// no reply and is surfaced via OnError.
func TestReply_ProviderError_ReturnsNilAndReportsError(t *testing.T) {
	wantErr := errors.New("provider boom")
	var gotErr error
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Provider:    &fakeProvider{err: wantErr},
		Synthesizer: stubSynth{},
		OnError:     func(err error) { gotErr = err },
	})

	got := r.Reply()(routed("bart", "Hello."))
	if got != nil {
		t.Errorf("reply on provider error = %+v, want nil", got)
	}
	if !errors.Is(gotErr, wantErr) {
		t.Errorf("OnError got %v, want %v", gotErr, wantErr)
	}
}

// TestReply_EmptyCompletion_ReturnsNil pins that an empty completion says
// nothing rather than dispatching an empty sentence (and does not panic).
func TestReply_EmptyCompletion_ReturnsNil(t *testing.T) {
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Provider:    &fakeProvider{reply: ""},
		Synthesizer: stubSynth{},
	})
	if got := r.Reply()(routed("bart", "Hello.")); got != nil {
		t.Errorf("reply for empty completion = %+v, want nil", got)
	}
}

// fakeStreamEngine is a [agent.StreamingEngine] that streams a scripted reply
// delta-by-delta through onText, honouring ctx cancellation between deltas so a
// barge-in can be simulated mid-stream. Its Generate is the non-streaming
// fallback (the whole reply at once).
type fakeStreamEngine struct {
	deltas []string // streamed in order through onText
	// pause, if non-nil, is closed by the test to release the stream after the
	// first delta — so a barge-in cancel can land deterministically mid-stream.
	pauseAfter int
	pause      chan struct{}
}

func (e *fakeStreamEngine) Generate(_ context.Context, _ []llm.Message) (string, error) {
	return strings.TrimSpace(strings.Join(e.deltas, "")), nil
}

func (e *fakeStreamEngine) GenerateStream(ctx context.Context, _ []llm.Message, onText func(string) error) (string, error) {
	var full strings.Builder
	for i, d := range e.deltas {
		if err := ctx.Err(); err != nil {
			return full.String(), err
		}
		full.WriteString(d)
		if err := onText(d); err != nil {
			return full.String(), err
		}
		if e.pause != nil && i == e.pauseAfter {
			select {
			case <-e.pause:
			case <-ctx.Done():
				return full.String(), ctx.Err()
			}
		}
	}
	return full.String(), nil
}

// streamReplier builds a Replier over a streaming engine, with a recording
// dispatch that captures the sentences in order.
func streamReplier(t *testing.T, eng agent.Engine) *agent.Replier {
	t.Helper()
	return agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Engine:      eng,
		Synthesizer: stubSynth{},
	})
}

// TestReplyStream_DispatchesSentencesInOrder pins the B1 win: a streamed reply is
// dispatched sentence-by-sentence, in order, as the deltas arrive — not as one
// blob after the whole completion.
func TestReplyStream_DispatchesSentencesInOrder(t *testing.T) {
	eng := &fakeStreamEngine{deltas: []string{"First one. ", "Second two! ", "Third three?"}}
	r := streamReplier(t, eng)

	var got []string
	err := r.ReplyStream()(context.Background(), routed("bart", "go"), func(rep orchestrator.Reply) error {
		got = append(got, rep.Sentence)
		return nil
	})
	if err != nil {
		t.Fatalf("ReplyStream: %v", err)
	}
	want := []string{"First one.", "Second two!", "Third three?"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("dispatched %q, want %q", got, want)
	}
}

// TestReplyStream_NotAddressed_NoDispatch pins the AgentID gate on the streaming
// path (mirrors the batch gate).
func TestReplyStream_NotAddressed_NoDispatch(t *testing.T) {
	eng := &fakeStreamEngine{deltas: []string{"Should not speak."}}
	r := streamReplier(t, eng)
	var dispatched int
	err := r.ReplyStream()(context.Background(), routed("someone-else", "hi"), func(orchestrator.Reply) error {
		dispatched++
		return nil
	})
	if err != nil || dispatched != 0 {
		t.Errorf("not-addressed stream: dispatched=%d err=%v, want 0/nil", dispatched, err)
	}
}

// TestReplyStream_BargeMidStream_CommitsOnlySpoken pins the ADR-0012 deliver-
// then-commit rule under barge-in: when the turn is cancelled after the first
// sentence is spoken, only that sentence is committed to history — not the
// untruncated completion — and the next turn's user message is appended AFTER
// it, so history reads user1 → assistant1(partial) → user2 in order.
func TestReplyStream_BargeMidStream_CommitsOnlySpoken(t *testing.T) {
	eng := &fakeStreamEngine{
		deltas:     []string{"Aye. ", "Two rooms. ", "Anything else?"},
		pauseAfter: 0, // pause after the first delta so we can cancel mid-stream
		pause:      make(chan struct{}),
	}
	r := streamReplier(t, eng)

	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var spoken []string
	spokenLen := func() int { mu.Lock(); defer mu.Unlock(); return len(spoken) }
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = r.ReplyStream()(ctx, routed("bart", "rooms?"), func(rep orchestrator.Reply) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			mu.Lock()
			spoken = append(spoken, rep.Sentence)
			mu.Unlock()
			return nil
		})
	}()

	// Wait until the first sentence dispatched, then barge-in.
	deadline := time.Now().Add(2 * time.Second)
	for spokenLen() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	close(eng.pause) // release the paused stream so the goroutine unwinds
	<-done

	if len(spoken) != 1 || spoken[0] != "Aye." {
		t.Fatalf("spoken before barge = %q, want exactly [\"Aye.\"]", spoken)
	}

	// A follow-up turn appends its user message; history must remain ordered.
	if err := r.ReplyStream()(context.Background(), routed("bart", "second turn"), func(orchestrator.Reply) error { return nil }); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	hist := r.HistorySnapshot()
	wantSeq := []struct{ role, contains string }{
		{"user", "rooms?"},
		{"assistant", "Aye."},
		{"user", "second turn"},
	}
	if len(hist) < len(wantSeq) {
		t.Fatalf("history has %d messages, want >= %d: %+v", len(hist), len(wantSeq), hist)
	}
	for i, w := range wantSeq {
		if string(hist[i].Role) != w.role || !strings.Contains(hist[i].Text, w.contains) {
			t.Errorf("history[%d] = {%s %q}, want role %s containing %q", i, hist[i].Role, hist[i].Text, w.role, w.contains)
		}
	}
	// The untruncated 2nd/3rd sentences must NOT be in the committed assistant turn.
	if strings.Contains(hist[1].Text, "Two rooms") || strings.Contains(hist[1].Text, "Anything else") {
		t.Errorf("committed assistant turn = %q, want only the spoken first sentence", hist[1].Text)
	}
}

// delayStreamEngine streams sentences with a fixed delay BEFORE each one,
// modelling the LLM taking perSentence to produce each sentence. Its Generate
// (the batch path) blocks for ALL sentences before returning, so a batch reply
// cannot dispatch until the whole completion is ready — exactly the B1 problem.
type delayStreamEngine struct {
	sentences   []string
	perSentence time.Duration
}

func (e *delayStreamEngine) Generate(ctx context.Context, _ []llm.Message) (string, error) {
	for range e.sentences {
		select {
		case <-time.After(e.perSentence):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return strings.Join(e.sentences, " "), nil
}

func (e *delayStreamEngine) GenerateStream(ctx context.Context, _ []llm.Message, onText func(string) error) (string, error) {
	var full strings.Builder
	for _, s := range e.sentences {
		select {
		case <-time.After(e.perSentence):
		case <-ctx.Done():
			return full.String(), ctx.Err()
		}
		chunk := s + " "
		full.WriteString(chunk)
		if err := onText(chunk); err != nil {
			return full.String(), err
		}
	}
	return full.String(), nil
}

// TestReplyStream_FirstAudioBeatsBatch is the B1 before/after number (preliminary,
// off the dispatch boundary the A3 FirstAudio hook stamps in production): with a
// model that takes ~perSentence per sentence, the STREAMING path dispatches the
// first sentence after ~1×perSentence, while the BATCH path cannot dispatch
// anything until the whole completion (~N×perSentence) is ready. The win is the
// (N−1)×perSentence the user no longer waits before hearing the first word.
func TestReplyStream_FirstAudioBeatsBatch(t *testing.T) {
	const per = 40 * time.Millisecond
	sentences := []string{"Aye, traveler.", "Two rooms upstairs.", "Anything else for ye?"}

	// Streaming: time to the FIRST dispatch.
	streamEng := &delayStreamEngine{sentences: sentences, perSentence: per}
	rs := streamReplier(t, streamEng)
	startS := time.Now()
	var firstStream time.Duration
	var once sync.Once
	_ = rs.ReplyStream()(context.Background(), routed("bart", "rooms?"), func(orchestrator.Reply) error {
		once.Do(func() { firstStream = time.Since(startS) })
		return nil
	})

	// Batch: time until the (single, full-text) Reply is returned and dispatchable.
	batchEng := &delayStreamEngine{sentences: sentences, perSentence: per}
	rb := streamReplier(t, batchEng)
	startB := time.Now()
	_ = rb.Reply()(routed("bart", "rooms?")) // batch path blocks for the whole completion
	firstBatch := time.Since(startB)

	t.Logf("B1 first-audio (preliminary): streaming first dispatch=%v, batch first dispatch=%v, saved≈%v",
		firstStream, firstBatch, firstBatch-firstStream)

	// Streaming must reach the first sentence well before the whole completion.
	if firstStream >= firstBatch {
		t.Errorf("streaming first dispatch %v not earlier than batch %v — B1 gave no win", firstStream, firstBatch)
	}
	// First streamed dispatch should land near one sentence's time, not three.
	if firstStream > 2*per {
		t.Errorf("streaming first dispatch %v > 2×perSentence (%v); expected ≈1×", firstStream, 2*per)
	}
}

// Compile-time assertion: the loop produces a value usable by the orchestrator
// reply seam without an adapter.
var _ orchestrator.ReplyFunc = (&agent.Replier{}).Reply()
