import { describe, it, expect } from "vitest";
import { render, screen, fireEvent, within, waitFor } from "@testing-library/react";
import { createRouterTransport, ConnectError, Code } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  CampaignService,
  NodeSchema,
  NodeType,
  EdgeType,
  EdgeSchema,
  AgentSchema,
  ListNodeEdgesResponseSchema,
  ListNodesResponseSchema,
  GetCampaignRosterResponseSchema,
  CreateEdgeResponseSchema,
  DeleteEdgeResponseSchema,
  SetNodeAgentResponseSchema,
  type Node as PbNode,
  type Edge as PbEdge,
  type CreateEdgeRequest,
  type DeleteEdgeRequest,
  type SetNodeAgentRequest,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { NodeRelations } from "./NodeRelations";

// An in-memory KG-edge store served over a router transport: listNodeEdges reads
// the outgoing/incoming closures so create/delete round-trip through
// invalidate → refetch; the recorded requests prove the chosen EdgeType, the
// target id, the deleted edge id, and the linked agent id reach the wire.
function edgeTransport(opts: {
  node: PbNode;
  outgoing?: PbEdge[];
  incoming?: PbEdge[];
  others?: PbNode[];
  setAgentError?: boolean;
  deleteError?: boolean;
}) {
  const outgoing = opts.outgoing ?? [];
  const incoming = opts.incoming ?? [];
  const others = opts.others ?? [];
  const createCalls: CreateEdgeRequest[] = [];
  const deleteCalls: DeleteEdgeRequest[] = [];
  const setAgentCalls: SetNodeAgentRequest[] = [];

  const butler = create(AgentSchema, { id: "butler", role: "butler", name: "Glyphoxa" });
  const cast = [
    create(AgentSchema, { id: "ag1", role: "character", name: "Bart" }),
    create(AgentSchema, { id: "ag2", role: "character", name: "Ana" }),
  ];

  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      listNodeEdges: () => create(ListNodeEdgesResponseSchema, { outgoing, incoming }),
      listNodes: () => create(ListNodesResponseSchema, { nodes: [opts.node, ...others] }),
      getCampaignRoster: () =>
        create(GetCampaignRosterResponseSchema, { roster: [butler, ...cast] }),
      createEdge: (req) => {
        createCalls.push(req);
        const target = others.find((n) => n.id === req.toNodeId);
        const edge = create(EdgeSchema, {
          id: `e${createCalls.length + 100}`,
          fromNodeId: req.fromNodeId,
          toNodeId: req.toNodeId,
          edgeType: req.edgeType,
          fromNodeName: opts.node.name,
          fromNodeType: opts.node.nodeType,
          toNodeName: target?.name ?? "?",
          toNodeType: target?.nodeType ?? NodeType.NOTE,
        });
        outgoing.push(edge);
        return create(CreateEdgeResponseSchema, { edge });
      },
      deleteEdge: (req) => {
        deleteCalls.push(req);
        if (opts.deleteError) {
          throw new ConnectError("edge not found", Code.NotFound);
        }
        const i = outgoing.findIndex((e) => e.id === req.id);
        if (i >= 0) outgoing.splice(i, 1);
        return create(DeleteEdgeResponseSchema, {});
      },
      setNodeAgent: (req) => {
        setAgentCalls.push(req);
        if (opts.setAgentError) {
          throw new ConnectError("agent already linked to another node", Code.AlreadyExists);
        }
        const node = create(NodeSchema, { ...opts.node, agentId: req.agentId });
        return create(SetNodeAgentResponseSchema, { node });
      },
    });
  });
  return { transport, createCalls, deleteCalls, setAgentCalls };
}

function renderRelations(node: PbNode, extra?: Parameters<typeof edgeTransport>[0]) {
  const ctx = edgeTransport({ node, ...extra });
  render(
    <Providers transport={ctx.transport} queryClient={makeQueryClient()}>
      <NodeRelations node={node} />
    </Providers>,
  );
  return ctx;
}

const npcNode = create(NodeSchema, {
  id: "n1",
  campaignId: "c1",
  nodeType: NodeType.NPC,
  name: "Aldric",
});
const locNode = create(NodeSchema, {
  id: "loc",
  campaignId: "c1",
  nodeType: NodeType.LOCATION,
  name: "Barrow",
});

// pick opens a Radix Select and chooses the named option. It awaits the option
// because some option sets (targets, cast) arrive from an async query, so the
// listbox may fill in after it opens.
async function pick(combobox: string, option: string) {
  fireEvent.keyDown(screen.getByRole("combobox", { name: combobox }), { key: "Enter" });
  fireEvent.click(await screen.findByRole("option", { name: option }));
}

// Edge delete is confirm-gated too (#209): the row's X opens a dialog; click the
// destructive button inside it to actually issue DeleteEdge.
async function confirmInDialog() {
  const dialog = await screen.findByRole("alertdialog");
  fireEvent.click(within(dialog).getByRole("button", { name: /^delete/i }));
}

describe("NodeRelations", () => {
  it("renders outgoing and incoming edges in separate sections", async () => {
    renderRelations(npcNode, {
      node: npcNode,
      outgoing: [
        create(EdgeSchema, {
          id: "e1",
          fromNodeId: "n1",
          toNodeId: "loc",
          edgeType: EdgeType.RESIDES_IN,
          toNodeName: "Barrow",
          toNodeType: NodeType.LOCATION,
        }),
      ],
      incoming: [
        create(EdgeSchema, {
          id: "e2",
          fromNodeId: "cyra",
          toNodeId: "n1",
          edgeType: EdgeType.KNOWS,
          fromNodeName: "Cyra",
          fromNodeType: NodeType.CHARACTER,
        }),
      ],
    });

    await screen.findByText("Barrow");
    const out = screen.getByRole("region", { name: /outgoing relations/i });
    expect(within(out).getByText("Barrow")).toBeInTheDocument();
    expect(within(out).getByText("resides_in")).toBeInTheDocument();

    const inc = screen.getByRole("region", { name: /incoming relations/i });
    expect(within(inc).getByText("Cyra")).toBeInTheDocument();
    // Incoming rows are context-only: no delete affordance.
    expect(within(inc).queryByRole("button", { name: /delete relation/i })).toBeNull();
  });

  it("creates a relation with the chosen type and target", async () => {
    const { createCalls } = renderRelations(npcNode, {
      node: npcNode,
      others: [locNode],
    });
    await screen.findByRole("button", { name: /add relation/i });

    fireEvent.click(screen.getByRole("button", { name: /add relation/i }));
    await pick("Relation", "resides_in");
    await pick("Target entry", "Barrow");
    fireEvent.click(screen.getByRole("button", { name: /^add$/i }));

    await waitFor(() => expect(createCalls).toHaveLength(1));
    expect(createCalls[0].edgeType).toBe(EdgeType.RESIDES_IN);
    expect(createCalls[0].fromNodeId).toBe("n1");
    expect(createCalls[0].toNodeId).toBe("loc");
  });

  it("deletes an outgoing relation after confirming", async () => {
    const { deleteCalls } = renderRelations(npcNode, {
      node: npcNode,
      outgoing: [
        create(EdgeSchema, {
          id: "e1",
          fromNodeId: "n1",
          toNodeId: "loc",
          edgeType: EdgeType.RESIDES_IN,
          toNodeName: "Barrow",
          toNodeType: NodeType.LOCATION,
        }),
      ],
    });
    await screen.findByText("Barrow");

    // The row's X opens a confirm dialog naming the relation; no RPC yet.
    fireEvent.click(screen.getByRole("button", { name: /delete relation/i }));
    expect(await screen.findByRole("alertdialog")).toHaveTextContent(/resides_in/i);
    expect(deleteCalls).toHaveLength(0);

    await confirmInDialog();
    await waitFor(() => expect(deleteCalls).toHaveLength(1));
    expect(deleteCalls[0].id).toBe("e1");
  });

  it("cancelling the relation-delete dialog issues no RPC and keeps the edge", async () => {
    const { deleteCalls } = renderRelations(npcNode, {
      node: npcNode,
      outgoing: [
        create(EdgeSchema, {
          id: "e1",
          fromNodeId: "n1",
          toNodeId: "loc",
          edgeType: EdgeType.RESIDES_IN,
          toNodeName: "Barrow",
          toNodeType: NodeType.LOCATION,
        }),
      ],
    });
    await screen.findByText("Barrow");

    fireEvent.click(screen.getByRole("button", { name: /delete relation/i }));
    const dialog = await screen.findByRole("alertdialog");
    fireEvent.click(within(dialog).getByRole("button", { name: /cancel/i }));

    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    expect(deleteCalls).toHaveLength(0);
    expect(screen.getByText("Barrow")).toBeInTheDocument();
  });

  it("links a Character NPC agent from the Voiced by select (NPC node only)", async () => {
    const { setAgentCalls } = renderRelations(npcNode, { node: npcNode });
    await screen.findByRole("combobox", { name: /voiced by/i });

    await pick("Voiced by", "Bart");

    await waitFor(() => expect(setAgentCalls).toHaveLength(1));
    expect(setAgentCalls[0].nodeId).toBe("n1");
    expect(setAgentCalls[0].agentId).toBe("ag1");
  });

  it("hides the Voiced by select for a non-NPC node", async () => {
    renderRelations(locNode, { node: locNode });
    // The relations section still renders (its header appears), but no agent link.
    await screen.findByRole("button", { name: /add relation/i });
    expect(screen.queryByRole("combobox", { name: /voiced by/i })).toBeNull();
  });

  // Finding 1: the select must DISPLAY the newly chosen agent after the link
  // succeeds — the value can't stay pinned to the stale `editing` snapshot.
  it("displays the newly linked agent in the Voiced by select after success", async () => {
    renderRelations(npcNode, { node: npcNode });
    const combo = await screen.findByRole("combobox", { name: /voiced by/i });
    expect(combo).toHaveTextContent(/none/i);

    await pick("Voiced by", "Bart");

    await waitFor(() =>
      expect(screen.getByRole("combobox", { name: /voiced by/i })).toHaveTextContent("Bart"),
    );
  });

  // Finding 2: a setNodeAgent failure (e.g. the agent already voices another
  // node — reachable, options aren't filtered) must surface, not vanish.
  it("surfaces a setNodeAgent failure as an alert", async () => {
    renderRelations(npcNode, { node: npcNode, setAgentError: true });
    await screen.findByRole("combobox", { name: /voiced by/i });

    await pick("Voiced by", "Bart");

    expect(await screen.findByRole("alert")).toHaveTextContent(/couldn't link/i);
  });

  // Finding 2: a deleteEdge failure must surface too.
  it("surfaces a deleteEdge failure as an alert", async () => {
    renderRelations(npcNode, {
      node: npcNode,
      deleteError: true,
      outgoing: [
        create(EdgeSchema, {
          id: "e1",
          fromNodeId: "n1",
          toNodeId: "loc",
          edgeType: EdgeType.RESIDES_IN,
          toNodeName: "Barrow",
          toNodeType: NodeType.LOCATION,
        }),
      ],
    });
    await screen.findByText("Barrow");

    fireEvent.click(screen.getByRole("button", { name: /delete relation/i }));
    await confirmInDialog();

    expect(await screen.findByRole("alert")).toHaveTextContent(/couldn't delete/i);
  });
});
