import { afterEach, describe, it, expect, vi } from "vitest";
import { act, render, screen, cleanup, fireEvent, within, waitFor } from "@testing-library/react";
import { createRouterTransport, ConnectError, Code } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { toast, Toaster } from "sonner";

import {
  CampaignService,
  NodeType,
  EdgeType,
  NodeSchema,
  GetKnowledgeGraphResponseSchema,
  KnowledgeProposalSchema,
  ProposedFactSchema,
  ProposedEdgeSchema,
  ProposedNodeSchema,
  ListKnowledgeProposalsResponseSchema,
  ApproveKnowledgeProposalResponseSchema,
  RejectKnowledgeProposalResponseSchema,
  ListSimilarKnowledgeResponseSchema,
  type ApproveKnowledgeProposalRequest,
  type KnowledgeProposal,
  type RejectKnowledgeProposalRequest,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { flushPendingWrites, hasPendingWrites } from "@/lib/pendingWrites";
import { makeQueryClient } from "@/lib/queryClient";
import { CampaignScreenProvider, type CampaignScreen } from "./campaignContext";
import { ProposalsPanel } from "./ProposalsPanel";

const at = (minute: number) => timestampFromDate(new Date(Date.UTC(2026, 6, 11, 10, minute)));

const factProposal = create(KnowledgeProposalSchema, {
  id: "p-fact",
  authoringAgentId: "a1",
  authoringAgentName: "Innkeeper NPC",
  createdAt: at(0),
  write: { case: "fact", value: create(ProposedFactSchema, { subject: "Bart", fact: "He fears the dark." }) },
});
const edgeProposal = create(KnowledgeProposalSchema, {
  id: "p-edge",
  authoringAgentName: "Glyphoxa",
  createdAt: at(1),
  write: {
    case: "edge",
    value: create(ProposedEdgeSchema, { subject: "Gundren", relation: EdgeType.RESIDES_IN, target: "The Inn" }),
  },
});
const nodeProposal = create(KnowledgeProposalSchema, {
  id: "p-node",
  authoringAgentName: "Glyphoxa",
  createdAt: at(2),
  write: {
    case: "node",
    value: create(ProposedNodeSchema, { nodeType: NodeType.FACTION, name: "Zhentarim", body: "A shadow network." }),
  },
});
// An unparseable row: the server left the write oneof unset.
const unreadableProposal = create(KnowledgeProposalSchema, {
  id: "p-bad",
  authoringAgentName: "Glyphoxa",
  createdAt: at(3),
});

const fact = (id: string, subject: string, text: string, author = "Glyphoxa"): KnowledgeProposal =>
  create(KnowledgeProposalSchema, {
    id,
    authoringAgentName: author,
    createdAt: at(10),
    write: { case: "fact", value: create(ProposedFactSchema, { subject, fact: text }) },
  });

// The wiki the queue is resolved against: Bart, Gundren and The Inn exist.
const WIKI = [
  { id: "n-bart", name: "Bart", nodeType: NodeType.NPC },
  { id: "n-gundren", name: "Gundren", nodeType: NodeType.NPC },
  { id: "n-inn", name: "The Inn", nodeType: NodeType.LOCATION },
];

type Options = {
  /** Approvals that are refused, by proposal id, with the server's reason. */
  refuse?: Record<string, string>;
  similar?: ReturnType<typeof create<typeof NodeSchema>>[];
  wiki?: typeof WIKI;
  /** Calls to GetKnowledgeGraph that fail before it answers. */
  graphFailures?: number;
  /** Every approve waits for this before it answers. */
  hold?: Promise<void>;
  undoWindowMs?: number;
  /** Mount the toast host, to reach Undo. */
  toaster?: boolean;
};

function mockTransport(proposals: KnowledgeProposal[], opts: Options) {
  let list = [...proposals];
  const approveCalls: ApproveKnowledgeProposalRequest[] = [];
  const rejectCalls: RejectKnowledgeProposalRequest[] = [];
  const counters = { similarCalls: 0, listCalls: 0, graphCalls: 0 };

  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      listKnowledgeProposals: () => {
        counters.listCalls++;
        return create(ListKnowledgeProposalsResponseSchema, { proposals: list });
      },
      getKnowledgeGraph: () => {
        counters.graphCalls++;
        if (counters.graphCalls <= (opts.graphFailures ?? 0)) {
          throw new ConnectError("graph is down", Code.Unavailable);
        }
        return create(GetKnowledgeGraphResponseSchema, { nodes: opts.wiki ?? WIKI });
      },
      listSimilarKnowledge: () => {
        counters.similarCalls++;
        return create(ListSimilarKnowledgeResponseSchema, { nodes: opts.similar ?? [] });
      },
      approveKnowledgeProposal: async (req) => {
        approveCalls.push(req);
        if (opts.hold) await opts.hold;
        const reason = opts.refuse?.[req.id];
        if (reason) throw new ConnectError(reason, Code.FailedPrecondition);
        list = list.filter((p) => p.id !== req.id);
        return create(ApproveKnowledgeProposalResponseSchema, {});
      },
      rejectKnowledgeProposal: (req) => {
        rejectCalls.push(req);
        list = list.filter((p) => p.id !== req.id);
        return create(RejectKnowledgeProposalResponseSchema, {});
      },
    });
  });
  return { transport, approveCalls, rejectCalls, counters };
}

function renderPanel(proposals: KnowledgeProposal[], opts: Options = {}) {
  const ctx = mockTransport(proposals, opts);
  const jumps = { openNode: vi.fn(), openAgent: vi.fn(), openView: vi.fn() };
  const host: CampaignScreen = { campaign: undefined, roster: [], ...jumps };
  const queryClient = makeQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false, staleTime: 30_000 } });
  const view = render(
    <Providers transport={ctx.transport} queryClient={queryClient}>
      <CampaignScreenProvider value={host}>
        {/* A long Undo window by default: a test that is not about the window
            must not have a reject fire under it. */}
        <ProposalsPanel undoWindowMs={opts.undoWindowMs ?? 60_000} />
        {opts.toaster && <Toaster />}
      </CampaignScreenProvider>
    </Providers>,
  );
  return { ...ctx, ...jumps, unmount: view.unmount };
}

// The row of the suggestion whose text contains `text`.
const row = (text: string | RegExp) => {
  const el = screen.getByText(text).closest("li");
  if (!el) throw new Error(`no row for ${text}`);
  return el as HTMLLIElement;
};
// A key pressed wherever keyboard focus is (the page itself when nowhere).
const press = (key: string, init: KeyboardEventInit = {}) =>
  fireEvent.keyDown(document.activeElement ?? document.body, { key, ...init });
// The wiki has loaded once a resolved name has become a link.
const resolved = () => screen.findAllByRole("button", { name: /in the World wiki$/ });

afterEach(() => {
  act(() => {
    toast.dismiss();
  });
});

describe("Suggestions inbox: reading", () => {
  it("explains where suggestions come from when there are none, and leads to Planning", async () => {
    const { openView } = renderPanel([]);
    expect(await screen.findByText("No pending suggestions")).toBeInTheDocument();
    expect(screen.getByText(/during a voice session or in Planning chat/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Open Planning" }));
    expect(openView).toHaveBeenCalledWith("planning");
  });

  it("heads the list with the pending count", async () => {
    renderPanel([factProposal, edgeProposal, nodeProposal]);
    expect(await screen.findByRole("heading", { level: 2, name: "3 pending suggestions" })).toBeInTheDocument();
    cleanup();
    renderPanel([factProposal]);
    expect(await screen.findByRole("heading", { level: 2, name: "1 pending suggestion" })).toBeInTheDocument();
  });

  it("renders fact, edge and node proposals in human form", async () => {
    renderPanel([factProposal, edgeProposal, nodeProposal]);

    // Fact: Subject — fact, with the authoring agent.
    await screen.findByText(/He fears the dark\./);
    expect(row(/He fears the dark\./)).toHaveTextContent("Innkeeper NPC");
    expect(row(/He fears the dark\./)).toHaveTextContent("Bart — He fears the dark.");
    // Edge: Subject —relation→ Target.
    expect(row(/lives in/)).toHaveTextContent("Gundren —lives in→ The Inn");
    // Node: New Faction: Name + body.
    expect(row(/A shadow network\./)).toHaveTextContent("New Faction: Zhentarim — A shadow network.");
  });

  it("stamps a suggestion in the display language, not the browser's locale", async () => {
    // Computed from Intl, not spelled out: the runner's timezone is not the
    // author's, so a literal clock would flake. The LOCALE is what is asserted.
    const when = new Date(Date.UTC(2026, 6, 11, 10, 0));
    const fmt = (lang: string) => when.toLocaleString(lang, { dateStyle: "medium", timeStyle: "short" });

    renderPanel([factProposal]);
    expect(await screen.findByText(fmt("en"))).toBeInTheDocument();

    cleanup();
    localStorage.setItem("gx-lang", "de");
    renderPanel([factProposal]);
    expect(await screen.findByText(fmt("de"))).toBeInTheDocument();
    localStorage.clear();
  });

  it("groups suggestions under the entry they are about", async () => {
    renderPanel([
      fact("f1", "Bart", "He fears the dark."),
      edgeProposal,
      fact("f2", "bart", "He owes the Zhentarim."),
      nodeProposal,
      unreadableProposal,
    ]);
    await resolved();

    // "Bart" and "bart" are one entry: both facts sit under it.
    const bart = screen.getByRole("group", { name: "Bart" });
    expect(within(bart).getAllByRole("listitem")).toHaveLength(2);
    expect(within(bart).getByRole("heading", { level: 3 })).toHaveTextContent("Bart2");
    // A connection sits under the entry it starts from.
    expect(within(screen.getByRole("group", { name: "Gundren" })).getAllByRole("listitem")).toHaveLength(1);
    // A suggested entry and an unreadable row have groups of their own.
    expect(screen.getByRole("group", { name: "New entry: Zhentarim" })).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Unreadable suggestions" })).toBeInTheDocument();
  });

  it("opens an entry from its name: the group, the subject and the target", async () => {
    const { openNode } = renderPanel([factProposal, edgeProposal]);
    await resolved();

    // The group heading and the subject inside the card both open Bart.
    const bart = screen.getAllByRole("button", { name: "Open Bart in the World wiki" });
    expect(bart).toHaveLength(2);
    fireEvent.click(bart[1]);
    expect(openNode).toHaveBeenLastCalledWith("n-bart");

    // A connection links both of its ends.
    fireEvent.click(within(row(/lives in/)).getByRole("button", { name: "Open The Inn in the World wiki" }));
    expect(openNode).toHaveBeenLastCalledWith("n-inn");
  });

  it("says before the click why a suggestion will be refused", async () => {
    renderPanel([
      fact("f-unknown", "Ilva", "She has a twin."),
      fact("f-ok", "Bart", "He fears the dark."),
      // A connection into an entry that only exists as another suggestion.
      create(KnowledgeProposalSchema, {
        id: "e-ghost",
        authoringAgentName: "Glyphoxa",
        write: {
          case: "edge",
          value: create(ProposedEdgeSchema, { subject: "Bart", relation: EdgeType.MEMBER_OF, target: "Zhentarim" }),
        },
      }),
      nodeProposal,
    ]);
    await resolved();

    // No entry is called Ilva: the server would refuse, and the card says so.
    expect(row(/She has a twin/)).toHaveTextContent("This can't be added yet: no entry called “Ilva” yet.");
    // A name that does not resolve is text, not a dead link.
    expect(screen.queryByRole("button", { name: "Open Ilva in the World wiki" })).not.toBeInTheDocument();
    // It depends on a suggested entry: add that one first.
    expect(row(/member of/i)).toHaveTextContent("Add the suggested entry “Zhentarim” first.");
    // A suggestion that will land carries no warning.
    expect(row(/He fears the dark/)).not.toHaveTextContent(/can't be added|first/);
  });

  it("refuses to guess while the wiki cannot be read, and still lists everything", async () => {
    const { counters } = renderPanel([fact("f-unknown", "Ilva", "She has a twin."), factProposal], {
      graphFailures: 1,
    });
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Couldn't check the suggestions against the World wiki");

    // Both suggestions are there, grouped by the name they carry…
    expect(screen.getByRole("group", { name: "Ilva" })).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Bart" })).toBeInTheDocument();
    // …with no forecast and no link: either would be a guess.
    expect(screen.queryByText(/can't be added yet/)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /in the World wiki$/ })).not.toBeInTheDocument();

    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByText(/no entry called “Ilva” yet/)).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(counters.graphCalls).toBe(2);
  });

  it("filters by kind and by author", async () => {
    renderPanel([factProposal, edgeProposal, nodeProposal]);
    await screen.findByRole("heading", { level: 2 });

    const kinds = screen.getByRole("group", { name: "Filter by kind" });
    expect(within(kinds).getByRole("button", { name: "All 3" })).toHaveAttribute("aria-pressed", "true");
    fireEvent.click(within(kinds).getByRole("button", { name: "Connections 1" }));
    expect(screen.getAllByRole("listitem")).toHaveLength(1);
    expect(screen.getByText(/lives in/)).toBeInTheDocument();
    // The heading still counts the whole queue.
    expect(screen.getByRole("heading", { level: 2, name: "3 pending suggestions" })).toBeInTheDocument();

    fireEvent.click(within(kinds).getByRole("button", { name: "All 3" }));
    const authors = screen.getByRole("group", { name: "Filter by author" });
    fireEvent.click(within(authors).getByRole("button", { name: "Innkeeper NPC 1" }));
    expect(screen.getAllByRole("listitem")).toHaveLength(1);
    expect(screen.getByText(/He fears the dark/)).toBeInTheDocument();

    // Two filters nothing satisfies: say so and offer the way back.
    fireEvent.click(within(kinds).getByRole("button", { name: "Connections 1" }));
    expect(screen.getByText("No suggestions match these filters")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
    expect(screen.getAllByRole("listitem")).toHaveLength(3);
  });

  it("offers no filter when there is nothing to choose between", async () => {
    renderPanel([factProposal]);
    await screen.findByRole("heading", { level: 2 });
    expect(screen.queryByRole("group", { name: "Filter by kind" })).not.toBeInTheDocument();
    expect(screen.queryByRole("group", { name: "Filter by author" })).not.toBeInTheDocument();
  });
});

describe("Suggestions inbox: one at a time", () => {
  it("adds a suggestion to the wiki, removing it from the queue", async () => {
    const { approveCalls } = renderPanel([factProposal]);
    await screen.findByText(/He fears the dark/);

    fireEvent.click(screen.getByRole("button", { name: "Add to wiki" }));

    await waitFor(() => expect(screen.queryByText(/He fears the dark/)).not.toBeInTheDocument());
    expect(approveCalls.map((c) => c.id)).toEqual(["p-fact"]);
    // The inbox is clear: it says so rather than showing an empty frame.
    expect(await screen.findByText("No pending suggestions")).toBeInTheDocument();
  });

  it("shows the server's reason inline when an approval is refused", async () => {
    renderPanel([factProposal], {
      refuse: { "p-fact": 'no wiki entry named "Bart" — create it first, then approve; or reject' },
    });
    await screen.findByText(/He fears the dark/);

    fireEvent.click(screen.getByRole("button", { name: "Add to wiki" }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/no wiki entry named "Bart"/);
    // The proposal is NOT removed on a refused approval.
    expect(screen.getByText(/He fears the dark\./)).toBeInTheDocument();
  });

  it("dismisses with one click and no dialog, and sends it when the Undo window closes", async () => {
    const { rejectCalls } = renderPanel([factProposal, edgeProposal], { undoWindowMs: 30 });
    await screen.findByText(/He fears the dark/);

    fireEvent.click(within(row(/He fears the dark/)).getByRole("button", { name: "Dismiss" }));
    // Gone at once, with nothing to confirm…
    expect(screen.queryByText(/He fears the dark/)).not.toBeInTheDocument();
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 2, name: "1 pending suggestion" })).toBeInTheDocument();
    // …and not yet sent: the window is the GM's chance to take it back.
    expect(rejectCalls).toHaveLength(0);

    await waitFor(() => expect(rejectCalls.map((c) => c.id)).toEqual(["p-fact"]));
  });

  it("brings a dismissed suggestion back on Undo, and never sends it", async () => {
    const { rejectCalls, unmount } = renderPanel([factProposal], { toaster: true });
    await screen.findByText(/He fears the dark/);

    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(screen.queryByText(/He fears the dark/)).not.toBeInTheDocument();
    // The toast names what was dismissed.
    expect(await screen.findByText("Suggestion dismissed")).toBeInTheDocument();
    expect(screen.getByText("Bart: He fears the dark.")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Undo" }));
    expect(await screen.findByRole("heading", { level: 2, name: "1 pending suggestion" })).toBeInTheDocument();
    const list = screen.getByRole("group", { name: "Pending suggestions" });
    expect(within(list).getByText(/He fears the dark/)).toBeInTheDocument();

    // Leaving the view sends whatever is still waiting; after Undo that is
    // nothing.
    unmount();
    await new Promise((r) => setTimeout(r, 30));
    expect(rejectCalls).toHaveLength(0);
  });

  it("sends a waiting dismissal when the GM leaves the view", async () => {
    const { rejectCalls, unmount } = renderPanel([factProposal]);
    await screen.findByText(/He fears the dark/);
    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(rejectCalls).toHaveLength(0);

    unmount();
    await waitFor(() => expect(rejectCalls.map((c) => c.id)).toEqual(["p-fact"]));
  });

  it("tries to send a waiting dismissal when the page goes away", async () => {
    const { rejectCalls } = renderPanel([factProposal]);
    await screen.findByText(/He fears the dark/);
    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(rejectCalls).toHaveLength(0);

    // A reload or a closed tab: best effort, but not dropped without trying.
    act(() => {
      window.dispatchEvent(new Event("pagehide"));
    });
    await waitFor(() => expect(rejectCalls.map((c) => c.id)).toEqual(["p-fact"]));
  });

  it("holds a waiting dismissal for a campaign switch to send first, and only once", async () => {
    const { rejectCalls, unmount } = renderPanel([factProposal]);
    await screen.findByText(/He fears the dark/);
    expect(hasPendingWrites()).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(hasPendingWrites()).toBe(true);

    await act(async () => {
      await flushPendingWrites();
    });
    expect(rejectCalls.map((c) => c.id)).toEqual(["p-fact"]);
    expect(hasPendingWrites()).toBe(false);
    // Leaving the view afterwards sends nothing more.
    unmount();
    await new Promise((r) => setTimeout(r, 20));
    expect(rejectCalls).toHaveLength(1);
  });

  it("releases the hold on Undo", async () => {
    renderPanel([factProposal], { toaster: true });
    await screen.findByText(/He fears the dark/);
    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    fireEvent.click(await screen.findByRole("button", { name: "Undo" }));
    expect(hasPendingWrites()).toBe(false);
  });

  it("renders an unreadable proposal so the GM can still dismiss it", async () => {
    const { rejectCalls } = renderPanel([unreadableProposal], { undoWindowMs: 10 });
    expect(await screen.findByText("Unreadable suggestion")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    await waitFor(() => expect(rejectCalls.map((c) => c.id)).toEqual(["p-bad"]));
  });

  it("only fetches the similarity hint after the GM opts in (lazy, no fan-out)", async () => {
    const { counters, openNode } = renderPanel([factProposal], {
      similar: [create(NodeSchema, { id: "n1", nodeType: NodeType.NPC, name: "Bartholomew" })],
    });
    await screen.findByText(/He fears the dark/);
    // Not fired on mount — the RPC embeds a provider call, so it must be opt-in.
    expect(counters.similarCalls).toBe(0);
    expect(screen.queryByText("Bartholomew")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /show similar/i }));
    expect(await screen.findByText("Bartholomew")).toBeInTheDocument();
    expect(screen.getByText(/Similar existing entries/i)).toBeInTheDocument();
    expect(counters.similarCalls).toBe(1);

    // A similar entry opens, so the GM can compare before deciding.
    fireEvent.click(screen.getByRole("button", { name: "Open Bartholomew in the World wiki" }));
    expect(openNode).toHaveBeenCalledWith("n1");
  });

  it("shows 'No similar entries.' when the hint is empty", async () => {
    renderPanel([factProposal], { similar: [] });
    await screen.findByText(/He fears the dark/);
    fireEvent.click(screen.getByRole("button", { name: /show similar/i }));
    expect(await screen.findByText(/No similar entries\./i)).toBeInTheDocument();
  });
});

describe("Suggestions inbox: batches", () => {
  const four = [
    fact("f1", "Bart", "He fears the dark."),
    fact("f2", "Ilva", "She has a twin."),
    fact("f3", "Gundren", "He owns the mine."),
    nodeProposal,
  ];

  it("adds the selection in turn behind one confirm: progress, failures by name, one refetch", async () => {
    let release: () => void = () => {};
    const hold = new Promise<void>((resolve) => {
      release = resolve;
    });
    const { approveCalls, counters, openView } = renderPanel(four, {
      hold,
      refuse: { f2: 'no wiki entry named "Ilva" — create it first, then approve; or reject' },
    });
    await resolved();
    expect(counters.listCalls).toBe(1);

    // Nothing is selected: the batch buttons wait.
    expect(screen.getByRole("button", { name: "Add selected" })).toBeDisabled();
    fireEvent.click(screen.getByRole("checkbox", { name: "Select all 4 shown" }));
    expect(screen.getByText("4 selected")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Add selected" }));
    // One confirm for the lot, and nothing sent before it.
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("Add 4 suggestions to the wiki?");
    expect(approveCalls).toHaveLength(0);
    fireEvent.click(within(dialog).getByRole("button", { name: "Add to wiki" }));

    // Progress is on screen while the calls run, one at a time.
    expect(await screen.findByText("Adding 0 of 4…")).toBeInTheDocument();
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuemax", "4");
    await waitFor(() => expect(approveCalls).toHaveLength(1));
    release();

    // Four calls. The suggested entry goes first, so what depends on it can land.
    await waitFor(() => expect(approveCalls.map((c) => c.id)).toEqual(["p-node", "f1", "f2", "f3"]));

    // The one that failed is named, with the server's reason…
    const summary = (await screen.findByText("1 suggestion couldn't be added")).closest('[role="alert"]');
    expect(summary).toHaveTextContent('Ilva: no wiki entry named "Ilva"');
    // …and it is the only one left, still selected for another try.
    await waitFor(() => expect(screen.getAllByRole("listitem").filter((li) => li.closest(".gx-inbox__list"))).toHaveLength(1));
    expect(screen.getByText(/She has a twin/)).toBeInTheDocument();
    expect(screen.queryByText(/^Adding/)).not.toBeInTheDocument();

    // One refetch for the whole batch, not one per suggestion.
    await waitFor(() => expect(counters.listCalls).toBe(2));
    await new Promise((r) => setTimeout(r, 30));
    expect(counters.listCalls).toBe(2);

    // What could not be added is fixed in the wiki: the summary leads there.
    fireEvent.click(within(summary as HTMLElement).getByRole("button", { name: "Open World wiki" }));
    expect(openView).toHaveBeenCalledWith("knowledge");
  });

  it("dismisses the selection behind a confirm, and only what is ticked", async () => {
    const { rejectCalls, counters } = renderPanel(four);
    await resolved();

    fireEvent.click(within(row(/He fears the dark/)).getByRole("checkbox"));
    fireEvent.click(within(row(/He owns the mine/)).getByRole("checkbox"));
    expect(screen.getByText("2 selected")).toBeInTheDocument();
    // Some, not all: the select-all box says so.
    expect(screen.getByRole("checkbox", { name: "Select all 4 shown" })).toHaveProperty("indeterminate", true);

    fireEvent.click(screen.getByRole("button", { name: "Dismiss selected" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("Dismiss 2 suggestions?");
    // Cancel sends nothing.
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
    expect(rejectCalls).toHaveLength(0);

    fireEvent.click(screen.getByRole("button", { name: "Dismiss selected" }));
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Dismiss" }));
    await waitFor(() => expect(rejectCalls.map((c) => c.id)).toEqual(["f1", "f3"]));
    await waitFor(() => expect(screen.queryByText(/He fears the dark/)).not.toBeInTheDocument());
    expect(screen.getByText(/She has a twin/)).toBeInTheDocument();
    await waitFor(() => expect(counters.listCalls).toBe(2));
  });

  it("leaves a suggestion whose own Add is still on its way out of the batch", async () => {
    let release: () => void = () => {};
    const hold = new Promise<void>((resolve) => {
      release = resolve;
    });
    const { approveCalls } = renderPanel(four, { hold });
    await resolved();

    fireEvent.click(within(row(/He fears the dark/)).getByRole("checkbox"));
    fireEvent.click(within(row(/He owns the mine/)).getByRole("checkbox"));
    // The first one's own Add is sent, and has not answered yet.
    fireEvent.click(within(row(/He fears the dark/)).getByRole("button", { name: "Add to wiki" }));
    await waitFor(() => expect(approveCalls.map((c) => c.id)).toEqual(["f1"]));
    expect(screen.getByText("1 selected")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Add selected" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("Add 1 suggestion to the wiki?");
    fireEvent.click(within(dialog).getByRole("button", { name: "Add to wiki" }));
    release();
    // f1 is sent once, not again by the batch.
    await waitFor(() => expect(approveCalls.map((c) => c.id)).toEqual(["f1", "f3"]));
    await waitFor(() => expect(screen.queryByText(/He owns the mine/)).not.toBeInTheDocument());
    expect(screen.queryByText(/couldn't be added/)).not.toBeInTheDocument();
  });

  it("puts keyboard focus on the inbox heading when a batch ends, not on the page", async () => {
    renderPanel(four, {
      refuse: { f2: 'no wiki entry named "Ilva" — create it first, then approve; or reject' },
    });
    await resolved();
    fireEvent.click(screen.getByRole("checkbox", { name: "Select all 4 shown" }));
    const start = screen.getByRole("button", { name: "Add selected" });
    act(() => start.focus());
    fireEvent.click(start);
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Add to wiki" }));

    await screen.findByText("1 suggestion couldn't be added");
    await waitFor(() => expect(screen.getByRole("heading", { level: 2, name: "1 pending suggestion" })).toHaveFocus());
  });

  it("puts keyboard focus on the empty state's action when a batch clears the queue", async () => {
    renderPanel(four);
    await resolved();
    fireEvent.click(screen.getByRole("checkbox", { name: "Select all 4 shown" }));
    const start = screen.getByRole("button", { name: "Dismiss selected" });
    act(() => start.focus());
    fireEvent.click(start);
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Dismiss" }));

    expect(await screen.findByText("No pending suggestions")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: "Open Planning" })).toHaveFocus());
  });

  it("acts only on what the filter shows", async () => {
    const { approveCalls } = renderPanel(four);
    await resolved();

    fireEvent.click(screen.getByRole("checkbox", { name: "Select all 4 shown" }));
    // Narrow to the facts: the suggested entry is ticked but off screen.
    fireEvent.click(within(screen.getByRole("group", { name: "Filter by kind" })).getByRole("button", { name: "Facts 3" }));
    expect(screen.getByText("3 selected")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Add selected" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("Add 3 suggestions to the wiki?");
    fireEvent.click(within(dialog).getByRole("button", { name: "Add to wiki" }));
    await waitFor(() => expect(approveCalls.map((c) => c.id)).toEqual(["f1", "f2", "f3"]));
  });
});

describe("Suggestions inbox: keyboard", () => {
  const three = [
    fact("f1", "Bart", "He fears the dark."),
    fact("f2", "Gundren", "He owns the mine."),
    fact("f3", "The Inn", "It has a cellar."),
  ];

  it("J and K move through the list, A adds and X dismisses the row they are on", async () => {
    const { approveCalls, rejectCalls } = renderPanel(three, { undoWindowMs: 20 });
    await resolved();

    // Tab enters the list on its first row: the one tab stop among the rows.
    expect(row(/He fears the dark/)).toHaveAttribute("tabindex", "0");
    expect(row(/He owns the mine/)).toHaveAttribute("tabindex", "-1");
    act(() => row(/He fears the dark/).focus());
    expect(row(/He fears the dark/)).toHaveAttribute("aria-current", "true");

    // J moves down, and keyboard focus goes with the cursor.
    press("j");
    expect(row(/He owns the mine/)).toHaveAttribute("aria-current", "true");
    expect(row(/He owns the mine/)).toHaveFocus();
    // The tab stop follows, so Tab from here reaches THIS row's buttons.
    expect(row(/He owns the mine/)).toHaveAttribute("tabindex", "0");
    expect(row(/He fears the dark/)).toHaveAttribute("tabindex", "-1");
    press("j");
    press("j"); // the end holds
    expect(row(/It has a cellar/)).toHaveAttribute("aria-current", "true");
    press("k");
    expect(row(/He owns the mine/)).toHaveFocus();

    // A adds the row the cursor is on; the cursor moves to the next one.
    press("a");
    await waitFor(() => expect(approveCalls.map((c) => c.id)).toEqual(["f2"]));
    await waitFor(() => expect(screen.queryByText(/He owns the mine/)).not.toBeInTheDocument());
    await waitFor(() => expect(row(/It has a cellar/)).toHaveFocus());

    // X dismisses it; at the end of the list the cursor steps back.
    press("x");
    expect(screen.queryByText(/It has a cellar/)).not.toBeInTheDocument();
    await waitFor(() => expect(row(/He fears the dark/)).toHaveFocus());
    await waitFor(() => expect(rejectCalls.map((c) => c.id)).toEqual(["f3"]));
  });

  it("names the shortcuts, and marks them on the current row's buttons only", async () => {
    renderPanel(three);
    await resolved();
    expect(screen.getByText(/J and K move, A adds to the wiki, X dismisses/)).toBeInTheDocument();
    act(() => row(/He fears the dark/).focus());
    const current = within(row(/He fears the dark/));
    expect(current.getByRole("button", { name: "Add to wiki" })).toHaveAttribute("aria-keyshortcuts", "A");
    expect(current.getByRole("button", { name: "Dismiss" })).toHaveAttribute("aria-keyshortcuts", "X");
    expect(within(row(/He owns the mine/)).getByRole("button", { name: "Add to wiki" })).not.toHaveAttribute(
      "aria-keyshortcuts",
    );
  });

  it("A and X do nothing while focus is on the bulk bar, outside every row", async () => {
    const { approveCalls, rejectCalls } = renderPanel(three, { undoWindowMs: 10 });
    await resolved();
    // A row was current, then the GM tabbed on to the select-all box.
    act(() => row(/He fears the dark/).focus());
    act(() => screen.getByRole("checkbox", { name: "Select all 3 shown" }).focus());

    press("x");
    press("a");
    await new Promise((r) => setTimeout(r, 30));
    expect(rejectCalls).toHaveLength(0);
    expect(approveCalls).toHaveLength(0);
    expect(screen.getByText(/He fears the dark/)).toBeInTheDocument();
    // J and K still work inbox-wide.
    press("j");
    expect(row(/He owns the mine/)).toHaveFocus();
  });

  it("puts keyboard focus on the empty state's action when the last row leaves", async () => {
    renderPanel([factProposal]);
    await resolved();
    act(() => row(/He fears the dark/).focus());
    press("x");
    expect(await screen.findByText("No pending suggestions")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByRole("button", { name: "Open Planning" })).toHaveFocus());
  });

  it("names a row by what it suggests", async () => {
    renderPanel(three);
    await resolved();
    expect(row(/He fears the dark/)).toHaveAccessibleName("Bart: He fears the dark.");
  });

  it("works from a control inside a row, too", async () => {
    const { approveCalls } = renderPanel(three);
    await resolved();
    // Focus on a row's checkbox makes that row the current one.
    act(() => within(row(/He owns the mine/)).getByRole("checkbox").focus());
    expect(row(/He owns the mine/)).toHaveAttribute("aria-current", "true");
    press("a");
    await waitFor(() => expect(approveCalls.map((c) => c.id)).toEqual(["f2"]));
  });

  // Single-character shortcuts must not fire from anywhere on the page
  // (WCAG 2.1.4): they belong to the list, and only while focus is in it.
  it("ignores the keys while keyboard focus is outside the inbox", async () => {
    const { approveCalls, rejectCalls } = renderPanel(three, { undoWindowMs: 10 });
    await resolved();
    // A row is current (the GM clicked into it), then focus leaves the inbox.
    act(() => row(/He fears the dark/).focus());
    act(() => (document.activeElement as HTMLElement).blur());
    expect(document.body).toHaveFocus();

    press("a");
    press("x");
    press("j");
    await new Promise((r) => setTimeout(r, 40));
    expect(approveCalls).toHaveLength(0);
    expect(rejectCalls).toHaveLength(0);
    expect(row(/He fears the dark/)).toHaveAttribute("aria-current", "true");
  });

  it("leaves the keys alone while a modifier is held or the confirm dialog is open", async () => {
    const { approveCalls } = renderPanel(three);
    await resolved();
    act(() => row(/He fears the dark/).focus());

    // Ctrl+A is the browser's select-all, not "add".
    press("a", { ctrlKey: true });
    expect(approveCalls).toHaveLength(0);

    fireEvent.click(within(row(/He fears the dark/)).getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: "Add selected" }));
    const dialog = await screen.findByRole("alertdialog");
    // A key pressed in the dialog is not a key pressed in the list, even
    // though React bubbles it through the portal.
    fireEvent.keyDown(within(dialog).getByRole("button", { name: "Cancel" }), { key: "a" });
    fireEvent.keyDown(within(dialog).getByRole("button", { name: "Cancel" }), { key: "x" });
    expect(approveCalls).toHaveLength(0);
    expect(screen.getByText(/He fears the dark/)).toBeInTheDocument();
  });
});
