import { createConnectTransport } from "@connectrpc/connect-web";
import type { Interceptor } from "@connectrpc/connect";

// The browser dials Connect-JSON at /api (ADR-0015 — JSON keeps the network tab
// human-readable). In dev, Vite proxies /api → the Go web tier (vite.config.ts);
// in prod the Go binary serves the SPA and mounts the Connect handler under /api
// on the same origin, so a relative baseUrl is correct in both modes. The
// session cookie rides along automatically (same-origin); the CSRF interceptor
// adds the double-submit header.

// readCookie returns a cookie value by name, or null. Used to mirror the
// non-HttpOnly glyphoxa_csrf cookie into the request header.
function readCookie(name: string): string | null {
  const match = document.cookie.match(new RegExp("(?:^|; )" + name + "=([^;]*)"));
  return match ? decodeURIComponent(match[1]) : null;
}

// csrf is the double-submit CSRF interceptor (ADR-0016): it echoes the
// glyphoxa_csrf cookie into the X-CSRF-Token header that the server's CSRF
// interceptor checks on state-changing calls. Harmless on reads (the server
// exempts NO_SIDE_EFFECTS RPCs); required on mutations like Logout.
const csrf: Interceptor = (next) => (req) => {
  const token = readCookie("glyphoxa_csrf");
  if (token) {
    req.header.set("X-CSRF-Token", token);
  }
  return next(req);
};

export const transport = createConnectTransport({
  baseUrl: "/api",
  interceptors: [csrf],
});
