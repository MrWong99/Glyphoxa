import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { Sparkles } from "lucide-react";

import { CampaignService, EdgeType, NodeType } from "@gen/glyphoxa/management/v1/management_pb";
import type { GraphEdge, GraphNode } from "@gen/glyphoxa/management/v1/management_pb";
import { Button } from "@/components/ui/Button";
import { Select } from "@/components/ui/Select";
import { EDGE_TYPES, TYPE_META, TYPE_ORDER, alphaBg, metaOf } from "../knowledgeVocab";
import { invalidateProposalReview } from "../knowledgeCache";
import { KindBadge, ProposalActions, ProposalWrite, SimilarHint, fmtWhen } from "../proposalParts";
import { AgentLensBar, useAgentLens } from "./AgentLens";
import { LAYOUT, egoNetwork, filterGraph, layout } from "./layout";
import type { Pin } from "./layout";
import { placeProposals, resolveProposals } from "./proposalGhosts";
import type { ResolvedProposal } from "./proposalGhosts";

// The Graph view (#534, ADR-0008 amendment "no graph viz" reversal). Edges were
// authorable through NodeRelations' dropdown pair and then never displayed
// anywhere — so the GM could not see the structure they built, which both hid
// errors and quietly discouraged building edges at all. Edges are exactly what
// AgentNodeFacts walks to fill an NPC's Hot Context, so that invisibility
// degraded every NPC silently.
//
// Rendered as SVG rather than canvas: it stays inside the ADR-0017 token/CSS
// vocabulary, the 7-type palette transfers unchanged from the list view, and it
// is inspectable in jsdom so the vitest suite can assert on the actual picture.
//
// The graph is a NAVIGATION surface, not a second editor: clicking a node opens
// the existing EntryEditor in the side rail, and the one thing authored here —
// drag one node onto another to create an Edge — goes through the existing
// CreateEdge RPC.

/** Focus depth options. Depth 1 is exactly what AgentNodeFacts walks (ADR-0008). */
const FOCUS_DEPTHS = [1, 2] as const;
type FocusDepth = (typeof FOCUS_DEPTHS)[number];

/**
 * LABEL_BUDGET is how many nodes may show their name at once. Past it the labels
 * collide into unreadable soup, so only the hovered / focused / selected node is
 * labelled and the filters and focus mode become the way to read a big campaign —
 * which is the answer this slice committed to for legibility at 200+ nodes.
 */
const LABEL_BUDGET = 80;

export function KnowledgeGraph({
  nodes,
  edges,
  selectedID,
  onSelectNode,
  onGraphChanged,
}: {
  nodes: GraphNode[];
  edges: GraphEdge[];
  selectedID: string | null;
  onSelectNode: (id: string) => void;
  onGraphChanged: () => void;
}) {
  const [types, setTypes] = useState<ReadonlySet<NodeType>>(() => new Set(TYPE_ORDER));
  const [relations, setRelations] = useState<ReadonlySet<EdgeType>>(
    () => new Set(EDGE_TYPES.map((e) => e.value)),
  );
  const [hidePrivate, setHidePrivate] = useState(false);
  const [focusID, setFocusID] = useState<string | null>(null);
  const [focusDepth, setFocusDepth] = useState<FocusDepth>(1);
  const [hoverID, setHoverID] = useState<string | null>(null);
  const [pan, setPan] = useState({ x: 0, y: 0 });
  // panFrom is the pointer position a canvas drag started at, in client pixels.
  // Dragging a NODE means "link these two", so panning is dragging the empty
  // canvas — the two gestures never compete for the same pointer-down.
  const [panFrom, setPanFrom] = useState<{ x: number; y: number } | null>(null);
  // dragFrom is the in-flight drag-to-link gesture's origin; pendingLink is the
  // completed pair the relation picker is asking about. They are separate because
  // the gesture ends (and dragFrom clears) the moment the picker opens — folding
  // them into one field made the picker forget which end it started from.
  const [dragFrom, setDragFrom] = useState<string | null>(null);
  const [pendingLink, setPendingLink] = useState<{ from: string; to: string } | null>(null);
  const [linkError, setLinkError] = useState<string | null>(null);
  // The agent-knowledge lens (#535): which NPC's injected subgraph to highlight.
  const [lensAgentID, setLensAgentID] = useState("");
  // Pending Knowledge Proposals drawn in place (#537). showProposals is on by
  // default: a queue the GM cannot see is a queue that grows.
  const [showProposals, setShowProposals] = useState(true);
  const [reviewID, setReviewID] = useState<string | null>(null);
  // layoutEpoch is the "Re-arrange" button. Bumping it drops the pinned positions
  // and lets the simulation lay the graph out fresh.
  const [layoutEpoch, setLayoutEpoch] = useState(0);
  const queryClient = useQueryClient();

  const lens = useAgentLens(lensAgentID, nodes, edges);
  const active = lens.state?.linked ? lens.state.lens : null;
  // filterGraph only needs to know WHETHER a lens is on. Depending on the lens
  // OBJECT would re-run the filter — and therefore the 300-tick layout — on every
  // preview refetch, for an output that did not change.
  const lensOn = active !== null;

  // The pending queue. It is the SAME read the Proposals panel uses, so the two
  // review surfaces can never disagree about what is pending.
  const proposalsQuery = useQuery(CampaignService.method.listKnowledgeProposals, {});
  const resolved = useMemo(
    () => resolveProposals(proposalsQuery.data?.proposals ?? [], nodes),
    [proposalsQuery.data, nodes],
  );

  const createEdge = useMutation(CampaignService.method.createEdge, {
    onSuccess: () => {
      setPendingLink(null);
      setLinkError(null);
      onGraphChanged();
    },
    onError: (err) => setLinkError(err.message),
  });

  // Focus is computed over the UNFILTERED edge set on purpose: a relation chip
  // narrows what is DRAWN, but an ego network the GM asked for should not silently
  // lose members because one relation type is toggled off.
  const focus = useMemo(
    () => (focusID ? egoNetwork(focusID, edges, focusDepth) : null),
    [focusID, edges, focusDepth],
  );

  const filtered = useMemo(
    () =>
      filterGraph(nodes, edges, {
        types,
        relations,
        // A lens exists to show what an NPC is DENIED, so table view (which removes
        // gm_private entries outright) would erase exactly the ghosts it is meant to
        // surface. The lens wins while it is on.
        hidePrivate: hidePrivate && !lensOn,
        focus,
      }),
    [nodes, edges, types, relations, hidePrivate, focus, lensOn],
  );

  // Position memory. `layout` is pure in its inputs, so adding one Node reshuffles
  // every other Node — which is exactly what happens when the GM approves a
  // suggestion mid-review, and "the graph jumped" is how a review session stops
  // being a review session (#537 AC3).
  //
  // A change to the FILTERS is a request for a fresh picture and clears the pins;
  // a change to the PAYLOAD is not, so an approve, an edit, or a refetch leaves
  // every Node exactly where it was.
  const filterKey = useMemo(
    () =>
      JSON.stringify([
        [...types].sort(),
        [...relations].sort(),
        hidePrivate,
        focusID,
        focusDepth,
        lensOn,
        layoutEpoch,
      ]),
    [types, relations, hidePrivate, focusID, focusDepth, lensOn, layoutEpoch],
  );
  const sticky = useRef<{ key: string; pins: Map<string, Pin>; seeds: Map<string, Pin> }>({
    key: "",
    pins: new Map(),
    seeds: new Map(),
  });

  const laid = useMemo(() => {
    // A DIFFERENT WORLD resets the memory too. The graph stays mounted across a
    // campaign switch, and seeds are keyed by lower-cased NAME — so an approved
    // "Bart" in campaign A would pin campaign B's "Bart" to A's coordinates. If
    // not one remembered id survives into the new payload, this is not the same
    // world.
    const carriedOver =
      sticky.current.pins.size === 0 ||
      filtered.nodes.some((n) => sticky.current.pins.has(n.id));
    if (sticky.current.key !== filterKey || !carriedOver) {
      sticky.current = { key: filterKey, pins: new Map(), seeds: new Map() };
    }
    const out = layout(filtered.nodes, filtered.edges, {
      pins: sticky.current.pins,
      seeds: sticky.current.seeds,
    });
    // Recording the result makes the next run idempotent: with every Node pinned,
    // layout returns exactly these coordinates again.
    sticky.current.pins = new Map(out.nodes.map((p) => [p.node.id, { x: p.x, y: p.y }]));
    return out;
  }, [filtered, filterKey]);

  // Table view is the players-are-watching mode: it removes gm_private entries
  // outright so the GM can share the screen. Unreviewed suggestions are strictly
  // worse than a private entry there — they are an Agent's unvetted guesses about
  // the plot, they can name a gm_private entry in their subject line, and the
  // review card spells out the whole write. So table view takes the overlay with
  // it, rather than leaving the GM to remember the toggle.
  const proposalsVisible = showProposals && !hidePrivate;
  const placed = useMemo(
    () => placeProposals(proposalsVisible ? resolved : [], laid),
    [resolved, laid, proposalsVisible],
  );
  const reviewing =
    proposalsVisible && reviewID ? (resolved.find((r) => r.id === reviewID) ?? null) : null;

  const afterReview = () => {
    setReviewID(null);
    invalidateProposalReview(queryClient);
    onGraphChanged();
  };

  const showAllLabels = laid.nodes.length <= LABEL_BUDGET;
  const width = placed.bounds.maxX - placed.bounds.minX;
  const height = placed.bounds.maxY - placed.bounds.minY;

  // Zoom. A viewBox alone fits the WHOLE graph into a fixed-height canvas, which
  // at a few hundred nodes shrinks every glyph to a few pixels — "it all fits" is
  // not the same as legible. Zoom keeps the glyphs readable and turns the canvas
  // into a window the GM pans, with filters and focus mode narrowing what has to
  // fit in the first place.
  const [zoom, setZoom] = useState(1);
  const view = {
    w: width / zoom,
    h: height / zoom,
    x: placed.bounds.minX + (width - width / zoom) / 2 + pan.x,
    y: placed.bounds.minY + (height - height / zoom) / 2 + pan.y,
  };

  const toggle = <T,>(set: ReadonlySet<T>, value: T): ReadonlySet<T> => {
    const next = new Set(set);
    if (next.has(value)) next.delete(value);
    else next.add(value);
    return next;
  };

  const nodeName = (id: string) => nodes.find((n) => n.id === id)?.name ?? "";

  return (
    <div className="gx-kg-graph">
      <AgentLensBar agentID={lensAgentID} onAgentChange={setLensAgentID} lens={lens} />

      <div className="gx-kg-graph__filters">
        <div className="gx-kg-graph__chips" role="group" aria-label="Filter by type">
          {TYPE_ORDER.map((t) => {
            const meta = TYPE_META[t];
            const on = types.has(t);
            return (
              <button
                key={t}
                type="button"
                className="gx-kg-chip"
                aria-pressed={on}
                onClick={() => setTypes((s) => toggle(s, t))}
                style={on ? { color: meta.color, background: alphaBg(meta.color) } : undefined}
              >
                {meta.label}
              </button>
            );
          })}
        </div>
        <div className="gx-kg-graph__chips" role="group" aria-label="Filter by relation">
          {EDGE_TYPES.map((e) => (
            <button
              key={e.value}
              type="button"
              className="gx-kg-chip gx-kg-chip--relation"
              aria-pressed={relations.has(e.value)}
              onClick={() => setRelations((s) => toggle(s, e.value))}
            >
              {e.label}
            </button>
          ))}
        </div>
        <div className="gx-kg-graph__modes">
          <button
            type="button"
            className="gx-kg-chip"
            aria-pressed={hidePrivate}
            onClick={() => setHidePrivate((v) => !v)}
          >
            Table view
          </button>
          {resolved.length > 0 && !hidePrivate && (
            <button
              type="button"
              className="gx-kg-chip gx-kg-chip--proposals"
              aria-pressed={showProposals}
              onClick={() => {
                setShowProposals((v) => !v);
                setReviewID(null);
              }}
            >
              <Sparkles size={12} /> Suggestions ({resolved.length})
            </button>
          )}
          <button
            type="button"
            className="gx-kg-chip"
            aria-label="Zoom in"
            onClick={() => setZoom((z) => Math.min(z * 1.5, 12))}
          >
            +
          </button>
          <button
            type="button"
            className="gx-kg-chip"
            aria-label="Zoom out"
            onClick={() => setZoom((z) => Math.max(z / 1.5, 1))}
          >
            −
          </button>
          {(zoom !== 1 || pan.x !== 0 || pan.y !== 0) && (
            <Button
              variant="ghost"
              size="sm"
              onClick={() => {
                setZoom(1);
                setPan({ x: 0, y: 0 });
              }}
            >
              Fit
            </Button>
          )}
          {/* Positions are sticky across edits so a review never reshuffles the
              GM's mental map. This is the deliberate way to ask for a fresh one. */}
          <Button variant="ghost" size="sm" onClick={() => setLayoutEpoch((n) => n + 1)}>
            Re-arrange
          </Button>
          {focusID && (
            <>
              <span className="gx-kg-graph__focus">Focused on {nodeName(focusID)}</span>
              {FOCUS_DEPTHS.map((d) => (
                <button
                  key={d}
                  type="button"
                  className="gx-kg-chip"
                  aria-pressed={focusDepth === d}
                  onClick={() => setFocusDepth(d)}
                >
                  Depth {d}
                </button>
              ))}
              <Button variant="ghost" size="sm" onClick={() => setFocusID(null)}>
                Clear focus
              </Button>
            </>
          )}
        </div>
      </div>

      {/* The canvas is skipped only when there is NOTHING to draw — including
          suggestions. A brand-new campaign whose Butler has proposed its first
          entries has no committed Nodes at all, and gating on those alone hid
          every ghost behind "nothing to draw" while the chip said there were
          suggestions. */}
      {laid.nodes.length === 0 && placed.ghostNodes.length === 0 ? (
        <p className="gx-kg-empty">
          {resolved.length > 0 && !proposalsVisible
            ? "Nothing to draw here. Suggestions are hidden in table view."
            : "Nothing to draw — every entry is filtered out. Turn a type chip back on."}
        </p>
      ) : (
        <svg
          className="gx-kg-graph__canvas"
          role="img"
          aria-label={`Knowledge graph: ${laid.nodes.length} entries, ${laid.edges.length} relationships`}
          viewBox={`${view.x} ${view.y} ${view.w} ${view.h}`}
          onMouseDown={(ev) => {
            // Only a press on the canvas ITSELF starts a pan; a press on a node
            // bubbles here and must stay a link gesture.
            if (ev.target === ev.currentTarget) setPanFrom({ x: ev.clientX, y: ev.clientY });
          }}
          onMouseMove={(ev) => {
            if (!panFrom) return;
            // Client pixels → view units, so a drag moves the graph by the distance
            // under the pointer regardless of zoom.
            const scale = view.w / Math.max(ev.currentTarget.clientWidth, 1);
            setPan((p) => ({
              x: p.x - (ev.clientX - panFrom.x) * scale,
              y: p.y - (ev.clientY - panFrom.y) * scale,
            }));
            setPanFrom({ x: ev.clientX, y: ev.clientY });
          }}
          // A release outside any node abandons the gesture rather than leaving a
          // half-started link armed forever.
          onMouseUp={() => {
            setDragFrom(null);
            setPanFrom(null);
          }}
          onMouseLeave={() => {
            setDragFrom(null);
            setPanFrom(null);
            setHoverID(null);
          }}
        >
          <g className="gx-kg-graph__edges">
            {laid.edges.map((e) => (
              <line
                key={e.edge.id}
                className="gx-kg-graph__edge"
                data-relation={EDGE_LABEL.get(e.edge.edgeType) ?? ""}
                // Disposition drives the colour (#546): a graph of bare `knows`
                // lines says nothing about how the world feels about itself.
                data-disposition={e.edge.disposition !== 0 ? e.edge.disposition : undefined}
                x1={e.x1}
                y1={e.y1}
                x2={e.x2}
                y2={e.y2}
              >
                {(e.edge.note !== "" || e.edge.disposition !== 0) && (
                  <title>{edgeTooltip(e.edge)}</title>
                )}
              </line>
            ))}
            {/* Proposed relations, dashed. A wide transparent line rides on top of
                each so it can actually be clicked — a 1px stroke is not a target. */}
            {placed.ghostEdges.map((g) => (
              <g key={g.key} className="gx-kg-graph__proposed-edge">
                <line x1={g.x1} y1={g.y1} x2={g.x2} y2={g.y2} />
                <line
                  className="gx-kg-graph__proposed-hit"
                  x1={g.x1}
                  y1={g.y1}
                  x2={g.x2}
                  y2={g.y2}
                  role="button"
                  tabIndex={0}
                  aria-label={`Suggested relationship from ${g.agentName} — review`}
                  onClick={() => setReviewID(g.proposalID)}
                  onKeyDown={(ev) => {
                    if (ev.key === "Enter" || ev.key === " ") {
                      ev.preventDefault();
                      setReviewID(g.proposalID);
                    }
                  }}
                />
              </g>
            ))}
          </g>
          <g className="gx-kg-graph__nodes">
            {laid.nodes.map((p) => {
              const meta = metaOf(p.node.nodeType);
              // Lens states, in the order they matter: a withheld gm_private
              // neighbour is a GHOST (adjacent, deliberately hidden), a capped-out
              // one is DROPPED (adjacent, did not fit — a different problem), the
              // injected ones are lit, and everything else is dimmed out of the way.
              const ghosted = active?.ghostIDs.has(p.node.id) ?? false;
              const dropped = active?.droppedIDs.has(p.node.id) ?? false;
              const lit = active ? active.includedIDs.has(p.node.id) : true;
              // Past the budget only the nodes the GM is actively looking at keep
              // their name — including the focus root, which is the one node they
              // definitely care about in a focused view.
              const labelled =
                showAllLabels ||
                hoverID === p.node.id ||
                selectedID === p.node.id ||
                focusID === p.node.id;
              return (
                <g
                  key={p.node.id}
                  className="gx-kg-graph__node"
                  data-node-id={p.node.id}
                  data-private={p.node.gmPrivate || undefined}
                  data-selected={selectedID === p.node.id || undefined}
                  data-lens={active ? (ghosted ? "ghost" : dropped ? "dropped" : lit ? "lit" : "dim") : undefined}
                  role="button"
                  tabIndex={0}
                  aria-label={
                    `${p.node.name} (${meta.label})` +
                    (p.node.gmPrivate ? ", GM private" : "") +
                    (ghosted ? `, hidden from ${active?.agentName}` : "") +
                    (dropped ? `, dropped from ${active?.agentName}'s prompt — over budget` : "")
                  }
                  transform={`translate(${p.x} ${p.y})`}
                  onMouseDown={() => setDragFrom(p.node.id)}
                  onMouseEnter={() => setHoverID(p.node.id)}
                  // Clearing on leave, not only on leaving the whole canvas: otherwise a
                  // stale label clings to the last node the pointer crossed.
                  onMouseLeave={() => setHoverID((id) => (id === p.node.id ? null : id))}
                  onMouseUp={() => {
                    // A drag that started on ANOTHER node lands here: offer the relation
                    // picker. A press and release on the same node is a click, not a link.
                    if (dragFrom && dragFrom !== p.node.id) {
                      setPendingLink({ from: dragFrom, to: p.node.id });
                      setLinkError(null);
                    }
                    setDragFrom(null);
                  }}
                  onClick={() => onSelectNode(p.node.id)}
                  onDoubleClick={() => setFocusID(p.node.id)}
                  onKeyDown={(ev) => {
                    if (ev.key === "Enter" || ev.key === " ") {
                      ev.preventDefault();
                      onSelectNode(p.node.id);
                    }
                    if (ev.key === "f") setFocusID(p.node.id);
                  }}
                >
                  {/* A tooltip, because the lens states were otherwise legible only
                      to a screen reader — a sighted GM saw two struck nodes and could
                      not tell which fix each one needed. */}
                  {(ghosted || dropped) && (
                    <title>
                      {ghosted
                        ? `Hidden from ${active?.agentName} — this entry is GM private`
                        : `Dropped from ${active?.agentName}'s prompt — the facts block is full`}
                    </title>
                  )}
                  <circle
                    r={LAYOUT.nodeRadius}
                    fill={alphaBg(meta.color)}
                    stroke={meta.color}
                    // gm_private is drawn dashed: present in the GM's map, visibly
                    // absent from the world their NPCs see.
                    strokeDasharray={p.node.gmPrivate ? "3 2" : undefined}
                  />
                  {/* A withheld neighbour gets a slash through the glyph; an
                      over-budget one gets a ring. Two exclusions, two fixes, two
                      shapes — the aria-label alone was not a visual distinction. */}
                  {ghosted && (
                    <line
                      className="gx-kg-graph__slash"
                      x1={-LAYOUT.nodeRadius - 3}
                      y1={LAYOUT.nodeRadius + 3}
                      x2={LAYOUT.nodeRadius + 3}
                      y2={-LAYOUT.nodeRadius - 3}
                    />
                  )}
                  {dropped && <circle className="gx-kg-graph__overflow" r={LAYOUT.nodeRadius + 4} />}
                  {(labelled || ghosted || dropped) && (
                    <text className="gx-kg-graph__label" x={LAYOUT.nodeRadius + 4} y={4}>
                      {p.node.name}
                    </text>
                  )}
                </g>
              );
            })}
          </g>

          {/* Pending proposals, drawn in place (#537). Dashed and hollow
              throughout: a suggestion must never be mistakable for canon, which is
              the whole reason the queue exists. */}
          <g className="gx-kg-graph__proposals">
            {placed.factMarks.map((m) => (
              <g
                key={m.key}
                className="gx-kg-graph__fact-mark"
                transform={`translate(${m.x} ${m.y})`}
                role="button"
                tabIndex={0}
                aria-label={`Suggested fact from ${m.agentName} — review`}
                onClick={() => setReviewID(m.proposalID)}
                onKeyDown={(ev) => {
                  if (ev.key === "Enter" || ev.key === " ") {
                    ev.preventDefault();
                    setReviewID(m.proposalID);
                  }
                }}
              >
                <title>{`${m.agentName} suggests a fact here`}</title>
                <circle r={LAYOUT.nodeRadius + 7} />
                {/* A 1.5px dashed ring is not a click target. */}
                <circle className="gx-kg-graph__fact-hit" r={LAYOUT.nodeRadius + 7} />
              </g>
            ))}
            {placed.ghostNodes.map((g) => {
              const meta = metaOf(g.nodeType);
              return (
                <g
                  key={g.key}
                  className="gx-kg-graph__proposed-node"
                  data-proposed
                  data-selected={reviewID === g.proposalID || undefined}
                  transform={`translate(${g.x} ${g.y})`}
                  role="button"
                  tabIndex={0}
                  aria-label={`Suggested new entry ${g.name} (${meta.label}) from ${g.agentName} — review`}
                  onClick={() => setReviewID(g.proposalID)}
                  onKeyDown={(ev) => {
                    if (ev.key === "Enter" || ev.key === " ") {
                      ev.preventDefault();
                      setReviewID(g.proposalID);
                    }
                  }}
                >
                  <title>{`${g.agentName} suggests creating ${g.name}`}</title>
                  <circle r={LAYOUT.nodeRadius} stroke={meta.color} />
                  <text className="gx-kg-graph__label" x={LAYOUT.nodeRadius + 4} y={4}>
                    {g.name}
                  </text>
                </g>
              );
            })}
          </g>
        </svg>
      )}

      {/* Suggestions that cannot be drawn are LISTED, never dropped: an
          unresolvable name is a proposal that will be refused at approval, and
          that is worth knowing before clicking. */}
      {proposalsVisible && placed.unplaced.length > 0 && (
        <details className="gx-kg-graph__unplaced">
          <summary>
            {placed.unplaced.length} suggestion{placed.unplaced.length === 1 ? "" : "s"} can&apos;t be
            drawn here
          </summary>
          <ul>
            {placed.unplaced.map((u) => (
              <li key={u.proposal.id}>
                {/* Labelled with the WRITE, not just the author: two undrawable
                    suggestions from the same Agent were two identical buttons. */}
                <button type="button" className="gx-kg-chip" onClick={() => setReviewID(u.proposal.id)}>
                  {u.proposal.agentName}: <ProposalWrite proposal={u.proposal.proposal} />
                </button>
                <span className="gx-kg-graph__unplaced-why">{u.reason}</span>
              </li>
            ))}
          </ul>
        </details>
      )}

      {reviewing && (
        <ProposalReview
          resolved={reviewing}
          onClose={() => setReviewID(null)}
          onApproved={() => {
            // Remember where the ghost stood so the created Node inherits the spot:
            // the server mints a fresh id at approval, and the NAME is the only
            // thing that survives the transition (#537 AC3).
            const ghost = placed.ghostNodes.find((g) => g.proposalID === reviewing.id);
            if (ghost) {
              sticky.current.seeds.set(ghost.name.trim().toLowerCase(), { x: ghost.x, y: ghost.y });
            }
          }}
          onReviewed={afterReview}
        />
      )}

      {pendingLink && (
        <RelationPicker
          fromName={nodeName(pendingLink.from)}
          toName={nodeName(pendingLink.to)}
          pending={createEdge.isPending}
          error={linkError}
          onCancel={() => {
            setPendingLink(null);
            setLinkError(null);
          }}
          onPick={(edgeType) =>
            createEdge.mutate({
              fromNodeId: pendingLink.from,
              toNodeId: pendingLink.to,
              edgeType,
            })
          }
        />
      )}
    </div>
  );
}

const EDGE_LABEL = new Map<EdgeType, string>(EDGE_TYPES.map((e) => [e.value, e.label]));

/** DISPOSITION_LABEL names the -2..+2 scale in words the GM chose it by. */
export const DISPOSITION_LABEL: Record<number, string> = {
  [-2]: "hostile",
  [-1]: "wary",
  0: "neutral",
  1: "warm",
  2: "devoted",
};

// edgeTooltip is what hovering a relation says: its feeling, and the GM's note.
function edgeTooltip(edge: GraphEdge): string {
  const parts: string[] = [];
  if (edge.disposition !== 0) parts.push(DISPOSITION_LABEL[edge.disposition] ?? "");
  if (edge.note !== "") parts.push(edge.note);
  return parts.filter(Boolean).join(" — ");
}

// ProposalReview is the in-place review card: everything the queue's card shows,
// beside the graph, for the ghost the GM just clicked.
//
// It renders the SAME widgets and drives the SAME RPCs as the Proposals panel —
// the write path is untouched, exactly as ADR-0052 requires. What the graph adds
// is POSITION: the question "does this belong here, next to what the world already
// says" is a question about the picture, and now it is asked in front of one.
function ProposalReview({
  resolved,
  onClose,
  onApproved,
  onReviewed,
}: {
  resolved: ResolvedProposal;
  onClose: () => void;
  onApproved: () => void;
  onReviewed: () => void;
}) {
  // The card renders BELOW the canvas, and the canvas is up to 720px tall — so on
  // an ordinary screen clicking a ghost opened a card the GM could not see, which
  // reads as "clicking did nothing". Found by using it, not by a test.
  const card = useRef<HTMLDivElement>(null);
  useEffect(() => {
    card.current?.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }, [resolved.id]);

  // An endpoint the client could not resolve will be REFUSED at approval. Saying
  // so before the GM clicks beats an inline server error afterwards.
  const unresolved =
    resolved.write.kind === "edge"
      ? [resolved.write.from, resolved.write.to].find((a) => a.at === "unknown")
      : resolved.write.kind === "fact" && resolved.write.anchor.at === "unknown"
        ? resolved.write.anchor
        : undefined;

  return (
    <div className="gx-kg-graph__review" role="dialog" aria-label="Review suggestion" ref={card}>
      <div className="gx-proposal-card__head">
        {/* WHO proposed is most of the judgement: a Character NPC may only propose
            on its own linked Node, the Butler campaign-wide. */}
        <span className="gx-proposal-card__author">{resolved.agentName}</span>
        <span className="gx-proposal-card__when">{fmtWhen(resolved.proposal)}</span>
        <KindBadge proposal={resolved.proposal} />
      </div>

      <div className="gx-proposal-card__write">
        <ProposalWrite proposal={resolved.proposal} />
      </div>

      {unresolved && unresolved.at === "unknown" && (
        <p className="gx-editor__status gx-editor__status--error" role="alert">
          Approving will be refused: {unresolved.reason}.
        </p>
      )}

      {/* The ADR-0011 similarity hint travels with the proposal rather than being
          dropped for the visual — "is this a duplicate" is the other half of the
          decision, and the graph does not answer it. */}
      <SimilarHint proposalId={resolved.id} />

      <ProposalActions proposalID={resolved.id} onApproved={onApproved} onReviewed={onReviewed} />

      <Button variant="ghost" size="sm" onClick={onClose}>
        Close
      </Button>
    </div>
  );
}

// RelationPicker is the one authoring affordance on the graph: after dragging one
// entry onto another, the GM names the relation. It deliberately reuses the closed
// relation vocabulary rather than inventing a graph-only one, and it lands through
// the existing CreateEdge RPC — including its server-side validity matrix, so an
// illegal relation is refused here exactly as it is in NodeRelations.
function RelationPicker({
  fromName,
  toName,
  pending,
  error,
  onPick,
  onCancel,
}: {
  fromName: string;
  toName: string;
  pending: boolean;
  error: string | null;
  onPick: (edgeType: EdgeType) => void;
  onCancel: () => void;
}) {
  const [edgeType, setEdgeType] = useState<EdgeType>(EDGE_TYPES[0].value);
  return (
    <div className="gx-kg-graph__picker" role="dialog" aria-label="Create relationship">
      <p className="gx-kg-graph__picker-text">
        <strong>{fromName}</strong> → <strong>{toName}</strong>
      </p>
      <Select
        label="Relation"
        options={EDGE_TYPES.map((e) => ({ value: String(e.value), label: e.label }))}
        value={String(edgeType)}
        onValueChange={(v) => setEdgeType(Number(v) as EdgeType)}
      />
      <div className="gx-kg-editor__actions">
        <Button variant="primary" disabled={pending} onClick={() => onPick(edgeType)}>
          {pending ? "Linking…" : "Create relationship"}
        </Button>
        <Button variant="ghost" onClick={onCancel} disabled={pending}>
          Cancel
        </Button>
        {error && (
          <span className="gx-editor__status gx-editor__status--error" role="alert">
            {error}
          </span>
        )}
      </div>
    </div>
  );
}
