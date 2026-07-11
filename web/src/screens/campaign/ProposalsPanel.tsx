import { useState } from "react";
import { useQuery, useMutation, createConnectQueryKey } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { Check, Sparkles, X } from "lucide-react";

import { CampaignService, EdgeType, NodeType } from "@gen/glyphoxa/management/v1/management_pb";
import type { KnowledgeProposal } from "@gen/glyphoxa/management/v1/management_pb";
import { Card } from "@/components/ui/Card";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import { failedPreconditionMessage } from "@/lib/connectError";

// The Proposals panel (#300, ADR-0052) backs the Campaign screen's "Proposals"
// view: the GM review queue an Agent's remember_knowledge call files into. Each
// card renders the parsed write (fact/edge/node — or "unreadable" when the stored
// payload could not be parsed), a lazy similarity hint of existing entries, and
// Approve / Reject actions. Approve lands the write server-side atomically; a
// refused approval (no such entry, matrix violation, duplicate) surfaces the
// server's actionable reason inline. Reject drops the suggestion. Similarity is a
// HINT the GM acts on — never an auto-merge (ADR-0052).

// Per-relation label, mirroring the kg_edge vocabulary (ADR-0008).
const EDGE_LABEL: Record<number, string> = {
  [EdgeType.RESIDES_IN]: "resides_in",
  [EdgeType.MEMBER_OF]: "member_of",
  [EdgeType.OWNS]: "owns",
  [EdgeType.KNOWS]: "knows",
  [EdgeType.ENEMY_OF]: "enemy_of",
  [EdgeType.ALLY_OF]: "ally_of",
  [EdgeType.PARENT_OF]: "parent_of",
  [EdgeType.PARTICIPATED_IN]: "participated_in",
  [EdgeType.MENTIONED_IN]: "mentioned_in",
};

// Per-type GM label (mirrors KnowledgePanel's TYPE_META labels). The Note label
// doubles as the defensive fallback for an unknown/unspecified type.
const NODE_TYPE_LABEL: Record<number, string> = {
  [NodeType.CHARACTER]: "Character",
  [NodeType.NPC]: "NPC",
  [NodeType.LOCATION]: "Location",
  [NodeType.FACTION]: "Faction",
  [NodeType.ITEM]: "Item",
  [NodeType.PLOT_THREAD]: "Plot thread",
  [NodeType.NOTE]: "Note",
};

function nodeTypeLabel(t: NodeType): string {
  return NODE_TYPE_LABEL[t] ?? "Note";
}

function fmtWhen(p: KnowledgeProposal): string {
  if (!p.createdAt) return "";
  return timestampDate(p.createdAt).toLocaleString(undefined, {
    dateStyle: "medium",
    timeStyle: "short",
  });
}

export function ProposalsPanel() {
  const queryClient = useQueryClient();
  const { data, status, error } = useQuery(CampaignService.method.listKnowledgeProposals, {});
  const proposals = data?.proposals ?? [];

  // A review action must refresh the queue AND the wiki list (an approved write
  // lands a new/edited Node the Knowledge panel shows). Both keys are built
  // without an input so they prefix-match every cached query.
  const invalidateAfterReview = () => {
    void queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: CampaignService.method.listKnowledgeProposals,
        cardinality: "finite",
      }),
    });
    void queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: CampaignService.method.listNodes,
        cardinality: "finite",
      }),
    });
  };

  if (status === "pending") {
    return <div className="gx-skeleton" data-testid="proposals-loading" />;
  }
  if (status === "error") {
    return (
      <p className="gx-campaign__error" role="alert">
        Could not load suggestions: {error.message}
      </p>
    );
  }

  if (proposals.length === 0) {
    return (
      <p className="gx-kg-empty">
        No pending suggestions. Agents with the remember grant will file them here.
      </p>
    );
  }

  return (
    <div className="gx-proposals-list">
      {proposals.map((p) => (
        <ProposalCard key={p.id} proposal={p} onReviewed={invalidateAfterReview} />
      ))}
    </div>
  );
}

// ProposalCard renders one pending proposal: who + when, the kind badge, the
// human-rendered write, a lazy similarity hint, and Approve/Reject. A refused
// approval (FailedPrecondition) shows the server's reason inline so the GM knows
// exactly what to fix; any other failure shows a generic inline error.
function ProposalCard({
  proposal,
  onReviewed,
}: {
  proposal: KnowledgeProposal;
  onReviewed: () => void;
}) {
  const [confirmReject, setConfirmReject] = useState(false);

  const approve = useMutation(CampaignService.method.approveKnowledgeProposal, {
    onSuccess: () => onReviewed(),
  });
  const reject = useMutation(CampaignService.method.rejectKnowledgeProposal, {
    onSuccess: () => onReviewed(),
  });

  // A refused approval carries an actionable server reason; anything else is a
  // generic failure. Reject failures fall into the same inline line so a dead
  // button is never silent.
  const blockedReason = approve.isError ? failedPreconditionMessage(approve.error) : null;
  const inlineError = blockedReason
    ? blockedReason
    : approve.isError
      ? `Couldn't approve: ${approve.error.message}`
      : reject.isError
        ? `Couldn't reject: ${reject.error.message}`
        : null;

  const pending = approve.isPending || reject.isPending;

  return (
    <Card className="gx-proposal-card">
      <div className="gx-proposal-card__head">
        <span className="gx-proposal-card__author">{proposal.authoringAgentName}</span>
        <span className="gx-proposal-card__when">{fmtWhen(proposal)}</span>
        <KindBadge proposal={proposal} />
      </div>

      <div className="gx-proposal-card__write">
        <ProposalWrite proposal={proposal} />
      </div>

      <SimilarHint proposalId={proposal.id} />

      <div className="gx-proposal-card__actions">
        <Button
          variant="primary"
          size="sm"
          iconStart={<Check size={14} />}
          onClick={() => approve.mutate({ id: proposal.id })}
          disabled={pending}
        >
          Approve
        </Button>
        <Button
          variant="danger"
          size="sm"
          iconStart={<X size={14} />}
          onClick={() => setConfirmReject(true)}
          disabled={pending}
        >
          Reject
        </Button>
        {inlineError && (
          <span className="gx-editor__status gx-editor__status--error" role="alert">
            {inlineError}
          </span>
        )}
      </div>

      {confirmReject && (
        <ConfirmDialog
          open
          onOpenChange={(open) => {
            if (!open) setConfirmReject(false);
          }}
          title="Reject this suggestion?"
          description="The suggestion is dropped and never becomes canon. This can't be undone."
          confirmLabel="Reject suggestion"
          onConfirm={() => {
            reject.mutate({ id: proposal.id });
            setConfirmReject(false);
          }}
        />
      )}
    </Card>
  );
}

// KindBadge labels the proposal's kind, or "Unreadable" when the write is unset.
function KindBadge({ proposal }: { proposal: KnowledgeProposal }) {
  const kind = proposal.write.case;
  if (kind === "fact") return <Badge size="sm">Fact</Badge>;
  if (kind === "edge") return <Badge size="sm">Relationship</Badge>;
  if (kind === "node") return <Badge size="sm">New entry</Badge>;
  return (
    <Badge variant="neutral" size="sm">
      Unreadable
    </Badge>
  );
}

// ProposalWrite renders the human form of the proposed write per kind.
function ProposalWrite({ proposal }: { proposal: KnowledgeProposal }) {
  const w = proposal.write;
  switch (w.case) {
    case "fact":
      return (
        <span>
          <strong>{w.value.subject}</strong> — {w.value.fact}
        </span>
      );
    case "edge":
      return (
        <span>
          <strong>{w.value.subject}</strong> —{EDGE_LABEL[w.value.relation] ?? ""}→{" "}
          <strong>{w.value.target}</strong>
        </span>
      );
    case "node":
      return (
        <span>
          New {nodeTypeLabel(w.value.nodeType)}: <strong>{w.value.name}</strong>
          {w.value.body && <span className="gx-proposal-card__body"> — {w.value.body}</span>}
        </span>
      );
    default:
      return <span className="gx-proposal-card__unreadable">Unreadable proposal</span>;
  }
}

// SimilarHint lazily fetches the existing entries most similar to a proposal's
// subject (the ADR-0011 vector hint) so the GM can merge or reject rather than
// duplicate. A skeleton shows while loading; "No similar entries." when none.
function SimilarHint({ proposalId }: { proposalId: string }) {
  const { data, status } = useQuery(CampaignService.method.listSimilarKnowledge, {
    proposalId,
  });

  if (status === "pending") {
    return <div className="gx-skeleton gx-proposal-card__similar-skel" data-testid="similar-loading" />;
  }
  if (status === "error") {
    return null; // the hint is best-effort; a failure is silent (no scary error on a suggestion)
  }

  const nodes = data.nodes;
  return (
    <div className="gx-proposal-card__similar">
      <span className="gx-proposal-card__similar-title">
        <Sparkles size={12} /> Similar existing entries
      </span>
      {nodes.length === 0 ? (
        <span className="gx-proposal-card__similar-empty">No similar entries.</span>
      ) : (
        <ul className="gx-proposal-card__similar-list">
          {nodes.map((n) => (
            <li key={n.id}>
              <span className="gx-proposal-card__similar-name">{n.name}</span>
              <Badge size="sm" variant="neutral">
                {nodeTypeLabel(n.nodeType)}
              </Badge>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
