# Voice media-path liveness: UDP keepalive, receive watchdog, honest close reason

A production Voice Session went silently deaf twice in a row (#633): after ~13–15
minutes Discord stopped delivering inbound RTP while both participants kept
talking. Every socket stayed open, the voice websocket kept heartbeating, no
error was logged, no metric moved, and 13.5 minutes of total log blackout ended
with Idle Close blaming an idle table (`idle_no_audio`). Root cause, confirmed
in the pinned disgo tree and upstream master: disgo sends **no voice UDP
keepalive of any kind**. The only outbound UDP is IP discovery at open and RTP
while the Bot speaks (plus ~100ms of trailing silence frames); a non-speaking
Bot is completely outbound-silent, and Discord's voice server stops routing
inbound media to a peer that stops proving liveness — the reason discordgo,
discord.js, and eris all send a small keepalive datagram every ~5s. Meanwhile
disgo's receiver goroutine blocks forever in a deadline-less read, so nothing
above it can ever notice. This ADR decides the three-layer fix.

## What this decides

- **pkg/voice owns the voice UDP transport.** `TransportOption()` (a sibling of
  `DaveOption()`, applied unconditionally at BOTH client build sites — the
  wirenpc per-cycle client and the presence standing client) swaps disgo's
  `UDPConn` for `keepaliveUDPConn` via `voice.WithUDPConnCreateFunc`. It
  reimplements the pinned disgo transport (re-diff `voice/udp_conn.go` on every
  deliberate disgo bump — see the go.mod pin comment) and adds what the stock
  one lacks:
  - a keepalive loop for the whole connection lifetime — one 8-byte
    little-endian counter datagram (the discordgo wire shape) every 5s, written
    directly to the raw socket, never through the RTP `Write` path (which
    mutates sequence/timestamp state and DAVE-encrypts);
  - a parsed-RTP packet counter, stamped before decryption so a DAVE key-roll
    hiccup still proves the path is alive — the liveness signal everything
    below reads via `Session.MediaLiveness`;
  - re-`Open` closes the previous socket (disgo overwrites it, stranding a
    reader blocked on the old fd forever — a second latent zombie this
    replaces).
- **A per-cycle media watchdog detects and recovers.** It lives in wirenpc's
  `connectAndServe`, bound to cycleCtx like the stage subscriber. Its verdict
  requires BOTH halves of the incident's signature: no RTP for the stall window
  (`GLYPHOXA_VOICE_MEDIA_STALL_WINDOW`, default 45s; doubled while a connection
  has never carried a packet, so a slow DAVE/MLS handshake cannot false-fire)
  AND a **remote** Speaking announcement on the still-healthy voice websocket
  after audio last moved (observed via `voice.WithConnEventHandlerFunc`; the
  Bot's own speaking echo is excluded). A genuinely quiet table produces no
  Speaking events and can never trip it — Idle Close remains that table's only
  policy. On a verdict it cancels the cycle with the `errMediaStall` cause, and
  `runWithReconnect` rebuilds the whole connection on its existing backoff;
  every rebuild still counts toward the ADR-0061 churn ceiling.
- **Idle Close stops lying about dead transports.** The watchdog also flags the
  session's idleclose handle (`Session.MediaSuspect`); an idle breach with the
  flag standing closes as the new ADR-0043-shaped reason
  `media_path_dead: the Voice Session received no audio while participants kept speaking`
  instead of `idle_no_audio` (amends ADR-0061's reason list). Audio flowing
  again clears the flag on the next sweep, and churn still outranks both.
- **A media blackout can never again be a log blackout.** The watchdog emits
  one INFO `voice media liveness` line per minute (packet and keepalive deltas,
  last-packet and last-speaking ages) whenever a session is open — even with
  the verdict disabled — plus a WARN at the moment of a stall verdict. New
  series, unlabelled per ADR-0032: `glyphoxa_voice_udp_keepalives_total`
  (~0.2/s per open connection; flat while `glyphoxa_voice_sessions > 0` is
  itself a transport alarm), `glyphoxa_voice_udp_keepalive_send_errors_total`,
  and `glyphoxa_voice_media_stall_rebuilds_total`.

## Consequences

- A dead media path now costs the table under a minute of deafness (one stall
  window + a backed-off rejoin) instead of up to 15 silent minutes, and the
  session record tells the truth about what happened when recovery fails.
- The keepalive removes the root cause, so the watchdog should be a
  rarely-firing safety net; a moving `media_stall_rebuilds_total` is a signal
  to investigate, not steady-state noise.
- pkg/voice carries ~300 lines of reimplemented disgo transport that must be
  re-diffed against `voice/udp_conn.go` whenever the deliberately-pinned disgo
  version moves. Upstreaming the keepalive to disgo would let this shrink back
  to a thin wrapper; until then the pin comment carries the warning.
- Watchdog rebuilds are indistinguishable from network-drop reconnects to
  everything downstream (cycle count, backoff, session continuity) — by
  design, since that path is battle-tested. The `errMediaStall` sentinel is
  the one place the cause is named (reconnect log + metric).
- Operators can tune or disable the verdict (`GLYPHOXA_VOICE_MEDIA_STALL_WINDOW`,
  literal `off`), but not the keepalive or the liveness log — there is no
  deployment in which the stock silently-deaf transport is the right choice.
