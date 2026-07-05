package wirenpc

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
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// These tests pin the Stage-3 wiring: a Roster assembles N Character NPCs onto
// one address Matcher + one agent Cast, the assembled pipeline routes a named
// utterance to the named NPC (and an unnamed follow-up to whoever last spoke),
// and the programmatic Add/RemoveNPC control surface makes an NPC addressable or
// silent end-to-end. They run over the real Matcher and real Cast on a real bus
// with a stub streaming engine — no LLM, STT, TTS, or Session (ADR-0019/0021).

// scriptEngine is a stub [agent.StreamingEngine] that always speaks one fixed
// line, so a dispatched sentence identifies which NPC's Replier produced it.
type scriptEngine struct{ line string }

func (e scriptEngine) Generate(context.Context, []llm.Message) (string, error) {
	return e.line, nil
}

func (e scriptEngine) GenerateStream(_ context.Context, _ []llm.Message, onText func(string) error) (string, error) {
	if onText != nil {
		if err := onText(e.line); err != nil {
			return e.line, err
		}
	}
	return e.line, nil
}

// recordingSynth is a [tts.Synthesizer] that records every sentence dispatched
// to it together with the Voice it was rendered in, so a test can attribute a
// spoken sentence to its NPC (the Voice's VoiceID carries the agentID, stamped
// by specFor). It returns an immediately-closed channel — no audio.
type recordingSynth struct {
	mu    sync.Mutex
	spoke []spoken
}

type spoken struct {
	sentence string
	voiceID  string
}

func (s *recordingSynth) Synthesize(_ context.Context, req tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	s.mu.Lock()
	s.spoke = append(s.spoke, spoken{sentence: req.Sentence, voiceID: req.Voice.VoiceID})
	s.mu.Unlock()
	ch := make(chan tts.AudioChunk)
	close(ch)
	return ch, nil
}

func (s *recordingSynth) AudioMarkupPrompt(tts.Voice) string { return "" }

func (s *recordingSynth) spokenBy(voiceID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, sp := range s.spoke {
		if sp.voiceID == voiceID {
			out = append(out, sp.sentence)
		}
	}
	return out
}

// specFor builds an npcSpec for a named NPC, stamping the agentID into the
// Voice's VoiceID so the recordingSynth can attribute a sentence to its speaker.
func specFor(agentID, name, line string) npcSpec {
	return npcSpec{
		agentID: agentID,
		name:    name,
		persona: "You are " + name + ".",
		voice:   tts.Voice{ProviderID: "test", VoiceID: agentID, Name: name},
		aliases: nil,
	}
}

// replierFor builds the Replier the Roster would assemble for spec, over a
// scripted engine that always says line — the test seam for replacing the live
// Groq engine.
func replierFor(spec npcSpec, line string, synth tts.Synthesizer) *agent.Replier {
	return agent.NewReplier(agent.Config{
		Persona: agent.Persona{
			AgentID:  spec.agentID,
			Markdown: spec.persona,
			Voice:    spec.voice,
		},
		Engine:      scriptEngine{line: line},
		Synthesizer: synth,
	})
}

// testRoster assembles a Roster over the real Matcher + Cast on bus, with each
// NPC backed by a scripted engine that speaks lines[spec.agentID]. It registers
// the detector + cast reply stream on a Conversation and returns the Roster, a
// publish helper that drives one utterance through the bus, and a teardown.
func testRoster(t *testing.T, bus *voiceevent.Bus, synth *recordingSynth, specs []npcSpec, lines map[string]string) (*Roster, func(text string)) {
	t.Helper()

	repliers := make([]*agent.Replier, 0, len(specs))
	for _, s := range specs {
		repliers = append(repliers, replierFor(s, lines[s.agentID], synth))
	}
	roster := newRosterFor(specs, repliers, synth)

	// Bind just the detector + streaming reply reactors on the bus: we drive the
	// pipeline by publishing STTFinal directly (no audio), so the VAD/STT segmenter
	// is not needed. The TTS stage records spoken sentences via the recordingSynth.
	ttsStage := orchestrator.NewTTS(bus, synth)
	detector := orchestrator.NewAddressDetector(roster.matcher)
	replier := orchestrator.NewStreamReplier(ttsStage, roster.cast.ReplyStream(), nil)
	cancel := orchestrator.Bind(context.Background(), bus, detector, replier)
	t.Cleanup(cancel)

	publish := func(text string) {
		bus.Publish(voiceevent.STTFinal{At: time.Now(), Text: text})
		// The reply path dispatches synchronously on the bus goroutine (no floor),
		// so the synth has recorded by the time Publish returns.
	}
	return roster, publish
}

// newRosterFor builds a Roster whose initial NPCs use the given pre-built
// repliers (test seam) instead of the live Groq engine, but the same Matcher /
// Cast assembly the production path uses. It mirrors the production constructor;
// production builds its repliers from a shared engine.
func newRosterFor(specs []npcSpec, repliers []*agent.Replier, synth tts.Synthesizer) *Roster {
	r := newRoster(rosterDeps{replierFor: func(s npcSpec) *agent.Replier {
		// Tests pre-build repliers; map agentID -> replier so a later AddNPC also
		// resolves through the same scripted engines.
		for i, sp := range specs {
			if sp.agentID == s.agentID {
				return repliers[i]
			}
		}
		// Fall back to a silent replier so an unscripted AddNPC still assembles.
		return replierFor(s, "(silent)", synth)
	}})
	for _, s := range specs {
		r.AddNPC(s)
	}
	return r
}

// TestRoster_MatcherUsesCampaignLanguage (#199): a Roster assembled for a "de"
// Campaign matches German names through Kölner Phonetik. "Yeager" is an
// EN-biased STT's rendering of "Jäger" — Kölner Phonetik codes both 047, while
// Double Metaphone separates them (JKR vs AKR) and the edit net is out of
// reach (Damerau-Levenshtein 3) — so only the German encoder routes it. Two
// NPCs keep the lone-NPC fallback inert, so a route proves the name tier.
func TestRoster_MatcherUsesCampaignLanguage(t *testing.T) {
	synth := &recordingSynth{}
	deps := rosterDeps{
		replierFor: func(s npcSpec) *agent.Replier { return replierFor(s, "(unused)", synth) },
		language:   "de",
	}
	r := newRoster(deps)
	r.AddNPC(specFor("npc-jaeger", "Jäger", ""))
	r.AddNPC(specFor("npc-lena", "Lena", ""))

	routed := r.matcher.TargetMatch("Yeager, wie läuft die Jagd?")
	if len(routed) != 1 || routed[0].Target.AgentID != "npc-jaeger" {
		got := make([]string, len(routed))
		for i, rt := range routed {
			got[i] = rt.Target.AgentID
		}
		t.Fatalf(`"Yeager" under language "de" addressed %v, want [npc-jaeger]`, got)
	}
}

// TestRoster_UnknownLanguageFallsBackToEnglishPhonetics (#199): a Campaign
// Language with no registered phonetic encoder degrades to the "en" encoder —
// pre-#199 behavior — rather than to the bare edit-distance net. "nite" is a
// phonetic rendering of "Knight" (Double Metaphone NT for both) that the edit
// net cannot reach (Damerau-Levenshtein 4), so a route proves the EN encoder
// is live under the unknown code.
func TestRoster_UnknownLanguageFallsBackToEnglishPhonetics(t *testing.T) {
	synth := &recordingSynth{}
	deps := rosterDeps{
		replierFor: func(s npcSpec) *agent.Replier { return replierFor(s, "(unused)", synth) },
		language:   "tlh",
	}
	r := newRoster(deps)
	r.AddNPC(specFor("npc-knight", "Knight", ""))
	r.AddNPC(specFor("npc-lena", "Lena", ""))

	routed := r.matcher.TargetMatch("nite, guard the gate at dawn")
	if len(routed) != 1 || routed[0].Target.AgentID != "npc-knight" {
		got := make([]string, len(routed))
		for i, rt := range routed {
			got[i] = rt.Target.AgentID
		}
		t.Fatalf(`"nite" under unregistered language "tlh" addressed %v, want [npc-knight]`, got)
	}
}

// TestRoster_RoutesNamedUtterancesToEachNPC pins multi-NPC routing through the
// assembled conversation: naming Aldra speaks Aldra; naming Bram speaks Bram.
// Real Matcher + real Cast over the bus, single-target default.
func TestRoster_RoutesNamedUtterancesToEachNPC(t *testing.T) {
	bus := voiceevent.NewBus()
	synth := &recordingSynth{}
	specs := []npcSpec{
		specFor("aldra", "Aldra", ""),
		specFor("bram", "Bram", ""),
	}
	lines := map[string]string{"aldra": "Aldra here.", "bram": "Bram here."}
	_, publish := testRoster(t, bus, synth, specs, lines)

	publish("Aldra, are you there?")
	if got := synth.spokenBy("aldra"); len(got) != 1 || got[0] != "Aldra here." {
		t.Fatalf("naming Aldra spoke %v, want [\"Aldra here.\"]", got)
	}
	if got := synth.spokenBy("bram"); len(got) != 0 {
		t.Fatalf("naming Aldra also spoke Bram %v, want none (single-target)", got)
	}

	publish("Bram, your turn.")
	if got := synth.spokenBy("bram"); len(got) != 1 || got[0] != "Bram here." {
		t.Fatalf("naming Bram spoke %v, want [\"Bram here.\"]", got)
	}
}

// TestRoster_UnnamedFollowUpContinuesLastAddressed pins the continuation
// heuristic end-to-end: after naming Bram, an unnamed follow-up routes back to
// Bram, not Aldra (last-addressed continuation over the real Matcher).
func TestRoster_UnnamedFollowUpContinuesLastAddressed(t *testing.T) {
	bus := voiceevent.NewBus()
	synth := &recordingSynth{}
	specs := []npcSpec{
		specFor("aldra", "Aldra", ""),
		specFor("bram", "Bram", ""),
	}
	lines := map[string]string{"aldra": "Aldra here.", "bram": "Bram here."}
	_, publish := testRoster(t, bus, synth, specs, lines)

	publish("Bram, tell me a tale.")
	publish("And then what happened?") // unnamed: continues Bram

	if got := synth.spokenBy("bram"); len(got) != 2 {
		t.Fatalf("Bram spoke %d times, want 2 (named + continuation): %v", len(got), got)
	}
	if got := synth.spokenBy("aldra"); len(got) != 0 {
		t.Fatalf("Aldra spoke %v on a continuation that belonged to Bram, want none", got)
	}
}

// TestRoster_AddNPC_BecomesAddressable pins the programmatic control surface: an
// NPC added after assembly becomes addressable end-to-end through both the
// Matcher (so it is routed) and the Cast (so it speaks).
func TestRoster_AddNPC_BecomesAddressable(t *testing.T) {
	bus := voiceevent.NewBus()
	synth := &recordingSynth{}
	specs := []npcSpec{specFor("aldra", "Aldra", "")}
	lines := map[string]string{"aldra": "Aldra here."}
	roster, publish := testRoster(t, bus, synth, specs, lines)

	// Before Add, naming Cyra routes to nobody the Cast holds (the lone NPC
	// fallback may route to Aldra, but never to Cyra) — Cyra is silent.
	publish("Cyra, are you about?")
	if got := synth.spokenBy("cyra"); len(got) != 0 {
		t.Fatalf("Cyra spoke %v before being added, want none", got)
	}

	roster.AddNPC(specFor("cyra", "Cyra", ""))

	publish("Cyra, are you about?")
	if got := synth.spokenBy("cyra"); len(got) != 1 {
		t.Fatalf("Cyra spoke %d times after AddNPC, want 1: %v", len(got), got)
	}
}

// TestRoster_RemoveNPC_GoesSilentAndStopsContinuations pins the other half of
// the control surface: a removed NPC stops being routed (matcher) and stops
// speaking (cast), AND stops catching unnamed continuations — its last-addressed
// state must be pruned so a later unnamed utterance does not resurrect it.
func TestRoster_RemoveNPC_GoesSilentAndStopsContinuations(t *testing.T) {
	bus := voiceevent.NewBus()
	synth := &recordingSynth{}
	specs := []npcSpec{
		specFor("aldra", "Aldra", ""),
		specFor("bram", "Bram", ""),
	}
	lines := map[string]string{"aldra": "Aldra here.", "bram": "Bram here."}
	roster, publish := testRoster(t, bus, synth, specs, lines)

	publish("Bram, stay a while.")
	if got := synth.spokenBy("bram"); len(got) != 1 {
		t.Fatalf("Bram spoke %d times before removal, want 1", len(got))
	}

	roster.RemoveNPC("bram")

	// Named: removed Bram says nothing.
	publish("Bram, are you still here?")
	if got := synth.spokenBy("bram"); len(got) != 1 {
		t.Fatalf("removed Bram spoke %d times total, want 1 (silent after removal)", len(got))
	}
	// Unnamed continuation must not resurrect Bram (his lastAddressed was pruned).
	publish("Well, anyone?")
	if got := synth.spokenBy("bram"); len(got) != 1 {
		t.Fatalf("removed Bram caught a continuation (%d total), want 1 — lastAddressed not pruned", len(got))
	}
}

// TestRoster_SetMuted_DropsAndRestoresMatching pins the matcher-only mute (#211):
// a muted NPC is never matched by name/alias nor caught as an unnamed
// continuation (its lastAddressed is pruned), so a 2-NPC scene's unnamed speech
// re-routes to the remaining NPC; unmuting makes it addressable again.
func TestRoster_SetMuted_DropsAndRestoresMatching(t *testing.T) {
	bus := voiceevent.NewBus()
	synth := &recordingSynth{}
	specs := []npcSpec{
		specFor("aldra", "Aldra", ""),
		specFor("bram", "Bram", ""),
	}
	lines := map[string]string{"aldra": "Aldra here.", "bram": "Bram here."}
	roster, publish := testRoster(t, bus, synth, specs, lines)

	publish("Bram, stay a while.")
	if got := synth.spokenBy("bram"); len(got) != 1 {
		t.Fatalf("Bram spoke %d times before mute, want 1", len(got))
	}

	roster.SetMuted("bram", true)

	// Named: muted Bram says nothing (its name no longer matches — it left the Matcher).
	publish("Bram, are you there?")
	if got := synth.spokenBy("bram"); len(got) != 1 {
		t.Fatalf("muted Bram spoke when named (%d total), want 1 (silent)", len(got))
	}
	// Unnamed: the continuation must NOT resurrect Bram (his lastAddressed was
	// pruned) — it re-routes to the remaining NPC (Aldra), who stays reachable.
	publish("Anyone still around?")
	if got := synth.spokenBy("bram"); len(got) != 1 {
		t.Fatalf("muted Bram caught an unnamed continuation (%d total), want 1 — lastAddressed not pruned", len(got))
	}
	if got := synth.spokenBy("aldra"); len(got) < 1 {
		t.Fatal("with Bram muted the remaining NPC (Aldra) must still catch unnamed speech — the 2-NPC fallback")
	}

	roster.SetMuted("bram", false)

	// Unmuted: Bram is addressable again.
	publish("Bram, welcome back.")
	if got := synth.spokenBy("bram"); len(got) != 2 {
		t.Fatalf("unmuted Bram spoke %d times total, want 2 (addressable again)", len(got))
	}
}

// TestRoster_SetMuted_UnmuteKeepsReplierHistory pins AC3's "context intact":
// SetMuted touches ONLY the Matcher, never the Cast, so a muted-then-unmuted NPC
// keeps its SAME agent.Replier — its conversation history (ADR-0012 delivered-only
// commit log) survives the mute. Implementing mute as RemoveNPC would destroy that
// history; this proves it does not.
func TestRoster_SetMuted_UnmuteKeepsReplierHistory(t *testing.T) {
	bus := voiceevent.NewBus()
	synth := &recordingSynth{}
	specs := []npcSpec{
		specFor("aldra", "Aldra", ""),
		specFor("bram", "Bram", ""),
	}
	// Real agent.Repliers (over scripted engines) so history accumulates per turn.
	aldra := replierFor(specs[0], "Aldra here.", synth)
	bram := replierFor(specs[1], "Bram here.", synth)
	roster := newRosterFor(specs, []*agent.Replier{aldra, bram}, synth)

	ttsStage := orchestrator.NewTTS(bus, synth)
	detector := orchestrator.NewAddressDetector(roster.matcher)
	replier := orchestrator.NewStreamReplier(ttsStage, roster.cast.ReplyStream(), nil)
	t.Cleanup(orchestrator.Bind(context.Background(), bus, detector, replier))
	publish := func(text string) { bus.Publish(voiceevent.STTFinal{At: time.Now(), Text: text}) }

	publish("Bram, tell me a tale.")
	before := len(bram.HistorySnapshot())
	if before != 2 { // user + assistant
		t.Fatalf("Bram history after turn 1 = %d messages, want 2", before)
	}

	roster.SetMuted("bram", true)
	roster.SetMuted("bram", false)

	publish("Bram, and then?")
	hist := bram.HistorySnapshot()
	if len(hist) != 4 {
		t.Fatalf("Bram history after unmute+turn 2 = %d messages, want 4 (turn 1 preserved — same Replier survived the mute)", len(hist))
	}
	// Turn 1's user message must still be first — the mute did not reset the log.
	if !strings.Contains(hist[0].Text, "tell me a tale") {
		t.Fatalf("Bram history[0] = %q, want turn 1's message preserved", hist[0].Text)
	}
}

// TestRoster_SetMuted_UnknownIDNoOp proves muting an id the Roster never held is a
// clean no-op (it neither panics nor touches the Matcher).
func TestRoster_SetMuted_UnknownIDNoOp(t *testing.T) {
	bus := voiceevent.NewBus()
	synth := &recordingSynth{}
	specs := []npcSpec{specFor("bart", "Bart", "")}
	lines := map[string]string{"bart": "What'll it be?"}
	roster, publish := testRoster(t, bus, synth, specs, lines)

	roster.SetMuted("ghost", true) // unknown id
	publish("Bart, a room please.")
	if got := synth.spokenBy("bart"); len(got) != 1 {
		t.Fatalf("Bart spoke %d times after an unknown-id mute, want 1 (no-op)", len(got))
	}
}

// fixedMutes is a fixed-membership orchestrator.MuteView for the wireMutes tests.
type fixedMutes map[string]bool

func (m fixedMutes) Muted(agentID string) bool { return m[agentID] }

// TestWireMutes_AppliesBusEvents pins the live control path (#211): a MuteChanged
// on the bus de-routes the NPC via the roster, and an unmute restores it —
// without touching the Cast.
func TestWireMutes_AppliesBusEvents(t *testing.T) {
	bus := voiceevent.NewBus()
	synth := &recordingSynth{}
	specs := []npcSpec{specFor("aldra", "Aldra", ""), specFor("bram", "Bram", "")}
	roster := newRosterFor(specs, []*agent.Replier{
		replierFor(specs[0], "Aldra here.", synth),
		replierFor(specs[1], "Bram here.", synth),
	}, synth)

	t.Cleanup(wireMutes(bus, roster, nil))

	bus.Publish(voiceevent.MuteChanged{AgentID: "bram", Muted: true})
	if routedTo(roster, "Bram, are you there?") == "bram" {
		t.Fatal("after a mute bus event, naming Bram still routed to Bram — he must have left the Matcher")
	}
	bus.Publish(voiceevent.MuteChanged{AgentID: "bram", Muted: false})
	routed := roster.matcher.TargetMatch("Bram, are you there?")
	if len(routed) != 1 || routed[0].Target.AgentID != "bram" {
		t.Fatalf("after an unmute bus event, naming Bram routed to %v, want [bram]", routed)
	}
}

// TestWireMutes_SeedsFromView pins the reconnect re-apply (#211, AC5): on connect
// a freshly-rebuilt roster seeds the current mute state from the view, so an NPC
// muted before a Discord reconnect stays muted after it — with no bus event.
func TestWireMutes_SeedsFromView(t *testing.T) {
	bus := voiceevent.NewBus()
	synth := &recordingSynth{}
	specs := []npcSpec{specFor("aldra", "Aldra", ""), specFor("bram", "Bram", "")}
	roster := newRosterFor(specs, []*agent.Replier{
		replierFor(specs[0], "Aldra here.", synth),
		replierFor(specs[1], "Bram here.", synth),
	}, synth)

	t.Cleanup(wireMutes(bus, roster, fixedMutes{"bram": true}))

	if routedTo(roster, "Bram, are you there?") == "bram" {
		t.Fatal("a roster seeded with Bram muted still routed his name to Bram, want de-routed")
	}
	// The un-muted NPC is unaffected by the seed.
	if got := routedTo(roster, "Aldra, hello"); got != "aldra" {
		t.Fatalf("seed muted the wrong NPC: Aldra routed to %q, want aldra", got)
	}
}

// routedTo returns the AgentID the matcher routes text to, or "" for no route.
func routedTo(r *Roster, text string) string {
	routed := r.matcher.TargetMatch(text)
	if len(routed) == 0 {
		return ""
	}
	return routed[0].Target.AgentID
}

// TestRoster_SingleNPCBehaviorPreserved pins the Stage-2 acceptance bar: with a
// Roster holding exactly one NPC, both a named utterance and an unnamed one route
// to it (the lone-NPC fallback) — identical to the pre-Roster single-NPC loop.
func TestRoster_SingleNPCBehaviorPreserved(t *testing.T) {
	bus := voiceevent.NewBus()
	synth := &recordingSynth{}
	specs := []npcSpec{specFor("bart", "Bart", "")}
	lines := map[string]string{"bart": "What'll it be?"}
	_, publish := testRoster(t, bus, synth, specs, lines)

	publish("Bart, a room please.")
	publish("Hello, is anyone here?") // unnamed: lone-NPC fallback

	got := synth.spokenBy("bart")
	if len(got) != 2 {
		t.Fatalf("lone NPC spoke %d times (named + unnamed fallback), want 2: %v", len(got), got)
	}
	for _, s := range got {
		if strings.TrimSpace(s) == "" {
			t.Fatalf("lone NPC produced an empty sentence: %q", got)
		}
	}
}
