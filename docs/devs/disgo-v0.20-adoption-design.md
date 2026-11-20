# disgo v0.20.0: in-place voice recovery

Status: implemented 2026-11-20 for #659, not yet soaked against live Discord.
The decision record is the 2026-11-20 amendment to
[ADR-0064](../adr/0064-voice-media-path-liveness.md); this note keeps the
reasoning that did not fit there and the soak checklist.

## Goal

A voice-session reset should look the way it does in the Discord client: a
short gap in the audio, with the Bot still sitting in the channel. Before this
change every reset ended the connect-and-serve cycle, so the Bot visibly left
and rejoined.

## What disgo v0.20.0 does

On a session-invalidating voice-gateway close (4006, 4009, 4015, a heartbeat
timeout, or five failed resumes) its conn closes the UDP transport, sends a
JOIN for the same channel, and re-opens the transport when the fresh session's
Ready arrives. It waits up to 30s; on failure it closes the conn, which sends
a LEAVE.

That recovery only helps if the audio goroutines survive it. disgo's sender
and receiver both reap themselves permanently on `net.ErrClosed`, and v0.20.0
additionally closes the frame provider when the sender goes. On the stock
transport, and on ours as it was, the conn would come back connected, deaf and
mute.

## What Glyphoxa does about it

Three pieces in `pkg/voice`, all installed by `TransportOption`:

1. **The transport pauses instead of closing** (`udpconn.go`). `Close` leaves
   it re-openable. While there is no socket, or no key for the new session
   yet, writes are dropped and reads block. Only `terminate` hands
   `net.ErrClosed` to the audio goroutines.
2. **A guard decides when a close was final** (`connguard.go`). disgo calls
   the conn's remove func only at the end of `conn.Close`; the guard uses that
   to terminate the transport, and from then on refuses the conn's voice-state
   updates. The refusal matters because disgo's recovery goroutine can still
   be running on a conn the Session has already closed, and its eventual
   `Close` sends a LEAVE for the whole guild.
3. **A sender that dies ends the cycle** (`provider.go`, `session.go`). The
   provider's `Close` also closes the inbound stream, so the existing rebuild
   path takes over.

The full rebuild is still there underneath: if disgo's recovery fails it
closes the conn, the guard terminates the transport, and `runWithReconnect`
builds a new connection as before. The media watchdog is unchanged.

## Known limits

- The utterance in progress when a session resets is lost. disgo does not
  re-announce Speaking for it on the new session; the next utterance does.
- A voice-server migration (a lone voice-server update) is ignored by disgo at
  both the old pin and v0.20.0. Recovery starts when Discord closes the old
  websocket.
- Nothing here has run against Discord. The tests drive disgo's real sender
  and receiver over fake sockets and a fake voice gateway.

## Live soak

1. Force a session-invalidating close mid-session (kill the voice websocket,
   or a long main-gateway outage). Expect a gap of a few seconds, the Bot
   staying in the channel, and both directions working afterwards.
2. Same, while an NPC is speaking and while a player is speaking.
3. Change the channel's region override mid-session (voice-server migration).
   Record how long recovery takes; anything near the 45s stall window is a
   reason to handle migration explicitly.
4. Stop a session during a recovery and start a new one within 30 seconds.
   The new session must not be kicked.
5. Kick the Bot from the channel. Behavior should match today's.
6. A DAVE-enabled channel for 30 minutes with speech both ways, then repeat
   scenario 1. This is the one that exercises MLS re-keying on a fresh
   session.
7. Two sessions on the standing presence client in different guilds; scenario
   1 on one must not disturb the other.
