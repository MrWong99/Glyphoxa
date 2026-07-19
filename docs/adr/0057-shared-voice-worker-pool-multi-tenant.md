# Shared voice-worker pool: per-tenant Discord clients, Postgres claim plane, presence owner

Self-signup targets concurrent voice per Tenant (ADR-0055), and the epic
tracking it (#483) needs a concurrency *shape*: K Voice Sessions across many
Tenants, on a SaaS K8s deploy, supporting both BYOK per-tenant Discord bot
tokens and one central operator-managed token. The working hypothesis —
"spawn a dedicated worker pod per tenant" — was assessed against verified
Discord platform constraints and the actual codebase on 2026-07-19; the full
assessment, including the constraint map, is committed at
`docs/devs/2026-07-19-multi-tenant-voice-assessment.md`. Two blockers stand in
the way of any shape: the presence layer reads the globally-newest
`deployment_config` row regardless of Tenant, so one Tenant saving Discord
settings tears down another Tenant's client (the presence-hijack blocker,
`internal/storage/deployment.go:48-69`); and `session.Manager` holds one
active-session pointer process-wide (`internal/session/manager.go:37-41`),
with the process-wide `voiceevent.Bus` carrying no SessionID for consumers to
key on. Both are named prerequisites in `self-signup-and-invitations-design.md`
and gate every option below, not just the one chosen here.

## Decision

**(a) One shared voice Deployment; a pool of Voice Instances.** The isolation
unit is a **per-tenant client registry** inside the presence layer — a standing
disgo client keyed by the Tenant's *resolved* bot token (central-token Tenants
share one client and its many-guild `pkg/voice.Manager`; a BYOK Tenant gets its
own client) — never a per-tenant pod. Discord platform realities make gateway
sessions cheap and voice connections portable-per-guild, so the isolation a
per-tenant pod buys has no scarce resource behind it; per-tenant Discord
*client* is the correct unit, per-tenant *pod* is not.

**(b) Session assignment is a Postgres claim plane.** A `voice_session_intents`
table, tenant-keyed, claimed with `FOR UPDATE SKIP LOCKED` plus a heartbeat —
the same coordination idiom the job runner already proves out (ADR-0049,
`internal/storage/job.go:115`). Poll only: **no LISTEN/NOTIFY.** Polling is
sufficient at this scale and keeps one coordination idiom in the codebase
instead of two.

**(c) Central-token interactions are dispatched by exactly one elected
presence owner.** Every gateway session on a shared token receives the full
event stream, so N Voice Instances holding sessions on the central token would
each see every `INTERACTION_CREATE` and each try to handle it. A
`presence_owner` claim row elects exactly one Voice Instance to register
command listeners and dispatch interactions for a given shared token;
non-owners drop the duplicate events they still receive.

**(d) Voice pods mount `GLYPHOXA_SECRET`.** A worker that holds a BYOK Tenant's
client must decrypt that Tenant's bot token itself, so the voice role reads the
platform secret cipher — deliberately widening its blast radius from today's
posture. Whether decryption happens in the voice role directly or credentials
are forwarded pre-decrypted from the web role is an open implementation knob,
tracked on #492.

**(e) No mid-session takeover.** ADR-0006 stands unchanged: the Voice Instance
that claims a session runs its own DAVE/MLS handshake; live migration to
another process is out of scope. A claim's heartbeat going stale means the
Voice Instance is dead, not that the session should be handed to another
instance — the Tenant restarts the session.

## Verified platform constraints this rests on

- **DAVE is mandatory and per-connection (P1).** Since 2026-03-01/02 the voice
  gateway closes non-DAVE clients with code 4017; MLS state is strictly
  per-voice-connection (one MLS group/epoch per call). N simultaneous
  connections in one process are N independent MLS groups — structurally
  sound, but concurrent multi-connection DAVE from a single process is
  field-unverified pending a soak test (#493), not merely assumed safe.
- **Gateway sessions are not scarce below 2500 guilds; the IDENTIFY budget is
  the real limit (P5).** Multiple concurrent gateway sessions on one token are
  permitted, including identical shard coordinates — but the budget is 1000
  IDENTIFYs/24h/token, and exhaustion terminates every session on that token
  and resets it (an outage-class event, with an owner-email reset flow).
  RESUME is free; `max_concurrency` (default 1) serializes IDENTIFYs at one
  per 5s per token. Sharding becomes mandatory only at 2500+ guilds.
- **Every gateway session on one token receives the full event stream (P5),
  including duplicate `INTERACTION_CREATE`.** Discord deduplicates nothing
  between sessions on the same token — this is what makes (c) load-bearing,
  not optional polish.
- **`SetGuildCommands` bulk overwrite is idempotent and quota-free for
  identical sets (P7).** Re-registering the same guild command set on every
  Voice Instance that touches a guild costs nothing extra; only genuinely new
  commands count against the 200-creates/day/guild quota.
- **One voice connection per guild per bot user (P6).** A pod holding no
  connection for a guild simply receives and ignores that guild's voice
  gateway events — the mechanism that makes multiple pods safely coexist on
  the central token for voice, distinct from the interaction-dispatch problem
  in (c).
- **BYOK tokens are independent rate-limit pools (P8).** Each BYOK Tenant's
  1000/day IDENTIFY budget and shard math is its own; a BYOK Tenant's token
  dying (revocation, rotation) is isolated to that Tenant's client.

## Rejected alternatives

- **Per-tenant pods (Option A).** Excellent BYOK fit — one token, one app, one
  pod — but poor at the central-token mode this platform also has to serve: N
  pods on the shared central token each receive the full interaction stream
  (duplicate handling unless centralized anyway), the shared 1000/day IDENTIFY
  budget turns a crash-looping fleet into an operator-token outage, and idle
  Tenants carry a nonzero pod cost floor. Also needs a tenant→pod
  controller/spawner the deploy story (single Helm chart, "scaling out is a
  design change") doesn't have.
- **Lavalink-style gateway/media split (Option B).** The canonical answer if
  the central bot were near sharding scale, but not yet: it correlates every
  worker's voice connection to one gateway service's session health (a 4014
  storm on any gateway-service hiccup, matching Lavalink's own documented
  failure mode), requires the credential-forwarding backplane ADR-0039
  deferred and the repo does not contain, and exercises an outside-a-`bot.Client`
  disgo usage mode that is untested. Re-evaluate when the central bot
  approaches the 2500-guild sharding threshold — the claim table and
  per-tenant client registry chosen here carry over unchanged if that day
  comes.
- **Hybrid A+B (Option D).** Superficially attractive symmetry (per-tenant
  pods for BYOK, gateway split for central) but means building and
  maintaining both the tenant-pod controller of A and the backplane of B —
  two divergent voice paths through `wirenpc` for no near-term benefit.
- **LISTEN/NOTIFY for claim handoff.** Polling suffices at this scale and
  keeps one coordination idiom instead of two; the job runner (ADR-0049)
  already establishes SKIP-LOCKED-plus-poll as the codebase's idiom for
  exactly this kind of work distribution.
- **Interactions Endpoint URL now.** Structurally the cleanest long-term fix
  for duplicate interaction delivery (mutually exclusive with gateway
  delivery, per Discord's own model) but requires Ed25519 webhook
  verification the web tier does not yet have. A future on-ramp, not adopted
  here — the presence-owner claim in (c) is the near-term mechanism.

## Consequences

- Non-owner Voice Instances receive but drop duplicate interaction events for
  a shared token; only the elected presence owner dispatches them.
- A voice pod split from the web tier still has no live SSE transcript relay
  (ADR-0014's Hop-A remains deferred) and no mute/say RPCs — those gaps are
  pre-existing and unchanged by this ADR.
- The secret blast radius is deliberately widened: the voice role now reads
  `GLYPHOXA_SECRET`, a posture change from today's chart (superseded below).
- Running K concurrent DAVE sessions in one process is soak-unverified; #493
  tracks the load test that must precede committing to a K value.

## Amendments

**ADR-0039** — its claim that "the `voice.v1 VoiceControlService` proto
(`claim_session` / `release_session` / `push_event`) is authored now" is
stale: `proto/` contains only `management.proto`. That plan is superseded by
this ADR's Postgres claim plane (`voice_session_intents`), not by a gRPC
control service. An amendment note recording this is added at
`docs/adr/0039-mvp-ui-backend-single-operator-web-tier.md`.

**ADR-0034** — the voice Helm template's single-replica-by-intent posture and
its "does NOT read `GLYPHOXA_SECRET`" comment
(`deploy/charts/glyphoxa/templates/voice-deployment.yaml:7-19`) are superseded
for the SaaS chart: the voice Deployment now runs a pool of Voice Instances
(`replicas` becomes a real tunable) and mounts `GLYPHOXA_SECRET` to decrypt
BYOK Tenant tokens. An amendment note recording this is added at
`docs/adr/0034-deployment-artifacts.md`.

**ADR-0005** — its coordination sketch, `voice_sessions(guild_id PK,
voice_instance_id, claimed_at, heartbeat_at)` plus `LISTEN/NOTIFY`, was never
built (`internal/storage/migrations/00006_voice_sessions.sql` is lifecycle
rows only) and is superseded by the tenant-keyed, poll-based
`voice_session_intents` claim plane decided here. An amendment note recording
this is added at `docs/adr/0005-single-binary-modes-no-audio-rpc.md`.

## Relationship to other ADRs

- **ADR-0006** — its no-mid-session-migration stance is adopted unchanged as
  decision (e); DAVE/MLS handshake-at-claim-time is exactly how a Voice
  Instance takes a `voice_session_intents` row.
- **ADR-0049** — its `FOR UPDATE SKIP LOCKED` job-claim idiom is the pattern
  `voice_session_intents` reuses for session assignment.
- **ADR-0055** — this ADR supplies the concurrency shape that ADR-0055
  deliberately left undecided ("gets its own ADR"); the presence-hijack and
  single-active-session blockers ADR-0055 names as hardening prerequisites are
  the ones decision (a)'s client registry and (b)'s claim plane close.
- **ADR-0014** — the SSE relay (Hop-A) stays deferred; this ADR does not
  change that.
