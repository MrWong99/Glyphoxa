# TanStack Router + Query + connect-query

Client routing is `@tanstack/react-router` with a code-based route tree, lazy-loaded route components for code splitting, and per-route `errorComponent` for typed Connect errors. Data fetching is TanStack Query + `@connectrpc/connect-query` — typed `useQuery` / `useMutation` hooks generated from `.proto`. One root `QueryClient` (`staleTime: 30s`, `refetchOnWindowFocus: true`).

URL shape puts the Tenant in the path (`/t/:tenantSlug/...`) so URLs are bookmarkable and two-tabs-two-tenants works. A Connect interceptor mirrors `:tenantSlug` into the `X-Tenant-Id` header (per ADR-0016) and adds the CSRF token.

SSE events for the live session call `queryClient.setQueryData(...)` to amend the cached snapshot rather than maintaining a separate React state tree. Single source of truth, survives unmount/remount.

Boot flow: mount → `GET /api/v1/auth/me` → 401 routes to `/login`; no tenants routes to `/onboarding/create-tenant`; otherwise routes to `/t/<last_or_first>`.

**Considered options:**

- **react-router 7** — rejected. Loaders/actions go unused since Connect-Query handles data; v5→v6→v7 churn pollutes the docs/recipe surface; we'd use <30% of the library.
- **wouter** — rejected. Too thin for the 15-route admin surface.
- **Hand-rolled** — rejected. The prototype's routing pattern lacks URL sync, deep-linking, and code splitting.

**Why TanStack on both sides:** same vendor as TanStack Query, same release cadence, integrated examples; one ecosystem to learn. Typed search params are a real ergonomic win for an admin tool heavy on filter URLs (audit log, transcripts, KG, sessions list).

## Amendment (2026-11-20): what is and is not implemented

Audited against the tree (`web/src/app/router.tsx`, `AuthGate.tsx`, `web/src/lib/queryClient.ts`, `transport.ts`):

**Implemented as decided.** `@tanstack/react-router` with a code-based route tree; TanStack Query + `@connectrpc/connect-query` hooks; one root `QueryClient` with `staleTime: 30s` and `refetchOnWindowFocus: true`; the Tenant in the path (`/t/$tenantSlug/...`); typed search params (`validateSearch`) for deep links; a CSRF interceptor on the Connect transport; SSE frames amending the cached snapshot through `queryClient.setQueryData` (`web/src/screens/session/useSessionEvents.ts`); an `/onboarding/create-tenant` route.

**Not implemented.**
- **Lazy-loaded route components / code splitting** — every screen is imported eagerly; there is one bundle.
- **Per-route `errorComponent`** for typed Connect errors — errors are handled inside the screens.
- **The `X-Tenant-Id` header interceptor** — the server resolves the Tenant from the session's operator binding (`auth.TenantResolver`, `internal/auth/policy.go`); the slug in the URL is not sent as a header.

**Changed.** The boot flow calls the Connect procedure `AuthService.GetCurrentUser` (not `GET /api/v1/auth/me`), and an unauthenticated result redirects to `/login` from `AuthGate`; `/t/$tenantSlug/` redirects to the Configuration screen rather than to a remembered last screen.
