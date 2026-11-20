// Where to go back to after a re-login. When a session lapses mid-work the
// AuthGate sends the operator to /login; the OAuth round trip then lands on "/"
// (internal/auth/oauth.go appRedirect), which used to dump them on the default
// screen. The path they were on is parked in sessionStorage — same tab, same
// origin, never on the wire and never in a URL the server or Discord sees — and
// RootEntry picks it up once the session probe says they are signed in again.
//
// The value is treated as hostile on the way OUT of storage (anything on the
// origin can write sessionStorage): only a same-origin path inside the app
// shell (/t/…) is ever returned, so this cannot be turned into an open
// redirect, and a stale entry expires instead of teleporting a later visit.

const KEY = "gx-return-to";
const MAX_AGE_MS = 15 * 60 * 1000;

/**
 * safeReturnPath reduces an arbitrary string to a same-origin, in-app path
 * ("/t/…", with its query and hash), or null when it is anything else — an
 * absolute or protocol-relative URL, a backslash trick, a path outside the
 * shell.
 */
export function safeReturnPath(raw: unknown): string | null {
  if (typeof raw !== "string" || !raw.startsWith("/") || raw.startsWith("//") || raw.includes("\\")) {
    return null;
  }
  let url: URL;
  try {
    url = new URL(raw, window.location.origin);
  } catch {
    return null;
  }
  if (url.origin !== window.location.origin || !url.pathname.startsWith("/t/")) return null;
  return url.pathname + url.search + url.hash;
}

/** Parks the current in-app location for the next successful sign-in. */
export function rememberReturnTo(path: string): void {
  const safe = safeReturnPath(path);
  if (!safe) return;
  try {
    sessionStorage.setItem(KEY, JSON.stringify({ path: safe, at: Date.now() }));
  } catch {
    // storage unavailable — the operator lands on the default screen instead
  }
}

/** Returns the parked path once (and clears it), or null. */
export function consumeReturnTo(): string | null {
  try {
    const raw = sessionStorage.getItem(KEY);
    if (raw === null) return null;
    sessionStorage.removeItem(KEY);
    const parsed = JSON.parse(raw) as { path?: unknown; at?: unknown };
    if (typeof parsed.at !== "number" || Date.now() - parsed.at > MAX_AGE_MS) return null;
    return safeReturnPath(parsed.path);
  } catch {
    return null;
  }
}
