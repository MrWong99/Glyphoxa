import { useEffect, useMemo, useState } from "react";
import { useQuery, useMutation } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { ArrowRight, ArrowLeft, Pencil, X, Plus, Link as LinkIcon } from "lucide-react";

import { CampaignService, EdgeType, NodeType } from "@gen/glyphoxa/management/v1/management_pb";
import type { Node as PbNode, Edge as PbEdge } from "@gen/glyphoxa/management/v1/management_pb";
import { useI18n } from "@/i18n";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Input";
import { Select } from "@/components/ui/Select";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import {
  MAX_EDGE_NOTE_RUNES,
  TYPE_META as NODE_TYPE_META,
  dispositionOptions,
  edgeLabel,
  edgeOptions,
} from "./knowledgeVocab";
import { invalidateKnowledgeReads } from "./knowledgeCache";
import { errorMessage } from "@/lib/connectError";

function typeMeta(t: NodeType) {
  return NODE_TYPE_META[t] ?? NODE_TYPE_META[NodeType.NOTE];
}

// NodeRelations (#132) is the editor card's "Connections · N" section on the live
// CampaignService edge RPCs (ADR-0008 v1.0 + 2026-07-04 amendment). Edges are
// strictly one-way, so outgoing and incoming are listed SEPARATELY: outgoing are
// editable here; incoming are shown dimmed for context and edited from the other
// entry. An NPC Node also carries the optional "Voiced by" Character NPC Agent
// link. Kept a self-contained component so it slots into the EntryEditor without
// entangling the #129 authoring code.

// AGENT_NONE is the Radix Select sentinel for "no agent" — Radix forbids an empty
// item value, so the unlink option carries this and maps back to "" on the wire.
const AGENT_NONE = "__none__";

export function NodeRelations({ node }: { node: PbNode }) {
  const { t } = useI18n();
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
      { value: AGENT_NONE, label: t("knowledge.noneOption") },
      ...cast.map((a) => ({ value: a.id, label: a.name })),
    ];
  }, [rosterQuery.data, t]);

  // Creating or deleting an Edge here changes what the GRAPH draws too (#534), so
  // this drops every read of the same data rather than just this node's edges.
  const invalidateEdges = () => invalidateKnowledgeReads(queryClient);
  const invalidateNodes = () => invalidateKnowledgeReads(queryClient);

  const [adding, setAdding] = useState(false);
  const [relType, setRelType] = useState<string>("");
  const [target, setTarget] = useState<string>("");
  // The outgoing edge a delete has been requested for; drives the confirm dialog
  // so no DeleteEdge fires on a single click (#209).
  const [confirmEdge, setConfirmEdge] = useState<PbEdge | null>(null);

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
  // The relation's texture (#546). It shares the edge invalidation because the
  // graph colours relations by disposition and shows the note on hover.
  //
  // savedEdgeID is what closes the inline editor: it must close on the SAVE
  // LANDING, not on the click. Closing on click hid every rejection — an
  // over-long note came back InvalidArgument and the GM saw a tidy closed row.
  const [savedEdgeID, setSavedEdgeID] = useState<string | null>(null);
  const updateDetails = useMutation(CampaignService.method.updateEdgeDetails, {
    onSuccess: (_res, vars) => {
      setSavedEdgeID(vars.id ?? null);
      void invalidateEdges();
    },
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
        <h3 className="gx-overline">{t("knowledge.connectionsHeading", { n: count })}</h3>
        <Button
          variant="ghost"
          iconStart={<Plus size={13} />}
          onClick={() => setAdding((a) => !a)}
        >
          {t("knowledge.addConnection")}
        </Button>
      </div>

      {adding && (
        <div className="gx-kg-relations__add">
          <div className="gx-kg-relations__addgrid">
            <Select
              label={t("knowledge.connectionLabel")}
              options={edgeOptions(t)}
              value={relType}
              onValueChange={setRelType}
              placeholder={t("knowledge.connectionPlaceholder")}
            />
            <Select
              label={t("knowledge.targetEntryLabel")}
              options={targetOptions}
              value={target}
              onValueChange={setTarget}
              placeholder={t("knowledge.targetEntryPlaceholder")}
            />
          </div>
          <span className="gx-field__hint">{t("knowledge.connectionTypedHint")}</span>
          <div className="gx-kg-relations__addactions">
            <Button
              variant="primary"
              onClick={submitEdge}
              disabled={relType === "" || target === "" || createEdge.isPending}
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
      )}

      <section className="gx-kg-relations__list" aria-label={t("knowledge.outgoingConnectionsAria")}>
        {outgoing.map((e) => (
          <OutgoingRow
            key={e.id}
            edge={e}
            onDelete={() => setConfirmEdge(e)}
            onSaveDetails={(note, disposition) =>
              updateDetails.mutate({ id: e.id, note, disposition })
            }
            saving={updateDetails.isPending && updateDetails.variables?.id === e.id}
            saveError={
              updateDetails.isError && updateDetails.variables?.id === e.id
                ? t("common.couldntSave", { message: errorMessage(updateDetails.error) })
                : null
            }
            saved={savedEdgeID === e.id}
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
            <IncomingRow key={e.id} edge={e} />
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
                {edgeLabel(t, confirmEdge.edgeType)} → {confirmEdge.toNodeName}
              </strong>
              {t("knowledge.deleteConnectionAfter")}
            </>
          }
          confirmLabel={t("knowledge.deleteConnection")}
          onConfirm={() => {
            deleteEdge.mutate({ id: confirmEdge.id });
            setConfirmEdge(null);
          }}
        />
      )}
    </div>
  );
}

function OutgoingRow({
  edge,
  onDelete,
  onSaveDetails,
  saving,
  saveError,
  saved,
}: {
  edge: PbEdge;
  onDelete: () => void;
  onSaveDetails: (note: string, disposition: number) => void;
  saving: boolean;
  saveError: string | null;
  saved: boolean;
}) {
  const { t } = useI18n();
  const meta = typeMeta(edge.toNodeType);
  const label = edgeLabel(t, edge.edgeType);
  const [open, setOpen] = useState(false);
  const [note, setNote] = useState(edge.note);
  const [disposition, setDisposition] = useState(String(edge.disposition));
  useEffect(() => {
    if (saved) setOpen(false);
  }, [saved]);
  return (
    <div className="gx-kg-edge">
      <ArrowRight size={13} className="gx-kg-edge__dir" aria-hidden />
      <span className="gx-kg-edge__type">{label}</span>
      <span className="gx-kg-edge__target">{edge.toNodeName}</span>
      <Badge size="sm" style={{ color: meta.color, background: `${meta.color}24` }}>
        {t(meta.labelKey)}
      </Badge>
      {/* The relation's texture, collapsed. "Knows and despises" is the
          difference between a flat NPC and a live one — but most relations are
          plain, so it stays out of the way until asked for. */}
      <button
        type="button"
        className="gx-kg-iconbtn"
        aria-label={t("knowledge.editConnectionAria", { relation: label, target: edge.toNodeName })}
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        <Pencil size={13} />
      </button>
      <button
        type="button"
        className="gx-kg-iconbtn gx-kg-iconbtn--danger"
        aria-label={t("knowledge.deleteConnectionAria", { relation: label, target: edge.toNodeName })}
        onClick={onDelete}
      >
        <X size={13} />
      </button>

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

function IncomingRow({ edge }: { edge: PbEdge }) {
  const { t } = useI18n();
  const meta = typeMeta(edge.fromNodeType);
  const label = edgeLabel(t, edge.edgeType);
  return (
    <div className="gx-kg-edge gx-kg-edge--incoming">
      <ArrowLeft size={13} className="gx-kg-edge__dir" aria-hidden />
      <span className="gx-kg-edge__type">{label}</span>
      <span className="gx-kg-edge__target">{edge.fromNodeName}</span>
      <Badge size="sm" style={{ color: meta.color, background: `${meta.color}24` }}>
        {t(meta.labelKey)}
      </Badge>
      <span className="gx-kg-edge__incoming">
        <LinkIcon size={11} /> {t("knowledge.incomingTag")}
      </span>
    </div>
  );
}
