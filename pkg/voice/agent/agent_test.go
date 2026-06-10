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

	reply    string // text streamed back, split into per-word EventText deltas
	err      error  // returned from Complete when non-nil
	truncate bool   // close the stream without EventDone (mid-stream failure)
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
		if !f.truncate {
			ch <- llm.StreamEvent{Type: llm.EventDone, StopReason: "end_turn"}
		}
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

	got := r.Reply()(t.Context(), routed("someone-else", "Hello there."))
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

	got := r.Reply()(t.Context(), routed("bart", "Hello, innkeeper."))
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

	reply(t.Context(), routed("bart", "Do you have rooms?"))
	reply(t.Context(), routed("bart", "And a meal?"))

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

	reply(t.Context(), routed("bart", "first-utterance"))
	reply(t.Context(), routed("bart", "second-utterance"))
	reply(t.Context(), routed("bart", "third-utterance"))

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

	got := r.Reply()(t.Context(), routed("bart", "Hello."))
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
	if got := r.Reply()(t.Context(), routed("bart", "Hello.")); got != nil {
		t.Errorf("reply for empty completion = %+v, want nil", got)
	}
}

// Compile-time assertion: the loop produces a value usable by the orchestrator
// reply seam without an adapter.
var _ orchestrator.ReplyFunc = (&agent.Replier{}).Reply()

// TestReply_TruncatedStream_ReturnsNilAndReportsError pins the truncation
// contract on the default engine: a stream that closes without [llm.EventDone]
// (mid-stream network failure) must not be spoken as a complete reply — the
// turn yields nothing and the failure surfaces via OnError.
func TestReply_TruncatedStream_ReturnsNilAndReportsError(t *testing.T) {
	var gotErr error
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Provider:    &fakeProvider{reply: "Half a sentence that never", truncate: true},
		Synthesizer: stubSynth{},
		OnError:     func(err error) { gotErr = err },
	})

	got := r.Reply()(t.Context(), routed("bart", "Hello."))
	if got != nil {
		t.Errorf("reply for truncated stream = %+v, want nil", got)
	}
	if gotErr == nil || !strings.Contains(gotErr.Error(), "without done") {
		t.Errorf("OnError got %v, want a truncation error", gotErr)
	}
}

// ctxCaptureEngine records the ctx the Replier hands the Engine, so tests can
// pin the per-turn deadline and ctx propagation.
type ctxCaptureEngine struct {
	ctx   context.Context
	reply string
}

func (e *ctxCaptureEngine) Generate(ctx context.Context, _ []llm.Message) (string, error) {
	e.ctx = ctx
	return e.reply, nil
}

// TestReply_TurnTimeout_AppliesDeadlineAndPropagatesCtx pins the hung-provider
// guard: the Engine's ctx must descend from the caller's turn ctx (so barge-in
// cancellation reaches the LLM call) and carry the TurnTimeout deadline (so a
// hung provider can never hold the turn open forever).
func TestReply_TurnTimeout_AppliesDeadlineAndPropagatesCtx(t *testing.T) {
	eng := &ctxCaptureEngine{reply: "Aye."}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Engine:      eng,
		Synthesizer: stubSynth{},
	})

	type ctxKey struct{}
	parent := context.WithValue(t.Context(), ctxKey{}, "turn")
	if got := r.Reply()(parent, routed("bart", "Hello.")); len(got) != 1 {
		t.Fatalf("reply = %+v, want one sentence", got)
	}

	if eng.ctx.Value(ctxKey{}) != "turn" {
		t.Error("engine ctx does not descend from the caller's turn ctx")
	}
	deadline, ok := eng.ctx.Deadline()
	if !ok {
		t.Fatal("engine ctx has no deadline; a hung provider would block the turn forever")
	}
	if remaining := time.Until(deadline); remaining > agent.DefaultTurnTimeout {
		t.Errorf("deadline %v out past DefaultTurnTimeout %v", remaining, agent.DefaultTurnTimeout)
	}
}

// TestReply_TurnTimeoutNegative_DisablesDeadline pins the documented escape
// hatch: TurnTimeout < 0 leaves the turn bounded only by the caller's ctx.
func TestReply_TurnTimeoutNegative_DisablesDeadline(t *testing.T) {
	eng := &ctxCaptureEngine{reply: "Aye."}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Engine:      eng,
		Synthesizer: stubSynth{},
		TurnTimeout: -1,
	})
	if got := r.Reply()(t.Context(), routed("bart", "Hello.")); len(got) != 1 {
		t.Fatalf("reply = %+v, want one sentence", got)
	}
	if _, ok := eng.ctx.Deadline(); ok {
		t.Error("engine ctx has a deadline despite TurnTimeout < 0")
	}
}
