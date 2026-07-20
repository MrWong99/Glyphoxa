# Voice claim plane rollout (#491, ADR-0057)

Session start is decoupled from the in-process Manager so a separate Voice
Instance can run it. This note records the operator-facing behaviour and the known
gaps a split deployment inherits.

## Modes

- **`-mode all` (self-host default) — unchanged.** The web tier drives the voice
  loop in-process via the Manager. It writes **no** `voice_session_intents` rows
  and behaves byte-for-byte as before.
- **`-mode voice` WITH `-guild`/`-channel`** — the legacy standalone node,
  unchanged: it joins one static channel.
- **`-mode voice` WITHOUT `-guild`/`-channel` (+ a database URL)** — the new
  **claim-plane Voice Instance**. It polls `voice_session_intents`, claims the oldest
  pending intent (`FOR UPDATE SKIP LOCKED`, ADR-0049), runs it through the
  tenant-aware Manager over the per-Tenant Discord client registry, heartbeats
  while live, and finishes the row on end. It takes each session's guild/channel
  from the Tenant's saved deployment config — the static flags are not required.
- **`-mode web` (split)** — the web tier's `StartSession`/`StopSession` write and
  flag intent rows via `IntentControl` instead of driving a loop. `GetSession`
  shows the session as *live* only once a Voice Instance has driven the intent live.

## Claim-plane knobs (env)

| Var | Default | Meaning |
|-----|---------|---------|
| `GLYPHOXA_VOICE_CLAIM_POLL` | `2s` | Voice Instance claim tick / web poll cadence |
| `GLYPHOXA_VOICE_HEARTBEAT_INTERVAL` | `5s` | live-session heartbeat stamp interval |
| `GLYPHOXA_VOICE_HEARTBEAT_EXPIRY` | `30s` | staleness before a claim is reaped dead |

## Voice Instance death

A Voice Instance crash stops its heartbeats; after `HEARTBEAT_EXPIRY` the reaper marks
the intent **dead** and the Tenant sees it as such and can restart. There is
**no mid-session takeover** — DAVE/MLS state cannot migrate (ADR-0006/0057 (e)),
so a stale claim is a death, never a hand-off to another Voice Instance.

Two reconcile paths close a crashed Voice Instance's leftover `running`
`voice_sessions` rows, both scoped to rows behind a **terminal** intent (never a
live Voice Instance's row): once at each Voice Instance boot, and inside the claim tick immediately after any
reap — so a fast pod restart never leaves those rows stranded until the next boot.

### Zero healthy Voice Instances

The reaper runs inside Voice Instance ticks, so with **no** running Voice Instance a `claimed`/
`live` intent's heartbeat never expires on its own and its Tenant would be blocked
(`AlreadyExists`) indefinitely. To escape this, the web tier's `StartSession`, on
hitting the one-live-per-tenant collision, reaps that specific blocking intent
**if its heartbeat is already stale** before failing — so a Tenant whose only
Voice Instance died can restart as soon as the expiry window passes, even before
a new Voice Instance boots.

### Start / Stop outcomes (split mode)

- **Start queued past its budget** — the pending intent is **cancelled** (→ `done`)
  and `StartSession` returns `CodeUnavailable` ("try again shortly"); the retry
  writes a fresh intent rather than colliding, and no Voice Instance booting later
  claims a stale row nobody is watching.
- **Start cancelled before a Voice Instance claimed it** — `CodeAborted` ("start was
  cancelled"), distinct from the still-queued Unavailable.
- **Stop unconfirmed within its budget** — `CodeUnavailable` (retry), never a
  false success carrying a still-`running` row.

## Known gaps in split mode (pre-existing, unchanged by this slice)

- **No live SSE transcript on the web tier.** ADR-0014's Hop-A relay is still
  deferred: the Voice Instance persists transcript lines to Postgres, but the split web
  tier does not stream them live. Reload shows the persisted transcript.
- **`mute` / `say` / `replay` / spend-meter web RPCs are unavailable in split
  mode.** That live state lives in the Voice Instance, so the web tier degrades these
  with `CodeFailedPrecondition` ("not available in a split deployment") rather
  than lie. `-mode all` retains all of them.

## Do NOT point `-mode all` at a Voice Instance's database

`-mode all` drives sessions in-process with **no** intent rows and runs the
**broad** boot reconcile (`ReconcileOrphanedVoiceSessions`), which closes *every*
`running` `voice_sessions` row it finds — including live rows a `-mode voice`
Voice Instance owns against the same DB. An all-mode process sharing a claim-plane
database would therefore both (a) close live Voice Instances' sessions on its boot and
(b) start intent-less sessions that break the one-live-per-tenant invariant.
Split (`-mode web` + `-mode voice`) and single (`-mode all`) are **distinct
deployments**; never mix them on one database.

## Slash commands in voice mode

`/glyphoxa start` and `/glyphoxa end` go through the claim plane, exactly like the
web tier: start writes an intent this same Voice Instance's loop claims (typically within
one poll), end requests the stop the loop honors. They never drive the Manager
directly, so a slash-started session always has an intent row (heartbeat,
reconcilable, visible to `GetSession` and the archive guard). The live controls
(`/glyphoxa search`/`recap`/`mute`/`say`) still drive the Manager — this Voice
Instance holds that live session.

## Scaling note

Running more than one Voice Instance replica is **not** enabled here: this is
the single-instance interim. Concurrent Voice Instances on the central token
still need the
elected presence owner (#492) and the DAVE soak test (#493) before `replicas > 1`.
