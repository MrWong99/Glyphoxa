import { useMemo, useState } from "react";
import { useMutation } from "@connectrpc/connect-query";

import { CampaignService, EdgeType, NodeType } from "@gen/glyphoxa/management/v1/management_pb";
import type { GraphEdge, GraphNode } from "@gen/glyphoxa/management/v1/management_pb";
import { Button } from "@/components/ui/Button";
import { Select } from "@/components/ui/Select";
import { EDGE_TYPES, TYPE_META, TYPE_ORDER, alphaBg, metaOf } from "../knowledgeVocab";
import { LAYOUT, egoNetwork, filterGraph, layout } from "./layout";

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
  // dragFrom is the in-flight drag-to-link gesture's origin; pendingLink is the
  // completed pair the relation picker is asking about. They are separate because
  // the gesture ends (and dragFrom clears) the moment the picker opens — folding
  // them into one field made the picker forget which end it started from.
  const [dragFrom, setDragFrom] = useState<string | null>(null);
  const [pendingLink, setPendingLink] = useState<{ from: string; to: string } | null>(null);
  const [linkError, setLinkError] = useState<string | null>(null);

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
    () => filterGraph(nodes, edges, { types, relations, hidePrivate, focus }),
    [nodes, edges, types, relations, hidePrivate, focus],
  );

  // Layout is a pure function of the filtered payload, so this memo is the whole
  // performance story — and re-running it yields the identical picture.
  const laid = useMemo(() => layout(filtered.nodes, filtered.edges), [filtered]);

  const showAllLabels = laid.nodes.length <= LABEL_BUDGET;
  const width = laid.bounds.maxX - laid.bounds.minX;
  const height = laid.bounds.maxY - laid.bounds.minY;

  const toggle = <T,>(set: ReadonlySet<T>, value: T): ReadonlySet<T> => {
    const next = new Set(set);
    if (next.has(value)) next.delete(value);
    else next.add(value);
    return next;
  };

  const nodeName = (id: string) => nodes.find((n) => n.id === id)?.name ?? "";

  return (
    <div className="gx-kg-graph">
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

      {laid.nodes.length === 0 ? (
        <p className="gx-kg-empty">
          Nothing to draw — every entry is filtered out. Turn a type chip back on.
        </p>
      ) : (
        <svg
          className="gx-kg-graph__canvas"
          role="img"
          aria-label={`Knowledge graph: ${laid.nodes.length} entries, ${laid.edges.length} relationships`}
          viewBox={`${laid.bounds.minX} ${laid.bounds.minY} ${width} ${height}`}
          // A release outside any node abandons the gesture rather than leaving a
          // half-started link armed forever.
          onMouseUp={() => setDragFrom(null)}
          onMouseLeave={() => {
            setDragFrom(null);
            setHoverID(null);
          }}
        >
          <g className="gx-kg-graph__edges">
            {laid.edges.map((e) => (
              <line
                key={e.edge.id}
                className="gx-kg-graph__edge"
                data-relation={EDGE_LABEL.get(e.edge.edgeType) ?? ""}
                x1={e.x1}
                y1={e.y1}
                x2={e.x2}
                y2={e.y2}
              />
            ))}
          </g>
          <g className="gx-kg-graph__nodes">
            {laid.nodes.map((p) => {
              const meta = metaOf(p.node.nodeType);
              const labelled = showAllLabels || hoverID === p.node.id || selectedID === p.node.id;
              return (
                <g
                  key={p.node.id}
                  className="gx-kg-graph__node"
                  data-node-id={p.node.id}
                  data-private={p.node.gmPrivate || undefined}
                  data-selected={selectedID === p.node.id || undefined}
                  role="button"
                  tabIndex={0}
                  aria-label={`${p.node.name} (${meta.label})${p.node.gmPrivate ? ", GM private" : ""}`}
                  transform={`translate(${p.x} ${p.y})`}
                  onMouseDown={() => setDragFrom(p.node.id)}
                  onMouseEnter={() => setHoverID(p.node.id)}
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
                  <circle
                    r={LAYOUT.nodeRadius}
                    fill={alphaBg(meta.color)}
                    stroke={meta.color}
                    // gm_private is drawn dashed: present in the GM's map, visibly
                    // absent from the world their NPCs see.
                    strokeDasharray={p.node.gmPrivate ? "3 2" : undefined}
                  />
                  {labelled && (
                    <text className="gx-kg-graph__label" x={LAYOUT.nodeRadius + 4} y={4}>
                      {p.node.name}
                    </text>
                  )}
                </g>
              );
            })}
          </g>
        </svg>
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
