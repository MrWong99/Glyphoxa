# Multi-replica voice fleet rollout (#492, ADR-0057)

Builds on the claim plane (#491, `2026-07-19-voice-claim-plane-rollout.md`). That
note made `-mode voice` a claim-plane Voice Instance; this one lets the chart run **more than
one** of them on the shared central token. The two mechanisms that make N replicas
safe are the Postgres claim plane (session assignment) and the presence-owner
election (interaction dispatch).

## Presence-owner election

Every gateway session on a shared central token receives the FULL event stream,
including duplicate `INTERACTION_CREATE` — Discord deduplicates nothing between
sessions on one token (ADR-0057 P5). So N Voice Instances would each try to handle
the same `/roll`. A singleton `presence_owner` claim row elects exactly ONE
Instance to register command listeners and dispatch interactions; every non-owner
Registry is `SetActive(false)` and drops the duplicate events it still receives.

- A `-mode voice` Voice Instance boots its Registry **inactive**; an `OwnerElector`
  runs beside the claim loop on the same `instanceID` and flips it active only
  while this Instance holds the `presence_owner` row.
- `-mode all` and the legacy standalone node are always their own single owner —
  their Registry defaults active, no election.

### Election knobs (env)

| Var | Default | Meaning |
|-----|---------|---------|
| `GLYPHOXA_PRESENCE_OWNER_INTERVAL` | `5s` | renew/acquire tick |
| `GLYPHOXA_PRESENCE_OWNER_EXPIRY` | `15s` | incumbent silence before its claim is stealable |

Interval sits well under expiry so a healthy owner never loses the row between
renewals; a dead owner's row is claimable by a challenger after one expiry, so
failover lands within roughly expiry + one interval (~20s worst case).

### Self-demotion when partitioned

An owner that can no longer reach Postgres self-demotes: once the MONOTONIC elapsed
since its last successful renew reaches `Expiry - Interval - opTimeout` (7s at
defaults), the elector calls `SetActive(false)` locally. The threshold sits BELOW
`Expiry` deliberately (#483): the demotion check runs only on ticks, so after the
elapsed crosses it up to one more `Interval` passes before the next tick and that
tick's failing acquire can burn its whole per-op timeout before the check runs — a
bare-`Expiry` threshold would therefore demote as late as
`Expiry + Interval + opTimeout`, several seconds INSIDE a challenger's ownership
(both dispatching the same `/roll`). With the padded threshold the local
deactivation always lands strictly before the DB steal horizon (`heartbeat_at +
Expiry`). It is judged on the process's own monotonic clock — never the DB
`heartbeat_at` or a wall clock. The acquire/renew call is bounded by a per-op DB
timeout of `min(Interval, 3s)` so a stuck connection cannot pin the loop and starve
the demotion check. The elector does NOT `Release` on demotion — the DB is
unreachable by assumption, so a local deactivation is all that is possible and the
row's own lease expiry hands ownership over. Re-promotion happens ONLY via the next
successful acquire in the normal loop.

**Interaction gap under partition.** Between an owner losing DB reachability and a
survivor claiming the expired lease, there is a window of up to roughly
`Expiry + Interval` (~20s at defaults) where the old owner has self-demoted (so it
dispatches nothing) but no new owner has been elected yet — interactions in that
window get no reply until the survivor promotes. This is the deliberate cost of
never running two owners at once (ADR-0057 (c) prefers a brief gap over a
double-dispatch); it is the same order as the failover window and does not affect
live voice (P6).

## Live slash controls at replicas > 1 (#483 → #503)

Interactions are dispatched by the elected presence OWNER, but a Tenant's live
session may be hosted by a DIFFERENT worker in the pool — and the live-control
state (the mute set, the say/replay outbound pump) lives in the hosting worker's
Manager, unreachable from the owner. So at `replicas > 1`:

- `/glyphoxa mute`, `/glyphoxa muteall` and `/say` work only when the presence
  owner happens to also host the session. When it does not, the handler consults
  the claim plane and replies honestly — "hosted by another worker; live controls
  aren't available from here yet" — instead of the false "No Voice Session is
  active." The cross-pod control plane that would make them work from any pod is
  tracked in **#503**.
- `/glyphoxa search` and `/glyphoxa recap` resolve the Active Campaign through
  the claim plane (pool-wide), so they work regardless of which worker hosts the
  session; a `voiced` recap degrades to public text when the Butler is not in the
  owner's own session (decision 6a).
- The web panel's mute/say already degrade with `CodeFailedPrecondition` in a
  split deployment (ADR-0057 consequence) — unchanged.

## Voice itself needs no election

A pod holding no voice connection for a guild simply receives and ignores that
guild's voice gateway events (ADR-0057 P6). The claim plane already guarantees one
Voice Instance per live session (one live intent per Tenant), so duplicate voice events on
the shared token are inert — only interaction dispatch needed the owner gate.

## Drain order (SIGTERM)

`stop claiming → Manager.Shutdown (Finish the live intent rows) → elector Release
(drop the presence_owner claim) → close the Discord clients`. The owner claim is
the LAST coordination write, so a survivor begins dispatching this instance's
interactions only after its sessions are wound down. Sessions are ENDED on drain,
never migrated (ADR-0006), so `voice.terminationGracePeriodSeconds` (default 300)
is sized to cover a DAVE/MLS wind-down before SIGKILL.

### Known windows (documented, accepted)

- **Heartbeat during drain — CLOSED by #505, hardened by the #509 review.** A
  draining worker now keeps heartbeating while it winds its sessions down: a
  drain-beat goroutine (every `GLYPHOXA_VOICE_HEARTBEAT_INTERVAL`, on detached
  bounded ctxs) covers the whole `Manager.Stop` window on the SIGTERM drain,
  the stop_requested wind-down, AND the mark-live-failure stop (the row still
  `claimed` there — the heartbeat fence accepts both states), so a clean
  wind-down within budget is never reaped `dead`, reconcile cannot race an
  in-flight CloseVoiceSession (the intent stays non-terminal until the close
  landed), and a true mark-live failure lands on the row as itself instead of
  as "worker heartbeat expired". Residual: a sustained DB outage spanning
  multiple missed beats past `GLYPHOXA_VOICE_HEARTBEAT_EXPIRY` still reaps
  mid-drain — the drain-beat goroutine then stops stamping (ADR-0006: never
  re-claim), the wind-down completes, the superseded finish is swallowed, and
  the history reads `dead`. Accepted: that now requires an outage, not merely a
  slow finalizer.
- **Wedged wind-down — bounded by the drain-beat cap (#509 review).** Drain
  beats cease after `GLYPHOXA_VOICE_DRAIN_BEAT_CAP` (default 10× heartbeat
  expiry, 300s): a run loop wedged past ctx cancel is the only unbounded step
  in `Manager.Stop`, and uncapped beats would keep its intent `live` forever —
  pinning the Tenant's voice plane, where pre-#505 the reaper freed it within
  30s. Past the cap the heartbeat goes stale and the reaper reclaims within one
  expiry; the wedged worker's eventual finish is fenced NotFound and swallowed.
  Note `voice.terminationGracePeriodSeconds` (300) bounds only the SIGTERM /
  pod-delete wind-down (the kubelet SIGKILLs at the deadline) — an in-place
  stop_requested wind-down on a healthy pod is bounded by this cap instead.
- **Reaped-but-alive overlap.** A worker that is merely SLOW (not dead) can be
  reaped: it learns of the supersede only on its next heartbeat (ErrNotFound) and
  then kills its local session — until that beat, its gateway/voice connection
  briefly coexists with whatever the Tenant restarted elsewhere. Bounded by one
  heartbeat interval + the wind-down; ADR-0006's "no takeover" already implies
  the old session is ENDED, never adopted, so the overlap is transient and
  side-effect-free (two gateway sessions on one token are permitted, P5/P6).

## IDENTIFY budget under fleet cold-start (#486)

No new code guards this — disgo serializes IDENTIFYs per client (`max_concurrency`
1, one per 5s per token), so two replicas booting on the shared token IDENTIFY a
handful of times total, nowhere near the 1000/24h/token budget. RESUME is free and
shows up in `glyphoxa_gateway_resume_total`, not identify. The fleet check below
scrapes `glyphoxa_gateway_identify_total` across the pods and fails on a blowout,
catching a serialization regression even without live traffic.

## Secret posture (ADR-0057 (d), resolved on #492)

Voice pods **mount** `GLYPHOXA_SECRET` (the `app-secret` key) — the "mounted
secret" arm of the knob ADR-0057 (d) left open, chosen over forwarding short-lived
credentials from the web tier. A Voice Instance in the pool holds BYOK Tenants' Discord
clients and must decrypt their bot tokens itself, so the voice role reads the
platform cipher; this deliberately widens the voice blast radius from ADR-0034's
old "does NOT read GLYPHOXA_SECRET" posture (which ADR-0057 already amends).

## Cross-pod consent poller

A tape-consent button (`/…grant`/`revoke`) is dispatched by the elected presence
OWNER, which publishes `TapeConsentChanged` on ITS OWN process bus. But in the fleet
the live tape may be running on a DIFFERENT pod (a claim-plane Voice Instance), whose bus
never sees that event — so the same-pod bus fast path alone would strand a cross-pod
grant/revoke. `wireTapeConsent` therefore also runs a poller goroutine on the cycle
ctx that re-reads the durable `tape_consent` rows every
`GLYPHOXA_TAPE_CONSENT_RECONCILE_INTERVAL` (default 5s), bounding cross-pod staleness
to one interval. The bus fast path stays (instant same-pod), `PublishToCampaign`
stays in the consent handler, and ADR-0051 holds because `ReconcileConsent`
authoritatively clears a revoked Speaker's ring. The poller dies with the cycle ctx.

## One-time upgrade transition

The FIRST `helm upgrade` from a pre-#492 chart to this one has a brief transition
window: the old voice pod runs the always-active Registry (no elector), and during
the `RollingUpdate` a new elector pod comes up and wins the `presence_owner` row —
for the surge overlap BOTH the old always-active pod and the new elected owner
dispatch interactions, a short duplicate-reply window. It self-heals the moment the
old pod terminates (its always-active Registry goes with it). Mitigation if a
duplicate reply during the upgrade is unacceptable: scale voice to 1
(`--set voice.replicas=1`) for that first upgrade so there is no surge overlap, then
scale back up. Subsequent upgrades are elector-to-elector and have no such window
(only the one owner ever dispatches).

## Helm

`voice.replicas` (default 1) is now a real tunable; `voice.pdb.minAvailable`
(default 1) arms a PodDisruptionBudget **only** when replicas > 1 (a single-replica
install has no availability to protect and a minAvailable-1 PDB over one pod would
block node drains). The Deployment uses `RollingUpdate{maxSurge:1,maxUnavailable:0}`
— the claim plane and per-Tenant guard make a brief pod overlap safe, replacing the
old single-channel `Recreate`.

## Manual fleet check

`scripts/k3d-fleet-check.sh` — NOT in CI (needs a live Discord token and a human to
type `/roll`). It installs the chart at `voice.replicas=2` and checks: both
replicas Available; `/roll` handled exactly once (human observation + per-pod log
cross-check); owner-pod kill → survivor elected within ~20s; IDENTIFY counters sane
under cold-start. Env conventions mirror `scripts/e2e-deploy-smoke.sh`
(`RELEASE`/`NAMESPACE`/`IMAGE_*`/`TIMEOUT`); see the script header for the full
recipe.
