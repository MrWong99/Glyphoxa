# Address Detection: deterministic fuzzy chain on raw STT

Address Detection is a deterministic staged chain behind an `AddressDetector` interface. An LLM "who is being addressed?" judge or a two-stage (heuristic + LLM fallback) variant is deferred to v1.5+ and can slot in behind the same seam without touching the orchestrator.

It runs on the **raw STT final transcript** — no LLM transcript-correction step sits in front of it. This removes v1's failure mode where a correction pass rewrote NPC names ("Grimjaw" → "Grindstone") before the matcher saw them.

The chain, per utterance:

1. **Explicit name/alias match** — fuzzy (see below) against active Agents' names and aliases.
2. **Last-speaker continuation** — if no name matched and the previous addressee is still active and not Address-Only.
3. **Single active NPC fallback** — if exactly one non-Address-Only NPC is active.
4. **No target** — the utterance routes to no Agent (it is still transcribed).

`Detect` returns the matched *set* of Agents (usually one). When more than one is named, the turn-taking layer would run an Ensemble Turn (ADR-0025) — but **v1.0 ships single-target by default** (see below). Puppet/"speak-as" is **not** a chain stage: the GM voicing an NPC is the `/say <text> as:<agent>` slash command (ADR-0010), an explicit-target path that bypasses Address Detection, so v1's voice-level puppet-override stage is not carried over.

**`MaxTargets` — single-target is the default.** The matcher caps how many Agents one utterance may address via `Config.MaxTargets`, applied to the score-sorted hits **before** the published decision set is built (so only the Agents actually addressed are recorded as the next turn's continuation context). The zero value defaults to **1**: naming two NPCs in one breath fires **one** turn on the top-scored Agent, not two. This is the safe default because all NPCs share one Barge-in floor (ADR-0027, ADR-0038) — a single addressed turn keeps the floor's one-turn-at-a-time invariant intact. The full Ensemble Turn set is **opt-in**: a positive `MaxTargets > 1` caps at N, and a negative value (`-1`) lifts the cap entirely, restoring the unbounded score-sorted set ADR-0025 assumes. The Ensemble Turn remains the design-of-record and is reachable through this knob; only its speculative-lead / cross-talk turn-taking layer is deferred (ADR-0025, ADR-0038).

**Butler addressability.** Agents carry an `AddressOnly` capability: reachable only via stage 1, excluded from last-speaker and single-NPC fallback. The Butler defaults `AddressOnly=true` so ambient roleplay never routes to it; Character NPCs default `false`. The Butler additionally responds to voice-address only from the GM, while Character NPCs are addressable by any participant.

**Fuzzy name matching.** STT mishears proper nouns, so matching is fuzzy yet deterministic. A per-language phonetic encoder is chosen from a registry — `de` → Kölner Phonetik (`gopkg.in/Regis24GmbH/go-phonetics.v3`), `en` → Double Metaphone (`github.com/antzucaro/matchr`); further languages register their own encoder. Matching joins sliding windows of 1..N adjacent tokens before encoding (so "grim jaw" ≈ "Grimjaw"), and a rune-length floor keeps short tokens exact-only to avoid article/filler collisions. Behind phonetics sits a universal **edit-distance net** (`matchr.DamerauLevenshtein`, bounded by a max-distance knob), tried on a phonetic miss; for a language with no registered phonetic encoder, edit-distance is the entire fuzzy layer. This requires a `language` field on Campaign.

**Pure function over a swappable index.** Each `TargetMatch` call is a pure function over the fuzzy name index — no vendor and no LLM in the loop — so the whole chain is unit-testable (ADR-0019, ADR-0021). The roster is **dynamic**, not fixed at construction: `Matcher.Add` and `Matcher.Remove` bring a Character NPC into or out of the scene mid-Voice Session. They rebuild the fuzzy index and the roster together under the matcher's mutex, and `TargetMatch` scores the index **under that same mutex**, so one scoring pass always sees a mutually consistent index/roster pair — never one rebuild's index against another's roster. (An earlier revision read the index lock-free via an `atomic.Pointer`; that let a `Remove` landing between the index read and the roster read shift survivor indices and misattribute name scores — #145.) Each scoring call remains pure against the index/roster snapshot it captured. (`Remove` also prunes the departed Agent's last-addressed and interruption state so a later unnamed continuation cannot resurrect it.)

**Why deterministic, not an LLM judge.** Address Detection is in the hot path before the Agent's own LLM call (Hot Context targets <50ms). An LLM judge would add 200–1000ms and drag cassette nondeterminism into the most-tested orchestrator component. Deterministic matching is sub-millisecond.

**Considered options:**

- **LLM judge on every utterance** — rejected for v1.0 (latency + nondeterminism); kept reachable behind the interface.
- **Two-stage (heuristic + LLM fallback)** — deferred; reintroduces nondeterminism on the least-predictable path.
- **Exact match only (v1-style)** — rejected; STT name mishearings fall through silently.
- **STT keyterm hints instead of fuzzy matching** — viable, deferred; fuzzy matching chosen so robustness does not depend on per-provider hint support.
