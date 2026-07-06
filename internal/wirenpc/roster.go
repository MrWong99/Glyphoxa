package wirenpc

import (
	"log/slog"
	"sync"

	"github.com/MrWong99/Glyphoxa/pkg/voice/address"
	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// Roster is the multi-NPC composition root: it ties one address [address.Matcher]
// to one [agent.Cast] so a Voice Session can host several Character NPCs that
// route and speak independently over one bus and one barge-in floor. It is THE
// programmatic control surface for the scene's membership — [Roster.AddNPC] and
// [Roster.RemoveNPC] add or drop an NPC from BOTH the matcher (so it is/ isn't
// routed) and the Cast (so it does/doesn't speak), keeping the two halves of an
// NPC's presence in lockstep (this slice has no Discord/HTTP trigger; the
// programmatic API is the only seam — issue #49).
//
// "Roster" is a wiring construct, not a domain term: the glossary (CONTEXT.md)
// names the parts it binds — the Voice Session's Agents, Address Detection, and
// the Cast multiplexer — but has no word for "the live set of NPCs in a Session"
// as one handle, so this is the composition-root name.
//
// The Matcher is built lazily on the first AddNPC because [address.NewMatcher]
// requires at least one Agent; subsequent adds use [address.Matcher.Add]. A
// Roster is built and mutated from the same goroutine that owns the voice loop;
// the Matcher and Cast are each independently concurrency-safe for the bus's
// dispatch.
type Roster struct {
	matcher *address.Matcher
	cast    *agent.Cast

	// language is the Campaign Language the Matcher's phonetic tier encodes
	// names under (#199); set from rosterDeps at construction.
	language string

	// replierFor builds the [agent.Replier] for one NPC. Production binds it to a
	// shared tool-engine (so N NPCs share one Groq client); tests inject scripted
	// engines through it. Always non-nil after [newRoster].
	replierFor func(npcSpec) *agent.Replier

	// specs retains each held NPC's spec by agentID so [Roster.ApplyMutes] can
	// iterate the current membership when reconciling the authoritative mute view
	// on a reconnect (#211). Populated by [Roster.AddNPC], pruned by
	// [Roster.RemoveNPC]. Mute state itself lives in the Matcher (#225), not here.
	specs map[string]npcSpec

	// mu guards the specs map (and the AddNPC/RemoveNPC Matcher mutations it moves
	// in lockstep with). Mute control breaks the "one goroutine owns the Roster"
	// contract (#211): wireMutes calls [Roster.SetMuted] from the bus-event
	// callback (the MuteChanged publisher's goroutine) AND seeds via
	// [Roster.ApplyMutes] from connectAndServe's goroutine — a mid-session
	// reconnect racing a GM mute would otherwise be a concurrent map read/write.
	// All specs access (and the ApplyMutes range) goes under mu; the Matcher's own
	// mute state is separately guarded by its mutex.
	mu sync.Mutex
}

// rosterDeps carries what a [Roster] needs to assemble an NPC beyond the NPC's
// own spec: the replier factory. It keeps [newRoster] callable from both the
// production wiring (repliers over a shared Groq engine) and the tests (scripted
// engines) through the one seam, rather than the Roster importing the engine.
type rosterDeps struct {
	// replierFor builds the Replier for one NPC; see [Roster.replierFor].
	replierFor func(npcSpec) *agent.Replier
	// language is the Campaign Language (CONTEXT.md) selecting the Matcher's
	// phonetic encoder (#199): the loaded campaign's language column on the DB
	// path, "" on the env-only path.
	language string
}

// newRoster builds an empty Roster wired to deps. It holds no NPCs yet — the
// Matcher is created on the first [Roster.AddNPC] (address.NewMatcher needs one
// Agent). deps.replierFor must be non-nil.
func newRoster(deps rosterDeps) *Roster {
	if deps.replierFor == nil {
		panic("wirenpc.newRoster: replierFor must not be nil")
	}
	return &Roster{
		cast:       agent.NewCast(),
		replierFor: deps.replierFor,
		language:   matcherLanguage(deps.language),
		specs:      map[string]npcSpec{},
	}
}

// matcherLanguage returns lang if the address package ships a phonetic encoder
// for it, else "en" (#199): a Campaign Language the platform has no phonetics
// for degrades to the EN encoder — the pre-#199 behavior — rather than to the
// bare edit-distance net.
func matcherLanguage(lang string) string {
	if _, ok := address.DefaultEncoders().For(lang); ok {
		return lang
	}
	return "en"
}

// matcherAgent builds the address.Agent for one NPC: its routing target, aliases,
// and the STT-truncation aliases derived from its name + aliases (#197). This is
// the ONE derivation site, so every path that builds an address.Agent — the
// hardcoded NPC and the DB load (via npcSpecFromAgent) — gets the derived aliases
// identically. A lone Character NPC is not AddressOnly, so it catches unaddressed
// speech via the single-NPC fallback (CONTEXT.md "Address-Only").
func matcherAgent(spec npcSpec) address.Agent {
	return address.Agent{
		Target: voiceevent.AddressTarget{
			AgentID:   spec.agentID,
			AgentRole: "character",
			Name:      spec.name,
		},
		Aliases:           spec.aliases,
		TruncationAliases: address.DeriveTruncationAliases(append([]string{spec.name}, spec.aliases...)...),
	}
}

// AddNPC brings one Character NPC into the scene: it registers the NPC's routing
// Agent in the Matcher (so utterances naming it — or, for a lone NPC, any
// unaddressed speech — route to it) and the NPC's Replier in the Cast (so the
// route is answered in its Voice). The first AddNPC builds the Matcher; later
// ones extend the live roster via [address.Matcher.Add]. Both halves move
// together so an NPC is never routable-but-silent or speaking-but-unroutable.
func (r *Roster) AddNPC(spec npcSpec) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.matcher == nil {
		// First NPC: build the Matcher around it. Single-target by default
		// (Config.MaxTargets unset ⇒ 1): naming two NPCs fires one turn on the
		// top-scored, the safe one-floor default (ADR-0025 deferred).
		r.matcher = address.NewMatcher(address.Config{Language: r.language}, matcherAgent(spec))
	} else {
		r.matcher.Add(matcherAgent(spec))
	}
	r.cast.Add(r.replierFor(spec))
	r.specs[spec.agentID] = spec
}

// RemoveNPC drops the NPC with agentID from the scene: it leaves the Matcher (so
// nothing routes to it, and its last-addressed/interruption state is pruned so a
// later unnamed continuation cannot resurrect it) and the Cast (so even a stray
// route says nothing). Removing an unknown agentID is a no-op. Removing every
// NPC leaves the Matcher routing to nobody — the voice loop simply stays quiet.
func (r *Roster) RemoveNPC(agentID string) {
	r.mu.Lock()
	if r.matcher != nil {
		r.matcher.Remove(agentID) // also clears the Matcher's mute flag for this NPC (#225)
	}
	delete(r.specs, agentID)
	r.mu.Unlock()
	r.cast.Remove(agentID) // Cast is independently concurrency-safe; outside r.mu
}

// SetMuted toggles one NPC's mute in the live scene (#211, #225). It is
// deliberately MATCHER-ONLY and NEVER touches the Cast, so the NPC keeps its SAME
// [agent.Replier] — and its ADR-0012 delivered-only history — across a mute,
// intact the instant it is unmuted (AC3 "context intact").
//
// Muting does NOT drop the NPC from the Matcher. That was the #225 failure: a
// dropped name stops matching, so "Bart, hörst du mich?" to a muted Bart
// re-routed to another NPC instead of staying unanswered. Instead the Matcher
// keeps the muted NPC ROUTABLE by name but name-gates it (excluded from the
// ambient heuristics, dropped from a shared decision, never recorded as
// lastAddressed) — see [address.Matcher.SetMuted]. A named-muted utterance thus
// still resolves to the muted NPC, whose turn the reactor's MuteView gate then
// ends before any audio (so it produces no audio and no transcript line), rather
// than leaking to a bystander. A muted NPC stays in the scene (the conversation
// keeps accruing around it).
//
// This is NOT [Roster.RemoveNPC]: removing the Cast entry would destroy the
// Replier and its history, the exact failure to avoid. Muting an id the Roster
// never held is a no-op, and re-applying the current state is idempotent (both
// contracts enforced by [address.Matcher.SetMuted]).
//
// Concurrency-safe: SetMuted is called from the bus-event goroutine AND the seed
// path (see [Roster.mu]).
func (r *Roster) SetMuted(agentID string, muted bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setMutedLocked(agentID, muted)
}

// ApplyMutes reconciles the whole roster to the authoritative view under the
// Roster lock (#211): for each held NPC it sets its mute to muted(agentID). It is
// the seed/reconnect path — a mid-session Discord reconnect re-applies the mutes
// that were in effect — run under [Roster.mu] so it never races the bus-event
// SetMuted. muted must be non-nil.
func (r *Roster) ApplyMutes(muted func(agentID string) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id := range r.specs {
		r.setMutedLocked(id, muted(id))
	}
}

// setMutedLocked forwards the mute to the Matcher, which owns the mute state
// (#225): muting keeps the NPC in the Matcher — still routable by name — but
// name-gates it (excluded from the ambient heuristics, dropped from a shared
// decision, never recorded as lastAddressed), so a named-muted utterance no
// longer re-routes to another NPC. The reactor's MuteView gate silences the
// muted NPC downstream (ADR-0012). Idempotence and the unknown-id no-op live in
// [address.Matcher.SetMuted]. The caller holds r.mu.
func (r *Roster) setMutedLocked(agentID string, muted bool) {
	if r.matcher == nil {
		return // no matcher yet: nothing to route or de-route
	}
	r.matcher.SetMuted(agentID, muted)
}

// rosterDepsForLive builds the production rosterDeps: every NPC's Replier is
// constructed from a per-NPC engine (engineFor), so each NPC carries its own
// hydrated GrantSet (#113) while still sharing one Groq client and Registry under
// the hood — N Character NPCs reuse one client rather than each opening their
// own. memory is the shared NPC memory recaller (#122) and facts the shared
// KG-facts recaller (#126); every NPC's loop consults the SAME recallers, which
// scope by the addressed AgentID / active Campaign per turn. A nil memory/facts
// disables that slot (the prompt stays byte-identical). language is the Campaign
// Language selecting the Matcher's phonetic encoder (#199).
func rosterDepsForLive(engineFor func(npcSpec) agent.Engine, synth tts.Synthesizer, historyTurns int, log *slog.Logger, memory agent.MemoryRecaller, facts agent.FactsRecaller, language string) rosterDeps {
	return rosterDeps{
		language: language,
		replierFor: func(spec npcSpec) *agent.Replier {
			return agent.NewReplier(agent.Config{
				Persona: agent.Persona{
					AgentID:  spec.agentID,
					Markdown: spec.persona,
					Voice:    spec.voice,
				},
				Engine:       engineFor(spec),
				Synthesizer:  synth,
				HistoryTurns: historyTurns,
				Memory:       memory,
				Facts:        facts,
				OnError: func(err error) {
					log.Warn("agent reply failed", "npc", spec.name, "err", err)
				},
			})
		},
	}
}
