# Voice claim plane rollout (#491, ADR-0057)

Session start is decoupled from the in-process Manager so a separate voice
worker can run it. This note records the operator-facing behaviour and the known
gaps a split deployment inherits.

## Modes

- **`-mode all` (self-host default) — unchanged.** The web tier drives the voice
  loop in-process via the Manager. It writes **no** `voice_session_intents` rows
  and behaves byte-for-byte as before.
- **`-mode voice` WITH `-guild`/`-channel`** — the legacy standalone node,
  unchanged: it joins one static channel.
- **`-mode voice` WITHOUT `-guild`/`-channel` (+ a database URL)** — the new
  **claim-plane worker**. It polls `voice_session_intents`, claims the oldest
  pending intent (`FOR UPDATE SKIP LOCKED`, ADR-0049), runs it through the
  tenant-aware Manager over the per-Tenant Discord client registry, heartbeats
  while live, and finishes the row on end. It takes each session's guild/channel
  from the Tenant's saved deployment config — the static flags are not required.
- **`-mode web` (split)** — the web tier's `StartSession`/`StopSession` write and
  flag intent rows via `IntentControl` instead of driving a loop. `GetSession`
  shows the session as *live* only once a worker has driven the intent live.

## Claim-plane knobs (env)

| Var | Default | Meaning |
|-----|---------|---------|
| `GLYPHOXA_VOICE_CLAIM_POLL` | `2s` | worker claim tick / web poll cadence |
| `GLYPHOXA_VOICE_HEARTBEAT_INTERVAL` | `5s` | live-session heartbeat stamp interval |
| `GLYPHOXA_VOICE_HEARTBEAT_EXPIRY` | `30s` | staleness before a claim is reaped dead |

## Worker death

A worker crash stops its heartbeats; after `HEARTBEAT_EXPIRY` the reaper marks
the intent **dead** and the Tenant sees it as such and can restart. There is
**no mid-session takeover** — DAVE/MLS state cannot migrate (ADR-0006/0057 (e)),
so a stale claim is a death, never a hand-off to another worker. A worker's boot
reconciliation closes only `voice_sessions` rows whose owning intent has gone
terminal, so two workers booting never close each other's live rows.

## Known gaps in split mode (pre-existing, unchanged by this slice)

- **No live SSE transcript on the web tier.** ADR-0014's Hop-A relay is still
  deferred: the worker persists transcript lines to Postgres, but the split web
  tier does not stream them live. Reload shows the persisted transcript.
- **`mute` / `say` / `replay` / spend-meter web RPCs are unavailable in split
  mode.** That live state lives in the worker, so the web tier degrades these
  with `CodeFailedPrecondition` ("not available in a split deployment") rather
  than lie. `-mode all` retains all of them.

## Scaling note

Running more than one worker replica is **not** enabled here: this is the
single-worker interim. Concurrent workers on the central token still need the
elected presence owner (#492) and the DAVE soak test (#493) before `replicas > 1`.
