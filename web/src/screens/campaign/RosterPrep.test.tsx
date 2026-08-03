import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";

import {
  AgentSchema,
  CampaignService,
  GetKnowledgeGraphResponseSchema,
  GetRosterReadinessResponseSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { RosterPrep } from "./RosterPrep";

const BUTLER = create(AgentSchema, { id: "b1", name: "Glyphoxa", role: "butler" });
const BART = create(AgentSchema, {
  id: "a1",
  name: "Bart",
  role: "character",
  persona: "Gruff.",
  voice: "v1",
});

function renderPrep(
  opts: { factCount?: number; lastSpokeAt?: Date; handlers?: Record<string, unknown> } = {},
) {
  const onSelectAgent = vi.fn();
  const onOpenNode = vi.fn();
  const onOpenKnowledge = vi.fn();
  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      getKnowledgeGraph: () =>
        create(GetKnowledgeGraphResponseSchema, {
          nodes: [{ id: "bart", nodeType: 2, name: "Bart", agentId: "a1", bodyLen: 40 }],
          edges: [],
        }),
      getRosterReadiness: () =>
        create(GetRosterReadinessResponseSchema, {
          agents: [
            {
              agentId: "a1",
              linked: true,
              factCount: opts.factCount ?? 3,
              chars: 400,
              maxChars: 4000,
              lastSpokeAt: opts.lastSpokeAt ? timestampFromDate(opts.lastSpokeAt) : undefined,
            },
          ],
        }),
    });
  });
  render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <RosterPrep
        roster={[BUTLER, BART]}
        onSelectAgent={onSelectAgent}
        onOpenNode={onOpenNode}
        onOpenKnowledge={onOpenKnowledge}
      />
    </Providers>,
  );
  return { onSelectAgent, onOpenNode, onOpenKnowledge };
}

describe("RosterPrep", () => {
  // The AC is precise: present, not flagged.
  it("lists the Butler without grading it", async () => {
    renderPrep();
    expect(await screen.findByRole("button", { name: "Glyphoxa" })).toBeInTheDocument();
    expect(screen.getByText("Butler — always ready")).toBeInTheDocument();
    expect(screen.queryByLabelText(/Glyphoxa: Persona written/)).not.toBeInTheDocument();
  });

  it("shows the fact count the voice loop will actually inject", async () => {
    renderPrep({ factCount: 7 });
    expect(await screen.findByLabelText(/Bart: 7 facts in reach — done/)).toBeInTheDocument();
  });

  it("shows when an NPC last spoke", async () => {
    renderPrep({ lastSpokeAt: new Date("2026-07-04T20:00:00Z") });
    expect(await screen.findByText(/last spoke/)).toBeInTheDocument();
  });

  it("says so when an NPC has never spoken", async () => {
    renderPrep();
    expect(await screen.findByText("never spoken")).toBeInTheDocument();
  });

  // Each unsatisfied mark must land where the fix can ACTUALLY be made — and with
  // the entry id, or the GM is dumped on a list to hunt through by hand.
  it("opens the specific entry when facts are out of reach", async () => {
    const { onOpenNode } = renderPrep({ factCount: 0 });
    fireEvent.click(await screen.findByLabelText(/Bart: 0 facts in reach — missing/));
    expect(onOpenNode).toHaveBeenCalledWith("bart");
  });
});
