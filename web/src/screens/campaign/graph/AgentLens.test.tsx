import { describe, it, expect } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  AgentSchema,
  CampaignService,
  CreateEdgeResponseSchema,
  EdgeSchema,
  EdgeType,
  GetAgentFactPreviewResponseSchema,
  GetCampaignRosterResponseSchema,
  GraphEdgeSchema,
  GraphNodeSchema,
  NodeType,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { KnowledgeGraph } from "./KnowledgeGraph";

// Bart is linked to "bart"; his neighbourhood is the town (public, injected) and
// the bribe note (gm_private — adjacent but withheld). The guild is two hops away
// and never enters his context at all.
const BART = "agent-bart";

function fixture() {
  const nodes = [
    create(GraphNodeSchema, { id: "bart", nodeType: NodeType.NPC, name: "Bart", agentId: BART }),
    create(GraphNodeSchema, { id: "town", nodeType: NodeType.LOCATION, name: "Saltmarsh" }),
    create(GraphNodeSchema, {
      id: "secret",
      nodeType: NodeType.NOTE,
      name: "The bribe",
      gmPrivate: true,
    }),
    create(GraphNodeSchema, { id: "guild", nodeType: NodeType.FACTION, name: "Smugglers" }),
  ];
  const edges = [
    create(GraphEdgeSchema, {
      id: "e1",
      fromNodeId: "bart",
      toNodeId: "town",
      edgeType: EdgeType.RESIDES_IN,
    }),
    create(GraphEdgeSchema, {
      id: "e2",
      fromNodeId: "bart",
      toNodeId: "secret",
      edgeType: EdgeType.KNOWS,
    }),
    create(GraphEdgeSchema, {
      id: "e3",
      fromNodeId: "town",
      toNodeId: "guild",
      edgeType: EdgeType.MEMBER_OF,
    }),
  ];
  return { nodes, edges };
}

function renderWithLens(
  preview: Partial<{
    facts: string[];
    includedNodeIds: string[];
    droppedNodeIds: string[];
    chars: number;
    maxChars: number;
    maxFacts: number;
    truncated: boolean;
    linked: boolean;
    linkedNodeId: string;
  }> = {},
) {
  const { nodes, edges } = fixture();
  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      getCampaignRoster: () =>
        create(GetCampaignRosterResponseSchema, {
          roster: [
            create(AgentSchema, { id: "agent-butler", name: "Glyphoxa", role: "butler" }),
            create(AgentSchema, { id: BART, name: "Bart", role: "character" }),
          ],
        }),
      getAgentFactPreview: () =>
        create(GetAgentFactPreviewResponseSchema, {
          facts: ["### Bart (NPC)\nAn innkeeper.", "### Saltmarsh (Location)\nA damp town."],
          includedNodeIds: ["bart", "town"],
          droppedNodeIds: [],
          chars: 1840,
          maxChars: 4000,
          maxFacts: 20,
          truncated: false,
          linked: true,
          linkedNodeId: "bart",
          ...preview,
        }),
      createEdge: () => create(CreateEdgeResponseSchema, { edge: create(EdgeSchema, {}) }),
    });
  });
  render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <KnowledgeGraph
        nodes={nodes}
        edges={edges}
        selectedID={null}
        onSelectNode={() => {}}
        onGraphChanged={() => {}}
      />
    </Providers>,
  );
}

const pickBart = async () => {
  const select = await screen.findByLabelText("Show what an NPC knows");
  fireEvent.click(select);
  fireEvent.click(await screen.findByRole("option", { name: "Bart" }));
};

describe("agent-knowledge lens", () => {
  it("only offers Character NPCs — the Butler has no linked entry by design", async () => {
    renderWithLens();
    fireEvent.click(await screen.findByLabelText("Show what an NPC knows"));
    expect(await screen.findByRole("option", { name: "Bart" })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "Glyphoxa" })).not.toBeInTheDocument();
  });

  it("dims the graph to exactly the injected subgraph", async () => {
    renderWithLens();
    await pickBart();

    // Wait for the preview to land — the labels exist before the lens applies.
    await screen.findByText(/chars ·/);

    // Injected: lit. Two hops away: dimmed out of the way.
    expect(screen.getByLabelText(/^Bart \(NPC\)/)).toHaveAttribute("data-lens", "lit");
    expect(screen.getByLabelText(/^Saltmarsh/)).toHaveAttribute("data-lens", "lit");
    expect(screen.getByLabelText(/^Smugglers/)).toHaveAttribute("data-lens", "dim");
  });

  // The ghosts come from diffing the client's graph payload against the preview —
  // the prompt seam returns public Nodes only, by design, and widening it to
  // report what it hides would defeat the point of having a seam.
  it("marks a gm_private neighbour as withheld from this NPC", async () => {
    renderWithLens();
    await pickBart();

    const ghost = await screen.findByLabelText(/The bribe .*hidden from Bart/);
    expect(ghost).toHaveAttribute("data-lens", "ghost");
  });

  it("reports the real budget consumption", async () => {
    renderWithLens();
    await pickBart();
    expect(await screen.findByText(/1,840 \/ 4,000 chars · 2 \/ 20 facts/)).toBeInTheDocument();
  });

  // The prefix-stop is a silent quality cliff; the whole point of the lens is to
  // make the cut visible and actionable.
  it("names the entries the caps dropped", async () => {
    renderWithLens({
      includedNodeIds: ["bart"],
      droppedNodeIds: ["town"],
      truncated: true,
      facts: ["### Bart (NPC)\nAn innkeeper."],
    });
    await pickBart();

    expect(await screen.findByText(/Truncated — 1 entry did not fit/)).toBeInTheDocument();
    expect(screen.getByLabelText(/Saltmarsh.*over budget/)).toHaveAttribute("data-lens", "dropped");
  });

  it("says an unlinked NPC has no entry, rather than showing an empty graph", async () => {
    renderWithLens({ linked: false, linkedNodeId: "", facts: [], includedNodeIds: [] });
    await pickBart();

    expect(await screen.findByText(/is not linked to a wiki entry/)).toBeInTheDocument();
    // The graph is NOT dimmed to nothing — that would read as "the world is empty".
    expect(screen.getByLabelText(/^Smugglers/)).not.toHaveAttribute("data-lens");
  });

  it("keeps withheld neighbours visible even in table view", async () => {
    renderWithLens();
    await pickBart();
    fireEvent.click(screen.getByRole("button", { name: "Table view" }));
    // Table view hides gm_private entries; the lens exists to show what an NPC is
    // DENIED, so it must win while it is on.
    expect(await screen.findByLabelText(/The bribe .*hidden from Bart/)).toBeInTheDocument();
  });
});
