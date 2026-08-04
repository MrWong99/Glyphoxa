import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  ApproveKnowledgeProposalResponseSchema,
  CampaignService,
  EdgeType,
  GraphEdgeSchema,
  GraphNodeSchema,
  ListKnowledgeProposalsResponseSchema,
  ListSimilarKnowledgeResponseSchema,
  NodeType,
  RejectKnowledgeProposalResponseSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { KnowledgeGraph } from "./KnowledgeGraph";

// Pending proposals drawn in place on the graph (#537, ADR-0052).
//
// The point of this slice is that the GM answers "does this belong HERE, next to
// what the world already says" — so what is tested is that the suggestion is
// visibly distinct from canon, reachable, reviewable through the existing RPCs,
// and that reviewing one does not move the world underneath it.

const NODES = [
  create(GraphNodeSchema, { id: "bart", nodeType: NodeType.NPC, name: "Bart" }),
  create(GraphNodeSchema, { id: "town", nodeType: NodeType.LOCATION, name: "Saltmarsh" }),
];
const EDGES = [
  create(GraphEdgeSchema, {
    id: "e1",
    fromNodeId: "bart",
    toNodeId: "town",
    edgeType: EdgeType.RESIDES_IN,
  }),
];

const FACT_PROPOSAL = {
  id: "p-fact",
  authoringAgentName: "Bart",
  write: {
    case: "fact" as const,
    value: { nodeId: "", subject: "Bart", fact: "keeps a ledger under the bar", aspectKey: "Rumour" },
  },
};

const EDGE_PROPOSAL = {
  id: "p-edge",
  authoringAgentName: "Glyphoxa",
  write: {
    case: "edge" as const,
    value: { nodeId: "", subject: "Bart", relation: EdgeType.MEMBER_OF, target: "Smugglers" },
  },
};

const NODE_PROPOSAL = {
  id: "p-node",
  authoringAgentName: "Glyphoxa",
  write: {
    case: "node" as const,
    value: { nodeType: NodeType.LOCATION, name: "The Sunken Chapel", body: "Half-drowned." },
  },
};

function renderGraph(proposals: unknown[] = [FACT_PROPOSAL], nodes = NODES) {
  const approve = vi.fn((_req: { id: string }) => create(ApproveKnowledgeProposalResponseSchema, {}));
  const reject = vi.fn(() => create(RejectKnowledgeProposalResponseSchema, {}));
  const similar = vi.fn(() => create(ListSimilarKnowledgeResponseSchema, { nodes: [] }));
  const onGraphChanged = vi.fn();
  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      listKnowledgeProposals: () =>
        create(ListKnowledgeProposalsResponseSchema, {
          proposals: proposals as never,
        }),
      approveKnowledgeProposal: approve,
      rejectKnowledgeProposal: reject,
      listSimilarKnowledge: similar,
    });
  });
  const queryClient = makeQueryClient();
  const view = (ns: typeof NODES) => (
    <Providers transport={transport} queryClient={queryClient}>
      <KnowledgeGraph
        nodes={ns}
        edges={EDGES}
        selectedID={null}
        onSelectNode={() => {}}
        onGraphChanged={onGraphChanged}
      />
    </Providers>
  );
  const { rerender } = render(view(nodes));
  return {
    approve,
    reject,
    similar,
    onGraphChanged,
    /** Re-render with a changed payload, as a post-approval refetch would. */
    withNodes: (ns: typeof NODES) => rerender(view(ns)),
  };
}

/** Every committed node's id and rendered transform, for a before/after compare. */
function nodePositions() {
  return screen
    .getAllByRole("button")
    .filter((el) => el.hasAttribute("data-node-id"))
    .map((el) => [el.getAttribute("data-node-id"), el.getAttribute("transform")]);
}

describe("proposals on the graph", () => {
  it("a proposed entry is drawn, and is never mistakable for canon", async () => {
    renderGraph([NODE_PROPOSAL]);
    const ghost = await screen.findByRole("button", { name: /Suggested new entry The Sunken Chapel/ });
    // data-proposed is what the dashed, hollow styling hangs off. A suggestion
    // that renders identically to committed canon defeats the review queue.
    expect(ghost).toHaveAttribute("data-proposed");

    const committed = screen.getByRole("button", { name: /^Bart \(/ });
    expect(committed).not.toHaveAttribute("data-proposed");
  });

  it("names the proposing Agent on the ghost itself", async () => {
    renderGraph([NODE_PROPOSAL]);
    // WHO proposed is most of the judgement: a Character NPC may only propose on
    // its own linked Node, the Butler campaign-wide.
    const ghost = await screen.findByRole("button", { name: /from Glyphoxa/ });
    expect(ghost).toBeInTheDocument();
  });

  it("clicking a ghost opens the review with the write, the author and the similarity hint", async () => {
    const { similar } = renderGraph([FACT_PROPOSAL]);
    fireEvent.click(await screen.findByRole("button", { name: /Suggested fact from Bart/ }));

    const dialog = await screen.findByRole("dialog", { name: /Review suggestion/ });
    // "Bart" appears twice on purpose: once as the AUTHOR and once as the
    // proposal's subject. A Character NPC may only propose on its own linked Node
    // (ADR-0052), so that is the realistic shape — and both lines must be there,
    // because "who suggested this" is most of the judgement.
    expect(within(dialog).getAllByText("Bart")).toHaveLength(2);
    expect(within(dialog).getByText(/keeps a ledger under the bar/)).toBeInTheDocument();

    // The ADR-0011 similarity hint travels with the proposal rather than being
    // dropped for the visual — and stays LAZY, so opening a card costs no Embed.
    expect(similar).not.toHaveBeenCalled();
    fireEvent.click(within(dialog).getByRole("button", { name: /Show similar entries/ }));
    await waitFor(() => expect(similar).toHaveBeenCalled());
  });

  it("approving goes through the existing RPC and refreshes both the queue and the graph", async () => {
    const { approve, onGraphChanged } = renderGraph([FACT_PROPOSAL]);
    fireEvent.click(await screen.findByRole("button", { name: /Suggested fact from Bart/ }));
    const dialog = await screen.findByRole("dialog", { name: /Review suggestion/ });

    fireEvent.click(within(dialog).getByRole("button", { name: "Approve" }));

    await waitFor(() => expect(approve).toHaveBeenCalled());
    expect(approve.mock.calls[0][0].id).toBe("p-fact");
    // ADR-0052 is untouched: no new write path, and the same invalidation the
    // queue does — an approved write lands a Node or Edge every KG read now shows.
    await waitFor(() => expect(onGraphChanged).toHaveBeenCalled());
  });

  it("rejecting asks first — a dropped suggestion never becomes canon", async () => {
    const { reject } = renderGraph([FACT_PROPOSAL]);
    fireEvent.click(await screen.findByRole("button", { name: /Suggested fact from Bart/ }));
    const dialog = await screen.findByRole("dialog", { name: /Review suggestion/ });

    fireEvent.click(within(dialog).getByRole("button", { name: "Reject" }));
    expect(reject).not.toHaveBeenCalled();
    fireEvent.click(await screen.findByRole("button", { name: "Reject suggestion" }));
    await waitFor(() => expect(reject).toHaveBeenCalled());
  });

  it("approving does not move a single committed entry", async () => {
    const { approve, withNodes } = renderGraph([NODE_PROPOSAL]);
    const before = nodePositions();
    expect(before.length).toBeGreaterThan(0);

    fireEvent.click(await screen.findByRole("button", { name: /Suggested new entry/ }));
    const dialog = await screen.findByRole("dialog", { name: /Review suggestion/ });
    fireEvent.click(within(dialog).getByRole("button", { name: "Approve" }));
    await waitFor(() => expect(approve).toHaveBeenCalled());

    // THE POINT. Approving lands a new Node, so the next payload is bigger — and
    // `layout` is pure in its inputs, so a bigger payload reshuffles everything
    // unless the positions are pinned. Without the re-render this assertion would
    // be vacuous: the props never change, so nothing could move anyway.
    withNodes([
      ...NODES,
      create(GraphNodeSchema, {
        id: "fresh-uuid",
        nodeType: NodeType.LOCATION,
        name: "The Sunken Chapel",
      }),
    ]);
    await screen.findByRole("button", { name: /^The Sunken Chapel \(/ });

    // A review session where the graph jumps on every approval is not a review
    // session (AC3).
    expect(nodePositions().filter(([id]) => id !== "fresh-uuid")).toEqual(before);
  });

  it("the approved entry lands exactly where its ghost stood", async () => {
    const { approve, withNodes } = renderGraph([NODE_PROPOSAL]);
    const ghost = await screen.findByRole("button", { name: /Suggested new entry The Sunken Chapel/ });
    const ghostAt = ghost.getAttribute("transform");
    expect(ghostAt).toBeTruthy();

    fireEvent.click(ghost);
    const dialog = await screen.findByRole("dialog", { name: /Review suggestion/ });
    fireEvent.click(within(dialog).getByRole("button", { name: "Approve" }));
    await waitFor(() => expect(approve).toHaveBeenCalled());

    withNodes([
      ...NODES,
      create(GraphNodeSchema, {
        id: "fresh-uuid",
        nodeType: NodeType.LOCATION,
        name: "The Sunken Chapel",
      }),
    ]);
    const solid = await screen.findByRole("button", { name: /^The Sunken Chapel \(/ });
    // The dashed circle became a solid one IN PLACE. The server minted a fresh id
    // at approval, so the name is the only thing that carried the position across.
    expect(solid.getAttribute("transform")).toBe(ghostAt);
  });

  it("a suggestion naming an entry that does not exist is listed, not dropped", async () => {
    renderGraph([EDGE_PROPOSAL]);
    // "Smugglers" is in no entry, so the edge cannot be drawn — and approving it
    // WILL be refused, which is worth knowing before clicking.
    const strip = await screen.findByText(/can't be drawn here/);
    expect(strip).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Glyphoxa" }));
    const dialog = await screen.findByRole("dialog", { name: /Review suggestion/ });
    expect(within(dialog).getByText(/Approving will be refused/)).toBeInTheDocument();
  });

  it("the suggestions toggle takes every ghost off the canvas", async () => {
    renderGraph([NODE_PROPOSAL]);
    expect(
      await screen.findByRole("button", { name: /Suggested new entry The Sunken Chapel/ }),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /Suggestions \(1\)/ }));
    await waitFor(() =>
      expect(
        screen.queryByRole("button", { name: /Suggested new entry The Sunken Chapel/ }),
      ).toBeNull(),
    );
    // And the committed graph is still there — the toggle hides suggestions, not
    // the world.
    expect(screen.getByRole("button", { name: /^Bart \(/ })).toBeInTheDocument();
  });

  it("shows no suggestions affordance when the queue is empty", async () => {
    renderGraph([]);
    await screen.findByRole("button", { name: /^Bart \(/ });
    expect(screen.queryByRole("button", { name: /Suggestions \(/ })).toBeNull();
    expect(screen.queryByText(/can't be drawn here/)).toBeNull();
  });
});
