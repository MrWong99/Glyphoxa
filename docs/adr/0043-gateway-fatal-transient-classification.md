# Gateway fatal-vs-transient classification and the connection-state taxonomy

Implementing #123 (E6) required deciding what counts as a *fatal* Discord gateway failure, how a fatal failure is persisted and surfaced, and what the connection-state event vocabulary is. The operator delegated these decisions to the implementation run (2026-07-07); this ADR records them.

## What this decides

- **Classification is by typed error, at the reconnect loop.** `internal/wirenpc` gains a `classifyFatal(err)` step inside `runWithReconnect`: gorilla `*websocket.CloseError` 4004 → `invalid_bot_token`, 4013/4014 → `disallowed_intents`, 4010–4012 → `gateway_rejected`; disgo `*rest.Error` HTTP 401 → `invalid_bot_token`, 403 → `bot_not_authorized`. Everything else (429, 5xx, network errors) stays transient and keeps the existing bounded backoff. Validated against disgo v0.19.6: an invalid token surfaces from `OpenGateway` as a wrapped close 4004 (the `/gateway` preflight is unauthenticated), and disgo does not internally retry non-reconnectable closes.
- **`end_reason` is a stable machine prefix plus prose**: `"invalid_bot_token: gateway rejected identify (close 4004: …)"`. The prefix set (`invalid_bot_token`, `disallowed_intents`, `bot_not_authorized`, `gateway_rejected`, `loop_error`, and `spend_cap_hard` from ADR-0046) is greppable and UI-renderable; the suffix stays human. Any non-context loop error — not just classified-fatal ones — closes the row as `failed` (prefix `loop_error:`), so no failure mode can leave a row `running`.
- **Connection state joins the shared voice event taxonomy (ADR-0020)** as `ConnectionStateChanged` with states `connecting` / `connected` / `failed` (event name `connection.state`). The SSE relay forwards it as a `connection` frame; live reload truth is the relay snapshot, terminal reload truth is `GetSession` (status `failed` + `end_reason`).
- **No new gRPC error mapping on Start.** Session start is asynchronous; a fatal rejection lands seconds later. The failure reaches the operator via the SSE `connection` frame and the persisted `failed` row, not a Start error.
- **Storage closes sessions through one seam.** `CloseVoiceSession(id, status, lineCount, endReason)` replaces `EndVoiceSession` as the session-manager contract (`EndVoiceSession` remains as a delegating wrapper); `VoiceSessionStatus` gains `failed`. No migration: status is unconstrained text and `end_reason` already exists.
- **Accepted behavior change:** env-only voice mode now *exits* on an invalid token instead of retrying forever. In Kubernetes this is a crashloop-with-backoff, which is the correct operational signal; the #44 resilience work targeted transient drops, and an invalid token is not transient.

## Considered and rejected

- **String-matching error text** — brittle across disgo versions; typed `errors.As` chains survive `%w` wrapping through both client paths (per-cycle acquire and presence provider).
- **Machine-code-only `end_reason`** — the Session screen would need a client-side catalogue; prefix+prose gives both audiences one field.
- **Mapping fatal failures onto the Start RPC** — wrong shape for an async failure; would race the gateway handshake.

## Relationship to other ADRs

ADR-0020 (taxonomy gains `connection.state`), ADR-0014 (frame rides the SSE ring), ADR-0039 (Session screen consumes it), ADR-0031 (no schema migration needed).
