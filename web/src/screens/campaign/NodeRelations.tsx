import { useEffect, useMemo, useState } from "react";
import { useQuery, useMutation } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ArrowRight, ArrowLeft, Pencil, X, Plus } from "lucide-react";

import { CampaignService, EdgeType, NodeType } from "@gen/glyphoxa/management/v1/management_pb";
import type { Node as PbNode, Edge as PbEdge } from "@gen/glyphoxa/management/v1/management_pb";
import { useI18n } from "@/i18n";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Combobox } from "@/components/ui/Combobox";
import { IconButton } from "@/components/ui/IconButton";
import { Input } from "@/components/ui/Input";
import { Select } from "@/components/ui/Select";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import {
  EDGE_TYPES,
  MAX_EDGE_NOTE_RUNES,
  alphaBg,
  dispositionOptions,
  edgeLabel,
  isEdgeLegal,
  metaOf,
} from "./knowledgeVocab";
import { invalidateKnowledgeReads } from "./knowledgeCache";
import { errorMessage } from "@/lib/connectError";

// NodeRelations (#132) is the editor's "Connections · N" section on the live
// CampaignService edge RPCs (ADR-0008 v1.0 + 2026-07-04 amendment). Edges are
// strictly one-way, so outgoing and incoming are listed SEPARATELY: an outgoing
// one is authored here (its relation, its target, and what it is like); an
// incoming one belongs to the other entry, is shown for context, and can be
// removed from here because that is where the GM notices it is wrong. Every
// name is a link to that entry. An NPC entry also carries the optional "Voiced
// by" link to a cast NPC.
//
// The add form only offers what the server will accept. The validity matrix
// (isEdgeLegal, mirroring storage.ValidateEdge) used to be enforced by letting
// the GM pick any pair and answering "invalid edge"; now a chosen relation
// narrows the targets ("lives in" lists only Locations) and a chosen target
// narrows the relations.

// AGENT_NONE is the Radix Select sentinel for "no agent": Radix forbids an empty
// item value, so the unlink option carries this and maps back to "" on the wire.
const AGENT_NONE = "__none__";

export function NodeRelations({
  node,
  onOpenNode,
}: {
  node: PbNode;
  /** Opens another entry; without it the names are plain text. */
  onOpenNode?: (nodeID: string) => void;
}) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const isNPC = node.nodeType === NodeType.NPC;

  const edgesQuery = useQuery(CampaignService.method.listNodeEdges, { nodeId: node.id });
  const outgoing = useMemo(() => edgesQuery.data?.outgoing ?? [], [edgesQuery.data]);
  const incoming = useMemo(() => edgesQuery.data?.incoming ?? [], [edgesQuery.data]);

  // Every other entry in the campaign (a self-edge is rejected server-side, so
  // it is never offered).
  const nodesQuery = useQuery(CampaignService.method.listNodes, {});
  const others = useMemo(
    () => (nodesQuery.data?.nodes ?? []).filter((n) => n.id !== node.id),
    [nodesQuery.data, node.id],
  );

  // The "Voiced by" roster is the campaign's Character NPC agents (never the
  // Butler). Only queried for an NPC node: it is the only type that can link.
  const rosterQuery = useQuery(CampaignService.method.getCampaignRoster, {}, { enabled: isNPC });
  const castOptions = useMemo(() => {
    const cast = (rosterQuery.data?.roster ?? []).filter((a) => a.role === "character");
    return [
      { value: AGENT_NONE, label: t("knowledge.noneOption") },
      ...cast.map((a) => ({ value: a.id, label: a.name })),
    ];
  }, [rosterQuery.data, t]);

  // Creating or deleting an Edge here changes what the GRAPH draws too (#534), so
  // this drops every read of the same data rather than just this node's edges.
  const invalidate = () => invalidateKnowledgeReads(queryClient);

  const [adding, setAdding] = useState(false);
  const [relType, setRelType] = useState<string>("");
  const [target, setTarget] = useState<string>("");
  const [newNote, setNewNote] = useState("");
  const [newDisposition, setNewDisposition] = useState("0");
  // The edge a delete has been requested for; drives the confirm dialog so no
  // DeleteEdge fires on a single click (#209).
  const [confirmEdge, setConfirmEdge] = useState<{ edge: PbEdge; direction: "out" | "in" } | null>(null);

  // The linked agent is held locally so the select reflects a fresh choice at
  // once, without waiting for the list refetch to hand down a new `node`.
  const [linkedAgentId, setLinkedAgentId] = useState(node.agentId);

  const createEdge = useMutation(CampaignService.method.createEdge);
  // The texture given while adding is a second write (CreateEdge carries only
  // the relation). It has its own mutation so a failure here is never shown on
  // some row's inline editor.
  const addDetails = useMutation(CampaignService.method.updateEdgeDetails);
  const deleteEdge = useMutation(CampaignService.method.deleteEdge, {
    onSuccess: () => void invalidate(),
  });
  // The relation's texture (#546). It shares the edge invalidation because the
  // graph colours relations by disposition and shows the note on hover.
  //
  // savedEdge is what closes the inline editor: it must close on the SAVE
  // LANDING, not on the click. Closing on click hid every rejection. It is a
  // per-save TOKEN (id + sequence), not just the id: keyed on the id alone, a
  // second edit of the same relation saved fine and then never closed.
  const [savedEdge, setSavedEdge] = useState<{ id: string; seq: number } | null>(null);
  const updateDetails = useMutation(CampaignService.method.updateEdgeDetails, {
    onSuccess: (_res, vars) => {
      setSavedEdge((prev) => ({ id: vars.id ?? "", seq: (prev?.seq ?? 0) + 1 }));
      void invalidate();
    },
  });
  const setNodeAgent = useMutation(CampaignService.method.setNodeAgent, {
    onSuccess: (res) => {
      setLinkedAgentId(res.node?.agentId ?? "");
      void invalidate();
    },
  });

  // What the two pickers offer. A relation is only listed when at least one
  // entry could be its target (no "parent of" on a Location). With no relation
  // chosen yet, a chosen target narrows the relations; once a relation is
  // chosen it stays switchable, and switching to one the current target cannot
  // take clears the target instead of trapping the GM in the first choice.
  const targetNode = others.find((n) => n.id === target);
  const relationOptions = useMemo(() => {
    const possible = EDGE_TYPES.filter((e) =>
      others.some((n) => isEdgeLegal(e.value, node.nodeType, n.nodeType)),
    );
    const narrowed =
      relType === "" && targetNode
        ? possible.filter((e) => isEdgeLegal(e.value, node.nodeType, targetNode.nodeType))
        : possible;
    return narrowed.map((e) => ({ value: String(e.value), label: t(e.labelKey) }));
  }, [others, node.nodeType, relType, targetNode, t]);
  const targetOptions = useMemo(
    () =>
      others
        .filter(
          (n) => relType === "" || isEdgeLegal(Number(relType) as EdgeType, node.nodeType, n.nodeType),
        )
        // The type rides in the label: it tells two same-named entries apart,
        // and typing "location" in the picker's search narrows to locations.
        .map((n) => ({ value: n.id, label: `${n.name} · ${t(metaOf(n.nodeType).labelKey)}` })),
    [others, relType, node.nodeType, t],
  );

  const pickRelation = (value: string) => {
    setRelType(value);
    if (targetNode && !isEdgeLegal(Number(value) as EdgeType, node.nodeType, targetNode.nodeType)) {
      setTarget("");
    }
  };

  const resetAddForm = () => {
    setRelType("");
    setTarget("");
    setNewNote("");
    setNewDisposition("0");
  };

  const submitEdge = async () => {
    if (relType === "" || target === "" || createEdge.isPending) return;
    const note = newNote.trim();
    const disposition = Number(newDisposition);
    let created;
    try {
      created = await createEdge.mutateAsync({
        fromNodeId: node.id,
        toNodeId: target,
        edgeType: Number(relType) as EdgeType,
      });
    } catch {
      return; // createEdge.isError renders under the form, which stays open
    }
    setAdding(false);
    resetAddForm();
    if (created.edge && (note !== "" || disposition !== 0)) {
      try {
        await addDetails.mutateAsync({ id: created.edge.id, note, disposition });
      } catch (err) {
        // The connection exists; only its texture did not land. Say exactly
        // that, so the GM adds it on the row instead of creating a duplicate.
        toast.error(t("knowledge.connectionDetailsError", { message: errorMessage(err) }));
      }
    }
    void invalidate();
  };

  const count = outgoing.length + incoming.length;
  const busyAdding = createEdge.isPending || addDetails.isPending;

  return (
    <div className="gx-kg-relations">
      {isNPC && (
        <div className="gx-field gx-kg-voicedby">
          <Select
            label={t("knowledge.voicedByLabel")}
            options={castOptions}
            value={linkedAgentId ? linkedAgentId : AGENT_NONE}
            onValueChange={(v) =>
              setNodeAgent.mutate({ nodeId: node.id, agentId: v === AGENT_NONE ? "" : v })
            }
            placeholder={t("knowledge.noneOption")}
          />
          <span className="gx-field__hint">{t("knowledge.voicedByHint")}</span>
          {setNodeAgent.isError && (
            <span className="gx-editor__status gx-editor__status--error" role="alert">
              {t("knowledge.linkAgentError", { message: errorMessage(setNodeAgent.error) })}
            </span>
          )}
        </div>
      )}

      <div className="gx-kg-relations__bar">
        <h4 className="gx-overline">{t("knowledge.connectionsHeading", { n: count })}</h4>
        <Button
          variant="ghost"
          size="sm"
          iconStart={<Plus size={13} />}
          aria-expanded={adding}
          onClick={() => {
            // A failed add belongs to the form that made it: reopening the
            // form must not greet the GM with the previous attempt's error.
            createEdge.reset();
            setAdding((a) => !a);
          }}
        >
          {t("knowledge.addConnection")}
        </Button>
      </div>

      {adding &&
        (others.length === 0 ? (
          <p className="gx-kg-relations__empty">{t("knowledge.noOtherEntries")}</p>
        ) : (
          <div className="gx-kg-relations__add">
            <div className="gx-kg-relations__addgrid">
              <Select
                label={t("knowledge.connectionLabel")}
                options={relationOptions}
                value={relType}
                onValueChange={pickRelation}
                placeholder={t("knowledge.connectionPlaceholder")}
              />
              <Combobox
                label={t("knowledge.targetEntryLabel")}
                options={targetOptions}
                value={target}
                onValueChange={setTarget}
                placeholder={t("knowledge.targetEntryPlaceholder")}
                searchPlaceholder={t("knowledge.targetSearchPlaceholder")}
                emptyText={
                  relType === ""
                    ? undefined
                    : t("knowledge.noLegalTargets", { relation: edgeLabel(t, Number(relType) as EdgeType) })
                }
              />
            </div>
            <span className="gx-field__hint">{t("knowledge.connectionTypedHint")}</span>
            <div className="gx-kg-relations__addgrid">
              <Input
                label={t("knowledge.connectionNoteOptionalLabel")}
                placeholder={t("knowledge.connectionNotePlaceholder")}
                value={newNote}
                maxLength={MAX_EDGE_NOTE_RUNES}
                onChange={(e) => setNewNote(e.target.value)}
              />
              <Select
                label={t("knowledge.dispositionSelectLabel")}
                options={dispositionOptions(t)}
                value={newDisposition}
                onValueChange={setNewDisposition}
              />
            </div>
            <div className="gx-kg-relations__addactions">
              <Button
                variant="primary"
                size="sm"
                onClick={() => void submitEdge()}
                disabled={relType === "" || target === "" || busyAdding}
              >
                {t("knowledge.add")}
              </Button>
              {createEdge.isError && (
                <span className="gx-editor__status gx-editor__status--error" role="alert">
                  {t("knowledge.addConnectionError", { message: errorMessage(createEdge.error) })}
                </span>
              )}
            </div>
          </div>
        ))}

      <section className="gx-kg-relations__list" aria-label={t("knowledge.outgoingConnectionsAria")}>
        {outgoing.map((e) => (
          <OutgoingRow
            key={e.id}
            edge={e}
            onOpenNode={onOpenNode}
            onDelete={() => {
              // Asking about another relation retires the last delete's error.
              deleteEdge.reset();
              setConfirmEdge({ edge: e, direction: "out" });
            }}
            onToggle={() => {
              // Reopening a row's editor starts clean, not on its last failure.
              if (updateDetails.variables?.id === e.id) updateDetails.reset();
            }}
            onSaveDetails={(note, disposition) => updateDetails.mutate({ id: e.id, note, disposition })}
            saving={updateDetails.isPending && updateDetails.variables?.id === e.id}
            saveError={
              updateDetails.isError && updateDetails.variables?.id === e.id
                ? t("common.couldntSave", { message: errorMessage(updateDetails.error) })
                : null
            }
            savedSeq={savedEdge?.id === e.id ? savedEdge.seq : 0}
          />
        ))}
        {outgoing.length === 0 && (
          <p className="gx-kg-relations__empty">{t("knowledge.noOutgoingConnections")}</p>
        )}
        {deleteEdge.isError && (
          <span className="gx-editor__status gx-editor__status--error" role="alert">
            {t("knowledge.deleteConnectionError", { message: errorMessage(deleteEdge.error) })}
          </span>
        )}
      </section>

      {incoming.length > 0 && (
        <section
          className="gx-kg-relations__list gx-kg-relations__list--incoming"
          aria-label={t("knowledge.incomingConnectionsAria")}
        >
          {incoming.map((e) => (
            <IncomingRow
              key={e.id}
              edge={e}
              onOpenNode={onOpenNode}
              onDelete={() => {
                deleteEdge.reset();
                setConfirmEdge({ edge: e, direction: "in" });
              }}
            />
          ))}
          <p className="gx-kg-relations__hint">{t("knowledge.oneWayHint")}</p>
        </section>
      )}

      {confirmEdge && (
        <ConfirmDialog
          open
          onOpenChange={(open) => {
            if (!open) setConfirmEdge(null);
          }}
          title={t("knowledge.deleteConnectionTitle")}
          description={
            <>
              {t("knowledge.deleteConnectionBefore")}
              <strong>
                {confirmEdge.direction === "out"
                  ? `${edgeLabel(t, confirmEdge.edge.edgeType)} → ${confirmEdge.edge.toNodeName}`
                  : `${confirmEdge.edge.fromNodeName} → ${edgeLabel(t, confirmEdge.edge.edgeType)}`}
              </strong>
              {t("knowledge.deleteConnectionAfter")}
            </>
          }
          confirmLabel={t("knowledge.deleteConnection")}
          onConfirm={() => {
            deleteEdge.mutate({ id: confirmEdge.edge.id });
            setConfirmEdge(null);
          }}
        />
      )}
    </div>
  );
}

// EntryName is the other end of a relation: a link that opens that entry where
// the surface can navigate, plain text where it cannot.
function EntryName({
  id,
  name,
  onOpenNode,
}: {
  id: string;
  name: string;
  onOpenNode?: (nodeID: string) => void;
}) {
  if (!onOpenNode) return <span className="gx-kg-edge__target">{name}</span>;
  return (
    <button type="button" className="gx-kg-edge__target gx-wiki-link" onClick={() => onOpenNode(id)}>
      {name}
    </button>
  );
}

function OutgoingRow({
  edge,
  onOpenNode,
  onDelete,
  onToggle,
  onSaveDetails,
  saving,
  saveError,
  savedSeq,
}: {
  edge: PbEdge;
  onOpenNode?: (nodeID: string) => void;
  onDelete: () => void;
  onToggle: () => void;
  onSaveDetails: (note: string, disposition: number) => void;
  saving: boolean;
  saveError: string | null;
  /** Steps on every save of THIS relation that lands; 0 = none yet. */
  savedSeq: number;
}) {
  const { t } = useI18n();
  const meta = metaOf(edge.toNodeType);
  const label = edgeLabel(t, edge.edgeType);
  const [open, setOpen] = useState(false);
  const [note, setNote] = useState(edge.note);
  const [disposition, setDisposition] = useState(String(edge.disposition));
  useEffect(() => {
    if (savedSeq > 0) setOpen(false);
  }, [savedSeq]);
  return (
    <div className="gx-kg-edge">
      <ArrowRight size={13} className="gx-kg-edge__dir" aria-hidden />
      <span className="gx-kg-edge__type">{label}</span>
      <EntryName id={edge.toNodeId} name={edge.toNodeName} onOpenNode={onOpenNode} />
      <Badge size="sm" style={{ color: meta.color, background: alphaBg(meta.color) }}>
        {t(meta.labelKey)}
      </Badge>
      {/* The relation's texture, collapsed. "Knows and despises" is the
          difference between a flat NPC and a live one, but most relations are
          plain, so it stays out of the way until asked for. */}
      <IconButton
        size="sm"
        label={t("knowledge.editConnectionAria", { relation: label, target: edge.toNodeName })}
        aria-expanded={open}
        onClick={() => {
          onToggle();
          setOpen((v) => !v);
        }}
      >
        <Pencil size={13} />
      </IconButton>
      <IconButton
        size="sm"
        tone="danger"
        label={t("knowledge.deleteConnectionAria", { relation: label, target: edge.toNodeName })}
        onClick={onDelete}
      >
        <X size={13} />
      </IconButton>

      {open && (
        <div className="gx-kg-edge__details">
          <Input
            label={t("knowledge.connectionNoteLabel")}
            placeholder={t("knowledge.connectionNotePlaceholder")}
            value={note}
            maxLength={MAX_EDGE_NOTE_RUNES}
            onChange={(e) => setNote(e.target.value)}
          />
          <Select
            label={t("knowledge.dispositionSelectLabel")}
            options={dispositionOptions(t)}
            value={disposition}
            onValueChange={setDisposition}
          />
          <Button
            variant="secondary"
            size="sm"
            disabled={saving}
            onClick={() => onSaveDetails(note.trim(), Number(disposition))}
          >
            {saving ? t("common.saving") : t("common.save")}
          </Button>
          {/* The editor stays OPEN until the save actually lands. Closing on click
              and never rendering the failure told the GM their note was saved when
              the server had rejected it. */}
          {saveError && (
            <span className="gx-editor__status gx-editor__status--error" role="alert">
              {saveError}
            </span>
          )}
        </div>
      )}
    </div>
  );
}

function IncomingRow({
  edge,
  onOpenNode,
  onDelete,
}: {
  edge: PbEdge;
  onOpenNode?: (nodeID: string) => void;
  onDelete: () => void;
}) {
  const { t } = useI18n();
  const meta = metaOf(edge.fromNodeType);
  const label = edgeLabel(t, edge.edgeType);
  return (
    <div className="gx-kg-edge gx-kg-edge--incoming">
      <ArrowLeft size={13} className="gx-kg-edge__dir" aria-hidden />
      <EntryName id={edge.fromNodeId} name={edge.fromNodeName} onOpenNode={onOpenNode} />
      <span className="gx-kg-edge__type">{label}</span>
      <Badge size="sm" style={{ color: meta.color, background: alphaBg(meta.color) }}>
        {t(meta.labelKey)}
      </Badge>
      <IconButton
        size="sm"
        tone="danger"
        label={t("knowledge.deleteIncomingConnectionAria", { source: edge.fromNodeName, relation: label })}
        onClick={onDelete}
      >
        <X size={13} />
      </IconButton>
    </div>
  );
}
