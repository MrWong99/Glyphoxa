# Buf Connect end-to-end RPC surface

Buf Connect is the structured RPC surface across the system. One set of `.proto` files is the source of truth for **both** browser↔gateway and Voice Instance↔gateway. `connect-go` serves Connect (JSON-over-HTTP), gRPC, and gRPC-Web from the same handler; Voice dials gRPC; the browser dials Connect-JSON so the network tab stays human-readable. `protoc-gen-es` + `protoc-gen-connect-es` produce a typed TypeScript client — no hand-written types, no zod, no drift.

Service granularity is small and focused:

- `glyphoxa.management.v1.TenantService` — orgs, members, audit
- `glyphoxa.management.v1.CampaignService` — Campaigns, NPCs, KG
- `glyphoxa.management.v1.SessionService` — snapshot RPCs (live events stay on SSE per ADR-0014)
- `glyphoxa.management.v1.ProviderService` — keys, models, test-call
- `glyphoxa.voice.v1.VoiceControlService` — voice↔gateway: `claim_session`, `release_session`, `push_event`

Carve-outs (plain `net/http`, outside Connect): SSE event stream, OAuth callbacks, file uploads. Connect server-streaming RPCs lack EventSource semantics (`Last-Event-ID`, proxy-compat); OAuth is HTML redirects; multipart is the right shape for uploads.

## Addendum (#65): connect-es v2 merges the TS generators

This ADR's "`protoc-gen-es` + `protoc-gen-connect-es`" wording predates Connect-ES v2. As of `@connectrpc/connect` v2 the separate `protoc-gen-connect-es` generator is **gone** — `protoc-gen-es` v2 now bakes the service descriptors into the message file (`*_pb.ts`), and the browser client is constructed at runtime with `createClient(CampaignService, transport)` rather than a generated `*PromiseClient`. The first codegen slice (#65) therefore wires a single TS plugin, `buf.build/bufbuild/es:v2`, instead of the v1 two-plugin pair. The decision is unchanged in substance — one `.proto` source of truth, a fully typed client, no hand-written types or zod — only the generator topology moved upstream. The Go side is unaffected (`connectrpc/go` is the connect-go generator).

