# Single binary with modes; no audio across process boundaries

A single binary runs in one of three Modes: `all` (default, self-host), `web` (HTTP only), or `voice` (Discord bot + voice pipeline). Coordination uses a Postgres `voice_sessions(guild_id PK, voice_instance_id, claimed_at, heartbeat_at)` table plus `LISTEN/NOTIFY` for handoff events. **Audio frames never cross process boundaries** — only credential metadata does. Voice instances open the Discord voice WebSocket directly using credentials forwarded by the gateway role.

The SaaS scale path splits `web` further into separate `gateway` + `voice` roles. v1's gRPC AudioBridge (audio frames between gateway and worker) is explicitly removed.

**Why:** v1's worker/gateway split shipped audio frames over gRPC and accumulated latency, encoding bugs, and DAVE-rekey complexity. The failure mode was audio crossing the wire; control/telemetry over RPC is unaffected (see ADR-0014).
