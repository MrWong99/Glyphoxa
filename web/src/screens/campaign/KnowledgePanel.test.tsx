import { describe, it, expect } from "vitest";
import { render, screen, fireEvent, within, waitFor } from "@testing-library/react";
import { createRouterTransport, ConnectError, Code } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  CampaignService,
  NodeSchema,
  NodeType,
  CreateNodeResponseSchema,
  ListNodesResponseSchema,
  UpdateNodeResponseSchema,
  DeleteNodeResponseSchema,
  SearchNodesResponseSchema,
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
function mockTransport(
  seed?: ReturnType<typeof create<typeof NodeSchema>>[],
  opts: { failDelete?: boolean; failSearch?: boolean } = {},
) {
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
  const searchCalls: string[] = [];

  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      listNodes: () => create(ListNodesResponseSchema, { nodes }),
      // Stand-in for the tsvector search: a case-insensitive substring over
      // name + body. The panel only cares that matches filter the visible list;
      // relevance ranking is asserted server-side. gm_private is not filtered.
      searchNodes: (req) => {
        searchCalls.push(req.query);
        if (opts.failSearch) {
          throw new ConnectError("search boom", Code.Internal);
        }
        const q = req.query.trim().toLowerCase();
        const hits = nodes.filter(
          (n) => n.name.toLowerCase().includes(q) || n.body.toLowerCase().includes(q),
        );
        return create(SearchNodesResponseSchema, { nodes: hits });
      },
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
        if (opts.failDelete) {
          throw new ConnectError("delete boom", Code.Internal);
        }
        const i = nodes.findIndex((n) => n.id === req.id);
        if (i >= 0) nodes.splice(i, 1);
        return create(DeleteNodeResponseSchema, {});
      },
    });
  });
  return { transport, nodes, createCalls, updateCalls, deleteCalls, searchCalls };
}

function renderPanel(
  seed?: ReturnType<typeof create<typeof NodeSchema>>[],
  opts?: { failDelete?: boolean; failSearch?: boolean },
) {
  const ctx = mockTransport(seed, opts);
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

  it("refreshes the filtered search results when an entry is deleted while searching", async () => {
    const { deleteCalls } = renderPanel([
      create(NodeSchema, { id: "n1", campaignId: "c1", nodeType: NodeType.NOTE, name: "The sealed vault", body: "" }),
      create(NodeSchema, { id: "n2", campaignId: "c1", nodeType: NodeType.LOCATION, name: "Quiet Harbor", body: "" }),
    ]);
    await screen.findByText("The sealed vault");

    // Filter to just the vault, then delete it from the filtered list.
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "vault" } });
    await waitFor(() => expect(screen.queryByText("Quiet Harbor")).not.toBeInTheDocument());
    fireEvent.click(screen.getByRole("button", { name: /delete the sealed vault/i }));

    // The mutation must invalidate the SEARCH cache too, so the deleted entry
    // leaves the filtered view instead of lingering (stale search results).
    await waitFor(() => expect(screen.queryByText("The sealed vault")).not.toBeInTheDocument());
    expect(screen.getByText(/no entries match/i)).toBeInTheDocument();
    expect(deleteCalls).toHaveLength(1);
  });

  it("surfaces a failed search RPC as an alert instead of silently showing the full list", async () => {
    renderPanel(
      [
        create(NodeSchema, { id: "n1", campaignId: "c1", nodeType: NodeType.NOTE, name: "The sealed vault", body: "" }),
        create(NodeSchema, { id: "n2", campaignId: "c1", nodeType: NodeType.LOCATION, name: "Quiet Harbor", body: "" }),
      ],
      { failSearch: true },
    );
    await screen.findByText("The sealed vault");

    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "vault" } });

    // The failure is announced...
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/couldn't search/i);
    expect(alert).toHaveTextContent(/search boom/i);
    // ...and the full unfiltered list is NOT shown as if it were the results (a
    // non-matching entry must not reappear while the box still holds a query).
    expect(screen.queryByText("Quiet Harbor")).not.toBeInTheDocument();
  });

  it("surfaces a failed delete as an alert instead of a silently dead button", async () => {
    renderPanel(undefined, { failDelete: true });
    await screen.findByText("The sealed vault");

    fireEvent.click(screen.getByRole("button", { name: /delete the sealed vault/i }));

    // The failure is announced, not swallowed — and the entry is still present.
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/couldn't delete/i);
    expect(alert).toHaveTextContent(/delete boom/i);
    expect(screen.getByText("The sealed vault")).toBeInTheDocument();
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

  it("filters the visible list to search matches, and clearing restores it with no search RPC", async () => {
    const { searchCalls } = renderPanel([
      create(NodeSchema, { id: "n1", campaignId: "c1", nodeType: NodeType.NOTE, name: "The sealed vault", body: "" }),
      create(NodeSchema, { id: "n2", campaignId: "c1", nodeType: NodeType.LOCATION, name: "Quiet Harbor", body: "" }),
    ]);
    // Both entries show before any search, and no search RPC has fired.
    await screen.findByText("The sealed vault");
    expect(screen.getByText("Quiet Harbor")).toBeInTheDocument();
    expect(searchCalls).toHaveLength(0);

    // Typing filters the visible list to matches (debounced), dropping the harbor.
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "vault" } });
    await waitFor(() => expect(screen.queryByText("Quiet Harbor")).not.toBeInTheDocument());
    expect(screen.getByText("The sealed vault")).toBeInTheDocument();
    expect(searchCalls.at(-1)).toBe("vault");
    const callsAfterSearch = searchCalls.length;

    // Clearing the box restores the full list from ListNodes — no search RPC on empty.
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "" } });
    await screen.findByText("Quiet Harbor");
    expect(screen.getByText("The sealed vault")).toBeInTheDocument();
    expect(searchCalls).toHaveLength(callsAfterSearch);
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

  it("marks an agent-linked NPC entry with a Linked agent badge (#132)", async () => {
    renderPanel([
      create(NodeSchema, {
        id: "n1",
        campaignId: "c1",
        nodeType: NodeType.NPC,
        name: "Bart",
        agentId: "ag1",
      }),
    ]);
    expect(await screen.findByText("Bart")).toBeInTheDocument();
    expect(screen.getByText("Linked agent")).toBeInTheDocument();
  });
});
