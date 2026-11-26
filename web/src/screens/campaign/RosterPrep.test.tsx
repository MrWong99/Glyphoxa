import { describe, it, expect, vi } from "vitest";
import { act, render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";

import {
  AgentSchema,
  CampaignService,
  EdgeType,
  GetKnowledgeGraphResponseSchema,
  GetRosterReadinessResponseSchema,
  NodeType,
} from "@gen/glyphoxa/management/v1/management_pb";
import type { Agent } from "@gen/glyphoxa/management/v1/management_pb";
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
// Mira has everything on the agent side but no wiki entry at all.
const MIRA = create(AgentSchema, { id: "a2", name: "Mira", role: "character", persona: "Schemes.", voice: "v2" });

type GraphInit = Parameters<typeof create<typeof GetKnowledgeGraphResponseSchema>>[1];
type ReadinessInit = Parameters<typeof create<typeof GetRosterReadinessResponseSchema>>[1];

function renderPrep(
  opts: {
    roster?: Agent[];
    factCount?: number;
    lastSpokeAt?: Date;
    truncated?: boolean;
    graph?: GraphInit;
    readiness?: ReadinessInit;
    /** Fail the readiness read this many times before answering. */
    failReadiness?: number;
  } = {},
) {
  const onSelectAgent = vi.fn();
  const onOpenNode = vi.fn();
  const onOpenKnowledge = vi.fn();
  const onAddNpc = vi.fn();
  let failures = opts.failReadiness ?? 0;
  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      getKnowledgeGraph: () =>
        create(
          GetKnowledgeGraphResponseSchema,
          opts.graph ?? {
            nodes: [{ id: "bart", nodeType: NodeType.NPC, name: "Bart", agentId: "a1", bodyLen: 40 }],
            edges: [],
          },
        ),
      getRosterReadiness: () => {
        if (failures > 0) {
          failures -= 1;
          throw new ConnectError("readiness store offline", Code.Unavailable);
        }
        return create(
          GetRosterReadinessResponseSchema,
          opts.readiness ?? {
            agents: [
              {
                agentId: "a1",
                linked: true,
                factCount: opts.factCount ?? 3,
                chars: opts.truncated ? 4000 : 400,
                maxChars: 4000,
                truncated: opts.truncated ?? false,
                lastSpokeAt: opts.lastSpokeAt ? timestampFromDate(opts.lastSpokeAt) : undefined,
              },
            ],
          },
        );
      },
    });
  });
  const queryClient = makeQueryClient();
  // No backoff, and no automatic retries: a test that fails a read wants the
  // settled error, and wants to be the one that retries.
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <Providers transport={transport} queryClient={queryClient}>
      {/* The view's styles and structure hang off the Cast panel's root. */}
      <div className="gx-cast">
        <RosterPrep
          roster={opts.roster ?? [BUTLER, BART]}
          onSelectAgent={onSelectAgent}
          onOpenNode={onOpenNode}
          onOpenKnowledge={onOpenKnowledge}
          onAddNpc={onAddNpc}
        />
      </div>
    </Providers>,
  );
  return { onSelectAgent, onOpenNode, onOpenKnowledge, onAddNpc };
}

const showAll = () => fireEvent.click(screen.getByRole("button", { name: "All" }));

describe("RosterPrep", () => {
  // The AC is precise: present, not flagged.
  it("lists the Butler without grading it", async () => {
    renderPrep();
    expect(await screen.findByRole("button", { name: "Glyphoxa" })).toBeInTheDocument();
    expect(screen.getByText("Butler — always ready")).toBeInTheDocument();
    expect(screen.queryByLabelText(/Glyphoxa: Personality written/)).not.toBeInTheDocument();
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

describe("RosterPrep headline and modes", () => {
  // Bart lives in a town AND belongs to a guild, so the grouped view lists him
  // twice; Mira is not ready. Two NPCs, one ready — whatever the row count.
  const twoPlaces = {
    roster: [BUTLER, BART, MIRA],
    graph: {
      nodes: [
        { id: "bart", nodeType: NodeType.NPC, name: "Bart", agentId: "a1", bodyLen: 40 },
        { id: "town", nodeType: NodeType.LOCATION, name: "Saltmarsh" },
        { id: "guild", nodeType: NodeType.FACTION, name: "Smugglers" },
      ],
      edges: [
        { id: "e1", fromNodeId: "bart", toNodeId: "town", edgeType: EdgeType.RESIDES_IN },
        { id: "e2", fromNodeId: "bart", toNodeId: "guild", edgeType: EdgeType.MEMBER_OF },
      ],
    },
    // The read answers with a row for EVERY Character NPC, linked or not.
    readiness: {
      agents: [
        { agentId: "a1", linked: true, factCount: 3, chars: 400, maxChars: 4000 },
        { agentId: "a2", linked: false, maxChars: 4000 },
      ],
    },
  };

  it("counts unique NPCs in the headline, the Butler exempt", async () => {
    renderPrep(twoPlaces);
    expect(await screen.findByText("1 of 2 NPCs ready")).toBeInTheDocument();
  });

  it("opens on the to-do list while somebody is not ready, each NPC once with only failing checks", async () => {
    renderPrep(twoPlaces);
    const todo = await screen.findByRole("group", { name: "NPCs that need attention" });
    // The mode switch says where we are and how much there is to do.
    expect(screen.getByRole("button", { name: "Needs attention 1 not ready" })).toHaveAttribute("aria-pressed", "true");

    // Mira, once; not Bart (ready), not the Butler (exempt).
    expect(within(todo).getAllByRole("button", { name: "Mira" })).toHaveLength(1);
    expect(within(todo).queryByRole("button", { name: "Bart" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Glyphoxa" })).not.toBeInTheDocument();

    // Only what fails is listed: her personality and voice are fine.
    expect(within(todo).getByLabelText("Mira: Wiki entry linked — missing, fix it")).toBeInTheDocument();
    expect(within(todo).queryByLabelText(/Mira: Personality written/)).not.toBeInTheDocument();
    expect(within(todo).queryByLabelText(/Mira: Voice configured/)).not.toBeInTheDocument();
  });

  it("lists everyone under their places in All, the ready ones marked ready", async () => {
    renderPrep(twoPlaces);
    await screen.findByText("1 of 2 NPCs ready");
    showAll();

    // Bart appears under both of his places.
    const town = screen.getByRole("region", { name: "Saltmarsh" });
    const guild = screen.getByRole("region", { name: "Smugglers" });
    expect(within(town).getByRole("button", { name: "Bart" })).toBeInTheDocument();
    expect(within(guild).getByRole("button", { name: "Bart" })).toBeInTheDocument();

    // The two states are told apart on the row itself.
    const bartRow = within(town).getByRole("button", { name: "Bart" }).closest(".gx-prep__row");
    const miraRow = screen.getByRole("button", { name: "Mira" }).closest(".gx-prep__row");
    expect(bartRow).toHaveAttribute("data-ready", "true");
    expect(miraRow).toHaveAttribute("data-ready", "false");
  });

  it("opens on All once everyone is ready, and says so on the to-do list", async () => {
    renderPrep();
    expect(await screen.findByText("1 of 1 NPC ready")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "All" })).toHaveAttribute("aria-pressed", "true");

    fireEvent.click(screen.getByRole("button", { name: "Needs attention" }));
    expect(screen.getByText("Everyone is ready")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Show all" }));
    expect(screen.getByRole("button", { name: "Bart" })).toBeInTheDocument();
  });

  // A failing link check opens AN ENTRY when there is an obvious one to link:
  // the unlinked NPC entry that carries the agent's name.
  it("opens the same-named unlinked entry from a failing link check", async () => {
    const { onOpenNode, onOpenKnowledge } = renderPrep({
      roster: [BUTLER, MIRA],
      graph: { nodes: [{ id: "mira-entry", nodeType: NodeType.NPC, name: "Mira", agentId: "" }], edges: [] },
      readiness: { agents: [{ agentId: "a2", linked: false, maxChars: 4000 }] },
    });
    fireEvent.click(await screen.findByLabelText("Mira: Wiki entry linked — missing, fix it"));
    expect(onOpenNode).toHaveBeenCalledWith("mira-entry");
    expect(onOpenKnowledge).not.toHaveBeenCalled();
  });

  it("falls back to the World wiki when there is no entry to link", async () => {
    const { onOpenNode, onOpenKnowledge } = renderPrep({
      roster: [BUTLER, MIRA],
      graph: { nodes: [], edges: [] },
      readiness: { agents: [{ agentId: "a2", linked: false, maxChars: 4000 }] },
    });
    fireEvent.click(await screen.findByLabelText("Mira: Wiki entry linked — missing, fix it"));
    expect(onOpenKnowledge).toHaveBeenCalled();
    expect(onOpenNode).not.toHaveBeenCalled();
  });

  // An NPC created a moment ago is not in a readiness read that predates it.
  // Until its own row arrives it is graded on what the roster itself knows.
  it("does not grade an NPC against a readiness read that does not cover it yet", async () => {
    renderPrep({ roster: [BUTLER, BART, MIRA] });
    await screen.findByText("2 of 2 NPCs ready");
    showAll();
    expect(screen.getByLabelText(/Mira: Voice configured — done/)).toBeInTheDocument();
    expect(screen.queryByLabelText(/Mira: Wiki entry linked/)).not.toBeInTheDocument();
  });

  it("sends a personality or voice fix to the agent editor", async () => {
    const blank = create(AgentSchema, { id: "a3", name: "Blank", role: "character" });
    const { onSelectAgent } = renderPrep({
      roster: [BUTLER, blank],
      readiness: { agents: [{ agentId: "a3", linked: false, maxChars: 4000 }] },
    });
    fireEvent.click(await screen.findByLabelText("Blank: Voice configured — missing, fix it"));
    expect(onSelectAgent).toHaveBeenCalledWith("a3");
  });
});

describe("RosterPrep knowledge budget", () => {
  // The caps cut part of the NPC's neighbourhood: it still speaks (not a failing
  // check), but the GM should know, with the figures the read already carries.
  it("warns when an NPC's knowledge was trimmed, and opens its entry", async () => {
    const { onOpenNode } = renderPrep({ truncated: true });
    const warning = await screen.findByRole("button", { name: "Bart: Knowledge trimmed 4000/4000" });
    expect(warning).toHaveTextContent("Knowledge trimmed 4000/4000");
    // Trimmed is a warning, not a failure: Bart still counts as ready.
    expect(screen.getByText("1 of 1 NPC ready")).toBeInTheDocument();

    // The reason is one keyboard focus away…
    act(() => warning.focus());
    expect(screen.getByRole("tooltip")).toHaveTextContent(/more than fits into one reply/i);
    // …and the fix is in the entry.
    fireEvent.click(warning);
    expect(onOpenNode).toHaveBeenCalledWith("bart");
  });

  it("shows no trimmed warning when everything fits", async () => {
    renderPrep();
    await screen.findByText("1 of 1 NPC ready");
    expect(screen.queryByText(/Knowledge trimmed/)).not.toBeInTheDocument();
  });
});

describe("RosterPrep loading, failure and empty", () => {
  it("shows a loading placeholder until BOTH reads have landed", async () => {
    renderPrep();
    expect(screen.getByRole("status", { name: "Loading…" })).toBeInTheDocument();
    // Nothing is graded on half the picture.
    expect(screen.queryByText(/NPCs? ready/)).not.toBeInTheDocument();
    expect(await screen.findByText("1 of 1 NPC ready")).toBeInTheDocument();
    expect(screen.queryByRole("status", { name: "Loading…" })).not.toBeInTheDocument();
  });

  it("says why when a read fails, and recovers on Try again", async () => {
    renderPrep({ failReadiness: 1 });
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Couldn't load the readiness check");
    expect(alert).toHaveTextContent(/readiness store offline/);

    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("1 of 1 NPC ready")).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
  });

  it("offers to add an NPC when the cast is just the Butler", async () => {
    const { onAddNpc } = renderPrep({ roster: [BUTLER], graph: { nodes: [], edges: [] }, readiness: { agents: [] } });
    expect(await screen.findByText("No NPCs yet")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Add NPC" }));
    expect(onAddNpc).toHaveBeenCalled();
  });
});
