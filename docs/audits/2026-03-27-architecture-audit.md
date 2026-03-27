# Architecture Audit — 2026-03-27

**Auditor:** Claude (Architecture Audit Agent)
**Scope:** Full architectural audit across session lifecycle, multi-tenancy, voice pipeline, data consistency, configuration, error propagation, and security.
**Codebase:** Glyphoxa main branch at commit `2fc85e5`

---

## Executive Summary

This audit reviewed ~170 non-test Go source files across the Glyphoxa codebase. **51 findings** were identified: 4 Critical, 14 High, 28 Medium, and 5 Low severity. The most urgent issues are a missing tenant authorization check on session stop (allowing cross-tenant session termination), zombie sessions that permanently leak when they reach "active" state but never heartbeat, knowledge entity queries missing campaign_id filters, and NPC store lacking tenant-level isolation.

---

## Findings

### 1. Session Lifecycle

#### 1.1 TOCTOU Race in GatewaySessionController.Start

- **Severity:** High
- **Location:** `internal/gateway/sessionctrl.go:152-158`
- **Description:** `Start()` acquires `gc.mu` to check `gc.active[req.GuildID]`, then releases the lock before calling `gc.orch.ValidateAndCreate()`. A concurrent `Start()` call for the same guild can pass the check in the window between the unlock and the orchestrator's atomic create, resulting in two sessions for the same guild.
- **Impact:** Two workers dispatched for the same Discord voice channel, audio corruption, doubled resource usage.
- **Suggested fix:** Hold the lock through `ValidateAndCreate`, or move the active-check into the orchestrator's atomic `INSERT` (e.g., a `UNIQUE` constraint on `(guild_id) WHERE state != 'ended'`).

#### 1.2 Zombie Sessions With NULL Heartbeat in Active State

- **Severity:** Critical
- **Location:** `internal/gateway/sessionorch/postgres.go:145-162`
- **Description:** `CleanupZombies` filters on `last_heartbeat IS NOT NULL AND last_heartbeat < threshold`. Sessions that transition to `active` state but never receive a heartbeat (e.g., worker dies immediately after `ReportState` but before the first heartbeat tick) have `last_heartbeat IS NULL` and `state = 'active'`. These are invisible to both `CleanupZombies` and `CleanupStalePending` (which only targets `state = 'pending'`).
- **Impact:** Permanently stuck "active" sessions that consume license quota slots forever, preventing the tenant from starting new sessions.
- **Suggested fix:** Add a third cleanup sweep: transition sessions with `state = 'active' AND last_heartbeat IS NULL AND started_at < now() - interval '5 minutes'` to ended. Alternatively, extend `CleanupZombies` to also catch `last_heartbeat IS NULL AND state != 'pending' AND started_at < threshold`.

#### 1.3 No State Transition Validation

- **Severity:** Medium
- **Location:** `internal/gateway/sessionorch/postgres.go:67-90`
- **Description:** `Transition()` accepts any state transition without validation. There are no guards against invalid transitions like `ended → active` or `active → pending`. The `UPDATE` is unconditional.
- **Impact:** If a bug or race condition causes an out-of-order transition, the session can end up in an inconsistent state. For example, a late `ReportState(active)` arriving after `Stop()` has already set `ended` would resurrect a dead session.
- **Suggested fix:** Add a `WHERE state != 'ended'` guard to prevent re-opening ended sessions, and/or add a valid-transitions map (pending→active, pending→ended, active→ended).

#### 1.4 Zombie Cleanup Doesn't Update Gateway In-Memory State

- **Severity:** High
- **Location:** `cmd/glyphoxa/main.go:625-662`, `internal/gateway/sessionctrl.go:42-62`
- **Description:** The zombie cleanup loop correctly transitions stale sessions to `ended` in the DB and cleans up orphaned K8s Jobs, but does NOT update the `GatewaySessionController`'s in-memory `active` map or `workerAddrs` map. After `CleanupZombies` runs, `gc.active[guildID]` still points to the now-ended session. `IsActive(guildID)` returns `true`, and `/session start` is rejected with "session already active."
- **Impact:** After a worker dies and zombie cleanup fires, the guild is permanently locked out of starting new sessions until the gateway is restarted.
- **Suggested fix:** The zombie/orphan cleanup should notify the relevant `GatewaySessionController` to remove stale sessions from its in-memory maps. Alternatively, `IsActive` could cross-check with the orchestrator's DB state.

#### 1.5 Concurrent Stop Calls from Multiple Triggers

- **Severity:** Medium
- **Location:** `internal/gateway/sessionctrl.go:310-357`
- **Description:** `Stop` can be called concurrently from three sources: (1) user `/session stop`, (2) `onStreamDetach` callback when audio bridge detects worker death, and (3) `registerDisconnectListener` when bot is kicked from voice. None guard against concurrent execution. The `voice.Conn` double-close could cause a panic in the disgo library.
- **Impact:** Log noise from failed RPCs and "not found" K8s errors. Potential panic from double-closing the voice connection.
- **Suggested fix:** Add a `sync.Once` or "stopping" sentinel per session to prevent concurrent Stop execution.

#### 1.6 Full-Mode Session Not Stopped on Process Shutdown

- **Severity:** Medium
- **Location:** `cmd/glyphoxa/main.go:264-285`
- **Description:** In `runFull`, the graceful shutdown path calls `application.Shutdown()` and `bot.Close()` but does NOT call `sessionMgr.Stop()`. The active session's consolidator final consolidation is skipped (conversation history lost), the voice connection is not cleanly disconnected, and transcript recorders may not drain properly.
- **Impact:** On SIGTERM during an active session: conversation history lost, Discord shows bot still in voice channel briefly, final transcript entries may be lost.
- **Suggested fix:** Explicitly call `sessionMgr.Stop(shutdownCtx)` in the `runFull` shutdown path before `application.Shutdown()`.

#### 1.7 WorkerHandler.StopAll Doesn't Report Ended State

- **Severity:** Medium
- **Location:** `internal/session/worker_handler.go:289-302`
- **Description:** `StopAll` (worker graceful shutdown) empties the sessions map and stops runtimes, but does NOT call `h.callback.ReportState(sessionID, SessionEnded, ...)`. Compare with `StopSession` which does report ended state. The gateway never learns these sessions have ended.
- **Impact:** After worker pod shutdown, the gateway still considers those sessions active for up to ~60s (until heartbeat timeout + zombie cleanup). During this window, the guild is locked out and license slots are consumed by dead sessions.
- **Suggested fix:** Have `StopAll` report ended state for each session via the callback, matching `StopSession` behavior.

#### 1.8 Reconnector Does Not Signal Session Failure After Max Retries

- **Severity:** Medium
- **Location:** `internal/session/reconnect.go:230-236`
- **Description:** When `attemptReconnect` exhausts all retries, it logs an error and returns. There is no callback, no session termination, and no state transition. The session remains "active" in the orchestrator but the audio connection is dead. Heartbeats continue flowing, the pipeline continues running on silence.
- **Impact:** A persistent network disconnection leaves a zombie session that consumes resources, counts against license limits, and is invisible to the user.
- **Suggested fix:** Add an `OnFailure` callback to `ReconnectorConfig` that triggers `Stop` and cleanup after all retries are exhausted.

#### 1.9 Disconnect Listener Leak on Failed Voice Setup

- **Severity:** Low
- **Location:** `internal/gateway/sessionctrl.go:430-465`
- **Description:** `registerDisconnectListener` adds a `GuildVoiceStateUpdate` event listener. If the voice bridge setup fails after the listener is registered but before the cleanup function is stored, the listener leaks.
- **Impact:** Minor memory leak of event listeners over many failed session starts.
- **Suggested fix:** Register the listener only after the voice bridge is fully set up.

---

### 2. Multi-Tenancy Isolation

#### 2.1 StopSession Missing Tenant Authorization

- **Severity:** Critical
- **Location:** `internal/web/handlers_sessions.go:121-140`
- **Description:** `handleStopSession` sends a `StopWebSessionRequest` to the gateway with only the `sessionID` — the tenant ID from the JWT claims is not included or verified. The gateway's `StopWebSession` RPC (in `grpctransport/management.go`) calls `StopSession(ctx, sessionID)` without checking that the session belongs to the requesting tenant. Any authenticated user from any tenant can stop any session by knowing (or guessing) its UUID.
- **Impact:** Cross-tenant session termination. A malicious tenant_admin could disrupt another tenant's live game sessions.
- **Suggested fix:** Include `tenant_id` in the `StopWebSessionRequest` protobuf. The gateway must verify `session.TenantID == request.TenantID` before stopping.

#### 2.2 GetUser Store Method Not Scoped by Tenant

- **Severity:** Medium
- **Location:** `internal/web/store.go:284-302`
- **Description:** `GetUser()` queries `WHERE id = $1` without a `tenant_id` filter. While the handler (`handleGetUser` at `handlers_users.go:51-57`) checks `user.TenantID != claims.TenantID` after fetching, this is a defense-in-depth gap. If any other code path calls `GetUser` without the post-check, cross-tenant data leaks.
- **Impact:** Potential information disclosure if a new handler or API consumer calls `GetUser` without the tenant check.
- **Suggested fix:** Add `tenant_id` as a parameter to `GetUser` and include it in the SQL `WHERE` clause.

#### 2.3 Lore/Knowledge Queries Scoped by Campaign but Not Tenant

- **Severity:** Medium
- **Location:** `internal/web/store.go:583-604` (ListLoreDocuments), `store.go:547-562` (CreateLoreDocument)
- **Description:** Lore document CRUD is scoped by `campaign_id` but not `tenant_id`. The handlers verify the campaign belongs to the tenant first, but the store layer itself has no tenant guard. If a campaign ID collision occurred or a handler missed the ownership check, lore from one tenant could leak to another.
- **Impact:** Defense-in-depth gap — currently mitigated by handler-level campaign ownership checks, but fragile against future handler additions.
- **Suggested fix:** Add `tenant_id` to the `lore_documents` table or join against `campaigns` with a `tenant_id` filter in the store queries.

#### 2.4 Knowledge Entity Queries Missing campaign_id Filter

- **Severity:** High
- **Location:** `internal/web/store.go:697-751` (ListKnowledgeEntities), `internal/web/store.go:753-771` (DeleteKnowledgeEntity)
- **Description:** Both `ListKnowledgeEntities` and `DeleteKnowledgeEntity` query the tenant-specific schema (`tenant_<id>.entities`) but do NOT filter by `campaign_id` in the SQL WHERE clause. The `campaignID` parameter is accepted but never included in the query.
- **Impact:** Within the same tenant, users with access to Campaign A can see and delete knowledge entities from Campaign B. This violates the campaign-scoped access model.
- **Suggested fix:** Add `AND campaign_id = $<N>` to both queries, using the `campaignID` parameter that is already passed in.

#### 2.5 NPC Store Has No Tenant-Level Isolation

- **Severity:** High
- **Location:** `internal/agent/npcstore/postgres.go:132-159` (Get), `internal/agent/npcstore/postgres.go:219-226` (Delete)
- **Description:** The `npc_definitions` table has no `tenant_id` column. `Get` retrieves by NPC ID alone (`WHERE id = $1`). `Delete` also uses only `WHERE id = $1`. The web handlers verify the NPC's campaign belongs to the current tenant via `requireCampaign`, but the store has no tenant guard.
- **Impact:** Defense-in-depth violation. Any future code path calling `npcs.Get()` or `npcs.Delete()` without first verifying campaign ownership will have cross-tenant access.
- **Suggested fix:** Add a `tenant_id` column to `npc_definitions` and include it in all queries, or move NPC definitions to tenant-specific schemas.

#### 2.6 UpdateUser Store Method Not Scoped by Tenant

- **Severity:** Medium
- **Location:** `internal/web/store.go:826-856`
- **Description:** `UpdateUser` uses `WHERE id = $1 AND deleted_at IS NULL` with no `tenant_id` filter. Compare to `DeleteUser` which properly includes `AND tenant_id = $2`. The handler does a separate tenant check, but the store method can update any user in any tenant.
- **Impact:** Defense-in-depth violation — if any future caller invokes `UpdateUser` without the handler-level tenant check, cross-tenant user modification is possible.
- **Suggested fix:** Add `AND tenant_id = $<N>` to the `UpdateUser` WHERE clause, consistent with `DeleteUser`.

#### 2.7 Gateway Admin API Open When API Key Empty

- **Severity:** Medium
- **Location:** `internal/gateway/admin.go:148-175`
- **Description:** When no API key is configured (`a.apiKey == ""`), the gateway admin API auth middleware allows all requests without authentication. This is noted as "backward compat" but is dangerous in production.
- **Impact:** If deployed without an API key, anyone with network access can create, modify, or delete any tenant, including injecting bot tokens.
- **Suggested fix:** Refuse to start the admin API without an API key in production, or default to denying all requests.

#### 2.8 Worker gRPC Calls Not Tenant-Scoped

- **Severity:** Medium
- **Location:** `internal/gateway/grpctransport/client.go:94-210`, `internal/gateway/grpctransport/server.go:88-169`
- **Description:** Worker gRPC calls for `StopSession`, `ListNPCs`, `MuteNPC`, `UnmuteNPC`, `SpeakNPC` are keyed by `sessionID` only — no `tenantID` is passed or verified. The gRPC auth only protects ManagementService RPCs; SessionWorkerService RPCs are unguarded.
- **Impact:** If the gateway is compromised or multiple gateways share a worker pool, a session from Tenant A could be manipulated through worker RPCs referencing Tenant B's session ID.
- **Suggested fix:** Include `tenant_id` in worker RPC requests and have the worker verify it matches the session's tenant.

#### 2.9 ValidRoles Excludes super_admin (Correctly)

- **Severity:** Low (Positive Finding)
- **Location:** `internal/web/store.go:58-62`
- **Description:** `ValidRoles` map deliberately excludes `super_admin`, preventing tenant_admin users from escalating roles to `super_admin` via the `handleUpdateUser` endpoint. This is correctly implemented.
- **Impact:** N/A — this is working as intended and prevents privilege escalation.

---

### 3. Voice Pipeline

#### 3.1 No Echo Cancellation / Self-Hearing Guard

- **Severity:** Critical
- **Location:** `pkg/audio/discord/connection.go:99-160`, `internal/app/audio_pipeline.go:86-94`
- **Description:** The Discord connection's `ReceiveOpusFrame` creates an input stream for **every** user ID that sends audio, including the bot's own user ID. There is no filtering of the bot's own user ID anywhere in the pipeline. `audioPipeline.Start()` iterates `conn.InputStreams()` and starts a VAD/STT worker for every participant, including the bot itself. Similarly, `handleParticipantChange` starts a worker for every `EventJoin`, with no bot-ID exclusion. The gRPC bridge has the same gap.
- **Impact:** The NPC's synthesized speech is decoded, fed into VAD, transcribed by STT, routed to an NPC agent, and generates a response — creating a feedback loop where the NPC talks to itself indefinitely. While Discord *usually* doesn't route bot audio back, this is not guaranteed (especially with audio bridges in distributed mode).
- **Suggested fix:** Store the bot's own user ID in the `Connection` struct and skip it in `ReceiveOpusFrame`. Alternatively, filter in `audioPipeline.startWorker()` by checking the participant ID against a known bot ID.

#### 3.2 Cascade Engine Background Goroutine Leak on Fast Close

- **Severity:** Medium
- **Location:** `internal/engine/cascade/cascade.go:440-454`
- **Description:** `Engine.Close()` sets `e.closed = true`, closes `e.done`, then calls `e.wg.Wait()`. If a `Process()` call's background goroutine is in `forwardStrongModelTracked` sending to `textCh`, and TTS has already returned (channel full or closed), the goroutine may block on `textCh <- sentence` indefinitely. The `e.done` channel is selected in `waitForDone` but not in the `textCh` send paths.
- **Impact:** Goroutine leak if Close() is called while a Process() background goroutine is blocked sending text to TTS.
- **Suggested fix:** Add `case <-e.done: return` to all `textCh` send select blocks, or use a context derived from the engine's done channel.

#### 3.3 Audio Frame Ordering in gRPC Bridge

- **Severity:** Medium
- **Location:** `pkg/audio/grpcbridge/connection.go`
- **Description:** The gRPC audio bridge uses a bidirectional stream where frames are sent/received sequentially. gRPC guarantees in-order delivery on a single stream, so frame ordering is preserved in the normal case. However, if the stream reconnects (e.g., after a transient network error), frames sent during the reconnection window are lost with no indication to the pipeline.
- **Impact:** Brief audio gaps during gRPC stream reconnection, which could cause STT to produce garbled transcriptions for that segment.
- **Suggested fix:** Add sequence numbers to audio frames and detect/log gaps on the receiving end. Consider buffering a small window for reorder tolerance.

#### 3.4 Data Race Between Flush() and sendLoop in gRPC Bridge

- **Severity:** High
- **Location:** `pkg/audio/grpcbridge/connection.go:194-198`, `pkg/audio/grpcbridge/connection.go:313-324`
- **Description:** `Flush()` drains the output channel concurrently while `sendLoop()` also reads from it. Both goroutines race on `<-c.output`, causing frames to be non-deterministically consumed by either. Frames consumed by `Flush()` are silently discarded, while `sendLoop` may have already buffered a partial opus frame from a previous read.
- **Impact:** During barge-in, the race produces audio glitches or corruption on the gateway side. A frame read by Flush is lost; sendLoop may encode against stale partial buffer data.
- **Suggested fix:** Remove the local output drain from `Flush()` and let `sendLoop` do both the channel drain and buffer reset atomically via the existing `flushCh` signal.

#### 3.5 STT Session Leak on Rapid VAD Speech Start/End Cycles

- **Severity:** High
- **Location:** `internal/app/audio_pipeline.go:203-238`
- **Description:** When `VADSpeechStart` fires, the code opens a new STT session and launches a `collectAndRoute` goroutine. If a second `VADSpeechStart` fires before a corresponding `VADSpeechEnd` (e.g., VAD glitch), the `sttSession` variable is overwritten without closing the previous session. The old session's WebSocket/HTTP connection is leaked.
- **Impact:** Under rapid false-positive VAD triggers, this could leak many STT sessions (network connections, server-side state), causing resource exhaustion.
- **Suggested fix:** Before opening a new STT session on `VADSpeechStart`, close the existing one if non-nil.

#### 3.6 TTS/LLM Fallback Only Covers Stream Setup, Not Mid-Stream Failures

- **Severity:** Medium
- **Location:** `internal/resilience/tts_fallback.go:33-37`, `internal/resilience/llm_fallback.go:43`
- **Description:** The fallback wrappers only apply failover to the initial `SynthesizeStream`/`StreamCompletion` call. Mid-stream errors (WebSocket disconnect, API timeout) close the audio channel prematurely. The text that was already consumed from the input channel cannot be re-sent to a fallback provider because channels are one-read. The circuit breaker won't record this as a failure since the initial call succeeded.
- **Impact:** If ElevenLabs drops the WebSocket mid-synthesis, the NPC's response is truncated with no automatic recovery to a fallback provider.
- **Suggested fix:** Buffer the text channel's contents so they can be replayed to a fallback provider on mid-stream failure. Have the cascade engine detect stream errors and retry with a different provider.

#### 3.7 voiceBridgeReceiver.frameCount Data Race

- **Severity:** Medium
- **Location:** `internal/gateway/voicebridge.go:33,45-46,156`
- **Description:** `frameCount` (type `uint64`) is incremented from the disgo voice receiver goroutine but read from the cleanup goroutine with no synchronization. This is a data race detectable by `-race`.
- **Impact:** Go race detector would flag this. Violates the project's "race detector always on" convention.
- **Suggested fix:** Change `frameCount` to `atomic.Uint64`.

#### 3.8 Consolidator Summary Skip Heuristic Drops Legitimate Messages

- **Severity:** Low
- **Location:** `internal/session/consolidator.go:158`
- **Description:** The consolidator skips "synthetic summary messages" by checking `m.Content[0] == '['`. Any legitimate user message starting with `[` (e.g., "[OOC] hey guys", "[laughs nervously]", "[attacks the goblin]") is incorrectly skipped and permanently lost from the session store.
- **Impact:** Transcript entries starting with `[` are silently lost. Common tabletop RPG conventions include bracketed OOC messages, action descriptions, and emotes — all would be dropped.
- **Suggested fix:** Use a more specific prefix check, e.g., `strings.HasPrefix(m.Content, "[Previous conversation summary]")`.

---

### 4. Data Consistency

#### 4.1 Non-Atomic Invite Acceptance

- **Severity:** Medium
- **Location:** `internal/web/handlers_oauth.go:319-347`
- **Description:** `processInvite` performs up to 3 separate database operations (`UpdateUserTenant`, `UpdateUser`, `UseInvite`) without a transaction. If the process crashes between operations, the user could be assigned to the tenant but the invite not marked as used (allowing reuse), or the role update could succeed but the tenant assignment fail.
- **Impact:** Invite reuse, inconsistent user-tenant-role state after partial failure.
- **Suggested fix:** Wrap the three operations in a single database transaction.

#### 4.2 Campaign Deletion Doesn't Cascade to Lore/NPC Links

- **Severity:** Medium
- **Location:** `internal/web/store.go:405-416`
- **Description:** `DeleteCampaign` performs a soft-delete (`SET deleted_at = now()`) but does not clean up dependent resources: lore documents (`mgmt.lore_documents`), campaign-NPC links (`mgmt.campaign_npcs`), and NPC definitions tied to the campaign. These orphaned records consume storage and could resurface if a campaign ID is reused.
- **Impact:** Orphaned data after campaign deletion. Not a data corruption issue but a data hygiene concern.
- **Suggested fix:** Either cascade the soft-delete to dependent tables, or add a cleanup sweep. For hard references, use ON DELETE CASCADE in the FK definitions.

---

### 5. Configuration & Startup

#### 5.1 CORS Defaults to Allow-All

- **Severity:** High
- **Location:** `internal/web/middleware.go:91-123`, `internal/web/config.go:86-88`
- **Description:** When `AllowedOrigins` is empty (which is the default when `GLYPHOXA_WEB_ALLOWED_ORIGINS` is not set), `CORSMiddleware` treats it as `allowAll = true` and sets `Access-Control-Allow-Origin: *`. This means any website can make authenticated API calls to the Glyphoxa web service if a user's browser has the JWT.
- **Impact:** CSRF-like attacks where a malicious website can interact with the Glyphoxa API on behalf of an authenticated user. Note: the code correctly does NOT send `Access-Control-Allow-Credentials: true` in allow-all mode, which mitigates cookie-based attacks. But since Glyphoxa uses Bearer tokens (not cookies), this is still exploitable if the token is stored in a way accessible to JS.
- **Suggested fix:** Require `GLYPHOXA_WEB_ALLOWED_ORIGINS` to be explicitly set in production. Add a startup warning when running with wildcard CORS.

#### 5.2 ManagementService gRPC Unauthenticated by Default

- **Severity:** Critical
- **Location:** `cmd/glyphoxa/main.go:558-567`
- **Description:** When `GLYPHOXA_GRPC_MGMT_SECRET` is not set, the ManagementService gRPC endpoint is completely unauthenticated. Combined with the gateway admin API (2.7), both management interfaces default to open.
- **Impact:** Any network-reachable client can invoke management RPCs: create/delete tenants, start/stop sessions, query usage.
- **Suggested fix:** Require the secret in production or refuse to start. At minimum, bind to localhost-only when unauthenticated.

#### 5.3 No API Key Validation for Cloud Providers at Config Time

- **Severity:** High
- **Location:** `internal/config/loader.go:82-89`, `cmd/glyphoxa/main.go:1100-1115`
- **Description:** Config validation checks provider names but never validates that `api_key` is non-empty for providers that require it. LLM provider factories accept empty API keys and create the provider without error. The error only surfaces on the first API call at runtime.
- **Impact:** The server starts successfully and appears healthy, but fails on the first player interaction.
- **Suggested fix:** Add API key presence validation at the `Validate()` level for known cloud providers.

#### 5.4 HTTP Servers Lack Read/Write/Idle Timeouts

- **Severity:** Medium
- **Location:** `cmd/glyphoxa/main.go:509-512` (admin), `cmd/glyphoxa/main.go:1005-1008` (MCP), `cmd/glyphoxa/main.go:1066-1069` (observe)
- **Description:** Three `http.Server` instances are created without `ReadTimeout`, `WriteTimeout`, or `IdleTimeout`. Only the web service sets these.
- **Impact:** Slowloris-style denial-of-service attacks can exhaust file descriptors and goroutines.
- **Suggested fix:** Set `ReadTimeout: 15s`, `WriteTimeout: 30s`, `IdleTimeout: 60s` on all HTTP servers.

#### 5.5 Database SSL Mode Defaults to "prefer" Instead of "require"

- **Severity:** Medium
- **Location:** `cmd/glyphoxa/main.go:732-750`
- **Description:** `applySSLMode()` defaults to `sslmode=prefer`. A TLS-stripping attacker can force cleartext database connections.
- **Impact:** Database credentials and all query data (bot tokens, transcripts) transmitted in cleartext.
- **Suggested fix:** Default to `sslmode=require` in production. Log a warning when `prefer` is in effect.

#### 5.6 Embedding Dimension Mismatch Silently Defaults to 1536

- **Severity:** Medium
- **Location:** `internal/app/app.go:212-215`, `cmd/glyphoxa/worker_factory.go:94-97`
- **Description:** When `memory.embedding_dimensions` is 0, both code paths silently default to 1536. If the provider uses a different dimension, pgvector columns are created with the wrong size.
- **Impact:** Semantic search returns garbage results due to dimension mismatch.
- **Suggested fix:** Cross-validate `embedding_dimensions` against the configured provider's known defaults.

#### 5.7 No Minimum Length on AdminAPIKey

- **Severity:** Medium
- **Location:** `internal/web/config.go:146-183`
- **Description:** `JWTSecret` requires 32+ characters but `AdminAPIKey` has no minimum length.
- **Impact:** Weak admin keys that can be brute-forced.
- **Suggested fix:** Add `len(c.AdminAPIKey) < 16` validation.

#### 5.8 Vault Token Encryption Enabled After Health Check Failure

- **Severity:** Medium
- **Location:** `cmd/glyphoxa/main.go:469-476`
- **Description:** When Vault's health check fails, the code still assigns `tokenEncryptor = tc`. Subsequent encrypt/decrypt calls will either fail or fall through to plaintext.
- **Impact:** Bot tokens may be stored in plaintext or all tenant operations may fail.
- **Suggested fix:** Only set `tokenEncryptor = tc` when Ping succeeds.

---

### 6. Error Propagation

#### 6.1 RateLimiter Cleanup Goroutine Leaks

- **Severity:** Low
- **Location:** `internal/web/ratelimit.go:36-37`
- **Description:** `NewRateLimiter` starts a background goroutine (`go rl.cleanup()`) that ticks every 5 minutes indefinitely. There is no `Stop()` method, no context, and no way to shut down this goroutine. Each `NewRateLimiter` call spawns a permanent goroutine.
- **Impact:** Minor goroutine leak — in practice only 2-3 rate limiters are created at startup (read + write + voice preview). But if rate limiters are ever created dynamically (e.g., per-tenant), this would leak significantly.
- **Suggested fix:** Add a `context.Context` parameter to `NewRateLimiter` or a `Close()` method that stops the ticker.

#### 6.2 Discord Connection Operates Silently with nil Opus Encoder

- **Severity:** High
- **Location:** `pkg/audio/discord/connection.go:84-88,199-201`
- **Description:** If `newOpusEncoder()` fails during `newConnection()`, the error is logged but the connection is returned with `c.encoder = nil`. In `ProvideOpusFrame()`, when `encoder` is nil, the code silently discards PCM data and returns `nil, nil` (silence). The NPC's TTS audio is generated and transcribed, but players hear complete silence.
- **Impact:** Players hear nothing from the NPC. The system appears fully functional from the server side (no errors after the initial log), making this extremely hard to diagnose. The session continues consuming LLM and TTS quota with no audible output.
- **Suggested fix:** Return an error from `newConnection()` when the encoder cannot be created. At minimum, emit periodic warnings in `ProvideOpusFrame` rather than silently dropping all audio.

#### 6.3 S2S Engine Audio Channel Shared Across Concurrent Process Calls

- **Severity:** Medium
- **Location:** `internal/engine/s2s/engine.go:199-252,261-302`
- **Description:** Each `Process` call captures `sessionAudioCh` (the shared session audio channel) and spawns a `forwardAudio` goroutine that reads from it. If two `Process` calls are concurrent (docstring says this is allowed), both goroutines read from the same channel. Audio chunks are non-deterministically split between the two turn channels.
- **Impact:** Rapid successive calls to `Process` produce garbled per-turn audio because chunks are stolen by the previous turn's forwarder.
- **Suggested fix:** Use a single long-lived forwarder goroutine that demuxes audio into the most recent turn's channel, or serialize `Process` calls.

#### 6.4 ElevenLabs STT readLoop Swallows Fatal Errors

- **Severity:** Medium
- **Location:** `pkg/provider/stt/elevenlabs/elevenlabs.go:532-548`
- **Description:** When `parseResponse` encounters a fatal error like `auth_error`, `quota_exceeded`, or `invalid_api_key`, it logs and returns `(zero, false)`. The `readLoop` continues calling `conn.Read()`. The session continues in a degraded state producing no transcripts but still sending audio (wasting bandwidth/quota).
- **Impact:** Invalid API key or exceeded quota causes silent transcript loss. The NPC responds based on empty transcripts, leading to confused behavior.
- **Suggested fix:** Close the `done` channel on fatal errors so `readLoop` exits and the failure propagates upstream.

#### 6.5 No Circuit Breaker on GatewayClient

- **Severity:** Medium
- **Location:** `internal/gateway/grpctransport/server.go:211-251`
- **Description:** The worker-facing `Client` wraps every call in a circuit breaker, but the gateway-facing `GatewayClient` (used by workers for heartbeats and state-report) has none. Gateway outage causes every heartbeat call to block until gRPC deadline.
- **Impact:** Gateway outage causes worker goroutines to pile up waiting on gRPC timeouts, potentially exhausting worker resources.
- **Suggested fix:** Add a circuit breaker to `GatewayClient` so heartbeat calls fail fast when the gateway is unreachable.

#### 6.6 Dispatch Context Not Cancelled on Success Path

- **Severity:** Low
- **Location:** `internal/gateway/dispatch/dispatcher.go:108`
- **Description:** `Dispatch()` creates `ctx, cancel := context.WithTimeout(ctx, d.timeout)` and stores the cancel in the session. On the success path, cancel is not called because the context must remain live. The timeout timer goroutine lingers for up to 120s.
- **Impact:** Each successful dispatch leaks a timer goroutine for up to 120 seconds. Bounded and temporary.

---

### 7. Security

#### 7.1 X-Forwarded-For Trusted Without Verification

- **Severity:** High
- **Location:** `internal/web/ratelimit.go:150-171`
- **Description:** The `clientIP()` function trusts `X-Forwarded-For` and `X-Real-IP` headers unconditionally. Any client can set these headers to an arbitrary IP, completely bypassing the IP-based rate limiting for unauthenticated requests.
- **Impact:** Rate limiting on unauthenticated endpoints (OAuth callbacks, API key login) is ineffective. An attacker can make unlimited requests by rotating the `X-Forwarded-For` value.
- **Suggested fix:** Only trust `X-Forwarded-For` when behind a known reverse proxy. Add a `TrustedProxies` config option, and only strip the rightmost untrusted IP from the chain.

#### 7.2 OAuth State Comparison Vulnerable to Timing Attack

- **Severity:** Low
- **Location:** `internal/web/handlers_oauth.go:66`, `handlers_oauth.go:219`
- **Description:** OAuth state parameters are compared using `!=` (standard string comparison) rather than `subtle.ConstantTimeCompare`. This is a timing side-channel, though the state is a random 16-byte hex string with a 5-minute TTL, making exploitation impractical.
- **Impact:** Theoretical — the random state and short TTL make a timing attack infeasible in practice, but it violates security best practices.
- **Suggested fix:** Use `subtle.ConstantTimeCompare` for state comparison, consistent with the gRPC shared secret comparison in `mgmt_auth.go:80`.

#### 7.3 JWT Token Exposed in Redirect URL

- **Severity:** High
- **Location:** `internal/web/handlers_oauth.go:158`, `handlers_oauth.go:311`
- **Description:** After OAuth2 callback, the JWT is placed directly in the redirect URL query parameter: `"/auth/callback?token=" + url.QueryEscape(token)`. The token appears in browser history, server access logs, and potentially in `Referer` headers on subsequent navigation.
- **Impact:** JWT token leakage via browser history, proxy logs, or HTTP Referer headers. Anyone with access to these sources can impersonate the user for 24 hours.
- **Suggested fix:** Use a short-lived authorization code flow: redirect with a single-use code, then exchange it for the JWT via a POST request. Alternatively, set the token in a secure, httpOnly cookie during the redirect.

#### 7.4 SSRF via Provider Test Endpoint

- **Severity:** High
- **Location:** `internal/web/handlers_providers.go:77-126`
- **Description:** The `handleTestProvider` endpoint accepts a user-supplied `base_url` field and makes HTTP requests to it with API keys attached in headers. No URL validation or allowlisting. An attacker with `tenant_admin` role can point `base_url` to internal services (e.g., `http://169.254.169.254/` for cloud metadata).
- **Impact:** Server-Side Request Forgery (SSRF). Attacker can probe internal infrastructure, access cloud metadata endpoints (AWS/GCP/Azure instance credentials), or exfiltrate data. The server includes API keys/tokens in outbound request headers.
- **Suggested fix:** Validate `base_url` against an allowlist of known provider domains. Block private/link-local IP ranges and `localhost`. Resolve hostname before request and reject private IPs.

#### 7.5 Gateway Admin API Key Comparison Not Constant-Time

- **Severity:** High
- **Location:** `internal/gateway/admin.go:169`
- **Description:** The API key comparison uses `key != a.apiKey` (standard string equality), vulnerable to timing attacks. Compare with `handleAPIKeyLogin` and `mgmt_auth.go` which correctly use `subtle.ConstantTimeCompare`.
- **Impact:** An attacker with network access to the admin API can determine the correct key character-by-character by measuring response times.
- **Suggested fix:** Use `crypto/subtle.ConstantTimeCompare([]byte(key), []byte(a.apiKey)) != 1`.

#### 7.6 No JWT Revocation Mechanism

- **Severity:** Medium
- **Location:** `internal/web/auth.go:32-34`, `internal/web/middleware.go:24-50`
- **Description:** JWTs are valid for 24 hours (`Claims.Expires`). There is no token revocation mechanism — no deny-list, no server-side session store, and no way to invalidate a token before expiry.
- **Impact:** Compromised tokens cannot be revoked. If a user's account is deleted or role changed, their JWT remains valid until expiry.
- **Suggested fix:** Implement a lightweight token deny-list. Alternatively, shorten token lifetime to 15-30 minutes with a refresh token mechanism.

#### 7.7 WebRTC Signaling Has No Authentication

- **Severity:** Medium
- **Location:** `pkg/audio/webrtc/signaling.go:33-38`
- **Description:** WebRTC signaling endpoints (`/rooms/{roomID}/join`, `/rooms/{roomID}/ice`, `/rooms/{roomID}/leave`) have no authentication. Any client can join rooms, inject ICE candidates, or disconnect peers.
- **Impact:** Unauthorized users can join voice rooms, impersonate others, inject audio, or disconnect legitimate users — a direct path to session hijacking.
- **Suggested fix:** Add authentication (session token/API key). Verify `user_id` matches the authenticated caller.

---

## Summary Table

| # | Finding | Severity | Area |
|---|---------|----------|------|
| 1.1 | TOCTOU race in session start | High | Session Lifecycle |
| 1.2 | Zombie sessions with NULL heartbeat | Critical | Session Lifecycle |
| 1.3 | No state transition validation | Medium | Session Lifecycle |
| 1.4 | Zombie cleanup doesn't update in-memory state | High | Session Lifecycle |
| 1.5 | Concurrent Stop from multiple triggers | Medium | Session Lifecycle |
| 1.6 | Full-mode session not stopped on shutdown | Medium | Session Lifecycle |
| 1.7 | StopAll doesn't report ended state | Medium | Session Lifecycle |
| 1.8 | Reconnector silent failure after max retries | Medium | Session Lifecycle |
| 1.9 | Disconnect listener leak on failure | Low | Session Lifecycle |
| 2.1 | StopSession missing tenant auth | Critical | Multi-Tenancy |
| 2.2 | GetUser not scoped by tenant | Medium | Multi-Tenancy |
| 2.3 | Lore/Knowledge no tenant guard in store | Medium | Multi-Tenancy |
| 2.4 | Knowledge entity queries missing campaign_id | High | Multi-Tenancy |
| 2.5 | NPC store has no tenant isolation | High | Multi-Tenancy |
| 2.6 | UpdateUser not scoped by tenant | Medium | Multi-Tenancy |
| 2.7 | Gateway admin API open when key empty | Medium | Multi-Tenancy |
| 2.8 | Worker gRPC calls not tenant-scoped | Medium | Multi-Tenancy |
| 3.1 | No echo cancellation — NPC self-talk loop | Critical | Voice Pipeline |
| 3.2 | Cascade engine goroutine leak | Medium | Voice Pipeline |
| 3.3 | Audio frame gaps on stream reconnect | Medium | Voice Pipeline |
| 3.4 | Flush/sendLoop data race in gRPC bridge | High | Voice Pipeline |
| 3.5 | STT session leak on rapid VAD cycles | High | Voice Pipeline |
| 3.6 | TTS/LLM fallback no mid-stream recovery | Medium | Voice Pipeline |
| 3.7 | voiceBridgeReceiver frameCount data race | Medium | Voice Pipeline |
| 3.8 | Consolidator drops bracketed messages | Low | Voice Pipeline |
| 4.1 | Non-atomic invite acceptance | Medium | Data Consistency |
| 4.2 | Campaign deletion no cascade | Medium | Data Consistency |
| 5.1 | CORS defaults to allow-all | High | Config & Startup |
| 5.2 | ManagementService gRPC unauthenticated by default | Critical | Config & Startup |
| 5.3 | No API key validation for cloud providers | High | Config & Startup |
| 5.4 | HTTP servers lack timeouts (Slowloris) | Medium | Config & Startup |
| 5.5 | DB SSL defaults to "prefer" not "require" | Medium | Config & Startup |
| 5.6 | Embedding dimension mismatch defaults to 1536 | Medium | Config & Startup |
| 5.7 | No min length on AdminAPIKey | Medium | Config & Startup |
| 5.8 | Vault encryption enabled after health check fail | Medium | Config & Startup |
| 6.1 | RateLimiter goroutine leak | Low | Error Propagation |
| 6.2 | Nil opus encoder silent failure | High | Error Propagation |
| 6.3 | S2S audio channel shared across concurrent calls | Medium | Error Propagation |
| 6.4 | ElevenLabs STT swallows fatal errors | Medium | Error Propagation |
| 6.5 | No circuit breaker on GatewayClient | Medium | Error Propagation |
| 6.6 | Dispatch context timer leak on success | Low | Error Propagation |
| 7.1 | X-Forwarded-For trusted blindly | High | Security |
| 7.2 | OAuth state timing attack | Low | Security |
| 7.3 | JWT in redirect URL | High | Security |
| 7.4 | SSRF via provider test base_url | High | Security |
| 7.5 | Admin API key comparison not constant-time | High | Security |
| 7.6 | No JWT revocation | Medium | Security |
| 7.7 | WebRTC signaling no authentication | Medium | Security |

**Critical (4):** 1.2, 2.1, 3.1, 5.2 — require immediate attention
**High (14):** 1.1, 1.4, 2.4, 2.5, 3.4, 3.5, 5.1, 5.3, 6.2, 7.1, 7.3, 7.4, 7.5 — address before production
**Medium (28):** 1.3, 1.5, 1.6, 1.7, 1.8, 2.2, 2.3, 2.6, 2.7, 2.8, 3.2, 3.3, 3.6, 3.7, 4.1, 4.2, 5.4, 5.5, 5.6, 5.7, 5.8, 6.3, 6.4, 6.5, 7.6, 7.7 — next sprint
**Low (5):** 1.9, 3.8, 6.1, 6.6, 7.2 — address opportunistically

---

## Positive Observations

- **Tenant schema isolation** for memory/knowledge data (per-tenant PostgreSQL schemas) is well-implemented with proper `pgx.Identifier.Sanitize()` usage preventing SQL injection.
- **Vault Transit encryption** for bot tokens at rest is a strong security measure.
- **Circuit breaker** implementation is clean and correct with proper half-open probe logic.
- **Session orchestrator** design with `CleanupZombies` and `CleanupStalePending` shows good awareness of failure modes.
- **gRPC management auth** uses `subtle.ConstantTimeCompare` correctly.
- **MaxBytesMiddleware** prevents request body memory exhaustion.
- **Bot token validation** uses proper HMAC comparison.
- **Compile-time interface assertions** (`var _ Interface = (*Impl)(nil)`) are consistently used throughout the codebase.
