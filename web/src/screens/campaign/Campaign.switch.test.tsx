import { describe, it, expect } from "vitest";
import { act, render, screen, fireEvent, waitFor } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  CampaignService,
  ChatService,
  VoiceService,
  AgentSchema,
  CampaignSchema,
  DraftNodeSchema,
  GetCampaignRosterResponseSchema,
  GenerateKnowledgeResponseSchema,
  ApplyGeneratedKnowledgeResponseSchema,
  GetCampaignTagsResponseSchema,
  GetPlanningThreadResponseSchema,
  ListNodesResponseSchema,
  ListPlanningThreadsResponseSchema,
  ListVoicesResponseSchema,
  NodeSchema,
  NodeType,
  PlanningThreadSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { invalidateActiveCampaignScopedQueries } from "@/lib/campaignCache";
import { makeQueryClient } from "@/lib/queryClient";
import { Campaign } from "./Campaign";
import type { CampaignView } from "./views";

// W3: the Active Campaign is ambient SERVER state — switching it in the topbar
// changes what every write on this screen lands in without changing a single
// request key. These tests switch campaigns under a mounted screen the way the
// CampaignSwitcher does (flip the server's selection, run the sweep) and prove
// no per-campaign UI state — least of all an unapplied AI draft — survives.
function twoCampaignTransport() {
  let active: "c1" | "c2" = "c1";
  const names = { c1: "The Sunless Citadel", c2: "Tides over Saltmarsh" };
  const applied: { campaign: string; names: string[] }[] = [];

  const transport = createRouterTransport(({ service }) => {
    service(VoiceService, { listVoices: () => create(ListVoicesResponseSchema, { voices: [] }) });
    service(CampaignService, {
      getCampaignRoster: () =>
        create(GetCampaignRosterResponseSchema, {
          campaign: create(CampaignSchema, { id: active, name: names[active], system: "5e", language: "en" }),
          roster: [
            create(AgentSchema, {
              id: `butler-${active}`,
              campaignId: active,
              role: "butler",
              name: "Glyphoxa",
              addressOnly: true,
            }),
          ],
        }),
      listNodes: () =>
        create(ListNodesResponseSchema, {
          nodes: [
            create(NodeSchema, {
              id: `node-${active}`,
              nodeType: NodeType.LOCATION,
              name: active === "c1" ? "Oakhurst" : "Saltmarsh",
              body: "",
            }),
          ],
        }),
      getCampaignTags: () => create(GetCampaignTagsResponseSchema, { entries: [] }),
      generateKnowledge: () =>
        create(GenerateKnowledgeResponseSchema, {
          nodes: [create(DraftNodeSchema, { nodeType: NodeType.NPC, name: "Meepo", body: "A kobold." })],
          edges: [],
        }),
      applyGeneratedKnowledge: (req) => {
        applied.push({ campaign: active, names: req.nodes.map((n) => n.name) });
        return create(ApplyGeneratedKnowledgeResponseSchema, { nodes: [], edgesCreated: 0 });
      },
    });
    service(ChatService, {
      listPlanningThreads: (req) =>
        create(ListPlanningThreadsResponseSchema, {
          threads: [
            create(PlanningThreadSchema, {
              id: `thread-${req.campaignId}`,
              campaignId: req.campaignId,
              title: req.campaignId === "c1" ? "Citadel prep" : "Saltmarsh prep",
            }),
          ],
        }),
      getPlanningThread: (req) =>
        create(GetPlanningThreadResponseSchema, {
          thread: create(PlanningThreadSchema, {
            id: req.threadId,
            campaignId: req.campaignId,
            title: req.campaignId === "c1" ? "Citadel prep" : "Saltmarsh prep",
          }),
          messages: [],
        }),
    });
  });

  return { transport, applied, setActive: (id: "c1" | "c2") => (active = id) };
}

function mount(view: CampaignView) {
  const h = twoCampaignTransport();
  const queryClient = makeQueryClient();
  render(
    <Providers transport={h.transport} queryClient={queryClient}>
      <Campaign view={view} />
    </Providers>,
  );
  // What CampaignSwitcher's SetActiveCampaign onSuccess does.
  const switchTo = async (id: "c1" | "c2") => {
    h.setActive(id);
    await act(async () => {
      await invalidateActiveCampaignScopedQueries(queryClient);
    });
  };
  return { ...h, switchTo };
}

describe("Campaign across an Active Campaign switch (W3)", () => {
  it("drops an unapplied AI draft instead of offering to add it to the new campaign", async () => {
    const { applied, switchTo } = mount("knowledge");
    await screen.findByRole("heading", { name: "The Sunless Citadel" });

    fireEvent.click(await screen.findByRole("button", { name: /generate entries with ai/i }));
    fireEvent.change(screen.getByLabelText("What should be drafted?"), {
      target: { value: "a kobold" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Generate draft" }));
    // The draft for the Citadel is on screen, one click from being written.
    expect(await screen.findByRole("button", { name: "Add 1 entry" })).toBeInTheDocument();

    await switchTo("c2");
    await screen.findByRole("heading", { name: "Tides over Saltmarsh" });
    expect(await screen.findByText("Saltmarsh")).toBeInTheDocument();

    // The Citadel's draft is gone: nothing can apply it into Saltmarsh.
    expect(screen.queryByRole("button", { name: "Add 1 entry" })).not.toBeInTheDocument();
    expect(screen.queryByText("Meepo")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /generate entries with ai/i })).toBeInTheDocument();
    expect(applied).toEqual([]);
  });

  it("closes an open entry editor from the previous campaign", async () => {
    const { switchTo } = mount("knowledge");
    fireEvent.click(await screen.findByRole("button", { name: "Edit Oakhurst" }));
    const name = screen.getByLabelText("Name") as HTMLInputElement;
    expect(name.value).toBe("Oakhurst");
    fireEvent.change(name, { target: { value: "Oakhurst (burned)" } });

    await switchTo("c2");
    await screen.findByText("Saltmarsh");

    // Back to the blank "new entry" editor — not the Citadel's node, whose id
    // an UpdateNode from here would no longer resolve in this campaign.
    await waitFor(() => expect((screen.getByLabelText("Name") as HTMLInputElement).value).toBe(""));
    expect(screen.queryByDisplayValue("Oakhurst (burned)")).not.toBeInTheDocument();
  });

  it("re-selects a planning thread of the new campaign", async () => {
    const { switchTo } = mount("planning");
    expect(await screen.findAllByText("Citadel prep")).not.toHaveLength(0);

    await switchTo("c2");
    expect(await screen.findAllByText("Saltmarsh prep")).not.toHaveLength(0);
    expect(screen.queryByText("Citadel prep")).not.toBeInTheDocument();
  });

  it("does not remount when the first campaign id arrives", async () => {
    mount("knowledge");
    // Open the generator BEFORE anything else settles, then let every query
    // land: the card must still be open (undefined → id is a load, not a switch).
    fireEvent.click(await screen.findByRole("button", { name: /generate entries with ai/i }));
    await screen.findByRole("heading", { name: "The Sunless Citadel" });
    expect(screen.getByLabelText("What should be drafted?")).toBeInTheDocument();
  });
});
