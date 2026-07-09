import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, act, within } from "@testing-library/react";
import { createRouterTransport, ConnectError, Code } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { toast } from "sonner";

// The screen surfaces mutation failures as toasts (ADR-0017: sonner). Mock the
// module so the tests assert the surface without the portal DOM.
vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
  Toaster: () => null,
}));

import {
  SessionService,
  CampaignService,
  VoiceSessionSchema,
  GetSessionResponseSchema,
  StartSessionResponseSchema,
  StopSessionResponseSchema,
  GetActiveCampaignResponseSchema,
  CampaignSchema,
  TranscriptLineMatchSchema,
  SearchTranscriptLinesResponseSchema,
  ListSessionsResponseSchema,
  GenerateRecapResponseSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { invalidateActiveCampaignScopedQueries } from "@/lib/campaignCache";
import { MockEventSource } from "@/test/mockEventSource";
import { Session, formatElapsed, sessionRefetchInterval, SESSION_REFETCH_MS } from "./Session";

// An in-memory voice-session store served over a router transport (no network):
// active/current/last mutate in this closure so Start → invalidate → refetch
// flips the screen to Live and Stop flips it back to Idle with a last-session
// summary — the #72 acceptance (Idle → Live → Idle + elapsed timer).
function mockTransport() {
  let active = false;
  let current: ReturnType<typeof create<typeof VoiceSessionSchema>> | undefined;
  let last: ReturnType<typeof create<typeof VoiceSessionSchema>> | undefined;

  const transport = createRouterTransport(({ service }) => {
    service(SessionService, {
      getSession: () =>
        create(GetSessionResponseSchema, { session: active ? current : last, active }),
      startSession: () => {
        current = create(VoiceSessionSchema, {
          id: "vs1",
          campaignId: "c1",
          status: "running",
          startedAt: timestampFromDate(new Date()),
        });
        active = true;
        return create(StartSessionResponseSchema, { session: current });
      },
      stopSession: () => {
        const started = new Date(Date.now() - 90 * 60 * 1000); // 1h30m ago
        const ended = create(VoiceSessionSchema, {
          id: "vs1",
          campaignId: "c1",
          status: "ended",
          startedAt: timestampFromDate(started),
          endedAt: timestampFromDate(new Date()),
          lineCount: 12,
        });
        last = ended;
        active = false;
        current = undefined;
        return create(StopSessionResponseSchema, { session: ended });
      },
    });
    service(CampaignService, {
      getActiveCampaign: () =>
        create(GetActiveCampaignResponseSchema, {
          campaign: create(CampaignSchema, { id: "c1", name: "The Sunless Citadel" }),
        }),
    });
  });
  return transport;
}

function renderScreen() {
  render(
    <Providers transport={mockTransport()} queryClient={makeQueryClient()}>
      <Session />
    </Providers>,
  );
}

describe("formatElapsed", () => {
  it("formats seconds as zero-padded HH:MM:SS", () => {
    expect(formatElapsed(0)).toBe("00:00:00");
    expect(formatElapsed(65)).toBe("00:01:05");
    expect(formatElapsed(3661)).toBe("01:01:01");
    expect(formatElapsed(-5)).toBe("00:00:00");
  });
});

describe("sessionRefetchInterval (#144)", () => {
  const query = (data?: { active: boolean }) => ({ state: { data } });

  it("polls on a modest interval while a session is active", () => {
    expect(sessionRefetchInterval(query({ active: true }))).toBe(SESSION_REFETCH_MS);
    expect(SESSION_REFETCH_MS).toBeGreaterThanOrEqual(1000);
    expect(SESSION_REFETCH_MS).toBeLessThanOrEqual(15000);
  });

  it("does not poll while idle or before the first read", () => {
    expect(sessionRefetchInterval(query({ active: false }))).toBe(false);
    expect(sessionRefetchInterval(query(undefined))).toBe(false);
  });
});

describe("Session", () => {
  it("renders idle with a zeroed timer and a Start button", async () => {
    renderScreen();
    expect(await screen.findByText("Idle")).toBeInTheDocument();
    expect(screen.getByTestId("elapsed")).toHaveTextContent("00:00:00");
    expect(screen.getByRole("button", { name: /start session/i })).toBeInTheDocument();
    // The campaign name renders in the header (subtle); it loads on its own query.
    expect(await screen.findByText("The Sunless Citadel")).toBeInTheDocument();
  });

  it("reflects Idle → Live → Idle and resets the timer + shows the last summary", async () => {
    renderScreen();

    // Idle → Live: Start flips the badge and swaps in the Stop button.
    fireEvent.click(await screen.findByRole("button", { name: /start session/i }));
    expect(await screen.findByText("Live")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /stop session/i })).toBeInTheDocument();

    // Live → Idle: Stop resets to Idle, the timer zeroes, and the last-session
    // summary appears with the transcribed line count.
    fireEvent.click(screen.getByRole("button", { name: /stop session/i }));
    expect(await screen.findByText("Idle")).toBeInTheDocument();
    expect(screen.getByTestId("elapsed")).toHaveTextContent("00:00:00");
    await waitFor(() =>
      expect(screen.getByText(/12 lines transcribed/i)).toBeInTheDocument(),
    );
    expect(screen.getByRole("button", { name: /start session/i })).toBeInTheDocument();
  });
});

// liveTransport reports an already-running session so the transcript hook is
// enabled immediately (no Start click needed for the SSE assertions).
function liveTransport() {
  const current = create(VoiceSessionSchema, {
    id: "vs1",
    campaignId: "c1",
    status: "running",
    startedAt: timestampFromDate(new Date()),
  });
  return createRouterTransport(({ service }) => {
    service(SessionService, {
      getSession: () => create(GetSessionResponseSchema, { session: current, active: true }),
      startSession: () => create(StartSessionResponseSchema, { session: current }),
      stopSession: () => create(StopSessionResponseSchema, { session: current }),
    });
    service(CampaignService, {
      getActiveCampaign: () =>
        create(GetActiveCampaignResponseSchema, {
          campaign: create(CampaignSchema, { id: "c1", name: "The Sunless Citadel" }),
        }),
    });
  });
}

// endedTransport reports an already-ENDED session (active:false) with a line
// count, so a fresh mount is the reload-of-an-ended-session case (#74).
function endedTransport() {
  const started = new Date(Date.now() - 90 * 60 * 1000); // 1h30m ago
  const ended = create(VoiceSessionSchema, {
    id: "vs1",
    campaignId: "c1",
    status: "ended",
    startedAt: timestampFromDate(started),
    endedAt: timestampFromDate(new Date()),
    lineCount: 12,
  });
  return createRouterTransport(({ service }) => {
    service(SessionService, {
      getSession: () => create(GetSessionResponseSchema, { session: ended, active: false }),
      startSession: () => create(StartSessionResponseSchema, { session: ended }),
      stopSession: () => create(StopSessionResponseSchema, { session: ended }),
    });
    service(CampaignService, {
      getActiveCampaign: () =>
        create(GetActiveCampaignResponseSchema, {
          campaign: create(CampaignSchema, { id: "c1", name: "The Sunless Citadel" }),
        }),
    });
  });
}

describe("Session reload of an ended session (#74)", () => {
  it("replays persisted history from the DB-backed snapshot and shows the line count", async () => {
    // The DB-backed snapshot for an ended session: persisted lines, status idle.
    globalThis.fetch = (async () =>
      new Response(
        JSON.stringify({
          lines: [
            { id: "u:1", who: "Player / DM", kind: "player", ts: new Date().toISOString(), text: "Hello Bart" },
            { id: "a:t1", who: "Bart", tag: "NPC", kind: "npc", ts: new Date().toISOString(), text: "Well met, traveller." },
          ],
          status: "idle",
          typing: { active: false, label: "" },
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      )) as typeof fetch;

    render(
      <Providers transport={endedTransport()} queryClient={makeQueryClient()}>
        <Session />
      </Providers>,
    );

    // Idle screen, but the persisted transcript replays and the summary shows the
    // real line count — reload reads the DB, not the in-memory ring.
    expect(await screen.findByText("Idle")).toBeInTheDocument();
    expect(await screen.findByText("Hello Bart")).toBeInTheDocument();
    expect(await screen.findByText("Well met, traveller.")).toBeInTheDocument();
    expect(screen.getByText(/12 lines transcribed/i)).toBeInTheDocument();

    // No live stream is opened for an ended session.
    expect(MockEventSource.last()).toBeUndefined();
  });
});

// deadSessionTransport models issue #144's failure scenario: the loop died
// server-side but the client's cached getSession still says active. Stop then
// fails FailedPrecondition (no active session); the NEXT getSession returns the
// ended truth. getSession calls are counted so invalidation is observable.
function deadSessionTransport() {
  const started = new Date(Date.now() - 90 * 60 * 1000);
  const live = create(VoiceSessionSchema, {
    id: "vs1",
    campaignId: "c1",
    status: "running",
    startedAt: timestampFromDate(started),
  });
  const ended = create(VoiceSessionSchema, {
    id: "vs1",
    campaignId: "c1",
    status: "ended",
    startedAt: timestampFromDate(started),
    endedAt: timestampFromDate(new Date()),
    lineCount: 12,
  });
  let gets = 0;
  const transport = createRouterTransport(({ service }) => {
    service(SessionService, {
      // First read: the stale "active" the screen cached; later reads: the truth.
      getSession: () =>
        create(GetSessionResponseSchema, gets++ === 0 ? { session: live, active: true } : { session: ended, active: false }),
      startSession: () => {
        throw new ConnectError("session: Discord guild/channel not configured", Code.FailedPrecondition);
      },
      stopSession: () => {
        throw new ConnectError("session: no active voice session", Code.FailedPrecondition);
      },
    });
    service(CampaignService, {
      getActiveCampaign: () =>
        create(GetActiveCampaignResponseSchema, {
          campaign: create(CampaignSchema, { id: "c1", name: "The Sunless Citadel" }),
        }),
    });
  });
  return { transport, getSessionCalls: () => gets };
}

describe("Session mutation failures (#144)", () => {
  beforeEach(() => {
    vi.mocked(toast.error).mockClear();
    globalThis.fetch = (async () =>
      new Response(
        JSON.stringify({ lines: [], status: "live", typing: { active: true, label: "Listening to the table…" } }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      )) as typeof fetch;
  });

  it("a failing Stop surfaces the error and re-syncs the badge off Live", async () => {
    const { transport } = deadSessionTransport();
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <Session />
      </Providers>,
    );

    // The stale cache says Live.
    expect(await screen.findByText("Live")).toBeInTheDocument();

    // Stop hits ErrNoActiveSession (FailedPrecondition): the error surfaces and
    // the invalidated query refetches the ended truth — the badge leaves Live.
    fireEvent.click(screen.getByRole("button", { name: /stop session/i }));
    await waitFor(() => expect(toast.error).toHaveBeenCalled());
    expect(String(vi.mocked(toast.error).mock.calls[0][0])).toMatch(/no active voice session/i);
    expect(await screen.findByText("Idle")).toBeInTheDocument();
  });

  it("a failing Start surfaces the error and invalidates the session query", async () => {
    const { transport, getSessionCalls } = deadSessionTransport();
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <Session />
      </Providers>,
    );
    expect(await screen.findByText("Live")).toBeInTheDocument();

    // Force the idle screen (second getSession read returns ended), then Start.
    fireEvent.click(screen.getByRole("button", { name: /stop session/i }));
    fireEvent.click(await screen.findByRole("button", { name: /start session/i }));

    await waitFor(() =>
      expect(vi.mocked(toast.error).mock.calls.some(([m]) => /not configured/i.test(String(m)))).toBe(true),
    );
    // onError invalidated the session query: another getSession read happened
    // after the failing Start (reads 1: mount, 2: post-Stop; >=3: post-Start).
    await waitFor(() => expect(getSessionCalls()).toBeGreaterThanOrEqual(3));
  });
});

describe("Session live transcript (#73)", () => {
  it("recovers a transient snapshot failure on the live session (retries, doesn't blank forever) (#270 finding 1)", async () => {
    // The live/current snapshot 500s ONCE then succeeds — with staleTime Infinity and
    // no refocus refetch, a non-retried failure would strand the screen on "Listening…"
    // forever because the SSE tail only opens after the snapshot resolves.
    let calls = 0;
    globalThis.fetch = (async () => {
      calls += 1;
      if (calls === 1) return new Response("boom", { status: 500 });
      return new Response(
        JSON.stringify({ lines: [], status: "live", typing: { active: true, label: "Listening to the table…" } }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      );
    }) as typeof fetch;

    render(
      <Providers transport={liveTransport()} queryClient={makeQueryClient()}>
        <Session />
      </Providers>,
    );

    // The retry lands the snapshot → the SSE stream opens → a streamed line renders.
    const es = await waitFor(
      () => {
        const inst = MockEventSource.last();
        if (!inst) throw new Error("live stream never opened — snapshot retry not absorbed");
        return inst;
      },
      { timeout: 4000 },
    );
    act(() =>
      es.emit("line", { id: "u:1", who: "Player / DM", kind: "player", ts: new Date().toISOString(), text: "recovered live line" }),
    );
    expect(await screen.findByText("recovered live line")).toBeInTheDocument();
  });

  it("surfaces an error (not the silent empty state) when the live session's snapshot fails persistently", async () => {
    // The live/current snapshot 500s on EVERY attempt: the retry budget (retry:2)
    // absorbs transients, but a failure that outlasts it must surface — the #326
    // accepted-residual was this case stranding the screen on "Listening…",
    // masquerading as no-data.
    globalThis.fetch = (async () => new Response("boom", { status: 500 })) as typeof fetch;

    render(
      <Providers transport={liveTransport()} queryClient={makeQueryClient()}>
        <Session />
      </Providers>,
    );
    expect(await screen.findByText("Live")).toBeInTheDocument();

    // Retries exhaust (1s + 2s backoff) → the inline error replaces the empty state.
    expect(await screen.findByTestId("snapshot-error", {}, { timeout: 8000 })).toBeInTheDocument();
    expect(screen.queryByText(/transcript lines will appear here/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/start a session to capture/i)).not.toBeInTheDocument();
    // The toast fired too — same convention as the past-session surface, with the
    // current-session wording.
    await waitFor(() =>
      expect(
        vi.mocked(toast.error).mock.calls.some(([m]) => /couldn't load the session transcript/i.test(String(m))),
      ).toBe(true),
    );
    // The stream never opened (it is gated on snapshot success) — the error card
    // is the operator's signal, not a dead "Listening…".
    expect(MockEventSource.last()).toBeUndefined();
  }, 15000);

  it("seeds from the snapshot then renders streamed lines + typing dots", async () => {
    // Snapshot: live + listening, no lines yet.
    globalThis.fetch = (async () =>
      new Response(
        JSON.stringify({ lines: [], status: "live", typing: { active: true, label: "Listening to the table…" } }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      )) as typeof fetch;

    render(
      <Providers transport={liveTransport()} queryClient={makeQueryClient()}>
        <Session />
      </Providers>,
    );

    // Live badge + the listening indicator (3 dots + label) from the snapshot.
    expect(await screen.findByText("Live")).toBeInTheDocument();
    expect(await screen.findByText("Listening to the table…")).toBeInTheDocument();
    expect(screen.getByTestId("typing")).toBeInTheDocument();

    // The screen opened the live stream; drive a coalesced NPC line + a
    // "speaking" status frame and assert they render.
    const es = await waitFor(() => {
      const inst = MockEventSource.last();
      if (!inst) throw new Error("EventSource not opened yet");
      return inst;
    });
    expect(es.url).toContain("/api/v1/sessions/vs1/events");

    act(() => {
      es.emit("line", {
        id: "a:t1",
        who: "Bart",
        tag: "NPC",
        kind: "npc",
        ts: new Date().toISOString(),
        text: "Well met, traveller.",
      });
      es.emit("status", { status: "live", typing: { active: true, label: "Bart is speaking…" } });
    });

    expect(await screen.findByText("Well met, traveller.")).toBeInTheDocument();
    expect(screen.getByText("Bart")).toBeInTheDocument();
    expect(screen.getByText("NPC")).toBeInTheDocument();
    expect(await screen.findByText("Bart is speaking…")).toBeInTheDocument();
  });

  it("does not open the stream until the snapshot resolves (FIX 4)", async () => {
    // A snapshot that never resolves until we say so.
    let resolveSnap!: (r: Response) => void;
    globalThis.fetch = (() =>
      new Promise<Response>((res) => {
        resolveSnap = res;
      })) as typeof fetch;

    render(
      <Providers transport={liveTransport()} queryClient={makeQueryClient()}>
        <Session />
      </Providers>,
    );
    // Session is live, but the snapshot is still pending.
    expect(await screen.findByText("Live")).toBeInTheDocument();

    // The stream must NOT be open yet — so an SSE line can never race ahead of a
    // late snapshot that would clobber it.
    expect(MockEventSource.last()).toBeUndefined();

    // Resolve the snapshot → the stream opens → a streamed line renders.
    act(() => {
      resolveSnap(
        new Response(JSON.stringify({ lines: [], status: "live", typing: { active: false, label: "" } }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        }),
      );
    });
    const es = await waitFor(() => {
      const inst = MockEventSource.last();
      if (!inst) throw new Error("stream not opened after snapshot resolved");
      return inst;
    });
    act(() => {
      es.emit("line", {
        id: "u:1",
        who: "Player / DM",
        kind: "player",
        ts: new Date().toISOString(),
        text: "hello there",
      });
    });
    expect(await screen.findByText("hello there")).toBeInTheDocument();
  });

  it("re-syncs the snapshot on EventSource reconnect (FIX 3)", async () => {
    const snap = { lines: [], status: "live", typing: { active: false, label: "" } };
    const fetchMock = vi.fn(
      async () =>
        new Response(JSON.stringify(snap), { status: 200, headers: { "Content-Type": "application/json" } }),
    );
    globalThis.fetch = fetchMock as unknown as typeof fetch;

    render(
      <Providers transport={liveTransport()} queryClient={makeQueryClient()}>
        <Session />
      </Providers>,
    );
    await screen.findByText("Live");
    const es = await waitFor(() => {
      const inst = MockEventSource.last();
      if (!inst) throw new Error("EventSource not opened yet");
      return inst;
    });
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1)); // initial snapshot

    // First open = initial connect (no refetch); a SECOND open = reconnect, which
    // re-fetches the authoritative snapshot.
    act(() => es.emit("open", null));
    act(() => es.emit("open", null));
    await waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThanOrEqual(2));
  });
});

// failedTransport reports a terminal FAILED session (active:false) carrying an
// end_reason — the reload-of-a-failed-session case (#123): the durable status is
// the reload truth, so the screen must show Failed + the reason with no live stream.
function failedTransport() {
  const started = new Date(Date.now() - 5 * 1000);
  const failed = create(VoiceSessionSchema, {
    id: "vs1",
    campaignId: "c1",
    status: "failed",
    startedAt: timestampFromDate(started),
    endedAt: timestampFromDate(new Date()),
    endReason: "invalid_bot_token: wirenpc: open gateway: websocket: close 4004: Authentication failed",
  });
  return createRouterTransport(({ service }) => {
    service(SessionService, {
      getSession: () => create(GetSessionResponseSchema, { session: failed, active: false }),
      startSession: () => create(StartSessionResponseSchema, { session: failed }),
      stopSession: () => create(StopSessionResponseSchema, { session: failed }),
    });
    service(CampaignService, {
      getActiveCampaign: () =>
        create(GetActiveCampaignResponseSchema, {
          campaign: create(CampaignSchema, { id: "c1", name: "The Sunless Citadel" }),
        }),
    });
  });
}

describe("Session gateway connection state (#123)", () => {
  it("reflects connecting then connected during a normal start", async () => {
    globalThis.fetch = (async () =>
      new Response(
        JSON.stringify({ lines: [], status: "live", typing: { active: true, label: "Listening to the table…" } }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      )) as typeof fetch;

    render(
      <Providers transport={liveTransport()} queryClient={makeQueryClient()}>
        <Session />
      </Providers>,
    );
    await screen.findByText("Live");
    const es = await waitFor(() => {
      const inst = MockEventSource.last();
      if (!inst) throw new Error("EventSource not opened yet");
      return inst;
    });

    act(() => es.emit("connection", { state: "connecting" }));
    expect(await screen.findByTestId("connection-state")).toHaveTextContent(/connecting/i);

    act(() => es.emit("connection", { state: "connected" }));
    await waitFor(() => expect(screen.getByTestId("connection-state")).toHaveTextContent(/connected/i));
  });

  it("shows Failed with a readable reason on a fatal rejection, without a reload", async () => {
    globalThis.fetch = (async () =>
      new Response(JSON.stringify({ lines: [], status: "live", typing: { active: false, label: "" } }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      })) as typeof fetch;

    render(
      <Providers transport={liveTransport()} queryClient={makeQueryClient()}>
        <Session />
      </Providers>,
    );
    await screen.findByText("Live");
    const es = await waitFor(() => {
      const inst = MockEventSource.last();
      if (!inst) throw new Error("EventSource not opened yet");
      return inst;
    });

    act(() =>
      es.emit("connection", {
        state: "failed",
        detail: "invalid_bot_token: wirenpc: open gateway: websocket: close 4004: Authentication failed",
      }),
    );

    expect(await screen.findByText("Failed")).toBeInTheDocument();
    expect(await screen.findByTestId("connection-failed")).toHaveTextContent(/invalid_bot_token/);
  });

  it("surfaces a failed session's reason on reload from getSession (terminal truth)", async () => {
    globalThis.fetch = (async () =>
      new Response(JSON.stringify({ lines: [], status: "idle", typing: { active: false, label: "" } }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      })) as typeof fetch;

    render(
      <Providers transport={failedTransport()} queryClient={makeQueryClient()}>
        <Session />
      </Providers>,
    );

    expect(await screen.findByText("Failed")).toBeInTheDocument();
    expect(await screen.findByTestId("connection-failed")).toHaveTextContent(/invalid_bot_token/);
    // A terminal (non-active) session opens no live stream.
    expect(MockEventSource.last()).toBeUndefined();
  });
});

// searchTransport is an ended session whose persisted transcript renders, plus a
// transcript-search RPC that filters a fixed match set by substring (a stand-in
// for the server-side tsvector search). searchCalls records the debounced queries.
function searchTransport(searchCalls: string[]) {
  const started = new Date(Date.now() - 60 * 60 * 1000);
  const ended = create(VoiceSessionSchema, {
    id: "vs1",
    campaignId: "c1",
    status: "ended",
    startedAt: timestampFromDate(started),
    endedAt: timestampFromDate(new Date()),
    lineCount: 2,
  });
  const matches = [
    create(TranscriptLineMatchSchema, {
      sessionId: "vs1", lineId: "a:t1", who: "Bart", tag: "NPC", kind: "npc",
      ts: timestampFromDate(new Date()), text: "Well met, traveller.",
    }),
    create(TranscriptLineMatchSchema, {
      sessionId: "vs1", lineId: "u:1", who: "Player / DM", kind: "player",
      ts: timestampFromDate(new Date()), text: "Where is the dragon?",
    }),
    // A hit from an EARLIER session that SHARES the rendered session's line id
    // ("u:1") — relay ids restart per session, so this collision must NOT be treated
    // as in-view (would highlight the wrong line).
    create(TranscriptLineMatchSchema, {
      sessionId: "vsOld", lineId: "u:1", who: "Grumnir", kind: "npc",
      ts: timestampFromDate(new Date()), text: "old dragon mention",
    }),
  ];
  const older = create(VoiceSessionSchema, {
    id: "vsOld",
    campaignId: "c1",
    status: "ended",
    startedAt: timestampFromDate(new Date(Date.now() - 3 * 60 * 60 * 1000)),
    endedAt: timestampFromDate(new Date(Date.now() - 2 * 60 * 60 * 1000)),
    lineCount: 1,
  });
  return createRouterTransport(({ service }) => {
    service(SessionService, {
      getSession: () => create(GetSessionResponseSchema, { session: ended, active: false }),
      listSessions: () => create(ListSessionsResponseSchema, { sessions: [ended, older] }),
      searchTranscriptLines: (req) => {
        searchCalls.push(req.query);
        const q = req.query.toLowerCase();
        return create(SearchTranscriptLinesResponseSchema, {
          lines: matches.filter((m) => m.text.toLowerCase().includes(q)),
        });
      },
    });
    service(CampaignService, {
      getActiveCampaign: () =>
        create(GetActiveCampaignResponseSchema, {
          campaign: create(CampaignSchema, { id: "c1", name: "The Sunless Citadel" }),
        }),
    });
  });
}

// persistedTranscriptFetch stubs the DB-backed snapshot PER SESSION id (#270): the
// rendered session (vs1) replays its two lines; an earlier session (vsOld) — which
// SHARES the line id "u:1" — replays its OWN distinct line, so navigating to it and
// jumping proves the session-scoped deep-link, not an id collision.
function persistedTranscriptFetch() {
  const snap = (lines: unknown[]) =>
    new Response(JSON.stringify({ lines, status: "idle", typing: { active: false, label: "" } }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes("/api/v1/sessions/vsOld")) {
      return snap([
        { id: "u:1", who: "Grumnir", kind: "npc", ts: new Date().toISOString(), text: "old dragon mention" },
      ]);
    }
    return snap([
      { id: "a:t1", who: "Bart", tag: "NPC", kind: "npc", ts: new Date().toISOString(), text: "Well met, traveller." },
      { id: "u:1", who: "Player / DM", kind: "player", ts: new Date().toISOString(), text: "Where is the dragon?" },
    ]);
  }) as typeof fetch;
}

function renderSearch(searchCalls: string[] = []) {
  render(
    <Providers transport={searchTransport(searchCalls)} queryClient={makeQueryClient()}>
      <Session />
    </Providers>,
  );
}

describe("Session transcript search (#120)", () => {
  beforeEach(persistedTranscriptFetch);

  it("renders ranked matches with speaker + time for a query, no RPC before typing", async () => {
    const searchCalls: string[] = [];
    renderSearch(searchCalls);

    await screen.findByText("Idle");
    const box = screen.getByRole("searchbox", { name: /search the transcript/i });
    expect(searchCalls).toHaveLength(0);

    fireEvent.change(box, { target: { value: "dragon" } });

    const results = await screen.findByTestId("transcript-search-results");
    await waitFor(() => expect(within(results).getByText("Where is the dragon?")).toBeInTheDocument());
    expect(within(results).getByText("Player / DM")).toBeInTheDocument();
    expect(searchCalls.at(-1)).toBe("dragon");
    // A term matching only one line does not surface the other.
    expect(within(results).queryByText("Well met, traveller.")).not.toBeInTheDocument();
  });

  it("shows a graceful no-match message", async () => {
    renderSearch();
    await screen.findByText("Idle");
    fireEvent.change(screen.getByRole("searchbox", { name: /search the transcript/i }), {
      target: { value: "unicorn" },
    });
    expect(await screen.findByText(/no lines match/i)).toHaveTextContent(/unicorn/);
  });

  it("highlights the transcript line when a result is clicked (deep-link)", async () => {
    renderSearch();
    // The persisted transcript renders both lines.
    expect(await screen.findByText("Where is the dragon?")).toBeInTheDocument();

    fireEvent.change(screen.getByRole("searchbox", { name: /search the transcript/i }), {
      target: { value: "dragon" },
    });
    const results = await screen.findByTestId("transcript-search-results");
    fireEvent.click(await within(results).findByText("Where is the dragon?"));

    await waitFor(() => {
      const li = document.querySelector('[data-line-id="u:1"]');
      expect(li).toHaveAttribute("data-highlighted", "true");
    });
  });

  it("navigates to an earlier-session hit that shares a line id and highlights ITS line (#270 AC4)", async () => {
    renderSearch();
    expect(await screen.findByText("Where is the dragon?")).toBeInTheDocument();

    fireEvent.change(screen.getByRole("searchbox", { name: /search the transcript/i }), {
      target: { value: "dragon" },
    });
    const results = await screen.findByTestId("transcript-search-results");

    // Click the older-session hit whose line id ("u:1") COLLIDES with the rendered
    // session's u:1. It must navigate to that session (replay ITS transcript) and
    // highlight ITS u:1 — never the rendered session's colliding u:1.
    fireEvent.click(await within(results).findByText("old dragon mention"));
    // The older session's own line is now on screen (session-scoped snapshot).
    expect(await screen.findByText("old dragon mention")).toBeInTheDocument();
    await waitFor(() =>
      expect(document.querySelector('[data-line-id="u:1"]')).toHaveAttribute("data-highlighted", "true"),
    );
    // The highlighted u:1 is the OLDER session's line — not the rendered session's
    // colliding u:1 ("Where is the dragon?", which still shows only in the results).
    expect(document.querySelector('[data-line-id="u:1"]')).toHaveTextContent("old dragon mention");
    // A "Back to current session" affordance appears while viewing a past session.
    expect(screen.getByRole("button", { name: /back to current session/i })).toBeInTheDocument();

    // Clicking the current-session hit navigates back and highlights there.
    fireEvent.click(within(results).getByText("Where is the dragon?"));
    await waitFor(() =>
      expect(document.querySelector('[data-line-id="u:1"]')).toHaveTextContent("Where is the dragon?"),
    );
    expect(document.querySelector('[data-line-id="u:1"]')).toHaveAttribute("data-highlighted", "true");
  });

  it("fires no RPC for a whitespace box and drops results when cleared", async () => {
    const searchCalls: string[] = [];
    renderSearch(searchCalls);
    await screen.findByText("Idle");
    const box = screen.getByRole("searchbox", { name: /search the transcript/i });

    fireEvent.change(box, { target: { value: "   " } });
    await new Promise((r) => setTimeout(r, 250)); // outlast the debounce
    expect(searchCalls).toHaveLength(0);

    fireEvent.change(box, { target: { value: "dragon" } });
    await screen.findByTestId("transcript-search-results");
    fireEvent.change(box, { target: { value: "" } });
    await waitFor(() =>
      expect(screen.queryByTestId("transcript-search-results")).not.toBeInTheDocument(),
    );
  });
});

// spendCapTransport reports a live session whose spend meter has crossed a cap,
// so GetSession carries the spend-cap state + estimated spend (#130).
function spendCapTransport(state: string, estimatedSpendUsd: number) {
  const current = create(VoiceSessionSchema, {
    id: "vs1",
    campaignId: "c1",
    status: "running",
    startedAt: timestampFromDate(new Date()),
  });
  return createRouterTransport(({ service }) => {
    service(SessionService, {
      getSession: () =>
        create(GetSessionResponseSchema, {
          session: current,
          active: true,
          spendCapState: state,
          estimatedSpendUsd,
        }),
      startSession: () => create(StartSessionResponseSchema, { session: current }),
      stopSession: () => create(StopSessionResponseSchema, { session: current }),
    });
    service(CampaignService, {
      getActiveCampaign: () =>
        create(GetActiveCampaignResponseSchema, {
          campaign: create(CampaignSchema, { id: "c1", name: "The Sunless Citadel" }),
        }),
    });
  });
}

describe("Session spend cap (#130)", () => {
  beforeEach(() => {
    globalThis.fetch = (async () =>
      new Response(
        JSON.stringify({ lines: [], status: "live", typing: { active: true, label: "Listening to the table…" } }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      )) as typeof fetch;
  });

  it("renders the soft spend-cap badge with the estimated spend, labelled estimated", async () => {
    render(
      <Providers transport={spendCapTransport("soft", 3.21)} queryClient={makeQueryClient()}>
        <Session />
      </Providers>,
    );
    const badge = await screen.findByTestId("spend-cap");
    expect(badge).toHaveTextContent(/soft spend cap reached/i);
    expect(badge).toHaveTextContent(/no new agent turns/i);
    expect(badge).toHaveTextContent("$3.21");
    expect(badge).toHaveTextContent(/estimated/i);
  });

  it("renders the hard spend-cap badge when the hard cap is crossed", async () => {
    render(
      <Providers transport={spendCapTransport("hard", 10)} queryClient={makeQueryClient()}>
        <Session />
      </Providers>,
    );
    const badge = await screen.findByTestId("spend-cap");
    expect(badge).toHaveTextContent(/hard spend cap reached/i);
    expect(badge).toHaveTextContent("$10.00");
  });

  it("shows no spend-cap badge when no cap is crossed", async () => {
    render(
      <Providers transport={spendCapTransport("", 1.5)} queryClient={makeQueryClient()}>
        <Session />
      </Providers>,
    );
    expect(await screen.findByText("Live")).toBeInTheDocument();
    expect(screen.queryByTestId("spend-cap")).not.toBeInTheDocument();
  });
});

// pickerFetch stubs the DB-backed snapshot per session id for the picker tests:
// the latest session (vs1) replays two lines in seq order; an older one (vsOld)
// replays its own two distinct lines. Proves AC3 (full transcript in seq order).
function pickerFetch() {
  const snap = (lines: unknown[]) =>
    new Response(JSON.stringify({ lines, status: "idle", typing: { active: false, label: "" } }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    const url = String(input);
    if (url.includes("/api/v1/sessions/vsOld")) {
      return snap([
        { id: "u:1", who: "Player / DM", kind: "player", ts: new Date().toISOString(), text: "old first line" },
        { id: "a:t1", who: "Grumnir", tag: "NPC", kind: "npc", ts: new Date().toISOString(), text: "old second line" },
      ]);
    }
    return snap([
      { id: "u:1", who: "Player / DM", kind: "player", ts: new Date().toISOString(), text: "latest first line" },
      { id: "a:t1", who: "Bart", tag: "NPC", kind: "npc", ts: new Date().toISOString(), text: "latest second line" },
    ]);
  }) as typeof fetch;
}

// pickerTransport is an IDLE screen whose ListSessions returns two past sessions
// newest-first — the latest (12 lines) then an older one (5 lines).
function pickerTransport() {
  const latest = create(VoiceSessionSchema, {
    id: "vs1",
    campaignId: "c1",
    status: "ended",
    startedAt: timestampFromDate(new Date(Date.now() - 60 * 60 * 1000)),
    endedAt: timestampFromDate(new Date()),
    lineCount: 12,
  });
  const older = create(VoiceSessionSchema, {
    id: "vsOld",
    campaignId: "c1",
    status: "ended",
    startedAt: timestampFromDate(new Date(Date.now() - 5 * 60 * 60 * 1000)),
    endedAt: timestampFromDate(new Date(Date.now() - 4 * 60 * 60 * 1000)),
    lineCount: 5,
  });
  return createRouterTransport(({ service }) => {
    service(SessionService, {
      getSession: () => create(GetSessionResponseSchema, { session: latest, active: false }),
      listSessions: () => create(ListSessionsResponseSchema, { sessions: [latest, older] }),
    });
    service(CampaignService, {
      getActiveCampaign: () =>
        create(GetActiveCampaignResponseSchema, {
          campaign: create(CampaignSchema, { id: "c1", name: "The Sunless Citadel" }),
        }),
    });
  });
}

describe("Session past-session picker (#270)", () => {
  beforeEach(pickerFetch);

  it("lists past sessions and replays a picked session's full transcript in seq order", async () => {
    render(
      <Providers transport={pickerTransport()} queryClient={makeQueryClient()}>
        <Session />
      </Providers>,
    );

    // The picker lists both sessions, labelled with their line counts.
    const picker = await screen.findByTestId("session-picker");
    expect(within(picker).getByText(/12 lines/)).toBeInTheDocument();
    expect(within(picker).getByText(/5 lines/)).toBeInTheDocument();

    // The default view is the latest (current) session's persisted transcript.
    expect(await screen.findByText("latest first line")).toBeInTheDocument();

    // Pick the older session → its OWN transcript replays, in snapshot (seq) order.
    fireEvent.click(within(picker).getByText(/5 lines/));
    expect(await screen.findByText("old first line")).toBeInTheDocument();
    expect(screen.getByText("old second line")).toBeInTheDocument();
    const items = screen.getAllByRole("listitem").filter((li) => li.hasAttribute("data-line-id"));
    expect(items.map((li) => li.getAttribute("data-line-id"))).toEqual(["u:1", "a:t1"]);
    // The latest session's lines are no longer on screen.
    expect(screen.queryByText("latest first line")).not.toBeInTheDocument();
    // A picked PAST session opens NO live stream — pins the `active && !viewingPast`
    // gate (#270 6a): replay is snapshot-only, never an EventSource.
    expect(MockEventSource.last()).toBeUndefined();

    // Back to current returns to the latest session's transcript.
    fireEvent.click(screen.getByRole("button", { name: /back to current session/i }));
    expect(await screen.findByText("latest first line")).toBeInTheDocument();
  });

  it("surfaces an error (not the empty state) when a past session's snapshot fails to load (#270 finding 4)", async () => {
    // vs1 (current) loads fine; vsOld's snapshot 500s.
    globalThis.fetch = (async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/api/v1/sessions/vsOld")) {
        return new Response("boom", { status: 500 });
      }
      return new Response(
        JSON.stringify({
          lines: [{ id: "u:1", who: "Player / DM", kind: "player", ts: new Date().toISOString(), text: "latest first line" }],
          status: "idle",
          typing: { active: false, label: "" },
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      );
    }) as typeof fetch;

    render(
      <Providers transport={pickerTransport()} queryClient={makeQueryClient()}>
        <Session />
      </Providers>,
    );
    const picker = await screen.findByTestId("session-picker");
    fireEvent.click(within(picker).getByText(/5 lines/));

    // Inline error shows + a toast fires — never the "Start a session…" empty state.
    expect(await screen.findByTestId("snapshot-error")).toBeInTheDocument();
    expect(screen.queryByText(/start a session to capture/i)).not.toBeInTheDocument();
    await waitFor(() =>
      expect(vi.mocked(toast.error).mock.calls.some(([m]) => /couldn't load that session/i.test(String(m)))).toBe(true),
    );
  });
});

// livePickerTransport is a LIVE session whose ListSessions also carries the running
// row (labelled "live") plus a past one, to guard AC5: the live feed stays default.
function livePickerTransport() {
  const running = create(VoiceSessionSchema, {
    id: "vs1",
    campaignId: "c1",
    status: "running",
    startedAt: timestampFromDate(new Date()),
  });
  const older = create(VoiceSessionSchema, {
    id: "vsOld",
    campaignId: "c1",
    status: "ended",
    startedAt: timestampFromDate(new Date(Date.now() - 5 * 60 * 60 * 1000)),
    endedAt: timestampFromDate(new Date(Date.now() - 4 * 60 * 60 * 1000)),
    lineCount: 5,
  });
  return createRouterTransport(({ service }) => {
    service(SessionService, {
      getSession: () => create(GetSessionResponseSchema, { session: running, active: true }),
      listSessions: () => create(ListSessionsResponseSchema, { sessions: [running, older] }),
      startSession: () => create(StartSessionResponseSchema, { session: running }),
      stopSession: () => create(StopSessionResponseSchema, { session: running }),
    });
    service(CampaignService, {
      getActiveCampaign: () =>
        create(GetActiveCampaignResponseSchema, {
          campaign: create(CampaignSchema, { id: "c1", name: "The Sunless Citadel" }),
        }),
    });
  });
}

describe("Session live default unchanged with picker present (#270 AC5)", () => {
  beforeEach(pickerFetch);

  it("keeps the live feed as the default and returns to it after browsing a past session", async () => {
    render(
      <Providers transport={livePickerTransport()} queryClient={makeQueryClient()}>
        <Session />
      </Providers>,
    );

    // Live is the default: Live badge + a "Live transcript" heading, the running
    // row in the picker is labelled "live".
    expect(await screen.findByText("Live")).toBeInTheDocument();
    expect(screen.getByText("Live transcript")).toBeInTheDocument();
    const picker = await screen.findByTestId("session-picker");
    expect(within(picker).getByText(/· live/)).toBeInTheDocument();

    // The live stream opens and a streamed line renders (unchanged live behavior).
    const es = await waitFor(() => {
      const inst = MockEventSource.last();
      if (!inst) throw new Error("EventSource not opened yet");
      return inst;
    });
    act(() =>
      es.emit("line", { id: "u:1", who: "Player / DM", kind: "player", ts: new Date().toISOString(), text: "live line here" }),
    );
    expect(await screen.findByText("live line here")).toBeInTheDocument();

    // Browse a past session → the heading switches away from Live transcript.
    const esCountBeforeBrowse = MockEventSource.instances.length;
    fireEvent.click(within(picker).getByText(/5 lines/));
    expect(await screen.findByText("Session transcript")).toBeInTheDocument();
    expect(await screen.findByText("old first line")).toBeInTheDocument();
    // Pin the `active && !viewingPast` gate (#270 6b): browsing a past session opens
    // NO new stream — not for vsOld, not at all. A broken gate would open ES(vsOld).
    expect(MockEventSource.instances.length).toBe(esCountBeforeBrowse);
    expect(MockEventSource.instances.some((i) => i.url.includes("vsOld"))).toBe(false);

    // Back to current → the live feed is the default again AND its stream reopens:
    // a newly streamed line renders (guards AC5 that returning resumes the live tail,
    // #270 6b — not a frozen snapshot).
    fireEvent.click(screen.getByRole("button", { name: /back to current session/i }));
    expect(await screen.findByText("Live transcript")).toBeInTheDocument();
    const es2 = await waitFor(() => {
      const inst = MockEventSource.last();
      if (!inst || inst === es) throw new Error("live stream not reopened after returning");
      return inst;
    });
    act(() =>
      es2.emit("line", { id: "a:t2", who: "Bart", tag: "NPC", kind: "npc", ts: new Date().toISOString(), text: "back on the live tail" }),
    );
    expect(await screen.findByText("back on the live tail")).toBeInTheDocument();
  });
});

// switchTransport is a MUTABLE two-campaign transport: flipping `state.campaign`
// then invalidating the Active-Campaign-scoped caches models the topbar campaign
// switch. Campaign A has a current + an older session; B has its own current.
function switchTransport(state: { campaign: "A" | "B" }) {
  const mk = (id: string, lineCount: number, status = "ended") =>
    create(VoiceSessionSchema, {
      id,
      campaignId: state.campaign === "A" ? "cA" : "cB",
      status,
      startedAt: timestampFromDate(new Date(Date.now() - 60 * 60 * 1000)),
      endedAt: timestampFromDate(new Date()),
      lineCount,
    });
  const aCurrent = mk("vsA", 3);
  const aOlder = create(VoiceSessionSchema, {
    id: "vsAold",
    campaignId: "cA",
    status: "ended",
    startedAt: timestampFromDate(new Date(Date.now() - 5 * 60 * 60 * 1000)),
    endedAt: timestampFromDate(new Date(Date.now() - 4 * 60 * 60 * 1000)),
    lineCount: 9,
  });
  const bCurrent = mk("vsB", 7);
  return createRouterTransport(({ service }) => {
    service(SessionService, {
      getSession: () =>
        create(GetSessionResponseSchema, {
          session: state.campaign === "A" ? aCurrent : bCurrent,
          active: false,
        }),
      listSessions: () =>
        create(ListSessionsResponseSchema, {
          sessions: state.campaign === "A" ? [aCurrent, aOlder] : [bCurrent],
        }),
    });
    service(CampaignService, {
      getActiveCampaign: () =>
        create(GetActiveCampaignResponseSchema, {
          campaign: create(CampaignSchema, {
            id: state.campaign === "A" ? "cA" : "cB",
            name: state.campaign === "A" ? "Campaign A" : "Campaign B",
          }),
        }),
    });
  });
}

describe("Session past-session view across a campaign switch (#270 finding 1)", () => {
  beforeEach(() => {
    globalThis.fetch = (async (input: RequestInfo | URL) => {
      const url = String(input);
      const line = (text: string) => ({
        lines: [{ id: "u:1", who: "Player / DM", kind: "player", ts: new Date().toISOString(), text }],
        status: "idle",
        typing: { active: false, label: "" },
      });
      const text = url.includes("/vsAold")
        ? "A archived line"
        : url.includes("/vsB")
          ? "B current line"
          : "A current line";
      return new Response(JSON.stringify(line(text)), { status: 200, headers: { "Content-Type": "application/json" } });
    }) as typeof fetch;
  });

  it("resets the viewed past session so the new campaign never renders the old one's transcript", async () => {
    const state: { campaign: "A" | "B" } = { campaign: "A" };
    const qc = makeQueryClient();
    render(
      <Providers transport={switchTransport(state)} queryClient={qc}>
        <Session />
      </Providers>,
    );

    // In campaign A, browse the older (archived) session.
    const picker = await screen.findByTestId("session-picker");
    fireEvent.click(within(picker).getByText(/9 lines/));
    expect(await screen.findByText("A archived line")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /back to current session/i })).toBeInTheDocument();

    // Switch to campaign B and run the Active-Campaign sweep (topbar switch).
    state.campaign = "B";
    await act(async () => {
      void invalidateActiveCampaignScopedQueries(qc);
    });

    // The view resets: B's current transcript renders, A's archived line is gone,
    // and the "back to current" affordance (viewingPast) is cleared.
    expect(await screen.findByText("B current line")).toBeInTheDocument();
    expect(screen.queryByText("A archived line")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /back to current session/i })).not.toBeInTheDocument();
  });
});

// --- Recap (#274) -----------------------------------------------------------

// recapStartISO is the fixed start instant of the idle ended session the recap
// tests cover; the result card's label is derived from it (formatStamp).
const recapStartISO = "2026-07-08T20:15:00Z";

// recapStampLabel replicates the screen's formatStamp for the covered session, so
// the label assertion is timezone-consistent with the render (both run in this env).
function recapStampLabel(iso: string): string {
  return new Date(iso).toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

// recapTransport serves an IDLE ended session (vs1) plus a configurable
// GenerateRecap handler, recording the session_ids each call requested. sessions
// seeds the past-session picker (empty by default).
function recapTransport(
  recap: (req: { sessionIds: string[] }) => Promise<ReturnType<typeof create<typeof GenerateRecapResponseSchema>>>,
  sessions: ReturnType<typeof create<typeof VoiceSessionSchema>>[] = [],
) {
  const ended = create(VoiceSessionSchema, {
    id: "vs1",
    campaignId: "c1",
    status: "ended",
    startedAt: timestampFromDate(new Date(recapStartISO)),
    endedAt: timestampFromDate(new Date("2026-07-08T21:00:00Z")),
    lineCount: 12,
  });
  const requested: string[][] = [];
  const transport = createRouterTransport(({ service }) => {
    service(SessionService, {
      getSession: () => create(GetSessionResponseSchema, { session: ended, active: false }),
      listSessions: () => create(ListSessionsResponseSchema, { sessions }),
      generateRecap: async (req) => {
        requested.push(req.sessionIds);
        return await recap(req);
      },
    });
    service(CampaignService, {
      getActiveCampaign: () =>
        create(GetActiveCampaignResponseSchema, {
          campaign: create(CampaignSchema, { id: "c1", name: "The Sunless Citadel" }),
        }),
    });
  });
  return { transport, requested };
}

describe("Session recap (#274)", () => {
  beforeEach(() => {
    vi.mocked(toast.error).mockClear();
    // Idle snapshot for the covered session(s): no lines needed for the recap tests.
    globalThis.fetch = (async () =>
      new Response(JSON.stringify({ lines: [], status: "idle", typing: { active: false, label: "" } }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      })) as typeof fetch;
  });

  it("recaps the latest ended session: pending spinner + disabled, then a labelled result card", async () => {
    // Gate the recap so the pending state is observable (a real call can take ~2min).
    let release!: (r: ReturnType<typeof create<typeof GenerateRecapResponseSchema>>) => void;
    const gate = new Promise<ReturnType<typeof create<typeof GenerateRecapResponseSchema>>>((res) => {
      release = res;
    });
    const { transport, requested } = recapTransport(() => gate);

    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <Session />
      </Providers>,
    );

    const button = await screen.findByTestId("recap-button");
    fireEvent.click(button);

    // Pending: spinner shown, button disabled, no result yet.
    expect(await screen.findByTestId("recap-pending")).toBeInTheDocument();
    expect(screen.getByTestId("recap-button")).toBeDisabled();
    expect(screen.queryByTestId("recap-result")).not.toBeInTheDocument();

    // The mutation carried exactly the covered session id.
    expect(requested).toEqual([["vs1"]]);

    // Resolve → result card with the recap prose, labelled by the covered session's start.
    await act(async () => {
      release(create(GenerateRecapResponseSchema, { text: "The party bested the goblin warren.", sessionIds: ["vs1"], windowed: false }));
    });
    const result = await screen.findByTestId("recap-result");
    expect(result).toHaveTextContent("The party bested the goblin warren.");
    expect(result).toHaveTextContent(recapStampLabel(recapStartISO));
    expect(screen.queryByTestId("recap-pending")).not.toBeInTheDocument();
  });

  it("recaps the picked past session (its id, not the current one)", async () => {
    const older = create(VoiceSessionSchema, {
      id: "vsOld",
      campaignId: "c1",
      status: "ended",
      startedAt: timestampFromDate(new Date(Date.now() - 5 * 60 * 60 * 1000)),
      endedAt: timestampFromDate(new Date(Date.now() - 4 * 60 * 60 * 1000)),
      lineCount: 5,
    });
    const latest = create(VoiceSessionSchema, {
      id: "vs1",
      campaignId: "c1",
      status: "ended",
      startedAt: timestampFromDate(new Date(recapStartISO)),
      endedAt: timestampFromDate(new Date("2026-07-08T21:00:00Z")),
      lineCount: 12,
    });
    const { transport, requested } = recapTransport(
      async () => create(GenerateRecapResponseSchema, { text: "Recap of the older delve.", sessionIds: ["vsOld"], windowed: true }),
      [latest, older],
    );

    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <Session />
      </Providers>,
    );

    // Browse the older past session, then recap it.
    const picker = await screen.findByTestId("session-picker");
    fireEvent.click(within(picker).getByText(/5 lines/));
    fireEvent.click(await screen.findByTestId("recap-button"));

    await waitFor(() => expect(requested).toEqual([["vsOld"]]));
    expect(await screen.findByTestId("recap-result")).toHaveTextContent("Recap of the older delve.");
  });

  it("surfaces a recap failure as a toast (ADR-0017 sonner), no result card", async () => {
    const { transport } = recapTransport(async () => {
      throw new ConnectError("recap: no active campaign", Code.FailedPrecondition);
    });

    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <Session />
      </Providers>,
    );

    fireEvent.click(await screen.findByTestId("recap-button"));
    await waitFor(() =>
      expect(vi.mocked(toast.error).mock.calls.some(([m]) => /couldn't generate the recap/i.test(String(m)))).toBe(true),
    );
    expect(screen.queryByTestId("recap-result")).not.toBeInTheDocument();
  });

  it("clears a shown recap when the viewed session changes", async () => {
    const older = create(VoiceSessionSchema, {
      id: "vsOld",
      campaignId: "c1",
      status: "ended",
      startedAt: timestampFromDate(new Date(Date.now() - 5 * 60 * 60 * 1000)),
      endedAt: timestampFromDate(new Date(Date.now() - 4 * 60 * 60 * 1000)),
      lineCount: 5,
    });
    const latest = create(VoiceSessionSchema, {
      id: "vs1",
      campaignId: "c1",
      status: "ended",
      startedAt: timestampFromDate(new Date(recapStartISO)),
      endedAt: timestampFromDate(new Date("2026-07-08T21:00:00Z")),
      lineCount: 12,
    });
    const { transport } = recapTransport(
      async () => create(GenerateRecapResponseSchema, { text: "A recap of the current session.", sessionIds: ["vs1"], windowed: false }),
      [latest, older],
    );

    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <Session />
      </Providers>,
    );

    // Recap the default (current) session → result shows.
    fireEvent.click(await screen.findByTestId("recap-button"));
    expect(await screen.findByTestId("recap-result")).toBeInTheDocument();

    // Browse a past session → the stale recap is dropped (it labelled the current one).
    const picker = screen.getByTestId("session-picker");
    fireEvent.click(within(picker).getByText(/5 lines/));
    await waitFor(() => expect(screen.queryByTestId("recap-result")).not.toBeInTheDocument());
  });

  it("does NOT render an in-flight recap that resolves after the session changed", async () => {
    const older = create(VoiceSessionSchema, {
      id: "vsOld",
      campaignId: "c1",
      status: "ended",
      startedAt: timestampFromDate(new Date(Date.now() - 5 * 60 * 60 * 1000)),
      endedAt: timestampFromDate(new Date(Date.now() - 4 * 60 * 60 * 1000)),
      lineCount: 5,
    });
    const latest = create(VoiceSessionSchema, {
      id: "vs1",
      campaignId: "c1",
      status: "ended",
      startedAt: timestampFromDate(new Date(recapStartISO)),
      endedAt: timestampFromDate(new Date("2026-07-08T21:00:00Z")),
      lineCount: 12,
    });
    // Gate the recap so it is still in flight when we switch sessions.
    let release!: (r: ReturnType<typeof create<typeof GenerateRecapResponseSchema>>) => void;
    const gate = new Promise<ReturnType<typeof create<typeof GenerateRecapResponseSchema>>>((res) => {
      release = res;
    });
    const { transport } = recapTransport(() => gate, [latest, older]);

    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <Session />
      </Providers>,
    );

    // Recap the current (vs1) session; while it is pending, switch to the past one.
    fireEvent.click(await screen.findByTestId("recap-button"));
    expect(await screen.findByTestId("recap-pending")).toBeInTheDocument();
    const picker = screen.getByTestId("session-picker");
    fireEvent.click(within(picker).getByText(/5 lines/));

    // Now the gated vs1 recap resolves — but vs1 is no longer on screen, so its
    // result must be discarded, never rendered above vsOld's transcript.
    await act(async () => {
      release(create(GenerateRecapResponseSchema, { text: "Stale recap of vs1.", sessionIds: ["vs1"], windowed: false }));
    });
    await waitFor(() => expect(screen.queryByTestId("recap-pending")).not.toBeInTheDocument());
    expect(screen.queryByTestId("recap-result")).not.toBeInTheDocument();
    expect(screen.queryByText("Stale recap of vs1.")).not.toBeInTheDocument();
  });
});

describe("Session zero-line live snapshot (#248)", () => {
  it("survives a snapshot with null lines and renders the live badge", async () => {
    // The pre-#248 relay (or any older server) serves "lines":null for a live
    // session with no transcript yet; the screen must normalize it, not crash
    // into the error boundary on lines.length.
    globalThis.fetch = (async () =>
      new Response(
        JSON.stringify({
          lines: null,
          status: "live",
          typing: { active: true, label: "Listening to the table…" },
          connection: "connected",
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      )) as typeof fetch;

    render(
      <Providers transport={liveTransport()} queryClient={makeQueryClient()}>
        <Session />
      </Providers>,
    );

    expect(await screen.findByText("Live")).toBeInTheDocument();
  });
});
