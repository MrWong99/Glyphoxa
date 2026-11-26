import { Code, ConnectError } from "@connectrpc/connect";
import type { QueryClient } from "@tanstack/react-query";

// "Can the web console reach Glyphoxa right now?" (#695: no offline
// indication). The browser's own online/offline flag is TanStack's
// onlineManager and needs nothing from here. This module covers the other
// half: the browser is online, but calls do not come back — the server is
// restarting, the tunnel in front of it is down, a proxy answers 502.
//
// One small store per QueryClient (so tests with their own client do not share
// a flag). The client's caches feed it (lib/queryClient.ts): a failed read
// that looks like "nobody answered" raises the flag, and anything the server
// itself answered — a result, or an error it authored — lowers it again.

/**
 * True when a failed call means "the server could not be reached", as opposed
 * to "the server answered with an error".
 *
 * Three shapes count:
 *   - the request never completed: fetch rejects with a TypeError ("Failed to
 *     fetch", "NetworkError when attempting to fetch resource.", "Load
 *     failed"), which Connect wraps (code Unknown, the TypeError as `cause`).
 *     Only that TypeError: one thrown by a bug in a query function is not an
 *     outage;
 *   - something in front of the server answered for it, with a bare HTTP
 *     status and no Connect body. Connect names such an error "HTTP <status>"
 *     and maps only 502/503/504 to Unavailable; everything else becomes
 *     Unknown. So the status itself is read: 408, 502–504, and the 52x/530
 *     pages Cloudflare serves when the tunnel or the origin behind it is
 *     down. A bare 429 is NOT counted, although Connect maps it to
 *     Unavailable: a rate limit is an answer from something that is up;
 *   - the call ran into its deadline with no response at all.
 *
 * Glyphoxa's own handlers ALSO use Unavailable and DeadlineExceeded for
 * things that are not an outage at all ("content generation is not
 * configured", a speech provider that timed out). Those arrive as a Connect
 * error body, which is always JSON — so an error that carries a JSON content
 * type is the server speaking, and proof that it is reachable.
 */
export function isUnreachableError(error: unknown): boolean {
  if (error instanceof TypeError) return isFetchFailure(error);
  const ce = ConnectError.from(error);
  if (ce.cause instanceof TypeError) return isFetchFailure(ce.cause);
  if (answeredByServer(ce)) return false;
  const status = bareHttpStatus(ce);
  if (status !== null) return UNREACHABLE_STATUS.test(String(status));
  return ce.code === Code.Unavailable || ce.code === Code.DeadlineExceeded;
}

// The statuses a gateway or tunnel answers with when the server behind it
// cannot be reached: 408, 502–504, 520–530 (Cloudflare's "origin is down" and
// "tunnel is down" pages).
const UNREACHABLE_STATUS = /^(408|50[234]|52\d|530)$/;

// bareHttpStatus: the status of an error Connect built from an HTTP response
// that carried no Connect error body (its message is then "HTTP <status>").
function bareHttpStatus(error: ConnectError): number | null {
  const match = /^HTTP (\d{3})$/.exec(error.rawMessage);
  return match ? Number(match[1]) : null;
}

// isFetchFailure: the TypeError fetch rejects with when the request never got
// an answer. Each engine words it differently; all of them say "fetch",
// "network" or "load failed".
function isFetchFailure(error: TypeError): boolean {
  return /fetch|network|load failed/i.test(error.message);
}

// answeredByServer: the error was parsed from a Connect error response, which
// only the server itself produces (a proxy's error page is HTML or empty).
function answeredByServer(error: ConnectError): boolean {
  return (error.metadata.get("content-type") ?? "").toLowerCase().startsWith("application/json");
}

export type ConnectionStore = {
  /** True while the last word from the network was "nobody answered". */
  isUnreachable: () => boolean;
  subscribe: (listener: () => void) => () => void;
  /** A call came back with a result: the server is reachable. */
  noteSuccess: () => void;
  /** A call failed: raises the flag, lowers it, or leaves it, by the kind of failure. */
  noteError: (error: unknown) => void;
};

function createConnectionStore(): ConnectionStore {
  let unreachable = false;
  const listeners = new Set<() => void>();
  const set = (next: boolean) => {
    if (unreachable === next) return;
    unreachable = next;
    for (const listener of listeners) listener();
  };
  return {
    isUnreachable: () => unreachable,
    subscribe: (listener) => {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    noteSuccess: () => set(false),
    noteError: (error) => {
      if (isUnreachableError(error)) set(true);
      // An error the server wrote is still an answer. Anything else (a
      // cancelled request, a bug in a query function) says nothing either way.
      else if (error instanceof ConnectError && answeredByServer(error)) set(false);
    },
  };
}

const stores = new WeakMap<QueryClient, ConnectionStore>();

/** The connection store that belongs to a QueryClient (created on first use). */
export function connectionStore(client: QueryClient): ConnectionStore {
  let store = stores.get(client);
  if (!store) {
    store = createConnectionStore();
    stores.set(client, store);
  }
  return store;
}
