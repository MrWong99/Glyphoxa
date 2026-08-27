import { useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import type { KnowledgeProposal } from "@gen/glyphoxa/management/v1/management_pb";
import { useI18n } from "@/i18n";
import { Card } from "@/components/ui/Card";
import { invalidateProposalReview } from "./knowledgeCache";
import { KindBadge, ProposalActions, ProposalWrite, SimilarHint, fmtWhen } from "./proposalParts";
import { errorMessage } from "@/lib/connectError";

// The Proposals panel (#300, ADR-0052) backs the Campaign screen's "Proposals"
// view: the GM review queue an Agent's remember_knowledge call files into. Each
// card renders the parsed write (fact/edge/node — or "unreadable" when the stored
// payload could not be parsed), a lazy similarity hint of existing entries, and
// Approve / Reject actions. Approve lands the write server-side atomically; a
// refused approval (no such entry, matrix violation, duplicate) surfaces the
// server's actionable reason inline. Reject drops the suggestion. Similarity is a
// HINT the GM acts on — never an auto-merge (ADR-0052).
//
// The card's parts live in proposalParts so the graph overlay (#537) reviews the
// same proposal through the same widgets and the same RPCs.

export function ProposalsPanel() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { data, status, error } = useQuery(CampaignService.method.listKnowledgeProposals, {});
  const proposals = data?.proposals ?? [];

  // A review action must refresh the queue AND every read of the KG — an approved
  // write lands a new/edited Node or Edge, so the list, the search, a node's
  // relations and the graph are all stale.
  const invalidateAfterReview = () => invalidateProposalReview(queryClient);

  if (status === "pending") {
    return <div className="gx-skeleton" data-testid="proposals-loading" />;
  }
  if (status === "error") {
    return (
      <p className="gx-campaign__error" role="alert">
        {t("knowledge.loadSuggestionsError", { message: errorMessage(error) })}
      </p>
    );
  }

  if (proposals.length === 0) {
    return <p className="gx-kg-empty">{t("knowledge.noSuggestions")}</p>;
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
// human-rendered write, a lazy similarity hint, and Approve/Reject.
function ProposalCard({
  proposal,
  onReviewed,
}: {
  proposal: KnowledgeProposal;
  onReviewed: () => void;
}) {
  const { lang } = useI18n();
  return (
    <Card className="gx-proposal-card">
      <div className="gx-proposal-card__head">
        <span className="gx-proposal-card__author">{proposal.authoringAgentName}</span>
        <span className="gx-proposal-card__when">{fmtWhen(proposal, lang)}</span>
        <KindBadge proposal={proposal} />
      </div>

      <div className="gx-proposal-card__write">
        <ProposalWrite proposal={proposal} />
      </div>

      <SimilarHint proposalId={proposal.id} />

      <ProposalActions proposalID={proposal.id} onReviewed={onReviewed} />
    </Card>
  );
}
