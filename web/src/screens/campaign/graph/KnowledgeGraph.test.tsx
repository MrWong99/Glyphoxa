import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  CampaignService,
  CreateEdgeResponseSchema,
  EdgeSchema,
  EdgeType,
  GraphEdgeSchema,
  GraphNodeSchema,
  NodeType,
  type CreateEdgeRequest,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { KnowledgeGraph } from "./KnowledgeGraph";

function fixture() {
  const nodes = [
    create(GraphNodeSchema, { id: "bart", nodeType: NodeType.NPC, name: "Bart" }),
    create(GraphNodeSchema, { id: "town", nodeType: NodeType.LOCATION, name: "Saltmarsh" }),
    create(GraphNodeSchema, { id: "guild", nodeType: NodeType.FACTION, name: "Smugglers" }),
    create(GraphNodeSchema, {
      id: "secret",
      nodeType: NodeType.NOTE,
      name: "The bribe",
      gmPrivate: true,
    }),
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
      fromNodeId: "town",
      toNodeId: "guild",
      edgeType: EdgeType.MEMBER_OF,
    }),
    create(GraphEdgeSchema, {
      id: "e3",
      fromNodeId: "guild",
      toNodeId: "secret",
      edgeType: EdgeType.KNOWS,
    }),
  ];
  return { nodes, edges };
}

function renderGraph(
  opts: { onSelectNode?: (id: string) => void; onGraphChanged?: () => void } = {},
) {
  const { nodes, edges } = fixture();
  const createCalls: CreateEdgeRequest[] = [];
  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      createEdge: (req) => {
        createCalls.push(req);
        return create(CreateEdgeResponseSchema, {
          edge: create(EdgeSchema, {
            id: "new",
            fromNodeId: req.fromNodeId,
            toNodeId: req.toNodeId,
            edgeType: req.edgeType,
          }),
        });
      },
    });
  });
  render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <KnowledgeGraph
        nodes={nodes}
        edges={edges}
        selectedID={null}
        onSelectNode={opts.onSelectNode ?? (() => {})}
        onGraphChanged={opts.onGraphChanged ?? (() => {})}
      />
    </Providers>,
  );
  return { createCalls, nodes, edges };
}

// node() finds a rendered node glyph by its accessible label.
const node = (name: string, suffix = "") => screen.getByLabelText(`${name}${suffix}`);

describe("KnowledgeGraph", () => {
  it("draws every node and edge", () => {
    const { container } = { container: document.body };
    renderGraph();
    expect(node("Bart (NPC)")).toBeInTheDocument();
    expect(node("Saltmarsh (Location)")).toBeInTheDocument();
    expect(container.querySelectorAll(".gx-kg-graph__edge")).toHaveLength(3);
  });

  // The graph is a navigation surface, not a second editor (#534): a click has to
  // hand off to the EXISTING side-rail editor.
  it("clicking a node asks the panel to open the existing editor", () => {
    const onSelectNode = vi.fn();
    renderGraph({ onSelectNode });
    fireEvent.click(node("Bart (NPC)"));
    expect(onSelectNode).toHaveBeenCalledWith("bart");
  });

  it("is keyboard reachable", () => {
    const onSelectNode = vi.fn();
    renderGraph({ onSelectNode });
    fireEvent.keyDown(node("Saltmarsh (Location)"), { key: "Enter" });
    expect(onSelectNode).toHaveBeenCalledWith("town");
  });

  // gm_private is PRESENT in the GM's map but visibly not part of the world the
  // NPCs see — dashed here, gone entirely in table view.
  it("draws gm_private nodes dashed and labels them as private", () => {
    renderGraph();
    const secret = node("The bribe (Note), GM private");
    expect(secret.querySelector("circle")).toHaveAttribute("stroke-dasharray");
    expect(node("Bart (NPC)").querySelector("circle")).not.toHaveAttribute("stroke-dasharray");
  });

  it("table view removes gm_private nodes entirely", () => {
    renderGraph();
    expect(screen.queryByLabelText("The bribe (Note), GM private")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Table view" }));
    expect(screen.queryByLabelText("The bribe (Note), GM private")).not.toBeInTheDocument();
    expect(node("Bart (NPC)")).toBeInTheDocument();
  });

  it("a type chip filters that type out of the drawing", () => {
    renderGraph();
    const chips = screen.getByRole("group", { name: "Filter by type" });
    fireEvent.click(within(chips).getByRole("button", { name: "Location" }));
    expect(screen.queryByLabelText("Saltmarsh (Location)")).not.toBeInTheDocument();
    expect(node("Bart (NPC)")).toBeInTheDocument();
  });

  it("a relation chip hides the relation but keeps its endpoints", () => {
    renderGraph();
    const chips = screen.getByRole("group", { name: "Filter by relation" });
    fireEvent.click(within(chips).getByRole("button", { name: "resides_in" }));
    expect(document.querySelectorAll('.gx-kg-graph__edge[data-relation="resides_in"]')).toHaveLength(
      0,
    );
    expect(node("Saltmarsh (Location)")).toBeInTheDocument();
  });

  // Focus mode at depth 1 is exactly the AgentNodeFacts walk, so it answers "what
  // would this entry's NPC actually see".
  it("focus mode narrows to the ego network and can be cleared", () => {
    renderGraph();
    fireEvent.doubleClick(node("Bart (NPC)"));
    expect(node("Saltmarsh (Location)")).toBeInTheDocument();
    expect(screen.queryByLabelText("Smugglers (Faction)")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Clear focus" }));
    expect(screen.getByLabelText("Smugglers (Faction)")).toBeInTheDocument();
  });

  it("focus depth 2 reaches one hop further", () => {
    renderGraph();
    fireEvent.doubleClick(node("Smugglers (Faction)"));
    expect(screen.queryByLabelText("Bart (NPC)")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Depth 2" }));
    expect(screen.getByLabelText("Bart (NPC)")).toBeInTheDocument();
  });

  // Building the graph ON the graph is the thing that fixes the authoring
  // friction the epic opens with: edges were authorable only through a dropdown
  // pair buried in a per-node card.
  it("dragging one node onto another creates an edge through the existing RPC", async () => {
    const onGraphChanged = vi.fn();
    const { createCalls } = renderGraph({ onGraphChanged });

    fireEvent.mouseDown(node("Bart (NPC)"));
    fireEvent.mouseUp(node("Smugglers (Faction)"));

    const picker = await screen.findByRole("dialog", { name: "Create relationship" });
    expect(picker).toHaveTextContent("Bart");
    expect(picker).toHaveTextContent("Smugglers");

    fireEvent.click(screen.getByRole("button", { name: "Create relationship" }));
    await waitFor(() => expect(createCalls).toHaveLength(1));
    expect(createCalls[0].fromNodeId).toBe("bart");
    expect(createCalls[0].toNodeId).toBe("guild");
    await waitFor(() => expect(onGraphChanged).toHaveBeenCalled());
  });

  // A press and release on the SAME node is a click, not a self-edge — self-edges
  // are refused server-side and offering the picker for one is just noise.
  it("pressing and releasing the same node does not offer the relation picker", () => {
    renderGraph();
    fireEvent.mouseDown(node("Bart (NPC)"));
    fireEvent.mouseUp(node("Bart (NPC)"));
    expect(screen.queryByRole("dialog", { name: "Create relationship" })).not.toBeInTheDocument();
  });

  it("shows an empty state when every type is filtered out", () => {
    renderGraph();
    const chips = screen.getByRole("group", { name: "Filter by type" });
    for (const label of ["Character", "NPC", "Location", "Faction", "Item", "Plot thread", "Note"]) {
      fireEvent.click(within(chips).getByRole("button", { name: label }));
    }
    expect(screen.getByText(/every entry is filtered out/i)).toBeInTheDocument();
  });
});
