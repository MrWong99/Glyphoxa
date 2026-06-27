import { describe, it, expect } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
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
