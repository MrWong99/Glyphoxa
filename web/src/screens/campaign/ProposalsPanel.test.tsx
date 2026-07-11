import { describe, it, expect } from "vitest";
import { render, screen, fireEvent, within, waitFor } from "@testing-library/react";
import { createRouterTransport, ConnectError, Code } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";

import {
  CampaignService,
  NodeType,
  EdgeType,
  NodeSchema,
  KnowledgeProposalSchema,
  ProposedFactSchema,
  ProposedEdgeSchema,
  ProposedNodeSchema,
  ListKnowledgeProposalsResponseSchema,
  ApproveKnowledgeProposalResponseSchema,
  RejectKnowledgeProposalResponseSchema,
  ListSimilarKnowledgeResponseSchema,
  type ApproveKnowledgeProposalRequest,
  type RejectKnowledgeProposalRequest,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { ProposalsPanel } from "./ProposalsPanel";

const factProposal = create(KnowledgeProposalSchema, {
  id: "p-fact",
  authoringAgentId: "a1",
  authoringAgentName: "Innkeeper NPC",
  createdAt: timestampFromDate(new Date("2026-07-11T10:00:00Z")),
  write: { case: "fact", value: create(ProposedFactSchema, { subject: "Bart", fact: "He fears the dark." }) },
});
const edgeProposal = create(KnowledgeProposalSchema, {
  id: "p-edge",
  authoringAgentName: "Glyphoxa",
  createdAt: timestampFromDate(new Date("2026-07-11T10:01:00Z")),
  write: {
    case: "edge",
    value: create(ProposedEdgeSchema, { subject: "Gundren", relation: EdgeType.RESIDES_IN, target: "The Inn" }),
  },
});
const nodeProposal = create(KnowledgeProposalSchema, {
  id: "p-node",
  authoringAgentName: "Glyphoxa",
  createdAt: timestampFromDate(new Date("2026-07-11T10:02:00Z")),
  write: {
    case: "node",
    value: create(ProposedNodeSchema, { nodeType: NodeType.FACTION, name: "Zhentarim", body: "A shadow network." }),
  },
});
// An unparseable row: the server left the write oneof unset.
const unreadableProposal = create(KnowledgeProposalSchema, {
  id: "p-bad",
  authoringAgentName: "Glyphoxa",
  createdAt: timestampFromDate(new Date("2026-07-11T10:03:00Z")),
});

function mockTransport(
  proposals: ReturnType<typeof create<typeof KnowledgeProposalSchema>>[],
  opts: {
    approveError?: ConnectError;
    similar?: ReturnType<typeof create<typeof NodeSchema>>[];
  } = {},
) {
  let list = [...proposals];
  const approveCalls: ApproveKnowledgeProposalRequest[] = [];
  const rejectCalls: RejectKnowledgeProposalRequest[] = [];

  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      listKnowledgeProposals: () => create(ListKnowledgeProposalsResponseSchema, { proposals: list }),
      listSimilarKnowledge: () =>
        create(ListSimilarKnowledgeResponseSchema, { nodes: opts.similar ?? [] }),
      approveKnowledgeProposal: (req) => {
        approveCalls.push(req);
        if (opts.approveError) throw opts.approveError;
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
  return { transport, approveCalls, rejectCalls };
}

function renderPanel(
  proposals: ReturnType<typeof create<typeof KnowledgeProposalSchema>>[],
  opts?: { approveError?: ConnectError; similar?: ReturnType<typeof create<typeof NodeSchema>>[] },
) {
  const ctx = mockTransport(proposals, opts);
  render(
    <Providers transport={ctx.transport} queryClient={makeQueryClient()}>
      <ProposalsPanel />
    </Providers>,
  );
  return ctx;
}

describe("ProposalsPanel", () => {
  it("renders the empty state when there are no pending proposals", async () => {
    renderPanel([]);
    expect(await screen.findByText(/No pending suggestions/i)).toBeInTheDocument();
  });

  it("renders fact, edge and node proposals in human form", async () => {
    renderPanel([factProposal, edgeProposal, nodeProposal]);

    // Fact: Subject — fact, with the authoring agent.
    expect(await screen.findByText("Innkeeper NPC")).toBeInTheDocument();
    expect(screen.getByText("Bart")).toBeInTheDocument();
    expect(screen.getByText(/He fears the dark\./)).toBeInTheDocument();
    // Edge: Subject —relation→ Target.
    expect(screen.getByText(/resides_in/)).toBeInTheDocument();
    expect(screen.getByText("The Inn")).toBeInTheDocument();
    // Node: New Faction: Name + body.
    expect(screen.getByText(/New Faction:/)).toBeInTheDocument();
    expect(screen.getByText("Zhentarim")).toBeInTheDocument();
    expect(screen.getByText(/A shadow network\./)).toBeInTheDocument();
  });

  it("renders an unreadable proposal so the GM can still reject it", async () => {
    renderPanel([unreadableProposal]);
    expect(await screen.findByText(/Unreadable proposal/i)).toBeInTheDocument();
    // The Reject action is still present.
    expect(screen.getByRole("button", { name: /reject/i })).toBeInTheDocument();
  });

  it("approves a proposal, removing it from the queue", async () => {
    const { approveCalls } = renderPanel([factProposal]);
    await screen.findByText("Bart");

    fireEvent.click(screen.getByRole("button", { name: /approve/i }));

    await waitFor(() => expect(screen.queryByText("Bart")).not.toBeInTheDocument());
    expect(approveCalls).toHaveLength(1);
    expect(approveCalls[0].id).toBe("p-fact");
  });

  it("shows the server's reason inline when an approval is refused", async () => {
    renderPanel([factProposal], {
      approveError: new ConnectError(
        'no wiki entry named "Bart" — create it first, then approve; or reject',
        Code.FailedPrecondition,
      ),
    });
    await screen.findByText("Bart");

    fireEvent.click(screen.getByRole("button", { name: /approve/i }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/no wiki entry named "Bart"/);
    // The proposal is NOT removed on a refused approval.
    expect(screen.getByText(/He fears the dark\./)).toBeInTheDocument();
  });

  it("rejects a proposal only after confirming in the dialog", async () => {
    const { rejectCalls } = renderPanel([factProposal]);
    await screen.findByText("Bart");

    fireEvent.click(screen.getByRole("button", { name: /reject/i }));
    // A confirm dialog gates the reject (no RPC yet).
    const dialog = await screen.findByRole("alertdialog");
    expect(rejectCalls).toHaveLength(0);

    fireEvent.click(within(dialog).getByRole("button", { name: /reject suggestion/i }));
    await waitFor(() => expect(screen.queryByText("Bart")).not.toBeInTheDocument());
    expect(rejectCalls).toHaveLength(1);
    expect(rejectCalls[0].id).toBe("p-fact");
  });

  it("renders the similarity hint's existing entries", async () => {
    renderPanel([factProposal], {
      similar: [create(NodeSchema, { id: "n1", nodeType: NodeType.NPC, name: "Bartholomew" })],
    });
    await screen.findByText("Bart");
    expect(await screen.findByText("Bartholomew")).toBeInTheDocument();
    expect(screen.getByText(/Similar existing entries/i)).toBeInTheDocument();
  });

  it("shows 'No similar entries.' when the hint is empty", async () => {
    renderPanel([factProposal], { similar: [] });
    await screen.findByText("Bart");
    expect(await screen.findByText(/No similar entries\./i)).toBeInTheDocument();
  });
});
