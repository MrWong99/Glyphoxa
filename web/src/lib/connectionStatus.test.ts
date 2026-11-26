import { describe, it, expect, vi } from "vitest";
import { Code, ConnectError, createClient, createRouterTransport } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { QueryClient } from "@tanstack/react-query";

import { AuthService } from "@gen/glyphoxa/management/v1/management_pb";
import { connectionStore, isUnreachableError } from "./connectionStatus";

// What counts as "nobody answered" — the banner's whole honesty depends on
// this line being drawn in the right place.

describe("isUnreachableError", () => {
  it("counts a request that never completed (fetch's TypeError, raw or wrapped by Connect)", () => {
    expect(isUnreachableError(new TypeError("Failed to fetch"))).toBe(true);
    expect(
      isUnreachableError(ConnectError.from(new TypeError("NetworkError when attempting to fetch resource."))),
    ).toBe(true);
  });

  it("counts a gateway answering for a server that is down (502/503/504 arrive as Unavailable, without a Connect body)", () => {
    const html = new Headers({ "content-type": "text/html; charset=utf-8" });
    expect(isUnreachableError(new ConnectError("HTTP 502", Code.Unavailable, html))).toBe(true);
    expect(isUnreachableError(new ConnectError("HTTP 504", Code.Unavailable))).toBe(true);
    expect(isUnreachableError(new ConnectError("the deadline passed", Code.DeadlineExceeded))).toBe(true);
  });

  it("counts the pages Cloudflare serves when the tunnel or the origin is down (530, 52x arrive as Unknown)", () => {
    // Connect maps every status but 429/502/503/504 to Unknown, so the code
    // alone would miss the most likely full outage of the hosted deployment.
    const html = new Headers({ "content-type": "text/html; charset=UTF-8" });
    expect(isUnreachableError(new ConnectError("HTTP 530", Code.Unknown, html))).toBe(true);
    expect(isUnreachableError(new ConnectError("HTTP 522", Code.Unknown, html))).toBe(true);
    expect(isUnreachableError(new ConnectError("HTTP 520", Code.Unknown))).toBe(true);
    expect(isUnreachableError(new ConnectError("HTTP 408", Code.Unknown, html))).toBe(true);
  });

  it("counts a bare 530 from a real fetch round trip", async () => {
    // What the browser actually gets from Cloudflare (error 1033): a 530
    // with an HTML page, through Connect's own response handling.
    const transport = createConnectTransport({
      baseUrl: "https://glyphoxa.example",
      fetch: async () =>
        new Response("<html>Error 1033</html>", { status: 530, headers: { "content-type": "text/html" } }),
    });
    const err = await createClient(AuthService, transport)
      .getCurrentUser({})
      .then(
        () => null,
        (e: unknown) => e,
      );
    expect(ConnectError.from(err).code).toBe(Code.Unknown);
    expect(isUnreachableError(err)).toBe(true);
  });

  it("does not count a 500 the server wrote as a Connect error body", () => {
    const json = new Headers({ "content-type": "application/json" });
    expect(isUnreachableError(new ConnectError("HTTP 500", Code.Unknown, json))).toBe(false);
    expect(isUnreachableError(new ConnectError("HTTP 530", Code.Unknown, json))).toBe(false);
    expect(isUnreachableError(new ConnectError("boom", Code.Internal, json))).toBe(false);
  });

  it("does not count a rate limit: whatever answered 429 is up", () => {
    const html = new Headers({ "content-type": "text/html" });
    expect(isUnreachableError(new ConnectError("HTTP 429", Code.Unavailable, html))).toBe(false);
  });

  it("does not count an Unavailable the server itself wrote — it answered, so it is reachable", async () => {
    // Through a real Connect round trip: the handler's error travels as a JSON
    // error body, which is what tells it apart from a proxy's error page.
    const transport = createRouterTransport(({ service }) => {
      service(AuthService, {
        getCurrentUser: () => {
          throw new ConnectError("content generation is not configured", Code.Unavailable);
        },
      });
    });
    const err = await createClient(AuthService, transport)
      .getCurrentUser({})
      .then(
        () => null,
        (e: unknown) => e,
      );
    expect(ConnectError.from(err).code).toBe(Code.Unavailable);
    expect(isUnreachableError(err)).toBe(false);
  });

  it("does not count ordinary failures", () => {
    expect(isUnreachableError(new ConnectError("no campaign", Code.NotFound))).toBe(false);
    expect(isUnreachableError(new ConnectError("not signed in", Code.Unauthenticated))).toBe(false);
    expect(isUnreachableError(new Error("a bug in a query function"))).toBe(false);
    // A TypeError from a bug is not fetch failing, raw or wrapped.
    expect(isUnreachableError(new TypeError("Cannot read properties of undefined (reading 'id')"))).toBe(false);
    expect(isUnreachableError(ConnectError.from(new TypeError("x is not a function")))).toBe(false);
    // Safari's wording of a failed fetch still counts.
    expect(isUnreachableError(ConnectError.from(new TypeError("Load failed")))).toBe(true);
    // A cancelled request (navigation, an aborted read) is not an outage.
    const aborted = new Error("The user aborted a request.");
    aborted.name = "AbortError";
    expect(isUnreachableError(ConnectError.from(aborted))).toBe(false);
  });
});

describe("connectionStore", () => {
  it("is one store per QueryClient", () => {
    const a = new QueryClient();
    const b = new QueryClient();
    expect(connectionStore(a)).toBe(connectionStore(a));
    expect(connectionStore(a)).not.toBe(connectionStore(b));
  });

  it("raises on an unanswered call, lowers on a result, and tells its subscribers only about changes", () => {
    const store = connectionStore(new QueryClient());
    const listener = vi.fn();
    const stop = store.subscribe(listener);

    expect(store.isUnreachable()).toBe(false);
    store.noteSuccess();
    expect(listener).not.toHaveBeenCalled();

    store.noteError(new TypeError("Failed to fetch"));
    expect(store.isUnreachable()).toBe(true);
    store.noteError(new TypeError("Failed to fetch"));
    expect(listener).toHaveBeenCalledTimes(1);

    store.noteSuccess();
    expect(store.isUnreachable()).toBe(false);
    expect(listener).toHaveBeenCalledTimes(2);

    stop();
    store.noteError(new TypeError("Failed to fetch"));
    expect(listener).toHaveBeenCalledTimes(2);
  });

  it("lowers on an error the server wrote, and leaves the flag alone on one that says nothing", () => {
    const store = connectionStore(new QueryClient());
    store.noteError(new TypeError("Failed to fetch"));

    // A plain Error (or a cancelled request) is no evidence either way.
    store.noteError(new Error("boom"));
    expect(store.isUnreachable()).toBe(true);

    // "Not signed in", as the server sends it: a Connect error body.
    const json = new Headers({ "content-type": "application/json" });
    store.noteError(new ConnectError("not signed in", Code.Unauthenticated, json));
    expect(store.isUnreachable()).toBe(false);
  });
});
