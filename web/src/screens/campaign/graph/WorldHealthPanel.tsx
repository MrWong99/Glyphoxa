import { useMemo, useState } from "react";
import { useMutation, useQuery } from "@connectrpc/connect-query";
import { AlertTriangle, Copy } from "lucide-react";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import type { GraphEdge, GraphNode } from "@gen/glyphoxa/management/v1/management_pb";
import { Button } from "@/components/ui/Button";
import { metaOf } from "../knowledgeVocab";
import { worldHealth } from "./worldHealth";

// The world health panel (#536): derived warnings about the campaign's wiki,
// computed from the graph payload the tab already holds. No new storage.
//
// Every row is a LINK INTO THE EDITOR, never a fix button. ADR-0052 rejected
// auto-merging near-duplicate facts because similarity is a hint, not a semantic
// judgment, and a wrong merge corrupts canon invisibly — the same reasoning
// applies to every category here.

export function WorldHealthPanel({
  nodes,
  edges,
  onOpenNode,
  onOpenCast,
}: {
  nodes: GraphNode[];
  edges: GraphEdge[];
  onOpenNode: (id: string) => void;
  /** Where a cast Agent with no entry gets fixed — the roster, not the wiki. */
  onOpenCast?: (agentID: string) => void;
}) {
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
  const duplicates = useMutation(CampaignService.method.findDuplicateEntries);
  const [ranDuplicates, setRanDuplicates] = useState(false);

  // "Nothing to flag" must not be claimed while the roster read is still in
  // flight or failed: the cast-without-entry category depends on it, so a clean
  // bill of health would be a lie the GM has no reason to doubt.
  const rosterKnown = rosterQuery.isSuccess;
  const healthy = categories.length === 0 && rosterKnown;

  return (
    <section className="gx-kg-health" aria-label="World health">
      {rosterQuery.isError && (
        <p className="gx-campaign__error" role="alert">
          Could not check the cast: {rosterQuery.error.message} — the entry checks below are
          still complete.
        </p>
      )}
      {healthy ? (
        <p className="gx-kg-health__clear">
          Nothing to flag — every entry is connected, and every voiced NPC has something to say.
        </p>
      ) : (
        categories.map((c) => (
          <div key={c.key} className="gx-kg-health__group">
            <h4 className="gx-kg-health__title">
              <AlertTriangle size={13} aria-hidden /> {c.title}
              <span className="gx-kg-health__count">{c.findings.length}</span>
            </h4>
            <p className="gx-kg-health__why">{c.why}</p>
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
                    {f.detail && <span className="gx-kg-health__detail"> — {f.detail}</span>}
                  </li>
                );
              })}
            </ul>
          </div>
        ))
      )}

      <div className="gx-kg-health__group">
        <h4 className="gx-kg-health__title">
          <Copy size={13} aria-hidden /> Probable duplicates
        </h4>
        <p className="gx-kg-health__why">
          Compares every entry's text against every other. It is a hint, never a merge — a wrong
          merge would rewrite your canon invisibly.
        </p>
        <Button
          variant="secondary"
          size="sm"
          disabled={duplicates.isPending}
          onClick={() => {
            setRanDuplicates(true);
            duplicates.mutate({});
          }}
        >
          {duplicates.isPending ? "Comparing…" : "Check for duplicates"}
        </Button>

        {duplicates.isError && (
          <span className="gx-editor__status gx-editor__status--error" role="alert">
            Couldn't check: {duplicates.error.message}
          </span>
        )}

        {ranDuplicates && duplicates.isSuccess && (
          <>
            {duplicates.data.pairs.length === 0 ? (
              <p className="gx-kg-health__why">No entries look like duplicates of each other.</p>
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
                      {" "}
                      — {Math.round(p.similarity * 100)}% alike
                    </span>
                  </li>
                ))}
              </ul>
            )}
            {duplicates.data.unembedded > 0 && (
              <p className="gx-kg-health__why">
                {duplicates.data.unembedded} entr
                {duplicates.data.unembedded === 1 ? "y is" : "ies are"} still being indexed and
                could not be compared yet.
              </p>
            )}
          </>
        )}
      </div>
    </section>
  );
}
