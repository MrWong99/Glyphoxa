import { useState } from "react";
import { useMutation } from "@connectrpc/connect-query";
import { useQuery } from "@connectrpc/connect-query";
import { timestampDate } from "@bufbuild/protobuf/wkt";
import { Check, Sparkles, X } from "lucide-react";

import { CampaignService, NodeType } from "@gen/glyphoxa/management/v1/management_pb";
import type { KnowledgeProposal } from "@gen/glyphoxa/management/v1/management_pb";
import { useI18n, type Lang, type TFunc } from "@/i18n";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import { failedPreconditionMessage } from "@/lib/connectError";
import { DISPOSITION_LABEL, edgeLabel, metaOf } from "./knowledgeVocab";

// The pieces of a Knowledge Proposal review — a "suggestion" in the GM-facing
// copy — shared by the queue (ProposalsPanel) and the graph overlay (#537).
//
// They live here rather than in the panel because the graph reviews the SAME
// proposal through the SAME RPCs — ADR-0052's rule is that nothing enters the KG
// without GM approval, and two review surfaces disagreeing about what a proposal
// says, or one of them quietly dropping the similarity hint, would be exactly the
// kind of drift that rule exists to prevent.

/**
 * The proposal's timestamp, in the DISPLAY language's locale — the same language
 * the rest of the review card speaks, rather than whatever the browser is set to.
 * lang is threaded from the calling component; this module-level helper holds no
 * translation of its own.
 */
export function fmtWhen(p: KnowledgeProposal, lang: Lang): string {
  if (!p.createdAt) return "";
  return timestampDate(p.createdAt).toLocaleString(lang, {
    dateStyle: "medium",
    timeStyle: "short",
  });
}

function nodeTypeLabel(t: TFunc, nt: NodeType): string {
  return t(metaOf(nt).labelKey);
}

/** KindBadge labels the proposal's kind, or "Unreadable" when the write is unset. */
export function KindBadge({ proposal }: { proposal: KnowledgeProposal }) {
  const { t } = useI18n();
  const kind = proposal.write.case;
  if (kind === "fact") return <Badge size="sm">{t("knowledge.kindFact")}</Badge>;
  if (kind === "edge") return <Badge size="sm">{t("knowledge.kindConnection")}</Badge>;
  if (kind === "node") return <Badge size="sm">{t("knowledge.kindNewEntry")}</Badge>;
  return (
    <Badge variant="neutral" size="sm">
      {t("knowledge.kindUnreadable")}
    </Badge>
  );
}

/** ProposalWrite renders the human form of the proposed write per kind. */
export function ProposalWrite({ proposal }: { proposal: KnowledgeProposal }) {
  const { t } = useI18n();
  const w = proposal.write;
  switch (w.case) {
    case "fact":
      // The aspect label is shown because approving files the fact UNDER it as a
      // row on the entry (#542) — the GM is deciding where it lands, not just
      // whether the sentence is true.
      return (
        <span>
          <strong>{w.value.subject}</strong> —{" "}
          {w.value.aspectKey && <em>{w.value.aspectKey}: </em>}
          {w.value.fact}
        </span>
      );
    case "edge":
      // The note and disposition are rendered because APPROVING WRITES THEM, and
      // the note reaches an NPC's system prompt through the relation clause. A card
      // that showed only "subject —knows→ target" asked the GM to approve up to 280
      // runes of model-authored text they had never seen — approval of unseen
      // content, which is the one thing the review queue exists to prevent (#546).
      return (
        <span>
          <strong>{w.value.subject}</strong> —{edgeLabel(t, w.value.relation)}→{" "}
          <strong>{w.value.target}</strong>
          {(w.value.note || w.value.disposition !== 0) && (
            <span className="gx-proposal-card__body">
              {" — "}
              {(() => {
                const key = DISPOSITION_LABEL.get(w.value.disposition);
                return key ? t(key) : "";
              })()}
              {w.value.note && w.value.disposition !== 0 ? ", " : ""}
              {/* Newlines are collapsed: a proposal is one clause, and a note that
                  can open a new line can forge a section header in the prompt. */}
              {w.value.note.replace(/\s+/g, " ")}
            </span>
          )}
        </span>
      );
    case "node":
      return (
        <span>
          {t("knowledge.proposalNewNode", { type: nodeTypeLabel(t, w.value.nodeType) })}{" "}
          <strong>{w.value.name}</strong>
          {w.value.body && <span className="gx-proposal-card__body"> — {w.value.body}</span>}
        </span>
      );
    default:
      return <span className="gx-proposal-card__unreadable">{t("knowledge.unreadableSuggestion")}</span>;
  }
}

/**
 * SimilarHint lazily fetches the existing entries most similar to a proposal's
 * subject (the ADR-0011 vector hint) so the GM can merge or reject rather than
 * duplicate. A skeleton shows while loading; "No similar entries." when none.
 */
export function SimilarHint({ proposalId }: { proposalId: string }) {
  const { t } = useI18n();
  // Truly lazy: the similarity RPC embeds the subject (a provider call), so it
  // fires ONLY after the GM opts in — never on mount for every card (that would
  // fan N concurrent Embed calls across the whole queue).
  const [show, setShow] = useState(false);
  const { data, status } = useQuery(
    CampaignService.method.listSimilarKnowledge,
    { proposalId },
    { enabled: show },
  );

  if (!show) {
    return (
      <button type="button" className="gx-proposal-card__similar-btn" onClick={() => setShow(true)}>
        <Sparkles size={12} /> {t("knowledge.showSimilar")}
      </button>
    );
  }
  if (status === "pending") {
    return (
      <div className="gx-skeleton gx-proposal-card__similar-skel" data-testid="similar-loading" />
    );
  }
  if (status === "error") {
    return null; // the hint is best-effort; a failure is silent (no scary error on a suggestion)
  }

  const nodes = data.nodes;
  return (
    <div className="gx-proposal-card__similar">
      <span className="gx-proposal-card__similar-title">
        <Sparkles size={12} /> {t("knowledge.similarTitle")}
      </span>
      {nodes.length === 0 ? (
        <span className="gx-proposal-card__similar-empty">{t("knowledge.similarEmpty")}</span>
      ) : (
        <ul className="gx-proposal-card__similar-list">
          {nodes.map((n) => (
            <li key={n.id}>
              <span className="gx-proposal-card__similar-name">{n.name}</span>
              <Badge size="sm" variant="neutral">
                {nodeTypeLabel(t, n.nodeType)}
              </Badge>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/**
 * ProposalActions is the Add-to-wiki / Dismiss pair, with the dismiss confirmation
 * and the inline failure line.
 *
 * `onApproved` fires BEFORE the caches drop, and receives nothing but the fact that
 * this proposal landed — the graph uses that moment to remember where the ghost was
 * standing (#537), which is only knowable on the pre-refetch picture.
 */
export function ProposalActions({
  proposalID,
  size = "sm",
  onApproved,
  onReviewed,
}: {
  proposalID: string;
  size?: "sm" | "md";
  onApproved?: () => void;
  onReviewed: () => void;
}) {
  const { t } = useI18n();
  const [confirmReject, setConfirmReject] = useState(false);

  const approve = useMutation(CampaignService.method.approveKnowledgeProposal, {
    onSuccess: () => {
      onApproved?.();
      onReviewed();
    },
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
      ? t("knowledge.addToWikiError", { message: approve.error.message })
      : reject.isError
        ? t("knowledge.dismissError", { message: reject.error.message })
        : null;

  const pending = approve.isPending || reject.isPending;

  return (
    <div className="gx-proposal-card__actions">
      <Button
        variant="primary"
        size={size}
        iconStart={<Check size={14} />}
        onClick={() => approve.mutate({ id: proposalID })}
        disabled={pending}
      >
        {t("knowledge.addToWiki")}
      </Button>
      <Button
        variant="danger"
        size={size}
        iconStart={<X size={14} />}
        onClick={() => setConfirmReject(true)}
        disabled={pending}
      >
        {t("knowledge.dismiss")}
      </Button>
      {inlineError && (
        <span className="gx-editor__status gx-editor__status--error" role="alert">
          {inlineError}
        </span>
      )}

      {confirmReject && (
        <ConfirmDialog
          open
          onOpenChange={(open) => {
            if (!open) setConfirmReject(false);
          }}
          title={t("knowledge.dismissTitle")}
          description={t("knowledge.dismissBody")}
          confirmLabel={t("knowledge.dismissConfirm")}
          onConfirm={() => {
            reject.mutate({ id: proposalID });
            setConfirmReject(false);
          }}
        />
      )}
    </div>
  );
}
