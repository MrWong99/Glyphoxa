# Multi-replica voice fleet rollout (#492, ADR-0057)

Builds on the claim plane (#491, `2026-07-19-voice-claim-plane-rollout.md`). That
note made `-mode voice` a claim worker; this one lets the chart run **more than
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

- The `-mode voice` worker boots its Registry **inactive**; an `OwnerElector`
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

## Voice itself needs no election

A pod holding no voice connection for a guild simply receives and ignores that
guild's voice gateway events (ADR-0057 P6). The claim plane already guarantees one
worker per live session (one live intent per Tenant), so duplicate voice events on
the shared token are inert — only interaction dispatch needed the owner gate.

## Drain order (SIGTERM)

`stop claiming → Manager.Shutdown (Finish the live intent rows) → elector Release
(drop the presence_owner claim) → close the Discord clients`. The owner claim is
the LAST coordination write, so a survivor begins dispatching this instance's
interactions only after its sessions are wound down. Sessions are ENDED on drain,
never migrated (ADR-0006), so `voice.terminationGracePeriodSeconds` (default 300)
is sized to cover a DAVE/MLS wind-down before SIGKILL.

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
credentials from the web tier. A worker in the pool holds BYOK Tenants' Discord
clients and must decrypt their bot tokens itself, so the voice role reads the
platform cipher; this deliberately widens the voice blast radius from ADR-0034's
old "does NOT read GLYPHOXA_SECRET" posture (which ADR-0057 already amends).

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
