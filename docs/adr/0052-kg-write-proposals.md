# Agent KG writes land as GM-reviewed Knowledge Proposals

`remember_knowledge` is the first side-effecting Tool. ADR-0030 specs *when* side effects may run (intents recorded during generation, flushed at turn-commit, dropped on barge) but deliberately not *what* writes are allowed — and its turn-commit machinery was never built (`pkg/tool/loop.go` hard-refuses side-effecting Tools). Decided with the operator 2026-07-07 (#298). This changes the KG's trust model, hence an ADR.

## What this decides

- **Mechanism: a GM proposal queue, not turn-commit.** An Agent's `remember_knowledge` call creates a **Knowledge Proposal** row (campaign, authoring agent, proposed write, status `pending`); **nothing touches the KG until the GM approves it** in the web UI. Approve → the write lands (and embeds via the normal path); reject → dropped. The KG's canon remains GM-authored: Agents can only ever *suggest*.
- **ADR-0030 is narrowed, not overturned**: turn-commit remains the specified mechanism for future Tools whose effects must be atomic with the spoken utterance (an executed trade, a state change the reply asserts). `remember_knowledge` doesn't need that atomicity — a proposal is not an effect the utterance promised. The `loop.go` hard-refusal stays for any side-effecting Tool that is not proposal-mediated.
- **Barge semantics** (the gate question): a barged reply still yields the proposal. The NPC *heard* the fact whether or not its answer finished; the GM review is the safety net for anything malformed. Proposals are created at Tool-execution time, not at turn-commit.
- **Write policy per Agent Role** (enforced via ADR-0029 `SupportsScope` grant narrowing, in the Tool handler, never by the LLM):
  - **Character NPC**: may propose facts on **its own linked Node** (the ADR-0008 NPC-Node↔Agent link) and Edges **from** that Node. No new Nodes, no writes elsewhere.
  - **Butler**: may propose campaign-wide — new Nodes, Edges, facts.
- **Duplicates and contradictions: no auto-merge.** The review UI surfaces embedding-similar existing facts beside each proposal (the ADR-0011 vector path); the GM merges, rewrites, or rejects. Writes are never silent — every KG mutation by an Agent passes human review in v1.

## Considered and rejected

- **Building ADR-0030's turn-commit machinery now** — a bigger build whose outcome is still *silent autonomous writes* to campaign canon; the trust-model question doesn't go away by executing writes at the right moment.
- **Turn-commit and proposals combined** — maximal machinery for v1; the proposal queue alone already prevents both hazards (bad writes and barge-orphaned effects that matter).
- **Auto-dedup/merge of near-duplicate facts** — similarity is a hint, not a semantic judgment; wrong merges would corrupt canon invisibly.
- **Campaign-wide write scope for NPCs** — floods the queue and invites cross-character contamination; an innkeeper has no business proposing facts about the distant war.

## Relationship to other ADRs

ADR-0030 (narrowed; amendment note added there), ADR-0029 (SupportsScope narrowing is the enforcement vehicle), ADR-0028 (Tool registry the handler lives in), ADR-0008 (NPC-Node↔Agent link defining "its own Node"), ADR-0011 (similarity surfacing in review).
