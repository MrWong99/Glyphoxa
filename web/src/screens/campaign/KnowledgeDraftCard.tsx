import { useState } from "react";
import { useMutation } from "@connectrpc/connect-query";
import { toast } from "sonner";
import { EyeOff, Plus, Sparkles, X } from "lucide-react";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import type { DraftEdge, DraftNode } from "@gen/glyphoxa/management/v1/management_pb";
import { apiErrorMessage, errorMessage } from "@/lib/connectError";
import { useI18n } from "@/i18n";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { IconButton } from "@/components/ui/IconButton";
import { stripMarkdown } from "@/components/ui/Markdown";
import { Textarea } from "@/components/ui/Textarea";
import { alphaBg, edgeLabel, metaOf } from "./knowledgeVocab";

// DraftState is the generator's persistent state, owned by the wiki panel so it
// survives a mode switch (#534): an unapplied draft is the product of a paid
// LLM call, and a view toggle must not silently discard it.
export type DraftState = {
  open: boolean;
  prompt: string;
  draft: { nodes: DraftNode[]; edges: DraftEdge[] } | null;
};

export const EMPTY_DRAFT: DraftState = { open: false, prompt: "", draft: null };

// KnowledgeDraftCard is the on-demand "generate entries" flow (#479). Strictly
// button-driven: it is opened from the wiki's New entry menu (or the empty
// wiki's "generate a starting world"), the GM writes a prompt and presses
// "Generate draft", and only then does an LLM call run. The result is a PREVIEW
// (nothing written): the GM can drop individual entries or relations, then
// lands the rest atomically via ApplyGeneratedKnowledge, or discards the draft.
export function KnowledgeDraftCard({
  state,
  onStateChange,
  onApplied,
}: {
  state: DraftState;
  onStateChange: (s: DraftState) => void;
  onApplied: () => void;
}) {
  const { t } = useI18n();
  const { prompt, draft } = state;
  const setPrompt = (v: string) => onStateChange({ ...state, prompt: v });
  const setDraft = (next: DraftState["draft"]) => onStateChange({ ...state, draft: next });
  // The error is genuinely transient: it describes the last attempt, not the
  // draft, so it stays local and dies with the card.
  const [error, setError] = useState<string | null>(null);

  const generate = useMutation(CampaignService.method.generateKnowledge);
  const apply = useMutation(CampaignService.method.applyGeneratedKnowledge);

  const runGenerate = async () => {
    setError(null);
    try {
      const res = await generate.mutateAsync({ prompt });
      setDraft({ nodes: res.nodes, edges: res.edges });
    } catch (err) {
      setError(apiErrorMessage(err, t));
    }
  };

  const removeNode = (idx: number) => {
    if (!draft) return;
    const nodes = draft.nodes.filter((_, i) => i !== idx);
    // Drop edges touching the removed entry; shift indices past it down.
    const edges = draft.edges
      .filter((e) => e.fromIndex !== idx && e.toIndex !== idx)
      .map((e) => ({
        ...e,
        fromIndex: e.fromIndex > idx ? e.fromIndex - 1 : e.fromIndex,
        toIndex: e.toIndex > idx ? e.toIndex - 1 : e.toIndex,
      })) as DraftEdge[];
    setDraft({ nodes, edges });
  };
  const removeEdge = (idx: number) => {
    if (draft) setDraft({ ...draft, edges: draft.edges.filter((_, i) => i !== idx) });
  };

  const close = () => {
    onStateChange(EMPTY_DRAFT);
    setError(null);
  };

  const runApply = async () => {
    if (!draft || draft.nodes.length === 0) return;
    setError(null);
    try {
      const res = await apply.mutateAsync({ nodes: draft.nodes, edges: draft.edges });
      onApplied();
      onStateChange(EMPTY_DRAFT);
      toast.success(
        (res.nodes.length === 1
          ? t("knowledge.draftAddedOne")
          : t("knowledge.draftAddedMany", { n: res.nodes.length })) +
          (res.edgesCreated > 0
            ? res.edgesCreated === 1
              ? t("knowledge.draftAddedConnectionsOne")
              : t("knowledge.draftAddedConnectionsMany", { n: res.edgesCreated })
            : ""),
      );
    } catch (err) {
      setError(errorMessage(err));
    }
  };

  return (
    <Card accent className="gx-kg-draft">
      <div className="gx-kg-editor__bar">
        <h2 className="gx-kg-editor__title">
          <Sparkles size={15} aria-hidden /> {t("knowledge.draftTitle")}
        </h2>
        <IconButton size="sm" label={t("knowledge.draftCloseAria")} onClick={close}>
          <X size={15} />
        </IconButton>
      </div>

      {draft === null ? (
        <>
          <Textarea
            label={t("knowledge.draftPromptLabel")}
            hint={t("knowledge.draftPromptHint")}
            autoGrow
            rows={3}
            maxRows={10}
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
            placeholder={t("knowledge.draftPromptPlaceholder")}
            // The card opens because the GM asked for it; the prompt is the
            // one thing to do in it.
            autoFocus
          />
          <div className="gx-kg-editor__actions">
            <Button
              variant="primary"
              iconStart={<Sparkles size={14} />}
              onClick={() => void runGenerate()}
              disabled={!prompt.trim() || generate.isPending}
            >
              {generate.isPending ? t("knowledge.draftGenerating") : t("knowledge.draftGenerate")}
            </Button>
            {error && (
              <span className="gx-editor__status gx-editor__status--error" role="alert">
                {t("knowledge.draftGenerateError", { message: error })}
              </span>
            )}
          </div>
        </>
      ) : (
        <>
          <span className="gx-field__hint">{t("knowledge.draftReviewHint")}</span>
          <ul className="gx-kg-draft__nodes">
            {draft.nodes.map((n, i) => {
              const meta = metaOf(n.nodeType);
              return (
                <li key={i} className="gx-kg-draft__node">
                  <span
                    className="gx-kg-card__icon"
                    style={{ color: meta.color, background: alphaBg(meta.color) }}
                    aria-hidden
                  >
                    <meta.Icon size={16} />
                  </span>
                  <div className="gx-kg-card__meta">
                    <div className="gx-kg-card__head">
                      <span className="gx-kg-card__name">{n.name}</span>
                      <Badge size="sm" style={{ color: meta.color, background: alphaBg(meta.color) }}>
                        {t(meta.labelKey)}
                      </Badge>
                      {n.gmPrivate && (
                        <Badge variant="neutral" size="sm">
                          <EyeOff size={11} aria-hidden /> {t("knowledge.gmOnlyBadge")}
                        </Badge>
                      )}
                    </div>
                    {/* Bodies may be markdown; a one-line ellipsized preview
                        can't render it, so flatten instead of leaking ** marks. */}
                    {n.body && <span className="gx-kg-card__snippet">{stripMarkdown(n.body)}</span>}
                  </div>
                  <IconButton
                    size="sm"
                    tone="danger"
                    label={t("knowledge.draftRemoveEntryAria", { name: n.name })}
                    onClick={() => removeNode(i)}
                  >
                    <X size={14} />
                  </IconButton>
                </li>
              );
            })}
          </ul>
          {draft.edges.length > 0 && (
            <ul className="gx-kg-draft__edges">
              {draft.edges.map((e, i) => (
                <li key={i} className="gx-kg-draft__edge">
                  <span className="gx-kg-draft__edge-text">
                    {draft.nodes[e.fromIndex]?.name} → <em>{edgeLabel(t, e.edgeType)}</em> →{" "}
                    {draft.nodes[e.toIndex]?.name}
                  </span>
                  <IconButton
                    size="sm"
                    tone="danger"
                    label={t("knowledge.draftRemoveConnectionAria")}
                    onClick={() => removeEdge(i)}
                  >
                    <X size={13} />
                  </IconButton>
                </li>
              ))}
            </ul>
          )}
          <div className="gx-kg-editor__actions">
            <Button
              variant="primary"
              iconStart={<Plus size={14} />}
              onClick={() => void runApply()}
              disabled={apply.isPending || draft.nodes.length === 0}
            >
              {apply.isPending
                ? t("knowledge.draftApplying")
                : (draft.nodes.length === 1
                    ? t("knowledge.draftApplyOne")
                    : t("knowledge.draftApplyMany", { n: draft.nodes.length })) +
                  (draft.edges.length > 0
                    ? draft.edges.length === 1
                      ? t("knowledge.draftApplyConnectionsOne")
                      : t("knowledge.draftApplyConnectionsMany", { n: draft.edges.length })
                    : "")}
            </Button>
            <Button
              variant="ghost"
              onClick={() => {
                onStateChange({ ...state, prompt: "", draft: null });
                setError(null);
              }}
              disabled={apply.isPending}
            >
              {t("knowledge.draftDiscard")}
            </Button>
            {error && (
              <span className="gx-editor__status gx-editor__status--error" role="alert">
                {t("knowledge.draftApplyError", { message: error })}
              </span>
            )}
          </div>
        </>
      )}
    </Card>
  );
}
