# gRPC bus to gateway + SSE to browser

Real-time delivery uses two hops:

- **Hop A (Voice Instance → gateway):** behind a `Bus` interface. In-process Go channels for `all` Mode; gRPC for split Modes. Future >1-replica scale needs a shared backplane (Redis pub/sub, NATS, or pg LISTEN/NOTIFY) — flagged but not designed today.
- **Hop B (gateway → browser):** Server-Sent Events at `GET /api/v1/sessions/:id/events`. Browser auto-reconnects with `Last-Event-ID`; the gateway holds a per-session ring buffer (~500 events) for replay-on-reconnect. Initial state comes from a snapshot REST endpoint (`GET /api/v1/sessions/:id`); SSE is the live tail.

WebSocket is rejected — the browser has no bidirectional needs on the session screen ("Stop session" is a normal POST), and SSE replay-on-reconnect is simpler than the WS reconnect dance.

**Note on ADR-0005:** "no gRPC AudioBridge" was specifically about audio frames crossing process boundaries. gRPC for control/telemetry events is fine.
