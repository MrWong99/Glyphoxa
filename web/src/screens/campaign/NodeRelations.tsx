import { useMemo, useState } from "react";
import { useQuery, useMutation, createConnectQueryKey } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { ArrowRight, ArrowLeft, X, Plus, Link as LinkIcon } from "lucide-react";

import { CampaignService, EdgeType, NodeType } from "@gen/glyphoxa/management/v1/management_pb";
import type { Node as PbNode, Edge as PbEdge } from "@gen/glyphoxa/management/v1/management_pb";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Select } from "@/components/ui/Select";

// NodeRelations (#132) is the editor card's "Relations · N" section on the live
// CampaignService edge RPCs (ADR-0008 v1.0 + 2026-07-04 amendment). Edges are
// strictly one-way, so outgoing and incoming are listed SEPARATELY: outgoing are
// editable here; incoming are shown dimmed for context and edited from the other
// entry. An NPC Node also carries the optional "Voiced by" Character NPC Agent
// link. Kept a self-contained component so it slots into the EntryEditor without
// entangling the #129 authoring code.

// The nine edge types in enum order. The label is the snake_case type itself —
// rendered in a mono arcane font in the row, matching the approved design.
const EDGE_TYPES: { value: EdgeType; label: string }[] = [
  { value: EdgeType.RESIDES_IN, label: "resides_in" },
  { value: EdgeType.MEMBER_OF, label: "member_of" },
  { value: EdgeType.OWNS, label: "owns" },
  { value: EdgeType.KNOWS, label: "knows" },
  { value: EdgeType.ENEMY_OF, label: "enemy_of" },
  { value: EdgeType.ALLY_OF, label: "ally_of" },
  { value: EdgeType.PARENT_OF, label: "parent_of" },
  { value: EdgeType.PARTICIPATED_IN, label: "participated_in" },
  { value: EdgeType.MENTIONED_IN, label: "mentioned_in" },
];
const EDGE_LABEL = new Map<EdgeType, string>(EDGE_TYPES.map((e) => [e.value, e.label]));
const EDGE_OPTIONS = EDGE_TYPES.map((e) => ({ value: String(e.value), label: e.label }));

// Per-type badge colors (approved design), with the Note grey as the fallback.
const NODE_TYPE_META: Record<number, { label: string; color: string }> = {
  [NodeType.CHARACTER]: { label: "Character", color: "#4fa9ff" },
  [NodeType.NPC]: { label: "NPC", color: "#9059ff" },
  [NodeType.LOCATION]: { label: "Location", color: "#35c48d" },
  [NodeType.FACTION]: { label: "Faction", color: "#ffbd4f" },
  [NodeType.ITEM]: { label: "Item", color: "#ff7139" },
  [NodeType.PLOT_THREAD]: { label: "Plot thread", color: "#ff4f5e" },
  [NodeType.NOTE]: { label: "Note", color: "#8b93a7" },
};
function typeMeta(t: NodeType) {
  return NODE_TYPE_META[t] ?? NODE_TYPE_META[NodeType.NOTE];
}

// AGENT_NONE is the Radix Select sentinel for "no agent" — Radix forbids an empty
// item value, so the unlink option carries this and maps back to "" on the wire.
const AGENT_NONE = "__none__";

export function NodeRelations({ node }: { node: PbNode }) {
  const queryClient = useQueryClient();
  const isNPC = node.nodeType === NodeType.NPC;

  const edgesQuery = useQuery(CampaignService.method.listNodeEdges, { nodeId: node.id });
  const outgoing = useMemo(() => edgesQuery.data?.outgoing ?? [], [edgesQuery.data]);
  const incoming = useMemo(() => edgesQuery.data?.incoming ?? [], [edgesQuery.data]);

  // Target-entry options: every other entry in the campaign (a self-edge is
  // rejected server-side, so it is never offered).
  const nodesQuery = useQuery(CampaignService.method.listNodes, {});
  const targetOptions = useMemo(
    () =>
      (nodesQuery.data?.nodes ?? [])
        .filter((n) => n.id !== node.id)
        .map((n) => ({ value: n.id, label: n.name })),
    [nodesQuery.data, node.id],
  );

  // The "Voiced by" roster is the campaign's Character NPC agents (never the
  // Butler). Only queried for an NPC node — it is the only type that can link.
  const rosterQuery = useQuery(CampaignService.method.getCampaignRoster, {}, { enabled: isNPC });
  const castOptions = useMemo(() => {
    const cast = (rosterQuery.data?.roster ?? []).filter((a) => a.role === "character");
    return [
      { value: AGENT_NONE, label: "— None —" },
      ...cast.map((a) => ({ value: a.id, label: a.name })),
    ];
  }, [rosterQuery.data]);

  const invalidateEdges = () =>
    queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: CampaignService.method.listNodeEdges,
        cardinality: "finite",
      }),
    });
  const invalidateNodes = () =>
    queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: CampaignService.method.listNodes,
        cardinality: "finite",
      }),
    });

  const [adding, setAdding] = useState(false);
  const [relType, setRelType] = useState<string>("");
  const [target, setTarget] = useState<string>("");

  // The linked agent is held locally so the select reflects a fresh choice: the
  // `node` prop is a snapshot of the editor's `editing` state that setNodeAgent
  // does not update, so a controlled value={node.agentId} would keep showing the
  // stale link. Seeded from the prop (NodeRelations remounts per edited node) and
  // refreshed from the SetNodeAgentResponse on success.
  const [linkedAgentId, setLinkedAgentId] = useState(node.agentId);

  const createEdge = useMutation(CampaignService.method.createEdge, {
    onSuccess: () => {
      setAdding(false);
      setRelType("");
      setTarget("");
      void invalidateEdges();
    },
  });
  const deleteEdge = useMutation(CampaignService.method.deleteEdge, {
    onSuccess: () => void invalidateEdges(),
  });
  const setNodeAgent = useMutation(CampaignService.method.setNodeAgent, {
    onSuccess: (res) => {
      setLinkedAgentId(res.node?.agentId ?? "");
      void invalidateNodes();
    },
  });

  const submitEdge = () => {
    if (relType === "" || target === "") return;
    createEdge.mutate({ fromNodeId: node.id, toNodeId: target, edgeType: Number(relType) as EdgeType });
  };

  const count = outgoing.length + incoming.length;

  return (
    <div className="gx-kg-relations">
      {isNPC && (
        <div className="gx-field gx-kg-voicedby">
          <Select
            label="Voiced by"
            options={castOptions}
            value={linkedAgentId ? linkedAgentId : AGENT_NONE}
            onValueChange={(v) =>
              setNodeAgent.mutate({ nodeId: node.id, agentId: v === AGENT_NONE ? "" : v })
            }
            placeholder="— None —"
          />
          <span className="gx-field__hint">
            Optional. Links this entry to a cast NPC — it then knows its own entry and its relations.
          </span>
          {setNodeAgent.isError && (
            <span className="gx-editor__status gx-editor__status--error" role="alert">
              Couldn't link agent: {setNodeAgent.error.message}
            </span>
          )}
        </div>
      )}

      <div className="gx-kg-relations__bar">
        <h3 className="gx-overline">Relations · {count}</h3>
        <Button
          variant="ghost"
          iconStart={<Plus size={13} />}
          onClick={() => setAdding((a) => !a)}
        >
          Add relation
        </Button>
      </div>

      {adding && (
        <div className="gx-kg-relations__add">
          <div className="gx-kg-relations__addgrid">
            <Select
              label="Relation"
              options={EDGE_OPTIONS}
              value={relType}
              onValueChange={setRelType}
              placeholder="Relation…"
            />
            <Select
              label="Target entry"
              options={targetOptions}
              value={target}
              onValueChange={setTarget}
              placeholder="Which entry?"
            />
          </div>
          <span className="gx-field__hint">
            Relations are typed: resides_in points at a Location, member_of at a Faction.
          </span>
          <div className="gx-kg-relations__addactions">
            <Button
              variant="primary"
              onClick={submitEdge}
              disabled={relType === "" || target === "" || createEdge.isPending}
            >
              Add
            </Button>
            {createEdge.isError && (
              <span className="gx-editor__status gx-editor__status--error" role="alert">
                Couldn't add: {createEdge.error.message}
              </span>
            )}
          </div>
        </div>
      )}

      <section className="gx-kg-relations__list" aria-label="Outgoing relations">
        {outgoing.map((e) => (
          <OutgoingRow key={e.id} edge={e} onDelete={() => deleteEdge.mutate({ id: e.id })} />
        ))}
        {outgoing.length === 0 && <p className="gx-kg-relations__empty">No outgoing relations yet.</p>}
        {deleteEdge.isError && (
          <span className="gx-editor__status gx-editor__status--error" role="alert">
            Couldn't delete relation: {deleteEdge.error.message}
          </span>
        )}
      </section>

      {incoming.length > 0 && (
        <section className="gx-kg-relations__list gx-kg-relations__list--incoming" aria-label="Incoming relations">
          {incoming.map((e) => (
            <IncomingRow key={e.id} edge={e} />
          ))}
          <p className="gx-kg-relations__hint">
            Relations are one-way. Incoming ones are shown for context and edited from the other entry.
          </p>
        </section>
      )}
    </div>
  );
}

function OutgoingRow({ edge, onDelete }: { edge: PbEdge; onDelete: () => void }) {
  const meta = typeMeta(edge.toNodeType);
  const label = EDGE_LABEL.get(edge.edgeType) ?? "";
  return (
    <div className="gx-kg-edge">
      <ArrowRight size={13} className="gx-kg-edge__dir" aria-hidden />
      <span className="gx-kg-edge__type">{label}</span>
      <span className="gx-kg-edge__target">{edge.toNodeName}</span>
      <Badge size="sm" style={{ color: meta.color, background: `${meta.color}24` }}>
        {meta.label}
      </Badge>
      <button
        type="button"
        className="gx-kg-iconbtn gx-kg-iconbtn--danger"
        aria-label={`Delete relation ${label} ${edge.toNodeName}`}
        onClick={onDelete}
      >
        <X size={13} />
      </button>
    </div>
  );
}

function IncomingRow({ edge }: { edge: PbEdge }) {
  const meta = typeMeta(edge.fromNodeType);
  const label = EDGE_LABEL.get(edge.edgeType) ?? "";
  return (
    <div className="gx-kg-edge gx-kg-edge--incoming">
      <ArrowLeft size={13} className="gx-kg-edge__dir" aria-hidden />
      <span className="gx-kg-edge__type">{label}</span>
      <span className="gx-kg-edge__target">{edge.fromNodeName}</span>
      <Badge size="sm" style={{ color: meta.color, background: `${meta.color}24` }}>
        {meta.label}
      </Badge>
      <span className="gx-kg-edge__incoming">
        <LinkIcon size={11} /> incoming
      </span>
    </div>
  );
}
