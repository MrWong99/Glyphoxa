import { useMemo } from "react";
import { useQuery } from "@connectrpc/connect-query";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import type { GraphEdge, GraphNode } from "@gen/glyphoxa/management/v1/management_pb";
import { useI18n } from "@/i18n";
import { Select } from "@/components/ui/Select";
import { egoNetwork } from "./layout";
import { errorMessage } from "@/lib/connectError";

// The agent-knowledge lens (#535): pick a Character NPC and the graph dims to
// exactly the subgraph kgfacts would inject for it, with a live budget readout.
//
// Today the only way to know why an NPC didn't mention something is to reason
// about AgentNodeFacts and renderFacts from memory. This makes the whole
// prompt-facing read visible — including the deterministic prefix-stop, which
// silently drops the remainder of the facts block once MaxBlockChars is crossed,
// turning a silent quality cliff into something the GM can see and fix.

/** What the lens tells the graph to draw differently. */
export type Lens = {
  agentName: string;
  /** Nodes whose facts really will be injected. */
  includedIDs: ReadonlySet<string>;
  /**
   * Nodes the caps cut. Drawn as included-but-struck, because "this is adjacent
   * and did not fit" is a different problem from "this is not adjacent".
   */
  droppedIDs: ReadonlySet<string>;
  /**
   * gm_private neighbours: adjacent, and deliberately withheld. Sourced
   * CLIENT-SIDE by diffing the graph payload against the preview — the prompt seam
   * returns public Nodes only, by design, and widening it to report what it hides
   * would defeat the point of having a seam.
   */
  ghostIDs: ReadonlySet<string>;
};

export type LensState = {
  lens: Lens | null;
  /** Rendered facts, byte-identical to what the turn injects. */
  facts: string[];
  chars: number;
  maxChars: number;
  factCount: number;
  maxFacts: number;
  truncated: boolean;
  /** The SQL read's own row cap clipped the neighbourhood before the renderer saw it. */
  clipped: boolean;
  maxNeighbours: number;
  /** false ⇒ the NPC has no linked wiki entry at all. */
  linked: boolean;
};

/**
 * useAgentLens resolves the lens for one Agent: the roster option list, the
 * preview read, and the derived node sets.
 *
 * It is a hook rather than a component-with-callback so the graph receives the
 * lens as a VALUE during the same render as the selection. Pushing it up from a
 * child's render would be a side effect during render; pushing it from an effect
 * would draw the graph one frame behind the GM's choice.
 */
export function useAgentLens(agentID: string, nodes: GraphNode[], edges: GraphEdge[]) {
  const { t } = useI18n();
  const rosterQuery = useQuery(CampaignService.method.getCampaignRoster, {});
  // Only Character NPCs: the Butler is Address-Only and has no linked Node by
  // design (ADR-0009), so there is nothing to lens.
  const cast = useMemo(
    () => (rosterQuery.data?.roster ?? []).filter((a) => a.role === "character"),
    [rosterQuery.data],
  );

  const previewQuery = useQuery(
    CampaignService.method.getAgentFactPreview,
    { agentId: agentID },
    { enabled: agentID !== "" },
  );

  const agentName = cast.find((a) => a.id === agentID)?.name ?? "";
  const preview = previewQuery.data;

  const state = useMemo<LensState | null>(() => {
    if (agentID === "" || !preview) return null;
    const included = new Set(preview.includedNodeIds);
    const dropped = new Set(preview.droppedNodeIds);

    // The ghosts: the Agent's own Node's single-hop neighbourhood — the SAME walk
    // AgentNodeFacts performs — minus everything the preview returned. What is left
    // is adjacent and withheld, i.e. gm_private.
    const ghosts = new Set<string>();
    if (preview.linked && preview.linkedNodeId !== "") {
      const byID = new Map(nodes.map((n) => [n.id, n]));
      for (const id of egoNetwork(preview.linkedNodeId, edges, 1)) {
        if (included.has(id) || dropped.has(id)) continue;
        if (byID.get(id)?.gmPrivate) ghosts.add(id);
      }
    }

    return {
      lens: { agentName, includedIDs: included, droppedIDs: dropped, ghostIDs: ghosts },
      facts: preview.facts,
      chars: preview.chars,
      maxChars: preview.maxChars,
      factCount: preview.facts.length,
      maxFacts: preview.maxFacts,
      truncated: preview.truncated,
      clipped: preview.neighbourhoodClipped,
      maxNeighbours: preview.maxNeighbours,
      linked: preview.linked,
    };
  }, [agentID, preview, nodes, edges, agentName]);

  const options = useMemo(
    () => [
      { value: LENS_NONE, label: t("knowledge.noneOption") },
      ...cast.map((a) => ({ value: a.id, label: a.name })),
    ],
    [cast, t],
  );

  return {
    state,
    options,
    agentName,
    pending: agentID !== "" && previewQuery.isPending,
    error: previewQuery.isError ? errorMessage(previewQuery.error) : null,
  };
}

/** AgentLensBar is the lens's selector and budget readout. Presentational only. */
export function AgentLensBar({
  agentID,
  onAgentChange,
  lens,
}: {
  agentID: string;
  onAgentChange: (id: string) => void;
  lens: ReturnType<typeof useAgentLens>;
}) {
  const { t, lang } = useI18n();
  const { state, options, agentName, pending, error } = lens;
  return (
    <div className="gx-kg-lens">
      <Select
        label={t("knowledge.lensLabel")}
        options={options}
        value={agentID === "" ? LENS_NONE : agentID}
        onValueChange={(v) => onAgentChange(v === LENS_NONE ? "" : v)}
      />

      {pending && (
        <span className="gx-field__hint">{t("knowledge.lensPending", { name: agentName })}</span>
      )}

      {error && (
        <span className="gx-editor__status gx-editor__status--error" role="alert">
          {t("knowledge.lensError", { name: agentName, message: error })}
        </span>
      )}

      {state && !state.linked && (
        <p className="gx-kg-lens__empty" role="status">
          {/* The name sits OUTSIDE the translated string so it can carry <strong>;
              both languages read naturally with the name first. */}
          <strong>{agentName}</strong>
          {t("knowledge.lensUnlinked")}
        </p>
      )}

      {state?.linked && (
        <div className="gx-kg-lens__budget" role="status">
          <span className="gx-kg-lens__meter">
            {/* Grouped in the DISPLAY language, not the browser's: the budget reads
                "1.234" beside German copy and "1,234" beside English. */}
            {t("knowledge.lensMeter", {
              chars: state.chars.toLocaleString(lang),
              maxChars: state.maxChars.toLocaleString(lang),
              facts: state.factCount,
              maxFacts: state.maxFacts,
            })}
          </span>
          {state.truncated && (
            <span className="gx-kg-lens__truncated">
              {(state.lens?.droppedIDs.size ?? 0) === 1
                ? t("knowledge.lensTruncatedOne")
                : t("knowledge.lensTruncatedMany", { n: state.lens?.droppedIDs.size ?? 0 })}
            </span>
          )}
          {state.clipped && (
            <span className="gx-kg-lens__truncated">
              {t("knowledge.lensClipped", { name: agentName, max: state.maxNeighbours })}
            </span>
          )}
          {state.factCount === 0 && (
            <span className="gx-kg-lens__truncated">{t("knowledge.lensNothing")}</span>
          )}

          {/* The actual block. Everything above is a summary OF this; without it the
              GM still cannot read what their NPC will be told. */}
          {state.facts.length > 0 && (
            <details className="gx-kg-lens__facts">
              <summary>{t("knowledge.lensFactsSummary", { name: agentName })}</summary>
              <pre className="gx-kg-lens__block">{state.facts.join("\n\n")}</pre>
            </details>
          )}
        </div>
      )}
    </div>
  );
}

// LENS_NONE is the Radix Select sentinel for "no lens" — Radix forbids an empty
// item value, so the clear option carries this and maps back to "".
const LENS_NONE = "__none__";
