# Address Detection: deterministic fuzzy chain on raw STT

Address Detection is a deterministic staged chain behind an `AddressDetector` interface. An LLM "who is being addressed?" judge or a two-stage (heuristic + LLM fallback) variant is deferred to v1.5+ and can slot in behind the same seam without touching the orchestrator.

It runs on the **raw STT final transcript** — no LLM transcript-correction step sits in front of it. This removes v1's failure mode where a correction pass rewrote NPC names ("Grimjaw" → "Grindstone") before the matcher saw them.

The chain, per utterance:

1. **Explicit name/alias match** — fuzzy (see below) against active Agents' names and aliases.
2. **Last-speaker continuation** — if no name matched and the previous addressee is still active and not Address-Only.
3. **Single active NPC fallback** — if exactly one non-Address-Only NPC is active.
4. **No target** — the utterance routes to no Agent (it is still transcribed).

`Detect` returns the matched *set* of Agents (usually one). When more than one is named, the turn-taking layer runs an Ensemble Turn (ADR-0025). Puppet/"speak-as" is **not** a chain stage: the GM voicing an NPC is the `/say <text> as:<agent>` slash command (ADR-0010), an explicit-target path that bypasses Address Detection, so v1's voice-level puppet-override stage is not carried over.

**Butler addressability.** Agents carry an `AddressOnly` capability: reachable only via stage 1, excluded from last-speaker and single-NPC fallback. The Butler defaults `AddressOnly=true` so ambient roleplay never routes to it; Character NPCs default `false`. The Butler additionally responds to voice-address only from the GM, while Character NPCs are addressable by any participant.

**Fuzzy name matching.** STT mishears proper nouns, so matching is fuzzy yet deterministic. A per-language phonetic encoder is chosen from a registry — `de` → Kölner Phonetik (`gopkg.in/Regis24GmbH/go-phonetics.v3`), `en` → Double Metaphone (`github.com/antzucaro/matchr`); further languages register their own encoder. Matching joins sliding windows of 1..N adjacent tokens before encoding (so "grim jaw" ≈ "Grimjaw"), and a rune-length floor keeps short tokens exact-only to avoid article/filler collisions. Behind phonetics sits a universal **edit-distance net** (`matchr.DamerauLevenshtein`, bounded by a max-distance knob), tried on a phonetic miss; for a language with no registered phonetic encoder, edit-distance is the entire fuzzy layer. This requires a `language` field on Campaign.

The matcher is a pure function over an index built at `Rebuild`, so the whole chain is unit-testable with no vendor or LLM in the loop (ADR-0019, ADR-0021).

**Why deterministic, not an LLM judge.** Address Detection is in the hot path before the Agent's own LLM call (Hot Context targets <50ms). An LLM judge would add 200–1000ms and drag cassette nondeterminism into the most-tested orchestrator component. Deterministic matching is sub-millisecond.

**Considered options:**

- **LLM judge on every utterance** — rejected for v1.0 (latency + nondeterminism); kept reachable behind the interface.
- **Two-stage (heuristic + LLM fallback)** — deferred; reintroduces nondeterminism on the least-predictable path.
- **Exact match only (v1-style)** — rejected; STT name mishearings fall through silently.
- **STT keyterm hints instead of fuzzy matching** — viable, deferred; fuzzy matching chosen so robustness does not depend on per-provider hint support.
