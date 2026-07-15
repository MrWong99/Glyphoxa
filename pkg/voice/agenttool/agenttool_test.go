package agenttool_test

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	"github.com/MrWong99/Glyphoxa/pkg/tool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/agenttool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/providererr"
	"github.com/MrWong99/Glyphoxa/pkg/voice/retry"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// stubSynth is a [tts.Synthesizer] supplying a fixed audio-markup instruction;
// the Replier needs one to assemble the system prompt. Synthesize is unused.
type stubSynth struct{}

func (stubSynth) Synthesize(context.Context, tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	ch := make(chan tts.AudioChunk)
	close(ch)
	return ch, nil
}
func (stubSynth) AudioMarkupPrompt(tts.Voice) string { return "Speak plainly." }

// addressed builds an AddressRouted event targeting the given Agent.
func addressed(agentID, text string) voiceevent.AddressRouted {
	return voiceevent.AddressRouted{
		Text:   text,
		Target: voiceevent.AddressTarget{AgentID: agentID, AgentRole: "character", Name: agentID},
	}
}

// Compile-time assertion: the bridge Engine satisfies the agent seam.
var _ agent.Engine = (*agenttool.Engine)(nil)

// scriptedProvider is a deterministic streaming [llm.Provider] for the bridge
// tests. Each call to Complete pops the next scripted step and streams it as
// EventText / EventToolCall deltas followed by EventDone. It records every
// Request so the test can assert the loop fed tool results back. Keyless — no
// live API.
type scriptedProvider struct {
	mu    sync.Mutex
	steps []step
	reqs  []llm.Request
	next  int
}

// step is one scripted generation: some text and/or some tool calls.
type step struct {
	text  string
	calls []llm.ToolCall
	stop  string
}

func (p *scriptedProvider) Complete(_ context.Context, req llm.Request) (<-chan llm.StreamEvent, error) {
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	var s step
	if p.next < len(p.steps) {
		s = p.steps[p.next]
		p.next++
	} else {
		s = step{stop: "end_turn"}
	}
	p.mu.Unlock()

	ch := make(chan llm.StreamEvent)
	go func() {
		defer close(ch)
		for _, w := range strings.Fields(s.text) {
			ch <- llm.StreamEvent{Type: llm.EventText, Text: w + " "}
		}
		for _, c := range s.calls {
			ch <- llm.StreamEvent{Type: llm.EventToolCall, ToolCall: c}
		}
		stop := s.stop
		if stop == "" {
			stop = "end_turn"
		}
		ch <- llm.StreamEvent{Type: llm.EventDone, StopReason: stop}
	}()
	return ch, nil
}

func (p *scriptedProvider) requests() []llm.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.Request(nil), p.reqs...)
}

// diceGrants builds a GrantSet granting the real dice Tool, with a seeded rng
// so the roll is deterministic.
func diceGrants(t *testing.T) *tool.GrantSet {
	t.Helper()
	reg := tool.NewRegistry()
	reg.MustRegister(tool.NewDiceWithRand(rand.New(rand.NewPCG(1, 2))))
	return tool.NewGrantSet(reg, tool.Grant{ToolName: "dice"})
}

// TestEngine_ToolUseRoundTrip_RunsDiceAndReturnsFinalText is the integration
// pin (ADR-0021: tool-call routing is the most important thing to verify): the
// model asks for the dice Tool, the bridge runs it via tool.Loop, feeds the
// result back, and the model's second generation returns the final spoken text.
func TestEngine_ToolUseRoundTrip_RunsDiceAndReturnsFinalText(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		// Round 1: the model requests a d20 roll.
		{calls: []llm.ToolCall{{ID: "toolu_1", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":20}`)}}, stop: "tool_use"},
		// Round 2: with the result in context, it answers.
		{text: "You rolled well, traveler.", stop: "end_turn"},
	}}

	eng := agenttool.NewEngine(prov, diceGrants(t), "", "claude-test", 256, 0)
	got, err := eng.Generate(context.Background(), []llm.Message{
		{Role: llm.RoleSystem, Text: "You are Bart."},
		{Role: llm.RoleUser, Text: "Roll a d20 for me."},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// The engine returns text verbatim; trimming is the Replier's job.
	if strings.TrimSpace(got) != "You rolled well, traveler." {
		t.Errorf("final text = %q, want the second-round answer", got)
	}

	// The loop must have made two generations, and the second must carry the
	// tool result fed back as a RoleTool message.
	reqs := prov.requests()
	if len(reqs) != 2 {
		t.Fatalf("provider called %d times, want 2 (request → tool result → answer)", len(reqs))
	}
	// First request advertised the dice tool.
	if len(reqs[0].Tools) != 1 || reqs[0].Tools[0].Name != "dice" {
		t.Errorf("first request tools = %+v, want one dice decl", reqs[0].Tools)
	}
	// Second request carries the assistant tool_use turn and a RoleTool result.
	var sawAssistantCall, sawToolResult bool
	for _, m := range reqs[1].Messages {
		if m.Role == llm.RoleAssistant && len(m.ToolCalls) > 0 && m.ToolCalls[0].Name == "dice" {
			sawAssistantCall = true
		}
		if m.Role == llm.RoleTool {
			for _, tr := range m.ToolResults {
				if tr.CallID == "toolu_1" && tr.Content != "" && !tr.IsError {
					sawToolResult = true
				}
			}
		}
	}
	if !sawAssistantCall {
		t.Error("second request missing the assistant tool_use turn")
	}
	if !sawToolResult {
		t.Error("second request missing the fed-back dice tool result")
	}
}

// llmRound is one captured LLMRound span: the labels the metric carries.
type llmRound struct {
	provider    observe.Provider
	roundIndex  int
	hadToolCall bool
}

// recordingStage is a [observe.StageRecorder] that captures LLMRound and
// provider-call invocations so the A3 instrumentation can be asserted without a
// Prometheus backend. Embeds observe.Discard so the unexercised methods are
// no-ops. Safe for the loop's single-goroutine use plus the race detector.
type recordingStage struct {
	observe.Discard
	mu           sync.Mutex
	rounds       []llmRound
	callsOK      int
	callsErr     int
	turns        []observe.Provider      // one LLMTurn span per Engine.Generate/GenerateStream
	outcomes     []observe.Outcome       // every ProviderCall outcome, in order
	providerErrs int                     // ProviderError invocations
	tokens       []llmTokensRec          // one LLMTokens per drained Complete (reported or estimate)
	malformed    []observe.MalformedPath // one per MalformedToolGen (#398)
}

func (r *recordingStage) MalformedToolGen(_ observe.Provider, path observe.MalformedPath) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.malformed = append(r.malformed, path)
}

func (r *recordingStage) malformedPaths() []observe.MalformedPath {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]observe.MalformedPath(nil), r.malformed...)
}

func (r *recordingStage) LLMRound(p observe.Provider, idx int, hadToolCall bool, _ time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rounds = append(r.rounds, llmRound{provider: p, roundIndex: idx, hadToolCall: hadToolCall})
}

func (r *recordingStage) LLMTurn(p observe.Provider, _ time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.turns = append(r.turns, p)
}

// llmTokensRec is one captured LLMTokens call: provider + model + the two counts.
type llmTokensRec struct {
	provider observe.Provider
	model    string
	in, out  int
}

func (r *recordingStage) LLMTokens(p observe.Provider, model string, in, out int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokens = append(r.tokens, llmTokensRec{provider: p, model: model, in: in, out: out})
}

func (r *recordingStage) tokenSpans() []llmTokensRec {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]llmTokensRec(nil), r.tokens...)
}

func (r *recordingStage) turnSpans() []observe.Provider {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]observe.Provider(nil), r.turns...)
}

func (r *recordingStage) ProviderCall(_ observe.Stage, _ observe.Provider, outcome observe.Outcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.outcomes = append(r.outcomes, outcome)
	if outcome == observe.OutcomeOK {
		r.callsOK++
	} else {
		r.callsErr++
	}
}

func (r *recordingStage) ProviderError(observe.Stage, observe.Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providerErrs++
}

func (r *recordingStage) snapshot() ([]llmRound, int, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]llmRound(nil), r.rounds...), r.callsOK, r.callsErr
}

func (r *recordingStage) providerCalls() ([]observe.Outcome, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]observe.Outcome(nil), r.outcomes...), r.providerErrs
}

// TestEngine_LLMRoundSpans_IndexAndToolCallPerRound pins the A3 per-round
// instrumentation: each Provider.Complete inside the tool loop emits one
// LLMRound span with a 0-based round_index and the had_tool_call flag that
// separates H1 thinking from H2 extra rounds. The dice round-trip is exactly
// two rounds: round 0 requests the tool (had_tool_call=true), round 1 answers
// (had_tool_call=false).
func TestEngine_LLMRoundSpans_IndexAndToolCallPerRound(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		{calls: []llm.ToolCall{{ID: "toolu_1", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":20}`)}}, stop: "tool_use"},
		{text: "You rolled well, traveler.", stop: "end_turn"},
	}}
	rec := &recordingStage{}
	eng := agenttool.NewEngine(prov, diceGrants(t), "", "claude-test", 256, 0,
		agenttool.WithMetrics(rec, observe.ProviderAnthropic))

	if _, err := eng.Generate(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Text: "Roll a d20 for me."},
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	rounds, callsOK, callsErr := rec.snapshot()
	if len(rounds) != 2 {
		t.Fatalf("recorded %d LLMRound spans, want 2 (request + answer)", len(rounds))
	}
	if rounds[0].roundIndex != 0 || !rounds[0].hadToolCall {
		t.Errorf("round 0 = %+v, want index 0 with had_tool_call=true", rounds[0])
	}
	if rounds[1].roundIndex != 1 || rounds[1].hadToolCall {
		t.Errorf("round 1 = %+v, want index 1 with had_tool_call=false", rounds[1])
	}
	if rounds[0].provider != observe.ProviderAnthropic {
		t.Errorf("round provider = %q, want the injected label", rounds[0].provider)
	}
	if callsOK != 2 || callsErr != 0 {
		t.Errorf("provider calls ok=%d err=%d, want 2/0", callsOK, callsErr)
	}
}

// TestEngine_LLMRoundSpans_IndexResetsPerTurn pins that round_index is scoped to
// one turn: a second Generate on the same Engine starts again at 0, never
// continuing the previous turn's count (the per-turn ctx counter, not a shared
// adapter field — which would bleed across the concurrent turns barge-in
// allows).
func TestEngine_LLMRoundSpans_IndexResetsPerTurn(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		{text: "First answer.", stop: "end_turn"},
		{text: "Second answer.", stop: "end_turn"},
	}}
	rec := &recordingStage{}
	eng := agenttool.NewEngine(prov, tool.NewGrantSet(tool.NewRegistry()), "", "claude-test", 256, 0,
		agenttool.WithMetrics(rec, observe.ProviderGemini))

	for i := 0; i < 2; i++ {
		if _, err := eng.Generate(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "Hi."}}); err != nil {
			t.Fatalf("Generate %d: %v", i, err)
		}
	}

	rounds, _, _ := rec.snapshot()
	if len(rounds) != 2 {
		t.Fatalf("recorded %d spans, want 2 (one per turn)", len(rounds))
	}
	for i, r := range rounds {
		if r.roundIndex != 0 {
			t.Errorf("turn %d round_index = %d, want 0 (per-turn reset)", i, r.roundIndex)
		}
	}
}

// flakyStartProvider start-errors its first errsBeforeOK Complete calls (each the
// pinned err) then streams the answer, counting calls so a retry test can prove
// the loop re-drove the LLM start exactly as expected. Only the START is exercised
// — the retry contract is start-errors-only (#124, ADR-0044).
type flakyStartProvider struct {
	err          error
	errsBeforeOK int
	answer       string
	calls        int
}

func (p *flakyStartProvider) Complete(context.Context, llm.Request) (<-chan llm.StreamEvent, error) {
	p.calls++
	if p.calls <= p.errsBeforeOK {
		return nil, p.err
	}
	ch := make(chan llm.StreamEvent)
	go func() {
		defer close(ch)
		for _, w := range strings.Fields(p.answer) {
			ch <- llm.StreamEvent{Type: llm.EventText, Text: w + " "}
		}
		ch <- llm.StreamEvent{Type: llm.EventDone, StopReason: "end_turn"}
	}()
	return ch, nil
}

// instantAgentRetry is the deterministic retry policy for the bridge tests: the
// backoff is a no-op sleep with fixed jitter, so a retry never sleeps wall-clock
// (ADR-0021). Defaults (3 attempts) otherwise stand.
func instantAgentRetry() retry.Policy {
	return retry.Policy{
		Sleep: func(context.Context, time.Duration) error { return nil },
		Rand:  func() float64 { return 0 },
	}
}

// TestEngine_Generate_RetriesTransientStartError pins the LLM AC: a Complete that
// start-errors one 429 then succeeds returns the full answer, and metrics record
// the FINAL outcome only — one LLMRound span and one provider_call(ok), never one
// per attempt (ADR-0044). No provider_error, because the retry recovered.
func TestEngine_Generate_RetriesTransientStartError(t *testing.T) {
	prov := &flakyStartProvider{
		err:          &providererr.HTTPError{Op: "groq.Complete", StatusCode: 429, Status: "Too Many Requests", Body: "slow"},
		errsBeforeOK: 1,
		answer:       "Aye, traveler.",
	}
	rec := &recordingStage{}
	eng := agenttool.NewEngine(prov, tool.NewGrantSet(tool.NewRegistry()), "", "m", 256, 0,
		agenttool.WithMetrics(rec, observe.ProviderGroq),
		agenttool.WithRetry(instantAgentRetry()))

	got, err := eng.Generate(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "Hi."}})
	if err != nil {
		t.Fatalf("Generate after one transient 429: %v", err)
	}
	if strings.TrimSpace(got) != "Aye, traveler." {
		t.Errorf("final text = %q, want the answer after the retry", got)
	}
	if prov.calls != 2 {
		t.Errorf("provider Complete calls = %d, want 2 (one 429 retried once)", prov.calls)
	}

	rounds, callsOK, callsErr := rec.snapshot()
	if len(rounds) != 1 {
		t.Errorf("LLMRound spans = %d, want 1 (final round only, not one per attempt)", len(rounds))
	}
	if callsOK != 1 || callsErr != 0 {
		t.Errorf("provider calls ok=%d err=%d, want 1/0 (final outcome only)", callsOK, callsErr)
	}
	if _, provErrs := rec.providerCalls(); provErrs != 0 {
		t.Errorf("provider_errors = %d, want 0 (the retry recovered)", provErrs)
	}
}

// TestEngine_Generate_NonRetryableStartErrorFailsFast pins that a 400 (bad
// request / auth) start-error fails on the first attempt with no retry and no
// sleep, and is recorded as a single provider fault.
func TestEngine_Generate_NonRetryableStartErrorFailsFast(t *testing.T) {
	prov := &flakyStartProvider{
		err:          &providererr.HTTPError{Op: "groq.Complete", StatusCode: 400, Status: "Bad Request", Body: "nope"},
		errsBeforeOK: 99,
	}
	rec := &recordingStage{}
	p := instantAgentRetry()
	p.Sleep = func(context.Context, time.Duration) error {
		t.Error("a non-retryable 400 must not sleep")
		return nil
	}
	eng := agenttool.NewEngine(prov, tool.NewGrantSet(tool.NewRegistry()), "", "m", 256, 0,
		agenttool.WithMetrics(rec, observe.ProviderGroq),
		agenttool.WithRetry(p))

	if _, err := eng.Generate(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "Hi."}}); err == nil {
		t.Fatal("Generate returned nil on a 400; want the bad-request error")
	}
	if prov.calls != 1 {
		t.Errorf("provider Complete calls = %d, want 1 (a 400 is not retried)", prov.calls)
	}

	_, callsOK, callsErr := rec.snapshot()
	if callsOK != 0 || callsErr != 1 {
		t.Errorf("provider calls ok=%d err=%d, want 0/1", callsOK, callsErr)
	}
	if _, provErrs := rec.providerCalls(); provErrs != 1 {
		t.Errorf("provider_errors = %d, want 1 (a 400 start error is a fault)", provErrs)
	}
}

// TestEngine_LLMTurn_OneSpanPerTurnAcrossRounds pins the #125 llm_turn wiring:
// the Engine records exactly ONE LLMTurn span per Generate — the full agenttool
// loop, spanning both rounds of a dice round-trip — labelled with the injected
// provider, distinct from the per-round LLMRound spans (two here).
func TestEngine_LLMTurn_OneSpanPerTurnAcrossRounds(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		{calls: []llm.ToolCall{{ID: "toolu_1", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":20}`)}}, stop: "tool_use"},
		{text: "You rolled well, traveler.", stop: "end_turn"},
	}}
	rec := &recordingStage{}
	eng := agenttool.NewEngine(prov, diceGrants(t), "", "claude-test", 256, 0,
		agenttool.WithMetrics(rec, observe.ProviderGroq))

	if _, err := eng.Generate(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Text: "Roll a d20 for me."},
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	rounds, _, _ := rec.snapshot()
	if len(rounds) != 2 {
		t.Fatalf("recorded %d LLMRound spans, want 2 (round-trip)", len(rounds))
	}
	turns := rec.turnSpans()
	if len(turns) != 1 {
		t.Fatalf("recorded %d LLMTurn spans, want exactly 1 per turn (across all rounds)", len(turns))
	}
	if turns[0] != observe.ProviderGroq {
		t.Errorf("LLMTurn provider = %q, want the injected groq label", turns[0])
	}
}

// TestEngine_GenerateStream_RecordsOneLLMTurn pins that the streaming production
// path records the SAME single LLMTurn span Generate does — the metric must not be
// dropped by the streaming loop.
func TestEngine_GenerateStream_RecordsOneLLMTurn(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		{calls: []llm.ToolCall{{ID: "toolu_1", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":20}`)}}, stop: "tool_use"},
		{text: "You rolled well, traveler.", stop: "end_turn"},
	}}
	rec := &recordingStage{}
	eng := agenttool.NewEngine(prov, diceGrants(t), "", "claude-test", 256, 0,
		agenttool.WithMetrics(rec, observe.ProviderGroq))

	if _, err := eng.GenerateStream(context.Background(),
		[]llm.Message{{Role: llm.RoleUser, Text: "Roll a d20 for me."}},
		func(string) error { return nil },
	); err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}

	if turns := rec.turnSpans(); len(turns) != 1 {
		t.Fatalf("streaming recorded %d LLMTurn spans, want exactly 1", len(turns))
	}
}

// TestEngine_LLMTurn_RecordsOnError pins that a turn whose loop fails still records
// its LLMTurn span — the full-turn latency series must see failed turns too, not
// just successful ones (otherwise a provider outage silently drops the samples).
func TestEngine_LLMTurn_RecordsOnError(t *testing.T) {
	rec := &recordingStage{}
	eng := agenttool.NewEngine(errorEventProvider{}, tool.NewGrantSet(tool.NewRegistry()), "", "m", 0, 0,
		agenttool.WithMetrics(rec, observe.ProviderGroq))

	if _, err := eng.Generate(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "hi"}}); err == nil {
		t.Fatal("Generate on a failing provider returned nil error")
	}
	if turns := rec.turnSpans(); len(turns) != 1 {
		t.Fatalf("recorded %d LLMTurn spans on the error path, want exactly 1", len(turns))
	}
}

// TestEngine_GenerateStream_ForwardsFinalTextAndKeepsMetrics is the streaming
// twin of the round-span test and the regression guard the streaming production
// path needs: GenerateStream must (a) forward the final answer's prose to onText
// and (b) record the SAME A3 LLMRound / provider-call spans Generate does — the
// streaming path must not silently drop the instrumentation bench-llm keys C1 on.
// A dice round-trip: round 0 requests the tool (no prose), round 1 answers.
func TestEngine_GenerateStream_ForwardsFinalTextAndKeepsMetrics(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		{calls: []llm.ToolCall{{ID: "toolu_1", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":20}`)}}, stop: "tool_use"},
		{text: "You rolled well, traveler.", stop: "end_turn"},
	}}
	rec := &recordingStage{}
	eng := agenttool.NewEngine(prov, diceGrants(t), "", "claude-test", 256, 0,
		agenttool.WithMetrics(rec, observe.ProviderGemini))

	var streamed strings.Builder
	full, err := eng.GenerateStream(context.Background(),
		[]llm.Message{{Role: llm.RoleUser, Text: "Roll a d20 for me."}},
		func(delta string) error { streamed.WriteString(delta); return nil },
	)
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	if strings.TrimSpace(full) != "You rolled well, traveler." {
		t.Errorf("full = %q, want the final answer", full)
	}
	// The tool-call round emits no prose, so onText only ever sees the answer.
	if strings.TrimSpace(streamed.String()) != "You rolled well, traveler." {
		t.Errorf("streamed = %q, want only the final answer's prose (no tool-round preamble)", streamed.String())
	}

	rounds, callsOK, callsErr := rec.snapshot()
	if len(rounds) != 2 {
		t.Fatalf("recorded %d LLMRound spans on the streaming path, want 2", len(rounds))
	}
	if rounds[0].roundIndex != 0 || !rounds[0].hadToolCall {
		t.Errorf("round 0 = %+v, want index 0 had_tool_call=true", rounds[0])
	}
	if rounds[1].roundIndex != 1 || rounds[1].hadToolCall {
		t.Errorf("round 1 = %+v, want index 1 had_tool_call=false", rounds[1])
	}
	if callsOK != 2 || callsErr != 0 {
		t.Errorf("streaming provider calls ok=%d err=%d, want 2/0", callsOK, callsErr)
	}
}

// TestEngine_GenerateStream_NonStreamingProviderFallsBack pins the fallback: a
// provider that is not a StreamingProvider still works through GenerateStream —
// it runs to completion and forwards the whole text once. (scriptedProvider IS
// wrapped into a streaming adapter, so this exercises the loop's own fallback by
// asserting the single forward equals the full answer for a no-tool turn.)
func TestEngine_GenerateStream_NoToolTurnStreamsAnswer(t *testing.T) {
	prov := &scriptedProvider{steps: []step{{text: "Welcome to the inn.", stop: "end_turn"}}}
	eng := agenttool.NewEngine(prov, tool.NewGrantSet(tool.NewRegistry()), "", "claude-test", 256, 0)

	var got strings.Builder
	full, err := eng.GenerateStream(context.Background(),
		[]llm.Message{{Role: llm.RoleUser, Text: "Hello."}},
		func(delta string) error { got.WriteString(delta); return nil },
	)
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	if strings.TrimSpace(full) != "Welcome to the inn." || strings.TrimSpace(got.String()) != "Welcome to the inn." {
		t.Errorf("full=%q streamed=%q, want both = the answer", full, got.String())
	}
}

// TestEngine_NoTools_SingleGenerationReturnsText pins that the same bridge path
// covers the no-tool case: with empty grants the loop does one Generate and
// returns the text, so an Agent with no Tool Grants behaves like the default
// provider engine.
func TestEngine_NoTools_SingleGenerationReturnsText(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		{text: "Welcome to the inn.", stop: "end_turn"},
	}}
	emptyGrants := tool.NewGrantSet(tool.NewRegistry())

	eng := agenttool.NewEngine(prov, emptyGrants, "", "claude-test", 256, 0)
	got, err := eng.Generate(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Text: "Hello."},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.TrimSpace(got) != "Welcome to the inn." {
		t.Errorf("text = %q, want single-generation answer", got)
	}
	if n := len(prov.requests()); n != 1 {
		t.Errorf("provider called %d times, want 1 (no tools → one generation)", n)
	}
}

// TestEngine_DiceGate_PlainTurnOffersNoDiceAndIsSingleRound is the core #5 pin:
// a conversational utterance with no dice intent must NOT declare the dice Tool,
// so the model cannot emit an empty tool-call round before its answer — the turn
// is structurally a single LLM round. This is the baseline-latency win.
func TestEngine_DiceGate_PlainTurnOffersNoDiceAndIsSingleRound(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		{text: "Ale's a copper, traveler.", stop: "end_turn"},
	}}
	eng := agenttool.NewEngine(prov, diceGrants(t), "", "claude-test", 256, 0)

	got, err := eng.Generate(context.Background(), []llm.Message{
		{Role: llm.RoleSystem, Text: "You are Bart."},
		{Role: llm.RoleUser, Text: "How much for a room and a pint?"},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.TrimSpace(got) != "Ale's a copper, traveler." {
		t.Errorf("text = %q, want the single-round answer", got)
	}
	reqs := prov.requests()
	if len(reqs) != 1 {
		t.Fatalf("provider called %d times, want 1 (a plain turn must be one round)", len(reqs))
	}
	if len(reqs[0].Tools) != 0 {
		t.Errorf("plain turn declared tools %+v, want none (dice gated out)", reqs[0].Tools)
	}
}

// TestEngine_DiceGate_KeywordIntentOffersDice proves the gate's recall: an
// utterance with ttrpg roll intent but no explicit NdM (a "saving throw") still
// arms the dice Tool, so the tool path is not broken when dice IS needed.
func TestEngine_DiceGate_KeywordIntentOffersDice(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		{calls: []llm.ToolCall{{ID: "toolu_1", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":20}`)}}, stop: "tool_use"},
		{text: "You steady your nerves.", stop: "end_turn"},
	}}
	eng := agenttool.NewEngine(prov, diceGrants(t), "", "claude-test", 256, 0)

	if _, err := eng.Generate(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Text: "Make a saving throw against the poison."},
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	reqs := prov.requests()
	if len(reqs) != 2 {
		t.Fatalf("provider called %d times, want 2 (dice intent → tool round-trip)", len(reqs))
	}
	if len(reqs[0].Tools) != 1 || reqs[0].Tools[0].Name != "dice" {
		t.Errorf("keyword-intent turn tools = %+v, want the dice decl offered", reqs[0].Tools)
	}
}

// TestEngine_DiceGate_HistoryDiceDoesNotArmCurrentTurn pins that the gate keys on
// the CURRENT utterance only: a dice roll three messages ago must not keep dice
// declared on a later plain turn.
func TestEngine_DiceGate_HistoryDiceDoesNotArmCurrentTurn(t *testing.T) {
	prov := &scriptedProvider{steps: []step{{text: "Right this way.", stop: "end_turn"}}}
	eng := agenttool.NewEngine(prov, diceGrants(t), "", "claude-test", 256, 0)

	if _, err := eng.Generate(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Text: "Roll a d20."},                // older turn: had dice intent
		{Role: llm.RoleAssistant, Text: "You rolled a 14."},      // its answer
		{Role: llm.RoleUser, Text: "Great, point me to a room."}, // current turn: plain
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	reqs := prov.requests()
	if len(reqs) != 1 || len(reqs[0].Tools) != 0 {
		t.Errorf("history dice armed the current plain turn: reqs=%d tools=%+v, want 1 req / 0 tools", len(reqs), reqs[0].Tools)
	}
}

// TestEngine_DiceGate_GermanUtteranceOffersDiceWithLanguage is the #226 pin: a
// German campaign (WithLanguage("de")) arms the dice Tool for the exact live
// failure utterance — „Würfelwerkzeug … würfle zwei sechsseitige Würfel" — so
// the model gets a real tool round-trip instead of improvising the roll.
func TestEngine_DiceGate_GermanUtteranceOffersDiceWithLanguage(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		{calls: []llm.ToolCall{{ID: "toolu_1", Name: "dice", Input: json.RawMessage(`{"count":2,"sides":6}`)}}, stop: "tool_use"},
		{text: "Eine Vier und eine Zwei.", stop: "end_turn"},
	}}
	eng := agenttool.NewEngine(prov, diceGrants(t), "", "claude-test", 256, 0,
		agenttool.WithLanguage("de-DE"))

	if _, err := eng.Generate(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Text: "Bart, benutze dein Würfelwerkzeug und würfle zwei sechsseitige Würfel."},
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	reqs := prov.requests()
	if len(reqs) != 2 {
		t.Fatalf("provider called %d times, want 2 (German dice intent → tool round-trip)", len(reqs))
	}
	if len(reqs[0].Tools) != 1 || reqs[0].Tools[0].Name != "dice" {
		t.Errorf("German dice turn tools = %+v, want the dice decl offered", reqs[0].Tools)
	}
}

// TestEngine_DiceGate_GermanPlainTurnGatedWithLanguage proves the other #226
// direction: with WithLanguage("de") the German article „die" no longer trips
// the gate, so a plain German turn stays a single dice-less round.
func TestEngine_DiceGate_GermanPlainTurnGatedWithLanguage(t *testing.T) {
	prov := &scriptedProvider{steps: []step{{text: "Es war einmal…", stop: "end_turn"}}}
	eng := agenttool.NewEngine(prov, diceGrants(t), "", "claude-test", 256, 0,
		agenttool.WithLanguage("de"))

	if _, err := eng.Generate(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Text: "Erzähl mir die Geschichte von diesem Ort."},
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	reqs := prov.requests()
	if len(reqs) != 1 || len(reqs[0].Tools) != 0 {
		t.Errorf("plain German turn: reqs=%d tools=%+v, want 1 req / 0 tools (article „die\" gated)", len(reqs), reqs[0].Tools)
	}
}

// TestEngine_DiceGate_GermanUtteranceWithoutLanguageStaysEnglish pins that the
// language must be threaded to fix #226: without WithLanguage the gate stays
// English (the pre-#226 behavior, byte-for-byte), so the same German dice
// utterance is gated out — reproducing the live false negative.
func TestEngine_DiceGate_GermanUtteranceWithoutLanguageStaysEnglish(t *testing.T) {
	prov := &scriptedProvider{steps: []step{{text: "role-played roll", stop: "end_turn"}}}
	eng := agenttool.NewEngine(prov, diceGrants(t), "", "claude-test", 256, 0)

	if _, err := eng.Generate(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Text: "Bart, benutze dein Würfelwerkzeug und würfle zwei sechsseitige Würfel."},
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	reqs := prov.requests()
	if len(reqs) != 1 || len(reqs[0].Tools) != 0 {
		t.Errorf("German utterance under the EN default: reqs=%d tools=%+v, want 1 req / 0 tools (dice gated — the #226 failure)", len(reqs), reqs[0].Tools)
	}
}

// TestEngine_AsAgentEngine_DrivesReplier pins that the bridge slots into the
// agent loop via agent.Config.Engine — the production wiring path — so an
// addressed utterance flows utterance → Hot Context → tool loop → spoken reply.
func TestEngine_AsAgentEngine_DrivesReplier(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		{text: "Aye, two rooms upstairs.", stop: "end_turn"},
	}}
	eng := agenttool.NewEngine(prov, tool.NewGrantSet(tool.NewRegistry()), "", "claude-test", 256, 0)

	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart."},
		Engine:      eng,
		Synthesizer: stubSynth{},
	})
	got := r.Reply()(t.Context(), addressed("bart", "Any rooms free?"))
	if len(got) != 1 || strings.TrimSpace(got[0].Sentence) != "Aye, two rooms upstairs." {
		t.Fatalf("reply = %+v, want one spoken sentence from the tool engine", got)
	}
	// The Replier assembled Hot Context (system prompt) before the engine ran.
	reqs := prov.requests()
	if len(reqs) == 0 || reqs[0].Messages[0].Role != llm.RoleSystem {
		t.Errorf("engine did not receive an assembled system prompt; first req = %+v", reqs)
	}
}

// truncatingProvider streams some text and closes without [llm.EventDone] — a
// mid-stream failure as the [llm.Provider] contract describes it.
type truncatingProvider struct{}

func (truncatingProvider) Complete(context.Context, llm.Request) (<-chan llm.StreamEvent, error) {
	ch := make(chan llm.StreamEvent, 1)
	ch <- llm.StreamEvent{Type: llm.EventText, Text: "Half a sen"}
	close(ch)
	return ch, nil
}

// TestEngine_TruncatedStream_ReturnsError pins the truncation contract on the
// tool-loop bridge: a stream that ends without EventDone must fail the
// generation, never hand the loop (and ultimately TTS) a partial answer.
func TestEngine_TruncatedStream_ReturnsError(t *testing.T) {
	reg := tool.NewRegistry()
	grants := tool.NewGrantSet(reg)
	eng := agenttool.NewEngine(truncatingProvider{}, grants, "", "m", 0, 0)

	_, err := eng.Generate(t.Context(), []llm.Message{{Role: llm.RoleUser, Text: "hi"}})
	if err == nil || !strings.Contains(err.Error(), "without done") {
		t.Fatalf("Generate on truncated stream = %v, want truncation error", err)
	}
}

// startErrProvider fails the Complete call itself (start error, no stream), the
// shape a 4xx/5xx takes.
type startErrProvider struct{}

func (startErrProvider) Complete(context.Context, llm.Request) (<-chan llm.StreamEvent, error) {
	return nil, errors.New("provider: start boom")
}

// bargeStartProvider blocks the Complete call until the ctx is cancelled, then
// returns ctx.Err() — a barge-in that cuts the dial in flight (a pre-cancelled ctx
// would instead make the tool loop bail before it ever calls the provider).
type bargeStartProvider struct{}

func (bargeStartProvider) Complete(ctx context.Context, _ llm.Request) (<-chan llm.StreamEvent, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestEngine_StartError_ClassifiesOutcomeLikeStages pins the #239 alignment: the
// LLM start-error path classifies via the shared observe.CallOutcome rule, so a
// barge cancelling the dial in flight is outcome=canceled with NO provider_error
// (not a fault), while a live-ctx start failure is outcome=error WITH a
// provider_error. This keeps the LLM stage in agreement with STT/TTS.
func TestEngine_StartError_ClassifiesOutcomeLikeStages(t *testing.T) {
	empty := tool.NewGrantSet(tool.NewRegistry())

	t.Run("barge cancel is canceled, not a fault", func(t *testing.T) {
		rec := &recordingStage{}
		eng := agenttool.NewEngine(bargeStartProvider{}, empty, "", "m", 0, 0,
			agenttool.WithMetrics(rec, observe.ProviderGroq))
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := eng.Generate(ctx, []llm.Message{{Role: llm.RoleUser, Text: "hi"}})
			done <- err
		}()
		time.Sleep(20 * time.Millisecond) // let the loop reach the blocked Complete
		cancel()                          // barge-in cuts the dial

		select {
		case err := <-done:
			if err == nil {
				t.Fatal("Generate returned nil on a cancelled dial")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("Generate did not return after the ctx was cancelled")
		}
		outcomes, provErrs := rec.providerCalls()
		if len(outcomes) != 1 || outcomes[0] != observe.OutcomeCanceled {
			t.Errorf("provider_call outcomes = %v, want [canceled]", outcomes)
		}
		if provErrs != 0 {
			t.Errorf("provider_errors = %d on a barge cancel, want 0 (not a fault)", provErrs)
		}
	})

	t.Run("live-ctx start failure is error + fault", func(t *testing.T) {
		rec := &recordingStage{}
		eng := agenttool.NewEngine(startErrProvider{}, empty, "", "m", 0, 0,
			agenttool.WithMetrics(rec, observe.ProviderGroq))
		if _, err := eng.Generate(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "hi"}}); err == nil {
			t.Fatal("Generate returned nil on a start failure")
		}
		outcomes, provErrs := rec.providerCalls()
		if len(outcomes) != 1 || outcomes[0] != observe.OutcomeError {
			t.Errorf("provider_call outcomes = %v, want [error]", outcomes)
		}
		if provErrs != 1 {
			t.Errorf("provider_errors = %d on a vendor start failure, want 1", provErrs)
		}
	})
}

// usageProvider streams a fixed exact text (no per-word spacing) and, optionally,
// one provider-reported EventUsage, so the #127 usage-metering tests drive both the
// reported-usage and estimate-fallback paths deterministically. usageFirst emits
// the usage BEFORE the text so a barge (onText error on the first delta) can see it.
// The channel is buffered so a test that bails early (barge) never leaks the
// producer goroutine.
type usageProvider struct {
	text       string
	usage      *llm.Usage
	usageFirst bool
}

func (p usageProvider) Complete(context.Context, llm.Request) (<-chan llm.StreamEvent, error) {
	ch := make(chan llm.StreamEvent, 8)
	go func() {
		defer close(ch)
		emitUsage := func() {
			if p.usage != nil {
				ch <- llm.StreamEvent{Type: llm.EventUsage, Usage: *p.usage}
			}
		}
		if p.usageFirst {
			emitUsage()
		}
		if p.text != "" {
			ch <- llm.StreamEvent{Type: llm.EventText, Text: p.text}
		}
		if !p.usageFirst {
			emitUsage()
		}
		ch <- llm.StreamEvent{Type: llm.EventDone, StopReason: "end_turn"}
	}()
	return ch, nil
}

// TestEngine_Usage_RecordsProviderReportedTokensAndModel pins the #127 happy path
// (ADR-0045): a completion that reports usage records exactly one LLMTokens with the
// provider-reported input/output counts, labelled with the injected provider AND the
// adapter model (the model rides to the spend meter; Prometheus drops it later).
func TestEngine_Usage_RecordsProviderReportedTokensAndModel(t *testing.T) {
	prov := usageProvider{text: "Aye, traveler.", usage: &llm.Usage{InputTokens: 100, OutputTokens: 50}}
	rec := &recordingStage{}
	eng := agenttool.NewEngine(prov, tool.NewGrantSet(tool.NewRegistry()), "", "claude-test-model", 256, 0,
		agenttool.WithMetrics(rec, observe.ProviderGroq))

	if _, err := eng.Generate(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "Hello."}}); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	toks := rec.tokenSpans()
	if len(toks) != 1 {
		t.Fatalf("recorded %d LLMTokens, want exactly 1", len(toks))
	}
	want := llmTokensRec{provider: observe.ProviderGroq, model: "claude-test-model", in: 100, out: 50}
	if toks[0] != want {
		t.Errorf("LLMTokens = %+v, want %+v (provider-reported counts + adapter model)", toks[0], want)
	}
}

// TestEngine_Usage_EstimatesWhenProviderReportsNone pins the AC estimate fallback:
// a completion with NO EventUsage (an old cassette, a gateway that omits it) records
// a documented ceil(chars/4) estimate per direction — never zero. Sent text is the
// one user message ("Hello." = 6 runes → 2); received text is the answer
// ("abcdefghijkl" = 12 runes → 3).
func TestEngine_Usage_EstimatesWhenProviderReportsNone(t *testing.T) {
	prov := usageProvider{text: "abcdefghijkl"} // 12 runes, no usage reported
	rec := &recordingStage{}
	eng := agenttool.NewEngine(prov, tool.NewGrantSet(tool.NewRegistry()), "", "m", 256, 0,
		agenttool.WithMetrics(rec, observe.ProviderGemini))

	if _, err := eng.Generate(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "Hello."}}); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	toks := rec.tokenSpans()
	if len(toks) != 1 {
		t.Fatalf("recorded %d LLMTokens, want exactly 1 (estimate, never zero)", len(toks))
	}
	want := llmTokensRec{provider: observe.ProviderGemini, model: "m", in: 2, out: 3}
	if toks[0] != want {
		t.Errorf("estimate = %+v, want %+v (ceil(6/4)=2 in, ceil(12/4)=3 out)", toks[0], want)
	}
}

// TestEngine_Usage_StartErrorRecordsNoTokens pins that a completion that never
// starts (a start error) records NO usage — there was no completion to meter.
func TestEngine_Usage_StartErrorRecordsNoTokens(t *testing.T) {
	rec := &recordingStage{}
	eng := agenttool.NewEngine(startErrProvider{}, tool.NewGrantSet(tool.NewRegistry()), "", "m", 0, 0,
		agenttool.WithMetrics(rec, observe.ProviderGroq))

	if _, err := eng.Generate(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "hi"}}); err == nil {
		t.Fatal("Generate returned nil on a start failure")
	}
	if toks := rec.tokenSpans(); len(toks) != 0 {
		t.Errorf("recorded %d LLMTokens on a start error, want 0", len(toks))
	}
}

// TestEngine_Usage_BargeRecordsReportedUsageOnlyIfSeen pins the barge rule
// (ADR-0045): a barge (onText error) records the provider-reported usage IF it
// already arrived, otherwise nothing — a partial turn is never estimated.
func TestEngine_Usage_BargeRecordsReportedUsageOnlyIfSeen(t *testing.T) {
	t.Run("usage already seen → recorded", func(t *testing.T) {
		prov := usageProvider{text: "partial answer", usage: &llm.Usage{InputTokens: 30, OutputTokens: 5}, usageFirst: true}
		rec := &recordingStage{}
		eng := agenttool.NewEngine(prov, tool.NewGrantSet(tool.NewRegistry()), "", "m", 256, 0,
			agenttool.WithMetrics(rec, observe.ProviderGroq))

		_, err := eng.GenerateStream(context.Background(),
			[]llm.Message{{Role: llm.RoleUser, Text: "Hello."}},
			func(string) error { return errors.New("barge") })
		if err == nil {
			t.Fatal("GenerateStream returned nil on a barge")
		}
		toks := rec.tokenSpans()
		want := llmTokensRec{provider: observe.ProviderGroq, model: "m", in: 30, out: 5}
		if len(toks) != 1 || toks[0] != want {
			t.Errorf("LLMTokens on a barge with usage seen = %+v, want one %+v (reported, not estimated)", toks, want)
		}
	})

	t.Run("no usage yet → nothing", func(t *testing.T) {
		prov := usageProvider{text: "partial answer"} // no usage before the barge
		rec := &recordingStage{}
		eng := agenttool.NewEngine(prov, tool.NewGrantSet(tool.NewRegistry()), "", "m", 256, 0,
			agenttool.WithMetrics(rec, observe.ProviderGroq))

		_, err := eng.GenerateStream(context.Background(),
			[]llm.Message{{Role: llm.RoleUser, Text: "Hello."}},
			func(string) error { return errors.New("barge") })
		if err == nil {
			t.Fatal("GenerateStream returned nil on a barge")
		}
		if toks := rec.tokenSpans(); len(toks) != 0 {
			t.Errorf("LLMTokens on a barge with no usage = %+v, want none (never estimate a partial turn)", toks)
		}
	})
}

// errorEventProvider terminates the stream with an [llm.EventError].
type errorEventProvider struct{}

func (errorEventProvider) Complete(context.Context, llm.Request) (<-chan llm.StreamEvent, error) {
	ch := make(chan llm.StreamEvent, 2)
	ch <- llm.StreamEvent{Type: llm.EventText, Text: "Half a sen"}
	ch <- llm.StreamEvent{Type: llm.EventError, Err: "provider: read stream: connection reset"}
	close(ch)
	return ch, nil
}

// TestEngine_EventError_PropagatesAsError pins that a terminal EventError
// surfaces as the generation error, carrying the provider's message.
func TestEngine_EventError_PropagatesAsError(t *testing.T) {
	reg := tool.NewRegistry()
	grants := tool.NewGrantSet(reg)
	eng := agenttool.NewEngine(errorEventProvider{}, grants, "", "m", 0, 0)

	_, err := eng.Generate(t.Context(), []llm.Message{{Role: llm.RoleUser, Text: "hi"}})
	if err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("Generate on EventError = %v, want the provider's error", err)
	}
}

// toolSyntaxProvider fails its leading `fails` Complete calls with the provider's
// tool-syntax failure class (#398) — either a *providererr.ToolSyntaxError start
// error (viaStream=false) or a mid-stream EventError carrying ErrClass tool_syntax
// (viaStream=true) — then streams `answer`. It records every Request (so a test can
// prove the fallback round declared the same tools with tool_choice none) and the
// tool-choice each call carried. Keyless.
type toolSyntaxProvider struct {
	mu        sync.Mutex
	fails     int
	viaStream bool
	answer    string
	reqs      []llm.Request
	calls     int
}

func (p *toolSyntaxProvider) Complete(_ context.Context, req llm.Request) (<-chan llm.StreamEvent, error) {
	p.mu.Lock()
	p.calls++
	n := p.calls
	p.reqs = append(p.reqs, req)
	p.mu.Unlock()

	if n <= p.fails {
		if !p.viaStream {
			return nil, &providererr.ToolSyntaxError{Op: "test.Complete", Msg: `test: HTTP 400: {"code":"tool_use_failed"}`}
		}
		ch := make(chan llm.StreamEvent, 1)
		ch <- llm.StreamEvent{Type: llm.EventError, Err: "test: read stream: tool_use_failed", ErrClass: llm.ErrClassToolSyntax}
		close(ch)
		return ch, nil
	}
	ch := make(chan llm.StreamEvent)
	go func() {
		defer close(ch)
		for _, w := range strings.Fields(p.answer) {
			ch <- llm.StreamEvent{Type: llm.EventText, Text: w + " "}
		}
		ch <- llm.StreamEvent{Type: llm.EventDone, StopReason: "end_turn"}
	}()
	return ch, nil
}

func (p *toolSyntaxProvider) requests() []llm.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.Request(nil), p.reqs...)
}

// TestEngine_ToolSyntax_RetriesOnceThenDelivers is AC 1 (#398): a tool-armed round
// that fails once with tool_use_failed is retried once with the SAME tools; the
// retry succeeds and its answer is delivered normally. The malformed generation is
// counted once, and the final metrics are the recovered-round shape (one LLMRound,
// one provider_call ok, no provider_error) — final-outcome-only per ADR-0044.
func TestEngine_ToolSyntax_RetriesOnceThenDelivers(t *testing.T) {
	for _, viaStream := range []bool{false, true} {
		name := "start_error"
		if viaStream {
			name = "in_stream_error"
		}
		t.Run(name, func(t *testing.T) {
			prov := &toolSyntaxProvider{fails: 1, viaStream: viaStream, answer: "You rolled well, traveler."}
			rec := &recordingStage{}
			eng := agenttool.NewEngine(prov, diceGrants(t), "", "m", 256, 0,
				agenttool.WithMetrics(rec, observe.ProviderGroq),
				agenttool.WithRetry(instantAgentRetry()))

			got, err := eng.Generate(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "Roll a d20 for me."}})
			if err != nil {
				t.Fatalf("Generate after one tool_use_failed: %v", err)
			}
			if strings.TrimSpace(got) != "You rolled well, traveler." {
				t.Errorf("final text = %q, want the retry answer", got)
			}
			if prov.calls != 2 {
				t.Errorf("Complete calls = %d, want 2 (one tool_use_failed retried once)", prov.calls)
			}
			if paths := rec.malformedPaths(); len(paths) != 1 || paths[0] != observe.MalformedStreamError {
				t.Errorf("MalformedToolGen paths = %v, want exactly [stream_error]", paths)
			}
			// The retry re-declared the same tools with the requested (auto) choice.
			reqs := prov.requests()
			if len(reqs) != 2 || len(reqs[1].Tools) == 0 {
				t.Fatalf("retry request tools = %+v, want the same tools re-declared", reqs[1].Tools)
			}
			if reqs[1].ToolChoice.Mode != llm.ToolChoiceAuto {
				t.Errorf("retry tool_choice = %q, want auto (same as the first attempt)", reqs[1].ToolChoice.Mode)
			}

			rounds, callsOK, callsErr := rec.snapshot()
			if len(rounds) != 1 {
				t.Errorf("LLMRound spans = %d, want 1 (final outcome only)", len(rounds))
			}
			if callsOK != 1 || callsErr != 0 {
				t.Errorf("provider calls ok=%d err=%d, want 1/0 (recovered)", callsOK, callsErr)
			}
			if _, provErrs := rec.providerCalls(); provErrs != 0 {
				t.Errorf("provider_errors = %d, want 0 (the retry recovered)", provErrs)
			}
		})
	}
}

// TestEngine_ToolSyntax_FallsBackToolLessAfterTwoFailures is AC 2 (#398): a
// tool-armed round that fails tool_use_failed TWICE regenerates without tool use —
// the third request keeps the tools DECLARED but sets tool_choice none — and that
// answer is delivered (nil error, NOT abandoned). Two malformed generations are
// counted; the recovered final round records provider_call ok with no error.
func TestEngine_ToolSyntax_FallsBackToolLessAfterTwoFailures(t *testing.T) {
	prov := &toolSyntaxProvider{fails: 2, answer: "I cannot consult the bones right now, but you steady yourself."}
	rec := &recordingStage{}
	eng := agenttool.NewEngine(prov, diceGrants(t), "", "m", 256, 0,
		agenttool.WithMetrics(rec, observe.ProviderGroq),
		agenttool.WithRetry(instantAgentRetry()))

	got, err := eng.Generate(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "Roll a d20 for me."}})
	if err != nil {
		t.Fatalf("Generate after two tool_use_failed: %v (turn must NOT be abandoned)", err)
	}
	if strings.TrimSpace(got) != "I cannot consult the bones right now, but you steady yourself." {
		t.Errorf("final text = %q, want the tool-less fallback answer", got)
	}
	if prov.calls != 3 {
		t.Errorf("Complete calls = %d, want 3 (attempt, retry, tool-less fallback)", prov.calls)
	}

	// The fallback request (third) must keep the tools declared AND set tool_choice
	// none — the conversation may hold prior tool_call/tool messages, so tools are
	// not stripped.
	reqs := prov.requests()
	if len(reqs) != 3 {
		t.Fatalf("recorded %d requests, want 3", len(reqs))
	}
	if len(reqs[2].Tools) == 0 {
		t.Errorf("fallback request stripped the tools; want them still declared")
	}
	if reqs[2].ToolChoice.Mode != llm.ToolChoiceNone {
		t.Errorf("fallback tool_choice = %q, want none", reqs[2].ToolChoice.Mode)
	}
	// The first two attempts used the requested (auto) choice.
	if reqs[0].ToolChoice.Mode != llm.ToolChoiceAuto || reqs[1].ToolChoice.Mode != llm.ToolChoiceAuto {
		t.Errorf("attempts 1/2 tool_choice = %q/%q, want auto/auto", reqs[0].ToolChoice.Mode, reqs[1].ToolChoice.Mode)
	}

	if paths := rec.malformedPaths(); len(paths) != 2 {
		t.Errorf("MalformedToolGen count = %d, want 2 (both failures counted)", len(paths))
	}
	rounds, callsOK, callsErr := rec.snapshot()
	if len(rounds) != 1 || callsOK != 1 || callsErr != 0 {
		t.Errorf("final metrics rounds=%d ok=%d err=%d, want 1/1/0 (recovered, final-outcome-only)", len(rounds), callsOK, callsErr)
	}
	if _, provErrs := rec.providerCalls(); provErrs != 0 {
		t.Errorf("provider_errors = %d, want 0 (fallback recovered the turn)", provErrs)
	}
}

// TestEngine_ToolSyntax_StripsToolsWhenNoneAttemptFails is AC 1 (#427): when the
// tool-less fallback attempt (tool_choice none, tools still declared) ALSO fails
// with the tool-syntax class and the conversation holds NO prior tool_call/tool
// messages, ONE final attempt is made with the tool declarations stripped entirely
// (nothing to call → the wire error is impossible). Its text is delivered and the
// turn is NOT abandoned. All three malformed generations are counted.
func TestEngine_ToolSyntax_StripsToolsWhenNoneAttemptFails(t *testing.T) {
	prov := &toolSyntaxProvider{fails: 3, answer: "The bones stay silent, but your hands are steady."}
	rec := &recordingStage{}
	eng := agenttool.NewEngine(prov, diceGrants(t), "", "m", 256, 0,
		agenttool.WithMetrics(rec, observe.ProviderGroq),
		agenttool.WithRetry(instantAgentRetry()))

	got, err := eng.Generate(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "Roll a d20 for me."}})
	if err != nil {
		t.Fatalf("Generate after three tool_use_failed: %v (turn must NOT be abandoned)", err)
	}
	if strings.TrimSpace(got) != "The bones stay silent, but your hands are steady." {
		t.Errorf("final text = %q, want the tool-stripped attempt's answer", got)
	}
	if prov.calls != 4 {
		t.Errorf("Complete calls = %d, want 4 (attempt, retry, tool-less, tool-stripped)", prov.calls)
	}

	reqs := prov.requests()
	if len(reqs) != 4 {
		t.Fatalf("recorded %d requests, want 4", len(reqs))
	}
	// The third request is the #398 fallback: tools declared, tool_choice none.
	if len(reqs[2].Tools) == 0 || reqs[2].ToolChoice.Mode != llm.ToolChoiceNone {
		t.Errorf("none-attempt tools=%d choice=%q, want declared tools with choice none",
			len(reqs[2].Tools), reqs[2].ToolChoice.Mode)
	}
	// The fourth request must carry NO tool declarations at all.
	if len(reqs[3].Tools) != 0 {
		t.Errorf("final attempt declared %d tools, want none (stripped entirely)", len(reqs[3].Tools))
	}
	if reqs[3].ToolChoice.Mode == llm.ToolChoiceNone || reqs[3].ToolChoice.Mode == llm.ToolChoiceTool {
		t.Errorf("final attempt tool_choice = %q, want the default (no tools to steer)", reqs[3].ToolChoice.Mode)
	}

	// All THREE malformed generations counted (#427: the third occurrence too).
	if paths := rec.malformedPaths(); len(paths) != 3 {
		t.Errorf("MalformedToolGen count = %d, want 3 (every failure counted)", len(paths))
	}
	rounds, callsOK, callsErr := rec.snapshot()
	if len(rounds) != 1 || callsOK != 1 || callsErr != 0 {
		t.Errorf("final metrics rounds=%d ok=%d err=%d, want 1/1/0 (recovered, final-outcome-only)", len(rounds), callsOK, callsErr)
	}
}

// toolHistoryThenSyntaxProvider emits one successful dice tool-call round, then
// fails every later Complete with the tool-syntax class — so the failing round's
// conversation carries prior tool_call/tool messages (#427's guard condition).
type toolHistoryThenSyntaxProvider struct {
	mu    sync.Mutex
	reqs  []llm.Request
	calls int
}

func (p *toolHistoryThenSyntaxProvider) Complete(_ context.Context, req llm.Request) (<-chan llm.StreamEvent, error) {
	p.mu.Lock()
	p.calls++
	n := p.calls
	p.reqs = append(p.reqs, req)
	p.mu.Unlock()

	if n == 1 {
		ch := make(chan llm.StreamEvent, 2)
		ch <- llm.StreamEvent{Type: llm.EventToolCall, ToolCall: llm.ToolCall{
			ID: "t1", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":20}`)}}
		ch <- llm.StreamEvent{Type: llm.EventDone, StopReason: "tool_use"}
		close(ch)
		return ch, nil
	}
	return nil, &providererr.ToolSyntaxError{Op: "test.Complete", Msg: "tool_use_failed"}
}

// TestEngine_ToolSyntax_PriorToolHistoryPropagates is AC 2 (#427): when the
// conversation already holds tool_call/tool messages (a dice round completed
// earlier in the turn), the tool-stripped final attempt is NOT made — stripping
// there risks a provider 400 on the dangling tool references (#420) — and the
// none-attempt's failure propagates as today. The third malformed generation is
// still counted.
func TestEngine_ToolSyntax_PriorToolHistoryPropagates(t *testing.T) {
	prov := &toolHistoryThenSyntaxProvider{}
	rec := &recordingStage{}
	eng := agenttool.NewEngine(prov, diceGrants(t), "", "m", 256, 0,
		agenttool.WithMetrics(rec, observe.ProviderGroq),
		agenttool.WithRetry(instantAgentRetry()))

	_, err := eng.Generate(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "Roll a d20 for me."}})
	if err == nil {
		t.Fatal("Generate = nil, want the propagated tool-syntax error (prior tool history pins today's behaviour)")
	}
	// 1 successful dice round + attempt/retry/none on the follow-up round — and NO
	// fifth, tool-stripped attempt.
	if prov.calls != 4 {
		t.Errorf("Complete calls = %d, want 4 (dice round + 3 failed attempts, no stripped attempt)", prov.calls)
	}
	if paths := rec.malformedPaths(); len(paths) != 3 {
		t.Errorf("MalformedToolGen count = %d, want 3 (the third occurrence is counted too)", len(paths))
	}
}

// TestEngine_NonToolSyntaxError_NotRetried is AC 3 (#398): a mid-stream provider
// error of a DIFFERENT class (not tool_use_failed) on a tool-armed round is NOT
// retried by the tool-syntax path — it propagates on the first call, and no
// malformed-generation is counted.
func TestEngine_NonToolSyntaxError_NotRetried(t *testing.T) {
	rec := &recordingStage{}
	eng := agenttool.NewEngine(errorEventProvider{}, diceGrants(t), "", "m", 0, 0,
		agenttool.WithMetrics(rec, observe.ProviderGroq),
		agenttool.WithRetry(instantAgentRetry()))

	_, err := eng.Generate(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "Roll a d20 for me."}})
	if err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("Generate on a non-tool-syntax error = %v, want the provider error propagated", err)
	}
	if paths := rec.malformedPaths(); len(paths) != 0 {
		t.Errorf("MalformedToolGen count = %d on a non-tool-syntax error, want 0", len(paths))
	}
}

// deltaThenToolSyntaxProvider forwards one prose delta, then aborts the stream with
// a tool_use_failed EventError — the ADR-0044 mid-stream case: audio may already be
// out, so the round must NOT be retried even though the failure class is tool-syntax.
type deltaThenToolSyntaxProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *deltaThenToolSyntaxProvider) Complete(context.Context, llm.Request) (<-chan llm.StreamEvent, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	ch := make(chan llm.StreamEvent, 2)
	ch <- llm.StreamEvent{Type: llm.EventText, Text: "You feel "}
	ch <- llm.StreamEvent{Type: llm.EventError, Err: "tool_use_failed", ErrClass: llm.ErrClassToolSyntax}
	close(ch)
	return ch, nil
}

// TestEngine_ToolSyntax_ForwardedDeltaNotRetried is AC-adjacent (#398, ADR-0044): a
// tool-syntax failure that arrives AFTER a prose delta was already forwarded to
// onText is never retried — a re-drive would re-speak. The error propagates on the
// first call.
//
// #399 note: dice-armed turns now BUFFER (no live streaming), so this re-speak
// protection is exercised on the still-live streaming path — a NON-dice tool
// (here "capture") declared on a plain, dice-unarmed turn, which routes through the
// gated loop's RunStream and forwards deltas live.
func TestEngine_ToolSyntax_ForwardedDeltaNotRetried(t *testing.T) {
	prov := &deltaThenToolSyntaxProvider{}
	rec := &recordingStage{}
	// Grant a non-dice read-only tool so the dice-unarmed (streaming) turn still
	// declares a tool — the round is tool-armed without being dice-armed.
	var seen string
	reg := tool.NewRegistry()
	reg.MustRegister(callerCaptureTool{got: &seen})
	grants := tool.NewGrantSet(reg, tool.Grant{ToolName: "capture"})
	eng := agenttool.NewEngine(prov, grants, "", "m", 0, 0,
		agenttool.WithMetrics(rec, observe.ProviderGroq),
		agenttool.WithRetry(instantAgentRetry()))

	var streamed strings.Builder
	_, err := eng.GenerateStream(context.Background(),
		[]llm.Message{{Role: llm.RoleUser, Text: "Tell me about the inn."}},
		func(delta string) error { streamed.WriteString(delta); return nil })
	if err == nil {
		t.Fatal("GenerateStream returned nil after a mid-stream tool_use_failed")
	}
	if prov.calls != 1 {
		t.Errorf("Complete calls = %d, want 1 (a forwarded delta forbids the retry)", prov.calls)
	}
	if strings.TrimSpace(streamed.String()) != "You feel" {
		t.Errorf("streamed = %q, want the one forwarded delta", streamed.String())
	}
}

// cancelOnFirstProvider fails the first tool-armed call with a tool-syntax start
// error but cancels the turn ctx first — so complete's between-attempt ctx check
// aborts before the retry ever dials.
type cancelOnFirstProvider struct {
	mu     sync.Mutex
	calls  int
	cancel context.CancelFunc
}

func (p *cancelOnFirstProvider) Complete(context.Context, llm.Request) (<-chan llm.StreamEvent, error) {
	p.mu.Lock()
	p.calls++
	n := p.calls
	p.mu.Unlock()
	if n == 1 {
		p.cancel() // barge lands between attempt 1's failure and the retry
		return nil, &providererr.ToolSyntaxError{Op: "test.Complete", Msg: "tool_use_failed"}
	}
	ch := make(chan llm.StreamEvent)
	go func() {
		defer close(ch)
		ch <- llm.StreamEvent{Type: llm.EventText, Text: "should not reach here "}
		ch <- llm.StreamEvent{Type: llm.EventDone, StopReason: "end_turn"}
	}()
	return ch, nil
}

// TestEngine_ToolSyntax_CtxCancelBetweenAttemptsAborts is AC-adjacent (#398): a ctx
// cancellation landing between the first tool-syntax failure and its retry aborts
// with the ctx error and never issues the retry.
func TestEngine_ToolSyntax_CtxCancelBetweenAttemptsAborts(t *testing.T) {
	prov := &cancelOnFirstProvider{}
	ctx, cancel := context.WithCancel(context.Background())
	prov.cancel = cancel
	rec := &recordingStage{}
	eng := agenttool.NewEngine(prov, diceGrants(t), "", "m", 0, 0,
		agenttool.WithMetrics(rec, observe.ProviderGroq),
		agenttool.WithRetry(instantAgentRetry()))

	_, err := eng.Generate(ctx, []llm.Message{{Role: llm.RoleUser, Text: "Roll a d20 for me."}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Generate = %v, want context.Canceled (abort between attempts)", err)
	}
	if prov.calls != 1 {
		t.Errorf("Complete calls = %d, want 1 (the retry must not dial after a cancel)", prov.calls)
	}
	// #399 drive-by B: the between-attempts abort must record its provider_call
	// (canceled), like the other cancel paths — not silently drop it. A barge cancel
	// is not a fault, so no provider_error.
	outcomes, provErrs := rec.providerCalls()
	if len(outcomes) != 1 || outcomes[0] != observe.OutcomeCanceled {
		t.Errorf("provider_call outcomes = %v, want [canceled] (abort must be counted)", outcomes)
	}
	if provErrs != 0 {
		t.Errorf("provider_errors = %d on a barge cancel between attempts, want 0 (not a fault)", provErrs)
	}
}

// callerCaptureTool is a read-only, scope-supporting Tool that records the
// [tool.CallerID] present in ctx at Execute time. It lets the bridge tests pin
// that the Engine stamps the Agent identity onto the turn ctx once (S2, #296),
// so a scope-narrowing handler resolves the caller from ctx, never the args.
type callerCaptureTool struct{ got *string }

func (callerCaptureTool) Name() string                 { return "capture" }
func (callerCaptureTool) Description() string          { return "capture caller" }
func (callerCaptureTool) InputSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (callerCaptureTool) ReadOnly() bool               { return true }
func (callerCaptureTool) SupportsScope() bool          { return true }
func (c callerCaptureTool) Execute(ctx context.Context, _ json.RawMessage, _ any) (string, error) {
	*c.got = tool.CallerID(ctx)
	return "ok", nil
}

// TestEngine_StampsCallerIdentity pins that NewEngine's agentID reaches the Tool
// handler via ctx: a granted Tool executed inside the loop sees CallerID equal to
// the Agent id the Engine was built with (S2). This is what keeps an own_node
// kg_query scoped to the calling NPC and un-widenable by crafted args.
func TestEngine_StampsCallerIdentity(t *testing.T) {
	var seen string
	reg := tool.NewRegistry()
	reg.MustRegister(callerCaptureTool{got: &seen})
	grants := tool.NewGrantSet(reg, tool.Grant{ToolName: "capture"})

	prov := &scriptedProvider{steps: []step{
		{calls: []llm.ToolCall{{ID: "c1", Name: "capture", Input: json.RawMessage(`{}`)}}, stop: "tool_use"},
		{text: "done", stop: "end_turn"},
	}}
	eng := agenttool.NewEngine(prov, grants, "agent-bart", "m", 0, 0)
	if _, err := eng.Generate(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "recall"}}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if seen != "agent-bart" {
		t.Errorf("handler saw CallerID %q, want the Engine's agentID agent-bart", seen)
	}
}

// --- #399 dice-gate enforcement: prompt hardening + invented-roll guard ---

// hardeningSubstr is a byte-stable slice of the dice-hardening instruction the
// armed turn appends to the system prompt; asserting containment avoids coupling
// the test to the (unexported) full constant while still pinning the instruction.
const hardeningSubstr = "Never invent, guess, or roleplay a die result."

// TestEngine_DiceHardening_ArmedTurnAppendsInstruction is AC "prompt hardening
// present on dice-armed turns": an armed utterance's first (system) request carries
// the hardening instruction; a plain unarmed turn's system prompt does not.
func TestEngine_DiceHardening_ArmedTurnAppendsInstruction(t *testing.T) {
	t.Run("armed turn hardens the system prompt", func(t *testing.T) {
		prov := &scriptedProvider{steps: []step{
			{calls: []llm.ToolCall{{ID: "t1", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":20}`)}}, stop: "tool_use"},
			{text: "You rolled well.", stop: "end_turn"},
		}}
		eng := agenttool.NewEngine(prov, diceGrants(t), "", "m", 256, 0)
		if _, err := eng.Generate(context.Background(), []llm.Message{
			{Role: llm.RoleSystem, Text: "You are Bart."},
			{Role: llm.RoleUser, Text: "Roll a d20 for me."},
		}); err != nil {
			t.Fatalf("Generate: %v", err)
		}
		sys := prov.requests()[0].Messages[0]
		if sys.Role != llm.RoleSystem || !strings.Contains(sys.Text, hardeningSubstr) {
			t.Errorf("armed system prompt = %q, want it to contain the hardening instruction", sys.Text)
		}
		if !strings.Contains(sys.Text, "You are Bart.") {
			t.Errorf("armed system prompt dropped the original persona: %q", sys.Text)
		}
	})

	t.Run("unarmed turn is not hardened", func(t *testing.T) {
		prov := &scriptedProvider{steps: []step{{text: "A copper, traveler.", stop: "end_turn"}}}
		eng := agenttool.NewEngine(prov, diceGrants(t), "", "m", 256, 0)
		if _, err := eng.Generate(context.Background(), []llm.Message{
			{Role: llm.RoleSystem, Text: "You are Bart."},
			{Role: llm.RoleUser, Text: "How much for a pint?"},
		}); err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if sys := prov.requests()[0].Messages[0]; strings.Contains(sys.Text, hardeningSubstr) {
			t.Errorf("unarmed system prompt was hardened: %q", sys.Text)
		}
	})
}

// TestEngine_InventedRoll_RegeneratesWithForcedDice is the headline AC: dice armed,
// the model narrates a roll result WITHOUT calling the tool → the reply is discarded
// and regenerated with the dice tool FORCED; the delivered reply reflects the
// actually executed roll, and exactly one roll-claim malformed-gen is counted.
//
// The seeded diceGrants rng rolls a 16 first — the regen narrates that same 16, so
// the #438 consistency check sees a matching claim and the reply ships unchanged.
func TestEngine_InventedRoll_RegeneratesWithForcedDice(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		// Run 1 (buffered): invents a result, no tool call.
		{text: "Ah, eine 19! Du hast Gluck.", stop: "end_turn"},
		// Forced regen round 0: now it calls the dice tool.
		{calls: []llm.ToolCall{{ID: "t1", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":20}`)}}, stop: "tool_use"},
		// Forced regen round 1: answers with the real result in context.
		{text: "The bones show a 16, traveler.", stop: "end_turn"},
	}}
	rec := &recordingStage{}
	eng := agenttool.NewEngine(prov, diceGrants(t), "", "m", 256, 0,
		agenttool.WithMetrics(rec, observe.ProviderGroq))

	got, err := eng.Generate(context.Background(), []llm.Message{
		{Role: llm.RoleSystem, Text: "You are Bart."},
		{Role: llm.RoleUser, Text: "Wurfel einen D20 fur mich."},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.TrimSpace(got) != "The bones show a 16, traveler." {
		t.Errorf("delivered = %q, want the regenerated reply reflecting the executed roll", got)
	}
	if paths := rec.malformedPaths(); len(paths) != 1 || paths[0] != observe.MalformedRollClaim {
		t.Errorf("MalformedToolGen paths = %v, want exactly [roll_claim]", paths)
	}

	reqs := prov.requests()
	if len(reqs) != 3 {
		t.Fatalf("Complete calls = %d, want 3 (buffered run + forced regen round-trip)", len(reqs))
	}
	// Buffered first run: auto choice.
	if reqs[0].ToolChoice.Mode != llm.ToolChoiceAuto {
		t.Errorf("first-run tool_choice = %q, want auto", reqs[0].ToolChoice.Mode)
	}
	// Forced regen round 0: pinned to the dice tool.
	if reqs[1].ToolChoice.Mode != llm.ToolChoiceTool || reqs[1].ToolChoice.Tool != "dice" {
		t.Errorf("forced-regen round-0 tool_choice = %+v, want tool:dice", reqs[1].ToolChoice)
	}
	// Forced regen round 1 (after the tool result): one-shot spent, back to auto.
	if reqs[2].ToolChoice.Mode != llm.ToolChoiceAuto {
		t.Errorf("forced-regen round-1 tool_choice = %q, want auto (forced choice is one-shot)", reqs[2].ToolChoice.Mode)
	}
}

// TestEngine_NativeDiceCall_NoRegeneration is AC "dice armed, model calls the tool
// properly → no regeneration": a proper tool round-trip fires the guard for nobody,
// so there is exactly one loop run and no roll-claim counted.
func TestEngine_NativeDiceCall_NoRegeneration(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		{calls: []llm.ToolCall{{ID: "t1", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":20}`)}}, stop: "tool_use"},
		{text: "You rolled an 8, traveler.", stop: "end_turn"},
	}}
	rec := &recordingStage{}
	eng := agenttool.NewEngine(prov, diceGrants(t), "", "m", 256, 0,
		agenttool.WithMetrics(rec, observe.ProviderGroq))

	got, err := eng.Generate(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "Roll a d20 for me."}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.TrimSpace(got) != "You rolled an 8, traveler." {
		t.Errorf("delivered = %q, want the native round-trip answer", got)
	}
	if paths := rec.malformedPaths(); len(paths) != 0 {
		t.Errorf("MalformedToolGen paths = %v, want none (the tool was called)", paths)
	}
	reqs := prov.requests()
	if len(reqs) != 2 {
		t.Fatalf("Complete calls = %d, want 2 (single round-trip, no regeneration)", len(reqs))
	}
	for i, r := range reqs {
		if r.ToolChoice.Mode == llm.ToolChoiceTool {
			t.Errorf("req[%d] used forced tool choice %+v; want no forced regeneration", i, r.ToolChoice)
		}
	}
}

// TestEngine_NotArmed_GuardNeverFires is AC "dice not armed → reply text never
// triggers the guard": a plain turn whose reply contains a bare number is delivered
// untouched — the guard is gated on the dice-armed decision.
func TestEngine_NotArmed_GuardNeverFires(t *testing.T) {
	prov := &scriptedProvider{steps: []step{{text: "That'll be 5 silver, traveler.", stop: "end_turn"}}}
	rec := &recordingStage{}
	eng := agenttool.NewEngine(prov, diceGrants(t), "", "m", 256, 0,
		agenttool.WithMetrics(rec, observe.ProviderGroq))

	got, err := eng.Generate(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "How much for a room?"}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.TrimSpace(got) != "That'll be 5 silver, traveler." {
		t.Errorf("delivered = %q, want the reply untouched", got)
	}
	if paths := rec.malformedPaths(); len(paths) != 0 {
		t.Errorf("MalformedToolGen paths = %v, want none (guard is armed-only)", paths)
	}
	if n := len(prov.requests()); n != 1 {
		t.Errorf("Complete calls = %d, want 1 (no regeneration on an unarmed turn)", n)
	}
}

// TestEngine_RegenMismatch_RetriesOnceThenDelivers is #438 AC "regen narrating a
// value ≠ actual Tool result does not ship as-is": the first forced regen claims a
// 17 without the dice Tool ever running, so it contradicts (no Tool result at all)
// and is discarded; the ONE bounded retry does a proper round-trip narrating the
// actual roll (the seeded 16) and ships. Counted: one roll_claim + one
// regen_mismatch, both bounded labels (#430).
func TestEngine_RegenMismatch_RetriesOnceThenDelivers(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		{text: "Ah, eine 19!", stop: "end_turn"},          // run 1: invented
		{text: "Trust me, a solid 17.", stop: "end_turn"}, // regen 1: claims a number, tool never ran → contradiction
		// Regen 2 (the one bounded retry): proper dice round-trip.
		{calls: []llm.ToolCall{{ID: "t1", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":20}`)}}, stop: "tool_use"},
		{text: "A 16! The bones favor you.", stop: "end_turn"},
	}}
	rec := &recordingStage{}
	eng := agenttool.NewEngine(prov, diceGrants(t), "", "m", 256, 0,
		agenttool.WithMetrics(rec, observe.ProviderGroq))

	got, err := eng.Generate(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "Roll a d20."}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.TrimSpace(got) != "A 16! The bones favor you." {
		t.Errorf("delivered = %q, want the consistent retry's reply", got)
	}
	if paths := rec.malformedPaths(); len(paths) != 2 ||
		paths[0] != observe.MalformedRollClaim || paths[1] != observe.MalformedRegenMismatch {
		t.Errorf("MalformedToolGen paths = %v, want [roll_claim regen_mismatch]", paths)
	}
	if n := len(prov.requests()); n != 4 {
		t.Errorf("Complete calls = %d, want 4 (buffered run + contradicting regen + retry round-trip)", n)
	}
}

// TestEngine_RegenMismatch_ContradictsRealRollRetries is the #438 headline: the
// forced regen DOES call the dice Tool (it rolls the seeded 16) but narrates a 20 —
// the invented-number-despite-rolling case. The contradicting draft is discarded
// and the bounded retry's consistent narration ships.
func TestEngine_RegenMismatch_ContradictsRealRollRetries(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		{text: "Ah, eine 19!", stop: "end_turn"}, // run 1: invented
		// Regen 1: calls dice (rolls 16)... then narrates a 20 anyway.
		{calls: []llm.ToolCall{{ID: "t1", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":20}`)}}, stop: "tool_use"},
		{text: "A natural 20! Incredible!", stop: "end_turn"},
		// Regen 2: rolls again (13) and narrates it faithfully.
		{calls: []llm.ToolCall{{ID: "t2", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":20}`)}}, stop: "tool_use"},
		{text: "The bones show a 13.", stop: "end_turn"},
	}}
	rec := &recordingStage{}
	eng := agenttool.NewEngine(prov, diceGrants(t), "", "m", 256, 0,
		agenttool.WithMetrics(rec, observe.ProviderGroq))

	got, err := eng.Generate(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "Roll a d20."}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.TrimSpace(got) != "The bones show a 13." {
		t.Errorf("delivered = %q, want the retry's faithful narration (the 20-claim must not ship)", got)
	}
	if paths := rec.malformedPaths(); len(paths) != 2 ||
		paths[0] != observe.MalformedRollClaim || paths[1] != observe.MalformedRegenMismatch {
		t.Errorf("MalformedToolGen paths = %v, want [roll_claim regen_mismatch]", paths)
	}
}

// TestEngine_RegenMismatch_BoundedThenFailsTurn pins the #438 bound: when the
// retry ALSO contradicts, the turn fails — never an unbounded loop, and the
// contradicting draft is NEVER delivered (ADR-0030). Exactly three loop runs
// (buffered + regen + one retry) and three bounded malformed labels.
func TestEngine_RegenMismatch_BoundedThenFailsTurn(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		{text: "Ah, eine 19!", stop: "end_turn"},          // run 1: invented
		{text: "Trust me, a solid 17.", stop: "end_turn"}, // regen 1: contradicts (no roll)
		{text: "Fine — an 11 then.", stop: "end_turn"},    // regen 2: still contradicts (no roll)
	}}
	rec := &recordingStage{}
	eng := agenttool.NewEngine(prov, diceGrants(t), "", "m", 256, 0,
		agenttool.WithMetrics(rec, observe.ProviderGroq))

	got, err := eng.Generate(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "Roll a d20."}})
	if err == nil {
		t.Fatal("Generate = nil error; want the turn to fail rather than speak a contradicting roll")
	}
	if got != "" {
		t.Errorf("delivered = %q; a contradicting draft must never surface", got)
	}
	if n := len(prov.requests()); n != 3 {
		t.Errorf("Complete calls = %d, want 3 (buffered + regen + ONE bounded retry)", n)
	}
	if paths := rec.malformedPaths(); len(paths) != 3 ||
		paths[0] != observe.MalformedRollClaim ||
		paths[1] != observe.MalformedRegenMismatch || paths[2] != observe.MalformedRegenMismatch {
		t.Errorf("MalformedToolGen paths = %v, want [roll_claim regen_mismatch regen_mismatch]", paths)
	}
}

// TestEngine_RegenWithoutClaim_ShipsUnchanged: a forced regen that makes NO
// numeric claim has nothing to contradict — it ships as-is even though the model
// (still) never called the dice Tool. The guard escalates on invented numbers,
// not on a number-free deflection.
func TestEngine_RegenWithoutClaim_ShipsUnchanged(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		{text: "Ah, eine 19!", stop: "end_turn"},                  // run 1: invented
		{text: "The fates keep their counsel.", stop: "end_turn"}, // regen: no claim, no roll
	}}
	rec := &recordingStage{}
	eng := agenttool.NewEngine(prov, diceGrants(t), "", "m", 256, 0,
		agenttool.WithMetrics(rec, observe.ProviderGroq))

	got, err := eng.Generate(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "Roll a d20."}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.TrimSpace(got) != "The fates keep their counsel." {
		t.Errorf("delivered = %q, want the claim-free regen unchanged", got)
	}
	if paths := rec.malformedPaths(); len(paths) != 1 || paths[0] != observe.MalformedRollClaim {
		t.Errorf("MalformedToolGen paths = %v, want exactly [roll_claim]", paths)
	}
	if n := len(prov.requests()); n != 2 {
		t.Errorf("Complete calls = %d, want 2 (buffered run + exactly one regeneration)", n)
	}
}

// TestEngine_InventedRoll_SpelledOutNumber is #438 item 1 at the engine level: a
// spelled-out invented result ("a natural twenty", no tool call) trips the guard
// exactly like a digit claim, and the forced regen's faithful narration ships.
func TestEngine_InventedRoll_SpelledOutNumber(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		{text: "You rolled a natural twenty! Astounding!", stop: "end_turn"}, // invented, spelled out
		{calls: []llm.ToolCall{{ID: "t1", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":20}`)}}, stop: "tool_use"},
		{text: "The bones show a 16, traveler.", stop: "end_turn"},
	}}
	rec := &recordingStage{}
	eng := agenttool.NewEngine(prov, diceGrants(t), "", "m", 256, 0,
		agenttool.WithMetrics(rec, observe.ProviderGroq))

	got, err := eng.Generate(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "Roll a d20."}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.TrimSpace(got) != "The bones show a 16, traveler." {
		t.Errorf("delivered = %q, want the regenerated faithful narration", got)
	}
	if paths := rec.malformedPaths(); len(paths) != 1 || paths[0] != observe.MalformedRollClaim {
		t.Errorf("MalformedToolGen paths = %v, want exactly [roll_claim]", paths)
	}
}

// guardRegenErrProvider narrates a bare roll result on its first (buffered) call,
// then start-errors every forced regeneration call — so the #399 guard's one
// escalation fails and the turn must propagate the error, never the invented draft.
type guardRegenErrProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *guardRegenErrProvider) Complete(_ context.Context, _ llm.Request) (<-chan llm.StreamEvent, error) {
	p.mu.Lock()
	p.calls++
	n := p.calls
	p.mu.Unlock()
	if n == 1 {
		ch := make(chan llm.StreamEvent)
		go func() {
			defer close(ch)
			ch <- llm.StreamEvent{Type: llm.EventText, Text: "Ah, eine 19! "}
			ch <- llm.StreamEvent{Type: llm.EventDone, StopReason: "end_turn"}
		}()
		return ch, nil
	}
	return nil, &providererr.HTTPError{Op: "test.Complete", StatusCode: 400, Status: "Bad Request", Body: "boom"}
}

// TestEngine_InventedRoll_RegenErrorPropagatesNeverDeliversDraft is AC-adjacent
// (ADR-0030, #398/#399): when the forced regeneration fails at the provider, the
// turn returns the error and NEVER delivers the discarded invented-roll draft.
func TestEngine_InventedRoll_RegenErrorPropagatesNeverDeliversDraft(t *testing.T) {
	prov := &guardRegenErrProvider{}
	rec := &recordingStage{}
	eng := agenttool.NewEngine(prov, diceGrants(t), "", "m", 256, 0,
		agenttool.WithMetrics(rec, observe.ProviderGroq),
		agenttool.WithRetry(instantAgentRetry()))

	got, err := eng.Generate(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "Roll a d20."}})
	if err == nil {
		t.Fatal("Generate returned nil; want the forced-regeneration provider error")
	}
	if got != "" {
		t.Errorf("delivered = %q on a failed regeneration; the discarded draft must never surface", got)
	}
}

// TestEngine_GenerateStream_ArmedTurnBuffersThenDeliversOnce is AC/TDD-9: on the
// streaming path a dice-armed turn BUFFERS — onText receives nothing until the guard
// clears, then the whole final text exactly once (never live word-by-word deltas),
// so an invented number can never be spoken mid-stream (ADR-0012).
func TestEngine_GenerateStream_ArmedTurnBuffersThenDeliversOnce(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		{calls: []llm.ToolCall{{ID: "t1", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":20}`)}}, stop: "tool_use"},
		{text: "You rolled a solid eight, traveler.", stop: "end_turn"},
	}}
	eng := agenttool.NewEngine(prov, diceGrants(t), "", "m", 256, 0)

	var deltas []string
	full, err := eng.GenerateStream(context.Background(),
		[]llm.Message{{Role: llm.RoleUser, Text: "Roll a d20 for me."}},
		func(delta string) error { deltas = append(deltas, delta); return nil })
	if err != nil {
		t.Fatalf("GenerateStream: %v", err)
	}
	if strings.TrimSpace(full) != "You rolled a solid eight, traveler." {
		t.Errorf("full = %q, want the buffered final text", full)
	}
	if len(deltas) != 1 {
		t.Fatalf("onText called %d times, want exactly 1 (whole-text push, buffered — not live deltas)", len(deltas))
	}
	if strings.TrimSpace(deltas[0]) != "You rolled a solid eight, traveler." {
		t.Errorf("single delta = %q, want the whole final text", deltas[0])
	}
}

// usageThenToolSyntaxProvider reports usage then fails tool-syntax on its first
// `fails` calls (a mid-stream tool_use_failed AFTER an EventUsage), then streams the
// answer with its own usage. It drives the #399 drive-by A: a discarded failed
// attempt's provider-REPORTED usage must still be metered (ADR-0045).
type usageThenToolSyntaxProvider struct {
	mu         sync.Mutex
	calls      int
	fails      int
	failUsage  llm.Usage
	answer     string
	finalUsage llm.Usage
}

func (p *usageThenToolSyntaxProvider) Complete(_ context.Context, _ llm.Request) (<-chan llm.StreamEvent, error) {
	p.mu.Lock()
	p.calls++
	n := p.calls
	p.mu.Unlock()
	ch := make(chan llm.StreamEvent, 3)
	if n <= p.fails {
		ch <- llm.StreamEvent{Type: llm.EventUsage, Usage: p.failUsage}
		ch <- llm.StreamEvent{Type: llm.EventError, Err: "tool_use_failed", ErrClass: llm.ErrClassToolSyntax}
		close(ch)
		return ch, nil
	}
	go func() {
		defer close(ch)
		ch <- llm.StreamEvent{Type: llm.EventText, Text: p.answer}
		ch <- llm.StreamEvent{Type: llm.EventUsage, Usage: p.finalUsage}
		ch <- llm.StreamEvent{Type: llm.EventDone, StopReason: "end_turn"}
	}()
	return ch, nil
}

// TestEngine_ToolSyntax_RetriedAttemptReportedUsageMetered is #399 drive-by A: a
// tool-syntax attempt that already reported usage before failing is discarded on
// retry, but its provider-REPORTED tokens are still metered — never dropped, never
// estimated (ADR-0045). Two LLMTokens spans: the failed attempt's + the final round's.
func TestEngine_ToolSyntax_RetriedAttemptReportedUsageMetered(t *testing.T) {
	prov := &usageThenToolSyntaxProvider{
		fails:      1,
		failUsage:  llm.Usage{InputTokens: 10, OutputTokens: 2},
		answer:     "You rolled well.",
		finalUsage: llm.Usage{InputTokens: 40, OutputTokens: 7},
	}
	rec := &recordingStage{}
	eng := agenttool.NewEngine(prov, diceGrants(t), "", "m", 256, 0,
		agenttool.WithMetrics(rec, observe.ProviderGroq),
		agenttool.WithRetry(instantAgentRetry()))

	if _, err := eng.Generate(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "Roll a d20 for me."}}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	toks := rec.tokenSpans()
	if len(toks) != 2 {
		t.Fatalf("LLMTokens spans = %d, want 2 (discarded attempt's reported usage + final round)", len(toks))
	}
	var sawFail, sawFinal bool
	for _, tk := range toks {
		if tk.in == 10 && tk.out == 2 {
			sawFail = true
		}
		if tk.in == 40 && tk.out == 7 {
			sawFinal = true
		}
	}
	if !sawFail {
		t.Error("the discarded tool-syntax attempt's reported usage (10/2) was dropped; want it metered")
	}
	if !sawFinal {
		t.Error("the final recovered round's usage (40/7) was not metered")
	}
}

// TestEngine_PseudoXMLTextLeak_MetersMalformedTextLeak pins the #410 wiring: when
// the model emits a tool call as malformed TEXT content (no provider error), the
// agenttool bridge counts it on MalformedToolGen with the text_leak path — the
// same observability family as #398's stream_error — and the leak never reaches
// the returned (spoken/persisted) text.
func TestEngine_PseudoXMLTextLeak_MetersMalformedTextLeak(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		{text: `Rolling. <function=dice {"count":1,"sides":20}</function>`, stop: "end_turn"},
		{text: "You rolled twelve.", stop: "end_turn"},
	}}
	rec := &recordingStage{}
	eng := agenttool.NewEngine(prov, diceGrants(t), "", "claude-test", 256, 0,
		agenttool.WithMetrics(rec, observe.ProviderGroq))

	final, err := eng.Generate(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Text: "Roll a d20 for me."},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.Contains(final, "<function") || strings.Contains(final, "</function") {
		t.Errorf("returned text leaked the pseudo-call: %q", final)
	}
	paths := rec.malformedPaths()
	if len(paths) != 1 || paths[0] != observe.MalformedTextLeak {
		t.Errorf("MalformedToolGen paths = %v, want exactly [text_leak]", paths)
	}
}

// TestEngine_RecoveredPseudoDice_SuppressesInventedRollGuard is the #399 review
// gate: on a dice-armed turn the model emits its dice call as pseudo-XML TEXT (no
// provider error). pkg/tool recovers it into a REAL executed ToolCall (#410) that
// the adapter never saw as a provider-native call — but the OnPseudoCall(recovered)
// seam marks "dice" in the turn's called-Tools set, so the invented-roll guard does
// NOT fire: exactly one loop run (no forced regeneration), NO roll_claim counted
// (only the text_leak from the recovery), and the delivered text is the follow-up
// round's answer built on the real roll.
func TestEngine_RecoveredPseudoDice_SuppressesInventedRollGuard(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		// Round 0: the dice call arrives as pseudo-XML text — recovered + executed.
		{text: `Let me roll. <function=dice {"count":1,"sides":20}</function>`, stop: "end_turn"},
		// Round 1: answers with the real result in context.
		{text: "The bones show a 14, traveler.", stop: "end_turn"},
	}}
	rec := &recordingStage{}
	eng := agenttool.NewEngine(prov, diceGrants(t), "", "m", 256, 0,
		agenttool.WithMetrics(rec, observe.ProviderGroq))

	got, err := eng.Generate(context.Background(), []llm.Message{{Role: llm.RoleUser, Text: "Roll a d20 for me."}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.TrimSpace(got) != "The bones show a 14, traveler." {
		t.Errorf("delivered = %q, want the follow-up round's answer (no regeneration)", got)
	}
	// text_leak counted once by the recovery; NO roll_claim — the guard saw dice called.
	if paths := rec.malformedPaths(); len(paths) != 1 || paths[0] != observe.MalformedTextLeak {
		t.Errorf("MalformedToolGen paths = %v, want exactly [text_leak] (recovered pseudo-dice must not trip the roll-claim guard)", paths)
	}
	// Exactly one loop run: round 0 (recovered call) + round 1 (answer) = 2 requests,
	// and no forced-regeneration round.
	reqs := prov.requests()
	if len(reqs) != 2 {
		t.Fatalf("Complete calls = %d, want 2 (recovered round-trip, no regeneration)", len(reqs))
	}
	for i, r := range reqs {
		if r.ToolChoice.Mode == llm.ToolChoiceTool {
			t.Errorf("req[%d] used forced tool choice %+v; want no forced regeneration", i, r.ToolChoice)
		}
	}
}
