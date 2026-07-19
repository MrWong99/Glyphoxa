package agent_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// castReplier builds a Replier for a named Agent over a streaming engine, so a
// Cast can be assembled from several distinguishable repliers. The Voice carries
// the AgentID in its identifying fields so a dispatched Reply can be attributed to
// its speaker.
func castReplier(agentID string, eng agent.Engine) *agent.Replier {
	return agent.NewReplier(agent.Config{
		Persona: agent.Persona{
			AgentID:  agentID,
			Markdown: "You are " + agentID + ".",
			Voice:    voiceNamed(agentID),
		},
		Engine:      eng,
		Synthesizer: stubSynth{},
	})
}

// voiceNamed is testVoice with the speaker's id stamped into its identifying
// fields so a dispatched Reply's Voice tells which Agent spoke it.
func voiceNamed(agentID string) tts.Voice {
	v := testVoice()
	v.VoiceID = agentID
	v.Name = agentID
	return v
}

// TestCast_RoutesToAddressedAgent pins the multiplexer core: a route for agent B
// drives B's reply (and only B's). The dispatched Reply's Voice attributes the
// sentence to its speaker, so a route for "b" must be spoken in b's voice.
func TestCast_RoutesToAddressedAgent(t *testing.T) {
	a := castReplier("a", &fakeStreamEngine{deltas: []string{"I am A."}})
	b := castReplier("b", &fakeStreamEngine{deltas: []string{"I am B."}})
	cast := agent.NewCast(a, b)

	var got []orchestrator.Reply
	err := cast.ReplyStream()(context.Background(), routed("b", "who are you?"), func(rep orchestrator.Reply) error {
		got = append(got, rep)
		return nil
	})
	if err != nil {
		t.Fatalf("ReplyStream: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("dispatched %d replies, want 1 (only B speaks)", len(got))
	}
	if got[0].Sentence != "I am B." {
		t.Errorf("sentence = %q, want B's reply", got[0].Sentence)
	}
	if got[0].Voice.VoiceID != "b" {
		t.Errorf("voice = %q, want B's voice (A must not have spoken)", got[0].Voice.VoiceID)
	}
}

// TestCast_UnknownAgentSaysNothing pins the unknown-target contract: a route for
// an AgentID no replier in the Cast answers for dispatches nothing and returns
// nil — the safe default when the matcher selected an Agent the Cast does not (or
// no longer) holds.
func TestCast_UnknownAgentSaysNothing(t *testing.T) {
	a := castReplier("a", &fakeStreamEngine{deltas: []string{"I am A."}})
	cast := agent.NewCast(a)

	var dispatched int
	err := cast.ReplyStream()(context.Background(), routed("ghost", "hello?"), func(orchestrator.Reply) error {
		dispatched++
		return nil
	})
	if err != nil {
		t.Errorf("ReplyStream for unknown agent = %v, want nil", err)
	}
	if dispatched != 0 {
		t.Errorf("dispatched %d replies for unknown agent, want 0", dispatched)
	}
}

// TestCast_AddNPC pins runtime registration: an Agent added after construction
// replies when addressed, so a Cast can grow as NPCs enter the scene.
func TestCast_AddNPC(t *testing.T) {
	cast := agent.NewCast()

	// Before Add, the target is unknown — nothing is said.
	var before int
	_ = cast.ReplyStream()(context.Background(), routed("c", "hi"), func(orchestrator.Reply) error {
		before++
		return nil
	})
	if before != 0 {
		t.Fatalf("dispatched %d before Add, want 0", before)
	}

	cast.Add(castReplier("c", &fakeStreamEngine{deltas: []string{"I am C."}}))

	var got []orchestrator.Reply
	if err := cast.ReplyStream()(context.Background(), routed("c", "hi"), func(rep orchestrator.Reply) error {
		got = append(got, rep)
		return nil
	}); err != nil {
		t.Fatalf("ReplyStream after Add: %v", err)
	}
	if len(got) != 1 || got[0].Sentence != "I am C." {
		t.Errorf("after Add dispatched %+v, want C's single reply", got)
	}
}

// TestCast_RemoveNPC pins runtime removal: an Agent removed from the Cast goes
// silent — a route for it dispatches nothing, as if it had never been registered.
func TestCast_RemoveNPC(t *testing.T) {
	d := castReplier("d", &fakeStreamEngine{deltas: []string{"I am D."}})
	cast := agent.NewCast(d)

	cast.Remove("d")

	var dispatched int
	if err := cast.ReplyStream()(context.Background(), routed("d", "still there?"), func(orchestrator.Reply) error {
		dispatched++
		return nil
	}); err != nil {
		t.Fatalf("ReplyStream after Remove: %v", err)
	}
	if dispatched != 0 {
		t.Errorf("removed agent dispatched %d replies, want 0 (gone silent)", dispatched)
	}
}

// TestCast_Reply_RoutesToAddressedAgent pins the batch [orchestrator.ReplyFunc]
// twin of the streaming multiplexer: Reply() looks up the addressed agent and
// returns its turn, nothing for the others.
func TestCast_Reply_RoutesToAddressedAgent(t *testing.T) {
	a := castReplier("a", &fakeStreamEngine{deltas: []string{"I am A."}})
	b := castReplier("b", &fakeStreamEngine{deltas: []string{"I am B."}})
	cast := agent.NewCast(a, b)

	got := cast.Reply()(context.Background(), routed("a", "who?"))
	if len(got) != 1 || got[0].Sentence != "I am A." {
		t.Fatalf("Reply for a = %+v, want A's single reply", got)
	}
	if got[0].Voice.VoiceID != "a" {
		t.Errorf("voice = %q, want A's voice", got[0].Voice.VoiceID)
	}

	if unknown := cast.Reply()(context.Background(), routed("ghost", "?")); unknown != nil {
		t.Errorf("Reply for unknown agent = %+v, want nil", unknown)
	}
}

// TestCast_ConcurrentAddRemoveDispatch races Add/Remove against reply dispatch to
// pin the RWMutex guard: under -race, runtime roster mutation must not corrupt the
// lookup nor data-race the dispatch path. Correctness of any single dispatch is
// not asserted (the roster is changing under it) — only that no race fires and no
// dispatch panics.
func TestCast_ConcurrentAddRemoveDispatch(t *testing.T) {
	cast := agent.NewCast()
	const n = 8
	ids := make([]string, n)
	for i := range ids {
		ids[i] = string(rune('a' + i))
		cast.Add(castReplier(ids[i], &fakeStreamEngine{deltas: []string{"hi"}}))
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Mutator: churn the roster.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			for _, id := range ids {
				cast.Remove(id)
				cast.Add(castReplier(id, &fakeStreamEngine{deltas: []string{"hi"}}))
			}
		}
	}()

	// Dispatchers: keep routing while the roster churns.
	dispatch := func(orchestrator.Reply) error { return nil }
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, id := range ids {
					_ = cast.ReplyStream()(context.Background(), routed(id, "go"), dispatch)
					_ = cast.Reply()(context.Background(), routed(id, "go"))
				}
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// Compile-time assertions: a Cast's strategies are drop-in for the orchestrator
// reply seams without an adapter.
var (
	_ orchestrator.StreamReplyFunc = agent.NewCast().ReplyStream()
	_ orchestrator.ReplyFunc       = agent.NewCast().Reply()
	_ orchestrator.CrossTalker     = agent.NewCast()
)

// TestCast_React_RoutesByAgentID pins the CrossTalker.React half (#302): a React
// for agent B produces B's would-be Cross-talk Reaction (via B's Replier), and an
// unknown Agent yields "", nil — the "no one reacts" signal the coordinator treats
// as a decline.
func TestCast_React_RoutesByAgentID(t *testing.T) {
	a := castReplier("a", &fakeStreamEngine{deltas: []string{"unused"}})
	b := castReplier("b", &fakeStreamEngine{deltas: []string{"I disagree."}})
	cast := agent.NewCast(a, b)

	reaction, err := cast.React(context.Background(), routed("b", "what do you two think?"), "A", "The bridge is out.")
	if err != nil {
		t.Fatalf("React: %v", err)
	}
	if reaction != "I disagree." {
		t.Fatalf("reaction = %q, want B's cross-talk reply", reaction)
	}

	// Unknown Agent: no member reacts, "" nil (not an error).
	got, err := cast.React(context.Background(), routed("zzz", "x"), "A", "y")
	if err != nil || got != "" {
		t.Fatalf("React(unknown) = (%q, %v), want (\"\", nil)", got, err)
	}
}

// TestCast_SpeakReaction_CommitsCompositeAndDelivered pins the CrossTalker.SpeakReaction
// half (#302, ADR-0012): SpeakReaction dispatches the reaction in B's Voice and
// commits to B's history the SAME composite user message React reasoned over (the
// utterance + the Lead's cross-talk line) plus the delivered assistant text. An
// unknown Agent dispatches nothing and commits nothing.
func TestCast_SpeakReaction_CommitsCompositeAndDelivered(t *testing.T) {
	b := castReplier("b", &fakeStreamEngine{deltas: []string{"unused"}})
	cast := agent.NewCast(b)

	var got []orchestrator.Reply
	delivered, err := cast.SpeakReaction(context.Background(), routed("b", "what do you two think?"), "A", "The bridge is out.", "I disagree. Strongly.", func(rep orchestrator.Reply) error {
		got = append(got, rep)
		return nil
	})
	if err != nil {
		t.Fatalf("SpeakReaction: %v", err)
	}
	if delivered != "I disagree. Strongly." {
		t.Fatalf("delivered = %q, want the reaction text", delivered)
	}
	if len(got) != 2 || got[0].Voice.VoiceID != "b" {
		t.Fatalf("dispatched = %+v, want two sentences in b's voice", got)
	}

	hist := b.HistorySnapshot()
	if len(hist) != 2 {
		t.Fatalf("history len = %d, want 2 (composite user + delivered assistant)", len(hist))
	}
	if !strings.Contains(hist[0].Text, "what do you two think?") || !strings.Contains(hist[0].Text, `A says: "The bridge is out."`) {
		t.Fatalf("committed user msg = %q, want the composite cross-talk text", hist[0].Text)
	}
	if hist[1].Text != "I disagree. Strongly." {
		t.Fatalf("committed assistant = %q, want the delivered reaction", hist[1].Text)
	}

	// Unknown Agent: nothing dispatched, "" nil.
	got = nil
	delivered, err = cast.SpeakReaction(context.Background(), routed("zzz", "x"), "A", "y", "hi", func(rep orchestrator.Reply) error {
		got = append(got, rep)
		return nil
	})
	if err != nil || delivered != "" || len(got) != 0 {
		t.Fatalf("SpeakReaction(unknown) = (%q, %v) dispatched %d, want (\"\", nil) and no dispatch", delivered, err, len(got))
	}
}

// TestCast_SpeakerID_ThreadedThroughEnsemblePaths pins that the Cast hands the
// route's SpeakerID (ADR-0050) to its member Replier on the Draft/Speak and
// Cross-talk paths, so an Ensemble Turn's committed user lines carry the same
// "<Name>: " attribution as a routed turn's.
func TestCast_SpeakerID_ThreadedThroughEnsemblePaths(t *testing.T) {
	namer := func(id string) string {
		if id == "111" {
			return "Artusas"
		}
		return ""
	}
	b := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "b", Markdown: "You are b.", Voice: voiceNamed("b")},
		Engine:      &fakeStreamEngine{deltas: []string{"unused"}},
		Synthesizer: stubSynth{},
		SpeakerName: namer,
	})
	cast := agent.NewCast(b)

	// Speak (the Lead path): the committed user line is name-prefixed.
	if _, err := cast.Speak(context.Background(), routedFrom("b", "111", "who are you?"), "I am B.", func(orchestrator.Reply) error { return nil }); err != nil {
		t.Fatalf("Speak: %v", err)
	}
	hist := b.HistorySnapshot()
	if len(hist) == 0 || hist[0].Text != "Artusas: who are you?" {
		t.Fatalf("Speak committed user line = %+v, want \"Artusas: who are you?\" first", hist)
	}

	// SpeakReaction (the Cross-talk path): the committed composite is the
	// name-prefixed utterance plus the Lead's attributed line.
	c := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "c", Markdown: "You are c.", Voice: voiceNamed("c")},
		Engine:      &fakeStreamEngine{deltas: []string{"unused"}},
		Synthesizer: stubSynth{},
		SpeakerName: namer,
	})
	cast.Add(c)
	if _, err := cast.SpeakReaction(context.Background(), routedFrom("c", "111", "thoughts?"), "A", "The bridge is out.", "I disagree.", func(orchestrator.Reply) error { return nil }); err != nil {
		t.Fatalf("SpeakReaction: %v", err)
	}
	hist = c.HistorySnapshot()
	want := "Artusas: thoughts?\n\nA says: \"The bridge is out.\""
	if len(hist) == 0 || hist[0].Text != want {
		t.Fatalf("SpeakReaction committed composite = %+v, want %q first", hist, want)
	}
}

// TestCast_Draft_RoutesByAgentID pins the EnsembleSpeaker.Draft half (#301): a
// Draft for agent B produces B's would-be text (via B's Replier), and an unknown
// Agent yields "", nil — the "no one answers" signal the coordinator treats as an
// empty draft.
func TestCast_Draft_RoutesByAgentID(t *testing.T) {
	a := castReplier("a", &fakeStreamEngine{deltas: []string{"I am A."}})
	b := castReplier("b", &fakeStreamEngine{deltas: []string{"I am B."}})
	cast := agent.NewCast(a, b)

	draft, err := cast.Draft(context.Background(), routed("b", "who are you?"))
	if err != nil {
		t.Fatalf("Draft: %v", err)
	}
	if draft != "I am B." {
		t.Fatalf("draft = %q, want B's text", draft)
	}

	// Unknown Agent: no member answers, "" nil (not an error).
	got, err := cast.Draft(context.Background(), routed("zzz", "hello"))
	if err != nil || got != "" {
		t.Fatalf("Draft(unknown) = (%q, %v), want (\"\", nil)", got, err)
	}
}

// TestCast_Speak_RoutesByAgentID pins the EnsembleSpeaker.Speak half (#301): a
// Speak for agent B dispatches B's draft in B's Voice and returns the delivered
// text; an unknown Agent dispatches nothing and returns "", nil.
func TestCast_Speak_RoutesByAgentID(t *testing.T) {
	a := castReplier("a", &fakeStreamEngine{deltas: []string{"unused"}})
	b := castReplier("b", &fakeStreamEngine{deltas: []string{"unused"}})
	cast := agent.NewCast(a, b)

	var got []orchestrator.Reply
	delivered, err := cast.Speak(context.Background(), routed("b", "who are you?"), "I am B.", func(rep orchestrator.Reply) error {
		got = append(got, rep)
		return nil
	})
	if err != nil {
		t.Fatalf("Speak: %v", err)
	}
	if delivered != "I am B." {
		t.Fatalf("delivered = %q, want the draft text", delivered)
	}
	if len(got) != 1 || got[0].Voice.VoiceID != "b" {
		t.Fatalf("dispatched = %+v, want one sentence in b's voice", got)
	}

	// Unknown Agent: nothing dispatched, "" nil.
	got = nil
	delivered, err = cast.Speak(context.Background(), routed("zzz", "x"), "hi", func(rep orchestrator.Reply) error {
		got = append(got, rep)
		return nil
	})
	if err != nil || delivered != "" || len(got) != 0 {
		t.Fatalf("Speak(unknown) = (%q, %v) dispatched %d, want (\"\", nil) and no dispatch", delivered, err, len(got))
	}
}

// capturingEngine records every prompt it is handed so a test can assert exactly
// which user line the model reasoned over, then returns a fixed reply.
type capturingEngine struct {
	reply string
	msgs  [][]llm.Message
}

func (e *capturingEngine) Generate(_ context.Context, msgs []llm.Message) (string, error) {
	e.msgs = append(e.msgs, msgs)
	return e.reply, nil
}

// lastUserMessage returns the newest prompt's last user message — the line the
// engine's most recent call reasoned over.
func lastUserMessage(t *testing.T, e *capturingEngine) string {
	t.Helper()
	if len(e.msgs) == 0 {
		t.Fatal("engine was never called")
	}
	msgs := e.msgs[len(e.msgs)-1]
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleUser {
			return msgs[i].Text
		}
	}
	t.Fatal("no user message in the captured prompt")
	return ""
}

// TestCast_EnsembleTurn_UserLineStableAcrossDraftAndSpeak pins the one-resolution
// rule (#473 review): [agent.Config.SpeakerName] is a live cache lookup whose
// answer can change between an Ensemble Turn's Draft (the prompt) and its Speak
// (the history commit) — e.g. the Warm fill landing mid-turn. The Cast resolves
// the attributed user line ONCE at Draft time and reuses the SAME string for the
// turn's Speak, so the committed history is exactly what the draft reasoned over.
func TestCast_EnsembleTurn_UserLineStableAcrossDraftAndSpeak(t *testing.T) {
	resolved := "" // cold cache at Draft time → "Player / DM"
	eng := &capturingEngine{reply: "I am B."}
	b := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "b", Markdown: "You are b.", Voice: voiceNamed("b")},
		Engine:      eng,
		Synthesizer: stubSynth{},
		SpeakerName: func(string) string { return resolved },
	})
	cast := agent.NewCast(b)
	e := routedFrom("b", "111", "a round of ale")
	e.TurnID = "T-ens"

	draft, err := cast.Draft(context.Background(), e)
	if err != nil || draft != "I am B." {
		t.Fatalf("Draft = (%q, %v), want B's text", draft, err)
	}
	if got := lastUserMessage(t, eng); got != "Player / DM: a round of ale" {
		t.Fatalf("Draft reasoned over %q, want the cold-cache line \"Player / DM: a round of ale\"", got)
	}

	// The Warm fill lands between Draft and Speak: the resolver now knows the name.
	resolved = "Artusas"

	if _, err := cast.Speak(context.Background(), e, draft, func(orchestrator.Reply) error { return nil }); err != nil {
		t.Fatalf("Speak: %v", err)
	}
	hist := b.HistorySnapshot()
	if len(hist) == 0 || hist[0].Text != "Player / DM: a round of ale" {
		t.Fatalf("Speak committed %+v, want the SAME line Draft reasoned over, never the re-resolved name", hist)
	}
}

// TestCast_EnsembleTurn_UserLineStableAcrossReactAndSpeakReaction extends the
// one-resolution rule to the Cross-talk half: the reactor's line was pinned at
// its OWN Draft in the fan-out, React's composite prompt reuses it even after
// the resolver warms, and SpeakReaction commits the SAME composite React
// reasoned over (the CrossTalker contract's never-drift guarantee).
func TestCast_EnsembleTurn_UserLineStableAcrossReactAndSpeakReaction(t *testing.T) {
	resolved := ""
	engB := &capturingEngine{reply: "B leads."}
	engC := &capturingEngine{reply: "I disagree."}
	namer := func(string) string { return resolved }
	b := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "b", Markdown: "You are b.", Voice: voiceNamed("b")},
		Engine:      engB,
		Synthesizer: stubSynth{},
		SpeakerName: namer,
	})
	c := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "c", Markdown: "You are c.", Voice: voiceNamed("c")},
		Engine:      engC,
		Synthesizer: stubSynth{},
		SpeakerName: namer,
	})
	cast := agent.NewCast(b, c)
	eb := routedFrom("b", "111", "thoughts?")
	eb.TurnID = "T-ens2"
	ec := routedFrom("c", "111", "thoughts?")
	ec.TurnID = "T-ens2"

	// The speculative fan-out: both candidates draft, pinning their lines.
	if _, err := cast.Draft(context.Background(), eb); err != nil {
		t.Fatalf("Draft(b): %v", err)
	}
	if _, err := cast.Draft(context.Background(), ec); err != nil {
		t.Fatalf("Draft(c): %v", err)
	}

	// The Warm fill lands during the Lead's playback.
	resolved = "Artusas"

	reaction, err := cast.React(context.Background(), ec, "B", "B leads.")
	if err != nil || reaction != "I disagree." {
		t.Fatalf("React = (%q, %v), want c's reaction", reaction, err)
	}
	want := "Player / DM: thoughts?\n\nB says: \"B leads.\""
	if got := lastUserMessage(t, engC); got != want {
		t.Fatalf("React reasoned over %q, want the Draft-time line composite %q", got, want)
	}

	if _, err := cast.SpeakReaction(context.Background(), ec, "B", "B leads.", reaction, func(orchestrator.Reply) error { return nil }); err != nil {
		t.Fatalf("SpeakReaction: %v", err)
	}
	hist := c.HistorySnapshot()
	if len(hist) == 0 || hist[0].Text != want {
		t.Fatalf("SpeakReaction committed %+v, want the SAME composite React reasoned over %q", hist, want)
	}
}

// TestCast_SpeakFallback pins the [orchestrator.FallbackSpeaker] seam (#473
// review): when an Ensemble Turn's every candidate Draft failed terminally, the
// coordinator speaks the top-scored candidate's canned line through the Cast —
// the routed path's Config.FallbackLine mechanism: dispatched in the member's
// Voice, never committed to history, refused for an unknown member, a voiceless
// Persona, or a cancelled ctx.
func TestCast_SpeakFallback(t *testing.T) {
	t.Run("voiced member speaks the canned line", func(t *testing.T) {
		b := castReplier("b", &fakeStreamEngine{deltas: []string{"unused"}})
		cast := agent.NewCast(b)

		var got []orchestrator.Reply
		ok := cast.SpeakFallback(context.Background(), "b", func(rep orchestrator.Reply) error {
			got = append(got, rep)
			return nil
		})
		if !ok {
			t.Fatal("SpeakFallback = false, want true for a voiced member")
		}
		if len(got) != 1 || got[0].Sentence != agent.DefaultFallbackLine || got[0].Voice.VoiceID != "b" {
			t.Fatalf("dispatched = %+v, want exactly the canned line in b's voice", got)
		}
		if hist := b.HistorySnapshot(); len(hist) != 0 {
			t.Fatalf("fallback committed to history: %+v", hist)
		}
	})

	t.Run("unknown member speaks nothing", func(t *testing.T) {
		cast := agent.NewCast()
		var dispatched int
		if cast.SpeakFallback(context.Background(), "ghost", func(orchestrator.Reply) error {
			dispatched++
			return nil
		}) {
			t.Fatal("SpeakFallback = true for an unknown member, want false")
		}
		if dispatched != 0 {
			t.Fatalf("dispatched %d replies for an unknown member, want 0", dispatched)
		}
	})

	t.Run("voiceless persona is refused", func(t *testing.T) {
		v := agent.NewReplier(agent.Config{
			Persona:     agent.Persona{AgentID: "v", Markdown: "You are v."}, // zero Voice: VoiceID ""
			Engine:      &fakeStreamEngine{deltas: []string{"unused"}},
			Synthesizer: stubSynth{},
		})
		cast := agent.NewCast(v)
		var dispatched int
		if cast.SpeakFallback(context.Background(), "v", func(orchestrator.Reply) error {
			dispatched++
			return nil
		}) {
			t.Fatal("SpeakFallback = true for a voiceless Persona, want false (empty VoiceID must never reach TTS)")
		}
		if dispatched != 0 {
			t.Fatalf("dispatched %d replies for a voiceless Persona, want 0", dispatched)
		}
	})

	t.Run("cancelled ctx is refused", func(t *testing.T) {
		b := castReplier("b", &fakeStreamEngine{deltas: []string{"unused"}})
		cast := agent.NewCast(b)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var dispatched int
		if cast.SpeakFallback(ctx, "b", func(orchestrator.Reply) error {
			dispatched++
			return nil
		}) {
			t.Fatal("SpeakFallback = true under a cancelled ctx, want false (a barged unit stays silent)")
		}
		if dispatched != 0 {
			t.Fatalf("dispatched %d replies under a cancelled ctx, want 0", dispatched)
		}
	})
}
