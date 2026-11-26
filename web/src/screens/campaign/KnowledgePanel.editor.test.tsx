import { describe, it, expect, vi, beforeEach } from "vitest";
import { act, render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { toast } from "sonner";

import {
  CampaignService,
  GetCampaignRosterResponseSchema,
  GetCampaignTagsResponseSchema,
  ListBoardsResponseSchema,
  ListNodeAppearancesResponseSchema,
  ListNodeEdgesResponseSchema,
  ListNodesResponseSchema,
  NodeSchema,
  NodeType,
  SetNodeTagsResponseSchema,
  UpdateNodeResponseSchema,
  type UpdateNodeRequest,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { invalidateKnowledgeReads } from "./knowledgeCache";
import { KnowledgePanel } from "./KnowledgePanel";
import { WikiNav, makeWikiHistory } from "./wikiTestKit";

vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() } }));

// The wiki entry editor: it STAYS OPEN after a save and keeps the next save
// from duplicating what the last one created, follows the live row (W7), does
// not wear another entry's mutation status (W13), rolls a rejected tag back
// (W12) and asks before throwing typed text away (W10).
function backend(
  opts: {
    failUpdateFor?: string;
    failTags?: boolean;
    /** Resolves each UpdateNode only when the test says so. */
    holdUpdates?: boolean;
    /** Hold each SetNodeTags until the test releases it. */
    holdTags?: boolean;
  } = {},
) {
  const nodes = [
    create(NodeSchema, {
      id: "n1",
      nodeType: NodeType.LOCATION,
      name: "Oakhurst",
      body: "A village.",
      aspects: [{ id: "a1", key: "Atmosphere", value: "Sleepy" }],
    }),
    create(NodeSchema, { id: "n2", nodeType: NodeType.LOCATION, name: "The Citadel", body: "Sunken." }),
  ];
  let tags: { nodeId: string; tag: string }[] = [{ nodeId: "n1", tag: "act one" }];
  const updateCalls: UpdateNodeRequest[] = [];
  const tagCalls: string[][] = [];
  const releases: (() => void)[] = [];
  let nextAspect = 1;
  const hold = () => new Promise<void>((resolve) => releases.push(resolve));

  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      listNodes: () => create(ListNodesResponseSchema, { nodes }),
      getCampaignTags: () => create(GetCampaignTagsResponseSchema, { entries: tags }),
      getCampaignRoster: () => create(GetCampaignRosterResponseSchema, {}),
      listBoards: () => create(ListBoardsResponseSchema, { boards: [] }),
      listNodeEdges: () => create(ListNodeEdgesResponseSchema, {}),
      listNodeAppearances: () => create(ListNodeAppearancesResponseSchema, {}),
      updateNode: async (req) => {
        updateCalls.push(req);
        if (opts.holdUpdates) await hold();
        if (req.id === opts.failUpdateFor) throw new ConnectError("name already taken", Code.AlreadyExists);
        const i = nodes.findIndex((n) => n.id === req.id);
        nodes[i] = create(NodeSchema, {
          ...nodes[i],
          name: req.name,
          body: req.body,
          // The server gives every new fact an id, exactly once.
          aspects: req.aspects.map((a) => ({ ...a, id: a.id || `srv-${nextAspect++}` })),
        });
        return create(UpdateNodeResponseSchema, { node: nodes[i] });
      },
      setNodeTags: async (req) => {
        tagCalls.push([...req.tags]);
        if (opts.holdTags) await hold();
        if (opts.failTags) throw new ConnectError("too many tags", Code.InvalidArgument);
        tags = [...tags.filter((e) => e.nodeId !== req.nodeId), ...req.tags.map((tag) => ({ nodeId: req.nodeId, tag }))];
        return create(SetNodeTagsResponseSchema, { tags: req.tags });
      },
    });
  });
  return {
    transport,
    nodes,
    updateCalls,
    tagCalls,
    /** Lets the oldest held RPC finish. */
    release: async () => {
      await waitFor(() => expect(releases.length).toBeGreaterThan(0));
      await act(async () => releases.shift()!());
    },
  };
}

function mount(opts: Parameters<typeof backend>[0] = {}) {
  const h = backend(opts);
  const queryClient = makeQueryClient();
  const history = makeWikiHistory();
  render(
    <Providers transport={h.transport} queryClient={queryClient}>
      <WikiNav history={history}>
        <KnowledgePanel />
      </WikiNav>
    </Providers>,
  );
  return { ...h, queryClient, history };
}

const nameField = () => screen.getByLabelText("Name") as HTMLInputElement;
const openEditor = async (name: string) => {
  fireEvent.click(await screen.findByRole("button", { name: `Edit ${name}` }));
  return screen.findByTestId("entry-editor");
};
const save = () => fireEvent.click(screen.getByRole("button", { name: "Save entry" }));

beforeEach(() => {
  vi.mocked(toast.success).mockClear();
  vi.mocked(toast.error).mockClear();
});

describe("KnowledgePanel entry editor: saving", () => {
  it("stays open after a save and says it saved", async () => {
    const { updateCalls } = mount();
    const editor = await openEditor("Oakhurst");
    // The type is fixed once an entry exists, so it is stated, not a control.
    expect(within(editor).queryByRole("combobox", { name: "Type" })).not.toBeInTheDocument();
    expect(within(editor).getByText("The type can't be changed once an entry exists.")).toBeInTheDocument();

    fireEvent.change(nameField(), { target: { value: "Oakhurst (burned)" } });
    expect(within(editor).getByText("Unsaved changes")).toBeInTheDocument();
    save();

    await waitFor(() => expect(updateCalls).toHaveLength(1));
    expect(updateCalls[0]).toMatchObject({ id: "n1", name: "Oakhurst (burned)" });
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("Saved “Oakhurst (burned)”"));
    // Still the editor, on the same entry, with what was typed, and clean.
    expect(screen.getByTestId("entry-editor")).toBe(editor);
    expect(nameField().value).toBe("Oakhurst (burned)");
    await waitFor(() => expect(within(editor).queryByText("Unsaved changes")).not.toBeInTheDocument());
    expect(within(editor).getByRole("button", { name: "Done" })).toBeInTheDocument();
  });

  it("a second save after adding a fact does not duplicate the fact", async () => {
    const { updateCalls } = mount();
    await openEditor("Oakhurst");

    fireEvent.click(screen.getByRole("button", { name: "Add fact" }));
    fireEvent.change(screen.getByLabelText("Fact 2 label"), { target: { value: "Dangers" } });
    fireEvent.change(screen.getByLabelText("Fact 2 text"), { target: { value: "Wolves" } });
    save();
    await waitFor(() => expect(updateCalls).toHaveLength(1));
    // The new fact goes out with no id: the server creates it.
    expect(updateCalls[0].aspects.map((a) => a.id)).toEqual(["a1", ""]);
    expect(updateCalls[0].knownAspectIds).toEqual(["a1"]);
    await waitFor(() => expect(toast.success).toHaveBeenCalledTimes(1));

    // A third fact, and a second save from the editor that never closed.
    fireEvent.click(screen.getByRole("button", { name: "Add fact" }));
    fireEvent.change(screen.getByLabelText("Fact 3 label"), { target: { value: "Who is here" } });
    fireEvent.change(screen.getByLabelText("Fact 3 text"), { target: { value: "A miller" } });
    save();
    await waitFor(() => expect(updateCalls).toHaveLength(2));
    // The fact the first save created now carries the id the server gave it,
    // so it is updated in place instead of inserted a second time…
    expect(updateCalls[1].aspects.map((a) => [a.id, a.key])).toEqual([
      ["a1", "Atmosphere"],
      ["srv-1", "Dangers"],
      ["", "Who is here"],
    ]);
    // …and the editor names it as loaded, so removing it later really deletes it.
    expect(updateCalls[1].knownAspectIds).toEqual(["a1", "srv-1"]);
  });

  it("the editor reopened right after a save also carries the new ids", async () => {
    const { updateCalls } = mount();
    await openEditor("Oakhurst");
    fireEvent.click(screen.getByRole("button", { name: "Add fact" }));
    fireEvent.change(screen.getByLabelText("Fact 2 label"), { target: { value: "Dangers" } });
    fireEvent.change(screen.getByLabelText("Fact 2 text"), { target: { value: "Wolves" } });
    save();
    await waitFor(() => expect(toast.success).toHaveBeenCalledTimes(1));

    // Leave the editor and come straight back: it seeds from the list, which
    // the save's own response must already have reached.
    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    fireEvent.click(within(await screen.findByTestId("entry-view")).getByRole("button", { name: "Edit" }));
    await screen.findByTestId("entry-editor");
    save();
    await waitFor(() => expect(updateCalls).toHaveLength(2));
    expect(updateCalls[1].aspects.map((a) => a.id)).toEqual(["a1", "srv-1"]);
  });

  it("keeps what is typed while a save is in flight, and keeps it unsaved", async () => {
    const { updateCalls, release } = mount({ holdUpdates: true });
    const editor = await openEditor("Oakhurst");
    const content = screen.getByLabelText("Content") as HTMLTextAreaElement;
    fireEvent.change(content, { target: { value: "A village, now ash." } });
    save();
    await waitFor(() => expect(updateCalls).toHaveLength(1));
    const saveButton = screen.getByRole("button", { name: "Saving…" });
    // Busy, but not disabled: a disabled button would drop keyboard focus.
    expect(saveButton).toHaveAttribute("aria-busy", "true");
    expect(saveButton).not.toBeDisabled();
    // A second press while saving is ignored.
    fireEvent.click(saveButton);
    expect(updateCalls).toHaveLength(1);

    // The GM keeps typing before the save returns.
    fireEvent.change(content, { target: { value: "A village, now ash. The mill still stands." } });
    await release();

    await waitFor(() => expect(toast.success).toHaveBeenCalledTimes(1));
    // Same textarea (no remount), same text, and it is still unsaved.
    expect(screen.getByLabelText("Content")).toBe(content);
    expect(content.value).toBe("A village, now ash. The mill still stands.");
    expect(within(editor).getByText("Unsaved changes")).toBeInTheDocument();
    expect(updateCalls[0].body).toBe("A village, now ash.");
  });

  it("Ctrl+S saves from inside the editor and keeps the caret where it was", async () => {
    const { updateCalls } = mount();
    await openEditor("Oakhurst");
    const content = screen.getByLabelText("Content") as HTMLTextAreaElement;
    act(() => content.focus());
    fireEvent.change(content, { target: { value: "A village by the weir." } });

    const event = new KeyboardEvent("keydown", { key: "s", ctrlKey: true, bubbles: true, cancelable: true });
    act(() => {
      content.dispatchEvent(event);
    });
    // The browser's own "save page" dialog is suppressed.
    expect(event.defaultPrevented).toBe(true);

    await waitFor(() => expect(updateCalls).toHaveLength(1));
    expect(updateCalls[0].body).toBe("A village by the weir.");
    await waitFor(() => expect(toast.success).toHaveBeenCalledTimes(1));
    expect(content).toHaveFocus();
  });

  it("shows a failed save in the editor, which stays open with the typing", async () => {
    mount({ failUpdateFor: "n1" });
    const editor = await openEditor("Oakhurst");
    fireEvent.change(nameField(), { target: { value: "The Citadel" } });
    save();
    expect(await within(editor).findByRole("alert")).toHaveTextContent("name already taken");
    expect(nameField().value).toBe("The Citadel");
    expect(toast.success).not.toHaveBeenCalled();
  });

  it("previews the content as rendered markdown without losing the text", async () => {
    await mount();
    const editor = await openEditor("Oakhurst");
    fireEvent.change(screen.getByLabelText("Content"), { target: { value: "A **quiet** village." } });

    fireEvent.click(within(editor).getByRole("button", { name: "Preview" }));
    const preview = within(editor).getByTestId("content-preview");
    expect(within(preview).getByText("quiet").tagName).toBe("STRONG");
    expect(screen.queryByLabelText("Content")).not.toBeInTheDocument();

    fireEvent.click(within(editor).getByRole("button", { name: "Write" }));
    expect(screen.getByLabelText("Content")).toHaveValue("A **quiet** village.");
  });

  it("names its two groups by how they are saved", async () => {
    mount();
    const editor = await openEditor("Oakhurst");
    const entry = within(editor).getByRole("region", { name: "Entry" });
    expect(entry).toHaveTextContent("Saved when you press Save");
    expect(within(entry).getByLabelText("Name")).toBeInTheDocument();
    expect(within(entry).getByLabelText("Content")).toBeInTheDocument();

    const linked = within(editor).getByRole("region", { name: "Linked" });
    expect(linked).toHaveTextContent("Each change here saves on its own.");
    expect(await within(linked).findByLabelText("Tags")).toBeInTheDocument();
    expect(within(linked).getByText("Portrait")).toBeInTheDocument();
    expect(within(linked).getByRole("button", { name: "Add connection" })).toBeInTheDocument();
    // What saves itself is not behind the Save button's group.
    expect(within(entry).queryByLabelText("Tags")).not.toBeInTheDocument();
  });
});

describe("KnowledgePanel entry editor: the live row and the guard", () => {
  it("follows the live row instead of the snapshot taken when Edit was clicked (W7)", async () => {
    const { nodes, queryClient } = mount();
    await openEditor("Oakhurst");
    expect(screen.queryByAltText("Portrait of Oakhurst")).not.toBeInTheDocument();

    // A portrait lands on the row (an upload elsewhere, a proposal approval…)
    // and the reads are dropped, exactly as NodePortrait does after applying.
    nodes[0] = create(NodeSchema, { ...nodes[0], hasPortrait: true });
    await act(async () => {
      invalidateKnowledgeReads(queryClient);
    });

    expect(await screen.findByAltText("Portrait of Oakhurst")).toBeInTheDocument();
  });

  it("keeps typed text when the row refetches underneath it (W7)", async () => {
    const { nodes, queryClient } = mount();
    await openEditor("Oakhurst");
    fireEvent.change(nameField(), { target: { value: "Oakhurst (burned)" } });
    nodes[0] = create(NodeSchema, { ...nodes[0], hasPortrait: true });
    await act(async () => {
      invalidateKnowledgeReads(queryClient);
    });
    await screen.findByAltText("Portrait of Oakhurst");
    expect(nameField().value).toBe("Oakhurst (burned)");
  });

  it("does not show one entry's failed save on the next entry opened (W13)", async () => {
    mount({ failUpdateFor: "n1" });
    await openEditor("Oakhurst");
    fireEvent.change(nameField(), { target: { value: "The Citadel" } });
    save();
    expect(await screen.findByRole("alert")).toHaveTextContent("name already taken");

    // Move on to the other entry (discarding the rejected edit).
    fireEvent.click(screen.getByRole("button", { name: "Edit The Citadel" }));
    fireEvent.click(await screen.findByRole("button", { name: "Discard changes" }));

    await waitFor(() => expect(nameField().value).toBe("The Citadel"));
    expect(screen.queryByText(/name already taken/)).not.toBeInTheDocument();
  });

  it("asks before another entry replaces unsaved typing, and keeps it on Cancel (W10)", async () => {
    const { history } = mount();
    await openEditor("Oakhurst");
    fireEvent.change(screen.getByLabelText("Content"), { target: { value: "A village, now ash." } });

    // Opening another entry's page is a move away from the editor too.
    fireEvent.click(screen.getByRole("button", { name: "The Citadel" }));
    const question = await screen.findByRole("alertdialog");
    expect(question).toHaveTextContent("Discard unsaved changes?");
    // Still on Oakhurst while the question is open.
    expect(nameField().value).toBe("Oakhurst");
    expect(history.current()).toEqual({ node: "n1" });

    fireEvent.click(within(question).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
    expect((screen.getByLabelText("Content") as HTMLTextAreaElement).value).toBe("A village, now ash.");

    fireEvent.click(screen.getByRole("button", { name: "The Citadel" }));
    fireEvent.click(await screen.findByRole("button", { name: "Discard changes" }));
    await waitFor(() =>
      expect(within(screen.getByTestId("entry-view")).getByRole("heading", { name: "The Citadel" })).toBeInTheDocument(),
    );
  });

  it("the editor's own Cancel is the discard: it does not ask again", async () => {
    mount();
    await openEditor("Oakhurst");
    fireEvent.change(nameField(), { target: { value: "Oakhurst (burned)" } });
    fireEvent.click(within(screen.getByTestId("entry-editor")).getByRole("button", { name: "Cancel" }));
    // Back on the page, showing what is saved.
    expect(
      within(await screen.findByTestId("entry-view")).getByRole("heading", { name: "Oakhurst" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  });

  it("switches entries without asking when nothing was typed (W10)", async () => {
    mount();
    await openEditor("Oakhurst");
    fireEvent.click(screen.getByRole("button", { name: "Edit The Citadel" }));
    await waitFor(() => expect(nameField().value).toBe("The Citadel"));
    expect(screen.queryByText("Discard unsaved changes?")).not.toBeInTheDocument();
  });

  it("a click on the card of the entry being edited does not throw the edit away", async () => {
    mount();
    await openEditor("Oakhurst");
    fireEvent.change(nameField(), { target: { value: "Oakhurst (burned)" } });
    fireEvent.click(screen.getByRole("button", { name: "Oakhurst" }));
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(nameField().value).toBe("Oakhurst (burned)");
  });
});

describe("KnowledgePanel entry editor: tags", () => {
  it("rolls an optimistic tag back when the server rejects it (W12)", async () => {
    mount({ failTags: true });
    await openEditor("Oakhurst");
    const input = await screen.findByLabelText("Tags");
    expect(await screen.findByRole("button", { name: "Remove act one" })).toBeInTheDocument();

    fireEvent.change(input, { target: { value: "act two" } });
    fireEvent.keyDown(input, { key: "Enter" });

    expect(await screen.findByText(/Couldn't save tags: too many tags/)).toBeInTheDocument();
    // The refused tag is not left on screen looking saved; the real one stays.
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Remove act two" })).not.toBeInTheDocument(),
    );
    expect(screen.getByRole("button", { name: "Remove act one" })).toBeInTheDocument();
  });

  it("takes a comma list in one go and never locks the field while saving", async () => {
    const { tagCalls, release } = mount({ holdTags: true });
    await openEditor("Oakhurst");
    const input = (await screen.findByLabelText("Tags")) as HTMLInputElement;
    await screen.findByRole("button", { name: "Remove act one" });

    fireEvent.paste(input, { clipboardData: { getData: () => "seafaring, Act One, harbor" } });
    await waitFor(() => expect(tagCalls).toHaveLength(1));
    // "Act One" is the tag it already has, in another case: not added twice.
    expect(tagCalls[0]).toEqual(["act one", "seafaring", "harbor"]);

    // The first save is still in flight; the field takes more.
    expect(input).not.toBeDisabled();
    fireEvent.change(input, { target: { value: "rumour" } });
    fireEvent.keyDown(input, { key: "Enter" });
    fireEvent.change(input, { target: { value: "night" } });
    fireEvent.keyDown(input, { key: "Enter" });
    expect(screen.getByRole("button", { name: "Remove night" })).toBeInTheDocument();
    // Nothing overtakes the write in flight…
    expect(tagCalls).toHaveLength(1);

    await release();
    // …and what was added meanwhile goes out as ONE write with the whole list.
    await waitFor(() => expect(tagCalls).toHaveLength(2));
    expect(tagCalls[1]).toEqual(["act one", "seafaring", "harbor", "rumour", "night"]);
    await release();
    await waitFor(() => expect(screen.getByRole("button", { name: "Remove night" })).toBeInTheDocument());
    expect(tagCalls).toHaveLength(2);
  });

  it("offers the campaign's other tags as one-click chips", async () => {
    const { tagCalls } = mount();
    // "act one" is Oakhurst's; open the entry that has no tags yet.
    await openEditor("The Citadel");
    const suggestions = await screen.findByRole("list", { name: "Tags used elsewhere in this campaign" });
    fireEvent.click(within(suggestions).getByRole("button", { name: "Add tag act one" }));
    await waitFor(() => expect(tagCalls).toEqual([["act one"]]));
    expect(await screen.findByRole("button", { name: "Remove act one" })).toBeInTheDocument();
  });
});
