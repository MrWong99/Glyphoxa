import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";

import {
  CampaignService,
  ListNodeAppearancesResponseSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { NodeAppearances } from "./NodeAppearances";

// The Appearances list (#545): "when did we last see that ogre", answered by a
// pointer to the exact Transcript Line.

function renderAppearances(appearances: unknown[]) {
  const onOpenLine = vi.fn();
  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      listNodeAppearances: () =>
        create(ListNodeAppearancesResponseSchema, { appearances: appearances as never }),
    });
  });
  render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <NodeAppearances nodeID="n1" onOpenLine={onOpenLine} />
    </Providers>,
  );
  return { onOpenLine };
}

const MENTION = {
  voiceSessionId: "s1",
  lineId: "u:7",
  at: timestampFromDate(new Date("2026-08-01T20:15:00Z")),
  who: "Ilva",
  kind: "player",
  text: "we should ask Bart about the ledger",
  sessionStartedAt: timestampFromDate(new Date("2026-08-01T20:00:00Z")),
};

describe("NodeAppearances", () => {
  it("shows what was said, so the GM reads it without following anything", async () => {
    renderAppearances([MENTION]);
    expect(await screen.findByText(/we should ask Bart about the ledger/)).toBeInTheDocument();
    expect(screen.getByText("Ilva")).toBeInTheDocument();
  });

  it("links to the exact Transcript Line", async () => {
    const { onOpenLine } = renderAppearances([MENTION]);
    fireEvent.click(await screen.findByRole("button", { name: /we should ask Bart/ }));
    // Session AND line: an appearance that only named the session would make the
    // GM scroll a transcript to find the thing they searched for.
    expect(onOpenLine).toHaveBeenCalledWith("s1", "u:7");
  });

  it("says so plainly when an entry has not come up yet", async () => {
    renderAppearances([]);
    expect(await screen.findByText(/Not mentioned yet/)).toBeInTheDocument();
  });
});
