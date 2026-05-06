# Buf Connect end-to-end RPC surface

Buf Connect is the structured RPC surface across the system. One set of `.proto` files is the source of truth for **both** browser↔gateway and Voice Instance↔gateway. `connect-go` serves Connect (JSON-over-HTTP), gRPC, and gRPC-Web from the same handler; Voice dials gRPC; the browser dials Connect-JSON so the network tab stays human-readable. `protoc-gen-es` + `protoc-gen-connect-es` produce a typed TypeScript client — no hand-written types, no zod, no drift.

Service granularity is small and focused:

- `glyphoxa.management.v1.TenantService` — orgs, members, audit
- `glyphoxa.management.v1.CampaignService` — Campaigns, NPCs, KG
- `glyphoxa.management.v1.SessionService` — snapshot RPCs (live events stay on SSE per ADR-0014)
- `glyphoxa.management.v1.ProviderService` — keys, models, test-call
- `glyphoxa.voice.v1.VoiceControlService` — voice↔gateway: `claim_session`, `release_session`, `push_event`

Carve-outs (plain `net/http`, outside Connect): SSE event stream, OAuth callbacks, file uploads. Connect server-streaming RPCs lack EventSource semantics (`Last-Event-ID`, proxy-compat); OAuth is HTML redirects; multipart is the right shape for uploads.
