package agent_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// funcStreamEngine is a [agent.StreamingEngine] whose generation is a test
// closure, so each test scripts exactly how the "provider" behaves under ctx.
// done is closed when GenerateStream returns — the "generation goroutine has
// unwound" signal.
type funcStreamEngine struct {
	gen  func(ctx context.Context, msgs []llm.Message, onText func(string) error) error
	done chan struct{}
	once sync.Once
}

func newFuncStreamEngine(gen func(ctx context.Context, msgs []llm.Message, onText func(string) error) error) *funcStreamEngine {
	return &funcStreamEngine{gen: gen, done: make(chan struct{})}
}

func (e *funcStreamEngine) Generate(context.Context, []llm.Message) (string, error) { return "", nil }

func (e *funcStreamEngine) GenerateStream(ctx context.Context, msgs []llm.Message, onText func(string) error) (string, error) {
	defer e.once.Do(func() { close(e.done) })
	return "", e.gen(ctx, msgs, onText)
}

// ctxGuardedDeltas mimics the real provider adapter (openaicompat's
// ctx-guarded unbuffered send): the provider has every delta ready at once, but
// hands each over only while ctx lives, so a consumer that blocks in onText
// loses the rest of the reply once ctx's deadline fires.
func ctxGuardedDeltas(deltas ...string) func(context.Context, []llm.Message, func(string) error) error {
	return func(ctx context.Context, _ []llm.Message, onText func(string) error) error {
		ch := make(chan string)
		go func() {
			defer close(ch)
			for _, d := range deltas {
				select {
				case <-ctx.Done():
					return
				case ch <- d:
				}
			}
		}()
		n := 0
		for d := range ch {
			n++
			if err := onText(d); err != nil {
				return err
			}
		}
		if n < len(deltas) {
			return ctx.Err()
		}
		return nil
	}
}

// spokenLog is a concurrency-safe record of dispatched sentences.
type spokenLog struct {
	mu   sync.Mutex
	said []string
}

func (l *spokenLog) add(s string) { l.mu.Lock(); l.said = append(l.said, s); l.mu.Unlock() }
func (l *spokenLog) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.said...)
}

func assistantTexts(r *agent.Replier) []string {
	var out []string
	for _, m := range r.HistorySnapshot() {
		if m.Role == llm.RoleAssistant {
			out = append(out, m.Text)
		}
	}
	return out
}

// TestReplyStream_PlaybackOutlastingTurnTimeout_IsNotTruncated is O2: the
// provider had the whole answer at t=0, the caller's turn ctx is alive, and
// PLAYBACK takes longer than TurnTimeout. The deadline bounds generation, so the
// whole reply is spoken and committed, and nothing is reported as an error.
// Before the fix the stream was consumed in lockstep with playback and the reply
// was cut after ~TurnTimeout of audio.
func TestReplyStream_PlaybackOutlastingTurnTimeout_IsNotTruncated(t *testing.T) {
	var onErr atomic.Value
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Voice: testVoice()},
		Engine:      newFuncStreamEngine(ctxGuardedDeltas("One. ", "Two. ", "Three. ", "Four. ", "Five. ", "Six.")),
		Synthesizer: stubSynth{},
		TurnTimeout: 60 * time.Millisecond,
		OnError:     func(err error) { onErr.Store(err) },
	})
	var log spokenLog
	err := r.ReplyStream()(context.Background(), routed("bart", "tell me a story"), func(rep orchestrator.Reply) error {
		time.Sleep(30 * time.Millisecond) // one sentence of paced playback; six outlast the deadline 3x
		log.add(rep.Sentence)
		return nil
	})
	if err != nil {
		t.Fatalf("ReplyStream = %v, want nil (a long reply is not a failure)", err)
	}
	if e := onErr.Load(); e != nil {
		t.Errorf("OnError = %v, want none", e)
	}
	want := []string{"One.", "Two.", "Three.", "Four.", "Five.", "Six."}
	if got := log.get(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("spoken %q, want the whole reply %q", got, want)
	}
	if got := assistantTexts(r); len(got) != 1 || got[0] != "One. Two. Three. Four. Five. Six." {
		t.Errorf("committed assistant = %q, want the whole delivered reply", got)
	}
}

// TestReplyStream_BargeWithQueuedSentences_DropsTheQueue pins the other side of
// the decoupling: generation finished long ago and several sentences sit in the
// queue when the turn is barged. Nothing queued may be dispatched or committed
// (ADR-0012: generated is not delivered), the generation goroutine is joined,
// and the barge is not reported as an error.
func TestReplyStream_BargeWithQueuedSentences_DropsTheQueue(t *testing.T) {
	eng := newFuncStreamEngine(ctxGuardedDeltas("One. ", "Two. ", "Three. ", "Four. ", "Five."))
	var onErrs atomic.Int32
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Voice: testVoice()},
		Engine:      eng,
		Synthesizer: stubSynth{},
		OnError:     func(error) { onErrs.Add(1) },
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var log spokenLog
	var dispatches atomic.Int32
	inSecond := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- r.ReplyStream()(ctx, routed("bart", "story"), func(rep orchestrator.Reply) error {
			if dispatches.Add(1) == 1 {
				log.add(rep.Sentence)
				return nil
			}
			close(inSecond) // the second sentence is "playing" when the barge lands
			<-ctx.Done()
			return ctx.Err()
		})
	}()

	select {
	case <-eng.done: // the whole reply is generated and queued
	case <-time.After(5 * time.Second):
		t.Fatal("generation did not finish while playback was blocked — still coupled")
	}
	<-inSecond
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("ReplyStream = %v, want nil on a barge", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("barged turn did not return")
	}

	if n := dispatches.Load(); n != 2 {
		t.Errorf("dispatch calls = %d, want 2 (the queue behind the cut sentence is dropped)", n)
	}
	if got := assistantTexts(r); len(got) != 1 || got[0] != "One." {
		t.Errorf("committed assistant = %q, want only the delivered [\"One.\"]", got)
	}
	if n := onErrs.Load(); n != 0 {
		t.Errorf("OnError fired %d times, want 0 on a barge", n)
	}
}

// TestReplyStream_BargeMidGeneration_CancelsGenerationPromptly pins that a cut
// in the dispatcher reaches the generation goroutine: an engine parked in its
// provider call (a tool round, a slow stream) is unwound by ctx, and the turn
// does not return before it has.
func TestReplyStream_BargeMidGeneration_CancelsGenerationPromptly(t *testing.T) {
	var sawCancel atomic.Bool
	eng := newFuncStreamEngine(func(ctx context.Context, _ []llm.Message, onText func(string) error) error {
		if err := onText("One. "); err != nil {
			return err
		}
		<-ctx.Done() // the provider is still generating when the barge lands
		sawCancel.Store(true)
		return ctx.Err()
	})
	r := streamReplier(t, eng)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := r.ReplyStream()(ctx, routed("bart", "story"), func(orchestrator.Reply) error {
		cancel() // barge during the first sentence's playback
		return ctx.Err()
	})
	if err != nil {
		t.Errorf("ReplyStream = %v, want nil on a barge", err)
	}
	if !sawCancel.Load() {
		t.Error("turn returned before its generation goroutine unwound")
	}
	if got := assistantTexts(r); len(got) != 0 {
		t.Errorf("committed assistant = %q, want nothing (the cut sentence was not delivered)", got)
	}
}

// TestReplyStream_GenerationDeadlineMidReply_SpeaksWhatWasGenerated pins the
// deadline's new meaning: the provider hangs mid-reply, TurnTimeout fires, and
// the sentences generated before it are still spoken and committed. The
// unterminated tail is dropped, the error is reported, and no canned fallback is
// appended to a partial reply.
func TestReplyStream_GenerationDeadlineMidReply_SpeaksWhatWasGenerated(t *testing.T) {
	eng := newFuncStreamEngine(func(ctx context.Context, _ []llm.Message, onText func(string) error) error {
		if err := onText("One. Two. And then the unfinished"); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	})
	var onErr atomic.Value
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Voice: testVoice()},
		Engine:      eng,
		Synthesizer: stubSynth{},
		TurnTimeout: 40 * time.Millisecond,
		OnError:     func(err error) { onErr.Store(err) },
	})
	var log spokenLog
	err := r.ReplyStream()(context.Background(), routed("bart", "story"), func(rep orchestrator.Reply) error {
		log.add(rep.Sentence)
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("ReplyStream = %v, want the generation deadline", err)
	}
	if e, _ := onErr.Load().(error); !errors.Is(e, context.DeadlineExceeded) {
		t.Errorf("OnError = %v, want the generation deadline", e)
	}
	if got := log.get(); strings.Join(got, "|") != "One.|Two." {
		t.Errorf("spoken %q, want [One. Two.] — no tail, no fallback line", got)
	}
	if got := assistantTexts(r); len(got) != 1 || got[0] != "One. Two." {
		t.Errorf("committed assistant = %q, want [\"One. Two.\"]", got)
	}
}

// TestReplyStream_RunawayGeneration_IsCappedAndEndsCleanly pins the spoken-text
// cap (F5): a generation that will not stop is cut off at agent.MaxSpokenRunes
// — whether it punctuates or not — what was queued is still spoken, and the
// turn ends cleanly: the limit is reported via OnError but is not a turn
// failure.
func TestReplyStream_RunawayGeneration_IsCappedAndEndsCleanly(t *testing.T) {
	for name, delta := range map[string]string{
		"punctuated":   "Und noch ein Satz. ",
		"unterminated": "und immer weiter ",
	} {
		t.Run(name, func(t *testing.T) {
			const runaway = 100 * agent.MaxSpokenRunes // runes the model would produce
			var generated atomic.Int64
			var genErr atomic.Value
			eng := newFuncStreamEngine(func(_ context.Context, _ []llm.Message, onText func(string) error) error {
				for generated.Load() < runaway {
					if err := onText(delta); err != nil {
						genErr.Store(err)
						return err
					}
					generated.Add(int64(len([]rune(delta))))
				}
				return nil
			})
			var onErr atomic.Value
			r := agent.NewReplier(agent.Config{
				Persona:     agent.Persona{AgentID: "bart", Voice: testVoice()},
				Engine:      eng,
				Synthesizer: stubSynth{},
				OnError:     func(err error) { onErr.Store(err) },
			})
			var log spokenLog
			err := r.ReplyStream()(context.Background(), routed("bart", "story"), func(rep orchestrator.Reply) error {
				<-eng.done // playback is stalled until generation has stopped
				log.add(rep.Sentence)
				return nil
			})
			if err != nil {
				t.Errorf("ReplyStream = %v, want nil (the cap is a limit, not a failure)", err)
			}
			if e, _ := genErr.Load().(error); !errors.Is(e, agent.ErrReplyTooLong) {
				t.Errorf("generation was stopped with %v, want ErrReplyTooLong", e)
			}
			if e, _ := onErr.Load().(error); !errors.Is(e, agent.ErrReplyTooLong) {
				t.Errorf("OnError = %v, want ErrReplyTooLong", e)
			}
			spoken := 0
			for _, sentence := range log.get() {
				spoken += len([]rune(sentence))
			}
			if spoken > agent.MaxSpokenRunes || spoken < agent.MaxSpokenRunes/2 {
				t.Errorf("spoke %d runes, want close to and at most the cap of %d", spoken, agent.MaxSpokenRunes)
			}
			if got := assistantTexts(r); len(got) != 1 || len([]rune(got[0])) > agent.MaxSpokenRunes {
				t.Errorf("committed %d assistant message(s), want the one capped reply", len(got))
			}
		})
	}
}

// TestReplyStream_SupersededTurnCommit_StaysBehindItsOwnQuestion pins the
// history ordering under a supersede. Turn A is still unwinding (its second
// sentence is cut) when turn B has already recorded its user line and finished.
// A's delivered prefix must land behind A's question — not at the end of the
// history, where the next generation would read it as the newest thing said.
func TestReplyStream_SupersededTurnCommit_StaysBehindItsOwnQuestion(t *testing.T) {
	eng := newFuncStreamEngine(nil)
	eng.gen = func(_ context.Context, msgs []llm.Message, onText func(string) error) error {
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role == llm.RoleUser {
				if strings.Contains(msgs[i].Text, "question A") {
					return onText("[cheerfully]A one. A two.")
				}
				return onText("B one.")
			}
		}
		return nil
	}
	r := streamReplier(t, eng)

	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	var n atomic.Int32
	inSecond := make(chan struct{})
	doneA := make(chan struct{})
	go func() {
		defer close(doneA)
		_ = r.ReplyStream()(ctxA, routed("bart", "question A"), func(orchestrator.Reply) error {
			if n.Add(1) == 1 {
				return nil
			}
			close(inSecond)
			<-ctxA.Done()
			return ctxA.Err()
		})
	}()
	<-inSecond

	// Turn B runs to completion while A is parked mid-playback.
	if err := r.ReplyStream()(context.Background(), routed("bart", "question B"), func(orchestrator.Reply) error { return nil }); err != nil {
		t.Fatalf("turn B: %v", err)
	}
	cancelA()
	<-doneA

	var got []string
	for _, m := range r.HistorySnapshot() {
		got = append(got, string(m.Role)+":"+m.Text)
	}
	want := []string{"user:question A", "assistant:[cheerfully]A one.", "user:question B", "assistant:B one."}
	if strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Errorf("history = %q\n          want %q", got, want)
	}
}

// TestReplyStream_MarkdownNeverReachesTheVoice pins O12's delivery path end to
// end: what a chat-trained model formats for a screen is scrubbed before TTS and
// before the history commit, while the bracketed audio tag survives verbatim.
func TestReplyStream_MarkdownNeverReachesTheVoice(t *testing.T) {
	eng := &fakeStreamEngine{deltas: []string{
		"[cheerfully] **Willkommen**, Reisende! ", "Ihr braucht:\n", "- ein *scharfes* Schwert\n", "- genau 3 * 4 Goldstücke. ",
		"```\n", "## Viel Glück!",
	}}
	r := streamReplier(t, eng)
	var got []string
	if err := r.ReplyStream()(context.Background(), routed("bart", "go"), func(rep orchestrator.Reply) error {
		got = append(got, rep.Sentence)
		return nil
	}); err != nil {
		t.Fatalf("ReplyStream: %v", err)
	}
	want := []string{"[cheerfully] Willkommen, Reisende!", "Ihr braucht:\nein scharfes Schwert\ngenau 3 * 4 Goldstücke.", "Viel Glück!"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("dispatched %q\n       want %q", got, want)
	}
	if hist := assistantTexts(r); len(hist) != 1 || strings.ContainsAny(hist[0], "*#`") && !strings.Contains(hist[0], "3 * 4") {
		t.Errorf("committed assistant = %q, want the scrubbed text", hist)
	}
}

// TestSystemPrompt_SpokenOutputRule pins O12's prompt half: the rule sits in the
// STABLE system prompt (ADR-0059's cacheable prefix), ahead of the audio-markup
// instruction it exempts, and only for an Agent whose every reply is spoken.
func TestSystemPrompt_SpokenOutputRule(t *testing.T) {
	sysFor := func(cfg agent.Config) string { return systemPromptFor(t, cfg) }

	voiced := sysFor(agent.Config{Persona: agent.Persona{Voice: testVoice()}})
	iRule, iMarkup := strings.Index(voiced, agent.SpokenOutputRuleWithMarkup), strings.Index(voiced, sentinelMarkup)
	if iRule < 0 || iMarkup < iRule {
		t.Errorf("voiced prompt lacks the spoken-output rule ahead of the markup instruction:\n%s", voiced)
	}

	// No markup instruction → no dangling reference to "the audio tags below".
	noMarkup := sysFor(agent.Config{Persona: agent.Persona{Voice: testVoice()}, Synthesizer: chunkSynth{}})
	if !strings.HasSuffix(noMarkup, agent.SpokenOutputRule) {
		t.Errorf("markup-less prompt should end on the bare rule:\n%s", noMarkup)
	}

	// A Butler that may answer in channel text is not told everything is spoken,
	// and a voiceless Persona never speaks at all.
	sink := func(context.Context, string) error { return nil }
	if butler := sysFor(agent.Config{Persona: agent.Persona{Voice: testVoice()}, TextSink: sink}); strings.Contains(butler, agent.SpokenOutputRule) {
		t.Errorf("TextSink prompt carries the spoken-output rule:\n%s", butler)
	}
	if voiceless := sysFor(agent.Config{Persona: agent.Persona{Voice: tts.Voice{}}}); strings.Contains(voiceless, agent.SpokenOutputRule) {
		t.Errorf("voiceless prompt carries the spoken-output rule:\n%s", voiceless)
	}
}

// TestFallbackLineFor pins the fallback line's Campaign Language key.
func TestFallbackLineFor(t *testing.T) {
	german := agent.FallbackLineFor("de")
	if german == agent.DefaultFallbackLine || german == "" {
		t.Fatalf("FallbackLineFor(de) = %q, want a German line", german)
	}
	for _, lang := range []string{"de-DE", "DE", "de_AT", " de "} {
		if got := agent.FallbackLineFor(lang); got != german {
			t.Errorf("FallbackLineFor(%q) = %q, want the German line", lang, got)
		}
	}
	for _, lang := range []string{"", "en", "en-US", "tlh"} {
		if got := agent.FallbackLineFor(lang); got != agent.DefaultFallbackLine {
			t.Errorf("FallbackLineFor(%q) = %q, want DefaultFallbackLine", lang, got)
		}
	}
}

// barrierRecallers implements all four Hot Context recallers. Each call parks
// until all four are in flight at once, so the turn only completes if they run
// concurrently — and fails fast, rather than deadlocking, if they do not.
type barrierRecallers struct {
	t       *testing.T
	mu      sync.Mutex
	waiting int
	all     chan struct{}
}

func (b *barrierRecallers) arrive() {
	b.mu.Lock()
	b.waiting++
	if b.waiting == 4 {
		close(b.all)
	}
	b.mu.Unlock()
	select {
	case <-b.all:
	case <-time.After(2 * time.Second):
		b.t.Error("recaller waited 2s for its siblings: the recallers run serially")
	}
}

func (b *barrierRecallers) Recall(context.Context, string, string) agent.Memory {
	b.arrive()
	return agent.Memory{Personal: []string{"MEMORY-BLOCK"}}
}
func (b *barrierRecallers) Facts(context.Context, string) []string {
	b.arrive()
	return []string{"FACT-BLOCK"}
}
func (b *barrierRecallers) Directive(context.Context, string, bool) string {
	b.arrive()
	return "DIRECTIVE-BLOCK"
}
func (b *barrierRecallers) Location(context.Context, string) string {
	b.arrive()
	return "LOCATION-BLOCK"
}

// TestReplyStream_HotContextRecallersRunConcurrently pins that the four
// recallers overlap instead of adding their latencies together before first
// audio, and that the volatile tail still renders their blocks in the fixed
// order: facts, location, memory, directive.
func TestReplyStream_HotContextRecallersRunConcurrently(t *testing.T) {
	rec := &barrierRecallers{t: t, all: make(chan struct{})}
	prov := &fakeProvider{reply: "Aye."}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Provider:    prov,
		Synthesizer: stubSynth{},
		Memory:      rec, Facts: rec, Directive: rec, Location: rec,
	})
	if err := r.ReplyStream()(context.Background(), routed("bart", "Hail."), func(orchestrator.Reply) error { return nil }); err != nil {
		t.Fatalf("ReplyStream: %v", err)
	}
	tail := volatileTail(t, prov.lastRequest(t).Messages)
	last := -1
	for _, block := range []string{"FACT-BLOCK", "LOCATION-BLOCK", "MEMORY-BLOCK", "DIRECTIVE-BLOCK"} {
		i := strings.Index(tail, block)
		if i < 0 || i < last {
			t.Fatalf("volatile tail blocks missing or out of order at %s:\n%s", block, tail)
		}
		last = i
	}
}

// TestReply_HistoryTrim_BlocksStartingOnUserMessage pins O6 over a long
// conversation: the recent-Transcript window always opens on a user message,
// stays within twice HistoryTurns, and its first message — where the request's
// cacheable prefix ends when it changes (ADR-0059) — moves once per block rather
// than on every turn.
func TestReply_HistoryTrim_BlocksStartingOnUserMessage(t *testing.T) {
	const keep, exchanges = 4, 24
	prov := &fakeProvider{reply: "ok"}
	r := agent.NewReplier(agent.Config{
		Persona:      agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Provider:     prov,
		Synthesizer:  stubSynth{},
		HistoryTurns: keep,
	})
	reply := r.Reply()

	var firsts []string
	for i := range exchanges {
		deliver(reply(t.Context(), routed("bart", fmt.Sprintf("utterance-%02d", i))))
		window := prov.lastRequest(t).Messages[1:] // behind the system prompt
		if window[0].Role != llm.RoleUser {
			t.Fatalf("exchange %d: window opens on a %s message %q, want user", i, window[0].Role, window[0].Text)
		}
		if len(window) > 2*keep {
			t.Fatalf("exchange %d: window holds %d messages, want at most %d", i, len(window), 2*keep)
		}
		if got := lastUserText(t, window); got != fmt.Sprintf("utterance-%02d", i) {
			t.Fatalf("exchange %d: newest utterance is %q", i, got)
		}
		firsts = append(firsts, window[0].Text)
	}
	moves := 0
	for i := 1; i < len(firsts); i++ {
		if firsts[i] != firsts[i-1] {
			moves++
		}
	}
	// A window sliding one message per append moves on every exchange past the
	// bound (~20 of 24 here); in blocks of keep messages it moves every keep/2
	// exchanges.
	if max := exchanges / (keep / 2); moves > max {
		t.Errorf("window start moved %d times in %d exchanges, want at most %d (block trim)", moves, exchanges, max)
	}
	if moves == 0 {
		t.Error("window never moved: the history is not bounded at all")
	}
}

// systemPromptFor returns the stable system prompt a Replier built from cfg
// sends (Persona "You are Bart."; the sentinel-markup synthesizer unless cfg
// names one).
func systemPromptFor(t *testing.T, cfg agent.Config) string {
	t.Helper()
	prov := &fakeProvider{reply: "Aye."}
	cfg.Provider = prov
	cfg.Persona.AgentID, cfg.Persona.Markdown = "bart", "You are Bart."
	if cfg.Synthesizer == nil {
		cfg.Synthesizer = stubSynth{}
	}
	agent.NewReplier(cfg).Reply()(t.Context(), routed("bart", "Hail."))
	return prov.lastRequest(t).Messages[0].Text
}

// TestSystemPrompt_StageDirectionRule pins how the prompt forbids narrated
// actions (#702) — the one spoken-output mistake with no deterministic
// backstop — and the two ways the first wording of it went wrong in review:
//
//   - The audio-tag exemption has to qualify the no-MARKUP sentence. Appended
//     after "no described actions or gestures" it read as a licence for
//     bracketed gestures, and the bench recorded exactly those
//     ("[leans on the bar]"), which a TTS engine may read aloud. With an
//     audio-markup instruction the rule also says in so many words that a
//     bracket is never an action.
//   - It must not forbid the third person. That sentence sits after the
//     Persona, and a GM-written speech pattern ("Grik always speaks of himself
//     in the third person") is the Persona's to decide.
func TestSystemPrompt_StageDirectionRule(t *testing.T) {
	const (
		noMarkupSentence = "no emojis, no code."
		exemption        = "The audio tags described below are the one exception."
		noActions        = "no stage directions"
	)
	voiced := systemPromptFor(t, agent.Config{Persona: agent.Persona{Voice: testVoice()}})
	iFormat, iExempt, iActions := strings.Index(voiced, noMarkupSentence), strings.Index(voiced, exemption), strings.Index(voiced, noActions)
	if iFormat < 0 || iExempt < 0 || iActions < 0 {
		t.Fatalf("voiced prompt lacks the no-markup sentence, the audio-tag exemption or the stage-direction rule:\n%s", voiced)
	}
	if iExempt != iFormat+len(noMarkupSentence)+1 {
		t.Errorf("the audio-tag exemption does not directly follow the no-markup sentence:\n%s", voiced)
	}
	if iActions < iExempt {
		t.Errorf("the stage-direction rule comes before the audio-tag exemption, which then reads as exempting actions:\n%s", voiced)
	}
	if rest := voiced[iActions:strings.Index(voiced, sentinelMarkup)]; !strings.Contains(rest, "square brackets") {
		t.Errorf("with audio markup the rule does not say a bracket is never an action:\n%s", rest)
	}

	bare := systemPromptFor(t, agent.Config{Persona: agent.Persona{Voice: testVoice()}, Synthesizer: chunkSynth{}})
	if !strings.Contains(bare, noActions) {
		t.Errorf("markup-less prompt lacks the stage-direction rule:\n%s", bare)
	}
	if strings.Contains(bare, "audio tag") || strings.Contains(bare, "square brackets") {
		t.Errorf("markup-less prompt talks about audio tags it never introduces:\n%s", bare)
	}

	for _, prompt := range []string{voiced, bare} {
		if strings.Contains(prompt, "third person") {
			t.Errorf("the rule forbids the third person, overriding a Persona's speech pattern:\n%s", prompt)
		}
	}
}
