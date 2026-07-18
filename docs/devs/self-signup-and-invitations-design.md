# Self-signup & tenant invitations — design note

Status: **decisions recorded** — ADR-0055 (self-signup Admission Modes) and ADR-0056
(Player Invitations / Linked Player access) were written 2026-07-18 from this note;
the transcript-text consent decision (D3) is folded into ADR-0056 rather than a
separate ADR. This note remains the implementation-grounding companion (verified
code citations, critic findings, phasing). `tenant_members` and the voice-concurrency
shape (D6) get their own ADRs when decided.

Two asks from the operator:

1. **Self-signup** — let a Discord user create their own Tenant instead of being
   hand-added to `GLYPHOXA_OPERATOR_IDS`.
2. **Tenant invitations (later)** — a Tenant owner invites other users into their
   Tenant to share transcripts / Highlights / more, with capabilities that depend
   on an assigned access level.

The SaaS foundation (ADR-0054) was built anticipating (1): it explicitly names "the
self-signup epic" and the two enforcement seams it must land. The membership model
for (2) is already decided on paper (ADR-0002 `owner`/`admin`/`gm`) but unbuilt.

The headline, though, is that **neither ask is primarily an auth feature — both are
gated on isolation debt and one hard architectural ceiling that the current
single-operator tier has never had to pay.** This note leads with those.

---

## 0. The two things that reframe the whole plan

### 0a. Cross-tenant isolation debt is larger than "add `WHERE tenant_id`"

The v1.0 web tier's isolation rests entirely on the allowlist admitting only trusted
operators (ADR-0039/0041). The moment a second, untrusted principal can hold a
session, a set of tenant-blind code paths become live cross-tenant holes. Verified
against `main`:

- **Active-campaign pivot.** `GetActiveCampaignForUser` joins on
  `u.discord_user_id` with **no tenant filter** (`internal/storage/auth.go`,
  the `SELECT … FROM users u JOIN campaign c … WHERE u.discord_user_id = $1 AND
  c.archived_at IS NULL`). `SetActiveCampaign` (`internal/rpc/campaign_management.go`)
  validates the target only for existence + not-archived. So a stranger in tenant T2
  can `SetActiveCampaign(<T1's campaign id>)`, and `resolveActiveCampaign`
  (`internal/rpc/active_campaign.go`) — which never reads `auth.TenantID(ctx)` —
  then resolves **every** campaign-scoped surface (roster, agent CRUD, KG, sessions,
  recap, transcript search) to T1. The injected tenant is decorative for these
  surfaces.
- **Destructive writes by client id.** `ArchiveCampaign` / `UnarchiveCampaign` /
  `DeleteCampaign` (`internal/rpc/campaign_archive.go`) and `UpdateCampaign` take the
  client id straight through to store methods scoped `WHERE id = $1` only. A known
  foreign campaign id → `DeleteCampaign` cascades away another tenant's Agents, KG,
  voice sessions and transcript PII.
- **StartSession blast radius.** After the pivot, `startCampaign` returns T1's
  campaign while `tenantID` is T2's; `mgr.Start(ctx, tenantID, campaign.ID)`
  (`internal/rpc/session.go`) runs the operator's live game and meters the spend to
  the stranger's tenant.
- **Bot-identity hijack via `GetLatestDeploymentConfig`.** Standing presence, slash
  registration, the Players picker, and Highlight Discord distribution all read the
  **globally most-recently-updated** `deployment_config` row across all tenants
  (`internal/storage/deployment.go`, `internal/presence/presence.go`,
  `cmd/glyphoxa/main.go`, `internal/rpc/highlight_sharer.go`). Any signup that saves
  Discord settings hijacks the single Bot's token/guild and becomes the identity
  through which *other* tenants' Highlights are posted — an unaudited cross-tenant
  breach of an ADR-0051 distribution surface.
- **Platform-key exposure is wider than the ADR-0054 seam.** `ResolveKey(nil)`
  returns `""` → adapter env fallback (`internal/llmbuild/llmbuild.go`). The
  ADR-0054 gate (a) as worded refuses configs whose `last4 == "env"`, but a tenant
  with **no config row at all** (the auto-Butler default) also resolves to `""` and
  silently spends the deployment's `GROQ/ELEVENLABS/GEMINI` keys. So **"BYOK free
  plan → zero platform-key exposure" is false** unless the resolve path fails
  *closed* for a BYOK tenant with no key. (Peer tenants' encrypted BYOK keys are
  genuinely safe — `provider_config` reads are tenant-scoped.)
- **Four allowlist-as-authorization sites, not three.** The design must relocate
  GM/operator identity off the env allowlist at: `oauth.go` (admission),
  `boot.go` gmSpeakerGate, `speaker/resolver.go` (transcript labels), **and
  `internal/presence/gate.go` `CheckGM`** (every `/glyphoxa` slash command).

**Consequence:** a real "isolation hardening" slice is a prerequisite epic, and it is
bigger than reads — it covers `SetActiveCampaign`/`GetActiveCampaignForUser`, the
id-driven destructive writes, session start, the deployment-config global read, and
the fail-closed key-resolve change. None of the softer signup work is safe to expose
before it lands.

### 0b. Concurrent voice per tenant (operator chose this) is a real epic — but a bounded one

**Decision D0 = concurrent voice too.** So the one-session-per-process ceiling must be
lifted. The good news from scoping the voice stack: the ceiling is a *domain guard*,
not a device/memory limit, and the hard parts are already solved.

What is **already** per-session and N-instanceable (verified): the entire audio
pipeline is built per connect-and-serve cycle — audio manager, codec, playback pump,
tee synthesizer, per-speaker VAD lanes, barge-in Floor, ensemble/reaction state,
STT/TTS/LLM clients, tool grants, BYOK keys, spend meter, usage ledger
(`internal/wirenpc/connect.go`, `pipeline.go`, `run.go`; `internal/session/manager.go`
`activeSession`). `pkg/voice.Manager` is **already multi-guild** on one bot client
(`sessions map[guildID]*Session`, "safe for concurrent use across many Guilds"). And
Discord permits **one bot app to hold one voice connection per guild across many
guilds simultaneously** — so tenants in different guilds run concurrent voice on the
**single shared Bot app**; per-tenant bot applications are *not* required (only wanted
if you ever want per-tenant bot branding).

What actually blocks concurrency — three things:

1. **The single-active guard.** `Manager.active` is a single pointer; `Start` returns
   `ErrSessionActive` if non-nil (`internal/session/manager.go`). Lifting it to a keyed
   `map[campaign]*activeSession` is trivial *in isolation* — but it's load-bearing
   because of #2.
2. **Session-blind event attribution (the deep one).** There is one process-wide
   `voiceevent.Bus` and its events carry **no session id**; the transcript relay,
   chunker, recall, kgfacts, knowledge adapter and highlight saver all resolve "which
   session" from a **single `Snapshot()`** of the one active session
   (`internal/transcript/relay.go`, `chunker.go`; `internal/session/view.go` panics on
   a second Manager bind). Two concurrent sessions would cross-attribute every
   transcript line, chunk, and Highlight. This was a deliberate design (#73/ADR-0014).
3. **Single-Bot / single-guild presence.** One shared gateway client built from
   `GetLatestDeploymentConfig` — the **globally most-recent** `deployment_config` row —
   plus one guild's slash-command registration (`internal/presence/presence.go`). When
   tenant B saves Discord settings, presence **tears down tenant A's client and
   rebuilds on B's token/guild**. The per-tenant `deployment_config.bot_token` column
   exists but is consumed as a global token today (and is ignored on the live voice
   path in favor of the shared presence client — a latent multi-tenant bug on its own).

Two credible shapes to fix it (this is the **key remaining architecture fork, D6**):

- **Shape A — one session per pod, scale pods + a backplane (ADR-blessed).** Keep
  today's single-active Manager per pod; concurrency = many pods. Build the deferred
  `voice.v1 VoiceControlService` (proto already authored, ADR-0039), a shared backplane
  for the SSE relay to read another pod's session events (Redis/NATS/pg LISTEN-NOTIFY,
  ADR-0014), and the `voice_sessions(guild_id PK, voice_instance_id, claimed_at,
  heartbeat_at)` claim/routing table already sketched in ADR-0005. Per-pod pipeline
  barely changes. Cost: real distributed-systems infra + the shared-bot-token gateway
  story across pods (sharding). Right long-term horizontal answer.
- **Shape B — K sessions in one process (no new infra).** Add `SessionID` to the
  `voiceevent` taxonomy and rekey the relay/chunker/recall/kgfacts/knowledge/highlight
  projectors off it (drop the single-Manager `View` bind); guard → keyed map; a
  per-tenant/per-guild presence client registry in the one process. Touches
  `internal/{session,transcript,recall,kgfacts,knowledge,highlight,presence}` +
  `pkg/voice/voiceevent` + the composition root, and it revises the intentional
  #73/ADR-0014 one-bus design. No Redis/pod fleet; all tenants' voice load on one pod
  (a real but honest scale ceiling, fine for a home-lab SaaS starting small).

**Recommendation:** for the "home k3s → cloud later, small audience first" trajectory
(ADR-0054), **Shape B first** — it delivers concurrent voice for a handful of tenants
with zero new infrastructure, and the `SessionID`-on-the-bus work is a prerequisite
Shape A would eventually want anyway. Graduate to Shape A when one pod's voice load is
the actual bottleneck. Either way this is a **standalone epic that gates paid
concurrent voice**, and it is now the critical-path item — ahead of invitations.

No hard external ceiling otherwise: audio never crosses process boundaries (ADR-0005),
so there's no shared mixer/egress to contend on; the only Discord limits are one voice
connection per guild per bot (met by mapping tenants to guilds) and gateway sharding
past ~2500 guilds (not near-term).

---

## 1. Self-signup (build now)

New **ADR-0055** superseding ADR-0041's admission posture *for the multi-tenant tier*
(0041 anticipates this: "Shared-Tenant membership waits for tenant_members"; open
tenant creation "may return with the multi-tenant tier" — the latter note actually
lives in ADR-0016). ADR-0055 must state, not imply, that it amends ADR-0041's
allowlist-as-sole-gate and ADR-0016's struck open-tenant-creation clause.

### 1.1 Admission mode

`GLYPHOXA_ADMISSION_MODE = allowlist (default) | open`.

- `allowlist`: today's behavior exactly. Self-host posture unchanged.
- `open`: any Discord user completing OAuth is admitted; signup provisions a
  **fresh** Tenant (create-only — never `ResolveOperatorTenant`'s claim-earliest,
  which would let a stranger claim the seed Tenant; honors ADR-0041's no-TOFU
  rejection).
- The allowlist is **not retired** in open mode — it is re-scoped as the
  **platform-admin list** (billing-CLI parity, future admin console, the deployment
  operator's own identity). Needs a CONTEXT.md term; "Operator" is defined *by*
  allowlisting today, so a signed-up founder is not an Operator — coin **Platform
  Admin** for the allowlist principal and keep **Operator**/**Owner** for the
  per-Tenant founder.

**Rollback/upgrade hazards to design around (all verified):**

- Admission mode as a plain env var is a **rollback brick**: an older binary ignores
  `GLYPHOXA_ADMISSION_MODE`, boots in allowlist posture, and its boot sweep
  (`RevokeSessionsOutsideAllowlist`, `cmd/glyphoxa/main.go`) mass-revokes every
  signup's session while the callback rejects their re-login. An open-mode deploy
  with an empty allowlist won't even boot the old binary (`requireWebEnv`). Mitigate
  by persisting admission state in the DB (a `deployment_settings` row), not only env,
  so posture is versioned and visible — and gate the boot sweep on it.
- The boot sweep must **still run in allowlist mode** (it is the lock-down escape
  hatch). In open mode it must not run (it would log out every signup each restart).
  A `users.suspended_at` sweep is *not* a substitute for allowlist revocation — keep
  both: allowlist revocation on allowlist boots, suspension-based revocation for
  open mode. Because suspension is a runtime DB decision with no restart,
  per-request re-check becomes mandatory here (ADR-0041's amendment pre-commits
  exactly this once admission is runtime-editable) — so per-request authz is part of
  the signup slice, not deferred to invitations.
- Boot contract: relax only the allowlist-nonempty branch for open mode; keep
  `DISCORD_OAUTH_*` mandatory (OAuth is still the signup mechanism). Add a
  `GLYPHOXA_ADMISSION_MODE` value + env to the Helm chart in the same slice
  (`secret.yaml`/`web-deployment.yaml` currently hard-require a non-empty
  `operator-ids`).

### 1.2 Default plan at signup

Provision binds the fresh Tenant to a default plan through `SetTenantPlan` semantics.
Options: a `default_for_signup` marker in the plan catalog **or**
`GLYPHOXA_SIGNUP_PLAN_SLUG`. Prefer the **env slug** — the catalog-marker option
trips `DisallowUnknownFields` (`internal/billing/catalog.go`) on any older binary,
which fails the pre-upgrade plans-sync hook Job and aborts the whole helm
upgrade/rollback. Ship the default as a **free BYOK plan** so no platform-key
entitlement is in play at signup. Add a **boot-time preflight**: if
`admission=open`, the signup plan slug must resolve to a non-archived plan, else
refuse to boot (otherwise signup fails at runtime *after* the user completed OAuth,
forever). This auto-bind is a new binding surface on ADR-0054's operator-CLI-only
decision — name it as an explicit amendment.

### 1.3 Entitlement enforcement (ADR-0054's named debt lands here)

- **(a) Provider-Config resolve fails closed.** A BYOK tenant with no real key must
  *error*, not fall back to platform env keys — change `ResolveKey`'s `nil`/`"env"`
  branch to refuse when the tenant lacks an active `key_source='platform'`
  subscription. **This must be conditioned on admission mode / subscription
  possibility**, or it breaks every existing self-host (whose only tenant has no
  subscription and rides `last4='env'` by design). i.e. in `allowlist` mode the gate
  is a no-op; in `open` mode it is enforced.
- **(b) Monthly allowance gate** over `usage_ledger` month-to-date `SUM(estimated_usd)`
  vs `plan.included_usage_usd`, patterned on the ADR-0046 spend-cap mechanics.
  Note the CONTEXT.md/ADR-0054 wording "the ledger never gates" — this gate reads
  the ledger but the *decision* stays with a spend-meter-style mechanism; call that
  out so the glossary and behavior don't contradict. Documented undercount:
  off-session usage (Recap / Highlight-enrich) is not yet tenant-attributed
  (ADR-0054 accepted loss).

### 1.4 Per-tenant GM/operator identity (prerequisite, ships in the hardening slice)

Relocate GM identity off the env allowlist at all four sites in §0a. Resolve GM as
"the Discord user bound to the Campaign's Tenant" during the transition
(`tenant.operator_user_id → users.discord_user_id`), becoming role-based when
`tenant_members` lands. Caveats the design must handle, all verified:

- This **contradicts ADR-0050's** "GM identity stays operator-allowlist membership"
  clause — ADR-0055 must amend it explicitly, not cite 0050 as "honored."
- `runVoice` (voice-only mode) never arms the gate (`cmd/glyphoxa/main.go`), so the
  standalone voice node is **fail-open** for Butler addressing today — rewiring only
  the all-mode gate leaves voice-only unaddressed.
- `speaker/resolver.go` computes GM **inline on every Lookup under a never-block
  contract** — a DB-backed source must be cached/prefetched, changing freshness
  semantics; preserve dev-mode's admit-all.
- **Not "zero behavior change":** an operator who only ever used slash commands has
  `operator_user_id = NULL` (only OAuth login / dev boot writes it) → GM resolves to
  nobody after the change. And a documented multi-entry allowlist's second account
  loses GM. The migration needs a backfill/fallback for NULL bindings.

### 1.5 Onboarding UI

Signup framing on `/login` in open mode (same Discord button); a minimal
`/onboarding/create-tenant` (name the Tenant). Grow `GetCurrentUserResponse` with
user id + tenant id/name (needed regardless; the proto deliberately omits user id
today — `management.proto`). Note dev-mode strips inbound cookies and pre-binds a
tenant (`boot.go`), so the OAuth-based signup flow is **structurally untestable under
`GLYPHOXA_DEV_MODE`** — signup UI needs a non-dev integration path.

### 1.6 Unchanged

Cookie sessions, CSRF double-submit, Discord-only OAuth (Google/GitHub stay v1.5+),
dev-mode loopback force, JWT ban.

---

## 2. Inviting players to share transcripts & highlights (later)

**Decision D1 = the "others" are the table's players, not collaborators.** That is a
big steer: the sharing feature is the **Linked Player lane** (ADR-0003), *not* a new
Member Role. Players do **not** become Tenant Members (CONTEXT.md/ADR-0003 forbid
adding `player` to the Member Role enum); they link their Discord identity to their
Character(s) and gain **character/campaign-scoped** read access. This *avoids* amending
ADR-0002/0003's membership tier — but it means building the ADR-0003 lane that is
**decided but entirely unbuilt** (`character.linked_user_id` exists in schema; **no
code path writes it** today).

So there are two distinct later tracks, and only the first serves this ask:

- **ADR-0056 — Linked Player access (this ask; written).** The player-invitation +
  link-up + scoped read authz + the transcript/Highlight surfaces a linked player
  sees, including the D3 sharing/consent position.
- **`tenant_members` (separate future ADR, for multi-GM tenants).** Only needed when a
  Tenant has more than one GM-tier human. Not required to share with players; keep it
  out of this epic unless multi-GM lands first. (Its backfill/cutover notes are parked
  in the appendix.)

### 2.1 The Linked Player lane

- Implement the write path for `character.linked_user_id` (set on the player's first
  Discord OAuth via an invitation — see 2.2). `CreateCharacter` currently excludes it
  and `UpdateCharacter`'s SET list omits it; both need the seam.
- A **linked player is not a session principal like an operator** — resolve their
  access by *the Characters whose `linked_user_id` = their user id*, joined to
  `campaign` → `tenant`. This is a new authorization dimension parallel to the
  membership one, and it must be tenant-safe from day one (it rides on §0a hardening).
- **Access level assigned** (the operator's phrase) = *what a linked player may see*,
  a per-link grant. Proposed levels, from the player's own scope outward:
  1. **own-character** — only their Character's own Transcript Lines / promoted
     Highlights they appear in.
  2. **campaign-highlights** — promoted Highlights for the Campaign, no raw text.
  3. **campaign-transcripts** — the whole Campaign's transcript text + promoted
     Highlights (GM opt-in; see 2.4 / D3).
  Store the level on the link/invitation, not on `character` (a player may have
  different levels across campaigns).

### 2.2 Player invitations

`player_invitation(id, tenant_id, campaign_id, character_id NULL, access_level,
token_hash, created_by, expires_at, single_use, pinned_discord_user_id NULL,
accepted_by NULL, accepted_at NULL, revoked_at NULL)`. A GM mints a link for a
specific Character (or campaign-wide), the player opens it, authenticates with Discord,
and the callback **links their identity to the Character** and records the access
level — it does **not** create a Tenant or a Subscription. Token primitive is sound
(reuse the 256-bit `crypto/rand` minter; store the hash). Redemption-flow requirements,
each from a confirmed weakness:

- **Do not overload the OAuth `state` nonce with the invite token** — `state` is the
  login-CSRF anti-forgery nonce (`internal/auth/oauth.go`); a token from a public link
  is neither unpredictable nor browser-bound. Carry the invite token in a **separate**
  signed cookie/param.
- **The callback must fork on intent** (this is now central, per domain critic #8):
  *found-a-tenant* (signup, §1) vs *link-a-character* (player invite) vs
  *accept-membership* (the future `tenant_members` track). Without the fork, a player linking a
  Character gets an auto-minted junk Tenant **and a junk Subscription row that pollutes
  ADR-0054's revenue record.** The invitation token is what selects the link-a-character
  branch.
- **Bind acceptance to the authenticated identity.** An unpinned single-use link is a
  bearer secret — whoever opens it first is linked as that player. Pin
  `discord_user_id` at mint (the GM already knows the player's Discord id from voice
  presence — ADR-0003), or bind on first auth and require a match.
- **Atomic single-use claim** — `UPDATE … WHERE accepted_at IS NULL` / `SELECT … FOR
  UPDATE`, else two concurrent redemptions double-link.
- Redemption mints a **cookie session** via the existing OAuth pipeline — humans never
  get bearer tokens (ADR-0016 holds).
- **Naming:** `internal/discordinvite` (ADR-0047) is Discord *guild*-invite resolution
  and `web/src/lib/discordLink.ts` parses `discord.com/invite/{code}` — do **not**
  reuse `/invite` or "invite" for this. Use a distinct route (e.g. `/join/<token>`) and
  the term **player invitation**.

### 2.3 Does a player invitation bypass admission mode? (D2 — decided)

**Decision D2 = only when the operator enables it.** A player invitation admits in open
mode, or on a self-host deploy only when the platform-admin explicitly turns on
per-Tenant invitation admission; **allowlist deploys stay sealed by default.** This keeps
ADR-0041 intact (a GM can't unilaterally admit arbitrary Discord users into a
locked-down self-host). The admission gate reads this flag *before* linking. And the
allowlist boot sweep must not evict linked players — since linked players aren't
allowlisted, the sweep needs the same open-mode/suspension rework as §1.1 (they are
gated by per-request authz over their character links, not the allowlist).

### 2.4 Sharing semantics under ADR-0051 (D3 — decided, needs an ADR)

**Decision D3 = players see Highlights *and* transcript text, level-gated, with a
per-campaign toggle.** ADR-0051 gates *audio distribution* on an explicit GM action but
says **nothing about transcript text** — so exposing transcript text to players is
**new policy surface**; the decision is recorded in ADR-0056 (with a relationship note
added to ADR-0051). Design:

- The **GM's per-Campaign "share transcripts with linked players" opt-in IS the
  explicit action** that satisfies ADR-0051's posture (default OFF, mirroring the tape
  consent default). Granting a player the `campaign-transcripts` access level is gated
  on it.
- Players see **promoted Highlights only** — never candidates (they purge 7 days after
  session end), never Promote/Delete/Share/GenerateRecap controls. Those stay GM-only.
- Whole-Campaign **bundle export includes full transcripts → operator-only**, never a
  linked-player capability.
- Open question for the ADR: transcript text may contain other players' consented (or
  unconsented-for-*audio*) speech — decide whether text visibility needs its own
  per-speaker consent, or whether the GM opt-in covers the table. Recommend GM opt-in
  covers it for v1 (text was never E2EE-shaped the way audio was), documented.

### 2.5 Player-scoped authorization

`auth.Policy` gains a **linked-player principal** alongside the operator/member one,
applied to **both** transports (Connect interceptor + `GuardedMount` rows — the #446
invariant). A linked player can call only: `GetCurrentUser`, and read Session/
transcript/Highlight **scoped to their linked Characters' Campaigns and their access
level**. Every read must intersect (their character links) × (per-campaign share
toggle) × (access level). Per-request re-check + targeted session revocation when a
GM revokes a link or lowers a level (ADR-0041's amendment obligation).

### 2.6 Plan limits

`plan.limits.max_linked_players` (or `max_players_per_campaign`) becomes the first
consumer of the currently-unread `limits` jsonb bag; invitation mint checks it.

---

## 3. Phasing (honest dependencies — "green alone" only where true)

Decisions D0–D3 are now made (see §4). The critical path runs through the voice
concurrency epic, because D0 = concurrent voice.

- **A. Isolation hardening (hard prerequisite, no new principal yet).**
  Tenant-scope `SetActiveCampaign` / `GetActiveCampaignForUser` /
  `resolveActiveCampaign` / the id-driven Archive/Delete/Update writes / session
  start; fix `GetLatestDeploymentConfig` global read (Bot/deployment-config per
  Tenant); fail-closed `ResolveKey`; per-tenant GM identity at all **four** allowlist
  sites (`oauth.go`, `boot.go` gmSpeakerGate, `speaker/resolver.go`,
  `presence/gate.go` `CheckGM`) incl. voice-only mode + resolver caching. Ships
  behavior-preserving for the single operator **only after** the
  NULL-`operator_user_id` fallback is handled. Also lands here (cheap, independent):
  proto `User` growth, `SetTenantPlan` `ErrNotFound` fix, stale "ADR-0018" migration
  comment → ADR-0002.
- **V. Concurrent-voice epic (critical path for paid product; its own ADR once the
  D6 shape is decided).**
  Recommended **Shape B**: `SessionID` on the `voiceevent` taxonomy; rekey
  relay/chunker/recall/kgfacts/knowledge/highlight off it; guard → keyed map;
  per-tenant/per-guild presence client registry; make the per-tenant
  `deployment_config` bot token actually drive `acquireClient`. Overlaps heavily with
  A's `GetLatestDeploymentConfig` fix — sequence A→V or merge them. (Shape A instead
  if you'd rather run a pod fleet + backplane now — see D6.)
- **B. Self-signup.** ADR-0055; admission mode (DB-persisted + chart value);
  create-only provisioning (transactional: tenant + plan-bind + session mint);
  default-plan env slug + boot preflight; entitlement seams (a)+(b) conditioned on
  mode; boot-sweep rework + per-request re-check; onboarding UI. **Gated on A. Can run
  parallel to V** (signup is web-tier; V is voice-tier) — but a tenant that signs up
  and can't run concurrent voice until V lands should be sold honestly.
- **P. Player invitations (this ask's "invite others").** ADR-0056 (written): Linked
  Player lane (`character.linked_user_id` write path); `player_invitation` + `/join`
  acceptance with the intent-fork callback; Player Access Levels; per-Campaign
  transcript-share toggle (transcript-text consent decided in ADR-0056);
  linked-player authorization principal; `limits.max_linked_players`. **Gated on
  A + B** (needs signup-provisioned tenants and the hardened reads).
- **M. (only if multi-GM tenants are wanted) `tenant_members`.** A future ADR —
  separate from P; see appendix. Not required to share with players.
- **Later, above unchanged ADR-0054 tables:** payment-processor automation of
  subscribe/cancel.

---

## 4. Decisions

**Made (this session):**

- **D0 = concurrent voice too.** Voice-concurrency epic (§0b / phase V) is on the
  critical path.
- **D1 = the "others" are players.** Sharing = Linked Player lane (ADR-0003), not a
  Member Role. `tenant_members` deferred to its own track.
- **D2 = invitations admit only when the operator enables it.** Allowlist deploys stay
  sealed by default.
- **D3 = players see Highlights + transcript text**, level-gated, per-Campaign GM
  toggle. The transcript-text consent decision is recorded in ADR-0056 (0051 covers
  audio only).

**Still open:**

- **D6 (new, load-bearing) — voice concurrency shape:** Shape B (K sessions/process,
  no new infra — recommended for the home-k3s start) vs Shape A (pod fleet + backplane,
  the ADR-0039/0014-deferred horizontal path). This sets the size and infra of the
  critical-path epic.
- **D4 — default signup plan:** free BYOK tier name/price? (Platform plans stay
  operator-CLI-assigned until a payment processor lands — out of ADR-0054 scope.)
- **D5 — allowlist re-scope:** its *survival* in open mode is decided (ADR-0055
  rejects retiring it); still open are its name ("Platform Admin" proposed) and the
  exact capabilities the list grants.

---

## Appendix — `tenant_members` notes (future-ADR track, parked)

Only if a Tenant needs more than one GM-tier human. `(tenant_id, user_id, role,
invited_by, created_at)`, `UNIQUE(tenant_id, user_id)`; backfill `operator_user_id →
'owner'`. Retirement of `operator_user_id` must be a **hard cutover or mandatory
dual-write**, never "gradual" — a window with the voice tier still reading
`operator_user_id` while member-edit RPCs run lets a *removed* member keep Butler voice
power and GM labels. Backfill edges: **dev-operator-held tenants** (synthetic user
can't OAuth — an 'owner' row is a dead end once claim-semantics retire) and
**zero-operator seed/demo tenants** (`operator_user_id = NULL` → zero membership rows →
unreachable after `TenantForUser` retires). Name an orphan policy. Also reconcile
ADR-0016's decided `X-Tenant-Id`-validated-against-`tenant_members` scoping against the
shipped server-side `TenantResolver` ("never from a client header") — pick one, amend
the other.
