# DAVE/MLS at session start; no mid-session migration

The DAVE/MLS handshake happens at Voice Session establishment; the Voice Instance that claims the session runs the MLS handshake itself using credentials forwarded by the gateway role. Live mid-session process migration is explicitly out of scope. If a Voice Instance crashes mid-session, the session drops and the user restarts.

libdave (CGo binding, cherry-picked from v1 per ADR-0007) is a v1.0 dependency.

**Why:** DAVE became mandatory after 2026-03-01 (Discord close code 4017 if unsupported). Mid-session migration would require MLS key-package transfer plus state replication across processes — complexity not justified for v1.0 when "session drops, user restarts" is acceptable.
