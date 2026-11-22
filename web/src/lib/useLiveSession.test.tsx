import { describe, it, expect, vi, afterEach } from "vitest";
import { act, render, screen, waitFor } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { useQuery } from "@connectrpc/connect-query";

import {
  CampaignService,
  GetCampaignRosterResponseSchema,
  GetSessionResponseSchema,
  SessionService,
  VoiceSessionSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { watchVoiceSessionEnd } from "@/lib/campaignCache";
import { makeQueryClient } from "@/lib/queryClient";
import {
  IDLE_SESSION_REFETCH_MS,
  LIVE_SESSION_REFETCH_MS,
  formatElapsed,
  liveSessionRefetchInterval,
  useElapsedSeconds,
  useLiveSession,
} from "./useLiveSession";

afterEach(() => {
  vi.useRealTimers();
});

function backend(initiallyLive: boolean) {
  const state = { live: initiallyLive, sessionReads: 0, rosterReads: 0 };
  const startedAt = new Date("2026-08-01T20:00:00Z");
  const transport = createRouterTransport(({ service }) => {
    service(SessionService, {
      getSession: () => {
        state.sessionReads++;
        return create(GetSessionResponseSchema, {
          active: state.live,
          // The server keeps returning the most recent session once it ended.
          session: create(VoiceSessionSchema, { id: "s1", startedAt: timestampFromDate(startedAt) }),
        });
      },
    });
    service(CampaignService, {
      getCampaignRoster: () => {
        state.rosterReads++;
        return create(GetCampaignRosterResponseSchema, {});
      },
    });
  });
  return { transport, state, startedAt };
}

function Probe() {
  const { live, session, startedMs } = useLiveSession();
  return (
    <p data-testid="live">
      {live ? `live:${session?.id}:${startedMs}` : `idle:${session?.id ?? "-"}:${startedMs ?? "-"}`}
    </p>
  );
}

describe("useLiveSession", () => {
  it("reports a running Voice Session with its start instant", async () => {
    const h = backend(true);
    render(
      <Providers transport={h.transport} queryClient={makeQueryClient()}>
        <Probe />
      </Providers>,
    );
    await waitFor(() =>
      expect(screen.getByTestId("live")).toHaveTextContent(`live:s1:${h.startedAt.getTime()}`),
    );
  });

  it("reports idle — with no session and no start — once the session has ended", async () => {
    const h = backend(false);
    render(
      <Providers transport={h.transport} queryClient={makeQueryClient()}>
        <Probe />
      </Providers>,
    );
    // GetSession still carries the ENDED session; it must not read as live.
    await waitFor(() => expect(h.state.sessionReads).toBe(1));
    expect(screen.getByTestId("live")).toHaveTextContent("idle:-:-");
  });

  it("polls briskly while live and slowly while idle", () => {
    expect(liveSessionRefetchInterval({ state: { data: { active: true } } })).toBe(LIVE_SESSION_REFETCH_MS);
    expect(liveSessionRefetchInterval({ state: { data: { active: false } } })).toBe(IDLE_SESSION_REFETCH_MS);
    // Before the first answer it is the idle cadence — never "off".
    expect(liveSessionRefetchInterval({ state: {} })).toBe(IDLE_SESSION_REFETCH_MS);
    expect(LIVE_SESSION_REFETCH_MS).toBe(5_000);
    expect(IDLE_SESSION_REFETCH_MS).toBe(30_000);
  });

  it("keeps the shared getSession entry fresh, so the session-end sweep fires on a screen that is not Session", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const h = backend(true);
    const queryClient = makeQueryClient();
    // What the always-mounted topbar switcher does.
    const unwatch = watchVoiceSessionEnd(queryClient);
    // A campaign-scoped read on some other screen — the sweep must refetch it.
    function OtherScreen() {
      useQuery(CampaignService.method.getCampaignRoster, {});
      return null;
    }
    render(
      <Providers transport={h.transport} queryClient={queryClient}>
        <Probe />
        <OtherScreen />
      </Providers>,
    );
    await waitFor(() => expect(screen.getByTestId("live")).toHaveTextContent(/^live:/));
    await waitFor(() => expect(h.state.rosterReads).toBe(1));

    // The session ends server-side. Nothing on this screen asks; only the
    // hook's poll can notice.
    h.state.live = false;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(LIVE_SESSION_REFETCH_MS + 50);
    });
    await waitFor(() => expect(screen.getByTestId("live")).toHaveTextContent(/^idle:/));
    // true→false on the shared entry ran the Active-Campaign sweep.
    await waitFor(() => expect(h.state.rosterReads).toBeGreaterThan(1));
    unwatch();
  });
});

describe("useElapsedSeconds", () => {
  function Clock({ startMs }: { startMs: number | null }) {
    return <p data-testid="clock">{formatElapsed(useElapsedSeconds(startMs))}</p>;
  }

  it("ticks once a second from the start instant and rests at zero without one", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-08-01T21:12:40Z"));
    const start = new Date("2026-08-01T20:00:00Z").getTime();
    const { rerender } = render(<Clock startMs={start} />);
    // Right from the first paint — not 00:00:00 for a second.
    expect(screen.getByTestId("clock")).toHaveTextContent("01:12:40");

    act(() => {
      vi.advanceTimersByTime(3000);
    });
    expect(screen.getByTestId("clock")).toHaveTextContent("01:12:43");

    rerender(<Clock startMs={null} />);
    expect(screen.getByTestId("clock")).toHaveTextContent("00:00:00");
    // No timer left running once there is nothing to count.
    expect(vi.getTimerCount()).toBe(0);
  });

  it("never shows a negative time when the client clock runs behind the server's", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-08-01T19:59:58Z"));
    render(<Clock startMs={new Date("2026-08-01T20:00:00Z").getTime()} />);
    expect(screen.getByTestId("clock")).toHaveTextContent("00:00:00");
  });
});

describe("formatElapsed", () => {
  it("renders zero-padded HH:MM:SS and lets hours run past 99", () => {
    expect(formatElapsed(0)).toBe("00:00:00");
    expect(formatElapsed(59)).toBe("00:00:59");
    expect(formatElapsed(4360)).toBe("01:12:40");
    expect(formatElapsed(360000)).toBe("100:00:00");
    expect(formatElapsed(-5)).toBe("00:00:00");
    expect(formatElapsed(61.9)).toBe("00:01:01");
  });
});
