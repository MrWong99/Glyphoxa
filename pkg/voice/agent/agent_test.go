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
	"github.com/MrWong99/Glyphoxa/pkg/voice/providererr"
	"github.com/MrWong99/Glyphoxa/pkg/voice/retry"
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

// flakyStartProvider start-errors its first errsBeforeOK Complete calls (each the
// pinned err) then streams reply, counting calls so a retry test can prove the
// default engine re-drove the LLM start (#124).
type flakyStartProvider struct {
	mu           sync.Mutex
	err          error
	errsBeforeOK int
	reply        string
	calls        int
}

func (f *flakyStartProvider) Complete(context.Context, llm.Request) (<-chan llm.StreamEvent, error) {
	f.mu.Lock()
	f.calls++
	n := f.calls
	f.mu.Unlock()
	if n <= f.errsBeforeOK {
		return nil, f.err
	}
	ch := make(chan llm.StreamEvent)
	go func() {
		defer close(ch)
		for _, w := range strings.Fields(f.reply) {
			ch <- llm.StreamEvent{Type: llm.EventText, Text: w + " "}
		}
		ch <- llm.StreamEvent{Type: llm.EventDone, StopReason: "end_turn"}
	}()
	return ch, nil
}

func (f *flakyStartProvider) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestReply_DefaultEngine_RetriesTransientStartError pins that the default
// (no-tool) engine retries a transient LLM start-error via [Config.Retry]: one
// 503 then success produces the reply and calls the provider twice. The injected
// Sleep keeps the test off the wall clock (ADR-0021).
func TestReply_DefaultEngine_RetriesTransientStartError(t *testing.T) {
	prov := &flakyStartProvider{
		err:          &providererr.HTTPError{Op: "anthropic.Complete", StatusCode: 503, Status: "503 Service Unavailable", Body: "down"},
		errsBeforeOK: 1,
		reply:        "Aye, traveler.",
	}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Provider:    prov,
		Synthesizer: stubSynth{},
		Retry: retry.Policy{
			Sleep: func(context.Context, time.Duration) error { return nil },
			Rand:  func() float64 { return 0 },
		},
	})

	got := r.Reply()(t.Context(), routed("bart", "Hail."))
	if len(got) == 0 {
		t.Fatal("no reply produced after one transient 503")
	}
	var b strings.Builder
	for _, rep := range got {
		b.WriteString(rep.Sentence)
	}
	if !strings.Contains(b.String(), "traveler") {
		t.Errorf("reply = %q, want the answer after the retry", b.String())
	}
	if prov.callCount() != 2 {
		t.Errorf("provider Complete calls = %d, want 2 (one 503 retried once)", prov.callCount())
	}
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

// deliver simulates the dispatch site delivering every returned Reply (#362): it
// invokes each non-nil OnDelivered hook, committing the batch turn's assistant
// message to history exactly as a clean spoken turn would. Tests that build
// multi-turn history call it so the batch commit-on-delivery change (assistant no
// longer committed eagerly) does not silently drop the prior turn's reply.
func deliver(replies []orchestrator.Reply) []orchestrator.Reply {
	for _, rep := range replies {
		if rep.OnDelivered != nil {
			rep.OnDelivered()
		}
	}
	return replies
}

// lastUserMessage returns the text of the LAST user-role message in msgs — the
// current utterance. Since ADR-0059 the volatile Hot Context tail (facts,
// memory, GM directive, cross-talk instruction) trails the conversation as a
// system-role message, so "the last message" is no longer necessarily the user
// line.
func lastUserText(t *testing.T, msgs []llm.Message) string {
	t.Helper()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleUser {
			return msgs[i].Text
		}
	}
	t.Fatal("no user message in the assembled conversation")
	return ""
}

// volatileTail returns the text of the trailing volatile Hot Context message
// (ADR-0059) — a system-role message AFTER the first — or "" when the turn
// appended none (no facts, no memory, no directive).
func volatileTail(t *testing.T, msgs []llm.Message) string {
	t.Helper()
	if len(msgs) < 2 {
		return ""
	}
	if last := msgs[len(msgs)-1]; last.Role == llm.RoleSystem {
		return last.Text
	}
	return ""
}

func routed(agentID, text string) voiceevent.AddressRouted {
	return voiceevent.AddressRouted{
		At:     time.Now(),
		Text:   text,
		Target: voiceevent.AddressTarget{AgentID: agentID, AgentRole: "character", Name: "Bart"},
	}
}

// routedFrom is routed with the utterance's Speaker Lane attribution set — the
// ADR-0050 SpeakerID the detector copies off the STTFinal.
func routedFrom(agentID, speakerID, text string) voiceevent.AddressRouted {
	e := routed(agentID, text)
	e.SpeakerID = speakerID
	return e
}

// namerFor is a [agent.Config.SpeakerName] stub over a fixed map: a cache-only
// lookup that never blocks, returning "" for an unknown speaker.
func namerFor(names map[string]string) func(string) string {
	return func(speakerID string) string { return names[speakerID] }
}

// TestReply_SpeakerName_PrefixesUserLine pins the agent-facing transcript
// attribution: with a SpeakerName resolver configured, the utterance enters the
// LLM conversation as "<Name>: <text>" — both in the engine-received prompt and
// in the committed history — so multiple humans' turns stay distinguishable. The
// assistant reply stays UNPREFIXED (RoleAssistant is self-attribution).
func TestReply_SpeakerName_PrefixesUserLine(t *testing.T) {
	prov := &fakeProvider{reply: "Well met, Artusas."}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Provider:    prov,
		Synthesizer: stubSynth{},
		SpeakerName: namerFor(map[string]string{"111": "Artusas"}),
	})

	deliver(r.Reply()(t.Context(), routedFrom("bart", "111", "hey how are you?")))

	req := prov.lastRequest(t)
	last := req.Messages[len(req.Messages)-1]
	if last.Role != llm.RoleUser || last.Text != "Artusas: hey how are you?" {
		t.Errorf("user message = {%s %q}, want the name-prefixed line", last.Role, last.Text)
	}
	hist := r.HistorySnapshot()
	if len(hist) != 2 {
		t.Fatalf("history len = %d, want 2 (user + assistant)", len(hist))
	}
	if hist[0].Role != llm.RoleUser || hist[0].Text != "Artusas: hey how are you?" {
		t.Errorf("committed user line = {%s %q}, want the name-prefixed line", hist[0].Role, hist[0].Text)
	}
	if hist[1].Role != llm.RoleAssistant || hist[1].Text != "Well met, Artusas." {
		t.Errorf("assistant line = {%s %q}, want the reply UNPREFIXED", hist[1].Role, hist[1].Text)
	}
}

// TestReply_SpeakerName_UnknownSpeakerLabeledPlayerDM pins the degraded label: a
// resolver miss (cold cache, unmapped speaker, or an unattributed lane) labels
// the line "Player / DM" — the relay/chunker's generic-human label (#281) — so
// the prompt still marks the line as human speech rather than dropping the seam.
func TestReply_SpeakerName_UnknownSpeakerLabeledPlayerDM(t *testing.T) {
	for _, tc := range []struct {
		name      string
		speakerID string
	}{
		{"resolver-miss", "999"},
		{"unattributed-lane", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prov := &fakeProvider{reply: "Aye."}
			r := agent.NewReplier(agent.Config{
				Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
				Provider:    prov,
				Synthesizer: stubSynth{},
				SpeakerName: namerFor(map[string]string{"111": "Artusas"}),
			})

			deliver(r.Reply()(t.Context(), routedFrom("bart", tc.speakerID, "hey how are you?")))

			req := prov.lastRequest(t)
			last := req.Messages[len(req.Messages)-1]
			if last.Text != "Player / DM: hey how are you?" {
				t.Errorf("user message = %q, want the generic Player / DM label", last.Text)
			}
		})
	}
}

// TestReply_NilSpeakerName_BareTextBytesIdentical pins the off default: without a
// SpeakerName resolver (voice standalone, the benchmark, every pre-seam caller)
// the user message is the BARE utterance — byte-identical to the pre-attribution
// prompt — even when the route carries a SpeakerID.
func TestReply_NilSpeakerName_BareTextBytesIdentical(t *testing.T) {
	prov := &fakeProvider{reply: "Aye."}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Provider:    prov,
		Synthesizer: stubSynth{},
	})

	deliver(r.Reply()(t.Context(), routedFrom("bart", "111", "hey how are you?")))

	req := prov.lastRequest(t)
	last := req.Messages[len(req.Messages)-1]
	if last.Text != "hey how are you?" {
		t.Errorf("user message = %q, want the bare utterance (nil namer)", last.Text)
	}
}

// recordingRecaller records the utterance argument of every Recall so a test can
// pin what the memory retrieval is keyed on.
type recordingRecaller struct {
	mu         sync.Mutex
	utterances []string
}

func (rr *recordingRecaller) Recall(_ context.Context, _, utterance string) agent.Memory {
	rr.mu.Lock()
	rr.utterances = append(rr.utterances, utterance)
	rr.mu.Unlock()
	return agent.Memory{}
}

func (rr *recordingRecaller) got() []string {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	return append([]string(nil), rr.utterances...)
}

// TestReply_SpeakerName_RecallKeyedOnRawUtterance pins the ADR-0042 constraint:
// memory recall stays keyed on the RAW utterance text, never the name-prefixed
// history line — the speculative-recall match compares the normalized STT final
// against queries embedded from STT partials, which carry no prefix, so a
// prefixed key would silently degrade every speculation hit to inline retrieval.
func TestReply_SpeakerName_RecallKeyedOnRawUtterance(t *testing.T) {
	rec := &recordingRecaller{}
	prov := &fakeProvider{reply: "Aye."}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Provider:    prov,
		Synthesizer: stubSynth{},
		Memory:      rec,
		SpeakerName: namerFor(map[string]string{"111": "Artusas"}),
	})

	deliver(r.Reply()(t.Context(), routedFrom("bart", "111", "hey how are you?")))

	got := rec.got()
	if len(got) != 1 || got[0] != "hey how are you?" {
		t.Errorf("Recall keyed on %q, want the RAW utterance %q (ADR-0042)", got, "hey how are you?")
	}
}

// TestReplyStream_SpeakerName_PrefixesUserLine pins the same attribution on the
// streaming path — the one production wires — including the raw-keyed recall.
func TestReplyStream_SpeakerName_PrefixesUserLine(t *testing.T) {
	rec := &recordingRecaller{}
	eng := &fakeStreamEngine{deltas: []string{"Well met."}}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Engine:      eng,
		Synthesizer: stubSynth{},
		Memory:      rec,
		SpeakerName: namerFor(map[string]string{"111": "Artusas"}),
	})

	if err := r.ReplyStream()(context.Background(), routedFrom("bart", "111", "hey how are you?"), func(orchestrator.Reply) error { return nil }); err != nil {
		t.Fatalf("ReplyStream: %v", err)
	}

	hist := r.HistorySnapshot()
	if len(hist) != 2 || hist[0].Text != "Artusas: hey how are you?" {
		t.Fatalf("history = %+v, want the name-prefixed user line first", hist)
	}
	if hist[1].Role != llm.RoleAssistant || hist[1].Text != "Well met." {
		t.Errorf("assistant line = {%s %q}, want the delivered text UNPREFIXED", hist[1].Role, hist[1].Text)
	}
	if got := rec.got(); len(got) != 1 || got[0] != "hey how are you?" {
		t.Errorf("Recall keyed on %q, want the RAW utterance (ADR-0042)", got)
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

	deliver(reply(t.Context(), routed("bart", "Do you have rooms?")))
	deliver(reply(t.Context(), routed("bart", "And a meal?")))

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

	deliver(reply(t.Context(), routed("bart", "first-utterance")))
	deliver(reply(t.Context(), routed("bart", "second-utterance")))
	deliver(reply(t.Context(), routed("bart", "third-utterance")))

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

// TestReply_ProviderError_SpeaksFallbackAndReportsError pins the no-error seam
// plus the terminal-error fallback: a [orchestrator.ReplyFunc] cannot return an
// error, so a failed completion surfaces via OnError — and instead of dead air
// the turn now returns ONE Reply carrying the canned fallback line in the
// Persona's Voice, with NO commit hook (the line is not the model's words and
// must never enter history).
func TestReply_ProviderError_SpeaksFallbackAndReportsError(t *testing.T) {
	wantErr := errors.New("provider boom")
	var gotErr error
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Provider:    &fakeProvider{err: wantErr},
		Synthesizer: stubSynth{},
		OnError:     func(err error) { gotErr = err },
	})

	got := r.Reply()(t.Context(), routed("bart", "Hello."))
	if !errors.Is(gotErr, wantErr) {
		t.Errorf("OnError got %v, want %v", gotErr, wantErr)
	}
	if len(got) != 1 {
		t.Fatalf("reply on provider error = %+v, want exactly the fallback line", got)
	}
	if got[0].Sentence != agent.DefaultFallbackLine {
		t.Errorf("fallback sentence = %q, want DefaultFallbackLine", got[0].Sentence)
	}
	if v := got[0].Voice; v.ProviderID != "elevenlabs" || v.VoiceID != "v1" {
		t.Errorf("fallback voice = %+v, want the Persona's voice", v)
	}
	if got[0].OnDelivered != nil {
		t.Error("fallback Reply must carry no OnDelivered hook (never committed to history)")
	}
	deliver(got)
	for _, m := range r.HistorySnapshot() {
		if m.Role == llm.RoleAssistant {
			t.Fatalf("fallback line committed to history: %q", m.Text)
		}
	}
}

// TestReply_EngineError_CustomFallbackLine pins the [agent.Config.FallbackLine]
// override: a configured line replaces the default on the terminal-error path.
func TestReply_EngineError_CustomFallbackLine(t *testing.T) {
	r := agent.NewReplier(agent.Config{
		Persona:      agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Provider:     &fakeProvider{err: errors.New("boom")},
		Synthesizer:  stubSynth{},
		FallbackLine: "Einen Moment, Reisender.",
	})

	got := r.Reply()(t.Context(), routed("bart", "Hello."))
	if len(got) != 1 || got[0].Sentence != "Einen Moment, Reisender." {
		t.Fatalf("reply = %+v, want the configured fallback line", got)
	}
}

// TestReply_CommitsOnlyOnDelivered pins the batch residual (#362, ADR-0012): the
// batch [Replier.Reply] no longer commits the assistant turn EAGERLY. It returns a
// Reply carrying a non-nil OnDelivered hook; the assistant message lands in history
// ONLY when the dispatch site invokes that hook (i.e. the sentence was delivered).
func TestReply_CommitsOnlyOnDelivered(t *testing.T) {
	prov := &fakeProvider{reply: "Welcome, traveler."}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Provider:    prov,
		Synthesizer: stubSynth{},
	})

	got := r.Reply()(t.Context(), routed("bart", "Hello."))
	if len(got) != 1 {
		t.Fatalf("got %d replies, want 1", len(got))
	}
	if got[0].OnDelivered == nil {
		t.Fatal("batch Reply must carry a non-nil OnDelivered commit hook (#362)")
	}
	// BEFORE the hook fires: the assistant turn must NOT be in history yet (this is
	// the RED against the old eager commit).
	for _, m := range r.HistorySnapshot() {
		if m.Role == llm.RoleAssistant {
			t.Fatalf("assistant committed BEFORE delivery: %q", m.Text)
		}
	}
	// AFTER the hook fires: the assistant turn is committed.
	got[0].OnDelivered()
	hist := r.HistorySnapshot()
	assistant := hist[len(hist)-1]
	if string(assistant.Role) != "assistant" || assistant.Text != "Welcome, traveler." {
		t.Fatalf("committed assistant turn = {%s %q}, want the delivered reply", assistant.Role, assistant.Text)
	}
}

// TestReply_ZeroDelivered_NothingLogged pins the batch zero-delivered rule (#362,
// ADR-0012): if the OnDelivered hook is never invoked (nothing reached the room),
// the user message is still committed (parity with the streaming path) but the
// assistant message is ABSENT — and a follow-up turn's prompt carries no ghost
// reply.
func TestReply_ZeroDelivered_NothingLogged(t *testing.T) {
	prov := &fakeProvider{reply: "Ghost reply."}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Provider:    prov,
		Synthesizer: stubSynth{},
	})
	reply := r.Reply()

	reply(t.Context(), routed("bart", "first utterance")) // hook never invoked → not delivered

	var sawUser, sawAssistant bool
	for _, m := range r.HistorySnapshot() {
		switch m.Role {
		case llm.RoleUser:
			sawUser = true
		case llm.RoleAssistant:
			sawAssistant = true
		}
	}
	if !sawUser {
		t.Fatal("user message must be committed eagerly (parity with streamTurn)")
	}
	if sawAssistant {
		t.Fatal("assistant message must be ABSENT when nothing was delivered")
	}

	// A follow-up turn's prompt must not carry the undelivered ghost reply.
	reply(t.Context(), routed("bart", "second utterance"))
	for _, m := range prov.lastRequest(t).Messages {
		if m.Role == llm.RoleAssistant {
			t.Fatalf("second-turn prompt carries a ghost assistant reply %q, want none", m.Text)
		}
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

// TestReplyStream_BargeMidStream_CommitsOnlySpoken pins the barge requirement and
// the ADR-0012 deliver-then-commit rule: when the turn is cancelled after the
// first sentence is spoken, (1) no further sentence is dispatched (pending
// sentences stop), and (2) only the spoken sentence is committed to history —
// not the untruncated completion.
//
// Scope note: the second turn is fired AFTER the first turn's goroutine has fully
// unwound (the <-done barrier), so this pins the committed CONTENT
// deterministically. It does NOT exercise the production interleaving where
// a zero barge confirm window lets turn 2 route while turn 1 is still committing — there,
// turn-1 unwind (~a few statements after cancel) precedes turn-2 routing
// (STT→address→bus fan-out) in practice, but ordering is not guaranteed by
// construction (only r.mu's mutual exclusion is). If a real ordering bug ever
// surfaces, a turn-sequence guard is the fix; today the mutex is sufficient.
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

// TestReplyStream_DispatchRejected_NotCommitted pins deliver-then-commit at the
// emit seam (ADR-0012): a sentence is committed to history only if its dispatch
// returned nil (delivered). When dispatch rejects sentence #2 (a turn cancelled
// between the two select-ready branches), only the delivered sentence #1 lands
// in the committed assistant turn — not the rejected one.
func TestReplyStream_DispatchRejected_NotCommitted(t *testing.T) {
	eng := &fakeStreamEngine{deltas: []string{"First. ", "Second. "}}
	r := streamReplier(t, eng)

	var n int
	err := r.ReplyStream()(context.Background(), routed("bart", "go"), func(orchestrator.Reply) error {
		n++
		if n == 1 {
			return nil // delivered
		}
		return context.Canceled // turn cancelled mid-drain: #2 never delivered
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReplyStream err = %v, want context.Canceled", err)
	}
	hist := r.HistorySnapshot()
	assistant := hist[len(hist)-1]
	if string(assistant.Role) != "assistant" || strings.TrimSpace(assistant.Text) != "First." {
		t.Fatalf("committed assistant turn = {%s %q}, want only the delivered sentence \"First.\"", assistant.Role, assistant.Text)
	}
}

// batchEngine is a non-streaming [agent.Engine]: it implements Generate but NOT
// GenerateStream, so ReplyStream falls back to fallbackTurn (the single-completion
// path).
type batchEngine struct{ reply string }

func (e batchEngine) Generate(context.Context, []llm.Message) (string, error) {
	return e.reply, nil
}

// TestReplyStream_FallbackCancelled_CommitsNothing pins deliver-then-commit on the
// non-streaming fallback path: if dispatch of the single reply is rejected (the
// turn was cancelled), nothing was delivered, so NO assistant message is committed
// (ADR-0012 zero-delivered rule).
func TestReplyStream_FallbackCancelled_CommitsNothing(t *testing.T) {
	r := streamReplier(t, batchEngine{reply: "The whole answer."})

	err := r.ReplyStream()(context.Background(), routed("bart", "go"), func(orchestrator.Reply) error {
		return context.Canceled
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReplyStream err = %v, want context.Canceled", err)
	}
	for _, m := range r.HistorySnapshot() {
		if m.Role == llm.RoleAssistant {
			t.Fatalf("committed assistant turn %q, want none (nothing delivered)", m.Text)
		}
	}
}

// TestReplyStream_StartError_SentenceNotCommitted_TurnSurvives pins the streaming
// residual (#362, ADR-0012): when dispatch returns [orchestrator.ErrNotDelivered]
// for sentence 1 (a TTS start-error under a live turn), that sentence is NOT
// committed but the turn SURVIVES — sentence 2, delivered, IS committed. The
// resulting history assistant turn is sentence 2 only (an intended mid-turn gap:
// the room heard exactly that), and streamTurn returns nil.
func TestReplyStream_StartError_SentenceNotCommitted_TurnSurvives(t *testing.T) {
	eng := &fakeStreamEngine{deltas: []string{"First. ", "Second. "}}
	r := streamReplier(t, eng)

	var n int
	err := r.ReplyStream()(context.Background(), routed("bart", "go"), func(orchestrator.Reply) error {
		n++
		if n == 1 {
			return orchestrator.ErrNotDelivered // start-error, turn stays alive
		}
		return nil // delivered
	})
	if err != nil {
		t.Fatalf("ReplyStream err = %v, want nil (a start-error must not fail the turn)", err)
	}
	hist := r.HistorySnapshot()
	assistant := hist[len(hist)-1]
	if string(assistant.Role) != "assistant" || strings.TrimSpace(assistant.Text) != "Second." {
		t.Fatalf("committed assistant turn = {%s %q}, want only the delivered sentence \"Second.\"", assistant.Role, assistant.Text)
	}
}

// TestReplyStream_FallbackStartError_CommitsNothing_NoProviderError pins the
// fallback residual (#362): when the single-completion fallback's dispatch returns
// ErrNotDelivered (a start-error), nothing was delivered so NO assistant message is
// committed, AND the sentinel is swallowed (nil return) — returning it up would
// misclassify the turn as provider_error in dispatchStream.
func TestReplyStream_FallbackStartError_CommitsNothing_NoProviderError(t *testing.T) {
	r := streamReplier(t, batchEngine{reply: "The whole answer."})

	err := r.ReplyStream()(context.Background(), routed("bart", "go"), func(orchestrator.Reply) error {
		return orchestrator.ErrNotDelivered
	})
	if err != nil {
		t.Fatalf("fallback ReplyStream err = %v, want nil (ErrNotDelivered must be swallowed)", err)
	}
	for _, m := range r.HistorySnapshot() {
		if m.Role == llm.RoleAssistant {
			t.Fatalf("committed assistant turn %q, want none (nothing delivered)", m.Text)
		}
	}
}

// errEngine is an [agent.Engine] whose Generate always fails terminally — the
// provider-outage / exhausted-tool-budget shape. It implements only Generate,
// so a streaming Replier routes it through fallbackTurn.
type errEngine struct{ err error }

func (e errEngine) Generate(context.Context, []llm.Message) (string, error) { return "", e.err }

// errStreamEngine is a [agent.StreamingEngine] that forwards its deltas (each a
// sentence already "spoken") and then fails terminally with err. No deltas
// models an engine dying before ANY audio; a pre-cancelled ctx returns the ctx
// error, mirroring a real engine's barge unwind.
type errStreamEngine struct {
	deltas []string
	err    error
}

func (e *errStreamEngine) Generate(context.Context, []llm.Message) (string, error) {
	return "", e.err
}

func (e *errStreamEngine) GenerateStream(ctx context.Context, _ []llm.Message, onText func(string) error) (string, error) {
	var full strings.Builder
	for _, d := range e.deltas {
		if err := ctx.Err(); err != nil {
			return full.String(), err
		}
		full.WriteString(d)
		if err := onText(d); err != nil {
			return full.String(), err
		}
	}
	if err := ctx.Err(); err != nil {
		return full.String(), err
	}
	return full.String(), e.err
}

// TestReplyStream_EngineErrorBeforeAudio_SpeaksFallback pins the streaming half
// of the terminal-error fallback: an engine that dies before ANY audio was
// dispatched still reports via OnError, then speaks the canned fallback line and
// returns nil — the turn counts delivered (first_audio), not abandoned. The
// canned line is never committed to history.
func TestReplyStream_EngineErrorBeforeAudio_SpeaksFallback(t *testing.T) {
	wantErr := errors.New("groq boom")
	var gotErr error
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Engine:      &errStreamEngine{err: wantErr},
		Synthesizer: stubSynth{},
		OnError:     func(err error) { gotErr = err },
	})

	var got []string
	err := r.ReplyStream()(context.Background(), routed("bart", "hi"), func(rep orchestrator.Reply) error {
		got = append(got, rep.Sentence)
		return nil
	})
	if err != nil {
		t.Fatalf("ReplyStream err = %v, want nil (the fallback turn counts delivered)", err)
	}
	if !errors.Is(gotErr, wantErr) {
		t.Errorf("OnError got %v, want %v (still reported before the fallback)", gotErr, wantErr)
	}
	if len(got) != 1 || got[0] != agent.DefaultFallbackLine {
		t.Fatalf("dispatched = %q, want exactly the fallback line", got)
	}
	for _, m := range r.HistorySnapshot() {
		if m.Role == llm.RoleAssistant {
			t.Fatalf("fallback line committed to history: %q", m.Text)
		}
	}
}

// TestReplyStream_EngineErrorAfterAudio_NoFallback pins the audio-already-out
// rule: once a sentence was delivered, a mid-completion engine death appends NO
// canned line (the room heard a partial reply; a non-sequitur stall would make
// it worse). The delivered sentence is committed and the error propagates.
func TestReplyStream_EngineErrorAfterAudio_NoFallback(t *testing.T) {
	wantErr := errors.New("mid-stream boom")
	var gotErr error
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Engine:      &errStreamEngine{deltas: []string{"Aye, two rooms. "}, err: wantErr},
		Synthesizer: stubSynth{},
		OnError:     func(err error) { gotErr = err },
	})

	var got []string
	err := r.ReplyStream()(context.Background(), routed("bart", "rooms?"), func(rep orchestrator.Reply) error {
		got = append(got, rep.Sentence)
		return nil
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("ReplyStream err = %v, want the engine error (audio already out → no fallback rescue)", err)
	}
	if !errors.Is(gotErr, wantErr) {
		t.Errorf("OnError got %v, want %v", gotErr, wantErr)
	}
	if len(got) != 1 || got[0] != "Aye, two rooms." {
		t.Fatalf("dispatched = %q, want only the delivered sentence (no fallback appended)", got)
	}
	if !committedAssistant(r, "Aye, two rooms.") {
		t.Errorf("delivered sentence not committed: %+v", r.HistorySnapshot())
	}
}

// TestReplyStream_BargeCancel_NoFallbackNoOnError pins the barge exclusion: a
// cancelled turn ctx produces neither a fallback line nor an OnError — the
// superseded turn stays silent, exactly the pre-fallback barge semantics.
func TestReplyStream_BargeCancel_NoFallbackNoOnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var onErrCalls int
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Engine:      &errStreamEngine{err: errors.New("unreached")},
		Synthesizer: stubSynth{},
		OnError:     func(error) { onErrCalls++ },
	})

	var dispatched int
	err := r.ReplyStream()(ctx, routed("bart", "hi"), func(orchestrator.Reply) error {
		dispatched++
		return nil
	})
	if err != nil {
		t.Fatalf("ReplyStream err = %v, want nil (a cancel is the expected barge path)", err)
	}
	if dispatched != 0 {
		t.Errorf("dispatched %d sentences on a barged turn, want 0 (no fallback)", dispatched)
	}
	if onErrCalls != 0 {
		t.Errorf("OnError called %d times on a barge cancel, want 0", onErrCalls)
	}
}

// TestReplyStream_FallbackEngineError_SpeaksFallback pins the non-streaming
// fallbackTurn path: a batch-only engine that fails terminally still speaks the
// canned line and reports the turn delivered (nil), after OnError.
func TestReplyStream_FallbackEngineError_SpeaksFallback(t *testing.T) {
	wantErr := errors.New("batch boom")
	var gotErr error
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Engine:      errEngine{err: wantErr},
		Synthesizer: stubSynth{},
		OnError:     func(err error) { gotErr = err },
	})

	var got []string
	err := r.ReplyStream()(context.Background(), routed("bart", "hi"), func(rep orchestrator.Reply) error {
		got = append(got, rep.Sentence)
		return nil
	})
	if err != nil {
		t.Fatalf("ReplyStream err = %v, want nil (the fallback turn counts delivered)", err)
	}
	if !errors.Is(gotErr, wantErr) {
		t.Errorf("OnError got %v, want %v", gotErr, wantErr)
	}
	if len(got) != 1 || got[0] != agent.DefaultFallbackLine {
		t.Fatalf("dispatched = %q, want exactly the fallback line", got)
	}
	for _, m := range r.HistorySnapshot() {
		if m.Role == llm.RoleAssistant {
			t.Fatalf("fallback line committed to history: %q", m.Text)
		}
	}
}

// TestReplyStream_TextSink_EngineError_FallbackOnlyWhenVoiced pins the Butler
// path (textModalityTurn): a VOICED Butler speaks the canned fallback on a
// terminal engine error (never posting to the TextSink — there is no answer to
// post), while a VOICELESS Butler stays silent and propagates the error — an
// empty VoiceID must never reach TTS (the structural-unreachability guarantee).
func TestReplyStream_TextSink_EngineError_FallbackOnlyWhenVoiced(t *testing.T) {
	wantErr := errors.New("butler boom")

	t.Run("voiced butler speaks the fallback", func(t *testing.T) {
		r := textSinkReplier(t, errEngine{err: wantErr}, false, func(context.Context, string) error {
			t.Error("a failed turn must not post to the TextSink")
			return nil
		})
		var got []string
		err := r.ReplyStream()(context.Background(), routed("butler", "Glyphoxa, recap"), func(rep orchestrator.Reply) error {
			got = append(got, rep.Sentence)
			return nil
		})
		if err != nil {
			t.Fatalf("ReplyStream err = %v, want nil (the fallback turn counts delivered)", err)
		}
		if len(got) != 1 || got[0] != agent.DefaultFallbackLine {
			t.Fatalf("dispatched = %q, want exactly the fallback line", got)
		}
	})

	t.Run("voiceless butler stays silent and propagates", func(t *testing.T) {
		r := textSinkReplier(t, errEngine{err: wantErr}, true, func(context.Context, string) error {
			t.Error("a failed turn must not post to the TextSink")
			return nil
		})
		var dispatched int
		err := r.ReplyStream()(context.Background(), routed("butler", "Glyphoxa, recap"), func(orchestrator.Reply) error {
			dispatched++
			return nil
		})
		if !errors.Is(err, wantErr) {
			t.Fatalf("ReplyStream err = %v, want the engine error (no voice to speak the fallback)", err)
		}
		if dispatched != 0 {
			t.Errorf("voiceless Butler dispatched %d to TTS, want 0", dispatched)
		}
	})
}

// textSinkReplier builds a Butler-style Replier with a TextSink installed,
// capturing text-delivered answers. voiceless picks whether the Persona carries a
// Voice (empty VoiceID = text-only Butler).
func textSinkReplier(t *testing.T, eng agent.Engine, voiceless bool, sink func(ctx context.Context, text string) error) *agent.Replier {
	t.Helper()
	voice := testVoice()
	if voiceless {
		voice.VoiceID = ""
	}
	return agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "butler", Markdown: "You are Glyphoxa.", Voice: voice},
		Engine:      eng,
		Synthesizer: stubSynth{},
		TextSink:    sink,
	})
}

// TestReplyStream_TextSink_LongAnswerPostsAsText pins the #299 text-delivery
// branch: with a TextSink installed, a long answer is posted whole via the sink
// (no TTS dispatch) and committed to history (ADR-0012 text-delivered commits).
func TestReplyStream_TextSink_LongAnswerPostsAsText(t *testing.T) {
	long := strings.Repeat("word ", 200) // > 400 runes
	eng := batchEngine{reply: long}
	var posted string
	var dispatched int
	r := textSinkReplier(t, eng, false, func(_ context.Context, text string) error {
		posted = text
		return nil
	})

	err := r.ReplyStream()(context.Background(), routed("butler", "Glyphoxa, what happened last session?"), func(orchestrator.Reply) error {
		dispatched++
		return nil
	})
	// A text-delivered turn returns the terminal sentinel (#299), not nil.
	if !errors.Is(err, orchestrator.ErrTextDelivered) {
		t.Fatalf("ReplyStream err = %v, want ErrTextDelivered", err)
	}
	if dispatched != 0 {
		t.Errorf("dispatched %d sentences to TTS, want 0 (text delivery)", dispatched)
	}
	if strings.TrimSpace(posted) != strings.TrimSpace(long) {
		t.Errorf("posted text = %q, want the whole answer", posted)
	}
	if !committedAssistant(r, strings.TrimSpace(long)) {
		t.Errorf("text-delivered answer not committed to history: %+v", r.HistorySnapshot())
	}
}

// TestReplyStream_TextSink_ShortAnswerSpoken pins that a short answer with a voice
// still speaks (sentence-split dispatch) and does NOT hit the TextSink, even
// though a TextSink is installed.
func TestReplyStream_TextSink_ShortAnswerSpoken(t *testing.T) {
	eng := batchEngine{reply: "Two sixes. Total nine."}
	sinkCalled := false
	r := textSinkReplier(t, eng, false, func(context.Context, string) error {
		sinkCalled = true
		return nil
	})

	var got []string
	err := r.ReplyStream()(context.Background(), routed("butler", "Glyphoxa, roll two d6"), func(rep orchestrator.Reply) error {
		got = append(got, rep.Sentence)
		return nil
	})
	if err != nil {
		t.Fatalf("ReplyStream: %v", err)
	}
	if sinkCalled {
		t.Error("TextSink called for a short spoken answer, want spoken via TTS")
	}
	want := []string{"Two sixes.", "Total nine."}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("dispatched %q, want %q (sentence-split spoken)", got, want)
	}
}

// TestReplyStream_TextSink_VoicelessAlwaysText pins that a voiceless Butler posts
// even a short answer as text — it has no Voice to speak with.
func TestReplyStream_TextSink_VoicelessAlwaysText(t *testing.T) {
	eng := batchEngine{reply: "Nine."}
	var posted string
	var dispatched int
	r := textSinkReplier(t, eng, true, func(_ context.Context, text string) error {
		posted = text
		return nil
	})

	err := r.ReplyStream()(context.Background(), routed("butler", "Glyphoxa, roll two d6"), func(orchestrator.Reply) error {
		dispatched++
		return nil
	})
	// A voiceless (text-delivered) turn returns the terminal sentinel (#299).
	if !errors.Is(err, orchestrator.ErrTextDelivered) {
		t.Fatalf("ReplyStream err = %v, want ErrTextDelivered", err)
	}
	if dispatched != 0 {
		t.Errorf("voiceless Butler dispatched %d to TTS, want 0", dispatched)
	}
	if strings.TrimSpace(posted) != "Nine." {
		t.Errorf("posted = %q, want %q", posted, "Nine.")
	}
}

// TestReplyStream_TextSink_ReturnsTextDeliveredSentinel pins the terminal signal
// (#299 finding 3): a text-delivered Butler turn returns the
// [orchestrator.ErrTextDelivered] sentinel so the reactor can publish
// TurnEnded(text_delivered) instead of letting the metrics TTL sweep miscount a
// successful voiceless/long answer as abandoned.
func TestReplyStream_TextSink_ReturnsTextDeliveredSentinel(t *testing.T) {
	eng := batchEngine{reply: "Nine."}
	r := textSinkReplier(t, eng, true, func(context.Context, string) error { return nil })

	err := r.ReplyStream()(context.Background(), routed("butler", "Glyphoxa, roll two d6"), func(orchestrator.Reply) error {
		return nil
	})
	if !errors.Is(err, orchestrator.ErrTextDelivered) {
		t.Fatalf("ReplyStream err = %v, want ErrTextDelivered", err)
	}
}

// TestReplyStream_TextSink_PostError_NotCommitted pins the ADR-0012
// deliver-then-commit claim in textModalityTurn's doc (#299 finding 4a): if the
// TextSink post FAILS, the answer was never delivered, so it must NOT be committed
// to history — the failed post leaves no phantom assistant message.
func TestReplyStream_TextSink_PostError_NotCommitted(t *testing.T) {
	long := strings.Repeat("word ", 200) // > 400 runes → text branch
	eng := batchEngine{reply: long}
	postErr := errors.New("channel post failed")
	r := textSinkReplier(t, eng, false, func(context.Context, string) error {
		return postErr
	})

	err := r.ReplyStream()(context.Background(), routed("butler", "Glyphoxa, what happened last session?"), func(orchestrator.Reply) error {
		return nil
	})
	if !errors.Is(err, postErr) {
		t.Fatalf("ReplyStream err = %v, want the sink post error", err)
	}
	// A failed post is NOT a text-delivered success.
	if errors.Is(err, orchestrator.ErrTextDelivered) {
		t.Fatal("a failed TextSink post must not report ErrTextDelivered")
	}
	for _, m := range r.HistorySnapshot() {
		if m.Role == llm.RoleAssistant {
			t.Fatalf("undelivered answer committed to history: %+v", m)
		}
	}
}

// TestReplyStream_TextSink_SpokenBargeMidTurn_CommitsOnlyDelivered pins the
// spoken branch of a Butler turn (#299 finding 4b): a short answer with a Voice is
// sentence-split and dispatched; when a barge/ctx-cancel aborts delivery
// mid-turn, only the sentences already delivered are committed (ADR-0012
// delivered-sentences-only), never the whole answer.
func TestReplyStream_TextSink_SpokenBargeMidTurn_CommitsOnlyDelivered(t *testing.T) {
	eng := batchEngine{reply: "One. Two. Three."}
	bargeErr := context.Canceled
	var dispatched int
	r := textSinkReplier(t, eng, false, func(context.Context, string) error {
		t.Fatal("spoken answer must not hit the TextSink")
		return nil
	})

	err := r.ReplyStream()(context.Background(), routed("butler", "Glyphoxa, roll"), func(orchestrator.Reply) error {
		dispatched++
		if dispatched == 2 {
			return bargeErr // barge cancels the turn during the second sentence
		}
		return nil
	})
	if !errors.Is(err, bargeErr) {
		t.Fatalf("ReplyStream err = %v, want the barge error", err)
	}
	// Only the first (delivered) sentence is committed; the barged tail is dropped.
	if !committedAssistant(r, "One.") {
		t.Errorf("delivered sentence not committed: %+v", r.HistorySnapshot())
	}
	if committedAssistant(r, "One. Two. Three.") || committedAssistant(r, "One. Two.") {
		t.Errorf("undelivered tail committed to history: %+v", r.HistorySnapshot())
	}
}

// TestReplyStream_TextSink_SpokenStartError_SentenceNotCommittedTurnSurvives pins
// the #362 contract on the Butler SPOKEN branch (textModalityTurn): a start-error
// (ErrNotDelivered) on sentence 1 skips that sentence's commit but the turn
// SURVIVES — sentence 2, delivered, IS committed, and the turn returns nil (not the
// sentinel, which would misclassify as provider_error). Mirrors streamTurn.
func TestReplyStream_TextSink_SpokenStartError_SentenceNotCommittedTurnSurvives(t *testing.T) {
	eng := batchEngine{reply: "One. Two."}
	r := textSinkReplier(t, eng, false, func(context.Context, string) error {
		t.Fatal("spoken answer must not hit the TextSink")
		return nil
	})

	var got []string
	err := r.ReplyStream()(context.Background(), routed("butler", "Glyphoxa, roll"), func(rep orchestrator.Reply) error {
		got = append(got, rep.Sentence)
		if rep.Sentence == "One." {
			return orchestrator.ErrNotDelivered // start-error, turn stays alive
		}
		return nil // delivered
	})
	if err != nil {
		t.Fatalf("ReplyStream err = %v, want nil (a start-error must not fail the spoken Butler turn)", err)
	}
	if len(got) != 2 || got[0] != "One." || got[1] != "Two." {
		t.Fatalf("dispatched = %v, want both sentences attempted (drain continues past start-error)", got)
	}
	if !committedAssistant(r, "Two.") {
		t.Errorf("delivered sentence 2 not committed: %+v", r.HistorySnapshot())
	}
	if committedAssistant(r, "One. Two.") || committedAssistant(r, "One.") {
		t.Errorf("start-errored sentence committed to history: %+v", r.HistorySnapshot())
	}
}

// committedAssistant reports whether the Replier committed an assistant message
// equal to want.
func committedAssistant(r *agent.Replier, want string) bool {
	for _, m := range r.HistorySnapshot() {
		if m.Role == llm.RoleAssistant && strings.TrimSpace(m.Text) == want {
			return true
		}
	}
	return false
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
	_ = rb.Reply()(t.Context(), routed("bart", "rooms?")) // batch path blocks for the whole completion
	firstBatch := time.Since(startB)

	t.Logf("B1 first-audio (preliminary): streaming first dispatch=%v, batch first dispatch=%v, saved≈%v",
		firstStream, firstBatch, firstBatch-firstStream)

	// The robust, relative invariant: streaming reaches the first sentence before
	// the batch path can dispatch anything at all (the whole-completion wait). An
	// absolute wall-clock bound is deliberately avoided — it flakes under CI
	// scheduling noise and the relative check already proves the win.
	if firstStream >= firstBatch {
		t.Errorf("streaming first dispatch %v not earlier than batch %v — B1 gave no win", firstStream, firstBatch)
	}
}

// Compile-time assertion: the loop produces a value usable by the orchestrator
// reply seam without an adapter.
var _ orchestrator.ReplyFunc = (&agent.Replier{}).Reply()

// TestReply_TruncatedStream_SpeaksFallbackAndReportsError pins the truncation
// contract on the default engine: a stream that closes without [llm.EventDone]
// (mid-stream network failure) must not be spoken as a complete reply — the
// failure surfaces via OnError, and the turn speaks the canned fallback line
// instead of the partial text (never the truncated completion).
func TestReply_TruncatedStream_SpeaksFallbackAndReportsError(t *testing.T) {
	var gotErr error
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Provider:    &fakeProvider{reply: "Half a sentence that never", truncate: true},
		Synthesizer: stubSynth{},
		OnError:     func(err error) { gotErr = err },
	})

	got := r.Reply()(t.Context(), routed("bart", "Hello."))
	if len(got) != 1 || got[0].Sentence != agent.DefaultFallbackLine {
		t.Errorf("reply for truncated stream = %+v, want the fallback line (never the partial text)", got)
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

// ctxCaptureStreamEngine is the [StreamingEngine] counterpart of
// [ctxCaptureEngine]: it records the ctx the Replier hands the streaming path so
// the per-turn deadline can be pinned for ReplyStream just as it is for Reply.
type ctxCaptureStreamEngine struct {
	ctx   context.Context
	reply string
}

func (e *ctxCaptureStreamEngine) Generate(ctx context.Context, _ []llm.Message) (string, error) {
	e.ctx = ctx
	return e.reply, nil
}

func (e *ctxCaptureStreamEngine) GenerateStream(ctx context.Context, _ []llm.Message, onText func(string) error) (string, error) {
	e.ctx = ctx
	if err := onText(e.reply); err != nil {
		return e.reply, err
	}
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

// TestReplyStream_TurnTimeout_AppliesDeadlineAndPropagatesCtx is the streaming
// twin of TestReply_TurnTimeout_*: the production path wires ReplyStream
// (orchestrator.ReplyStrategy.Stream), and the Gemini client has no overall HTTP
// timeout by design, so the per-turn deadline MUST be applied here too — without
// it a thinking-then-stalling completion runs unbounded and never produces first
// audio (the survivorship-biased latency the 20s live test hit).
func TestReplyStream_TurnTimeout_AppliesDeadlineAndPropagatesCtx(t *testing.T) {
	eng := &ctxCaptureStreamEngine{reply: "Aye."}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Engine:      eng,
		Synthesizer: stubSynth{},
	})

	type ctxKey struct{}
	parent := context.WithValue(t.Context(), ctxKey{}, "turn")
	if err := r.ReplyStream()(parent, routed("bart", "Hello."), func(orchestrator.Reply) error { return nil }); err != nil {
		t.Fatalf("ReplyStream returned %v", err)
	}

	if eng.ctx.Value(ctxKey{}) != "turn" {
		t.Error("engine ctx does not descend from the caller's turn ctx")
	}
	deadline, ok := eng.ctx.Deadline()
	if !ok {
		t.Fatal("streaming engine ctx has no deadline; a hung provider would block the turn forever")
	}
	if remaining := time.Until(deadline); remaining > agent.DefaultTurnTimeout {
		t.Errorf("deadline %v out past DefaultTurnTimeout %v", remaining, agent.DefaultTurnTimeout)
	}
}

// TestReplyStream_TurnTimeoutNegative_DisablesDeadline mirrors the batch escape
// hatch on the streaming path: TurnTimeout < 0 leaves the turn bounded only by
// the caller's ctx.
func TestReplyStream_TurnTimeoutNegative_DisablesDeadline(t *testing.T) {
	eng := &ctxCaptureStreamEngine{reply: "Aye."}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Engine:      eng,
		Synthesizer: stubSynth{},
		TurnTimeout: -1,
	})
	if err := r.ReplyStream()(t.Context(), routed("bart", "Hello."), func(orchestrator.Reply) error { return nil }); err != nil {
		t.Fatalf("ReplyStream returned %v", err)
	}
	if _, ok := eng.ctx.Deadline(); ok {
		t.Error("streaming engine ctx has a deadline despite TurnTimeout < 0")
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

// hangingEngine blocks Generate until ctx dies — the hung-provider shape the
// per-turn [agent.Config.TurnTimeout] deadline exists to kill.
type hangingEngine struct{}

func (hangingEngine) Generate(ctx context.Context, _ []llm.Message) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// hangingStreamEngine is hangingEngine's streaming twin: GenerateStream emits no
// deltas and blocks until ctx dies.
type hangingStreamEngine struct{ hangingEngine }

func (hangingStreamEngine) GenerateStream(ctx context.Context, _ []llm.Message, _ func(string) error) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// bargeMidGenEngine models a barge landing DURING generation: Generate cancels
// the PARENT turn ctx (what a floor yield does), then unwinds with the ctx error
// like a real provider adapter would.
type bargeMidGenEngine struct{ cancel context.CancelFunc }

func (e bargeMidGenEngine) Generate(ctx context.Context, _ []llm.Message) (string, error) {
	e.cancel()
	<-ctx.Done()
	return "", ctx.Err()
}

func (e bargeMidGenEngine) GenerateStream(ctx context.Context, _ []llm.Message, _ func(string) error) (string, error) {
	return e.Generate(ctx, nil)
}

// TestReplyStream_MidStreamTTSFailureThenEngineError_NoFallback pins the
// audio-MAY-be-out exclusion (#473 review): a sentence whose dispatch returned
// [orchestrator.ErrNotDelivered] was ATTEMPTED — per the #436 contract that
// covers a mid-stream TTS failure where a fragment already played, not only a
// start-error — so a terminal engine error afterwards must NOT speak the canned
// fallback even though the COMMITTED text is empty. The engine error surfaces
// instead of nil (the turn was not delivered).
func TestReplyStream_MidStreamTTSFailureThenEngineError_NoFallback(t *testing.T) {
	wantErr := errors.New("provider died with the tts")
	var gotErr error
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Engine:      &errStreamEngine{deltas: []string{"The bridge is out. "}, err: wantErr},
		Synthesizer: stubSynth{},
		OnError:     func(err error) { gotErr = err },
	})

	var dispatched []string
	err := r.ReplyStream()(context.Background(), routed("bart", "the bridge?"), func(rep orchestrator.Reply) error {
		dispatched = append(dispatched, rep.Sentence)
		return orchestrator.ErrNotDelivered // #436: the TTS stream died after a fragment
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("ReplyStream err = %v, want the engine error (a dispatch was attempted → no fallback rescue)", err)
	}
	if !errors.Is(gotErr, wantErr) {
		t.Errorf("OnError got %v, want %v", gotErr, wantErr)
	}
	if len(dispatched) != 1 || dispatched[0] != "The bridge is out." {
		t.Fatalf("dispatched = %q, want only the attempted sentence (never the canned line)", dispatched)
	}
	for _, m := range r.HistorySnapshot() {
		if m.Role == llm.RoleAssistant {
			t.Fatalf("committed assistant turn %q, want none (nothing delivered)", m.Text)
		}
	}
}

// TestReply_TurnTimeout_SpeaksFallback pins the batch half of the timeout/barge
// distinction (#473 review): a provider hang killed by the turn's OWN TurnTimeout
// deadline is a TERMINAL engine failure, not a barge — the caller's ctx is still
// live, so the turn reports OnError and speaks the canned fallback line instead
// of ending 60s of dead air with more silence.
func TestReply_TurnTimeout_SpeaksFallback(t *testing.T) {
	var gotErr error
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Engine:      hangingEngine{},
		Synthesizer: stubSynth{},
		TurnTimeout: 15 * time.Millisecond,
		OnError:     func(err error) { gotErr = err },
	})

	got := r.Reply()(context.Background(), routed("bart", "hi"))
	if len(got) != 1 || got[0].Sentence != agent.DefaultFallbackLine {
		t.Fatalf("reply after TurnTimeout expiry = %+v, want exactly the fallback line (parent ctx live → not a barge)", got)
	}
	if !errors.Is(gotErr, context.DeadlineExceeded) {
		t.Errorf("OnError got %v, want context.DeadlineExceeded", gotErr)
	}
	for _, m := range r.HistorySnapshot() {
		if m.Role == llm.RoleAssistant {
			t.Fatalf("fallback line committed to history: %q", m.Text)
		}
	}
}

// TestReplyStream_TurnTimeout_SpeaksFallback is the streaming twin of
// TestReply_TurnTimeout_SpeaksFallback, over both streaming engines (streamTurn)
// and batch-only ones (fallbackTurn): the deadline expiry of the turn's OWN
// TurnTimeout — the caller's ctx still live — fires OnError and speaks the
// canned line, reporting the turn delivered (nil).
func TestReplyStream_TurnTimeout_SpeaksFallback(t *testing.T) {
	engines := map[string]agent.Engine{
		"streaming engine":  hangingStreamEngine{},
		"batch-only engine": hangingEngine{},
	}
	for name, eng := range engines {
		t.Run(name, func(t *testing.T) {
			var gotErr error
			r := agent.NewReplier(agent.Config{
				Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
				Engine:      eng,
				Synthesizer: stubSynth{},
				TurnTimeout: 15 * time.Millisecond,
				OnError:     func(err error) { gotErr = err },
			})

			var got []string
			err := r.ReplyStream()(context.Background(), routed("bart", "hi"), func(rep orchestrator.Reply) error {
				got = append(got, rep.Sentence)
				return nil
			})
			if err != nil {
				t.Fatalf("ReplyStream err = %v, want nil (the fallback turn counts delivered)", err)
			}
			if !errors.Is(gotErr, context.DeadlineExceeded) {
				t.Errorf("OnError got %v, want context.DeadlineExceeded", gotErr)
			}
			if len(got) != 1 || got[0] != agent.DefaultFallbackLine {
				t.Fatalf("dispatched = %q, want exactly the fallback line", got)
			}
			for _, m := range r.HistorySnapshot() {
				if m.Role == llm.RoleAssistant {
					t.Fatalf("fallback line committed to history: %q", m.Text)
				}
			}
		})
	}
}

// TestReplyStream_BargeDuringGeneration_NoFallback pins the other half of the
// distinction: a PARENT-ctx cancel (barge/supersede) landing mid-generation stays
// silent — no fallback line, no OnError, nil return — even though the engine
// surfaced the cancellation as its terminal error.
func TestReplyStream_BargeDuringGeneration_NoFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var onErrCalls int
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Engine:      bargeMidGenEngine{cancel: cancel},
		Synthesizer: stubSynth{},
		OnError:     func(error) { onErrCalls++ },
	})

	var dispatched int
	err := r.ReplyStream()(ctx, routed("bart", "hi"), func(orchestrator.Reply) error {
		dispatched++
		return nil
	})
	if err != nil {
		t.Fatalf("ReplyStream err = %v, want nil (a barge is the expected path)", err)
	}
	if dispatched != 0 {
		t.Errorf("dispatched %d sentences on a barged turn, want 0 (no fallback)", dispatched)
	}
	if onErrCalls != 0 {
		t.Errorf("OnError called %d times on a barge cancel, want 0", onErrCalls)
	}
}
