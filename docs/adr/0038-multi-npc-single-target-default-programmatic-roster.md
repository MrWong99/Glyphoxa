# Multi-NPC: single-target default, one shared floor, programmatic roster

A Voice Session may host several Character NPCs at once (ADR-0009's single Agent table already makes them homogeneous). v1.0 wires that multi-NPC scene around three decisions: **single-target by default** (naming two NPCs fires one turn), **one shared Barge-in floor** with reply multiplexing through per-Agent self-filtering Repliers, and a **programmatic-only roster** control surface. The human-feeling Ensemble Turn (ADR-0025) is the design-of-record but its turn-taking layer is deferred, not built.

## What this decides

- **Single-target is the safe default.** Address Detection (ADR-0024) returns a score-sorted set, but `Config.MaxTargets` defaults to 1, so the published decision set holds at most the top-scored Agent. Naming two NPCs in one breath fires **one** turn on the winner. The multi-target Ensemble set is opt-in via `MaxTargets > 1` (cap at N) or `-1` (unbounded). The default is single-target because there is exactly **one Barge-in floor** for the whole scene (next point), and one addressed turn is the only thing that keeps that floor's one-turn-at-a-time invariant intact.

- **One floor, reply multiplexing via `Cast`, not N bound strategies.** The reply reactor takes the Barge-in floor (ADR-0027) on every `AddressRouted`. Binding N independent reply strategies to one shared floor would corrupt floor state — each would take and release the floor for the same turn. So a multi-NPC conversation wires **one** reply strategy, an `agent.Cast`, that holds a map of `AgentID → *Replier` and delegates each route to the single Replier whose `Persona.AgentID` matches the route's `AddressTarget.AgentID`. Each Agent thereby **self-filters**: it answers only routes addressed to it, says nothing for the rest. N independent Agents — each with its own Persona, Voice, and LLM config — speak on one bus over one floor. A route naming no current member dispatches nothing (yields nil), the right answer when the matcher selected an Agent the Cast does not hold or one removed mid-conversation.

- **The roster is the programmatic control surface; Discord/HTTP triggers are deferred.** `internal/wirenpc.Roster` is the composition root that ties one `address.Matcher` to one `agent.Cast`. `Roster.AddNPC` / `Roster.RemoveNPC` move an NPC into or out of **both** halves in lockstep — the matcher (so it is/isn't routed, via `Matcher.Add`/`Remove`'s atomic index swap) and the Cast (so it does/doesn't speak) — so an NPC is never routable-but-silent or speaking-but-unroutable. v1.0 exposes this only as a **programmatic API**: there is no Discord command or HTTP endpoint to add or drop an NPC mid-scene. Those triggers are deferred; the in-process seam is the whole control surface for now.

## Why

The three decisions are one decision seen from three sides: **there is one shared floor, so the safe unit of work is one addressed turn.**

Single-target falls straight out of the floor. An Ensemble Turn (ADR-0025) needs speculative fan-out, a Lead race, and a queued Reaction — a whole turn-taking layer that coordinates several generations against the *one* floor without the reply reactors fighting over it. That layer is real work and orthogonal to "can a scene hold several NPCs at all." Capping at one target lets the multi-NPC scene ship now: every routed turn is a normal single-Agent turn the existing floor and barge-in machinery already handle unchanged. The Ensemble design is preserved verbatim (ADR-0025, marked Deferred) and is reachable the moment its turn-taking layer is built — flip `MaxTargets` and add the Lead/Reaction coordinator behind the same `AddressRouted` set the matcher already returns.

Multiplexing through one `Cast` rather than binding N repliers is what makes single-target *correct* rather than merely cheap. Self-filtering per Agent keeps the floor's invariant a structural property — only one Replier ever runs per route — instead of a thing each reply strategy has to cooperatively respect.

Programmatic-only roster control matches where the project is: the wiring and the algorithms are under test first (ADR-0019), and a Discord/HTTP trigger would commit to a UX (who may add an NPC mid-scene, how it is authorized) ahead of the demand for it. The lockstep `AddNPC`/`RemoveNPC` seam is the load-bearing part; a trigger is a thin caller added when the product wants one.

## Considered options

- **N reply strategies, one per NPC, all bound to the floor** — rejected: several independent reply reactors on one shared Barge-in floor each take and release the floor for the same turn, corrupting floor state. The `Cast` multiplexer exists precisely to route one addressed turn to one Replier.
- **Ship the Ensemble Turn now (multi-target by default)** — rejected for v1.0: it requires the speculative-lead / cross-talk turn-taking layer (ADR-0025) coordinating multiple generations against one floor. Deferred behind `MaxTargets`, not dropped.
- **Per-NPC Barge-in floor** — rejected: a Barge-in is a human reclaiming the *room's* floor (ADR-0027); humans always have the upper hand over all NPCs at once. Per-NPC floors would let one NPC keep talking through a human interruption aimed at another, which is the floor-stealing barge-in exists to prevent.
- **Discord/HTTP trigger for roster membership in v1.0** — deferred: the programmatic `AddNPC`/`RemoveNPC` seam is the load-bearing mechanism; the trigger is a thin caller added when the UX and authorization model are decided.

## Relationship to other ADRs

- **ADR-0024 (Address Detection)** — supplies `MaxTargets` (single-target default) and the dynamic `Add`/`Remove` roster with the atomic index swap that `Roster.AddNPC`/`RemoveNPC` drives. This ADR is why the default is 1.
- **ADR-0025 (Ensemble Turns)** — the deferred turn-taking layer this ADR chooses not to build for v1.0. Design-of-record, reachable via `MaxTargets > 1`. **Deferred, not superseded.**
- **ADR-0027 (Barge-in)** — establishes the single shared floor whose one-turn-at-a-time invariant makes single-target the safe default; an Ensemble Turn is already defined there as one floor-holding unit, so the floor is ready for the deferred layer when it lands.
- **ADR-0009 (single Agent table / auto-Butler)** — the polymorphic `agents` table makes Butler and Character NPCs homogeneous, so one Matcher and one Cast host a mixed roster on one code path.
