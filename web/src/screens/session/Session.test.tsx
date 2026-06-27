import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent, waitFor, act } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";

import {
  SessionService,
  CampaignService,
  VoiceSessionSchema,
  GetSessionResponseSchema,
  StartSessionResponseSchema,
  StopSessionResponseSchema,
  GetActiveCampaignResponseSchema,
  CampaignSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { MockEventSource } from "@/test/mockEventSource";
import { Session, formatElapsed } from "./Session";

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
