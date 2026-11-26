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
  opts: {
    onSelectNode?: (id: string) => void;
    onGraphChanged?: () => void;
    types?: ReadonlySet<NodeType>;
    matchIDs?: ReadonlySet<string> | null;
  } = {},
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
        types={opts.types}
        matchIDs={opts.matchIDs}
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
    const secret = node("The bribe (Note), GM-only");
    expect(secret.querySelector("circle")).toHaveAttribute("stroke-dasharray");
    expect(node("Bart (NPC)").querySelector("circle")).not.toHaveAttribute("stroke-dasharray");
  });

  it("table view removes gm_private nodes entirely", () => {
    renderGraph();
    expect(screen.queryByLabelText("The bribe (Note), GM-only")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Player view" }));
    expect(screen.queryByLabelText("The bribe (Note), GM-only")).not.toBeInTheDocument();
    expect(node("Bart (NPC)")).toBeInTheDocument();
  });

  // The type chips live in the wiki toolbar (shared with the list); the map
  // draws the types it is handed. None handed means no narrowing.
  it("draws only the types the toolbar chose", () => {
    renderGraph({ types: new Set([NodeType.NPC, NodeType.FACTION]) });
    expect(screen.queryByLabelText("Saltmarsh (Location)")).not.toBeInTheDocument();
    expect(node("Bart (NPC)")).toBeInTheDocument();
    expect(node("Smugglers (Faction)")).toBeInTheDocument();
    // The graph no longer carries a type filter of its own.
    expect(screen.queryByRole("group", { name: "Filter by type" })).not.toBeInTheDocument();
  });

  it("an empty type set draws every type", () => {
    renderGraph({ types: new Set() });
    expect(node("Saltmarsh (Location)")).toBeInTheDocument();
    expect(node("Bart (NPC)")).toBeInTheDocument();
  });

  // A search on the map answers "where is it": matches are named, the rest
  // recede, and nothing leaves the picture.
  it("labels search matches and dims the rest without removing them", () => {
    renderGraph({ matchIDs: new Set(["town"]) });
    const hit = node("Saltmarsh (Location), matches the search");
    expect(hit).toHaveAttribute("data-match", "hit");
    expect(node("Bart (NPC)")).toHaveAttribute("data-match", "miss");
    expect(within(hit).getByText("Saltmarsh")).toBeInTheDocument();
    expect(document.querySelectorAll(".gx-kg-graph__node")).toHaveLength(4);
  });

  it("a relation chip hides the relation but keeps its endpoints", () => {
    renderGraph();
    const chips = screen.getByRole("group", { name: "Filter by connection" });
    fireEvent.click(within(chips).getByRole("button", { name: "lives in" }));
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

    const picker = await screen.findByRole("group", { name: "Create connection" });
    expect(picker).toHaveTextContent("Bart");
    expect(picker).toHaveTextContent("Smugglers");

    fireEvent.click(screen.getByRole("button", { name: "Create connection" }));
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
    expect(screen.queryByRole("group", { name: "Create connection" })).not.toBeInTheDocument();
  });

  it("shows an empty state when no entry is of the chosen types", () => {
    renderGraph({ types: new Set([NodeType.ITEM]) });
    expect(screen.getByText(/no entry matches the filters above/i)).toBeInTheDocument();
  });

  // The relation picker offers only what the server will accept for THIS pair
  // (storage.ValidateEdge) and starts on the first of them. It used to start on
  // "lives in" for every pair, which is illegal for NPC → Faction.
  it("the relation picker offers only legal relations and never preselects an illegal one", async () => {
    renderGraph();
    fireEvent.mouseDown(node("Bart (NPC)"));
    fireEvent.mouseUp(node("Smugglers (Faction)"));
    const picker = await screen.findByRole("group", { name: "Create connection" });
    const select = within(picker).getByRole("combobox", { name: "Connection" });
    expect(select).toHaveTextContent("member of");
    fireEvent.click(select);
    const options = (await screen.findAllByRole("option")).map((o) => o.textContent);
    expect(options).toContain("member of");
    expect(options).toContain("knows");
    expect(options).not.toContain("lives in");
    expect(options).not.toContain("took part in");
    expect(options).not.toContain("parent of");
  });

  // The issue explicitly refused "a smaller test fixture" as the answer to
  // legibility at 200+ nodes, so this drives a real one: the filters and focus
  // mode must actually narrow it, labels must thin out, and zoom must exist for
  // what is left.
  describe("at 200+ nodes", () => {
    function bigFixture() {
      const nodes = [];
      const edges = [];
      for (let i = 0; i < 220; i++) {
        nodes.push(
          create(GraphNodeSchema, {
            id: `n${i}`,
            nodeType: i % 2 === 0 ? NodeType.NPC : NodeType.LOCATION,
            name: `Entry ${i}`,
          }),
        );
        if (i > 0) {
          edges.push(
            create(GraphEdgeSchema, {
              id: `e${i}`,
              fromNodeId: `n${i}`,
              toNodeId: `n${i - 1}`,
              edgeType: EdgeType.KNOWS,
            }),
          );
        }
      }
      return { nodes, edges };
    }

    function renderBig(types?: ReadonlySet<NodeType>) {
      const { nodes, edges } = bigFixture();
      const transport = createRouterTransport(({ service }) => {
        service(CampaignService, {
          createEdge: () => create(CreateEdgeResponseSchema, { edge: create(EdgeSchema, {}) }),
        });
      });
      const queryClient = makeQueryClient();
      const tree = (chosen?: ReadonlySet<NodeType>) => (
        <Providers transport={transport} queryClient={queryClient}>
          <KnowledgeGraph
            nodes={nodes}
            edges={edges}
            selectedID={null}
            types={chosen}
            onSelectNode={() => {}}
            onGraphChanged={() => {}}
          />
        </Providers>
      );
      const view = render(tree(types));
      return { rerender: (chosen?: ReadonlySet<NodeType>) => view.rerender(tree(chosen)) };
    }

    it("draws every node but drops the label soup", () => {
      renderBig();
      expect(document.querySelectorAll(".gx-kg-graph__node")).toHaveLength(220);
      // Past the label budget only the actively-looked-at node is named; 220 labels
      // on one canvas is the actual failure mode at this size.
      expect(document.querySelectorAll(".gx-kg-graph__label")).toHaveLength(0);
    });

    it("a type chip halves it and focus mode cuts it to a neighbourhood", () => {
      const { rerender } = renderBig(new Set([NodeType.NPC]));
      expect(document.querySelectorAll(".gx-kg-graph__node")).toHaveLength(110);

      rerender(); // every type again
      fireEvent.doubleClick(screen.getByLabelText("Entry 100 (NPC)"));
      // A chain graph: depth 1 around one node is itself plus two neighbours.
      expect(document.querySelectorAll(".gx-kg-graph__node")).toHaveLength(3);
      // The focus root keeps its name even past the label budget.
      expect(screen.getByText("Entry 100")).toBeInTheDocument();
    });

    it("offers zoom so the surviving glyphs are readable, and a way back", () => {
      renderBig();
      const svg = document.querySelector(".gx-kg-graph__canvas")!;
      const fitted = svg.getAttribute("viewBox");

      fireEvent.click(screen.getByRole("button", { name: "Zoom in" }));
      expect(svg.getAttribute("viewBox")).not.toBe(fitted);

      fireEvent.click(screen.getByRole("button", { name: "Fit" }));
      expect(svg.getAttribute("viewBox")).toBe(fitted);
    });
  });
});
