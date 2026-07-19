*Assessment behind ADR-0057; platform claims verified 2026-07-19.*

# Architecture Assessment: Parallel Multi-Tenant Voice Sessions on Kubernetes

**Scope.** Enable multiple concurrent voice sessions across tenants in the K8s SaaS deployment, supporting both (a) BYOK per-tenant Discord bot tokens and (b) one central operator-managed token. Self-hosted `-mode all` keeps its current single-session shape. The working hypothesis "spawn dedicated workers per tenant" is evaluated against Discord platform realities (DAVE, gateway sessions, slash commands) and the actual codebase.

---

## 1. Constraint map

### 1.1 Hard platform constraints (verified against primary sources; verification verdicts noted)

**P1 — DAVE is mandatory and per-connection. [CONFIRMED]**
Since March 1–2, 2026, the voice gateway rejects any non-DAVE client on non-Stage voice with close code 4017 (official changelog "Deprecating Non-E2EE Voice Calls"; enforcement PR discord/discord-api-docs#8167; real-world bot rejections in DiscordPHP #1461 and Pycord #3135). The downgrade-to-transport-only path is documented but dead in practice. MLS state is strictly per voice connection: one MLS group/epoch/per-sender-ratchet set per call; N simultaneous guild connections = N independent MLS groups (dave-protocol/protocol.md; disgo creates one `godave.Session` per `voice.Conn`).
*Verification caveat:* "concurrent multi-connection DAVE from one process is demonstrated working" was **refuted as overstated** — the sole evidence (godave PR #5's author's relay test) doesn't state single-process topology and used an unmerged patch. Treat single-process multi-connection DAVE as *structurally sound but field-unverified*; validate directly before GA.

**P2 — MLS state cannot be handed off across process restart. [CONFIRMED, overdetermined]**
No serialization/resume of MLS group state exists in the protocol, libdave, godave, or dave-go. A severed voice websocket triggers a member-removal proposal, so a restarted worker rejoins as a *new* MLS member (op 31 → reset → new key package → re-add, epoch transition for everyone in the call). A *short same-process* voice reconnect can catch up via v8 buffered resume (op 7 + seq_ack) if in-memory MLS state is retained. Consequence: **mid-session pod migration is impossible; a worker restart = session drop + rejoin.** This matches ADR-0006's existing stance (`docs/adr/0006-dave-mls-no-mid-session-migration.md:3`).

**P3 — No cross-process identity-keypair coordination is needed. [Original claim REFUTED — good news]**
The whitepaper's "same keypair for all active voice gateway connections" rule only affects verification UX for *previously verified* members — which bots never are. Discord's own bot docs explicitly bless "a new ephemeral keypair for every protocol call." **Per-tenant workers do not need to share or coordinate DAVE identity keys.** This removes a potential blocker for the per-worker hypothesis.

**P4 — DAVE lives entirely on the voice WebSocket + UDP path. [CONFIRMED]**
All DAVE opcodes (21–31) and version negotiation happen on the voice gateway; the main gateway's role is unchanged (op-4 out, VOICE_STATE_UPDATE/VOICE_SERVER_UPDATE in, no DAVE fields). DAVE therefore does **not** constrain which process owns the main gateway vs. the voice websocket — the Lavalink-style split remains DAVE-compatible.

**P5 — Gateway sessions per token: multiple allowed, but budgeted.**
Official docs permit multiple concurrent sessions on one token, even with identical shard coordinates (`[0,1]`), naming zero-downtime handoff as a use case. Both sessions receive the full event stream — Discord deduplicates nothing ("zombie shard" duplicate-processing is real; medium confidence). The budget: **1000 IDENTIFYs per 24h per token**, and exhaustion **terminates all sessions and resets the token** with an owner email — an outage-class event. RESUME is free. `max_concurrency` (default 1) serializes IDENTIFYs at 1 per 5s per token. Sharding is mandatory at 2500+ guilds; shard routing is `(guild_id >> 22) % num_shards`.

**P6 — Voice-per-guild and voice↔gateway-session coupling.**
One voice state (one channel) per guild per bot user; unlimited guilds concurrently per app (no documented cap; practical bound is the 120 cmds/60s per-connection limit on op-4). The voice connection's `session_id` **is** the main-gateway session that sent op-4: when that gateway session is invalidated, Discord tears down the voice websocket with **4014** ("main gateway session was dropped… should not reconnect"). A successful gateway RESUME preserves voice (community consensus, medium confidence — not an explicit official guarantee). The Lavalink credential-forwarding handoff ({session_id, token, endpoint} → separate media process opens voice WS/UDP itself) is de-facto stable, 8+ years of ecosystem use, structurally supported by docs — but the forwarding must be a *persistent channel*, not one-shot (mid-call voice-server moves deliver fresh VOICE_SERVER_UPDATEs; leave is an op-4 back through the main gateway).

**P7 — Slash commands are per-application, not per-process.**
Guild-scoped registration (`PUT /applications/{app}/guilds/{guild}/commands`) is an idempotent bulk overwrite; re-PUTting an identical set does **not** burn the 200-creates/day/guild quota (only genuinely new commands count). Per-guild permission *overwrites* require a user Bearer token (`applications.commands.permissions.update`) — bot tokens are rejected — so per-guild lockdown is done by guild admins, matching Glyphoxa's ADR-0010 "server-side gate, Discord permissions are a UX hint" stance. Interactions delivery is **mutually exclusive per application**: gateway INTERACTION_CREATE (default) *or* an Interactions Endpoint URL (Ed25519-verified webhook, no gateway client required). Voice always requires a gateway session regardless.

**P8 — BYOK friction is low.**
A Glyphoxa-shaped bot needs **no privileged intents** (`GUILD_VOICE_STATES` is standard; slash commands don't need MESSAGE_CONTENT). Unverified apps are capped at 100 servers — irrelevant for single-guild tenant bots. Each BYOK token is its own independent rate-limit pool (own 1000/day IDENTIFY budget, own shard math). Tenant tokens can die out-of-band (GitHub secret-scanning auto-revocation); detect via REST 401 / error 50014 / gateway close 4004 and surface a re-paste flow. The BYOK "customer pastes bot token, platform hosts it" model is commercially proven (Tickets whitelabel, InHouseQueue, BotGhost).

**P9 — Network shape.**
Voice needs egress-only UDP (bot initiates outbound; stateful NAT return-path suffices) — worker pods need no Service/LoadBalancer. Scale limits are conntrack/SNAT-port ones. For live-session protection, the Agones playbook applies: SIGTERM drain, `terminationGracePeriodSeconds` ≈ session length, PDBs (with the ~1h managed-platform eviction caveat).

**P10 — Library status flags (both original claims REFUTED in detail; corrections load-bearing for Glyphoxa):**
- **disgo v0.19.6 — the exact version Glyphoxa pins (`go.mod:7`) — leaks DAVE sessions on conn discard.** Issue #566's fix (PR #568, `dave.Close()` in `connImpl.Close`) merged 2026-07-16 but is in **no tagged release** as of 2026-07-19. Glyphoxa creates a *fresh conn per reconnect cycle* (`internal/wirenpc/connect.go:145-165`), so every cycle leaks a DAVE session under v0.19.6. Action: pin a pseudo-version past PR #568 or close the session explicitly. (Also: disgo issue #555's "encrypt errors before epoch ready" was fixed via the godave `Ready()` interface contract, not in disgo — custom `Session` implementors must honor passthrough semantics.)
- **godave PR #5 (key-ratchet race, ~1s encrypt failure on join/leave)** is a *disputed, maintainer-unreproduced* report against the CGO golibdave backend, not an acknowledged defect — and Glyphoxa uses **thomas-vilte/dave-go** (pure Go, confirmed CGO-free, godave.Session-compatible, audio-only, interop-verified by its author, **young/API-unstable/unaudited**), so this race doesn't directly apply, but it illustrates the ecosystem's youth.

### 1.2 Hard codebase constraints (file:line)

**C1 — One gateway session per process, one token globally.** Two client creation sites: per-cycle `disgo.New` in standalone voice mode (`internal/wirenpc/connect.go:45,98-106`, seam `var newDiscordClient = disgo.New`) and the one boot-owned standing client in `internal/presence/presence.go:59-89` that voice cycles *borrow* via `ClientProvider` (`connect.go:52-59`). Token resolution is DB-ciphertext-first with env fallback (`internal/wirenpc/credentials.go:59-71`), but the presence reads **`GetLatestDeploymentConfig`** — the globally newest row, tenant-unscoped (`internal/storage/deployment.go:48-69`) — which is the **presence-hijack blocker**: tenant B saving Discord settings tears down tenant A's client (`docs/devs/self-signup-and-invitations-design.md:122-127`). The per-tenant token column already exists dormant (`internal/storage/migrations/00005_configuration.sql:27-28`).

**C2 — Single-active-session guard + session-blind event plumbing.** `session.Manager.active` is one pointer (the `activeSession` record at `internal/session/manager.go:167-173,213`, the `active *activeSession` field at `:230`); second Start → `ErrSessionActive` (`:394-396`). The process-wide `voiceevent.Bus` carries no SessionID; relay/chunker/recall/kgfacts/highlight resolve "which session" from one `View.Snapshot()`, and `session.View` **panics on a second Manager bind** (`internal/session/view.go:46`). Meanwhile `pkg/voice.Manager` is *already multi-guild-safe on one client* (`pkg/voice/manager.go:13-31,189-228`).

**C3 — DAVE is wired at client construction only.** `pkg/voice/dave.go` (build tag `dave`) installs dave-go via `voice.WithDaveSessionCreateFunc` at `disgo.New` time; it cannot be enabled later (`internal/wirenpc/connect.go:75`). The `!dave` stub is tests-only (4017 in production). Every reconnect cycle re-runs the MLS handshake — already the norm.

**C4 — The credential-forwarding seam exists in disgo but is unbuilt in Glyphoxa.** `voice.NewManager` accepts an arbitrary `StateUpdateFunc`, and `Conn.HandleVoiceStateUpdate`/`HandleVoiceServerUpdate` are exported (disgo v0.19.6 `voice/manager.go:44`, `voice/conn.go:54,194`) — a worker *could* receive forwarded credentials. But Glyphoxa's `pkg/voice.NewManager` hardwires `client.VoiceManager` (`pkg/voice/manager.go:141`), **ADR-0039's claim that the voice.v1 VoiceControlService proto "is authored now" is contradicted by the repo** (proto/ contains only management.proto — flagged, not smoothed over), and ADR-0005's claim table (guild_id PK, voice_instance_id, heartbeat) was never built (`internal/storage/migrations/00006_voice_sessions.sql:9-21` is lifecycle rows only). No LISTEN/NOTIFY anywhere.

**C5 — Slash commands: per-guild, gateway-delivered, tenant-blind.** Registration is `Rest.SetGuildCommands` per configured guild (`internal/presence/presence.go:311-317`); interactions arrive via gateway listeners (`presence.go:298-305`); no webhook/ed25519 handling exists. GM identity is deployment-scoped ("any tenant's operator is GM everywhere", `internal/auth/gmidentity.go:25-44`), and campaign resolution in handlers is tenant-free (`internal/storage/auth.go:135`); tenant-scoped variants exist but only the web tier uses them.

**C6 — Deploy posture.** Helm renders single-replica web + voice Deployments, Recreate strategy, "scaling out is a design change, not a value" (`deploy/charts/glyphoxa/values.yaml:170,300`). **The voice pod deliberately lacks GLYPHOXA_SECRET** (`templates/voice-deployment.yaml:17`) — a BYOK-decrypting voice worker changes this blast-radius posture, or must receive decrypted credentials from the web role.

**C7 — What's already N-safe.** The DB job runner (FOR UPDATE SKIP LOCKED, `internal/storage/job.go:115`, ADR-0049) is replica-safe by construction; the entire per-session audio pipeline has no process-global state except the bus/View; per-session goroutine/memory footprint is modest (≈6 disgo goroutines + pipeline workers + ≤4 STT lanes per session).

---

## 2. Architecture options

### Option A — Full per-tenant worker (gateway + voice + commands per tenant pod)

Each tenant gets a dedicated pod running essentially today's `-mode voice` grown up: own disgo client on the tenant's token, own presence/command registration for the tenant's guild(s), own voice sessions.

- **BYOK fit: excellent.** One token = one app = one gateway session = one pod. Independent IDENTIFY budgets, independent blast radius (a revoked tenant token 4004s one pod), per-guild command registration is already how `internal/presence` works — it just needs tenant-scoped config reads instead of `GetLatestDeploymentConfig`.
- **Central-token fit: poor at scale.** N pods each holding a session on the *same* token: allowed (P5), but (1) every pod receives the full event stream, so **INTERACTION_CREATE is delivered to all N pods → duplicate slash-command handling** unless interactions are centralized (endpoint URL or a designated handler pod); (2) the shared 1000/day IDENTIFY budget means a crash-looping fleet can **reset the operator token** — a total outage; (3) `max_concurrency=1` serializes fleet cold-start at 1 identify/5s; (4) past 2500 guilds, naive `[0,1]` identifies break entirely.
- **DAVE:** clean — one process per tenant, few conns each, per-conn MLS (P1), no keypair coordination needed (P3).
- **K8s shape:** needs a controller/operator (or KubeSpawner-style spawner) creating a Deployment + Secret per tenant; per-tenant secrets favor External Secrets Operator's per-namespace SecretStore model or forwarding from the control plane.
- **Failure/restart:** session drops, rejoin as new MLS member (P2); per-tenant Recreate is fine.
- **Cost floor:** nonzero per idle tenant (a Go process + gateway websocket per tenant, plausibly tens of MB; the only public datapoint — gateway-proxy's ~52MB/shard — is cache-dominated and overstates an uncached client). Silo model economics (AWS SaaS Lens): great blast-radius, poor utilization.
- **Code changes:** `internal/presence` tenant-scoping (`GetDeploymentConfig(tenantID)`), `internal/wirenpc` token plumb-through, per-tenant GM scoping in `internal/auth/gmidentity.go`, chart: per-tenant Deployment templating + secret delivery (GLYPHOXA_SECRET posture change, C6), a tenant→worker assignment record.

### Option B — Central gateway service + voice/media workers via credential forwarding (Lavalink pattern)

One (eventually sharded) gateway service holds the Discord session(s), handles interactions, and forwards {session_id, token, endpoint} plus subsequent voice events over a persistent backplane to media workers, which open the voice WS + UDP + DAVE themselves.

- **Central-token fit: the canonical answer** — exactly one gateway session per token/shard, no duplicate events, IDENTIFY budget spent once, sharding handled in one place, interactions handled once.
- **BYOK fit: awkward.** The gateway service must now hold N tenant clients anyway (each tenant app needs its own gateway session for its own guild) — so the "one fragile connection, centrally managed" benefit evaporates; you've built a multiplexer for connections that were never scarce.
- **DAVE fit: compatible (P4) but hardest here.** The worker owns the voice WS, hence the MLS session — fine. But the coupling in P6 bites: any gateway-service session invalidation 4014s *every* worker's voice conn at once (Lavalink's own docs: "when your shard's main WS dies, so do all your Lavalink audio connections") — the central gateway becomes a correlated-failure single point for all live sessions. The forwarding channel must be persistent and bidirectional (voice-server moves, op-4 leave).
- **Codebase distance: largest.** Requires the voice.v1 backplane ADR-0039 defers and the repo does not contain (C4), an RPC-backed `StateUpdateFunc`, substitution of the hardwired `client.VoiceManager` seam (`pkg/voice/manager.go:141`), plus an untested disgo usage mode (open question in the voice-stack findings: feeding `HandleVoiceStateUpdate/HandleVoiceServerUpdate` from outside a `bot.Client` is API-compatible but unexercised).
- **K8s shape:** gateway StatefulSet (ordinal=shard) + stateless-ish worker Deployment + claim/routing plane. Three moving parts before the first session plays audio.

### Option C — Shared multi-tenant worker pool with per-tenant routing (pool of full-client pods)

A homogeneous fleet of voice-worker pods. Each pod runs K concurrent sessions (design-doc Shape B internals) and holds **full disgo clients keyed by bot token**: for the central token, one client serving many guilds (P6, `pkg/voice.Manager` is already multi-guild, C2); for BYOK tenants, one client per tenant token *on whichever pod claims that tenant's session*. Session assignment via a revived ADR-0005 claim table (Postgres, SKIP LOCKED + heartbeat — the same coordination idiom the job runner already uses, C7).

- **BYOK fit: good.** A tenant client exists only while that tenant has a live session (or presence need); it lives wholly inside one pod; token revocation is already classified (`gatewayfatal.go` 4004 → `invalid_bot_token`) and can be surfaced per-tenant.
- **Central-token fit: good with one carve-out.** Multiple pods each hold a session on the central token — permitted (P5); guild-scoped voice events from other pods' joins are ignored by a pod with no conn for that guild. Two real costs: duplicate INTERACTION_CREATE (must centralize interaction handling — designate exactly one presence-owner via the claim table, or move to the Interactions Endpoint URL handled by the web tier) and the shared IDENTIFY budget (mitigate: resume-first reconnect policy, backoff on identify, budget alarm; disgo already resumes by default).
- **DAVE:** K MLS sessions per process — structurally sound, field-unverified (P1 caveat) → needs a load test.
- **Cost floor:** near-zero marginal per idle tenant (pool model); noisy-neighbor bounded by K and pod resources.
- **Blast radius:** a pod crash drops its K sessions (across tenants) — bounded by K; a poisoned tenant config fails that tenant's cycle, not the pod.

### Option D (hybrid A+B) — per-tenant workers for BYOK, gateway-split for central

Superficially attractive symmetry, but it means building **both** the tenant-operator machinery of A **and** the backplane of B, and maintaining two divergent voice paths through `wirenpc`. Only worth it if the central bot approaches sharding scale (~2500 guilds) while BYOK stays niche — not Glyphoxa's near-term reality.

---

## 3. Recommendation

**Option C — a shared worker pool of full-client pods, reached via Shape B in-process first — with interaction handling centralized, and per-tenant *clients* (not per-tenant *pods*) as the BYOK unit.** The user's per-tenant-worker hypothesis is half right: **per-tenant Discord *client* is the correct isolation unit; per-tenant *pod* is not.**

Reasoning:

1. **The platform makes gateway sessions cheap and voice connections portable-per-guild, so the scarce resource the Lavalink split (B) conserves doesn't exist here.** Under 2500 guilds one session per token suffices (P5); a pod can hold one. B's backplane, its unbuilt proto (C4, with ADR-0039's "authored now" claim being false), its untested outside-a-bot.Client disgo mode, and its correlated 4014 failure domain are all cost with no matching benefit at this scale. B is the right *eventual* shape only if the central bot nears sharding territory — and the claim-table + `wirenpc` seams chosen here don't foreclose it.
2. **Pure A fails the central-token mode**, which is a launch requirement: duplicate interaction delivery and a shared, resettable IDENTIFY budget across a per-tenant pod fleet is an operator-token outage waiting for a crash-loop. It also carries a per-idle-tenant cost floor and a tenant-controller that Glyphoxa's deploy story (single Helm chart, "scaling is a design change") doesn't yet have.
3. **C is the shortest path through the existing code.** The three enumerated blockers (single-active guard, session-blind bus, single-token presence — C1/C2) must be fixed for *any* option; fixing them yields Shape B in-process, which *is* Option C at fleet-size 1. This matches the design doc's own recommendation (`docs/devs/self-signup-and-invitations-design.md:127`: Shape B first) and reconciles D6: **Shape B is phase 1 of Shape A-lite**, with Postgres claim rows (idiom already proven in `internal/storage/job.go`) instead of a new RPC backplane.
4. **DAVE is neutral-to-favorable to C**: per-connection MLS (P1), no keypair coordination (P3), no cross-process handoff to design for because none is possible anyway (P2) — ADR-0006's "session drops, user restarts" already accepts the only restart semantics K8s can offer.

Non-negotiable riders on the recommendation:

- **Fix the disgo v0.19.6 DAVE-session leak before running many cycles per pod** (P10): bump to a pseudo-version including PR #568 or explicitly close the dave session per cycle. At one session per process this leak was survivable; at K sessions × reconnect cycles it is not.
- **Load-test K concurrent DAVE sessions in one process** (P1 caveat: field-unverified) plus the N×20ms sender-loop scheduler-jitter ceiling the voice-stack findings flagged, before committing to a K value.
- **Centralize interactions before running >1 pod on the central token**: near-term, exactly one claim-table-designated "presence owner" pod registers gateway command listeners for the central app; medium-term, move the central app (and optionally BYOK apps) to the Interactions Endpoint URL served by the web tier (P7 — mutually exclusive with gateway delivery, so it structurally eliminates duplicates; workers keep gateway sessions purely for voice).

---

## 4. Migration path (each step shippable)

**Step 0 — Hygiene (no behavior change).** Bump disgo past PR #568 (DAVE leak); write the D6 ADR recording this document's decision and correcting ADR-0039's stale voice.v1 claim; add a budget alarm concept for IDENTIFY counting (log identify vs resume per token).

**Step 1 — Session-scoped events (Shape B prerequisite).** Add `SessionID` to `voiceevent.Bus` events; replace the panic-on-second-bind `session.View` (`internal/session/view.go:46`) with a keyed registry; teach relay/chunker/recall/kgfacts/highlight consumers to resolve session context from the event, not a global Snapshot. Shippable: behavior identical at one session; unlocks everything else.

**Step 2 — K sessions in one process.** Replace `session.Manager.active` with a map keyed by (tenantID, campaignID) plus a per-tenant one-session guard and a configurable process cap K (`internal/session/manager.go:230` pointer, `:394-396` guard). `pkg/voice.Manager` needs nothing (C2). Allowance/spend-cap snapshotting already carries the documented concurrency caveat (`manager.go:438-439`). Shippable in `-mode all`: the self-signup test env immediately gets concurrent sessions on the shared bot.

**Step 3 — Per-tenant client registry (kills the presence hijack).** Replace the singleton `internal/presence` with a registry keyed by resolved bot token: `Ensure(tenantID)` reads `GetDeploymentConfig(tenantID)` (the tenant-scoped query the storage comment already anticipates, `internal/storage/deployment.go:55`), builds/reuses one standing client per distinct token, registers commands for that tenant's guild via the existing idempotent `SetGuildCommands` path (no quota cost for identical re-PUTs, P7). `wirenpc.Config.Client` becomes a per-tenant provider; `Manager.Start`'s currently-dead per-tenant token resolution (`connect.go:52-59` preferring cfg.Client) comes alive. Central token = all tenants resolve to one client (many guilds on it); BYOK = tenant's own client. In the same slice: tenant-scope GM identity (`internal/auth/gmidentity.go`) and switch presence handlers to the existing `GetActiveCampaignForUserInTenant`/`ListCampaignsInTenant` (C5) — routing interaction→tenant by the interaction's guild_id against deployment_config. This closes both pre-open blockers (presence hijack, GM scoping). **After this step, both bot-token modes work correctly in one process** — arguably a shippable SaaS v1.
   *Helm note:* whichever pod runs this needs GLYPHOXA_SECRET to decrypt BYOK tokens — the `-mode all` web pod already has it, so shipping Steps 1–3 under `-mode all` (single pod, K sessions) defers the chart posture change (C6).

**Step 4 — Claim plane + worker fleet.** Revive ADR-0005's sketch as `voice_claims(tenant_id or guild_id PK, voice_instance_id, claimed_at, heartbeat_at)` using the SKIP-LOCKED idiom from `internal/storage/job.go:115`; poll-based first (no LISTEN/NOTIFY yet — none exists in the codebase, keep it that way until latency demands otherwise). `-mode voice` becomes a claim-loop worker (dropping `-guild/-channel` flags for DB-driven assignment); web-tier `SessionService.StartSession` writes an intent row instead of calling the in-process Manager; workers pick it up. Elect exactly one presence-owner for the central app's command listeners via a dedicated claim row. Chart: voice Deployment gets `replicas: N`, GLYPHOXA_SECRET (or Step 4b: web tier decrypts and stores per-claim short-lived credentials — the "credentials forwarded by the gateway role" pattern ADR-0005/0006 anticipated, preserving today's blast-radius posture), `terminationGracePeriodSeconds` sized to drain, SIGTERM → stop claiming, finish/end sessions.

**Step 5 (optional, later) — Interactions Endpoint URL + sharding readiness.** Move central-app interactions to the web tier's HTTPS endpoint (Ed25519 verification, 3s initial response, PING/probe handling — all currently absent from the repo, C5); BYOK apps can follow (route by `application_id`, verify against per-tenant public keys). This is also the on-ramp to Option B's gateway split if the central bot ever approaches 2500 guilds — the claim table and per-tenant client registry carry over unchanged.

Seams that help, explicitly: the `newDiscordClient = disgo.New` seam and `ClientProvider` borrow in `internal/wirenpc/connect.go`; the `voiceManager` interface seam in `pkg/voice/internal_iface.go`; the dormant per-tenant token column (`00005_configuration.sql:27-28`); connectrpc's `SessionService` (its Start/Stop RPC shape survives the move to claim-intent rows); the `-mode` flag (workers are a behavior change inside `-mode voice`, not a new binary); `gatewayfatal.go`'s existing terminal classification for per-tenant token death.

---

## 5. Open questions and risks

1. **K-sessions-per-process DAVE is field-unverified** (verification explicitly downgraded the "demonstrated working" claim). Mitigation: a soak test with K real Discord voice channels before choosing K; start K small (2–4).
2. **Gateway-RESUME-preserves-voice is community consensus, not an official guarantee** (P6, medium confidence). The reconnect design should tolerate 4014-on-resume as a normal path (full cycle rebuild — which `runWithReconnect` already does).
3. **Duplicate-event delivery semantics on same-coordinate sessions are empirically supported but not literally documented** (P5, medium). Before running >1 pod on the central token, verify in the k3d env that pod 2 cleanly ignores pod 1's guild-scoped voice events (expected: disgo drops events for guilds with no conn) and that interactions are handled exactly once under the presence-owner claim.
4. **IDENTIFY-budget exhaustion resets the operator token** — the worst failure mode this design introduces for central mode. Needs: resume-first policy audit, identify-rate circuit breaker, and an alert well below 1000/day. BYOK tenants are insulated (per-token pools).
5. **Presence-hijack interaction:** until Step 3 lands, *any* concurrency work is unsafe in multi-tenant deployments — Step 2 without Step 3 would run tenant A's session through whatever token tenant B saved last (`internal/storage/deployment.go:48`). Sequence Steps 2 and 3 into the same release, or gate Step 2 behind allowlist admission mode.
6. **dave-go maturity:** young, API-unstable, author-audited only (P10). The `WithDaveSessionCreateFunc` seam is the rollback path (ADR-0006), but there is no second production-ready pure-Go implementation to roll back *to* — golibdave means CGO + libdave install + a disputed ratchet race. Track upstream; consider funding/contributing an audit before open signup sells voice.
7. **Tenant↔Guild mapping has no schema** beyond deployment_config's single guild_id per tenant (open question from the storage findings). Multi-guild tenants (CONTEXT.md: "a Tenant may have many Guilds linked") need a guilds table before the claim plane can key on guild; keying claims on tenant_id defers this.
8. **DISCORD_OAUTH_CLIENT_ID vs BYOK app mismatch:** the invite URL shown in the Configuration screen is built from the operator's central app id — wrong for BYOK tenants (open question from the presence findings). Needs a per-tenant application-id field (derive from the token's first base64 segment as disgo does, but confirm disgo's parsing failure mode on malformed tokens — flagged unverified).
9. **DAVE enforcement timeline risk is now zero in the scary direction** (enforcement already happened; Glyphoxa already ships `-tags dave`) but nonzero in the churn direction: DAVE protocol v2, or Discord discontinuing v1, would land on a young pure-Go stack. The per-conn session seam contains the blast radius.
10. **Off-session spend attribution** (Recap/Highlight enrichment) remains un-tenant-attributed (ADR-0054 gap) — worker-fleet parallelism multiplies the size of that blind spot; fold into the usage-ledger work.
