package wirenpc

import (
	"log/slog"

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

// matcherAgent builds the address.Agent for one NPC: its routing target plus
// aliases. A lone Character NPC is not AddressOnly, so it catches unaddressed
// speech via the single-NPC fallback (CONTEXT.md "Address-Only").
func matcherAgent(spec npcSpec) address.Agent {
	return address.Agent{
		Target: voiceevent.AddressTarget{
			AgentID:   spec.agentID,
			AgentRole: "character",
			Name:      spec.name,
		},
		Aliases: spec.aliases,
	}
}

// AddNPC brings one Character NPC into the scene: it registers the NPC's routing
// Agent in the Matcher (so utterances naming it — or, for a lone NPC, any
// unaddressed speech — route to it) and the NPC's Replier in the Cast (so the
// route is answered in its Voice). The first AddNPC builds the Matcher; later
// ones extend the live roster via [address.Matcher.Add]. Both halves move
// together so an NPC is never routable-but-silent or speaking-but-unroutable.
func (r *Roster) AddNPC(spec npcSpec) {
	if r.matcher == nil {
		// First NPC: build the Matcher around it. Single-target by default
		// (Config.MaxTargets unset ⇒ 1): naming two NPCs fires one turn on the
		// top-scored, the safe one-floor default (ADR-0025 deferred).
		r.matcher = address.NewMatcher(address.Config{Language: r.language}, matcherAgent(spec))
	} else {
		r.matcher.Add(matcherAgent(spec))
	}
	r.cast.Add(r.replierFor(spec))
}

// RemoveNPC drops the NPC with agentID from the scene: it leaves the Matcher (so
// nothing routes to it, and its last-addressed/interruption state is pruned so a
// later unnamed continuation cannot resurrect it) and the Cast (so even a stray
// route says nothing). Removing an unknown agentID is a no-op. Removing every
// NPC leaves the Matcher routing to nobody — the voice loop simply stays quiet.
func (r *Roster) RemoveNPC(agentID string) {
	if r.matcher != nil {
		r.matcher.Remove(agentID)
	}
	r.cast.Remove(agentID)
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
