# Tool Grants: least-privilege, per-grant scoping config enforced in the handler

A **Tool Grant** is an explicit permission for an Agent to invoke a named Tool, modeled as `{tool_name, config?}` — a struct, not a bare name string, so the per-grant config door is open from day one even though `dice`'s config is `nil`.

## Least-privilege enforcement (grant-stripping)

The LLM only ever sees Tools the Agent is granted. Ungranted Tools are filtered out *before* the prompt is built and are never declared to the model. This is standard least-privilege tool-use (distinct from the v1 tier-stripping we cut in ADR-0028), and it is nearly free, so it ships from day one.

## Per-grant config narrows authority

The config may **narrow a Tool's authority** for one Agent, not just tune it. The same Tool granted to two Agents can carry different scope: an NPC granted `remember_knowledge` scoped to its own facts vs the Butler granted it campaign-wide. Two consequences:

- **Interface:** the handler receives the grant config at execution time — `Execute(ctx, args, grantConfig)`. The same registered Tool behaves differently per caller purely via the grant.
- **Security (mirrors ADR-0010's server-side rule):** the scope is enforced **in the handler, never by the LLM**. The model is told "you can remember knowledge"; the "only about yourself" constraint is applied by the handler reading `grantConfig`. The LLM cannot widen its scope by crafting clever args — the tool-layer twin of "Discord permissions are a UX hint; the server check is the only safe place."

## Persistence is deferred

`internal/` is empty and there is no `agents` table yet (the orchestrator work is in-memory/cassette-driven, and migration tooling is Q15). So Tool Grants are an **in-memory value now** — a set of `{tool_name, config}` on the Agent config the orchestrator already holds. When the `agents` table lands (its own future slice, gated on Q15), grants hydrate from the DB into the *identical* in-memory shape. The orchestrator never knows the difference. No grant table in the dice PoC.

**Why:** modeling the grant as a struct with optional scoping config now (vs a `[]string`) avoids a later migration of a live interface; enforcing scope in the handler is the only safe place; deferring persistence keeps the DB layer from being forced into existence early.
