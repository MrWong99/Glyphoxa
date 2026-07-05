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
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
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
  ];
  return createRouterTransport(({ service }) => {
    service(SessionService, {
      getSession: () => create(GetSessionResponseSchema, { session: ended, active: false }),
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

// persistedTranscriptFetch stubs the DB-backed snapshot so the ended session's
// transcript renders (needed for the click-to-highlight deep-link).
function persistedTranscriptFetch() {
  globalThis.fetch = (async () =>
    new Response(
      JSON.stringify({
        lines: [
          { id: "a:t1", who: "Bart", tag: "NPC", kind: "npc", ts: new Date().toISOString(), text: "Well met, traveller." },
          { id: "u:1", who: "Player / DM", kind: "player", ts: new Date().toISOString(), text: "Where is the dragon?" },
        ],
        status: "idle",
        typing: { active: false, label: "" },
      }),
      { status: 200, headers: { "Content-Type": "application/json" } },
    )) as typeof fetch;
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
