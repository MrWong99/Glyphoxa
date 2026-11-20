import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { createRouterTransport } from "@connectrpc/connect";

import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { MockEventSource } from "@/test/mockEventSource";
import { useSessionEvents, type Transcript, type TranscriptLine } from "./useSessionEvents";

const line = (id: string, text: string): TranscriptLine => ({
  id,
  who: "Flo",
  kind: "npc",
  ts: "2026-11-20T20:00:00Z",
  text,
});

const snapshot = (lines: TranscriptLine[]): Transcript => ({
  lines,
  status: "live",
  typing: { active: false, label: "" },
});

// A fetch stand-in whose every call parks until the test resolves it, so a test
// can interleave SSE frames with an in-flight snapshot read.
function deferredFetch() {
  const calls: { resolve: (t: Transcript) => void; reject: (status: number) => void }[] = [];
  const fetchMock = vi.fn(
    () =>
      new Promise<Response>((res) => {
        calls.push({
          resolve: (t) => res(new Response(JSON.stringify(t), { status: 200 })),
          reject: (status) => res(new Response("", { status })),
        });
      }),
  );
  vi.stubGlobal("fetch", fetchMock);
  return { calls, fetchMock };
}

function mountHook() {
  const queryClient = makeQueryClient();
  const transport = createRouterTransport(() => {});
  const wrapper = ({ children }: { children: ReactNode }) => (
    <Providers transport={transport} queryClient={queryClient}>
      {children}
    </Providers>
  );
  return renderHook(() => useSessionEvents("s1", true), { wrapper });
}

const streams = () => MockEventSource.instances.filter((i) => !i.closed);

beforeEach(() => MockEventSource.reset());
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("useSessionEvents live tail recovery (W5)", () => {
  it("re-creates the stream with backoff once the browser gives up on it, and resyncs the snapshot", async () => {
    const { calls, fetchMock } = deferredFetch();
    const { result } = mountHook();
    await waitFor(() => expect(calls).toHaveLength(1));
    await act(async () => calls[0]!.resolve(snapshot([line("1", "hello")])));
    await waitFor(() => expect(MockEventSource.instances).toHaveLength(1));
    const first = MockEventSource.last()!;
    act(() => first.emit("open", {}));

    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    // A reconnect answered with a non-200 (deploy, expired cookie, 503): the
    // browser closes the source permanently and never retries.
    act(() => first.fail(true));
    expect(first.closed).toBe(true);
    expect(MockEventSource.instances).toHaveLength(1); // not immediately: backoff

    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });
    expect(MockEventSource.instances).toHaveLength(2);
    const second = MockEventSource.last()!;
    expect(second.url).toBe("/api/v1/sessions/s1/events");

    // The replacement fails too: the next attempt waits longer.
    act(() => second.fail(true));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });
    expect(MockEventSource.instances).toHaveLength(2);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1_000);
    });
    expect(MockEventSource.instances).toHaveLength(3);
    vi.useRealTimers();

    // Once a replacement connects, the gap it bridged is unbounded — the
    // snapshot is re-read rather than trusting the ring replay.
    const third = MockEventSource.last()!;
    expect(fetchMock).toHaveBeenCalledTimes(1);
    act(() => third.emit("open", {}));
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    await act(async () => calls[1]!.resolve(snapshot([line("1", "hello"), line("2", "missed")])));
    await waitFor(() => expect(result.current.lines.map((l) => l.text)).toEqual(["hello", "missed"]));

    // And the replacement stream is live: frames reach the cache.
    act(() => third.emit("line", line("3", "after")));
    await waitFor(() =>
      expect(result.current.lines.map((l) => l.text)).toEqual(["hello", "missed", "after"]),
    );
    expect(streams()).toHaveLength(1);
  });

  it("leaves a dropped connection the browser is already retrying alone", async () => {
    const { calls } = deferredFetch();
    mountHook();
    await waitFor(() => expect(calls).toHaveLength(1));
    await act(async () => calls[0]!.resolve(snapshot([])));
    await waitFor(() => expect(MockEventSource.instances).toHaveLength(1));
    const es = MockEventSource.last()!;
    act(() => es.emit("open", {}));

    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    act(() => es.fail(false));
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(es.closed).toBe(false);
    expect(MockEventSource.instances).toHaveLength(1);
  });

  it("cancels a pending re-creation on unmount", async () => {
    const { calls } = deferredFetch();
    const { unmount } = mountHook();
    await waitFor(() => expect(calls).toHaveLength(1));
    await act(async () => calls[0]!.resolve(snapshot([])));
    await waitFor(() => expect(MockEventSource.instances).toHaveLength(1));

    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    act(() => MockEventSource.last()!.fail(true));
    unmount();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(MockEventSource.instances).toHaveLength(1);
  });
});

describe("useSessionEvents resync (W14)", () => {
  it("keeps lines the stream delivered while a resync snapshot was in flight", async () => {
    const { calls } = deferredFetch();
    const { result } = mountHook();
    await waitFor(() => expect(calls).toHaveLength(1));
    await act(async () => calls[0]!.resolve(snapshot([line("1", "one")])));
    await waitFor(() => expect(MockEventSource.instances).toHaveLength(1));
    const es = MockEventSource.last()!;
    act(() => es.emit("open", {}));
    // A reconnect "open" triggers the resync read…
    act(() => es.emit("open", {}));
    await waitFor(() => expect(calls).toHaveLength(2));

    // …and while it is in flight the stream keeps talking: a new line, and a
    // newer revision of a line the snapshot will also carry.
    act(() => es.emit("line", line("2", "two (final)")));
    act(() => es.emit("line", line("3", "three")));
    act(() => es.emit("status", { status: "live", typing: { active: true, label: "Flo is speaking…" } }));

    // The snapshot was captured server-side BEFORE those frames.
    await act(async () => calls[1]!.resolve(snapshot([line("1", "one"), line("2", "two (par")])));

    await waitFor(() => expect(result.current.lines).toHaveLength(3));
    expect(result.current.lines.map((l) => l.text)).toEqual(["one", "two (final)", "three"]);
    expect(result.current.typing).toEqual({ active: true, label: "Flo is speaking…" });
  });
});
