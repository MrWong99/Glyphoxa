package wirenpc

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
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

// TestRoster_SetMuted_KeepsNamedRoutingForDownstreamGate pins the #225 fix at the
// Roster seam: muting keeps the NPC ROUTABLE by name — it stays in the Matcher —
// and the reactor's MuteView gate (ADR-0012, reactor.go) silences it downstream,
// rather than the mute dropping the name from the Matcher (the #211 bug that let
// a named-muted utterance re-route to another NPC). The test harness wires no
// floor/MuteView, so the named-muted route is asserted at the Matcher level; the
// ambient half (a muted NPC excluded from unnamed continuation/fallback so a
// 2-NPC scene re-routes unnamed speech to the survivor) and the unmute-restore
// half stay at the end-to-end speak level.
func TestRoster_SetMuted_KeepsNamedRoutingForDownstreamGate(t *testing.T) {
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

	// Named: the muted Bram STAYS matched by name (#225). His name must still
	// route to him at the Matcher so the downstream reactor MuteView gate can end
	// the turn with the mute reason — instead of the utterance re-routing to Aldra.
	if got := routedTo(roster, "Bram, are you there?"); got != "bram" {
		t.Fatalf("muted Bram routed to %q when named, want bram (name-gated, silenced downstream — not de-routed)", got)
	}

	// Unnamed: the continuation must NOT resurrect Bram (his lastAddressed was
	// pruned by the mute transition) — it re-routes to the remaining NPC (Aldra).
	publish("Anyone still around?")
	if got := synth.spokenBy("bram"); len(got) != 1 {
		t.Fatalf("muted Bram caught an unnamed continuation (%d total), want 1 — lastAddressed not pruned", len(got))
	}
	if got := synth.spokenBy("aldra"); len(got) < 1 {
		t.Fatal("with Bram muted the remaining NPC (Aldra) must still catch unnamed speech — the 2-NPC fallback")
	}

	roster.SetMuted("bram", false)

	// Unmuted: Bram is addressable again end-to-end.
	publish("Bram, welcome back.")
	if got := synth.spokenBy("bram"); len(got) != 2 {
		t.Fatalf("unmuted Bram spoke %d times total, want 2 (addressable again)", len(got))
	}
}

// TestRoster_MutedDegradedNameStaysWithMutedNPC composes #225 with #197: an
// STT-degraded address to a MUTED NPC ("Art" for "Bart") must still resolve to
// that muted NPC via its derived truncation alias and NEVER leak to another NPC.
// The muted Bart is silenced by the downstream MuteView gate, but the re-route
// bug is closed at the Matcher: "Art, …" routes to Bart, never Greta.
func TestRoster_MutedDegradedNameStaysWithMutedNPC(t *testing.T) {
	synth := &recordingSynth{}
	deps := rosterDeps{
		replierFor: func(s npcSpec) *agent.Replier { return replierFor(s, "(unused)", synth) },
		language:   "de",
	}
	r := newRoster(deps)
	r.AddNPC(specFor("npc-bart", "Bart", ""))
	r.AddNPC(specFor("npc-greta", "Greta", ""))

	r.SetMuted("npc-bart", true)

	if got := routedTo(r, "Art, hörst du mich?"); got != "npc-bart" {
		t.Fatalf(`STT-degraded "Art" to muted Bart routed to %q, want npc-bart (never leaked to Greta)`, got)
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

// TestWireMutes_AppliesBusEvents pins the live control path (#211, #225): a
// MuteChanged on the bus reaches the Matcher's mute state via the roster, and an
// unmute restores it — without touching the Cast. Since #225 a muted NPC stays
// routable by name (silenced downstream by the reactor gate), so the mute is
// observed at the Matcher through the AMBIENT path: the muted NPC is excluded
// from the sole-NPC fallback, leaving the surviving NPC the sole active one.
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
	// Muted Bram is excluded from ambient, so Aldra is the sole active NPC and
	// unnamed speech routes to her (with Bram unmuted, two active NPCs make the
	// sole-NPC fallback inert and unnamed speech routes to nobody).
	if got := routedTo(roster, "is anyone about?"); got != "aldra" {
		t.Fatalf("after a mute bus event, unnamed speech routed to %q, want aldra (muted Bram excluded from ambient)", got)
	}
	bus.Publish(voiceevent.MuteChanged{AgentID: "bram", Muted: false})
	routed := roster.matcher.TargetMatch("Bram, are you there?")
	if len(routed) != 1 || routed[0].Target.AgentID != "bram" {
		t.Fatalf("after an unmute bus event, naming Bram routed to %v, want [bram]", routed)
	}
}

// TestWireMutes_ConcurrentSeedAndBusEvents_RaceClean pins the Roster-lock fix
// (#211): SetMuted is called from the bus-event goroutine (a GM mute) AND the seed
// goroutine (connectAndServe re-applying mutes on a mid-session reconnect). Both
// mutate the Roster's muted/specs maps, so without the lock this is a concurrent
// map write (a runtime FATAL). Run with -race.
func TestWireMutes_ConcurrentSeedAndBusEvents_RaceClean(t *testing.T) {
	bus := voiceevent.NewBus()
	synth := &recordingSynth{}
	specs := []npcSpec{specFor("aldra", "Aldra", ""), specFor("bram", "Bram", ""), specFor("cyra", "Cyra", "")}
	roster := newRosterFor(specs, []*agent.Replier{
		replierFor(specs[0], "a", synth),
		replierFor(specs[1], "b", synth),
		replierFor(specs[2], "c", synth),
	}, synth)
	ids := []string{"aldra", "bram", "cyra"}
	view := fixedMutes{"bram": true}

	t.Cleanup(wireMutes(bus, roster, view)) // subscribes (bus-event → SetMuted) + seeds once

	var wg sync.WaitGroup
	// Bus-event side: each MuteChanged fires the subscription's SetMuted on THIS
	// goroutine.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			bus.Publish(voiceevent.MuteChanged{AgentID: ids[i%len(ids)], Muted: i%2 == 0})
		}
	}()
	// Seed side: connectAndServe's reconnect reconcile, hammered concurrently.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			roster.ApplyMutes(view.Muted)
		}
	}()
	wg.Wait()
}

// TestWireMutes_ReReadsAuthoritativeViewNotPayload pins the cross-op ordering fix
// (#211): wireMutes applies the AUTHORITATIVE view (mutes.Muted), never the
// event's payload — so a stale/reordered MuteChanged (e.g. a mute-all event that
// straddled a later unmute) cannot de-sync the matcher from the Manager. A
// payload that disagrees with the view is ignored in favour of the view.
func TestWireMutes_ReReadsAuthoritativeViewNotPayload(t *testing.T) {
	bus := voiceevent.NewBus()
	synth := &recordingSynth{}
	specs := []npcSpec{specFor("aldra", "Aldra", ""), specFor("bram", "Bram", "")}
	roster := newRosterFor(specs, []*agent.Replier{
		replierFor(specs[0], "Aldra here.", synth),
		replierFor(specs[1], "Bram here.", synth),
	}, synth)

	view := fixedMutes{} // authoritative: bram currently UNMUTED
	t.Cleanup(wireMutes(bus, roster, view))

	// A stale event claims bram is muted, but the view says unmuted → ignored, so
	// bram stays UNMUTED: two active NPCs make the sole-NPC fallback inert and
	// unnamed speech routes to nobody. (Since #225 the mute is observed via the
	// ambient path — a muted NPC stays name-routable, silenced downstream.)
	bus.Publish(voiceevent.MuteChanged{AgentID: "bram", Muted: true})
	if got := routedTo(roster, "is anyone about?"); got != "" {
		t.Fatalf("a stale {bram,true} event muted Bram against the view (unnamed routed to %q, want nobody) — payload was trusted over the view", got)
	}

	// Flip the authoritative view to muted; a stale {bram,false} event must not
	// unmute him — muted Bram is excluded from ambient, so unnamed speech routes
	// to the sole active Aldra.
	view["bram"] = true
	bus.Publish(voiceevent.MuteChanged{AgentID: "bram", Muted: false})
	if got := routedTo(roster, "is anyone about?"); got != "aldra" {
		t.Fatalf("a stale {bram,false} event unmuted Bram against the muted view (unnamed routed to %q, want aldra) — payload trusted over the view", got)
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

	// Seeded muted: Bram is excluded from ambient, so unnamed speech routes to the
	// sole active Aldra. (Since #225 a muted NPC stays name-routable — silenced
	// downstream — so the seed is observed through the ambient path.)
	if got := routedTo(roster, "is anyone about?"); got != "aldra" {
		t.Fatalf("a roster seeded with Bram muted did not exclude him from ambient (unnamed routed to %q, want aldra)", got)
	}
	// The un-muted NPC is unaffected by the seed.
	if got := routedTo(roster, "Aldra, hello"); got != "aldra" {
		t.Fatalf("seed muted the wrong NPC: Aldra routed to %q, want aldra", got)
	}
}

// TestMatcherAgent_DerivesTruncationAliases pins that the ONE derivation call
// site (#197) feeds every wiring path — hardcoded, DB (npcSpecFromAgent), and the
// SetMuted unmute re-add all build their address.Agent through matcherAgent. The
// hardcoded Bart (name "Bart", aliases "innkeeper"/"barkeep") derives "art" from
// the name and "arkeep" from "barkeep"; the vowel-initial "innkeeper" derives
// nothing.
func TestMatcherAgent_DerivesTruncationAliases(t *testing.T) {
	got := matcherAgent(hardcodedNPC()).TruncationAliases
	want := []string{"art", "arkeep"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("matcherAgent truncation aliases = %v, want %v", got, want)
	}
}

// TestMatcherAgent_ButlerRoleAndAddressOnly pins the #299 Butler derivation: a
// butler-role spec produces an address.Agent with AgentRole "butler" and
// AddressOnly true, so ambient heuristics never route to it. A zero-role spec
// stays a non-AddressOnly Character (byte-identical to the pre-#299 default).
func TestMatcherAgent_ButlerRoleAndAddressOnly(t *testing.T) {
	butlerSpec := npcSpec{agentID: "glyphoxa", name: "Glyphoxa", role: string(voiceevent.AgentRoleButler), addressOnly: true}
	got := matcherAgent(butlerSpec)
	if got.Target.AgentRole != voiceevent.AgentRoleButler {
		t.Fatalf("butler matcherAgent role = %q, want %q", got.Target.AgentRole, voiceevent.AgentRoleButler)
	}
	if !got.AddressOnly {
		t.Fatal("butler matcherAgent must be AddressOnly")
	}

	character := matcherAgent(specFor("npc-bart", "Bart", ""))
	if character.Target.AgentRole != voiceevent.AgentRoleCharacter {
		t.Fatalf("zero-role matcherAgent role = %q, want %q", character.Target.AgentRole, voiceevent.AgentRoleCharacter)
	}
	if character.AddressOnly {
		t.Fatal("Character matcherAgent must not be AddressOnly")
	}
}

// TestRoster_ButlerVoiceEndToEnd is the #299 pipeline pin over the real Matcher +
// Cast + TTS: a GM naming the Butler with a short answer gets it SPOKEN in the
// Butler's Voice (AC1's spoken path), a NON-GM naming the Butler is dropped
// matcher-side (GM gate), and a Character NPC named by anyone is undisturbed
// (AC3). Long Butler answers post as text via the TextSink instead of speaking.
func TestRoster_ButlerVoiceEndToEnd(t *testing.T) {
	bus := voiceevent.NewBus()
	synth := &recordingSynth{}
	const gm = "gm-1"
	var posted []string
	poster := func(_ context.Context, text string) error { posted = append(posted, text); return nil }

	butlerVoice := tts.Voice{ProviderID: "test", VoiceID: "glyphoxa", Name: "Glyphoxa"}
	repliers := map[string]*agent.Replier{
		"npc-bart": replierFor(specFor("npc-bart", "Bart", ""), "What'll it be?", synth),
	}
	newButler := func(line string) *agent.Replier {
		return agent.NewReplier(agent.Config{
			Persona:     agent.Persona{AgentID: "glyphoxa", Markdown: "You are Glyphoxa.", Voice: butlerVoice},
			Engine:      scriptEngine{line: line},
			Synthesizer: synth,
			TextSink:    poster,
		})
	}

	deps := rosterDeps{
		replierFor: func(s npcSpec) *agent.Replier {
			if s.agentID == "glyphoxa" {
				return repliers["glyphoxa"]
			}
			return repliers[s.agentID]
		},
		butlerGate: func(id string) bool { return id == gm },
	}
	repliers["glyphoxa"] = newButler("Two sixes. Total nine.")
	r := newRoster(deps)
	r.AddNPC(specFor("npc-bart", "Bart", ""))
	r.AddNPC(npcSpec{agentID: "glyphoxa", name: "Glyphoxa", role: voiceevent.AgentRoleButler, addressOnly: true, voice: butlerVoice})

	ttsStage := orchestrator.NewTTS(bus, synth)
	detector := orchestrator.NewAddressDetector(r.matcher)
	streamRep := orchestrator.NewStreamReplier(ttsStage, r.cast.ReplyStream(), nil)
	t.Cleanup(orchestrator.Bind(context.Background(), bus, detector, streamRep))

	pubFrom := func(speaker, text string) {
		bus.Publish(voiceevent.STTFinal{At: time.Now(), Text: text, SpeakerID: speaker})
	}

	// GM addresses the Butler with a short answer → spoken in the Butler's Voice.
	pubFrom(gm, "Glyphoxa, roll two d6")
	if got := synth.spokenBy("glyphoxa"); len(got) == 0 {
		t.Fatalf("GM 'Glyphoxa, roll two d6' produced no spoken Butler answer; spoke=%+v posted=%v", synth.spoke, posted)
	}
	if len(posted) != 0 {
		t.Errorf("short Butler answer was posted as text %v, want spoken", posted)
	}

	// A NON-GM naming the Butler is dropped matcher-side: no new Butler speech.
	spokenBefore := len(synth.spokenBy("glyphoxa"))
	pubFrom("player-9", "Glyphoxa, roll two d6")
	if got := len(synth.spokenBy("glyphoxa")); got != spokenBefore {
		t.Errorf("non-GM Butler address produced %d Butler lines, want %d (GM gate)", got, spokenBefore)
	}

	// A Character NPC named by anyone is undisturbed (AC3).
	pubFrom("player-9", "Bart, a room please")
	if got := synth.spokenBy("npc-bart"); len(got) == 0 {
		t.Error("Character NPC did not answer when named by a non-GM (AC3 regression)")
	}
}

// TestRoster_ButlerExcludedFromFallback is the AC3 pin at the Roster level: in a
// scene of one Character NPC + the Address-Only Butler, an unnamed utterance
// reaches the Character via the sole-NPC fallback (the Butler is not counted),
// while naming the Butler reaches it. NPC-only routing is unchanged by the
// Butler's presence.
func TestRoster_ButlerExcludedFromFallback(t *testing.T) {
	synth := &recordingSynth{}
	deps := rosterDeps{
		replierFor: func(s npcSpec) *agent.Replier { return replierFor(s, "(unused)", synth) },
	}
	r := newRoster(deps)
	r.AddNPC(specFor("npc-bart", "Bart", ""))
	r.AddNPC(npcSpec{agentID: "glyphoxa", name: "Glyphoxa", role: string(voiceevent.AgentRoleButler), addressOnly: true,
		voice: tts.Voice{ProviderID: "test", VoiceID: "glyphoxa", Name: "Glyphoxa"}})

	if got := routedTo(r, "so what is on tap tonight?"); got != "npc-bart" {
		t.Fatalf("unnamed utterance routed to %q, want npc-bart (Butler excluded from fallback)", got)
	}
	if got := routedTo(r, "Glyphoxa, roll two d6"); got != "glyphoxa" {
		t.Fatalf("named-Butler utterance routed to %q, want glyphoxa", got)
	}
}

// TestRoster_TruncatedNameRoutesViaDerivedAlias is the end-to-end #197 bar over
// the Roster: a "de" scene of Bart/Greta/Marek routes an utterance opening with
// the STT truncation "Art" to Bart and "Arek" to Marek via their derived
// aliases, while the same "Art" mid-sentence (checked first, before any turn
// leaves continuation state) reaches nobody — three NPCs keep the lone-NPC
// fallback inert.
func TestRoster_TruncatedNameRoutesViaDerivedAlias(t *testing.T) {
	synth := &recordingSynth{}
	deps := rosterDeps{
		replierFor: func(s npcSpec) *agent.Replier { return replierFor(s, "(unused)", synth) },
		language:   "de",
	}
	r := newRoster(deps)
	r.AddNPC(specFor("npc-bart", "Bart", ""))
	r.AddNPC(specFor("npc-greta", "Greta", ""))
	r.AddNPC(specFor("npc-marek", "Marek", ""))

	// Mid-sentence "Art" first, while there is no continuation state to lean on.
	if got := routedTo(r, "was für eine Art von Bier hast du?"); got != "" {
		t.Fatalf(`mid-sentence "Art" routed to %q, want nobody (derived alias is utterance-initial only)`, got)
	}
	if got := routedTo(r, "Art, wie läuft das Geschäft heute Abend?"); got != "npc-bart" {
		t.Fatalf(`"Art, …" routed to %q, want npc-bart`, got)
	}
	if got := routedTo(r, "Arek, was liegt auf deinem Amboss?"); got != "npc-marek" {
		t.Fatalf(`"Arek, …" routed to %q, want npc-marek`, got)
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

// routedFrom returns the AgentID the matcher routes text to for a given speaker,
// or "" for no route (the SpeakerID-aware Butler GM-gate path).
func routedFrom(r *Roster, speakerID, text string) string {
	routed := r.matcher.TargetMatchFrom(speakerID, text)
	if len(routed) == 0 {
		return ""
	}
	return routed[0].Target.AgentID
}

// TestRosterDepsForLive_TextSinkOnButlerOnly pins the #299 live wiring: the text
// poster is installed as the TextSink on butler-role specs only (so a long Butler
// answer posts to the channel chat), while a Character NPC keeps the pure-TTS
// path (nil TextSink), and the GM-gate predicate reaches the Roster.
func TestRosterDepsForLive_TextSinkOnButlerOnly(t *testing.T) {
	synth := &recordingSynth{}
	long := strings.Repeat("word ", 200) // > 400 runes → text modality
	engineFor := func(npcSpec) agent.Engine { return scriptEngine{line: long} }
	var posted []string
	poster := func(_ context.Context, text string) error { posted = append(posted, text); return nil }
	gm := func(string) bool { return false }
	log := slog.New(slog.DiscardHandler)

	deps := rosterDepsForLive(conversationDeps{log: log, gmSpeaker: gm, textPoster: poster}, engineFor, synth, 16)
	if deps.butlerGate == nil {
		t.Fatal("rosterDepsForLive did not thread the GM-gate predicate")
	}

	// Butler spec: a long answer posts through the TextSink, not TTS.
	butlerR := deps.replierFor(npcSpec{agentID: "glyphoxa", name: "Glyphoxa", role: voiceevent.AgentRoleButler,
		voice: tts.Voice{ProviderID: "test", VoiceID: "g", Name: "Glyphoxa"}})
	butlerRoute := voiceevent.AddressRouted{At: time.Now(), Text: "Glyphoxa, recap everything",
		Target: voiceevent.AddressTarget{AgentID: "glyphoxa", AgentRole: voiceevent.AgentRoleButler, Name: "Glyphoxa"}}
	// A text-delivered Butler turn returns the terminal sentinel (#299), not nil.
	if err := butlerR.ReplyStream()(context.Background(), butlerRoute, func(orchestrator.Reply) error { return nil }); !errors.Is(err, orchestrator.ErrTextDelivered) {
		t.Fatalf("butler ReplyStream err = %v, want ErrTextDelivered", err)
	}
	if len(posted) == 0 {
		t.Error("butler long answer was not posted via the TextSink")
	}

	// Character spec: no TextSink — the answer goes to TTS, the poster is untouched.
	posted = nil
	charR := deps.replierFor(specFor("npc-bart", "Bart", ""))
	charRoute := voiceevent.AddressRouted{At: time.Now(), Text: "Bart, tell me a long story",
		Target: voiceevent.AddressTarget{AgentID: "npc-bart", AgentRole: voiceevent.AgentRoleCharacter, Name: "Bart"}}
	var dispatched int
	if err := charR.ReplyStream()(context.Background(), charRoute, func(orchestrator.Reply) error { dispatched++; return nil }); err != nil {
		t.Fatalf("character ReplyStream: %v", err)
	}
	if len(posted) != 0 {
		t.Errorf("character answer posted via TextSink %d times, want 0", len(posted))
	}
	if dispatched == 0 {
		t.Error("character answer was not dispatched to TTS")
	}
}

// TestRoster_ButlerGateThreadedIntoMatcher pins the #299 wiring: rosterDeps.butlerGate
// reaches the Matcher's ButlerGMGate, so a non-GM naming the Butler routes nowhere
// while the GM's identical utterance reaches it — enforced matcher-side (pre-cap).
func TestRoster_ButlerGateThreadedIntoMatcher(t *testing.T) {
	synth := &recordingSynth{}
	const gm = "gm-1"
	deps := rosterDeps{
		replierFor: func(s npcSpec) *agent.Replier { return replierFor(s, "(unused)", synth) },
		butlerGate: func(id string) bool { return id == gm },
	}
	r := newRoster(deps)
	r.AddNPC(specFor("npc-bart", "Bart", ""))
	r.AddNPC(npcSpec{agentID: "glyphoxa", name: "Glyphoxa", role: voiceevent.AgentRoleButler, addressOnly: true,
		voice: tts.Voice{ProviderID: "test", VoiceID: "glyphoxa", Name: "Glyphoxa"}})

	if got := routedFrom(r, "player", "Glyphoxa, roll a d6"); got != "" {
		t.Errorf("non-GM naming Butler routed to %q, want nobody (matcher gate)", got)
	}
	if got := routedFrom(r, gm, "Glyphoxa, roll a d6"); got != "glyphoxa" {
		t.Errorf("GM naming Butler routed to %q, want glyphoxa", got)
	}
	// Character routing is unaffected by the gate.
	if got := routedFrom(r, "player", "Bart, a drink"); got != "npc-bart" {
		t.Errorf("Character routing = %q, want npc-bart", got)
	}
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
