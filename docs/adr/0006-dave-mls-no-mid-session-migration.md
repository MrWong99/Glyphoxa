# DAVE/MLS at session start; no mid-session migration

The DAVE/MLS handshake happens at Voice Session establishment; the Voice Instance that claims the session runs the MLS handshake itself using credentials forwarded by the gateway role. Live mid-session process migration is explicitly out of scope. If a Voice Instance crashes mid-session, the session drops and the user restarts.

libdave (CGo binding, cherry-picked from v1 per ADR-0007) is a v1.0 dependency.

**Why:** DAVE became mandatory after 2026-03-01 (Discord close code 4017 if unsupported). Mid-session migration would require MLS key-package transfer plus state replication across processes — complexity not justified for v1.0 when "session drops, user restarts" is acceptable.

---

**Amendment (2026-07-16, dave-go migration):** The DAVE implementation is now
[thomas-vilte/dave-go](https://github.com/thomas-vilte/dave-go) — pure Go
(DAVE v1 over the author's RFC 9420 [mls-go](https://github.com/thomas-vilte/mls-go)),
wired through the same `godave.Session` interface disgo consumes. libdave and
its CGo binding (godave/golibdave) are removed; the `-tags dave` build needs no
native library. Everything above about handshake-at-session-start and
no-mid-session-migration is unchanged.

Trade-off accepted deliberately: libdave is Discord's official implementation
(protocol audited by Trail of Bits); dave-go is a young solo-author
reimplementation with **no third-party security audit** (its mls-go layer is
interop-verified against mlspp — the MLS library inside libdave — and OpenMLS).
Accepted because Glyphoxa's threat model treats DAVE as a transport requirement
(close code 4017), not a security boundary of the product: the bot is a
legitimate E2EE participant that transcribes the session anyway. Revisit if the
threat model changes or an audited pure-Go MLS emerges. Rollback path: restore
godave/golibdave behind the same one-line `voice.WithDaveSessionCreateFunc`
seam in `pkg/voice/dave.go`.
