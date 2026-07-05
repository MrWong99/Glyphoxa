import { describe, it, expect } from "vitest";
import { render, screen, fireEvent, within } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  CampaignService,
  NodeSchema,
  NodeType,
  CreateNodeResponseSchema,
  ListNodesResponseSchema,
  UpdateNodeResponseSchema,
  DeleteNodeResponseSchema,
  type CreateNodeRequest,
  type UpdateNodeRequest,
  type DeleteNodeRequest,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { KnowledgePanel } from "./KnowledgePanel";

// An in-memory KG store served over a router transport (no network): the four
// CampaignService node RPCs mutate a closure so create/edit/delete each round-trip
// through invalidate → refetch, and the recorded requests prove the chosen
// NodeType, the update fields, and the delete id reach the wire.
function mockTransport(seed?: ReturnType<typeof create<typeof NodeSchema>>[]) {
  const nodes =
    seed ??
    [
      create(NodeSchema, {
        id: "n1",
        campaignId: "c1",
        nodeType: NodeType.NOTE,
        name: "The sealed vault",
        body: "Nobody has opened it in a century.",
        gmPrivate: false,
      }),
    ];
  let nextId = 2;
  const createCalls: CreateNodeRequest[] = [];
  const updateCalls: UpdateNodeRequest[] = [];
  const deleteCalls: DeleteNodeRequest[] = [];

  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      listNodes: () => create(ListNodesResponseSchema, { nodes }),
      createNode: (req) => {
        createCalls.push(req);
        const node = create(NodeSchema, {
          id: `n${nextId++}`,
          campaignId: "c1",
          nodeType: req.nodeType,
          name: req.name,
          body: req.body,
          gmPrivate: req.gmPrivate,
        });
        nodes.push(node);
        return create(CreateNodeResponseSchema, { node });
      },
      updateNode: (req) => {
        updateCalls.push(req);
        const node = nodes.find((n) => n.id === req.id)!;
        node.name = req.name;
        node.body = req.body;
        node.gmPrivate = req.gmPrivate;
        return create(UpdateNodeResponseSchema, { node });
      },
      deleteNode: (req) => {
        deleteCalls.push(req);
        const i = nodes.findIndex((n) => n.id === req.id);
        if (i >= 0) nodes.splice(i, 1);
        return create(DeleteNodeResponseSchema, {});
      },
    });
  });
  return { transport, nodes, createCalls, updateCalls, deleteCalls };
}

function renderPanel(seed?: ReturnType<typeof create<typeof NodeSchema>>[]) {
  const ctx = mockTransport(seed);
  render(
    <Providers transport={ctx.transport} queryClient={makeQueryClient()}>
      <KnowledgePanel />
    </Providers>,
  );
  return ctx;
}

// pickType opens the Radix Type select and chooses the named option.
function pickType(label: string) {
  fireEvent.keyDown(screen.getByRole("combobox", { name: "Type" }), { key: "Enter" });
  fireEvent.click(screen.getByRole("option", { name: label }));
}

describe("KnowledgePanel", () => {
  it("lists the campaign's knowledge entries from ListNodes", async () => {
    renderPanel();
    expect(await screen.findByText("The sealed vault")).toBeInTheDocument();
    expect(screen.getByText(/Nobody has opened it/)).toBeInTheDocument();
  });

  it("creates an entry of a chosen type, sending that NodeType", async () => {
    const { nodes, createCalls } = renderPanel();
    await screen.findByText("The sealed vault");

    pickType("Location");
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "The old harbor" } });
    fireEvent.change(screen.getByLabelText(/content/i), { target: { value: "Ships dock here." } });
    fireEvent.click(screen.getByRole("button", { name: /add entry/i }));

    expect(await screen.findByText("The old harbor")).toBeInTheDocument();
    expect(nodes).toHaveLength(2);
    expect(createCalls).toHaveLength(1);
    expect(createCalls[0].nodeType).toBe(NodeType.LOCATION);
    expect(createCalls[0].name).toBe("The old harbor");
  });

  it("creates a gm-private entry, sending the gm_private flag", async () => {
    const { createCalls } = renderPanel();
    await screen.findByText("The sealed vault");

    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Hidden cult" } });
    fireEvent.click(screen.getByLabelText(/gm private/i));
    fireEvent.click(screen.getByRole("button", { name: /add entry/i }));

    expect(await screen.findByText("Hidden cult")).toBeInTheDocument();
    expect(createCalls[0].gmPrivate).toBe(true);
  });

  it("edits an entry: name + gm_private round-trip via UpdateNode", async () => {
    const { updateCalls } = renderPanel();
    await screen.findByText("The sealed vault");

    fireEvent.click(screen.getByRole("button", { name: /edit the sealed vault/i }));
    // The editor is now in edit mode and pre-filled.
    expect(screen.getByRole("heading", { name: /edit entry/i })).toBeInTheDocument();
    const name = screen.getByLabelText("Name") as HTMLInputElement;
    expect(name.value).toBe("The sealed vault");
    // The type is not editable while editing.
    expect(screen.getByRole("combobox", { name: "Type" })).toBeDisabled();

    fireEvent.change(name, { target: { value: "The opened vault" } });
    fireEvent.click(screen.getByLabelText(/gm private/i));
    fireEvent.click(screen.getByRole("button", { name: /save entry/i }));

    expect(await screen.findByText("The opened vault")).toBeInTheDocument();
    expect(updateCalls).toHaveLength(1);
    expect(updateCalls[0].id).toBe("n1");
    expect(updateCalls[0].name).toBe("The opened vault");
    expect(updateCalls[0].gmPrivate).toBe(true);
  });

  it("deletes an entry from its card via DeleteNode", async () => {
    const { deleteCalls } = renderPanel();
    await screen.findByText("The sealed vault");

    fireEvent.click(screen.getByRole("button", { name: /delete the sealed vault/i }));

    await screen.findByText(/no entries yet/i);
    expect(deleteCalls).toHaveLength(1);
    expect(deleteCalls[0].id).toBe("n1");
  });

  it("groups the list by node type", async () => {
    renderPanel([
      create(NodeSchema, { id: "a", campaignId: "c1", nodeType: NodeType.LOCATION, name: "Harbor" }),
      create(NodeSchema, { id: "b", campaignId: "c1", nodeType: NodeType.FACTION, name: "Dockers Guild" }),
    ]);
    // A group heading per distinct type, and each entry under its own group.
    const locGroup = await screen.findByRole("region", { name: "Location" });
    expect(within(locGroup).getByText("Harbor")).toBeInTheDocument();
    const facGroup = screen.getByRole("region", { name: "Faction" });
    expect(within(facGroup).getByText("Dockers Guild")).toBeInTheDocument();
  });

  it("marks a gm-private entry with a private badge", async () => {
    renderPanel([
      create(NodeSchema, {
        id: "n1",
        campaignId: "c1",
        nodeType: NodeType.NOTE,
        name: "Secret pact",
        body: "signed in blood",
        gmPrivate: true,
      }),
    ]);
    expect(await screen.findByText("Secret pact")).toBeInTheDocument();
    expect(screen.getByText("GM private")).toBeInTheDocument();
  });
});
