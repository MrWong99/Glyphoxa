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

## Amendment: how the route-level promises are met (2026-11-20, audit sweep)

The first paragraph promised lazy-loaded route components and a per-route `errorComponent`; until this date neither existed (one 1 MB chunk, and the router's unstyled English defaults on a thrown render or an unmatched URL). They are now met as follows, and two cross-cutting behaviours are added at the same layer:

- **Code splitting is per SCREEN, not per route file.** `Configuration`, `Campaign`, `Session` and the three legal pages are `React.lazy` chunks inside their own `Suspense` boundary (so a chunk in flight leaves the shell painted); the shell warms the screen chunks shortly after it mounts. The landing/login pages and the shell stay in the entry bundle. The non-English display catalog is likewise its own chunk, fetched before the first render when it is the active language.
- **Error and not-found handling is router-level, not per-route**: `defaultErrorComponent`, `defaultNotFoundComponent` and `defaultPendingComponent` (`web/src/app/RouteFallbacks.tsx`), localized. A route may still override; none needs to today.
- **Expired sessions are handled once, in the `QueryClient`**: any read or write that returns `CodeUnauthenticated` invalidates the `GetCurrentUser` probe, and the `AuthGate` — which observes it — redirects to `/login`. The path being left is parked in `sessionStorage` and restored by the root route after the next sign-in (same-origin `/t/…` paths only; the OAuth round trip itself carries nothing).
- **Unsaved edits block navigation at the route layer.** Screens stay router-free: an editor reports dirtiness through `useDirtyGuard`, which lists it in `lib/navigationGuard`, and a `useBlocker` mounted in the tenant layout asks before a path change or a tab close discards it.
