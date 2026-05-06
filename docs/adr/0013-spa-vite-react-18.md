# Web app is a SPA (Vite + React 18)

The web app is a SPA built with Vite + React 18 + plain CSS. Go serves the built bundle from `embed.FS` in production; in development `vite dev` runs alongside `go run` with an API proxy. The single-binary deployment shape (ADR-0005) is preserved.

React 18, not 19 — the prototype is on 18, library compatibility is broader, and there is no Suspense-data-fetch surface that needs 19.

**Considered options:**

- **htmx + Go templates** — rejected. The live-session screen has too much synchronized real-time partial state across three columns; htmx would balloon partial-diff payloads and lose the prototype's local React state.
- **Next.js** — rejected. No SSR/SEO surface justifies framework weight, and Next inside a single Go binary is awkward.
