import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, within, waitFor } from "@testing-library/react";
import { createRouterTransport, ConnectError, Code } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate, type Timestamp } from "@bufbuild/protobuf/wkt";
import { toast } from "sonner";

import {
  CampaignService,
  NodeSchema,
  NodeType,
  EdgeSchema,
  EdgeType,
  CreateNodeResponseSchema,
  FindDuplicateEntriesResponseSchema,
  GetCampaignRosterResponseSchema,
  GetCampaignTagsResponseSchema,
  GetKnowledgeGraphResponseSchema,
  ListBoardsResponseSchema,
  ListKnowledgeProposalsResponseSchema,
  ListNodeAppearancesResponseSchema,
  ListNodesResponseSchema,
  ListNodeEdgesResponseSchema,
  RenameTagResponseSchema,
  SetNodeTagsResponseSchema,
  UpdateNodeResponseSchema,
  DeleteNodeResponseSchema,
  SearchNodesResponseSchema,
  type Edge as PbEdge,
  type CreateNodeRequest,
  type UpdateNodeRequest,
  type DeleteNodeRequest,
  type RenameTagRequest,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import type { ScreenSearch } from "@/app/screenNav";
import { makeQueryClient } from "@/lib/queryClient";
import { KnowledgePanel } from "./KnowledgePanel";
import { WikiNav, makeWikiHistory, pickNewEntry } from "./wikiTestKit";

// The shell's <Toaster> is not mounted here; the toasts are asserted as calls.
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() } }));

type Seed = ReturnType<typeof create<typeof NodeSchema>>[];
type Opts = {
  failDelete?: boolean;
  failSearch?: boolean;
  /** Holds the answer to one search query open until the promise settles. */
  holdSearch?: { query: string; until: Promise<void> };
  failEdges?: boolean;
  hangEdges?: boolean;
  edges?: PbEdge[];
  tags?: { nodeId: string; tag: string }[];
  search?: ScreenSearch;
};

// An in-memory KG store served over a router transport (no network): the
// CampaignService node RPCs mutate a closure so create/edit/delete each
// round-trip through invalidate → refetch, and the recorded requests prove the
// chosen NodeType, the update fields, and the delete id reach the wire.
function mockTransport(seed?: Seed, opts: Opts = {}) {
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
  let nextId = nodes.length + 1;
  let nextAspect = 1;
  const createCalls: CreateNodeRequest[] = [];
  const updateCalls: UpdateNodeRequest[] = [];
  const deleteCalls: DeleteNodeRequest[] = [];
  const renameCalls: RenameTagRequest[] = [];
  const searchCalls: string[] = [];
  let duplicateScans = 0;

  const edges = opts.edges ?? [];
  let tags = opts.tags ?? [];
  // The server gives every new fact an id; a saved row sent back without one
  // would be inserted again.
  const withIds = (aspects: UpdateNodeRequest["aspects"]) =>
    aspects.map((a) => ({ ...a, id: a.id || `srv-${nextAspect++}` }));

  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      listNodes: () => create(ListNodesResponseSchema, { nodes }),
      // The graph payload mirrors the list, minus prose (#534).
      getKnowledgeGraph: () =>
        create(GetKnowledgeGraphResponseSchema, {
          nodes: nodes.map((n) => ({
            id: n.id,
            nodeType: n.nodeType,
            name: n.name,
            gmPrivate: n.gmPrivate,
            agentId: n.agentId,
            bodyLen: n.body.length,
            aspectCount: n.aspects.length,
          })),
          edges: edges.map((e) => ({
            id: e.id,
            fromNodeId: e.fromNodeId,
            toNodeId: e.toNodeId,
            edgeType: e.edgeType,
          })),
        }),
      listNodeEdges: (req) => {
        if (opts.failEdges) throw new ConnectError("edges boom", Code.Internal);
        // A never-settling response models a slow/stuck count: the dialog must
        // stay in its "counting…" + confirm-disabled state, never implying 0.
        if (opts.hangEdges) return new Promise<never>(() => {});
        return create(ListNodeEdgesResponseSchema, {
          outgoing: edges.filter((e) => e.fromNodeId === req.nodeId),
          incoming: edges.filter((e) => e.toNodeId === req.nodeId),
        });
      },
      listNodeAppearances: () => create(ListNodeAppearancesResponseSchema, {}),
      listKnowledgeProposals: () => create(ListKnowledgeProposalsResponseSchema, {}),
      getCampaignRoster: () => create(GetCampaignRosterResponseSchema, {}),
      listBoards: () => create(ListBoardsResponseSchema, { boards: [{ id: "b1", name: "Tonight", nodeIds: ["n1"] }] }),
      getCampaignTags: () => create(GetCampaignTagsResponseSchema, { entries: tags }),
      setNodeTags: (req) => {
        tags = [...tags.filter((e) => e.nodeId !== req.nodeId), ...req.tags.map((tag) => ({ nodeId: req.nodeId, tag }))];
        return create(SetNodeTagsResponseSchema, { tags: req.tags });
      },
      renameTag: (req) => {
        renameCalls.push(req);
        tags = tags.map((e) => (e.tag === req.from ? { ...e, tag: req.to } : e));
        return create(RenameTagResponseSchema, {});
      },
      findDuplicateEntries: () => {
        duplicateScans++;
        return create(FindDuplicateEntriesResponseSchema, {
          pairs: [{ aId: "n1", aName: nodes[0]?.name ?? "", bId: "n2", bName: nodes[1]?.name ?? "", similarity: 0.93 }],
        });
      },
      // Stand-in for the tsvector search: a case-insensitive substring over
      // name + body, in list order. Relevance ranking is asserted server-side.
      searchNodes: async (req) => {
        searchCalls.push(req.query);
        if (opts.failSearch) throw new ConnectError("search boom", Code.Internal);
        if (opts.holdSearch?.query === req.query) await opts.holdSearch.until;
        const q = req.query.trim().toLowerCase();
        const hits = nodes.filter(
          (n) => n.name.toLowerCase().includes(q) || n.body.toLowerCase().includes(q),
        );
        // Reversed, so a test can tell the server's order from the list's.
        return create(SearchNodesResponseSchema, { nodes: [...hits].reverse() });
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
          aspects: withIds(req.aspects),
        });
        nodes.push(node);
        return create(CreateNodeResponseSchema, { node });
      },
      updateNode: (req) => {
        updateCalls.push(req);
        const i = nodes.findIndex((n) => n.id === req.id);
        nodes[i] = create(NodeSchema, {
          ...nodes[i],
          name: req.name,
          body: req.body,
          gmPrivate: req.gmPrivate,
          aspects: withIds(req.aspects),
        });
        return create(UpdateNodeResponseSchema, { node: nodes[i] });
      },
      deleteNode: (req) => {
        deleteCalls.push(req);
        if (opts.failDelete) throw new ConnectError("delete boom", Code.Internal);
        const i = nodes.findIndex((n) => n.id === req.id);
        if (i >= 0) nodes.splice(i, 1);
        return create(DeleteNodeResponseSchema, {});
      },
    });
  });
  return {
    transport,
    nodes,
    createCalls,
    updateCalls,
    deleteCalls,
    renameCalls,
    searchCalls,
    duplicateScans: () => duplicateScans,
  };
}

function renderPanel(seed?: Seed, opts: Opts = {}) {
  const ctx = mockTransport(seed, opts);
  const history = makeWikiHistory();
  render(
    <Providers transport={ctx.transport} queryClient={makeQueryClient()}>
      <WikiNav history={history} initial={opts.search}>
        <KnowledgePanel />
      </WikiNav>
    </Providers>,
  );
  return { ...ctx, history };
}

// Delete is guarded by a confirm dialog: open it via the card/editor trash
// button, then click the destructive button inside the dialog itself. The
// confirm stays disabled until the cascade-edge count has loaded.
async function confirmInDialog() {
  const dialog = await screen.findByRole("alertdialog");
  const btn = within(dialog).getByRole("button", { name: /^delete/i });
  await waitFor(() => expect(btn).toBeEnabled());
  fireEvent.click(btn);
}

const note = (
  id: string,
  name: string,
  extra: { body?: string; gmPrivate?: boolean; updatedAt?: Timestamp } = {},
) => create(NodeSchema, { id, campaignId: "c1", nodeType: NodeType.NOTE, name, ...extra });

const world = () => [
  create(NodeSchema, {
    id: "bart",
    nodeType: NodeType.NPC,
    name: "Bart",
    body: "Runs the **Rusty Anchor**.\n\n- pours short\n- hears everything",
    aspects: [
      { id: "a1", key: "Role", value: "Innkeeper", gmPrivate: false },
      { id: "a2", key: "Secret", value: "Took the smugglers' bribe", gmPrivate: true },
    ],
  }),
  create(NodeSchema, { id: "town", nodeType: NodeType.LOCATION, name: "Saltmarsh", body: "A fishing town." }),
  create(NodeSchema, { id: "guild", nodeType: NodeType.FACTION, name: "Smugglers", body: "They own the night." }),
];
const worldEdges = () => [
  create(EdgeSchema, {
    id: "e1",
    fromNodeId: "bart",
    fromNodeName: "Bart",
    fromNodeType: NodeType.NPC,
    toNodeId: "town",
    toNodeName: "Saltmarsh",
    toNodeType: NodeType.LOCATION,
    edgeType: EdgeType.RESIDES_IN,
  }),
];

beforeEach(() => {
  vi.mocked(toast.success).mockClear();
  vi.mocked(toast.error).mockClear();
});

describe("KnowledgePanel list", () => {
  it("lists the campaign's entries from ListNodes", async () => {
    renderPanel();
    expect(await screen.findByRole("button", { name: "The sealed vault" })).toBeInTheDocument();
    expect(screen.getByText(/Nobody has opened it/)).toBeInTheDocument();
    expect(screen.getByText("1 entry")).toBeInTheDocument();
  });

  it("groups the list by type, with a count, and a group collapses", async () => {
    renderPanel([
      create(NodeSchema, { id: "a", nodeType: NodeType.LOCATION, name: "Harbor" }),
      create(NodeSchema, { id: "b", nodeType: NodeType.FACTION, name: "Dockers Guild" }),
    ]);
    const locGroup = await screen.findByRole("region", { name: "Location" });
    expect(within(locGroup).getByRole("button", { name: "Harbor" })).toBeInTheDocument();
    const facGroup = screen.getByRole("region", { name: "Faction" });
    expect(within(facGroup).getByRole("button", { name: "Dockers Guild" })).toBeInTheDocument();

    // Grouped under its type heading, a card does not repeat the type.
    expect(within(locGroup).queryByText("Location", { selector: ".gx-badge" })).not.toBeInTheDocument();

    const toggle = within(locGroup).getByRole("button", { name: "Location 1" });
    expect(toggle).toHaveAttribute("aria-expanded", "true");
    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(within(locGroup).queryByRole("button", { name: "Harbor" })).not.toBeInTheDocument();
  });

  it("a card falls back to its first two public facts, counts the private ones and caps its tags", async () => {
    renderPanel(
      [
        create(NodeSchema, {
          id: "n1",
          nodeType: NodeType.NPC,
          name: "Bart",
          aspects: [
            { id: "a1", key: "Secret", value: "Bribed", gmPrivate: true },
            { id: "a2", key: "Role", value: "Innkeeper" },
            { id: "a3", key: "Wants", value: "A quiet life" },
            { id: "a4", key: "Manner", value: "Grumbles" },
          ],
        }),
      ],
      {
        tags: ["act one", "harbor", "seafaring", "needs a voice", "rumour"].map((tag) => ({ nodeId: "n1", tag })),
      },
    );
    const card = (await screen.findByRole("button", { name: "Bart" })).closest("li")!;
    // The secret never reaches the list: it is what is on screen when shared.
    expect(within(card).getByText("Role: Innkeeper · Wants: A quiet life")).toBeInTheDocument();
    expect(within(card).queryByText(/Bribed/)).not.toBeInTheDocument();
    expect(within(card).getByText("1 GM-only fact")).toBeInTheDocument();
    await waitFor(() => expect(within(card).getByText("+2")).toBeInTheDocument());
    expect(card.querySelectorAll(".gx-wiki-tag")).toHaveLength(3);
  });

  it("marks a GM-only entry and a voiced NPC on their cards", async () => {
    renderPanel([
      note("n1", "Secret pact", { body: "signed in blood", gmPrivate: true }),
      create(NodeSchema, { id: "n2", nodeType: NodeType.NPC, name: "Bart", agentId: "ag1" }),
    ]);
    expect(await screen.findByText("GM-only")).toBeInTheDocument();
    expect(screen.getByText("Voiced")).toBeInTheDocument();
  });

  it("sorts by name by default and by most recently updated on request", async () => {
    const at = (day: number) => timestampFromDate(new Date(Date.UTC(2026, 0, day)));
    renderPanel([
      note("n1", "Zephyr", { updatedAt: at(9) }),
      note("n2", "Anchor", { updatedAt: at(2) }),
      note("n3", "Mast", { updatedAt: at(5) }),
    ]);
    const names = () =>
      [...document.querySelectorAll(".gx-wiki-card__open")].map((el) => el.textContent);
    await screen.findByRole("button", { name: "Zephyr" });
    expect(names()).toEqual(["Anchor", "Mast", "Zephyr"]);

    fireEvent.keyDown(screen.getByRole("combobox", { name: "Sort entries" }), { key: "Enter" });
    fireEvent.click(screen.getByRole("option", { name: "Recently updated" }));
    expect(names()).toEqual(["Zephyr", "Mast", "Anchor"]);
  });

  it("offers three ways to start an empty wiki", async () => {
    renderPanel([]);
    expect(await screen.findByRole("heading", { name: "No entries yet" })).toBeInTheDocument();
    // Nothing to filter yet: no search box, no chips.
    expect(screen.queryByRole("searchbox")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Add your first NPC" }));
    expect(await screen.findByRole("heading", { name: "New NPC" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));

    fireEvent.click(screen.getByRole("button", { name: "Add a location" }));
    expect(await screen.findByRole("heading", { name: "New Location" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));

    // The generator opens with a prompt to edit; nothing is sent for the GM.
    fireEvent.click(screen.getByRole("button", { name: "Generate a starting world" }));
    expect(
      ((await screen.findByLabelText("What should be drafted?")) as HTMLTextAreaElement).value,
    ).toMatch(/starting town/);
  });
});

describe("KnowledgePanel filters", () => {
  it("type chips show counts and narrow the list", async () => {
    renderPanel(world());
    const chips = await screen.findByRole("group", { name: "Filter by type" });
    expect(within(chips).getByRole("button", { name: "NPC 1" })).toHaveAttribute("aria-pressed", "false");
    // A type nothing has yet is not offered.
    expect(within(chips).queryByRole("button", { name: /Item/ })).not.toBeInTheDocument();

    fireEvent.click(within(chips).getByRole("button", { name: "Location 1" }));
    expect(screen.getByRole("button", { name: "Saltmarsh" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Bart" })).not.toBeInTheDocument();
    expect(screen.getByText("1 of 3 entries")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
    expect(screen.getByRole("button", { name: "Bart" })).toBeInTheDocument();
    expect(screen.getByText("3 entries")).toBeInTheDocument();
  });

  it("the same type chips narrow the relationship map", async () => {
    renderPanel(world(), { edges: worldEdges() });
    fireEvent.click(await screen.findByRole("button", { name: /Relationship map/ }));
    expect(await screen.findByLabelText("Bart (NPC)")).toBeInTheDocument();
    expect(screen.getByLabelText("Saltmarsh (Location)")).toBeInTheDocument();

    fireEvent.click(
      within(screen.getByRole("group", { name: "Filter by type" })).getByRole("button", { name: "Location 1" }),
    );
    expect(screen.getByLabelText("Saltmarsh (Location)")).toBeInTheDocument();
    expect(screen.queryByLabelText("Bart (NPC)")).not.toBeInTheDocument();
  });

  it("searching shows one flat list in the server's order, with type badges", async () => {
    const { searchCalls } = renderPanel([
      note("n1", "The sealed vault"),
      create(NodeSchema, { id: "n2", nodeType: NodeType.LOCATION, name: "Vault street" }),
      create(NodeSchema, { id: "n3", nodeType: NodeType.LOCATION, name: "Quiet Harbor" }),
    ]);
    await screen.findByRole("button", { name: "The sealed vault" });
    expect(searchCalls).toHaveLength(0);

    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "vault" } });
    const results = await screen.findByRole("list", { name: "Search results, best match first" });
    // No type groups while searching: the best match must not be buried.
    expect(screen.queryByRole("region", { name: "Location" })).not.toBeInTheDocument();
    await waitFor(() =>
      expect(
        within(results)
          .getAllByRole("button", { name: /vault/i })
          .filter((b) => b.classList.contains("gx-wiki-card__open"))
          .map((b) => b.textContent),
      ).toEqual(["Vault street", "The sealed vault"]),
    );
    expect(within(results).getByText("Location", { selector: ".gx-badge" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Quiet Harbor" })).not.toBeInTheDocument();
    expect(searchCalls.at(-1)).toBe("vault");
    const callsAfterSearch = searchCalls.length;

    // Clearing the box restores the full list from ListNodes: no search RPC.
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "" } });
    await screen.findByRole("button", { name: "Quiet Harbor" });
    expect(searchCalls).toHaveLength(callsAfterSearch);
  });

  it("on the map a search labels the matches and dims the rest", async () => {
    renderPanel(world(), { edges: worldEdges() });
    fireEvent.click(await screen.findByRole("button", { name: /Relationship map/ }));
    await screen.findByLabelText("Bart (NPC)");

    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "bart" } });
    const hit = await screen.findByLabelText("Bart (NPC), matches the search");
    expect(hit).toHaveAttribute("data-match", "hit");
    // Nothing is removed from the picture: a search on a map answers "where".
    expect(screen.getByLabelText("Saltmarsh (Location)")).toHaveAttribute("data-match", "miss");
    // So the count is of what is drawn, not of the matches.
    expect(screen.getByText("3 entries")).toBeInTheDocument();
    expect(screen.queryByText("1 of 3 entries")).not.toBeInTheDocument();
  });

  it("surfaces a failed search instead of silently showing the full list", async () => {
    renderPanel([note("n1", "The sealed vault"), note("n2", "Quiet Harbor")], { failSearch: true });
    await screen.findByRole("button", { name: "The sealed vault" });

    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "vault" } });

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/couldn't search/i);
    expect(alert).toHaveTextContent(/search boom/i);
    // A non-matching entry must not reappear while the box still holds a query.
    expect(screen.queryByRole("button", { name: "Quiet Harbor" })).not.toBeInTheDocument();
  });

  it("says so when the filters match nothing, with a way out", async () => {
    renderPanel([note("n1", "The sealed vault")]);
    await screen.findByRole("button", { name: "The sealed vault" });
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "dragon" } });
    expect(await screen.findByText("No entries match “dragon”.")).toBeInTheDocument();
    fireEvent.click(screen.getAllByRole("button", { name: "Clear filters" })[0]);
    expect(await screen.findByRole("button", { name: "The sealed vault" })).toBeInTheDocument();
  });

  it("does not claim no match for a search that is still in flight", async () => {
    let release!: () => void;
    const until = new Promise<void>((resolve) => {
      release = resolve;
    });
    const { searchCalls } = renderPanel([note("n1", "The sealed vault")], {
      holdSearch: { query: "vault", until },
    });
    await screen.findByRole("button", { name: "The sealed vault" });
    const box = screen.getByRole("searchbox");
    fireEvent.change(box, { target: { value: "xvault" } });
    expect(await screen.findByText("No entries match “xvault”.")).toBeInTheDocument();

    // "vault" is asked but unanswered: the kept empty answer is for "xvault".
    fireEvent.change(box, { target: { value: "vault" } });
    await waitFor(() => expect(searchCalls).toContain("vault"));
    expect(screen.queryByText(/No entries match/)).not.toBeInTheDocument();
    // Nor does the unfiltered list stand in for the answer.
    expect(screen.queryByRole("button", { name: "The sealed vault" })).not.toBeInTheDocument();

    release();
    expect(await screen.findByRole("button", { name: "The sealed vault" })).toBeInTheDocument();
    expect(screen.queryByText(/No entries match/)).not.toBeInTheDocument();
  });

  it("folds the tag row behind a count past eight tags, and a tag filters", async () => {
    const tagNames = ["a", "b", "c", "d", "e", "f", "g", "h", "seafaring"];
    renderPanel([note("n1", "Mast"), note("n2", "Anchor")], {
      tags: tagNames.map((tag) => ({ nodeId: tag === "seafaring" ? "n1" : "n2", tag })),
    });
    const group = await screen.findByRole("group", { name: "Filter by tag" });
    const fold = within(group).getByRole("button", { name: "Tags (9)" });
    expect(fold).toHaveAttribute("aria-expanded", "false");
    expect(within(group).queryByRole("button", { name: "seafaring" })).not.toBeInTheDocument();

    fireEvent.click(fold);
    fireEvent.click(within(group).getByRole("button", { name: "seafaring" }));
    expect(screen.queryByRole("button", { name: "Anchor" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Mast" })).toBeInTheDocument();

    // Folded again, the tag that is filtering stays in sight.
    fireEvent.click(fold);
    expect(within(group).getByRole("button", { name: "seafaring" })).toHaveAttribute("aria-pressed", "true");
    expect(within(group).queryByRole("button", { name: "a" })).not.toBeInTheDocument();
  });

  it("renaming a tag onto an existing one asks to merge and calls RenameTag once (#695)", async () => {
    const { renameCalls } = renderPanel([note("n1", "Mast"), note("n2", "Anchor")], {
      tags: [
        { nodeId: "n1", tag: "harbor" },
        { nodeId: "n2", tag: "harbour" },
        { nodeId: "n1", tag: "harbour" },
      ],
    });
    fireEvent.click(await screen.findByRole("button", { name: "Manage tags" }));
    const dialog = await screen.findByRole("dialog", { name: "Manage tags" });
    // Usage counts, so the GM sees which spelling is the stray one.
    expect(within(dialog).getByText("1 entry")).toBeInTheDocument();
    expect(within(dialog).getByText("2 entries")).toBeInTheDocument();

    fireEvent.click(within(dialog).getByRole("button", { name: "Rename tag harbor" }));
    fireEvent.change(within(dialog).getByLabelText("New name for tag harbor"), {
      target: { value: "Harbour" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Rename" }));

    // A merge cannot be undone, so it is a question first and no RPC yet.
    const confirm = await screen.findByRole("alertdialog");
    expect(confirm).toHaveTextContent("Merge “harbor” into “harbour”?");
    expect(renameCalls).toHaveLength(0);

    fireEvent.click(within(confirm).getByRole("button", { name: "Merge tags" }));
    await waitFor(() => expect(renameCalls).toHaveLength(1));
    // The surviving spelling is the existing tag's, not what was typed.
    expect(renameCalls[0]).toMatchObject({ from: "harbor", to: "harbour" });
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("Merged “harbor” into “harbour”"));
    await waitFor(() =>
      expect(within(dialog).queryByRole("button", { name: "Rename tag harbor" })).not.toBeInTheDocument(),
    );
    expect(renameCalls).toHaveLength(1);
  });

  it("a rename that differs only in spacing is still a merge, as the server sees it", async () => {
    const { renameCalls } = renderPanel([note("n1", "Mast"), note("n2", "Anchor")], {
      tags: [
        { nodeId: "n1", tag: "act two" },
        { nodeId: "n2", tag: "harbor" },
      ],
    });
    fireEvent.click(await screen.findByRole("button", { name: "Manage tags" }));
    const dialog = await screen.findByRole("dialog", { name: "Manage tags" });
    fireEvent.click(within(dialog).getByRole("button", { name: "Rename tag harbor" }));
    fireEvent.change(within(dialog).getByLabelText("New name for tag harbor"), {
      target: { value: " act   two " },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Rename" }));
    expect(await screen.findByRole("alertdialog")).toHaveTextContent("Merge “harbor” into “act two”?");
    expect(renameCalls).toHaveLength(0);
  });

  it("an open editor's tag chips follow a rename made in Manage tags", async () => {
    const { renameCalls } = renderPanel([note("n1", "Mast")], { tags: [{ nodeId: "n1", tag: "harbor" }] });
    fireEvent.click(await screen.findByRole("button", { name: "Edit Mast" }));
    const input = await screen.findByLabelText("Tags");
    await screen.findByRole("button", { name: "Remove harbor" });
    // A chip edit first, so the editor holds its own list.
    fireEvent.change(input, { target: { value: "rumour" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(await screen.findByRole("button", { name: "Remove rumour" })).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Manage tags" }));
    const dialog = await screen.findByRole("dialog", { name: "Manage tags" });
    fireEvent.click(await within(dialog).findByRole("button", { name: "Rename tag harbor" }));
    fireEvent.change(within(dialog).getByLabelText("New name for tag harbor"), { target: { value: "docks" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Rename" }));
    await waitFor(() => expect(renameCalls).toHaveLength(1));
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("Renamed “harbor” to “docks”"));
    fireEvent.keyDown(dialog, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Manage tags" })).not.toBeInTheDocument());

    await waitFor(() => expect(screen.getByRole("button", { name: "Remove docks" })).toBeInTheDocument());
    expect(screen.queryByRole("button", { name: "Remove harbor" })).not.toBeInTheDocument();
  });

  it("a plain rename does not ask", async () => {
    const { renameCalls } = renderPanel([note("n1", "Mast")], { tags: [{ nodeId: "n1", tag: "harbor" }] });
    fireEvent.click(await screen.findByRole("button", { name: "Manage tags" }));
    const dialog = await screen.findByRole("dialog", { name: "Manage tags" });
    fireEvent.click(within(dialog).getByRole("button", { name: "Rename tag harbor" }));
    fireEvent.change(within(dialog).getByLabelText("New name for tag harbor"), { target: { value: "docks" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Rename" }));
    await waitFor(() => expect(renameCalls).toHaveLength(1));
    expect(renameCalls[0]).toMatchObject({ from: "harbor", to: "docks" });
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  });
});

describe("KnowledgePanel entry page", () => {
  it("clicking a card shows the entry as prose, facts and links, and puts it in the URL", async () => {
    const { history } = renderPanel(world(), { edges: worldEdges() });
    const card = await screen.findByRole("button", { name: "Bart" });
    fireEvent.click(card);

    const page = await screen.findByTestId("entry-view");
    expect(within(page).getByRole("heading", { name: "Bart" })).toBeInTheDocument();
    // The body is rendered markdown, not a textarea and not raw asterisks.
    expect(within(page).getByText("Rusty Anchor").tagName).toBe("STRONG");
    expect(within(page).getAllByRole("listitem").map((li) => li.textContent)).toEqual(
      expect.arrayContaining(["pours short", "hears everything"]),
    );
    // Facts as a definition list; the secret one is marked.
    expect(within(page).getByText("Role").tagName).toBe("DT");
    const secret = within(page).getByText("Secret").closest("div")!;
    expect(secret).toHaveAttribute("data-private");
    expect(within(secret).getByText("GM only")).toBeInTheDocument();
    // Nothing on the page is an input.
    expect(within(page).queryByRole("textbox")).not.toBeInTheDocument();

    // A pushed navigation carrying the entry: linkable, and Back can undo it.
    expect(history.moves).toEqual([{ search: { node: "bart" }, replace: false }]);
    expect(card).toHaveAttribute("aria-current", "true");
  });

  it("a connection's name opens that entry, and Back returns to the previous one", async () => {
    const { history } = renderPanel(world(), { edges: worldEdges() });
    fireEvent.click(await screen.findByRole("button", { name: "Bart" }));
    const page = await screen.findByTestId("entry-view");

    const outgoing = await within(page).findByRole("list", { name: "Outgoing connections" });
    expect(outgoing).toHaveTextContent("lives in");
    fireEvent.click(within(outgoing).getByRole("button", { name: "Saltmarsh" }));

    await waitFor(() =>
      expect(within(screen.getByTestId("entry-view")).getByRole("heading", { name: "Saltmarsh" })).toBeInTheDocument(),
    );
    expect(history.current()).toEqual({ node: "town" });
    // On Saltmarsh's page the same relation reads from the other side.
    expect(await screen.findByRole("list", { name: "Incoming connections" })).toHaveTextContent(
      "Bart lives in Saltmarsh",
    );

    history.back();
    await waitFor(() =>
      expect(within(screen.getByTestId("entry-view")).getByRole("heading", { name: "Bart" })).toBeInTheDocument(),
    );
  });

  it("Edit swaps the page for the editor, and Done returns to the page", async () => {
    renderPanel(world());
    fireEvent.click(await screen.findByRole("button", { name: "Bart" }));
    fireEvent.click(within(await screen.findByTestId("entry-view")).getByRole("button", { name: "Edit" }));

    const editor = await screen.findByTestId("entry-editor");
    expect(within(editor).getByLabelText("Name")).toHaveValue("Bart");
    expect(screen.queryByTestId("entry-view")).not.toBeInTheDocument();

    fireEvent.click(within(editor).getByRole("button", { name: "Done" }));
    expect(await screen.findByTestId("entry-view")).toBeInTheDocument();
  });

  it("opens the entry a deep link names, in whatever mode the GM is in", async () => {
    const { history } = renderPanel(world(), { edges: worldEdges(), search: { node: "town" } });
    const page = await screen.findByTestId("entry-view");
    expect(within(page).getByRole("heading", { name: "Saltmarsh" })).toBeInTheDocument();
    // The param is the selection: it stays.
    expect(history.moves).toEqual([]);

    // On the map, a jump from the palette opens the entry and leaves the map up.
    fireEvent.click(screen.getByRole("button", { name: /Relationship map/ }));
    await screen.findByLabelText("Bart (NPC)");
    history.go({ node: "guild" });
    await waitFor(() =>
      expect(within(screen.getByTestId("entry-view")).getByRole("heading", { name: "Smugglers" })).toBeInTheDocument(),
    );
    expect(screen.getByRole("button", { name: /Relationship map/ })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByLabelText("Smugglers (Faction)")).toHaveAttribute("data-selected");
  });

  it("says so once and clears the param when ?node names nothing in this campaign", async () => {
    const { history } = renderPanel(world(), { search: { node: "no-such-entry" } });
    await screen.findByRole("button", { name: "Bart" });
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith("That entry is not in this campaign."));
    expect(history.moves).toEqual([{ search: {}, replace: true }]);
    expect(toast.error).toHaveBeenCalledTimes(1);
    expect(screen.queryByTestId("entry-view")).not.toBeInTheDocument();
  });

  it("looks a ?node the cached list does not know up once more before calling it missing", async () => {
    const { nodes, history } = renderPanel(world());
    await screen.findByRole("button", { name: "Bart" });
    // Made since the list was read: on another tab, or a proposal approved on
    // a phone. The palette's server-side search can already find it.
    nodes.push(create(NodeSchema, { id: "late", nodeType: NodeType.NOTE, name: "The late letter" }));
    history.go({ node: "late" });
    await waitFor(() =>
      expect(
        within(screen.getByTestId("entry-view")).getByRole("heading", { name: "The late letter" }),
      ).toBeInTheDocument(),
    );
    expect(toast.error).not.toHaveBeenCalled();
    expect(history.current()).toEqual({ node: "late" });
  });

  it("the same dead link followed twice is reported and cleared both times", async () => {
    const { history } = renderPanel(world());
    await screen.findByRole("button", { name: "Bart" });
    history.go({ node: "gone" });
    await waitFor(() => expect(toast.error).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(history.current()).toEqual({}));

    history.go({ node: "gone" });
    await waitFor(() => expect(toast.error).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(history.current()).toEqual({}));
  });

  it("an entry opened inside a folded type group unfolds it and marks its card", async () => {
    const { history } = renderPanel(world());
    const group = await screen.findByRole("region", { name: "Location" });
    fireEvent.click(within(group).getByRole("button", { name: "Location 1" }));
    expect(within(group).queryByRole("button", { name: "Saltmarsh" })).not.toBeInTheDocument();

    history.go({ node: "town" });
    expect(await within(group).findByRole("button", { name: "Saltmarsh" })).toHaveAttribute("aria-current", "true");
  });

  it("clicking a map node opens the same entry page the list opens (#534)", async () => {
    renderPanel(world(), { edges: worldEdges() });
    fireEvent.click(await screen.findByRole("button", { name: /Relationship map/ }));
    fireEvent.click(await screen.findByLabelText("Bart (NPC)"));
    expect(
      within(await screen.findByTestId("entry-view")).getByRole("heading", { name: "Bart" }),
    ).toBeInTheDocument();
  });

  it("the Boards mode lists the prep boards, and a board entry opens its page", async () => {
    renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Boards" }));
    const boards = await screen.findByRole("region", { name: "Prep boards" });
    // The filters belong to the list and the map.
    expect(screen.queryByRole("searchbox")).not.toBeInTheDocument();
    fireEvent.click(await within(boards).findByRole("button", { name: "The sealed vault" }));
    expect(
      within(await screen.findByTestId("entry-view")).getByRole("heading", { name: "The sealed vault" }),
    ).toBeInTheDocument();
  });

  it("keeps the duplicate scan's result across a mode switch", async () => {
    const ctx = renderPanel([note("n1", "The sealed vault"), note("n2", "The sealed crypt")]);
    fireEvent.click(await screen.findByRole("button", { name: "Checkup" }));
    fireEvent.click(await screen.findByRole("button", { name: "Check for duplicates" }));
    expect(await screen.findByText(/93% alike/)).toBeInTheDocument();

    // Following a finding to the list and coming back must not cost a rescan.
    fireEvent.click(screen.getByRole("button", { name: "List" }));
    await screen.findByRole("searchbox");
    fireEvent.click(screen.getByRole("button", { name: "Checkup" }));
    expect(await screen.findByText(/93% alike/)).toBeInTheDocument();
    expect(ctx.duplicateScans()).toBe(1);
  });
});

describe("KnowledgePanel create", () => {
  it("New entry > Location creates, selects the new entry and makes tags and connections available", async () => {
    const { nodes, createCalls, history } = renderPanel();
    await screen.findByRole("button", { name: "The sealed vault" });

    pickNewEntry("Location");
    const form = await screen.findByTestId("entry-editor");
    expect(within(form).getByRole("heading", { name: "New Location" })).toBeInTheDocument();
    // Until it exists there is nothing to link to.
    expect(within(form).queryByLabelText("Tags")).not.toBeInTheDocument();
    expect(within(form).getByText(/become available as soon as the entry is created/)).toBeInTheDocument();

    fireEvent.change(within(form).getByLabelText("Name"), { target: { value: "The old harbor" } });
    fireEvent.change(within(form).getByLabelText("Content"), { target: { value: "Ships dock here." } });
    fireEvent.click(within(form).getByRole("button", { name: "Create entry" }));

    await waitFor(() => expect(createCalls).toHaveLength(1));
    expect(createCalls[0].nodeType).toBe(NodeType.LOCATION);
    expect(createCalls[0].name).toBe("The old harbor");
    expect(nodes).toHaveLength(2);

    // Selected (in the URL, pushed) and open in the editor, where its tags and
    // connections can be given at once.
    await waitFor(() => expect(history.current()).toEqual({ node: "n2" }));
    const editor = await screen.findByTestId("entry-editor");
    expect(within(editor).getByRole("heading", { name: "The old harbor" })).toBeInTheDocument();
    // The form became the entry's editor, with the caret at its name: focus
    // did not fall to <body> when the create form went away.
    expect(within(editor).getByText("Edit entry")).toBeInTheDocument();
    expect(within(editor).getByLabelText("Name")).toHaveFocus();
    expect(await within(editor).findByLabelText("Tags")).toBeInTheDocument();
    expect(within(editor).getByRole("button", { name: "Add connection" })).toBeInTheDocument();
    expect(toast.success).toHaveBeenCalledWith("Created “The old harbor”");
    // Nothing called it "not in this campaign" while the list caught up.
    expect(toast.error).not.toHaveBeenCalled();
    expect(await screen.findByRole("button", { name: "The old harbor" })).toHaveAttribute("aria-current", "true");
  });

  it("creates a GM-only entry, sending the gm_private flag", async () => {
    const { createCalls } = renderPanel();
    await screen.findByRole("button", { name: "The sealed vault" });
    pickNewEntry("Note");
    fireEvent.change(await screen.findByLabelText("Name"), { target: { value: "Hidden cult" } });
    fireEvent.click(screen.getByLabelText(/gm-only/i));
    fireEvent.click(screen.getByRole("button", { name: "Create entry" }));
    await waitFor(() => expect(createCalls).toHaveLength(1));
    expect(createCalls[0].gmPrivate).toBe(true);
  });

  it("authors facts with their own visibility and sends them on create (#542)", async () => {
    const ctx = renderPanel([]);
    await screen.findByRole("heading", { name: "No entries yet" });
    pickNewEntry("NPC");
    fireEvent.change(await screen.findByLabelText("Name"), { target: { value: "Bart the innkeeper" } });

    fireEvent.click(screen.getByRole("button", { name: "Add fact" }));
    fireEvent.change(screen.getByLabelText("Fact 1 label"), { target: { value: "Role" } });
    fireEvent.change(screen.getByLabelText("Fact 1 text"), { target: { value: "Runs the Rusty Anchor" } });
    fireEvent.click(screen.getByRole("button", { name: "Add fact" }));
    fireEvent.change(screen.getByLabelText("Fact 2 label"), { target: { value: "Bribe" } });
    fireEvent.change(screen.getByLabelText("Fact 2 text"), { target: { value: "Took the smugglers' coin" } });

    // Only the second fact is GM-only: the entry itself stays public.
    const pill = screen.getByRole("button", { name: "Fact 2: Public. Make it GM only" });
    expect(pill).toHaveTextContent("Public");
    fireEvent.click(pill);
    expect(screen.getByRole("button", { name: "Fact 2: GM only. Make it public" })).toHaveTextContent("GM only");

    fireEvent.click(screen.getByRole("button", { name: "Create entry" }));
    await waitFor(() => expect(ctx.createCalls).toHaveLength(1));
    const sent = ctx.createCalls[0];
    expect(sent.gmPrivate).toBe(false);
    expect(sent.aspects.map((a) => [a.key, a.value, a.gmPrivate])).toEqual([
      ["Role", "Runs the Rusty Anchor", false],
      ["Bribe", "Took the smugglers' coin", true],
    ]);
  });

  it("offers per-type fact suggestions, and an NPC's secret starts GM only", async () => {
    const ctx = renderPanel([]);
    await screen.findByRole("heading", { name: "No entries yet" });
    pickNewEntry("NPC");
    fireEvent.change(await screen.findByLabelText("Name"), { target: { value: "Bart" } });

    const suggestions = screen.getByRole("list", { name: "Suggested facts" });
    expect(within(suggestions).getAllByRole("button").map((b) => b.textContent)).toEqual([
      "Role",
      "Appearance",
      "Wants",
      "Secret",
    ]);
    fireEvent.click(within(suggestions).getByRole("button", { name: "Add a “Secret” fact, GM only" }));
    expect(screen.getByLabelText("Fact 1 label")).toHaveValue("Secret");
    // The caret goes to the fact: the label is already there.
    expect(screen.getByLabelText("Fact 1 text")).toHaveFocus();
    // A label in use is not offered twice.
    expect(within(suggestions).queryByRole("button", { name: /Secret/ })).not.toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Fact 1 text"), { target: { value: "Bribed" } });
    fireEvent.click(screen.getByRole("button", { name: "Create entry" }));
    await waitFor(() => expect(ctx.createCalls).toHaveLength(1));
    expect(ctx.createCalls[0].aspects.map((a) => [a.key, a.gmPrivate])).toEqual([["Secret", true]]);
  });

  it("the facts are typed through with the keyboard: Enter, Backspace, Alt+arrows", async () => {
    const ctx = renderPanel([]);
    await screen.findByRole("heading", { name: "No entries yet" });
    pickNewEntry("Note");
    fireEvent.change(await screen.findByLabelText("Name"), { target: { value: "Bart" } });

    fireEvent.click(screen.getByRole("button", { name: "Add fact" }));
    const key1 = screen.getByLabelText("Fact 1 label");
    expect(key1).toHaveFocus();
    fireEvent.change(key1, { target: { value: "Role" } });
    // Enter in the label moves to the fact…
    fireEvent.keyDown(key1, { key: "Enter" });
    expect(screen.getByLabelText("Fact 1 text")).toHaveFocus();
    fireEvent.change(screen.getByLabelText("Fact 1 text"), { target: { value: "Innkeeper" } });
    // …and Enter in the fact starts the next row.
    fireEvent.keyDown(screen.getByLabelText("Fact 1 text"), { key: "Enter" });
    expect(screen.getByLabelText("Fact 2 label")).toHaveFocus();
    fireEvent.change(screen.getByLabelText("Fact 2 label"), { target: { value: "Manner" } });
    fireEvent.change(screen.getByLabelText("Fact 2 text"), { target: { value: "Grumbles" } });

    // Shift+Enter is a line break, not a new row.
    fireEvent.keyDown(screen.getByLabelText("Fact 2 text"), { key: "Enter", shiftKey: true });
    expect(screen.queryByLabelText("Fact 3 label")).not.toBeInTheDocument();

    // Alt+Up moves the row and keeps the caret in it.
    fireEvent.keyDown(screen.getByLabelText("Fact 2 text"), { key: "ArrowUp", altKey: true });
    expect(screen.getByLabelText("Fact 1 label")).toHaveValue("Manner");
    expect(screen.getByLabelText("Fact 1 text")).toHaveFocus();

    // Backspace in an empty row removes it and returns to the row above.
    fireEvent.keyDown(screen.getByLabelText("Fact 2 text"), { key: "Enter" });
    expect(screen.getByLabelText("Fact 3 label")).toHaveFocus();
    fireEvent.keyDown(screen.getByLabelText("Fact 3 label"), { key: "Backspace" });
    expect(screen.queryByLabelText("Fact 3 label")).not.toBeInTheDocument();
    expect(screen.getByLabelText("Fact 2 text")).toHaveFocus();

    // An untouched trailing row must not become a save error.
    fireEvent.click(screen.getByRole("button", { name: "Add fact" }));
    fireEvent.click(screen.getByRole("button", { name: "Create entry" }));
    await waitFor(() => expect(ctx.createCalls).toHaveLength(1));
    expect(ctx.createCalls[0].aspects.map((a) => a.key)).toEqual(["Manner", "Role"]);
  });

  it("keeps an unapplied generated draft across a mode switch (#534)", async () => {
    renderPanel();
    await screen.findByRole("button", { name: "The sealed vault" });
    pickNewEntry(/Generate entries with AI/);
    fireEvent.change(await screen.findByLabelText("What should be drafted?"), {
      target: { value: "the harbour heist" },
    });

    // Toggling views must not discard the prompt: the draft it produces is a
    // paid LLM call the GM has not reviewed yet.
    fireEvent.click(screen.getByRole("button", { name: /Relationship map/ }));
    fireEvent.click(await screen.findByRole("button", { name: "List" }));
    expect(await screen.findByLabelText("What should be drafted?")).toHaveValue("the harbour heist");
  });
});

describe("KnowledgePanel delete", () => {
  it("deletes an entry from its card via DeleteNode, after confirming", async () => {
    const { deleteCalls } = renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Delete The sealed vault" }));
    expect(await screen.findByRole("alertdialog")).toHaveTextContent(/sealed vault/i);
    expect(deleteCalls).toHaveLength(0);

    await confirmInDialog();
    await screen.findByRole("heading", { name: "No entries yet" });
    expect(deleteCalls).toHaveLength(1);
    expect(deleteCalls[0].id).toBe("n1");
    expect(toast.success).toHaveBeenCalledWith("Deleted “The sealed vault”");
  });

  it("cancelling or Escape issues no delete RPC and leaves the entry", async () => {
    const { deleteCalls } = renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Delete The sealed vault" }));
    let dialog = await screen.findByRole("alertdialog");
    fireEvent.click(within(dialog).getByRole("button", { name: /cancel/i }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());

    fireEvent.click(screen.getByRole("button", { name: "Delete The sealed vault" }));
    dialog = await screen.findByRole("alertdialog");
    fireEvent.keyDown(dialog, { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());

    expect(deleteCalls).toHaveLength(0);
    expect(screen.getByRole("button", { name: "The sealed vault" })).toBeInTheDocument();
  });

  it("shows the count of connections that will cascade-delete", async () => {
    const edges = [
      create(EdgeSchema, { id: "e1", fromNodeId: "n1", toNodeId: "n2", edgeType: EdgeType.KNOWS }),
      create(EdgeSchema, { id: "e2", fromNodeId: "n1", toNodeId: "n3", edgeType: EdgeType.OWNS }),
      create(EdgeSchema, { id: "e3", fromNodeId: "n4", toNodeId: "n1", edgeType: EdgeType.ALLY_OF }),
    ];
    renderPanel(undefined, { edges });
    fireEvent.click(await screen.findByRole("button", { name: "Delete The sealed vault" }));
    const dialog = await screen.findByRole("alertdialog");
    // Two outgoing + one incoming = three cascade edges.
    await waitFor(() => expect(dialog).toHaveTextContent(/3 connection/i));
  });

  it("deletes the open entry from the editor, then clears it from the URL", async () => {
    const { deleteCalls, history } = renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Edit The sealed vault" }));
    fireEvent.click(
      within(await screen.findByTestId("entry-editor")).getByRole("button", { name: "Delete entry" }),
    );
    expect(await screen.findByRole("alertdialog")).toBeInTheDocument();
    expect(deleteCalls).toHaveLength(0);

    await confirmInDialog();
    await screen.findByRole("heading", { name: "No entries yet" });
    expect(deleteCalls).toHaveLength(1);
    await waitFor(() => expect(history.current()).toEqual({}));
    // Its id leaving the list is the delete, not a broken link.
    expect(toast.error).not.toHaveBeenCalled();
    expect(screen.queryByTestId("entry-editor")).not.toBeInTheDocument();
  });

  it("when the cascade count can't be fetched, admits it instead of implying zero, and still deletes", async () => {
    const { deleteCalls } = renderPanel(undefined, { failEdges: true });
    fireEvent.click(await screen.findByRole("button", { name: "Delete The sealed vault" }));
    const dialog = await screen.findByRole("alertdialog");
    await waitFor(() => expect(dialog).toHaveTextContent(/couldn't be counted/i));

    await confirmInDialog();
    await screen.findByRole("heading", { name: "No entries yet" });
    expect(deleteCalls).toHaveLength(1);
  });

  it("holds the confirm disabled while the cascade count is still loading; Cancel still exits", async () => {
    const { deleteCalls } = renderPanel(undefined, { hangEdges: true });
    fireEvent.click(await screen.findByRole("button", { name: "Delete The sealed vault" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent(/counting its connections/i);
    const confirm = within(dialog).getByRole("button", { name: /delete entry/i });
    expect(confirm).toBeDisabled();
    fireEvent.click(confirm);
    expect(deleteCalls).toHaveLength(0);

    fireEvent.click(within(dialog).getByRole("button", { name: /cancel/i }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).toBeNull());
    expect(deleteCalls).toHaveLength(0);
  });

  it("refreshes the search results when an entry is deleted while searching", async () => {
    const { deleteCalls } = renderPanel([note("n1", "The sealed vault"), note("n2", "Quiet Harbor")]);
    await screen.findByRole("button", { name: "The sealed vault" });

    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "vault" } });
    await waitFor(() => expect(screen.queryByRole("button", { name: "Quiet Harbor" })).not.toBeInTheDocument());
    fireEvent.click(screen.getByRole("button", { name: "Delete The sealed vault" }));
    await confirmInDialog();

    // The mutation must invalidate the SEARCH cache too.
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "The sealed vault" })).not.toBeInTheDocument(),
    );
    expect(screen.getByText(/no entries match/i)).toBeInTheDocument();
    expect(deleteCalls).toHaveLength(1);
  });

  it("a failed delete says which entry it could not delete", async () => {
    renderPanel(undefined, { failDelete: true });
    fireEvent.click(await screen.findByRole("button", { name: "Delete The sealed vault" }));
    await confirmInDialog();

    await waitFor(() => expect(toast.error).toHaveBeenCalledTimes(1));
    const message = vi.mocked(toast.error).mock.calls[0][0] as string;
    expect(message).toContain("The sealed vault");
    expect(message).toContain("delete boom");
    expect(screen.getByRole("button", { name: "The sealed vault" })).toBeInTheDocument();
  });
});

describe("KnowledgePanel below 1100px", () => {
  const realMatchMedia = window.matchMedia;
  beforeEach(() => {
    // The "stack" breakpoint is true: there is no column beside the list.
    window.matchMedia = ((query: string) => ({
      matches: query === "(max-width: 1100px)",
      media: query,
      addEventListener: () => {},
      removeEventListener: () => {},
    })) as unknown as typeof window.matchMedia;
  });
  afterEach(() => {
    window.matchMedia = realMatchMedia;
  });

  it("opens the entry in a sheet instead of beside the list", async () => {
    const { history } = renderPanel(world());
    fireEvent.click(await screen.findByRole("button", { name: "Bart" }));

    const sheet = await screen.findByRole("dialog", { name: "Bart" });
    expect(sheet).toHaveAttribute("data-variant", "sheet");
    expect(within(sheet).getByTestId("entry-view")).toBeInTheDocument();
    expect(screen.queryByRole("complementary", { name: "Selected entry" })).not.toBeInTheDocument();

    // Closing the sheet closes the entry: the URL lets go of it.
    fireEvent.click(within(sheet).getByRole("button", { name: "Close entry" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(history.current()).toEqual({});
  });
});
