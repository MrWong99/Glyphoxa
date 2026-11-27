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
    hiccup still proves the path is alive, plus a keepalive-echo counter (the
    server reflects the 8-byte datagram back, so a healthy socket receives one
    ~every 5s regardless of anyone speaking) — the liveness signals everything
    below reads via `Session.MediaLiveness`;
  - re-`Open` retires the previous socket on a short linger (disgo overwrites
    it un-closed, stranding a reader blocked on the old fd forever — a second
    latent zombie this replaces; the linger, rather than an immediate close,
    keeps an in-flight sender `Write` holding the old snapshot from eating
    `net.ErrClosed`, on which disgo's sender permanently self-reaps);
  - `Close` is terminal: a voice-gateway `Ready` still in flight during
    teardown gets a refusal instead of reviving a socket and keepalive
    goroutine nothing would ever stop.
- **A per-cycle media watchdog detects and recovers.** It lives in wirenpc's
  `connectAndServe`, bound to cycleCtx like the stage subscriber, and carries
  two independent evidence tiers because neither signal alone is trustworthy:
  - *Echo cessation* (primary): once the current socket has proven the voice
    server echoes our keepalives (≥3 echoes), a full stall window
    (`GLYPHOXA_VOICE_MEDIA_STALL_WINDOW`, default 45s) with NOTHING inbound —
    no RTP, no echoes, while keepalives go out every 5s — is a dead socket,
    whatever the table is doing. Self-calibrating: a server generation that
    never echoes never arms this tier, so echo absence alone is never read as
    death.
  - *Speaking without audio* (secondary): no RTP for the window (doubled while
    the connection has never carried a packet, so a slow DAVE/MLS handshake
    cannot false-fire) while a **remote** participant announced ACTIVE speech
    on the still-healthy voice websocket comfortably after audio last moved
    (observed via `voice.WithConnEventHandlerFunc`; stop announcements and the
    Bot's own speaking echo are excluded). Discord does not guarantee
    per-utterance Speaking relays — historically they arrive at least on a
    user's first transmission — which is exactly why this tier is secondary
    and the echo tier exists.

  Both tiers read counters that only move while disgo's receiver reads, and it
  does not read while the DAVE session is not ready. So while DAVE is not ready
  the two tiers are skipped, and the gated time is not charged to them once
  reading resumes (#688). Not ready means one of two things, told apart by
  whether anyone else is in the channel (the voice gateway's
  clients_connect/client_disconnect, counted on the DAVE session wrapper; a
  remote Speaking announcement during the spell counts too):
  - *With another member present* it is an MLS handshake. One still pending a
    full window after that member was first seen (doubled for a connection
    that was never ready) is its own verdict, `dave_not_ready`, checked first.
  - *With the Bot alone* it is no handshake: Discord establishes the DAVE group
    only once a second member is present, so a Voice Session started in an
    empty channel (the ADR-0061 case) is legitimately not ready until someone
    joins. No verdict, however long it lasts — a rebuild would only rejoin the
    same empty channel.

  A genuinely quiet table trips none of them: quiet stops RTP, never the
  echoes, and produces no Speaking announcements; an empty channel has no
  member to handshake with — Idle Close remains that table's only policy. On
  a verdict the watchdog cancels the cycle with the
  `errMediaStall` cause, and `runWithReconnect` rebuilds the whole connection;
  every rebuild still counts toward the ADR-0061 churn ceiling.
- **Idle Close stops lying about dead transports.** The watchdog also flags the
  session's idleclose handle (`Session.MediaSuspect`); an idle breach with the
  flag standing closes as the new ADR-0043-shaped reason
  `media_path_dead: the Voice Session received no audio while participants kept speaking`
  instead of `idle_no_audio` (amends ADR-0061's reason list). Audio flowing
  again clears the flag on the next sweep, and churn still outranks both.
- **A media blackout can never again be a log blackout.** The watchdog emits
  one INFO `voice media liveness` line per minute (packet and keepalive deltas,
  last-packet and last-speaking ages, and `dave_ready` / `dave_peers` — not
  ready means the receiver is gated and the deltas are frozen; with zero peers
  that is the Bot waiting alone, not a fault) whenever a session is open — even with
  the verdict disabled — plus a WARN at the moment of a stall verdict. New
  series, unlabelled per ADR-0032: `glyphoxa_voice_udp_keepalives_total`
  (~0.2/s per open connection; flat while `glyphoxa_voice_sessions > 0` is
  itself a transport alarm), `glyphoxa_voice_udp_keepalive_send_errors_total`,
  and `glyphoxa_voice_media_stall_rebuilds_total`.

## Consequences

- A dead media path now costs the table under a minute of deafness (one stall
  window + a rejoin) instead of up to 15 silent minutes, and the session record
  tells the truth about what happened when recovery fails. A PERSISTENTLY dead
  path rebuild-loops at roughly one cycle per stall window — each cycle serves
  past `healthyAfter`, so the reconnect backoff stays at its initial delay by
  design — bounded by the ADR-0061 churn ceiling and, 15 minutes in, by the
  Idle Close window delivering the `media_path_dead` verdict.
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

## Amendment 2026-11-20: in-place recovery on disgo v0.20.0 (#659)

disgo v0.20.0 changed what its voice conn does on a session-invalidating
voice-gateway close (4006, 4009, 4015, a heartbeat timeout, five failed
resumes): it now closes the UDP transport and re-opens it for a fresh voice
session **without leaving the channel**, the way the Discord client does. Two
clauses above assumed the opposite and are superseded:

- **`Close` is no longer terminal; it is a pause.** The transport has three
  states — open, paused, terminated. A `Close` stops the keepalive loop and
  closes the socket (nothing leaks) but a later `Open` resumes. While paused,
  and after a re-`Open` until the new session's key arrives, `Write` drops the
  frame and reports it written (the RTP counters still advance, so the far
  side sees packet loss) and `ReadPacket` blocks. Neither returns
  `net.ErrClosed`: disgo's sender and receiver reap themselves permanently on
  it, and a recovery that came back connected, deaf and mute would be worse
  than the rebuild it replaces. A socket retired by a re-`Open` no longer ends
  the reader either; it moves to the new one. So a voice-session reset costs
  a gap in the audio, not a LEAVE and a JOIN.
- **Termination comes from the conn guard, not from `Close`.**
  `TransportOption` now also installs `guardedConnCreate`
  (`pkg/voice/connguard.go`), which wraps the two funcs disgo's voice manager
  hands every conn:
  - `removeConnFunc` is called only from the last step of the conn's `Close`.
    The guard treats it as the discard signal and terminates the transport:
    `Open` is refused (the original straggler-Ready protection), and the
    audio goroutines get `net.ErrClosed` and reap. That is what ends the
    cycle when disgo gives up on its own recovery and closes the conn behind
    our back, and what `Session.Close`'s sender poke (#579) relies on.
  - the voice-state update func is fenced once the conn is discarded. disgo's
    recovery runs on its own goroutine for up to 30s and ends in a `Close`,
    which sends a LEAVE for the whole guild; on a conn the Session already
    closed, that LEAVE would kick whichever conn serves the guild by then.
- **A provider closed by disgo ends the cycle.** v0.20.0's sender calls
  `Close` on its provider when it shuts down. `switchingProvider.Close` now
  also closes the inbound dispatcher, so a sender that died mid-session is an
  ordinary "connection finished" event instead of an undetectable mute NPC.

What stays as decided: the keepalive, the counters, the watchdog and its two
tiers. A recovery that outlasts the stall window still trips the watchdog and
falls back to a full rebuild; disgo bounds its own attempt at 30s, inside the
default 45s window. Counters reset on the re-`Open`, which the watchdog
already reads as a fresh path.

Not covered: a voice-server migration (a lone voice-server update). disgo
ignores it at the pin and at v0.20.0 alike; recovery waits for Discord to
close the old websocket and then takes the path above. The first utterance in
progress when a session resets is lost, and disgo does not re-announce
Speaking for it.
