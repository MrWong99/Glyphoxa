import { describe, it, expect, vi } from "vitest";
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
  UpdateEdgeDetailsResponseSchema,
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
  const detailCalls: { id: string; note: string; disposition: number }[] = [];

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
      updateEdgeDetails: (req) => {
        detailCalls.push({ id: req.id, note: req.note, disposition: req.disposition });
        return create(UpdateEdgeDetailsResponseSchema, {
          edge: create(EdgeSchema, { id: req.id, note: req.note, disposition: req.disposition }),
        });
      },
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
        const j = incoming.findIndex((e) => e.id === req.id);
        if (j >= 0) incoming.splice(j, 1);
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
  return { transport, createCalls, deleteCalls, setAgentCalls, detailCalls };
}

function renderRelations(
  node: PbNode,
  extra?: Partial<Parameters<typeof edgeTransport>[0]>,
  onOpenNode?: (id: string) => void,
) {
  const ctx = edgeTransport({ node, ...extra });
  render(
    <Providers transport={ctx.transport} queryClient={makeQueryClient()}>
      <NodeRelations node={node} onOpenNode={onOpenNode} />
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
const factionNode = create(NodeSchema, { id: "fac", nodeType: NodeType.FACTION, name: "Smugglers" });
const otherNpc = create(NodeSchema, { id: "n2", nodeType: NodeType.NPC, name: "Cyra" });

// The target picker is the filterable Combobox: a button that opens a listbox.
const openTargets = () => fireEvent.click(screen.getByRole("button", { name: "Target entry" }));
const targetNames = () => screen.getAllByRole("option").map((o) => o.textContent);
async function pickTarget(option: string) {
  openTargets();
  fireEvent.click(await screen.findByRole("option", { name: option }));
}
// The relations a Radix Select is offering right now (it stays open).
function relationNames() {
  fireEvent.keyDown(screen.getByRole("combobox", { name: "Connection" }), { key: "Enter" });
  return screen.getAllByRole("option").map((o) => o.textContent);
}

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
    const out = screen.getByRole("region", { name: /outgoing connections/i });
    expect(within(out).getByText("Barrow")).toBeInTheDocument();
    expect(within(out).getByText("lives in")).toBeInTheDocument();

    const inc = screen.getByRole("region", { name: /incoming connections/i });
    expect(within(inc).getByText("Cyra")).toBeInTheDocument();
    // An incoming relation is authored on the other entry: it has no texture
    // editor here, only the way to remove it.
    expect(within(inc).queryByRole("button", { name: /edit what/i })).toBeNull();
    expect(within(inc).getByRole("button", { name: "Delete connection from Cyra: knows" })).toBeInTheDocument();
    // With nowhere to navigate to, a name is text, not a dead control.
    expect(within(out).queryByRole("button", { name: "Barrow" })).toBeNull();
  });

  it("an incoming relation can be removed from here, after confirming", async () => {
    const { deleteCalls } = renderRelations(npcNode, {
      node: npcNode,
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
    fireEvent.click(await screen.findByRole("button", { name: "Delete connection from Cyra: knows" }));
    // The question names the relation from the side it is asserted on.
    expect(await screen.findByRole("alertdialog")).toHaveTextContent("Cyra → knows");
    expect(deleteCalls).toHaveLength(0);

    await confirmInDialog();
    await waitFor(() => expect(deleteCalls).toHaveLength(1));
    expect(deleteCalls[0].id).toBe("e2");
    await waitFor(() => expect(screen.queryByText("Cyra")).not.toBeInTheDocument());
  });

  it("a connection's name opens that entry", async () => {
    const onOpenNode = vi.fn();
    renderRelations(
      npcNode,
      {
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
      },
      onOpenNode,
    );
    fireEvent.click(await screen.findByRole("button", { name: "Barrow" }));
    expect(onOpenNode).toHaveBeenLastCalledWith("loc");
    fireEvent.click(screen.getByRole("button", { name: "Cyra" }));
    expect(onOpenNode).toHaveBeenLastCalledWith("cyra");
  });

  it("choosing 'lives in' offers only Location targets", async () => {
    renderRelations(npcNode, { node: npcNode, others: [locNode, factionNode, otherNpc] });
    fireEvent.click(await screen.findByRole("button", { name: /add connection/i }));

    // Before a relation is chosen, every other entry is a possible target,
    // each with its type.
    openTargets();
    await screen.findByRole("option", { name: "Barrow · Location" });
    expect(targetNames()).toEqual(["Barrow · Location", "Smugglers · Faction", "Cyra · NPC"]);
    fireEvent.click(screen.getByRole("button", { name: "Target entry" }));

    await pick("Connection", "lives in");
    openTargets();
    expect(targetNames()).toEqual(["Barrow · Location"]);
  });

  it("a chosen target narrows the relations to the ones it can take", async () => {
    renderRelations(npcNode, { node: npcNode, others: [locNode, factionNode, otherNpc] });
    fireEvent.click(await screen.findByRole("button", { name: /add connection/i }));
    await pickTarget("Smugglers · Faction");

    const offered = relationNames();
    expect(offered).toContain("member of");
    expect(offered).toContain("knows");
    // A faction is not a place, not a plot thread, and nobody's child.
    expect(offered).not.toContain("lives in");
    expect(offered).not.toContain("took part in");
    expect(offered).not.toContain("parent of");
  });

  it("never offers a relation no entry could be the target of", async () => {
    // A Location cannot be anyone's parent, and there is no plot thread yet.
    renderRelations(locNode, { node: locNode, others: [npcNode, factionNode] });
    fireEvent.click(await screen.findByRole("button", { name: /add connection/i }));
    await screen.findByRole("combobox", { name: "Connection" });
    await waitFor(() => expect(relationNames()).toContain("member of"));
    const offered = screen.getAllByRole("option").map((o) => o.textContent);
    expect(offered).not.toContain("parent of");
    expect(offered).not.toContain("took part in");
    expect(offered).not.toContain("lives in");
  });

  it("switching to a relation the chosen target cannot take clears the target", async () => {
    const { createCalls } = renderRelations(npcNode, { node: npcNode, others: [locNode, factionNode] });
    fireEvent.click(await screen.findByRole("button", { name: /add connection/i }));
    await pick("Connection", "lives in");
    await pickTarget("Barrow · Location");
    expect(screen.getByRole("button", { name: "Target entry" })).toHaveTextContent("Barrow · Location");

    // The GM changes their mind: the relation stays switchable…
    await pick("Connection", "member of");
    // …and the location, which cannot be the target of it, is let go.
    expect(screen.getByRole("button", { name: "Target entry" })).not.toHaveTextContent("Barrow");
    expect(screen.getByRole("button", { name: /^add$/i })).toBeDisabled();

    await pickTarget("Smugglers · Faction");
    fireEvent.click(screen.getByRole("button", { name: /^add$/i }));
    await waitFor(() => expect(createCalls).toHaveLength(1));
    expect(createCalls[0]).toMatchObject({ edgeType: EdgeType.MEMBER_OF, toNodeId: "fac" });
  });

  it("takes a note and a feeling while adding, chained through UpdateEdgeDetails", async () => {
    const { createCalls, detailCalls } = renderRelations(npcNode, { node: npcNode, others: [otherNpc] });
    fireEvent.click(await screen.findByRole("button", { name: /add connection/i }));
    await pick("Connection", "knows");
    await pickTarget("Cyra · NPC");
    fireEvent.change(screen.getByLabelText("What it's like (optional)"), {
      target: { value: "  owes her money  " },
    });
    await pick("How you feel", "wary");
    fireEvent.click(screen.getByRole("button", { name: /^add$/i }));

    await waitFor(() => expect(createCalls).toHaveLength(1));
    // CreateEdge carries only the relation; the texture follows on the edge
    // the server just created.
    await waitFor(() => expect(detailCalls).toHaveLength(1));
    expect(detailCalls[0]).toEqual({ id: "e101", note: "owes her money", disposition: -1 });
    // The form closes once the connection exists.
    await waitFor(() => expect(screen.queryByRole("button", { name: /^add$/i })).not.toBeInTheDocument());
  });

  it("a plain connection is one write: no details call", async () => {
    const { createCalls, detailCalls } = renderRelations(npcNode, { node: npcNode, others: [otherNpc] });
    fireEvent.click(await screen.findByRole("button", { name: /add connection/i }));
    await pick("Connection", "knows");
    await pickTarget("Cyra · NPC");
    fireEvent.click(screen.getByRole("button", { name: /^add$/i }));
    await waitFor(() => expect(createCalls).toHaveLength(1));
    await waitFor(() => expect(screen.queryByRole("button", { name: /^add$/i })).not.toBeInTheDocument());
    expect(detailCalls).toHaveLength(0);
  });

  it("creates a relation with the chosen type and target", async () => {
    const { createCalls } = renderRelations(npcNode, {
      node: npcNode,
      others: [locNode],
    });
    await screen.findByRole("button", { name: /add connection/i });

    fireEvent.click(screen.getByRole("button", { name: /add connection/i }));
    await pick("Connection", "lives in");
    await pickTarget("Barrow · Location");
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
    fireEvent.click(screen.getByRole("button", { name: /delete connection/i }));
    expect(await screen.findByRole("alertdialog")).toHaveTextContent(/lives in/i);
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

    fireEvent.click(screen.getByRole("button", { name: /delete connection/i }));
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
    await screen.findByRole("button", { name: /add connection/i });
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

    fireEvent.click(screen.getByRole("button", { name: /delete connection/i }));
    await confirmInDialog();

    expect(await screen.findByRole("alert")).toHaveTextContent(/couldn't delete/i);
  });

  it("authors a relation's texture and sends it (#546)", async () => {
    const ctx = renderRelations(npcNode, {
      outgoing: [
        create(EdgeSchema, {
          id: "e1",
          fromNodeId: "n1",
          toNodeId: "n2",
          edgeType: EdgeType.KNOWS,
          toNodeName: "Mira",
          toNodeType: NodeType.NPC,
        }),
      ],
    });
    fireEvent.click(await screen.findByRole("button", { name: /Edit what knows/ }));

    fireEvent.change(screen.getByLabelText("What it's like"), {
      target: { value: "  owes you money since the siege  " },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(ctx.detailCalls).toHaveLength(1));
    // Trimmed, because a trailing space is not texture.
    expect(ctx.detailCalls[0].note).toBe("owes you money since the siege");
  });

  it("closes the texture editor after EVERY save of the same relation, not just the first (W13)", async () => {
    const ctx = renderRelations(npcNode, {
      outgoing: [
        create(EdgeSchema, {
          id: "e1",
          fromNodeId: "n1",
          toNodeId: "n2",
          edgeType: EdgeType.KNOWS,
          toNodeName: "Mira",
          toNodeType: NodeType.NPC,
        }),
      ],
    });
    const toggle = await screen.findByRole("button", { name: /Edit what knows/ });

    fireEvent.click(toggle);
    fireEvent.change(screen.getByLabelText("What it's like"), { target: { value: "wary" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(screen.queryByLabelText("What it's like")).not.toBeInTheDocument());

    // Second edit of the SAME relation: it must save and close again.
    fireEvent.click(toggle);
    fireEvent.change(await screen.findByLabelText("What it's like"), { target: { value: "hostile" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(ctx.detailCalls).toHaveLength(2));
    await waitFor(() => expect(screen.queryByLabelText("What it's like")).not.toBeInTheDocument());
  });
});
