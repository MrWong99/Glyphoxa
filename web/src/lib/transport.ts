import { createConnectTransport } from "@connectrpc/connect-web";

// The browser dials Connect-JSON at /api (ADR-0015 — JSON keeps the network tab
// human-readable). In dev, Vite proxies /api → the Go web tier (vite.config.ts);
// in prod the Go binary serves the SPA and mounts the Connect handler under /api
// on the same origin, so a relative baseUrl is correct in both modes.
export const transport = createConnectTransport({
  baseUrl: "/api",
});
