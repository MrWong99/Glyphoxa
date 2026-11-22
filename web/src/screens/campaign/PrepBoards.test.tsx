import { describe, it, expect, vi, afterEach } from "vitest";
import { act, render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { toast, Toaster } from "sonner";

import {
  BoardSchema,
  CampaignService,
  CreateBoardResponseSchema,
  DeleteBoardResponseSchema,
  ListBoardsResponseSchema,
  ListNodesResponseSchema,
  NodeSchema,
  NodeType,
  UpdateBoardResponseSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { NodeBoards, PrepBoards } from "./PrepBoards";

// Prep boards against a small in-memory store: UpdateBoard really replaces the
// board's name and entry list, so every test reads the result of its write off
// the screen after the refetch, not just the request.

type BoardRow = { id: string; name: string; nodeIds: string[] };

const NODES = [
  { id: "n1", nodeType: NodeType.LOCATION, name: "Oakhurst" },
  { id: "n2", nodeType: NodeType.NPC, name: "Kerowyn Hucrele" },
  { id: "n3", nodeType: NodeType.ITEM, name: "Gulthias Fruit" },
  { id: "n4", nodeType: NodeType.FACTION, name: "The Hucrele Trading House" },
];

function store(init: { boards?: BoardRow[]; failUpdate?: { on: boolean } } = {}) {
  const boards: BoardRow[] = (init.boards ?? [{ id: "b1", name: "Tonight", nodeIds: ["n1", "n2", "n3"] }]).map(
    (b) => ({ ...b, nodeIds: [...b.nodeIds] }),
  );
  const updates: BoardRow[] = [];
  const creates: string[] = [];
  const deletes: string[] = [];
  let nextID = 2;

  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      listBoards: () =>
        create(ListBoardsResponseSchema, { boards: boards.map((b) => create(BoardSchema, b)) }),
      listNodes: () => create(ListNodesResponseSchema, { nodes: NODES.map((n) => create(NodeSchema, n)) }),
      createBoard: (req) => {
        creates.push(req.name);
        const b = { id: `b${nextID++}`, name: req.name, nodeIds: [] };
        boards.push(b);
        return create(CreateBoardResponseSchema, { board: create(BoardSchema, b) });
      },
      updateBoard: (req) => {
        updates.push({ id: req.id, name: req.name, nodeIds: [...req.nodeIds] });
        if (init.failUpdate?.on) throw new ConnectError("the board is locked", Code.FailedPrecondition);
        const b = boards.find((x) => x.id === req.id);
        if (!b) throw new ConnectError("board or entry not found in this campaign", Code.NotFound);
        b.name = req.name;
        b.nodeIds = [...req.nodeIds];
        return create(UpdateBoardResponseSchema, { board: create(BoardSchema, b) });
      },
      deleteBoard: (req) => {
        deletes.push(req.id);
        const i = boards.findIndex((x) => x.id === req.id);
        if (i >= 0) boards.splice(i, 1);
        return create(DeleteBoardResponseSchema, {});
      },
    });
  });
  return { transport, boards, updates, creates, deletes };
}

type Store = ReturnType<typeof store>;

function mount(s: Store = store(), onOpenNode?: (id: string) => void) {
  render(
    <Providers transport={s.transport} queryClient={makeQueryClient()}>
      <PrepBoards onOpenNode={onOpenNode} />
      <Toaster />
    </Providers>,
  );
  return s;
}

afterEach(() => {
  act(() => {
    toast.dismiss();
  });
});

/** The board's entries, top to bottom, as the GM reads them. */
const theList = () => within(screen.getByRole("region", { name: "Prep boards" })).getByRole("list");
const entries = () =>
  within(theList())
    .getAllByRole("listitem")
    .map((li) => li.querySelector(".gx-boards__entry-name")?.textContent);

// Radix opens a dropdown menu from the keyboard on Enter.
function openBoardMenu(name = "Tonight") {
  const trigger = screen.getByRole("button", { name: `Actions for board ${name}` });
  act(() => trigger.focus());
  fireEvent.keyDown(trigger, { key: "Enter" });
}

// W15: on the Session screen the boards are a reference list (no onOpenNode),
// but every entry still rendered as a button that did nothing when pressed.
describe("PrepBoards entries", () => {
  it("are plain text where there is nothing to open", async () => {
    mount();
    expect(await screen.findByText("Oakhurst")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Oakhurst" })).not.toBeInTheDocument();
  });

  it("are buttons that open the entry where a handler is wired", async () => {
    const onOpenNode = vi.fn();
    mount(store(), onOpenNode);
    fireEvent.click(await screen.findByRole("button", { name: "Oakhurst" }));
    expect(onOpenNode).toHaveBeenCalledWith("n1");
  });

  it("are a numbered list in board order", async () => {
    mount();
    await screen.findByText("Oakhurst");
    // An <ol>: the order is the point, and a screen reader hears "1 of 3".
    expect(theList().tagName).toBe("OL");
    expect(entries()).toEqual(["Oakhurst", "Kerowyn Hucrele", "Gulthias Fruit"]);
  });
});

describe("PrepBoards rename", () => {
  it("renames from a click on the name, keeping the entries", async () => {
    const s = mount();
    fireEvent.click(await screen.findByRole("button", { name: "Tonight" }));

    const field = screen.getByRole("textbox", { name: "New name for board Tonight" });
    expect(field).toHaveValue("Tonight");
    await waitFor(() => expect(field).toHaveFocus());
    fireEvent.change(field, { target: { value: "  The harbour heist " } });
    fireEvent.keyDown(field, { key: "Enter" });

    // UpdateBoard replaces name AND entries: the rename must carry the list.
    await waitFor(() =>
      expect(s.updates).toEqual([{ id: "b1", name: "The harbour heist", nodeIds: ["n1", "n2", "n3"] }]),
    );
    const renamed = await screen.findByRole("button", { name: "The harbour heist" });
    expect(screen.getByRole("heading", { name: "The harbour heist" })).toBeInTheDocument();
    // Focus is back on the name, not lost to <body>.
    await waitFor(() => expect(renamed).toHaveFocus());
    expect(entries()).toEqual(["Oakhurst", "Kerowyn Hucrele", "Gulthias Fruit"]);
  });

  it("Escape leaves the name alone, and an empty name is not a rename", async () => {
    const s = mount();
    fireEvent.click(await screen.findByRole("button", { name: "Tonight" }));
    const field = screen.getByRole("textbox", { name: "New name for board Tonight" });

    fireEvent.change(field, { target: { value: "   " } });
    fireEvent.click(screen.getByRole("button", { name: "Save name" }));
    // Still editing: nothing was sent.
    expect(screen.getByRole("textbox", { name: "New name for board Tonight" })).toBeInTheDocument();

    fireEvent.change(field, { target: { value: "Something else" } });
    fireEvent.keyDown(field, { key: "Escape" });
    expect(await screen.findByRole("button", { name: "Tonight" })).toBeInTheDocument();
    expect(s.updates).toEqual([]);
  });

  it("is also in the board's menu", async () => {
    mount();
    await screen.findByText("Oakhurst");
    openBoardMenu();
    fireEvent.click(await screen.findByRole("menuitem", { name: "Rename" }));
    const field = await screen.findByRole("textbox", { name: "New name for board Tonight" });
    await waitFor(() => expect(field).toHaveFocus());
  });
});

describe("PrepBoards reorder", () => {
  it("moves an entry with the up/down buttons through UpdateBoard's ordered node_ids", async () => {
    const s = mount();
    await screen.findByText("Oakhurst");

    const down = screen.getByRole("button", { name: "Move Oakhurst down" });
    act(() => down.focus());
    fireEvent.click(down);

    await waitFor(() => expect(s.updates).toEqual([{ id: "b1", name: "Tonight", nodeIds: ["n2", "n1", "n3"] }]));
    expect(entries()).toEqual(["Kerowyn Hucrele", "Oakhurst", "Gulthias Fruit"]);
    // The button that moved the row went with it and still has focus, so a
    // second press moves the same entry again.
    expect(screen.getByRole("button", { name: "Move Oakhurst down" })).toHaveFocus();
  });

  it("moves with Alt+Arrow from any control in the row", async () => {
    const s = mount(store(), vi.fn());
    const name = await screen.findByRole("button", { name: "Gulthias Fruit" });
    act(() => name.focus());

    fireEvent.keyDown(name, { key: "ArrowUp", altKey: true });
    await waitFor(() => expect(s.updates[0]?.nodeIds).toEqual(["n1", "n3", "n2"]));
    expect(entries()).toEqual(["Oakhurst", "Gulthias Fruit", "Kerowyn Hucrele"]);
    expect(screen.getByRole("button", { name: "Gulthias Fruit" })).toHaveFocus();

    // A plain arrow is not a move.
    fireEvent.keyDown(screen.getByRole("button", { name: "Gulthias Fruit" }), { key: "ArrowUp" });
    expect(entries()).toEqual(["Oakhurst", "Gulthias Fruit", "Kerowyn Hucrele"]);
  });

  it("does nothing at the ends — and the end buttons stay focusable", async () => {
    const s = mount();
    await screen.findByText("Oakhurst");
    const firstUp = screen.getByRole("button", { name: "Move Oakhurst up" });
    const lastDown = screen.getByRole("button", { name: "Move Gulthias Fruit down" });
    expect(firstUp).toHaveAttribute("aria-disabled", "true");
    expect(firstUp).not.toBeDisabled();
    expect(lastDown).toHaveAttribute("aria-disabled", "true");

    fireEvent.click(firstUp);
    fireEvent.click(lastDown);
    expect(s.updates).toEqual([]);
  });

  it("three quick moves are three steps: the writes queue, each built on the one before", async () => {
    const s = mount();
    await screen.findByText("Oakhurst");

    // Oakhurst down twice, then Kerowyn down once — pressed faster than any
    // response can arrive. UpdateBoard replaces the whole list, so overlapping
    // writes built from stale lists would lose steps.
    fireEvent.click(screen.getByRole("button", { name: "Move Oakhurst down" }));
    fireEvent.click(screen.getByRole("button", { name: "Move Oakhurst down" }));
    fireEvent.click(screen.getByRole("button", { name: "Move Kerowyn Hucrele down" }));
    // Shown at once, before the server has said anything.
    expect(entries()).toEqual(["Gulthias Fruit", "Kerowyn Hucrele", "Oakhurst"]);

    await waitFor(() => expect(s.updates).toHaveLength(3));
    expect(s.updates.map((u) => u.nodeIds)).toEqual([
      ["n2", "n1", "n3"],
      ["n2", "n3", "n1"],
      ["n3", "n2", "n1"],
    ]);
    await waitFor(() => expect(s.boards[0].nodeIds).toEqual(["n3", "n2", "n1"]));
    expect(entries()).toEqual(["Gulthias Fruit", "Kerowyn Hucrele", "Oakhurst"]);
  });

  it("a refused move says so and goes back to what the server has", async () => {
    const failUpdate = { on: true };
    mount(store({ failUpdate }));
    await screen.findByText("Oakhurst");

    fireEvent.click(screen.getByRole("button", { name: "Move Oakhurst down" }));
    expect(await screen.findByText("Couldn't update the board: the board is locked")).toBeInTheDocument();
    await waitFor(() => expect(entries()).toEqual(["Oakhurst", "Kerowyn Hucrele", "Gulthias Fruit"]));
  });
});

describe("PrepBoards entries in and out", () => {
  it("adds an entry from the board's own search box", async () => {
    const s = mount();
    await screen.findByText("Oakhurst");

    fireEvent.click(screen.getByRole("button", { name: "Add an entry to Tonight" }));
    // Only what is not on the board yet is offered.
    expect(screen.getAllByRole("option").map((o) => o.textContent)).toEqual(["The Hucrele Trading House"]);
    fireEvent.click(screen.getByRole("option", { name: "The Hucrele Trading House" }));

    await waitFor(() =>
      expect(s.updates).toEqual([{ id: "b1", name: "Tonight", nodeIds: ["n1", "n2", "n3", "n4"] }]),
    );
    expect(entries()).toEqual(["Oakhurst", "Kerowyn Hucrele", "Gulthias Fruit", "The Hucrele Trading House"]);
    // The box is ready for the next entry, not showing the last one.
    expect(screen.getByRole("button", { name: "Add an entry to Tonight" })).toHaveTextContent("Add entry…");
  });

  it("fills an empty board the same way", async () => {
    const s = mount(store({ boards: [{ id: "b1", name: "Tonight", nodeIds: [] }] }));
    expect(await screen.findByText("Nothing on this board yet. Add entries below.")).toBeInTheDocument();

    const add = screen.getByRole("button", { name: "Add an entry to Tonight" });
    await waitFor(() => expect(add).toBeEnabled());
    fireEvent.click(add);
    fireEvent.change(screen.getByPlaceholderText("Search entries…"), { target: { value: "oak" } });
    fireEvent.click(screen.getByRole("option", { name: "Oakhurst" }));

    await waitFor(() => expect(s.updates).toEqual([{ id: "b1", name: "Tonight", nodeIds: ["n1"] }]));
    expect(entries()).toEqual(["Oakhurst"]);
  });

  it("removes an entry from the board", async () => {
    const s = mount();
    await screen.findByText("Oakhurst");
    fireEvent.click(screen.getByRole("button", { name: "Remove Kerowyn Hucrele from Tonight" }));
    await waitFor(() => expect(s.updates).toEqual([{ id: "b1", name: "Tonight", nodeIds: ["n1", "n3"] }]));
    expect(entries()).toEqual(["Oakhurst", "Gulthias Fruit"]);
  });
});

describe("PrepBoards boards", () => {
  it("duplicates a board with its entries in order", async () => {
    const s = mount();
    await screen.findByText("Oakhurst");
    openBoardMenu();
    fireEvent.click(await screen.findByRole("menuitem", { name: "Duplicate" }));

    await waitFor(() => expect(s.creates).toEqual(["Tonight (copy)"]));
    await waitFor(() =>
      expect(s.updates).toEqual([{ id: "b2", name: "Tonight (copy)", nodeIds: ["n1", "n2", "n3"] }]),
    );
    expect(await screen.findByRole("heading", { name: "Tonight (copy)" })).toBeInTheDocument();
    expect(await screen.findByText("Duplicated “Tonight”")).toBeInTheDocument();
  });

  it("creates a board from the name field", async () => {
    const s = mount(store({ boards: [] }));
    expect(await screen.findByText("No boards yet")).toBeInTheDocument();

    const field = screen.getByRole("textbox", { name: "New board name" });
    fireEvent.change(field, { target: { value: "Next week" } });
    fireEvent.keyDown(field, { key: "Enter" });

    await waitFor(() => expect(s.creates).toEqual(["Next week"]));
    expect(await screen.findByRole("heading", { name: "Next week" })).toBeInTheDocument();
    expect(field).toHaveValue("");
  });

  it("deletes a board behind a confirm gate", async () => {
    const s = mount();
    await screen.findByText("Oakhurst");
    openBoardMenu();
    fireEvent.click(await screen.findByRole("menuitem", { name: "Delete board" }));
    const confirm = await screen.findByRole("alertdialog");
    fireEvent.click(within(confirm).getByRole("button", { name: "Delete board" }));

    await waitFor(() => expect(s.deletes).toEqual(["b1"]));
    expect(await screen.findByText("No boards yet")).toBeInTheDocument();
  });
});

// ── NodeBoards: the wiki editor's "put this on a board" field ────────────────

function mountNodeBoards(s: Store, nodeID = "n4") {
  render(
    <Providers transport={s.transport} queryClient={makeQueryClient()}>
      <NodeBoards nodeID={nodeID} />
    </Providers>,
  );
  return s;
}

describe("NodeBoards", () => {
  it("offers a 'New board' input when no board exists, and puts the entry on the board it makes", async () => {
    const s = mountNodeBoards(store({ boards: [] }));
    // It used to render nothing here, so the feature was invisible from the wiki.
    const field = await screen.findByRole("textbox", { name: "New board name" });
    expect(screen.getByText(/A board is a shortlist for one session/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New board" })).toBeDisabled();

    fireEvent.change(field, { target: { value: "Tonight" } });
    fireEvent.click(screen.getByRole("button", { name: "New board" }));

    await waitFor(() => expect(s.creates).toEqual(["Tonight"]));
    await waitFor(() => expect(s.updates).toEqual([{ id: "b2", name: "Tonight", nodeIds: ["n4"] }]));
    // The board exists now and the entry is on it.
    expect(await screen.findByRole("button", { name: "Tonight", pressed: true })).toBeInTheDocument();
  });

  it("toggles membership of an existing board, and can still start a new one", async () => {
    const s = mountNodeBoards(store());
    const chip = await screen.findByRole("button", { name: "Tonight", pressed: false });
    // The input is tucked away while there are boards to pick from.
    expect(screen.queryByRole("textbox", { name: "New board name" })).toBeNull();

    fireEvent.click(chip);
    await waitFor(() =>
      expect(s.updates).toEqual([{ id: "b1", name: "Tonight", nodeIds: ["n1", "n2", "n3", "n4"] }]),
    );
    expect(await screen.findByRole("button", { name: "Tonight", pressed: true })).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "New board" }));
    const field = screen.getByRole("textbox", { name: "New board name" });
    fireEvent.change(field, { target: { value: "Next week" } });
    fireEvent.keyDown(field, { key: "Enter" });
    await waitFor(() => expect(s.creates).toEqual(["Next week"]));
    expect(await screen.findByRole("button", { name: "Next week", pressed: true })).toBeInTheDocument();
  });

  it("says so when the board cannot be updated", async () => {
    mountNodeBoards(store({ failUpdate: { on: true } }));
    fireEvent.click(await screen.findByRole("button", { name: "Tonight", pressed: false }));
    expect(await screen.findByRole("alert")).toHaveTextContent("Couldn't update the board: the board is locked");
  });

  it("renders nothing until the boards have loaded", () => {
    const { container } = render(
      <Providers transport={store().transport} queryClient={makeQueryClient()}>
        <NodeBoards nodeID="n1" />
      </Providers>,
    );
    expect(container).toBeEmptyDOMElement();
  });
});
