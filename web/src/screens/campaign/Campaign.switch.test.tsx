import { describe, it, expect, vi } from "vitest";
import { act, render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { toast } from "sonner";

import {
  CampaignService,
  ChatService,
  SessionService,
  VoiceService,
  AgentSchema,
  CampaignSchema,
  DraftNodeSchema,
  GetActiveCampaignResponseSchema,
  GetKnowledgeGraphResponseSchema,
  GetSessionResponseSchema,
  KnowledgeProposalSchema,
  ListKnowledgeProposalsResponseSchema,
  ProposedFactSchema,
  RejectKnowledgeProposalResponseSchema,
  ListCampaignsResponseSchema,
  SetActiveCampaignResponseSchema,
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
import { MemoryScreenNav, type ScreenLocation } from "@/app/screenNav";
import { CampaignSwitcher } from "@/components/CampaignSwitcher";
import { invalidateActiveCampaignScopedQueries } from "@/lib/campaignCache";
import { hasUnsavedEdits } from "@/lib/navigationGuard";
import { makeQueryClient } from "@/lib/queryClient";
import { Campaign } from "./Campaign";
import type { CampaignView } from "./views";

// The toast is the switch's feedback; the shell's <Toaster> is not mounted here.
// It is callable (and dismissable) too: the Suggestions inbox's Undo toast.
vi.mock("sonner", () => ({
  toast: Object.assign(vi.fn(), { error: vi.fn(), success: vi.fn(), info: vi.fn(), dismiss: vi.fn() }),
}));

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

// A selection held in the URL names a row of ONE campaign. After a switch it
// would point the new campaign's view at an id that is not in it.
describe("URL selection across an Active Campaign switch", () => {
  it("drops the previous campaign's selection params with the rest of its state", async () => {
    const h = twoCampaignTransport();
    const queryClient = makeQueryClient();
    const moves: { next: ScreenLocation; replace: boolean }[] = [];
    render(
      <Providers transport={h.transport} queryClient={queryClient}>
        <MemoryScreenNav
          initial={{ screen: "campaign", view: "planning", search: { thread: "thread-c1" } }}
          onNavigate={(next, opts) => moves.push({ next, replace: opts.replace })}
        >
          {(loc) => loc.view && <Campaign view={loc.view} />}
        </MemoryScreenNav>
      </Providers>,
    );
    expect(await screen.findAllByText("Citadel prep")).not.toHaveLength(0);
    // Loading the first campaign is not a switch: the selection stays.
    expect(moves).toEqual([]);

    h.setActive("c2");
    await act(async () => {
      await invalidateActiveCampaignScopedQueries(queryClient);
    });
    expect(await screen.findAllByText("Saltmarsh prep")).not.toHaveLength(0);

    // Stripped by replace, on the same view.
    await waitFor(() =>
      expect(moves).toEqual([
        { next: { screen: "campaign", view: "planning", search: {} }, replace: true },
      ]),
    );
  });
});

// The acceptance path of the guarded switch, end to end: the real topbar
// switcher beside the real Cast editor.
describe("switching campaign with unsaved cast edits", () => {
  function mountWithSwitcher() {
    let active: "c1" | "c2" = "c1";
    const names = { c1: "The Sunless Citadel", c2: "Tides over Saltmarsh" };
    const campaign = (id: "c1" | "c2") =>
      create(CampaignSchema, { id, name: names[id], system: "5e", language: "en" });
    const switches: string[] = [];
    const transport = createRouterTransport(({ service }) => {
      service(VoiceService, { listVoices: () => create(ListVoicesResponseSchema, { voices: [] }) });
      service(SessionService, { getSession: () => create(GetSessionResponseSchema, { active: false }) });
      service(CampaignService, {
        listCampaigns: () =>
          create(ListCampaignsResponseSchema, { campaigns: [campaign("c1"), campaign("c2")] }),
        getActiveCampaign: () => create(GetActiveCampaignResponseSchema, { campaign: campaign(active) }),
        setActiveCampaign: (req) => {
          switches.push(req.campaignId);
          active = req.campaignId as "c1" | "c2";
          return create(SetActiveCampaignResponseSchema, { campaign: campaign(active) });
        },
        getCampaignRoster: () =>
          create(GetCampaignRosterResponseSchema, {
            campaign: campaign(active),
            roster: [
              create(AgentSchema, {
                id: `butler-${active}`,
                campaignId: active,
                role: "butler",
                name: "Glyphoxa",
                persona: active === "c1" ? "Keeper of the Citadel." : "Keeper of the tides.",
                addressOnly: true,
              }),
            ],
          }),
      });
    });
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <CampaignSwitcher />
        <Campaign view="cast" />
      </Providers>,
    );
    return { switches };
  }

  const persona = () => screen.getByLabelText("Personality & backstory") as HTMLTextAreaElement;

  async function typeAndPickSaltmarsh() {
    await waitFor(() => expect(persona().value).toBe("Keeper of the Citadel."));
    fireEvent.change(persona(), { target: { value: "Keeper of the Citadel, and tired of it." } });
    fireEvent.click(screen.getByTestId("campaign-switcher-trigger"));
    const list = await screen.findByRole("group", { name: /campaigns/i });
    fireEvent.click(await within(list).findByRole("button", { name: /Tides over Saltmarsh/ }));
  }

  it("asks first, and Cancel keeps both the campaign and the edits", async () => {
    vi.mocked(toast.success).mockClear();
    const { switches } = mountWithSwitcher();
    await typeAndPickSaltmarsh();

    expect(await screen.findByText("Discard unsaved changes?")).toBeInTheDocument();
    expect(switches).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByText("Discard unsaved changes?")).not.toBeInTheDocument());
    expect(switches).toEqual([]);
    expect(screen.getByRole("heading", { level: 1, name: "The Sunless Citadel" })).toBeInTheDocument();
    expect(persona().value).toBe("Keeper of the Citadel, and tired of it.");
    expect(vi.mocked(toast.success)).not.toHaveBeenCalled();
  });

  it("switches on confirm, toasts the new campaign's name, and shows ITS cast", async () => {
    vi.mocked(toast.success).mockClear();
    const { switches } = mountWithSwitcher();
    await typeAndPickSaltmarsh();

    fireEvent.click(await screen.findByRole("button", { name: "Discard changes" }));

    await waitFor(() => expect(switches).toEqual(["c2"]));
    await waitFor(() =>
      expect(vi.mocked(toast.success)).toHaveBeenCalledWith("Now working in Tides over Saltmarsh"),
    );
    expect(
      await screen.findByRole("heading", { level: 1, name: "Tides over Saltmarsh" }),
    ).toBeInTheDocument();
    // The editor is the new campaign's Butler — the discarded text is gone, and
    // nothing of it can be saved into Saltmarsh.
    await waitFor(() => expect(persona().value).toBe("Keeper of the tides."));
    expect(screen.queryByDisplayValue("Keeper of the Citadel, and tired of it.")).not.toBeInTheDocument();
    expect(hasUnsavedEdits()).toBe(false);
  });
});

// A dismissal in the Suggestions inbox waits out its Undo window before the
// reject is sent — and a reject carries no campaign id: the server resolves it
// against whatever campaign is active when it arrives. A switch inside the
// window must send it while the OLD campaign is still the active one.
describe("switching campaign while a dismissal waits for Undo", () => {
  it("sends the dismissal to the old campaign before the switch is written", async () => {
    vi.mocked(toast.error).mockClear();
    let active: "c1" | "c2" = "c1";
    const names = { c1: "The Sunless Citadel", c2: "Tides over Saltmarsh" };
    const campaign = (id: "c1" | "c2") =>
      create(CampaignSchema, { id, name: names[id], system: "5e", language: "en" });
    const pending: Record<"c1" | "c2", string[]> = { c1: ["p-bart"], c2: [] };
    // What reached the server, in order, and in which campaign.
    const log: string[] = [];
    const transport = createRouterTransport(({ service }) => {
      service(SessionService, { getSession: () => create(GetSessionResponseSchema, { active: false }) });
      service(CampaignService, {
        listCampaigns: () =>
          create(ListCampaignsResponseSchema, { campaigns: [campaign("c1"), campaign("c2")] }),
        getActiveCampaign: () => create(GetActiveCampaignResponseSchema, { campaign: campaign(active) }),
        setActiveCampaign: (req) => {
          log.push(`switch ${req.campaignId}`);
          active = req.campaignId as "c1" | "c2";
          return create(SetActiveCampaignResponseSchema, { campaign: campaign(active) });
        },
        getCampaignRoster: () =>
          create(GetCampaignRosterResponseSchema, {
            campaign: campaign(active),
            roster: [
              create(AgentSchema, { id: `butler-${active}`, campaignId: active, role: "butler", name: "Glyphoxa" }),
            ],
          }),
        listKnowledgeProposals: () =>
          create(ListKnowledgeProposalsResponseSchema, {
            proposals: pending[active].map((id) =>
              create(KnowledgeProposalSchema, {
                id,
                authoringAgentName: "Glyphoxa",
                write: { case: "fact", value: create(ProposedFactSchema, { subject: "Bart", fact: "He fears the dark." }) },
              }),
            ),
          }),
        getKnowledgeGraph: () => create(GetKnowledgeGraphResponseSchema, { nodes: [] }),
        rejectKnowledgeProposal: (req) => {
          // The store's WHERE campaign_id = <active>: a proposal of another
          // campaign is not found.
          log.push(`reject ${req.id} in ${active}`);
          if (!pending[active].includes(req.id)) throw new ConnectError("proposal not found", Code.NotFound);
          pending[active] = pending[active].filter((id) => id !== req.id);
          return create(RejectKnowledgeProposalResponseSchema, {});
        },
      });
    });
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <CampaignSwitcher />
        <Campaign view="proposals" />
      </Providers>,
    );

    await screen.findByText(/He fears the dark/);
    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    // Waiting out its Undo window (8 s): nothing sent yet.
    expect(log).toEqual([]);

    fireEvent.click(screen.getByTestId("campaign-switcher-trigger"));
    const list = await screen.findByRole("group", { name: /campaigns/i });
    fireEvent.click(await within(list).findByRole("button", { name: /Tides over Saltmarsh/ }));

    await waitFor(() => expect(log).toEqual(["reject p-bart in c1", "switch c2"]));
    expect(pending.c1).toEqual([]);
    expect(
      await screen.findByRole("heading", { level: 1, name: "Tides over Saltmarsh" }),
    ).toBeInTheDocument();
    // Nothing more is sent on the remount, and nothing failed.
    await new Promise((r) => setTimeout(r, 30));
    expect(log).toEqual(["reject p-bart in c1", "switch c2"]);
    expect(vi.mocked(toast.error)).not.toHaveBeenCalled();
  });
});
