import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, act, within } from "@testing-library/react";
import { createRouterTransport, ConnectError, Code } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { toast } from "sonner";

// The screen surfaces mutation failures as toasts (ADR-0017: sonner). Mock the
// module so the tests assert the surface without the portal DOM.
vi.mock("sonner", () => ({
  toast: Object.assign(vi.fn(), { error: vi.fn(), success: vi.fn(), warning: vi.fn(), dismiss: vi.fn() }),
  Toaster: () => null,
}));

import {
  SessionService,
  CampaignService,
  ProviderService,
  VoiceSessionSchema,
  GetSessionResponseSchema,
  StartSessionResponseSchema,
  StopSessionResponseSchema,
  GetActiveCampaignResponseSchema,
  CampaignSchema,
  TranscriptLineMatchSchema,
  SearchTranscriptLinesResponseSchema,
  ListSessionsResponseSchema,
  ListHighlightsResponseSchema,
  HighlightSchema,
  PromoteHighlightResponseSchema,
  DeleteHighlightResponseSchema,
  GenerateRecapResponseSchema,
  ListSessionVoiceChannelsResponseSchema,
  VoiceChannelSchema,
  SaveDiscordSettingsResponseSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { invalidateMethodQueries, makeQueryClient } from "@/lib/queryClient";
import { invalidateActiveCampaignScopedQueries } from "@/lib/campaignCache";
import { MockEventSource } from "@/test/mockEventSource";
import { formatElapsed, sessionRefetchInterval, SESSION_REFETCH_MS } from "./Session";
import { Screen } from "./testScreen";

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
      listHighlights: () => create(ListHighlightsResponseSchema, { highlights: [] }),
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
      <Screen />
    </Providers>,
  );
}

// snapshotFetch stubs the transcript snapshot endpoint with one fixed body.
function snapshotFetch(body: unknown): typeof fetch {
  return (async () =>
    new Response(JSON.stringify(body), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })) as typeof fetch;
}

// The suite's default snapshot is a live session whose Bot IS in the channel
// (connection "connected"), so a test about something else reads "Live". The
// badge's own Connecting → Live behaviour has its tests further down, which
// serve a snapshot without a connection state — what the relay sends before
// the gateway's first transition.
const CONNECTED = { lines: [], status: "live", connection: "connected", typing: { active: false, label: "" } };
beforeEach(() => {
  vi.mocked(toast).mockClear();
  vi.mocked(toast.error).mockClear();
  vi.mocked(toast.warning).mockClear();
  globalThis.fetch = snapshotFetch(CONNECTED);
});

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
  it("renders idle with no timer and a Start button", async () => {
    renderScreen();
    expect(await screen.findByText("Idle")).toBeInTheDocument();
    // Idle hides the clock — a frozen 00:00:00 reads as a stuck timer.
    expect(screen.queryByTestId("elapsed")).toBeNull();
    expect(screen.getByRole("button", { name: /start session/i })).toBeInTheDocument();
    // The campaign name renders in the header (subtle); it loads on its own query.
    expect(await screen.findByText("The Sunless Citadel")).toBeInTheDocument();
  });

  it("reflects Idle → Live → Idle and resets the timer + shows the last summary", async () => {
    renderScreen();

    // Idle → Live: Start flips the badge and swaps in the Stop button. Start is
    // held disabled while the channel list loads, so wait for it to enable
    // (here the list read fails fast — the fakes serve no channels).
    const startBtn = await screen.findByRole("button", { name: /start session/i });
    await waitFor(() => expect(startBtn).toBeEnabled());
    fireEvent.click(startBtn);
    expect(await screen.findByText("Live")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /stop session/i })).toBeInTheDocument();
    expect(screen.getByTestId("elapsed")).toBeInTheDocument();

    // Live → Idle: Stop resets to Idle, the timer disappears, and the last-session
    // summary appears with the transcribed line count. The session has run for
    // seconds, so Stop does not ask first.
    fireEvent.click(screen.getByRole("button", { name: /stop session/i }));
    expect(await screen.findByText("Idle")).toBeInTheDocument();
    expect(screen.queryByTestId("elapsed")).toBeNull();
    await waitFor(() =>
      expect(screen.getByText(/12 lines transcribed/i)).toBeInTheDocument(),
    );
    expect(screen.getByRole("button", { name: /start session/i })).toBeInTheDocument();
  });

  it("announces the end of a session once, offering the recap", async () => {
    renderScreen();
    const startBtn = await screen.findByRole("button", { name: /start session/i });
    await waitFor(() => expect(startBtn).toBeEnabled());
    fireEvent.click(startBtn);
    fireEvent.click(await screen.findByRole("button", { name: /stop session/i }));
    expect(await screen.findByText("Idle")).toBeInTheDocument();

    await waitFor(() => expect(toast).toHaveBeenCalledTimes(1));
    const [message, options] = vi.mocked(toast).mock.calls[0];
    expect(message).toBe("Session ended.");
    expect((options as { action: { label: string } }).action.label).toBe("Generate recap");
  });

  it("hands keyboard focus to Stop when Start is replaced by it", async () => {
    renderScreen();
    const startBtn = await screen.findByRole("button", { name: /start session/i });
    await waitFor(() => expect(startBtn).toBeEnabled());
    startBtn.focus();
    fireEvent.click(startBtn);
    const stopBtn = await screen.findByRole("button", { name: /stop session/i });
    // The busy Start button was disabled (focus fell to <body>) and then
    // unmounted; the button that took its place has the focus.
    await waitFor(() => expect(stopBtn).toHaveFocus());
  });
});

// A session that has run for an hour and a half: long enough that Stop asks.
function longRunningTransport(stops: { count: number }) {
  const current = create(VoiceSessionSchema, {
    id: "vs1",
    campaignId: "c1",
    status: "running",
    startedAt: timestampFromDate(new Date(Date.now() - 90 * 60 * 1000)),
  });
  return createRouterTransport(({ service }) => {
    service(SessionService, {
      listHighlights: () => create(ListHighlightsResponseSchema, { highlights: [] }),
      getSession: () => create(GetSessionResponseSchema, { session: current, active: true }),
      stopSession: () => {
        stops.count += 1;
        return create(StopSessionResponseSchema, { session: current });
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

describe("Session stop confirmation", () => {
  it("asks before stopping a session that has run for more than a minute, naming how long", async () => {
    const stops = { count: 0 };
    render(
      <Providers transport={longRunningTransport(stops)} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );
    fireEvent.click(await screen.findByRole("button", { name: /stop session/i }));

    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("Stop the session?");
    expect(dialog).toHaveTextContent(/running for 1h 30m/);
    // Nothing was stopped by the first click.
    expect(stops.count).toBe(0);

    // "Keep playing" backs out without stopping.
    fireEvent.click(within(dialog).getByRole("button", { name: /keep playing/i }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
    expect(stops.count).toBe(0);

    // Confirming stops it.
    fireEvent.click(screen.getByRole("button", { name: /stop session/i }));
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: /stop session/i }));
    await waitFor(() => expect(stops.count).toBe(1));
  });

  it("shows the estimated spend beside the clock whenever there is one", async () => {
    render(
      <Providers transport={spendCapTransport("", 1.5)} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );
    expect(await screen.findByTestId("spend-inline")).toHaveTextContent("Estimated spend $1.50");
  });
});

/** Confirms the Stop dialog a long-running session raises. */
async function confirmStop() {
  fireEvent.click(await screen.findByRole("button", { name: /stop session/i }));
  fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: /stop session/i }));
}

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
      listHighlights: () => create(ListHighlightsResponseSchema, { highlights: [] }),
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
      listHighlights: () => create(ListHighlightsResponseSchema, { highlights: [] }),
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
        <Screen />
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

  it("renders leaked markdown inline on Agent lines but keeps human lines verbatim", async () => {
    globalThis.fetch = (async () =>
      new Response(
        JSON.stringify({
          lines: [
            { id: "u:1", who: "Player / DM", kind: "player", ts: new Date().toISOString(), text: "I said **loudly** hello" },
            { id: "a:t1", who: "Bart", tag: "NPC", kind: "npc", ts: new Date().toISOString(), text: "Beware the **red** dragon." },
          ],
          status: "idle",
          typing: { active: false, label: "" },
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      )) as typeof fetch;

    render(
      <Providers transport={endedTransport()} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );

    // The Agent (npc) line's emphasis renders as a real <strong>…
    await screen.findByText("Idle");
    await waitFor(() =>
      expect(document.querySelector('[data-line-id="a:t1"] .gx-line__text strong')?.textContent).toBe("red"),
    );
    // …while the human (player) line — STT output — stays literal: markdown is
    // disabled there so transcribed asterisks can never reformat spoken words.
    expect(screen.getByText("I said **loudly** hello")).toBeInTheDocument();
    expect(document.querySelector('[data-line-id="u:1"] .gx-line__text strong')).toBeNull();
  });

  it("renders expressive-TTS audio tags on Agent lines as a stage direction, spaced from the words", async () => {
    globalThis.fetch = (async () =>
      new Response(
        JSON.stringify({
          lines: [
            { id: "a:t1", who: "Flo", tag: "NPC", kind: "npc", ts: new Date().toISOString(), text: "[cheerfully]Hier ist dein Wurf" },
            { id: "u:1", who: "Player / DM", kind: "player", ts: new Date().toISOString(), text: "[laughs] stays literal" },
          ],
          status: "idle",
          typing: { active: false, label: "" },
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      )) as typeof fetch;

    render(
      <Providers transport={endedTransport()} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );

    await screen.findByText("Idle");
    const npcText = await waitFor(() => {
      const el = document.querySelector('[data-line-id="a:t1"] .gx-line__text');
      if (!el) throw new Error("line not rendered yet");
      return el as HTMLElement;
    });
    expect(npcText.querySelector('[data-testid="audio-tag"]')?.textContent).toBe("cheerfully");
    // The spoken words follow the direction after a space — no raw bracket text.
    expect(npcText.textContent).toBe("cheerfully Hier ist dein Wurf");
    // Human STT lines stay verbatim.
    expect(screen.getByText("[laughs] stays literal")).toBeInTheDocument();
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
      listHighlights: () => create(ListHighlightsResponseSchema, { highlights: [] }),
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
        JSON.stringify({ lines: [], status: "live", connection: "connected", typing: { active: true, label: "Listening to the table…" } }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      )) as typeof fetch;
  });

  it("a failing Stop surfaces the error and re-syncs the badge off Live", async () => {
    const { transport } = deadSessionTransport();
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );

    // The stale cache says Live.
    expect(await screen.findByText("Live")).toBeInTheDocument();

    // Stop hits ErrNoActiveSession (FailedPrecondition): the error surfaces and
    // the invalidated query refetches the ended truth — the badge leaves Live.
    await confirmStop();
    await waitFor(() => expect(toast.error).toHaveBeenCalled());
    expect(String(vi.mocked(toast.error).mock.calls[0][0])).toMatch(/no active voice session/i);
    expect(await screen.findByText("Idle")).toBeInTheDocument();
  });

  it("a failing Start surfaces the error and invalidates the session query", async () => {
    const { transport, getSessionCalls } = deadSessionTransport();
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );
    expect(await screen.findByText("Live")).toBeInTheDocument();

    // Force the idle screen (second getSession read returns ended), then Start.
    await confirmStop();
    const startBtn = await screen.findByRole("button", { name: /start session/i });
    await waitFor(() => expect(startBtn).toBeEnabled());
    fireEvent.click(startBtn);

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
        JSON.stringify({ lines: [], status: "live", connection: "connected", typing: { active: true, label: "Listening to the table…" } }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      );
    }) as typeof fetch;

    render(
      <Providers transport={liveTransport()} queryClient={makeQueryClient()}>
        <Screen />
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
        <Screen />
      </Providers>,
    );
    // With no snapshot the relay has reported nothing: the badge does not claim Live.
    expect(await screen.findByTestId("connection-state")).toHaveTextContent("Connecting…");

    // Retries exhaust (1s + 2s backoff) → the inline error replaces the empty
    // state, with a way to try again.
    const error = await screen.findByTestId("snapshot-error", {}, { timeout: 8000 });
    expect(within(error).getByRole("button", { name: /try again/i })).toBeInTheDocument();
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
        JSON.stringify({ lines: [], status: "live", connection: "connected", typing: { active: true, label: "Listening to the table…" } }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      )) as typeof fetch;

    render(
      <Providers transport={liveTransport()} queryClient={makeQueryClient()}>
        <Screen />
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
      // A relay that predates the structured typing state: only the label.
      es.emit("status", { status: "live", typing: { active: true, label: "Bart is speaking…" } });
    });

    expect(await screen.findByText("Well met, traveller.")).toBeInTheDocument();
    expect(screen.getByText("Bart")).toBeInTheDocument();
    expect(screen.getByText("NPC")).toBeInTheDocument();
    expect(await screen.findByText("Bart is speaking…")).toBeInTheDocument();
  });

  it("renders the typing line and the relay's own labels in German from the structured state (#695)", async () => {
    localStorage.setItem("gx-lang", "de");
    globalThis.fetch = snapshotFetch({
      lines: [],
      status: "live",
      connection: "connected",
      typing: { active: true, label: "Listening to the table…", state: "listening" },
    });
    render(
      <Providers transport={liveTransport()} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );

    // "listening" is a code, so the sentence is the catalog's — not the relay's English.
    expect(await screen.findByText("Hört der Runde zu…")).toBeInTheDocument();
    expect(screen.queryByText("Listening to the table…")).not.toBeInTheDocument();

    const es = await waitFor(() => {
      const inst = MockEventSource.last();
      if (!inst) throw new Error("EventSource not opened yet");
      return inst;
    });
    act(() => {
      es.emit("line", { id: "u:1", who: "Player / DM", kind: "player", ts: new Date().toISOString(), text: "Hallo Bart" });
      es.emit("line", { id: "a:t1", who: "Bart", tag: "NPC", kind: "npc", ts: new Date().toISOString(), text: "Seid gegrüßt." });
      es.emit("status", {
        status: "live",
        typing: { active: true, label: "Bart is speaking…", state: "speaking", who: "Bart" },
      });
    });

    // The unnamed human lane is the relay's label, not a name: it is localized.
    expect(await screen.findByText("Spieler / GM")).toBeInTheDocument();
    expect(screen.queryByText("Player / DM")).not.toBeInTheDocument();
    // "{who} is speaking" in German; the name itself is data and stays.
    expect(await screen.findByText("Bart spricht…")).toBeInTheDocument();
    localStorage.clear();
  });

  it("falls back to the relay's English label for a typing state it does not know", async () => {
    globalThis.fetch = snapshotFetch({
      lines: [],
      status: "live",
      connection: "connected",
      typing: { active: true, label: "Thinking it over…", state: "thinking" },
    });
    render(
      <Providers transport={liveTransport()} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );
    expect(await screen.findByText("Thinking it over…")).toBeInTheDocument();
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
        <Screen />
      </Providers>,
    );
    // The session is running, but the snapshot is still pending.
    expect(await screen.findByRole("button", { name: /stop session/i })).toBeInTheDocument();

    // The stream must NOT be open yet — so an SSE line can never race ahead of a
    // late snapshot that would clobber it.
    expect(MockEventSource.last()).toBeUndefined();

    // Resolve the snapshot → the stream opens → a streamed line renders.
    act(() => {
      resolveSnap(
        new Response(JSON.stringify({ lines: [], status: "live", connection: "connected", typing: { active: false, label: "" } }), {
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
    const snap = { lines: [], status: "live", connection: "connected", typing: { active: false, label: "" } };
    const fetchMock = vi.fn(
      async () =>
        new Response(JSON.stringify(snap), { status: 200, headers: { "Content-Type": "application/json" } }),
    );
    globalThis.fetch = fetchMock as unknown as typeof fetch;

    render(
      <Providers transport={liveTransport()} queryClient={makeQueryClient()}>
        <Screen />
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

// failedTransport reports a terminal session (active:false) carrying an
// end_reason: a FAILED one by default — the reload-of-a-failed-session case
// (#123), where the durable status is the reload truth — or an "ended" one that
// stopped by itself.
function failedTransport(
  endReason = "invalid_bot_token: wirenpc: open gateway: websocket: close 4004: Authentication failed",
  status: "failed" | "ended" = "failed",
) {
  const started = new Date(Date.now() - 45 * 60 * 1000);
  const failed = create(VoiceSessionSchema, {
    id: "vs1",
    campaignId: "c1",
    status,
    startedAt: timestampFromDate(started),
    endedAt: timestampFromDate(new Date()),
    lineCount: 4,
    endReason,
  });
  return createRouterTransport(({ service }) => {
    service(SessionService, {
      listHighlights: () => create(ListHighlightsResponseSchema, { highlights: [] }),
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
  it("reads Connecting until the relay reports the gateway connected, then Live", async () => {
    // What the relay serves before the gateway's first transition: no
    // connection state at all.
    globalThis.fetch = snapshotFetch({
      lines: [],
      status: "live",
      typing: { active: true, label: "Listening to the table…" },
    });

    render(
      <Providers transport={liveTransport()} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );
    // The session is running, but "Live" would be a claim that the Bot is in
    // the channel — and nothing has said so yet.
    expect(await screen.findByTestId("connection-state")).toHaveTextContent("Connecting…");
    expect(screen.queryByText("Live")).not.toBeInTheDocument();

    const es = await waitFor(() => {
      const inst = MockEventSource.last();
      if (!inst) throw new Error("EventSource not opened yet");
      return inst;
    });
    act(() => es.emit("connection", { state: "connecting" }));
    expect(screen.getByTestId("connection-state")).toHaveTextContent("Connecting…");

    act(() => es.emit("connection", { state: "connected" }));
    expect(await screen.findByText("Live")).toBeInTheDocument();
    expect(screen.queryByTestId("connection-state")).not.toBeInTheDocument();
  });

  it("takes a transcript line as proof of a connection the relay never reported", async () => {
    globalThis.fetch = snapshotFetch({
      lines: [{ id: "u:1", who: "Player / DM", kind: "player", ts: new Date().toISOString(), text: "we are in" }],
      status: "live",
      typing: { active: false, label: "" },
    });
    render(
      <Providers transport={liveTransport()} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );
    expect(await screen.findByText("Live")).toBeInTheDocument();
  });

  it("shows Failed with a readable reason on a fatal rejection, without a reload", async () => {
    globalThis.fetch = (async () =>
      new Response(JSON.stringify({ lines: [], status: "live", connection: "connected", typing: { active: false, label: "" } }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      })) as typeof fetch;

    render(
      <Providers transport={liveTransport()} queryClient={makeQueryClient()}>
        <Screen />
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
    // A known code is a sentence with the next step — and the way there.
    const banner = await screen.findByTestId("connection-failed");
    expect(banner).toHaveTextContent(/Discord rejected the Bot token\. Paste a current token under Setup/);
    expect(banner).not.toHaveTextContent(/websocket: close 4004/);
    expect(within(banner).getByRole("button", { name: "Open Setup" })).toBeInTheDocument();
  });

  it("surfaces a failed session's reason on reload from getSession (terminal truth)", async () => {
    globalThis.fetch = snapshotFetch({ lines: [], status: "idle", typing: { active: false, label: "" } });

    render(
      <Providers transport={failedTransport()} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );

    expect(await screen.findByText("Failed")).toBeInTheDocument();
    expect(await screen.findByTestId("connection-failed")).toHaveTextContent(/Discord rejected the Bot token/);
    // A terminal (non-active) session opens no live stream.
    expect(MockEventSource.last()).toBeUndefined();
  });

  it("shows a reason it does not know exactly as the server wrote it", async () => {
    globalThis.fetch = snapshotFetch({ lines: [], status: "idle", typing: { active: false, label: "" } });
    render(
      <Providers
        transport={failedTransport("loop_error: wirenpc: encoder: opus: buffer too small")}
        queryClient={makeQueryClient()}
      >
        <Screen />
      </Providers>,
    );
    const banner = await screen.findByTestId("connection-failed");
    expect(banner).toHaveTextContent("loop_error: wirenpc: encoder: opus: buffer too small");
    // Not one of the causes fixed on the Setup screen: no link that leads nowhere.
    expect(within(banner).queryByRole("button", { name: "Open Setup" })).not.toBeInTheDocument();
  });

  it("says why a session that ended by itself ended, under the last-session line", async () => {
    globalThis.fetch = snapshotFetch({ lines: [], status: "idle", typing: { active: false, label: "" } });
    render(
      <Providers
        transport={failedTransport(
          "idle_no_audio: the Voice Session processed no audio for the idle close window",
          "ended",
        )}
        queryClient={makeQueryClient()}
      >
        <Screen />
      </Providers>,
    );
    // An ended session is Idle, not Failed — the reason is a line, not an alarm.
    expect(await screen.findByText("Idle")).toBeInTheDocument();
    expect(screen.getByText(/Last session ended/)).toBeInTheDocument();
    expect(await screen.findByTestId("end-reason")).toHaveTextContent(
      "It ended by itself because nobody spoke for a while.",
    );
    expect(screen.queryByTestId("connection-failed")).not.toBeInTheDocument();
  });

  it("localizes the reason line", async () => {
    localStorage.setItem("gx-lang", "de");
    globalThis.fetch = snapshotFetch({ lines: [], status: "idle", typing: { active: false, label: "" } });
    render(
      <Providers transport={failedTransport("spend_cap_hard: estimated spend crossed the hard cap", "ended")} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );
    expect(await screen.findByTestId("end-reason")).toHaveTextContent(/hartes Ausgabenlimit erreicht/);
    localStorage.clear();
  });
});

// autoEndTransport is a LIVE session that the server ends by itself: the next
// getSession read after `state.ended` flips says idle, with an end_reason.
function autoEndTransport(state: { ended: boolean }) {
  const started = new Date(Date.now() - 40 * 60 * 1000);
  const running = create(VoiceSessionSchema, {
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
    lineCount: 9,
    endReason: "idle_no_audio: the Voice Session processed no audio for the idle close window",
  });
  return createRouterTransport(({ service }) => {
    service(SessionService, {
      listHighlights: () => create(ListHighlightsResponseSchema, { highlights: [] }),
      getSession: () =>
        create(GetSessionResponseSchema, state.ended ? { session: ended, active: false } : { session: running, active: true }),
    });
    service(CampaignService, {
      getActiveCampaign: () =>
        create(GetActiveCampaignResponseSchema, {
          campaign: create(CampaignSchema, { id: "c1", name: "The Sunless Citadel" }),
        }),
    });
  });
}

describe("Session ended by the server", () => {
  it("announces live → idle once, with the reason", async () => {
    const state = { ended: false };
    const qc = makeQueryClient();
    render(
      <Providers transport={autoEndTransport(state)} queryClient={qc}>
        <Screen />
      </Providers>,
    );
    expect(await screen.findByText("Live")).toBeInTheDocument();

    // The idle watchdog closes the session; the screen learns it from its poll.
    state.ended = true;
    await act(async () => {
      await invalidateMethodQueries(qc, SessionService.method.getSession);
    });
    expect(await screen.findByText("Idle")).toBeInTheDocument();

    await waitFor(() => expect(toast.warning).toHaveBeenCalledTimes(1));
    expect(String(vi.mocked(toast.warning).mock.calls[0][0])).toBe(
      "The session ended. It ended by itself because nobody spoke for a while.",
    );
    // A later refetch of the same idle state is not a second announcement.
    await act(async () => {
      await invalidateMethodQueries(qc, SessionService.method.getSession);
    });
    expect(toast.warning).toHaveBeenCalledTimes(1);
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
      listHighlights: () => create(ListHighlightsResponseSchema, { highlights: [] }),
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
      <Screen />
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
    // The relay's unnamed-speaker label is localized, here as in the transcript.
    expect(within(results).getByText("Player / GM")).toBeInTheDocument();
    expect(searchCalls.at(-1)).toBe("dragon");
    // A term matching only one line does not surface the other.
    expect(within(results).queryByText("Well met, traveller.")).not.toBeInTheDocument();
  });

  it("groups the hits under the session they were said in, with its date and a count", async () => {
    renderSearch();
    await screen.findByText("Idle");
    fireEvent.change(screen.getByRole("searchbox", { name: /search the transcript/i }), {
      target: { value: "dragon" },
    });

    const results = await screen.findByTestId("transcript-search-results");
    // Two hits, in two different sessions.
    expect(within(results).getByRole("status")).toHaveTextContent("2 matching lines in 2 sessions");
    const groups = within(results).getAllByTestId("search-group");
    expect(groups).toHaveLength(2);

    // Each group is headed by ITS session's start (ListSessions), not by "now".
    const day = (msAgo: number) =>
      new Date(Date.now() - msAgo).toLocaleDateString("en", { weekday: "short", month: "short", day: "numeric" });
    expect(within(groups[0]).getByRole("heading")).toHaveTextContent(`Session of ${day(60 * 60 * 1000)}`);
    expect(within(groups[0]).getByRole("heading")).toHaveTextContent("on screen");
    expect(within(groups[0]).getByText("Where is the dragon?")).toBeInTheDocument();
    expect(within(groups[1]).getByRole("heading")).toHaveTextContent(`Session of ${day(3 * 60 * 60 * 1000)}`);
    expect(within(groups[1]).getByRole("heading")).not.toHaveTextContent("on screen");
    expect(within(groups[1]).getByText("old dragon mention")).toBeInTheDocument();
  });

  it("clears the search with its button and with Escape", async () => {
    renderSearch();
    await screen.findByText("Idle");
    const box = screen.getByRole("searchbox", { name: /search the transcript/i });
    // Nothing typed: nothing to clear.
    expect(screen.queryByRole("button", { name: "Clear search" })).not.toBeInTheDocument();

    fireEvent.change(box, { target: { value: "dragon" } });
    await screen.findByTestId("transcript-search-results");
    fireEvent.click(screen.getByRole("button", { name: "Clear search" }));
    expect(box).toHaveValue("");
    expect(screen.queryByTestId("transcript-search-results")).not.toBeInTheDocument();

    fireEvent.change(box, { target: { value: "dragon" } });
    await screen.findByTestId("transcript-search-results");
    fireEvent.keyDown(box, { key: "Escape" });
    expect(box).toHaveValue("");
    expect(screen.queryByTestId("transcript-search-results")).not.toBeInTheDocument();
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
    await waitFor(() =>
      expect(document.querySelector('[data-line-id="u:1"]')).toHaveAttribute("data-highlighted", "true"),
    );
    // The highlighted u:1 is the OLDER session's line — not the rendered session's
    // colliding u:1 ("Where is the dragon?", which still shows only in the results).
    expect(document.querySelector('[data-line-id="u:1"]')).toHaveTextContent("old dragon mention");
    // The banner says which session this is, and offers the way back.
    expect(screen.getByTestId("viewing-past")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /back to latest/i })).toBeInTheDocument();

    // Clicking the current-session hit navigates back and highlights there.
    fireEvent.click(within(screen.getByTestId("transcript-search-results")).getByText("Where is the dragon?"));
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
      listHighlights: () => create(ListHighlightsResponseSchema, { highlights: [] }),
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
        JSON.stringify({ lines: [], status: "live", connection: "connected", typing: { active: true, label: "Listening to the table…" } }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      )) as typeof fetch;
  });

  it("renders the soft spend-cap badge with the estimated spend, labelled estimated", async () => {
    render(
      <Providers transport={spendCapTransport("soft", 3.21)} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );
    const badge = await screen.findByTestId("spend-cap");
    expect(badge).toHaveTextContent(/soft spending limit reached/i);
    expect(badge).toHaveTextContent(/no new ai replies/i);
    expect(badge).toHaveTextContent("$3.21");
    expect(badge).toHaveTextContent(/estimated/i);
  });

  it("renders the estimate in the display language, keeping the currency USD", async () => {
    // The providers bill in USD whatever the operator reads the console in — the
    // AMOUNT is localized, the currency is not converted.
    localStorage.setItem("gx-lang", "de");
    render(
      <Providers transport={spendCapTransport("soft", 3.21)} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );
    const badge = await screen.findByTestId("spend-cap");
    expect(badge).toHaveTextContent(/Geschätzte Ausgaben/);
    expect(badge).toHaveTextContent("3,21 $");
    localStorage.clear();
  });

  it("renders the hard spend-cap badge when the hard cap is crossed", async () => {
    render(
      <Providers transport={spendCapTransport("hard", 10)} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );
    const badge = await screen.findByTestId("spend-cap");
    expect(badge).toHaveTextContent(/hard spending limit reached/i);
    expect(badge).toHaveTextContent("$10.00");
  });

  it("shows no spend-cap badge when no cap is crossed", async () => {
    render(
      <Providers transport={spendCapTransport("", 1.5)} queryClient={makeQueryClient()}>
        <Screen />
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
      listHighlights: () => create(ListHighlightsResponseSchema, { highlights: [] }),
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

/** Opens the History popover and returns its list. */
async function openHistory() {
  fireEvent.click(await screen.findByRole("button", { name: "History" }));
  return await screen.findByTestId("session-picker");
}

describe("Session past-session picker (#270)", () => {
  beforeEach(pickerFetch);

  it("lists past sessions and replays a picked session's full transcript in seq order", async () => {
    const navigated = vi.fn();
    render(
      <Providers transport={pickerTransport()} queryClient={makeQueryClient()}>
        <Screen onNavigate={navigated} />
      </Providers>,
    );

    // The history lists both sessions: when (weekday and date), how long, how much.
    const picker = await openHistory();
    const rows = within(picker).getAllByRole("button");
    expect(rows).toHaveLength(2);
    const day = new Date(Date.now() - 60 * 60 * 1000).toLocaleDateString("en", {
      weekday: "short",
      month: "short",
      day: "numeric",
    });
    expect(rows[0]).toHaveTextContent(day);
    expect(rows[0]).toHaveTextContent("1h 0m · 12 lines");
    expect(rows[1]).toHaveTextContent("1h 0m · 5 lines");
    // The session on screen is marked.
    expect(rows[0]).toHaveAttribute("aria-current", "true");
    expect(rows[1]).not.toHaveAttribute("aria-current");

    // The default view is the latest (current) session's persisted transcript.
    expect(await screen.findByText("latest first line")).toBeInTheDocument();

    // Pick the older session → its OWN transcript replays, in snapshot (seq) order.
    fireEvent.click(rows[1]);
    expect(await screen.findByText("old first line")).toBeInTheDocument();
    expect(screen.getByText("old second line")).toBeInTheDocument();
    const items = screen.getAllByRole("listitem").filter((li) => li.hasAttribute("data-line-id"));
    expect(items.map((li) => li.getAttribute("data-line-id"))).toEqual(["u:1", "a:t1"]);
    // The latest session's lines are no longer on screen.
    expect(screen.queryByText("latest first line")).not.toBeInTheDocument();
    // A picked PAST session opens NO live stream — pins the `active && !viewingPast`
    // gate (#270 6a): replay is snapshot-only, never an EventSource.
    expect(MockEventSource.last()).toBeUndefined();

    // The pick is a navigation: ?session=<id>, pushed, so Back steps out of it
    // and a reload restores it. The popover has closed behind it.
    expect(navigated).toHaveBeenLastCalledWith(
      expect.objectContaining({ search: { session: "vsOld" } }),
      { replace: false },
    );
    expect(screen.queryByTestId("session-picker")).not.toBeInTheDocument();

    // The banner names the session being viewed; idle, the way back is "latest".
    const banner = screen.getByTestId("viewing-past");
    expect(banner).toHaveTextContent(/Viewing the session of/);
    fireEvent.click(within(banner).getByRole("button", { name: "Back to latest" }));
    expect(await screen.findByText("latest first line")).toBeInTheDocument();
    expect(navigated).toHaveBeenLastCalledWith(expect.objectContaining({ search: {} }), { replace: false });
    expect(screen.queryByTestId("viewing-past")).not.toBeInTheDocument();
  });

  it("marks failed, live and self-ended sessions and folds the short and empty ones away", async () => {
    const at = (minAgo: number) => timestampFromDate(new Date(Date.now() - minAgo * 60 * 1000));
    const sessions = [
      create(VoiceSessionSchema, { id: "run", campaignId: "c1", status: "running", startedAt: at(10) }),
      create(VoiceSessionSchema, {
        id: "auto", campaignId: "c1", status: "ended", startedAt: at(300), endedAt: at(200), lineCount: 40,
        endReason: "idle_no_audio: the Voice Session processed no audio for the idle close window",
      }),
      create(VoiceSessionSchema, {
        id: "bad", campaignId: "c1", status: "failed", startedAt: at(400), endedAt: at(399),
        endReason: "voice_join_timeout: no answer",
      }),
      // Two false starts: nothing said, and stopped within the minute.
      create(VoiceSessionSchema, { id: "empty", campaignId: "c1", status: "ended", startedAt: at(500), endedAt: at(480), lineCount: 0 }),
      create(VoiceSessionSchema, {
        id: "short", campaignId: "c1", status: "ended",
        startedAt: timestampFromDate(new Date(Date.now() - 600 * 60 * 1000)),
        endedAt: timestampFromDate(new Date(Date.now() - 600 * 60 * 1000 + 20_000)),
        lineCount: 2,
      }),
    ];
    const transport = createRouterTransport(({ service }) => {
      service(SessionService, {
        listHighlights: () => create(ListHighlightsResponseSchema, { highlights: [] }),
        getSession: () => create(GetSessionResponseSchema, { session: sessions[0], active: true }),
        listSessions: () => create(ListSessionsResponseSchema, { sessions }),
      });
      service(CampaignService, {
        getActiveCampaign: () =>
          create(GetActiveCampaignResponseSchema, {
            campaign: create(CampaignSchema, { id: "c1", name: "The Sunless Citadel" }),
          }),
      });
    });
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );

    const picker = await openHistory();
    const list = within(picker).getAllByRole("list")[0];
    const rows = within(list).getAllByRole("button");
    expect(rows).toHaveLength(3);
    // The running session: a Live badge, and no "0 lines" beside a live transcript.
    expect(rows[0]).toHaveTextContent("Live");
    expect(rows[0]).not.toHaveTextContent(/lines?/);
    // Ended by the idle watchdog: marked, and the reason spelled out.
    expect(rows[1]).toHaveTextContent("Ended by itself");
    expect(rows[1]).toHaveTextContent("It ended by itself because nobody spoke for a while.");
    // Failed: never folded away, however short it was.
    expect(rows[2]).toHaveTextContent("Failed");
    expect(rows[2]).toHaveTextContent(/The Bot couldn't join the voice channel/);

    // The two false starts are one line until asked for.
    const more = within(picker).getByRole("button", { name: "2 short or empty sessions" });
    expect(more).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(more);
    expect(more).toHaveAttribute("aria-expanded", "true");
    expect(within(picker).getAllByRole("list")).toHaveLength(2);
    expect(within(within(picker).getAllByRole("list")[1]).getAllByRole("button")).toHaveLength(2);
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
        <Screen />
      </Providers>,
    );
    const picker = await openHistory();
    fireEvent.click(within(picker).getByText(/5 lines/));

    // Inline error shows + a toast fires — never the "Start a session…" empty state.
    expect(await screen.findByTestId("snapshot-error")).toBeInTheDocument();
    expect(screen.queryByText(/start a session to capture/i)).not.toBeInTheDocument();
    await waitFor(() =>
      expect(vi.mocked(toast.error).mock.calls.some(([m]) => /couldn't load that session/i.test(String(m)))).toBe(true),
    );
  });
});

describe("Session palette deep link (#591)", () => {
  beforeEach(pickerFetch);

  it("views the linked session, jumps to the linked line, and strips only the one-shot line param", async () => {
    const navigated = vi.fn();
    render(
      <Providers transport={pickerTransport()} queryClient={makeQueryClient()}>
        <Screen search={{ session: "vsOld", line: "a:t1" }} onNavigate={navigated} />
      </Providers>,
    );

    // The deep-linked OLDER session's transcript renders (not the latest), and
    // the linked line carries the highlight marker once its snapshot lands.
    expect(await screen.findByText("old second line")).toBeInTheDocument();
    expect(screen.queryByText("latest first line")).not.toBeInTheDocument();
    await waitFor(() => {
      const line = document.querySelector('[data-line-id="a:t1"]');
      expect(line).not.toBeNull();
      expect(line).toHaveAttribute("data-highlighted", "true");
    });
    // ?line= is consumed (replace, no history entry) exactly once; ?session=
    // stays, because it IS the view — a reload lands on the same session.
    expect(navigated).toHaveBeenCalledTimes(1);
    expect(navigated).toHaveBeenCalledWith(
      expect.objectContaining({ search: { session: "vsOld" } }),
      { replace: true },
    );
    expect(screen.getByTestId("viewing-past")).toBeInTheDocument();
  });

  it("restores the viewed session and its banner from ?session= alone (a reload)", async () => {
    const navigated = vi.fn();
    render(
      <Providers transport={pickerTransport()} queryClient={makeQueryClient()}>
        <Screen search={{ session: "vsOld" }} onNavigate={navigated} />
      </Providers>,
    );
    expect(await screen.findByText("old first line")).toBeInTheDocument();
    expect(screen.getByTestId("viewing-past")).toHaveTextContent(/viewing the session of/i);
    expect(screen.getByRole("button", { name: /back to latest/i })).toBeInTheDocument();
    // Nothing to consume: the URL is left alone.
    expect(navigated).not.toHaveBeenCalled();
  });

  it("strips stray line/highlight params that arrive without their session half", async () => {
    const navigated = vi.fn();
    render(
      <Providers transport={pickerTransport()} queryClient={makeQueryClient()}>
        <Screen search={{ line: "u:1" }} onNavigate={navigated} />
      </Providers>,
    );
    // The default (latest) session renders unaffected; the stray param is shed.
    expect(await screen.findByText("latest first line")).toBeInTheDocument();
    await waitFor(() =>
      expect(navigated).toHaveBeenCalledWith(expect.objectContaining({ search: {} }), { replace: true }),
    );
    const line = document.querySelector('[data-line-id="u:1"]');
    expect(line).not.toHaveAttribute("data-highlighted");
  });

  it("a deep link to the LIVE session lands on the live default, never a past-session view", async () => {
    // The screen waits for getSession to settle, so the live session's id
    // compares against the REAL current id — not null — and the screen never
    // flashes past-session UI for the session that is running.
    const navigated = vi.fn();
    render(
      <Providers transport={livePickerTransport()} queryClient={makeQueryClient()}>
        <Screen search={{ session: "vs1" }} onNavigate={navigated} />
      </Providers>,
    );
    // ?session= naming the current session is only noise: it is dropped.
    await waitFor(() =>
      expect(navigated).toHaveBeenCalledWith(expect.objectContaining({ search: {} }), { replace: true }),
    );
    expect(screen.queryByTestId("viewing-past")).not.toBeInTheDocument();
    expect(await screen.findByText("Live transcript")).toBeInTheDocument();
  });

  it("a highlight deep link opens the Highlights tab on that session", async () => {
    render(
      <Providers transport={highlightsTransport(false)} queryClient={makeQueryClient()}>
        <Screen search={{ session: "vsOld", highlight: "h-vsOld" }} />
      </Providers>,
    );
    expect(await screen.findByText(/A whisper from the sealed crypt/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Highlights/ })).toHaveAttribute("aria-pressed", "true");
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
      listHighlights: () => create(ListHighlightsResponseSchema, { highlights: [] }),
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
        <Screen />
      </Providers>,
    );

    // Live is the default: Live badge + a "Live transcript" heading, the running
    // row in the history carries the Live badge.
    expect(await screen.findByText("Live")).toBeInTheDocument();
    expect(screen.getByText("Live transcript")).toBeInTheDocument();
    let picker = await openHistory();
    expect(within(within(picker).getAllByRole("button")[0]).getByText("Live")).toBeInTheDocument();
    // Close the popover again (Escape): the GM only looked.
    fireEvent.keyDown(picker, { key: "Escape" });
    await waitFor(() => expect(screen.queryByTestId("session-picker")).not.toBeInTheDocument());

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
    picker = await openHistory();
    fireEvent.click(within(picker).getByText(/5 lines/));
    expect(await screen.findByText("Session transcript")).toBeInTheDocument();
    expect(await screen.findByText("old first line")).toBeInTheDocument();
    // The session is still running: Stop stays on screen, and the way back says so.
    expect(screen.getByRole("button", { name: /stop session/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Back to live" })).toBeInTheDocument();
    // Pin the `active && !viewingPast` gate (#270 6b): browsing a past session opens
    // NO new stream — not for vsOld, not at all. A broken gate would open ES(vsOld).
    expect(MockEventSource.instances.length).toBe(esCountBeforeBrowse);
    expect(MockEventSource.instances.some((i) => i.url.includes("vsOld"))).toBe(false);

    // Back to current → the live feed is the default again AND its stream reopens:
    // a newly streamed line renders (guards AC5 that returning resumes the live tail,
    // #270 6b — not a frozen snapshot).
    fireEvent.click(screen.getByRole("button", { name: /back to (latest|live)/i }));
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
      listHighlights: () => create(ListHighlightsResponseSchema, { highlights: [] }),
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
        <Screen />
      </Providers>,
    );

    // In campaign A, browse the older (archived) session.
    const picker = await openHistory();
    fireEvent.click(within(picker).getByText(/9 lines/));
    expect(await screen.findByText("A archived line")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /back to (latest|live)/i })).toBeInTheDocument();

    // Switch to campaign B and run the Active-Campaign sweep (topbar switch).
    state.campaign = "B";
    await act(async () => {
      void invalidateActiveCampaignScopedQueries(qc);
    });

    // The view resets: B's current transcript renders, A's archived line is gone,
    // and the "back to current" affordance (viewingPast) is cleared.
    expect(await screen.findByText("B current line")).toBeInTheDocument();
    expect(screen.queryByText("A archived line")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /back to (latest|live)/i })).not.toBeInTheDocument();
  });
});

// --- Recap (#274) -----------------------------------------------------------

// recapStartISO is the fixed start instant of the idle ended session the recap
// tests cover; the result card's label is derived from it (formatStamp).
const recapStartISO = "2026-07-08T20:15:00Z";

// recapStampLabel replicates the screen's formatStamp for the covered session, so
// the label assertion is timezone-consistent with the render (both run in this env).
// The screen formats in the display language's locale — "en" without a provider.
function recapStampLabel(iso: string): string {
  return new Date(iso).toLocaleString("en", {
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
      listHighlights: () => create(ListHighlightsResponseSchema, { highlights: [] }),
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
        <Screen />
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
        <Screen />
      </Providers>,
    );

    // Browse the older past session, then recap it.
    const picker = await openHistory();
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
        <Screen />
      </Providers>,
    );

    fireEvent.click(await screen.findByTestId("recap-button"));
    await waitFor(() =>
      expect(vi.mocked(toast.error).mock.calls.some(([m]) => /couldn't generate the recap/i.test(String(m)))).toBe(true),
    );
    expect(screen.queryByTestId("recap-result")).not.toBeInTheDocument();
  });

  it("keeps a recap with the session it covers: gone while another is viewed, back on return", async () => {
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
      async () => create(GenerateRecapResponseSchema, { text: "A recap of the current session.", sessionIds: ["vs1"], windowed: false }),
      [latest, older],
    );

    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );

    // Recap the default (current) session → result shows.
    fireEvent.click(await screen.findByTestId("recap-button"));
    expect(await screen.findByTestId("recap-result")).toBeInTheDocument();

    // Browse a past session → the recap does not follow (it covers the other one).
    const picker = await openHistory();
    fireEvent.click(within(picker).getByText(/5 lines/));
    await waitFor(() => expect(screen.queryByTestId("recap-result")).not.toBeInTheDocument());

    // Back on the session it covers → it is there again, without a second call.
    fireEvent.click(screen.getByRole("button", { name: "Back to latest" }));
    expect(await screen.findByTestId("recap-result")).toHaveTextContent("A recap of the current session.");
    expect(requested).toEqual([["vs1"]]);
  });

  it("is still there after the screen was left and reopened (the wiki and back)", async () => {
    const { transport, requested } = recapTransport(async () =>
      create(GenerateRecapResponseSchema, { text: "The party bested the goblin warren.", sessionIds: ["vs1"], windowed: false }),
    );
    const qc = makeQueryClient();
    const first = render(
      <Providers transport={transport} queryClient={qc}>
        <Screen />
      </Providers>,
    );
    fireEvent.click(await screen.findByTestId("recap-button"));
    expect(await screen.findByTestId("recap-result")).toBeInTheDocument();

    // Navigating to another screen unmounts this one; the app's QueryClient lives on.
    first.unmount();
    expect(screen.queryByTestId("recap-result")).not.toBeInTheDocument();
    render(
      <Providers transport={transport} queryClient={qc}>
        <Screen />
      </Providers>,
    );
    expect(await screen.findByTestId("recap-result")).toHaveTextContent("The party bested the goblin warren.");
    // Nothing was generated (or paid for) a second time.
    expect(requested).toHaveLength(1);
  });

  it("lands a recap that finishes while the screen is closed, and shows it on return", async () => {
    let release!: (r: ReturnType<typeof create<typeof GenerateRecapResponseSchema>>) => void;
    const gate = new Promise<ReturnType<typeof create<typeof GenerateRecapResponseSchema>>>((res) => {
      release = res;
    });
    const { transport } = recapTransport(() => gate);
    const qc = makeQueryClient();
    const first = render(
      <Providers transport={transport} queryClient={qc}>
        <Screen />
      </Providers>,
    );
    fireEvent.click(await screen.findByTestId("recap-button"));
    expect(await screen.findByTestId("recap-pending")).toBeInTheDocument();

    // The GM opens the wiki while it generates…
    first.unmount();
    // …comes back to find it still generating (not an idle button to press twice)…
    render(
      <Providers transport={transport} queryClient={qc}>
        <Screen />
      </Providers>,
    );
    expect(await screen.findByTestId("recap-pending")).toBeInTheDocument();
    expect(screen.getByTestId("recap-button")).toBeDisabled();

    // …and the result arrives where they are.
    await act(async () => {
      release(create(GenerateRecapResponseSchema, { text: "Written while away.", sessionIds: ["vs1"], windowed: false }));
    });
    expect(await screen.findByTestId("recap-result")).toHaveTextContent("Written while away.");
  });

  it("can be copied and regenerated, and says how long it is kept", async () => {
    const writeText = vi.fn(async () => {});
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    let n = 0;
    const { transport, requested } = recapTransport(async () =>
      create(GenerateRecapResponseSchema, { text: `Recap take ${++n}.`, sessionIds: ["vs1"], windowed: false }),
    );
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );
    fireEvent.click(await screen.findByTestId("recap-button"));
    const card = await screen.findByTestId("recap-result");
    expect(card).toHaveTextContent("Recap take 1.");
    expect(card).toHaveTextContent(/Recaps are not saved/);
    // The first "Recap" button has done its job; the card's own actions take over.
    expect(screen.queryByTestId("recap-button")).not.toBeInTheDocument();

    fireEvent.click(within(card).getByRole("button", { name: "Copy" }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("Recap take 1."));
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("Recap copied."));

    fireEvent.click(within(card).getByRole("button", { name: "Regenerate" }));
    await waitFor(() => expect(screen.getByTestId("recap-result")).toHaveTextContent("Recap take 2."));
    expect(requested).toEqual([["vs1"], ["vs1"]]);
  });

  it("says a recap can take up to two minutes, in both languages", async () => {
    let release!: (r: ReturnType<typeof create<typeof GenerateRecapResponseSchema>>) => void;
    const gate = new Promise<ReturnType<typeof create<typeof GenerateRecapResponseSchema>>>((res) => {
      release = res;
    });
    localStorage.setItem("gx-lang", "de");
    const { transport } = recapTransport(() => gate);
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );
    fireEvent.click(await screen.findByTestId("recap-button"));
    expect(await screen.findByTestId("recap-pending")).toHaveTextContent(/bis zu zwei Minuten/);
    await act(async () => {
      release(create(GenerateRecapResponseSchema, { text: "Fertig.", sessionIds: ["vs1"], windowed: false }));
    });
    localStorage.clear();
  });

  it("files an in-flight recap under ITS session when the GM has moved on to another", async () => {
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
        <Screen />
      </Providers>,
    );

    // Recap the current (vs1) session; while it is pending, switch to the past one.
    fireEvent.click(await screen.findByTestId("recap-button"));
    expect(await screen.findByTestId("recap-pending")).toBeInTheDocument();
    const picker = await openHistory();
    fireEvent.click(within(picker).getByText(/5 lines/));

    // The other session is not the one being generated: no pending row there.
    await screen.findByTestId("viewing-past");
    expect(screen.queryByTestId("recap-pending")).not.toBeInTheDocument();

    // Now the gated vs1 recap resolves — but vs1 is no longer on screen, so it
    // must never render above vsOld's transcript…
    await act(async () => {
      release(create(GenerateRecapResponseSchema, { text: "Recap of vs1.", sessionIds: ["vs1"], windowed: false }));
    });
    expect(screen.queryByTestId("recap-result")).not.toBeInTheDocument();
    expect(screen.queryByText("Recap of vs1.")).not.toBeInTheDocument();

    // …and it is not lost either: it is waiting under the session it covers.
    fireEvent.click(screen.getByRole("button", { name: "Back to latest" }));
    expect(await screen.findByTestId("recap-result")).toHaveTextContent("Recap of vs1.");
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
        <Screen />
      </Providers>,
    );

    expect(await screen.findByText("Live")).toBeInTheDocument();
  });
});

// highlightsTransport wires the SessionService highlight RPCs to prove the strip
// is mounted for the rendered session (#309): listHighlights returns one promoted
// Highlight per session id so a session's OWN highlights render when it is the
// rendered session (live, or a picked past one). getSession reflects `active`.
function highlightsTransport(active: boolean) {
  const current = create(VoiceSessionSchema, {
    id: "vs1",
    campaignId: "c1",
    status: active ? "running" : "ended",
    startedAt: timestampFromDate(new Date(Date.now() - 30 * 60 * 1000)),
    ...(active ? {} : { endedAt: timestampFromDate(new Date()), lineCount: 7 }),
  });
  const older = create(VoiceSessionSchema, {
    id: "vsOld",
    campaignId: "c1",
    status: "ended",
    startedAt: timestampFromDate(new Date(Date.now() - 5 * 60 * 60 * 1000)),
    endedAt: timestampFromDate(new Date(Date.now() - 4 * 60 * 60 * 1000)),
    lineCount: 5,
  });
  const excerptFor: Record<string, string> = {
    vs1: "The keep's gate shattered inward.",
    vsOld: "A whisper from the sealed crypt.",
  };
  return createRouterTransport(({ service }) => {
    service(SessionService, {
      getSession: () => create(GetSessionResponseSchema, { session: current, active }),
      listSessions: () => create(ListSessionsResponseSchema, { sessions: [current, older] }),
      listHighlights: (req) =>
        create(ListHighlightsResponseSchema, {
          highlights: [
            create(HighlightSchema, {
              id: `h-${req.voiceSessionId}`,
              voiceSessionId: req.voiceSessionId,
              status: "promoted",
              startsAt: timestampFromDate(new Date()),
              endsAt: timestampFromDate(new Date()),
              score: 9,
              excerpt: excerptFor[req.voiceSessionId] ?? "",
              reason: "Peak moment.",
              clipContentType: "audio/wav",
            }),
          ],
        }),
      promoteHighlight: (req) =>
        create(PromoteHighlightResponseSchema, {
          highlight: create(HighlightSchema, { id: req.id, status: "promoted" }),
        }),
      deleteHighlight: () => create(DeleteHighlightResponseSchema, {}),
    });
    service(CampaignService, {
      getActiveCampaign: () =>
        create(GetActiveCampaignResponseSchema, {
          campaign: create(CampaignSchema, { id: "c1", name: "The Sunless Citadel" }),
        }),
    });
  });
}

/** Switches the pane to one of its three views. */
function openTab(name: RegExp) {
  fireEvent.click(within(screen.getByRole("group", { name: "Session view" })).getByRole("button", { name }));
}

describe("Session tabs: transcript, highlights, board", () => {
  it("opens on the transcript; Highlights carries the session's count", async () => {
    render(
      <Providers transport={highlightsTransport(true)} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );
    expect(await screen.findByText("Live")).toBeInTheDocument();
    const tabs = screen.getByRole("group", { name: "Session view" });
    expect(within(tabs).getByRole("button", { name: "Transcript" })).toHaveAttribute("aria-pressed", "true");
    // The count is there before the tab was ever opened.
    expect(await within(tabs).findByRole("button", { name: "Highlights 1" })).toHaveAttribute("aria-pressed", "false");
    // The strip itself is not mounted until asked for.
    expect(screen.queryByText(/The keep's gate shattered inward/)).not.toBeInTheDocument();
  });

  it("shows the LIVE session's highlights on the Highlights tab", async () => {
    render(
      <Providers transport={highlightsTransport(true)} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );
    expect(await screen.findByText("Live")).toBeInTheDocument();
    openTab(/^Highlights/);
    expect(await screen.findByText(/The keep's gate shattered inward/)).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Highlights" })).toBeInTheDocument();
  });

  it("keeps a visited tab mounted when another is shown (a clip keeps playing)", async () => {
    render(
      <Providers transport={highlightsTransport(true)} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );
    await screen.findByText("Live");
    openTab(/^Highlights/);
    const excerpt = await screen.findByText(/The keep's gate shattered inward/);
    const audio = excerpt.closest("li")!.querySelector("audio");

    openTab(/^Transcript$/);
    // Hidden, not unmounted: the very same <audio> element is still in the document.
    expect(excerpt.closest("section")).toHaveAttribute("hidden");
    expect(document.contains(audio)).toBe(true);
    expect(screen.getByTestId("transcript-pane").closest("section")).not.toHaveAttribute("hidden");
  });

  it("shows a PICKED past session's highlights — that session's, not the current one's", async () => {
    globalThis.fetch = snapshotFetch({ lines: [], status: "idle", typing: { active: false, label: "" } });
    render(
      <Providers transport={highlightsTransport(false)} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );
    await screen.findByText("Idle");
    openTab(/^Highlights/);
    // Default view is the current (ended) session — its highlight shows first.
    expect(await screen.findByText(/The keep's gate shattered inward/)).toBeInTheDocument();

    // Pick the older session → the strip now renders THAT session's highlight.
    const picker = await openHistory();
    fireEvent.click(within(picker).getByText(/5 lines/));
    expect(await screen.findByText(/A whisper from the sealed crypt/)).toBeInTheDocument();
    expect(screen.queryByText(/The keep's gate shattered inward/)).not.toBeInTheDocument();
  });

  it("says what the Highlights tab is for when no session has run yet", async () => {
    const navigated = vi.fn();
    render(
      <Providers transport={mockTransport()} queryClient={makeQueryClient()}>
        <Screen onNavigate={navigated} />
      </Providers>,
    );
    await screen.findByText("Idle");
    // No strip, no skeleton that would never resolve — and no count.
    expect(screen.getByRole("button", { name: "Highlights" })).toBeInTheDocument();
    openTab(/^Highlights/);
    // This campaign has highlight recording off: that is why the tab is empty,
    // and the campaign's settings are where it is turned on.
    const panel = screen.getByRole("region", { name: "Highlights" });
    expect(await within(panel).findByText("Highlight recording is off")).toBeInTheDocument();
    fireEvent.click(within(panel).getByRole("button", { name: "Campaign settings" }));
    expect(navigated).toHaveBeenCalledWith(
      expect.objectContaining({ screen: "campaign", view: "settings" }),
      { replace: false },
    );
  });

  it("announces a highlight captured while live, with a way to see it", async () => {
    const current = create(VoiceSessionSchema, {
      id: "vs1",
      campaignId: "c1",
      status: "running",
      startedAt: timestampFromDate(new Date()),
    });
    const mk = (id: string) =>
      create(HighlightSchema, {
        id,
        voiceSessionId: "vs1",
        status: "candidate",
        startsAt: timestampFromDate(new Date()),
        endsAt: timestampFromDate(new Date()),
        score: 9,
        excerpt: `moment ${id}`,
        reason: "Peak moment.",
        clipContentType: "audio/wav",
      });
    const state = { highlights: [mk("h1")] };
    const transport = createRouterTransport(({ service }) => {
      service(SessionService, {
        getSession: () => create(GetSessionResponseSchema, { session: current, active: true }),
        listHighlights: () => create(ListHighlightsResponseSchema, { highlights: state.highlights }),
      });
      service(CampaignService, {
        getActiveCampaign: () =>
          create(GetActiveCampaignResponseSchema, {
            campaign: create(CampaignSchema, { id: "c1", name: "The Sunless Citadel", tapeArmed: true }),
          }),
      });
    });
    const qc = makeQueryClient();
    render(
      <Providers transport={transport} queryClient={qc}>
        <Screen />
      </Providers>,
    );
    // What was already there when the screen opened is not news.
    expect(await screen.findByRole("button", { name: "Highlights 1" })).toBeInTheDocument();
    expect(toast).not.toHaveBeenCalled();

    // The live poll brings a second one.
    state.highlights = [mk("h2"), mk("h1")];
    await act(async () => {
      await invalidateMethodQueries(qc, SessionService.method.listHighlights);
    });
    expect(await screen.findByRole("button", { name: "Highlights 2" })).toBeInTheDocument();
    await waitFor(() => expect(toast).toHaveBeenCalledTimes(1));
    const [message, options] = vi.mocked(toast).mock.calls[0];
    expect(message).toBe("New highlight captured.");

    // "Show" takes the GM to the Highlights tab.
    act(() => (options as unknown as { action: { onClick: () => void } }).action.onClick());
    expect(await screen.findByText("moment h2")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Highlights 2" })).toHaveAttribute("aria-pressed", "true");
  });
});

// --- Voice-channel picker ---------------------------------------------------

// channelTransport is an IDLE screen whose ListSessionVoiceChannels serves a
// configurable channel list + stored Default Voice Channel (or throws, the
// unlinked-guild case). Start flips the session Live (so the picker's wiring
// into StartSessionRequest is observable end-to-end) and records each request's
// voice_channel_id. ProviderService.saveDiscordSettings records the exact wire
// fields ("Set as default" must carry voice_channel_id ONLY — guild untouched)
// and persists the new default so the invalidated picker refetch reflects it.
function channelTransport(opts: {
  channels?: { id: string; name: string }[];
  defaultChannelId?: string;
  listError?: ConnectError;
}) {
  const state = { defaultChannelId: opts.defaultChannelId ?? "" };
  const startRequests: string[] = [];
  const saveRequests: { botToken?: string; guildId?: string; voiceChannelId?: string }[] = [];
  let active = false;
  let current: ReturnType<typeof create<typeof VoiceSessionSchema>> | undefined;

  const transport = createRouterTransport(({ service }) => {
    service(SessionService, {
      listHighlights: () => create(ListHighlightsResponseSchema, { highlights: [] }),
      getSession: () => create(GetSessionResponseSchema, { session: current, active }),
      listSessionVoiceChannels: () => {
        if (opts.listError) throw opts.listError;
        return create(ListSessionVoiceChannelsResponseSchema, {
          channels: (opts.channels ?? []).map((c) => create(VoiceChannelSchema, c)),
          defaultChannelId: state.defaultChannelId,
        });
      },
      startSession: (req) => {
        startRequests.push(req.voiceChannelId);
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
        active = false;
        current = undefined;
        return create(StopSessionResponseSchema, {});
      },
    });
    service(ProviderService, {
      saveDiscordSettings: (req) => {
        // Record the PRESENCE of each optional field as sent on the wire: the
        // "Set as default" save must carry voice_channel_id only.
        saveRequests.push({ botToken: req.botToken, guildId: req.guildId, voiceChannelId: req.voiceChannelId });
        if (req.voiceChannelId !== undefined) state.defaultChannelId = req.voiceChannelId;
        return create(SaveDiscordSettingsResponseSchema, {
          guildId: "g1",
          voiceChannelId: state.defaultChannelId,
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
  return { transport, startRequests, saveRequests };
}

const twoChannels = [
  { id: "c-general", name: "General" },
  { id: "c-tavern", name: "The Tavern" },
];

describe("Session voice-channel picker", () => {
  it("renders the picker on the idle screen with the stored default pre-selected", async () => {
    const { transport } = channelTransport({ channels: twoChannels, defaultChannelId: "c-tavern" });
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );

    expect(await screen.findByText("Idle")).toBeInTheDocument();
    expect(await screen.findByTestId("channel-picker")).toBeInTheDocument();
    // The trigger shows the DEFAULT channel's name pre-selected (default wins
    // over first-in-list).
    const trigger = screen.getByRole("combobox", { name: "Voice channel" });
    await waitFor(() => expect(trigger).toHaveTextContent("The Tavern"));
    // The selection IS the stored default → no "Set as default" affordance.
    expect(screen.queryByTestId("set-default-channel")).not.toBeInTheDocument();
  });

  it("Start sends the pre-selected default channel id in the StartSessionRequest", async () => {
    const { transport, startRequests } = channelTransport({
      channels: twoChannels,
      defaultChannelId: "c-tavern",
    });
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );

    await screen.findByTestId("channel-picker");
    fireEvent.click(screen.getByRole("button", { name: /start session/i }));

    await waitFor(() => expect(startRequests).toEqual(["c-tavern"]));
    // The wired-through start still flips the screen Live (no regression).
    expect(await screen.findByText("Live")).toBeInTheDocument();
  });

  it("picking another channel sends ITS id on Start and offers Set as default", async () => {
    const { transport, startRequests } = channelTransport({
      channels: twoChannels,
      defaultChannelId: "c-tavern",
    });
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );
    await screen.findByTestId("channel-picker");

    // Open the Radix Select (jsdom: keyboard open, the KnowledgePanel pattern)
    // and pick the NON-default channel.
    fireEvent.keyDown(screen.getByRole("combobox", { name: "Voice channel" }), { key: "Enter" });
    fireEvent.click(await screen.findByRole("option", { name: "General" }));
    await waitFor(() =>
      expect(screen.getByRole("combobox", { name: "Voice channel" })).toHaveTextContent("General"),
    );

    // The pick differs from the stored default → the affordance appears.
    expect(await screen.findByTestId("set-default-channel")).toBeInTheDocument();

    // Start joins the PICKED channel, not the stored default.
    fireEvent.click(screen.getByRole("button", { name: /start session/i }));
    await waitFor(() => expect(startRequests).toEqual(["c-general"]));
  });

  it("a missing Discord link blocks Start and says where to fix it", async () => {
    // The unlinked-guild case: ListSessionVoiceChannels fails FailedPrecondition
    // once (retry:false). There is nowhere to join, so Start is held — and the
    // pre-flight's Discord row says why, with the way to Setup. (Silence here
    // once dead-ended a GM: no picker, no clue, and a Start that could only fail.)
    const { transport, startRequests } = channelTransport({
      listError: new ConnectError("link a Discord server first", Code.FailedPrecondition),
    });
    const navigated = vi.fn();
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <Screen onNavigate={navigated} />
      </Providers>,
    );

    expect(await screen.findByText("Idle")).toBeInTheDocument();
    const row = await screen.findByTestId("preflight-discord");
    // The two known lister preconditions render through the message catalog
    // (localizable); any unknown server message still renders verbatim.
    await waitFor(() => expect(row).toHaveTextContent("Link a Discord server first."));
    expect(row).toHaveAttribute("data-tone", "block");
    expect(screen.queryByTestId("channel-picker")).not.toBeInTheDocument();

    const startBtn = screen.getByRole("button", { name: /start session/i });
    expect(startBtn).toBeDisabled();
    fireEvent.click(startBtn);
    expect(startRequests).toEqual([]);

    fireEvent.click(within(row).getByRole("button", { name: "Open Setup" }));
    expect(navigated).toHaveBeenCalledWith(expect.objectContaining({ screen: "configuration" }), { replace: false });
  });

  it("a missing Bot token blocks Start the same way", async () => {
    const { transport } = channelTransport({
      listError: new ConnectError("save a Discord Bot token first", Code.FailedPrecondition),
    });
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );
    const row = await screen.findByTestId("preflight-discord");
    await waitFor(() => expect(row).toHaveTextContent("Save a Discord Bot token first."));
    expect(screen.getByRole("button", { name: /start session/i })).toBeDisabled();
  });

  it("any other lister failure is a warning in the server's words, and Start stays possible", async () => {
    const { transport, startRequests } = channelTransport({
      listError: new ConnectError("discord: rate limited, retry in 3s", Code.Unavailable),
    });
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );
    const row = await screen.findByTestId("preflight-discord");
    await waitFor(() => expect(row).toHaveTextContent("discord: rate limited, retry in 3s"));
    expect(row).toHaveAttribute("data-tone", "warn");

    // Start re-enables once the errored list read settles (the loading hold
    // must not outlive the failure) and defers to the server.
    const startBtn = screen.getByRole("button", { name: /start session/i });
    await waitFor(() => expect(startBtn).toBeEnabled());
    fireEvent.click(startBtn);
    await waitFor(() => expect(startRequests).toEqual([""]));
    expect(await screen.findByText("Live")).toBeInTheDocument();
  });

  it("starts without a picker on a server that cannot list channels (Unimplemented)", async () => {
    const { transport, startRequests } = channelTransport({
      listError: new ConnectError("voice channel listing is not enabled on this server", Code.Unimplemented),
    });
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );

    expect(await screen.findByText("Idle")).toBeInTheDocument();
    expect(screen.queryByTestId("channel-picker")).not.toBeInTheDocument();
    // Not an alarm: the row says what will happen, in the catalog's words.
    const row = await screen.findByTestId("preflight-discord");
    await waitFor(() => expect(row).toHaveAttribute("data-tone", "info"));
    expect(row).not.toHaveTextContent(/not enabled on this server/);

    const startBtn = screen.getByRole("button", { name: /start session/i });
    await waitFor(() => expect(startBtn).toBeEnabled());
    fireEvent.click(startBtn);
    await waitFor(() => expect(startRequests).toEqual([""]));
  });

  it("notes an empty channel list instead of rendering nothing", async () => {
    const { transport, startRequests } = channelTransport({ channels: [] });
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );

    expect(await screen.findByText("Idle")).toBeInTheDocument();
    const row = await screen.findByTestId("preflight-discord");
    await waitFor(() => expect(row).toHaveTextContent("The linked Discord server has no voice channels."));
    expect(row).toHaveAttribute("data-tone", "warn");
    expect(screen.queryByTestId("channel-picker")).not.toBeInTheDocument();

    const startBtn = screen.getByRole("button", { name: /start session/i });
    await waitFor(() => expect(startBtn).toBeEnabled());
    fireEvent.click(startBtn);
    await waitFor(() => expect(startRequests).toEqual([""]));
  });

  it("the Discord row names the channel the Bot will join", async () => {
    const { transport } = channelTransport({ channels: twoChannels, defaultChannelId: "c-tavern" });
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );
    const row = await screen.findByTestId("preflight-discord");
    await waitFor(() => expect(row).toHaveTextContent("Server linked. The Bot joins “The Tavern”."));
    expect(row).toHaveAttribute("data-tone", "ok");
  });

  it("Set as default saves voice_channel_id ONLY and disappears once the default matches", async () => {
    // No stored default and nothing remembered → nothing is pre-selected (W2);
    // the GM's pick DIFFERS from the (empty) default, so the affordance shows.
    const { transport, saveRequests } = channelTransport({ channels: twoChannels });
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <Screen />
      </Providers>,
    );

    await screen.findByTestId("channel-picker");
    fireEvent.keyDown(screen.getByRole("combobox", { name: "Voice channel" }), { key: "Enter" });
    fireEvent.click(await screen.findByRole("option", { name: "General" }));
    await waitFor(() =>
      expect(screen.getByRole("combobox", { name: "Voice channel" })).toHaveTextContent("General"),
    );
    fireEvent.click(await screen.findByTestId("set-default-channel"));

    // The wire message carries voice_channel_id ONLY: guild_id and bot_token are
    // ABSENT (their stored values must stay untouched).
    await waitFor(() => expect(saveRequests).toHaveLength(1));
    expect(saveRequests[0].voiceChannelId).toBe("c-general");
    expect(saveRequests[0].guildId).toBeUndefined();
    expect(saveRequests[0].botToken).toBeUndefined();

    // Success invalidates the channel list → the refetched default now matches
    // the selection, so the affordance goes away.
    await waitFor(() => expect(screen.queryByTestId("set-default-channel")).not.toBeInTheDocument());
    expect(vi.mocked(toast.error)).not.toHaveBeenCalledWith(expect.stringMatching(/default channel/i));
  });
});
