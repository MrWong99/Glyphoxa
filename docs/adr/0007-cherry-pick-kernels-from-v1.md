# Cherry-pick kernels from v1, rewrite the rest

Inherit only tightly-scoped, hard-to-rewrite kernels from v1:

- `internal/dave` — DAVE/MLS binding (libdave CGo)
- `pkg/provider` — Provider interfaces (implementations re-derived to the matrix in ADR-0004)
- `internal/mcp` — MCP host
- VAD wrapper — Silero/ONNX Runtime CGo

Everything else is rewritten fresh in small reviewable steps.

**Why:** most of v1 was AI-generated and parts don't work. The kernels above are mechanical adapters where rewrite has no payoff and the failure modes (wrong audio framing, DAVE state machine bugs) are subtle and well-shaken-out in v1. Rewriting the rest preserves the option to fix architectural choices that v1 baked in.
