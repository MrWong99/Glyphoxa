# Web app is a SPA (Vite + React 18)

The web app is a SPA built with Vite + React 18 + plain CSS. Go serves the built bundle from `embed.FS` in production; in development `vite dev` runs alongside `go run` with an API proxy. The single-binary deployment shape (ADR-0005) is preserved.

React 18, not 19 — the prototype is on 18, library compatibility is broader, and there is no Suspense-data-fetch surface that needs 19.

**Considered options:**

- **htmx + Go templates** — rejected. The live-session screen has too much synchronized real-time partial state across three columns; htmx would balloon partial-diff payloads and lose the prototype's local React state.
- **Next.js** — rejected. No SSR/SEO surface justifies framework weight, and Next inside a single Go binary is awkward.

## Amendment (2026-11-20): React 19

The SPA runs **React 19** (`web/package.json`: `react`/`react-dom` `^19.3.1`). The upgrade landed in the dependency refresh of #656 once the libraries the console uses (TanStack Router/Query, connect-query, Radix) supported 19; none of the reasons above for staying on 18 still held. Nothing else in this decision changes: a Vite-built SPA, served by Go from `embed.FS`, `vite dev` with an API proxy in development. The title keeps "React 18" so existing links stay valid.
