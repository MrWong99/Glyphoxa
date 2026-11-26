import { useMemo } from "react";
import { useMutation, useQuery } from "@connectrpc/connect-query";
import { AlertTriangle, Copy } from "lucide-react";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import type {
  FindDuplicateEntriesResponse,
  GraphEdge,
  GraphNode,
} from "@gen/glyphoxa/management/v1/management_pb";
import { useI18n } from "@/i18n";
import { Button } from "@/components/ui/Button";
import { metaOf } from "../knowledgeVocab";
import { worldHealth } from "./worldHealth";
import { errorMessage } from "@/lib/connectError";

// The world checkup panel (#536): derived warnings about the campaign's wiki,
// computed from the graph payload the tab already holds. No new storage.
//
// Every row is a LINK INTO THE EDITOR, never a fix button. ADR-0052 rejected
// auto-merging near-duplicate facts because similarity is a hint, not a semantic
// judgment, and a wrong merge corrupts canon invisibly — the same reasoning
// applies to every category here.

/** The slice of the FindDuplicateEntries mutation this panel reads and drives. */
export type DuplicateScan = {
  mutate: (input: Record<string, never>) => void;
  data: FindDuplicateEntriesResponse | undefined;
  error: unknown;
  isPending: boolean;
  isError: boolean;
  isSuccess: boolean;
};

export function WorldHealthPanel({
  nodes,
  edges,
  onOpenNode,
  onOpenCast,
  duplicateScan,
}: {
  nodes: GraphNode[];
  edges: GraphEdge[];
  onOpenNode: (id: string) => void;
  /**
   * The duplicate scan, when the host holds it. The scan is an exact pairwise
   * comparison the GM waits for, and its result is a to-do list they work
   * through by opening entries; held here it was thrown away by the first
   * switch to the list. The wiki panel owns it so it survives a mode switch.
   * Without it (the panel mounted alone) it lives and dies with this panel.
   */
  duplicateScan?: DuplicateScan;
  /** Where a cast Agent with no entry gets fixed — the roster, not the wiki. */
  onOpenCast?: (agentID: string) => void;
}) {
  const { t } = useI18n();
  // retry:false so a failure settles into isError promptly. This panel's whole job
  // is telling the GM what is wrong; silently backing off for seconds while
  // withholding the cast checks is the opposite of that.
  const rosterQuery = useQuery(CampaignService.method.getCampaignRoster, {}, { retry: false });
  const categories = useMemo(
    () => worldHealth(nodes, edges, rosterQuery.data?.roster ?? []),
    [nodes, edges, rosterQuery.data],
  );

  // The duplicate check is a mutation, not a query, precisely so it CANNOT fire on
  // render: it is an exact pairwise embedding scan and belongs behind a button.
  const ownScan = useMutation(CampaignService.method.findDuplicateEntries);
  const duplicates = duplicateScan ?? ownScan;

  // "Nothing to flag" must not be claimed while the roster read is still in
  // flight or failed: the cast-without-entry category depends on it, so a clean
  // bill of health would be a lie the GM has no reason to doubt.
  const rosterKnown = rosterQuery.isSuccess;
  const healthy = categories.length === 0 && rosterKnown;

  return (
    <section className="gx-kg-health" aria-label={t("knowledge.healthAria")}>
      {rosterQuery.isError && (
        <p className="gx-campaign__error" role="alert">
          {t("knowledge.healthRosterError", { message: errorMessage(rosterQuery.error) })}
        </p>
      )}
      {healthy ? (
        <p className="gx-kg-health__clear">{t("knowledge.healthClear")}</p>
      ) : (
        categories.map((c) => (
          <div key={c.key} className="gx-kg-health__group">
            <h4 className="gx-kg-health__title">
              <AlertTriangle size={13} aria-hidden /> {t(c.title)}
              <span className="gx-kg-health__count">{c.findings.length}</span>
            </h4>
            <p className="gx-kg-health__why">{t(c.why)}</p>
            <ul className="gx-kg-health__list">
              {c.findings.map((f, i) => {
                const meta = metaOf(f.nodeType);
                return (
                  <li key={f.nodeID || `${c.key}-${i}`} className="gx-kg-health__row">
                    {/* Every row is actionable: an entry opens in the editor, and a
                        cast Agent with no entry opens on the roster — its fix is
                        there, not in the wiki. */}
                    <button
                      type="button"
                      className="gx-kg-health__link"
                      style={{ color: meta.color }}
                      onClick={() =>
                        f.nodeID ? onOpenNode(f.nodeID) : f.agentID && onOpenCast?.(f.agentID)
                      }
                    >
                      {f.name}
                    </button>
                    {f.detail && <span className="gx-kg-health__detail"> — {t(f.detail)}</span>}
                  </li>
                );
              })}
            </ul>
          </div>
        ))
      )}

      <div className="gx-kg-health__group">
        <h4 className="gx-kg-health__title">
          <Copy size={13} aria-hidden /> {t("knowledge.duplicatesTitle")}
        </h4>
        <p className="gx-kg-health__why">{t("knowledge.duplicatesWhy")}</p>
        <Button
          variant="secondary"
          size="sm"
          disabled={duplicates.isPending}
          onClick={() => duplicates.mutate({})}
        >
          {duplicates.isPending ? t("knowledge.duplicatesRunning") : t("knowledge.duplicatesRun")}
        </Button>

        {duplicates.isError && (
          <span className="gx-editor__status gx-editor__status--error" role="alert">
            {t("knowledge.duplicatesError", { message: errorMessage(duplicates.error) })}
          </span>
        )}

        {duplicates.isSuccess && duplicates.data && (
          <>
            {duplicates.data.pairs.length === 0 ? (
              <p className="gx-kg-health__why">{t("knowledge.duplicatesNone")}</p>
            ) : (
              <ul className="gx-kg-health__list">
                {duplicates.data.pairs.map((p) => (
                  <li key={`${p.aId}-${p.bId}`} className="gx-kg-health__row">
                    <button
                      type="button"
                      className="gx-kg-health__link"
                      onClick={() => onOpenNode(p.aId)}
                    >
                      {p.aName}
                    </button>
                    {" ≈ "}
                    <button
                      type="button"
                      className="gx-kg-health__link"
                      onClick={() => onOpenNode(p.bId)}
                    >
                      {p.bName}
                    </button>
                    <span className="gx-kg-health__detail">
                      {" — "}
                      {t("knowledge.duplicatesAlike", { pct: Math.round(p.similarity * 100) })}
                    </span>
                  </li>
                ))}
              </ul>
            )}
            {duplicates.data.unembedded > 0 && (
              <p className="gx-kg-health__why">
                {duplicates.data.unembedded === 1
                  ? t("knowledge.duplicatesUnembeddedOne")
                  : t("knowledge.duplicatesUnembeddedMany", { n: duplicates.data.unembedded })}
              </p>
            )}
          </>
        )}
      </div>
    </section>
  );
}
