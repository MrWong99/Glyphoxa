import { useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import { useQuery, useMutation, createConnectQueryKey } from "@connectrpc/connect-query";
import { useQueryClient, keepPreviousData } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  ChevronDown,
  ChevronUp,
  Eye,
  EyeOff,
  Network,
  Plus,
  Pencil,
  Rows3,
  Search,
  Sparkles,
  Trash2,
  Link as LinkIcon,
  X,
} from "lucide-react";

import { CampaignService, NodeType } from "@gen/glyphoxa/management/v1/management_pb";
import type { DraftEdge, DraftNode, Node } from "@gen/glyphoxa/management/v1/management_pb";
import { Card } from "@/components/ui/Card";
import { Badge } from "@/components/ui/Badge";
import { Input } from "@/components/ui/Input";
import { Switch } from "@/components/ui/Switch";
import { Button } from "@/components/ui/Button";
import { Select } from "@/components/ui/Select";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import { NodeRelations } from "./NodeRelations";
import { KnowledgeGraph } from "./graph/KnowledgeGraph";
import { EDGE_LABEL as EDGE_TYPE_LABEL, TYPE_META, TYPE_ORDER, alphaBg, metaOf } from "./knowledgeVocab";

// The Knowledge panel (#126, #129) backs the Campaign screen's "Knowledge" view
// on the live CampaignService node RPCs (ADR-0008 v1.0). An "entry" is the
// GM-facing name for a Node (the code keeps the Node domain term). This slice
// authors all seven Node types (type chosen once on create — immutable per
// ADR-0008), edits and deletes entries, and groups the list by type. Public
// entries are injected into NPC prompts; a gm_private entry never reaches the
// table. Type-filter chips + fulltext search arrive in #131.

const TYPE_OPTIONS = TYPE_ORDER.map((t) => ({ value: String(t), label: TYPE_META[t].label }));
const TYPE_HINT = TYPE_ORDER.map((t) => TYPE_META[t].label).join(" · ");

// ViewMode is the Knowledge tab's [ List | Graph ] switch (#534). The List mode
// is unchanged — the graph is an ADDITIONAL way to read the same wiki, not a
// replacement, and the editor rail is shared by both.
type ViewMode = "list" | "graph";

export function KnowledgePanel() {
  const queryClient = useQueryClient();
  const listQuery = useQuery(CampaignService.method.listNodes, {});
  const [editing, setEditing] = useState<Node | null>(null);
  const [mode, setMode] = useState<ViewMode>("list");

  // The whole-graph payload (#534). It is fetched only in graph mode, so a GM who
  // never opens the graph pays nothing for it.
  const graphQuery = useQuery(
    CampaignService.method.getKnowledgeGraph,
    {},
    { enabled: mode === "graph" },
  );
  // The entry a delete has been requested for; drives the confirm dialog. Delete
  // is a hard, cascading DELETE (ADR-0008), so no DeleteNode fires until the
  // operator confirms here (#209).
  const [confirmNode, setConfirmNode] = useState<Node | null>(null);

  // Live wiki search (#131, ADR-0008 tsvector): the raw box value drives a
  // 200ms-debounced SearchNodes query. The RPC runs only while the debounced
  // query is non-empty (keepPreviousData holds the last matches steady across
  // keystrokes); an empty box falls straight back to the full ListNodes list
  // with no search RPC.
  const [search, setSearch] = useState("");
  const [debounced, setDebounced] = useState("");
  useEffect(() => {
    const t = setTimeout(() => setDebounced(search), 200);
    return () => clearTimeout(t);
  }, [search]);
  const searching = debounced.trim() !== "";
  const searchQuery = useQuery(
    CampaignService.method.searchNodes,
    { query: debounced },
    // retry:false surfaces a failure promptly rather than backing off silently for
    // seconds — a typeahead re-fires on the next keystroke anyway.
    { enabled: searching, placeholderData: keepPreviousData, retry: false },
  );

  // Destructured together so the status-discriminated union still narrows `error`
  // to non-null in the error branch below.
  const { status, error } = listQuery;
  // While searching, show the matches; before the FIRST search result lands (no
  // previous data to keep) fall back to the full list so the view never flashes
  // empty. An empty match array is a real "no matches" and is shown as such.
  const nodes = useMemo(() => {
    if (!searching) return listQuery.data?.nodes ?? [];
    return searchQuery.data?.nodes ?? listQuery.data?.nodes ?? [];
  }, [searching, searchQuery.data, listQuery.data]);

  // A failed search must NOT silently fall back to the full list — the box still
  // holds a query, so an unfiltered list would look filtered and the GM could act
  // on the wrong entry. Surface the failure instead.
  const searchFailed = searching && searchQuery.isError;

  // A mutation must refresh BOTH reads: the full ListNodes list AND any active
  // SearchNodes result. Invalidating only listNodes left a stale search view — a
  // deleted/renamed entry lingered in the filtered list (second delete then
  // 404s). The searchNodes key is built without an input so it prefix-matches
  // every cached query string.
  const invalidateNodes = () => {
    void queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: CampaignService.method.listNodes,
        cardinality: "finite",
      }),
    });
    void queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: CampaignService.method.searchNodes,
        cardinality: "finite",
      }),
    });
  };

  // A node hard-delete cascades to every edge that touches it (ADR-0008 ON
  // DELETE CASCADE), so it can silently kill edges belonging to OTHER nodes.
  // Drop every cached ListNodeEdges result so the next relations view / delete
  // dialog doesn't count a dead edge (the query's staleTime would otherwise
  // serve it). No input key = prefix match across all node ids.
  const invalidateEdges = () => {
    void queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: CampaignService.method.listNodeEdges,
        cardinality: "finite",
      }),
    });
  };

  // The graph payload is a THIRD read of the same data (#534), so every mutation
  // that changes nodes or edges has to drop it too — otherwise a node created in
  // the list is missing from the graph until a reload, which reads as a bug in
  // the graph rather than a stale cache.
  const invalidateGraph = () => {
    void queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: CampaignService.method.getKnowledgeGraph,
        cardinality: "finite",
      }),
    });
  };

  const createNode = useMutation(CampaignService.method.createNode, {
    onSuccess: () => {
      invalidateNodes();
      invalidateGraph();
    },
  });
  const updateNode = useMutation(CampaignService.method.updateNode, {
    onSuccess: () => {
      setEditing(null);
      invalidateNodes();
      invalidateGraph();
    },
  });
  const deleteNode = useMutation(CampaignService.method.deleteNode, {
    onSuccess: () => {
      invalidateNodes();
      invalidateEdges();
      invalidateGraph();
    },
  });

  if (status === "pending") {
    return <div className="gx-skeleton" data-testid="kg-loading" />;
  }
  if (status === "error") {
    return (
      <p className="gx-campaign__error" role="alert">
        Could not load knowledge entries: {error.message}
      </p>
    );
  }

  // Group by type in enum order; empty groups are dropped so the list only shows
  // the types actually present.
  const groups = TYPE_ORDER.map((t) => ({
    type: t,
    items: nodes.filter((n) => n.nodeType === t),
  })).filter((g) => g.items.length > 0);

  const removeNode = (n: Node) =>
    deleteNode.mutate(
      { id: n.id },
      { onSuccess: () => setEditing((e) => (e?.id === n.id ? null : e)) },
    );

  // The editor's alert line. The active save (create or edit) takes precedence;
  // a failed delete — which otherwise leaves the button looking dead (#204) —
  // falls back into the same role=alert line so it is never swallowed.
  const saveError = editing
    ? updateNode.isError
      ? updateNode.error.message
      : null
    : createNode.isError
      ? createNode.error.message
      : null;
  const editorError = saveError
    ? `Couldn't save: ${saveError}`
    : deleteNode.isError
      ? `Couldn't delete: ${deleteNode.error.message}`
      : null;

  // Clicking a graph node opens the SAME editor the list opens (#534): the graph
  // is a navigation surface, not a second editing surface.
  const editByID = (id: string) => {
    const node = listQuery.data?.nodes.find((n) => n.id === id);
    if (node) setEditing(node);
  };

  return (
    <div className="gx-kg-layout">
      <div className="gx-kg-list">
        <div className="gx-kg-modes" role="group" aria-label="View mode">
          <button
            type="button"
            className="gx-kg-chip"
            aria-pressed={mode === "list"}
            onClick={() => setMode("list")}
          >
            <Rows3 size={13} /> List
          </button>
          <button
            type="button"
            className="gx-kg-chip"
            aria-pressed={mode === "graph"}
            onClick={() => setMode("graph")}
          >
            <Network size={13} /> Graph
          </button>
        </div>

        {mode === "graph" ? (
          graphQuery.isPending ? (
            <div className="gx-skeleton" data-testid="kg-graph-loading" />
          ) : graphQuery.isError ? (
            <p className="gx-campaign__error" role="alert">
              Could not load the graph: {graphQuery.error.message}
            </p>
          ) : (
            <KnowledgeGraph
              nodes={graphQuery.data.nodes}
              edges={graphQuery.data.edges}
              selectedID={editing?.id ?? null}
              onSelectNode={editByID}
              onGraphChanged={() => {
                invalidateGraph();
                invalidateEdges();
              }}
            />
          )
        ) : (
          <>
            <KnowledgeDraftCard
              onApplied={() => {
                invalidateNodes();
                invalidateEdges();
                invalidateGraph();
              }}
            />
            <Input
              type="search"
              aria-label="Search entries"
              icon={<Search size={15} />}
              placeholder="Search the wiki — names and content"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              className="gx-kg-search"
            />
            {searchFailed ? (
              <p className="gx-campaign__error" role="alert">
                Couldn't search: {searchQuery.error?.message}
              </p>
            ) : (
              <>
                {groups.map((g) => (
                  <section key={g.type} className="gx-kg-group" aria-label={metaOf(g.type).label}>
                    <h3 className="gx-kg-group__title">{metaOf(g.type).label}</h3>
                    {g.items.map((n) => (
                      <KnowledgeCard
                        key={n.id}
                        node={n}
                        onEdit={() => setEditing(n)}
                        onDelete={() => setConfirmNode(n)}
                        deleting={deleteNode.isPending && deleteNode.variables?.id === n.id}
                      />
                    ))}
                  </section>
                ))}
                {nodes.length === 0 &&
                  (searching ? (
                    <p className="gx-kg-empty">No entries match “{debounced.trim()}”.</p>
                  ) : (
                    <p className="gx-kg-empty">
                      No entries yet. Add what the world knows and your NPCs will speak to it.
                    </p>
                  ))}
              </>
            )}
          </>
        )}
      </div>

      <EntryEditor
        key={editing?.id ?? "new"}
        node={editing}
        pending={editing ? updateNode.isPending : createNode.isPending}
        error={editorError}
        onCancel={() => setEditing(null)}
        onDelete={editing ? () => setConfirmNode(editing) : undefined}
        onSubmit={(fields, reset) => {
          if (editing) {
            updateNode.mutate({
              id: editing.id,
              name: fields.name,
              body: fields.body,
              gmPrivate: fields.gmPrivate,
              aspects: toWireAspects(fields.aspects),
              knownAspectIds: fields.knownAspectIds,
            });
          } else {
            createNode.mutate(
              {
                nodeType: fields.nodeType,
                name: fields.name,
                body: fields.body,
                gmPrivate: fields.gmPrivate,
                aspects: toWireAspects(fields.aspects),
              },
              { onSuccess: reset },
            );
          }
        }}
      />

      {confirmNode && (
        <NodeDeleteConfirm
          node={confirmNode}
          onCancel={() => setConfirmNode(null)}
          onConfirm={() => {
            removeNode(confirmNode);
            setConfirmNode(null);
          }}
        />
      )}
    </div>
  );
}

// KnowledgeDraftCard is the on-demand "generate entries" flow (#479). Strictly
// button-driven: collapsed it is a single affordance; expanded, the GM writes a
// prompt and presses "Generate draft" — only then does an LLM call run. The
// result is a PREVIEW (nothing written): the GM can drop individual entries or
// relations, then lands the rest atomically via ApplyGeneratedKnowledge, or
// discards the whole draft.
function KnowledgeDraftCard({ onApplied }: { onApplied: () => void }) {
  const [open, setOpen] = useState(false);
  const [prompt, setPrompt] = useState("");
  // The reviewable draft. Held locally so remove-buttons can edit it before the
  // apply; edge indices are remapped on every node removal.
  const [draft, setDraft] = useState<{ nodes: DraftNode[]; edges: DraftEdge[] } | null>(null);
  const [error, setError] = useState<string | null>(null);

  const generate = useMutation(CampaignService.method.generateKnowledge);
  const apply = useMutation(CampaignService.method.applyGeneratedKnowledge);

  const runGenerate = async () => {
    setError(null);
    try {
      const res = await generate.mutateAsync({ prompt });
      setDraft({ nodes: res.nodes, edges: res.edges });
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  const removeNode = (idx: number) => {
    setDraft((d) => {
      if (!d) return d;
      const nodes = d.nodes.filter((_, i) => i !== idx);
      // Drop edges touching the removed entry; shift indices past it down.
      const edges = d.edges
        .filter((e) => e.fromIndex !== idx && e.toIndex !== idx)
        .map((e) => ({
          ...e,
          fromIndex: e.fromIndex > idx ? e.fromIndex - 1 : e.fromIndex,
          toIndex: e.toIndex > idx ? e.toIndex - 1 : e.toIndex,
        })) as DraftEdge[];
      return { nodes, edges };
    });
  };
  const removeEdge = (idx: number) => {
    setDraft((d) => (d ? { ...d, edges: d.edges.filter((_, i) => i !== idx) } : d));
  };

  const reset = () => {
    setDraft(null);
    setPrompt("");
    setError(null);
  };

  const runApply = async () => {
    if (!draft || draft.nodes.length === 0) return;
    setError(null);
    try {
      const res = await apply.mutateAsync({ nodes: draft.nodes, edges: draft.edges });
      onApplied();
      reset();
      setOpen(false);
      toast.success(
        `Added ${res.nodes.length} entr${res.nodes.length === 1 ? "y" : "ies"}` +
          (res.edgesCreated > 0
            ? ` and ${res.edgesCreated} relationship${res.edgesCreated === 1 ? "" : "s"}`
            : ""),
      );
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  if (!open) {
    return (
      <Button
        variant="secondary"
        iconStart={<Sparkles size={14} />}
        className="gx-kg-draft-open"
        onClick={() => setOpen(true)}
      >
        Generate entries with your LLM…
      </Button>
    );
  }

  return (
    <Card accent className="gx-kg-draft">
      <div className="gx-kg-editor__bar">
        <h2 className="gx-kg-editor__title">
          <Sparkles size={15} /> Generate entries
        </h2>
        <button
          type="button"
          className="gx-kg-iconbtn"
          aria-label="Close generator"
          onClick={() => {
            reset();
            setOpen(false);
          }}
        >
          <X size={15} />
        </button>
      </div>

      {draft === null ? (
        <>
          <div className="gx-field">
            <label className="gx-field__label" htmlFor="gx-kg-draft-prompt">
              What should be drafted?
            </label>
            <textarea
              id="gx-kg-draft-prompt"
              className="gx-input gx-textarea"
              rows={3}
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
              placeholder="e.g. The thieves' guild of Saltmarsh — its leader, hideout, prized relic and rival faction"
            />
            <span className="gx-field__hint">
              Your configured LLM drafts linked entries from this. Nothing is saved until you review
              and apply the draft.
            </span>
          </div>
          <div className="gx-kg-editor__actions">
            <Button
              variant="primary"
              iconStart={<Sparkles size={14} />}
              onClick={() => void runGenerate()}
              disabled={!prompt.trim() || generate.isPending}
            >
              {generate.isPending ? "Drafting…" : "Generate draft"}
            </Button>
            {error && (
              <span className="gx-editor__status gx-editor__status--error" role="alert">
                Couldn't generate: {error}
              </span>
            )}
          </div>
        </>
      ) : (
        <>
          <span className="gx-field__hint">
            A draft — nothing is saved yet. Remove what doesn't fit, then apply.
          </span>
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
                      <Badge
                        size="sm"
                        style={{ color: meta.color, background: alphaBg(meta.color) }}
                      >
                        {meta.label}
                      </Badge>
                      {n.gmPrivate && (
                        <Badge variant="neutral" size="sm">
                          <EyeOff size={11} /> GM private
                        </Badge>
                      )}
                    </div>
                    {n.body && <span className="gx-kg-card__snippet">{n.body}</span>}
                  </div>
                  <button
                    type="button"
                    className="gx-kg-iconbtn gx-kg-iconbtn--danger"
                    aria-label={`Remove ${n.name} from the draft`}
                    onClick={() => removeNode(i)}
                  >
                    <X size={14} />
                  </button>
                </li>
              );
            })}
          </ul>
          {draft.edges.length > 0 && (
            <ul className="gx-kg-draft__edges">
              {draft.edges.map((e, i) => (
                <li key={i} className="gx-kg-draft__edge">
                  <span className="gx-kg-draft__edge-text">
                    {draft.nodes[e.fromIndex]?.name} →{" "}
                    <em>{EDGE_TYPE_LABEL.get(e.edgeType) ?? ""}</em> → {draft.nodes[e.toIndex]?.name}
                  </span>
                  <button
                    type="button"
                    className="gx-kg-iconbtn gx-kg-iconbtn--danger"
                    aria-label="Remove this relation from the draft"
                    onClick={() => removeEdge(i)}
                  >
                    <X size={13} />
                  </button>
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
                ? "Adding…"
                : `Add ${draft.nodes.length} entr${draft.nodes.length === 1 ? "y" : "ies"}` +
                  (draft.edges.length > 0 ? ` + ${draft.edges.length} relation${draft.edges.length === 1 ? "" : "s"}` : "")}
            </Button>
            <Button variant="ghost" onClick={reset} disabled={apply.isPending}>
              Discard draft
            </Button>
            {error && (
              <span className="gx-editor__status gx-editor__status--error" role="alert">
                Couldn't apply: {error}
              </span>
            )}
          </div>
        </>
      )}
    </Card>
  );
}

// NodeDeleteConfirm is the delete gate for one entry. Mounted only while a delete
// is pending confirmation, it fetches the node's edges so the dialog can name how
// many relationships the cascade (ADR-0008 ON DELETE CASCADE) will take with it.
// The count must never be IMPLIED — while the fetch is in flight a raw 0 would
// falsely promise "no cascade", so the dialog says "counting…" and holds the
// confirm disabled until the real count lands; a failed fetch says so plainly
// rather than showing 0 (deletion still cascades server-side either way).
function NodeDeleteConfirm({
  node,
  onConfirm,
  onCancel,
}: {
  node: Node;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  // retry:false so a failed count settles into isError at once — this query
  // blocks a modal, so backing off silently for seconds would leave "counting…"
  // stuck and the confirm disabled. Fail fast and say so.
  const edgesQuery = useQuery(
    CampaignService.method.listNodeEdges,
    { nodeId: node.id },
    { retry: false },
  );
  const edgeCount =
    (edgesQuery.data?.outgoing.length ?? 0) + (edgesQuery.data?.incoming.length ?? 0);

  let cascade: ReactNode;
  if (edgesQuery.isPending) {
    cascade = <> Counting its relationships…</>;
  } else if (edgesQuery.isError) {
    cascade = <> Its relationships couldn't be counted, but any it has will be deleted too.</>;
  } else if (edgeCount > 0) {
    cascade = (
      <>
        {" This also deletes its "}
        <strong>
          {edgeCount} relationship{edgeCount === 1 ? "" : "s"}
        </strong>
        .
      </>
    );
  } else {
    cascade = null;
  }

  return (
    <ConfirmDialog
      open
      onOpenChange={(open) => {
        if (!open) onCancel();
      }}
      title={`Delete “${node.name}”?`}
      description={
        <>
          This permanently deletes this entry. Hard delete — this can't be undone.
          {cascade}
        </>
      }
      confirmLabel="Delete entry"
      // Don't let the operator confirm against an unknown count; a fetch failure
      // is not a reason to block the delete itself.
      confirmDisabled={edgesQuery.isPending}
      onConfirm={onConfirm}
    />
  );
}

// KnowledgeCard renders one entry: a type-colored icon tile, the name, a
// type-colored badge, a "GM private" badge (EyeOff) when private, a one-line body
// snippet, and edit/delete affordances.
function KnowledgeCard({
  node,
  onEdit,
  onDelete,
  deleting,
}: {
  node: Node;
  onEdit: () => void;
  onDelete: () => void;
  deleting: boolean;
}) {
  const meta = metaOf(node.nodeType);
  return (
    <Card className="gx-kg-card">
      <span
        className="gx-kg-card__icon"
        style={{ color: meta.color, background: alphaBg(meta.color) }}
        aria-hidden
      >
        <meta.Icon size={16} />
      </span>
      <div className="gx-kg-card__meta">
        <div className="gx-kg-card__head">
          <span className="gx-kg-card__name">{node.name}</span>
          <Badge
            size="sm"
            className="gx-kg-card__type"
            style={{ color: meta.color, background: alphaBg(meta.color) }}
          >
            {meta.label}
          </Badge>
          {node.gmPrivate && (
            <Badge variant="neutral" size="sm">
              <EyeOff size={11} /> GM private
            </Badge>
          )}
          {node.agentId !== "" && (
            <Badge size="sm" className="gx-kg-card__linked" style={{ color: "#ffcf5a", background: "#ffcf5a24" }}>
              <LinkIcon size={11} /> Linked agent
            </Badge>
          )}
        </div>
        {node.body && <span className="gx-kg-card__snippet">{node.body}</span>}
      </div>
      <div className="gx-kg-card__actions">
        <button
          type="button"
          className="gx-kg-iconbtn"
          aria-label={`Edit ${node.name}`}
          onClick={onEdit}
        >
          <Pencil size={14} />
        </button>
        <button
          type="button"
          className="gx-kg-iconbtn gx-kg-iconbtn--danger"
          aria-label={`Delete ${node.name}`}
          onClick={onDelete}
          disabled={deleting}
        >
          <Trash2 size={14} />
        </button>
      </div>
    </Card>
  );
}

// AspectRow is one editable aspect in the editor's local state (#542). `uid` is a
// client-only React key: a freshly added row has no server id yet, and reusing the
// array index as a key made a reorder remount the wrong inputs and lose focus
// mid-typing.
//
// `id` is the PERSISTED row's id, empty for a row the GM just added. It is sent
// back on save so the server deletes only the rows this editor actually loaded —
// which is what stops a save from wiping a fact a proposal approval appended while
// the editor was open.
type AspectRow = { uid: number; id: string; key: string; value: string; gmPrivate: boolean };

let nextAspectUid = 0;
const newAspectRow = (): AspectRow => ({
  uid: ++nextAspectUid,
  id: "",
  key: "",
  value: "",
  gmPrivate: false,
});

function toAspectRows(node: Node | null): AspectRow[] {
  return (node?.aspects ?? []).map((a) => ({
    uid: ++nextAspectUid,
    id: a.id,
    key: a.key,
    value: a.value,
    gmPrivate: a.gmPrivate,
  }));
}

// NodeAspects is the per-fact editor: an ordered list of (label, fact) rows, each
// with its own "GM only" toggle, plus reorder and delete. This is what lets an NPC
// keep its public facts and lose only its secret — the entry itself stays public.
function NodeAspects({
  rows,
  onChange,
  disabled,
}: {
  rows: AspectRow[];
  onChange: (rows: AspectRow[]) => void;
  disabled: boolean;
}) {
  const patch = (uid: number, next: Partial<AspectRow>) =>
    onChange(rows.map((r) => (r.uid === uid ? { ...r, ...next } : r)));
  const remove = (uid: number) => onChange(rows.filter((r) => r.uid !== uid));
  const move = (index: number, delta: number) => {
    const to = index + delta;
    if (to < 0 || to >= rows.length) return;
    const next = [...rows];
    [next[index], next[to]] = [next[to], next[index]];
    onChange(next);
  };

  return (
    <div className="gx-field gx-kg-aspects">
      <span className="gx-field__label">Facts</span>
      <span className="gx-field__hint">
        Each fact hides on its own — an entry can be public and still keep a secret.
      </span>
      <ul className="gx-kg-aspects__list">
        {rows.map((row, i) => (
          <li key={row.uid} className="gx-kg-aspect" data-private={row.gmPrivate || undefined}>
            <div className="gx-kg-aspect__fields">
              <Input
                aria-label={`Fact ${i + 1} label`}
                placeholder="Role"
                className="gx-kg-aspect__key"
                value={row.key}
                disabled={disabled}
                onChange={(e) => patch(row.uid, { key: e.target.value })}
              />
              <Input
                aria-label={`Fact ${i + 1} text`}
                placeholder="Runs the Rusty Anchor"
                className="gx-kg-aspect__value"
                value={row.value}
                disabled={disabled}
                onChange={(e) => patch(row.uid, { value: e.target.value })}
              />
            </div>
            <div className="gx-kg-aspect__actions">
              <button
                type="button"
                className="gx-kg-iconbtn"
                aria-label={`Move fact ${i + 1} up`}
                disabled={disabled || i === 0}
                onClick={() => move(i, -1)}
              >
                <ChevronUp size={14} />
              </button>
              <button
                type="button"
                className="gx-kg-iconbtn"
                aria-label={`Move fact ${i + 1} down`}
                disabled={disabled || i === rows.length - 1}
                onClick={() => move(i, 1)}
              >
                <ChevronDown size={14} />
              </button>
              <button
                type="button"
                className="gx-kg-iconbtn"
                aria-label={
                  row.gmPrivate ? `Make fact ${i + 1} public` : `Make fact ${i + 1} GM private`
                }
                aria-pressed={row.gmPrivate}
                disabled={disabled}
                onClick={() => patch(row.uid, { gmPrivate: !row.gmPrivate })}
              >
                {row.gmPrivate ? <EyeOff size={14} /> : <Eye size={14} />}
              </button>
              <button
                type="button"
                className="gx-kg-iconbtn gx-kg-iconbtn--danger"
                aria-label={`Remove fact ${i + 1}`}
                disabled={disabled}
                onClick={() => remove(row.uid)}
              >
                <X size={14} />
              </button>
            </div>
          </li>
        ))}
      </ul>
      <Button
        variant="ghost"
        iconStart={<Plus size={13} />}
        disabled={disabled}
        onClick={() => onChange([...rows, newAspectRow()])}
      >
        Add fact
      </Button>
    </div>
  );
}

type EditorFields = {
  nodeType: NodeType;
  name: string;
  body: string;
  gmPrivate: boolean;
  aspects: AspectRow[];
  /** The persisted aspect ids this editor loaded — see UpdateNodeRequest.known_aspect_ids. */
  knownAspectIds: string[];
};

// toWireAspects drops rows the GM left entirely blank — the editor keeps an empty
// row around for the next fact, and that convenience must not become a save error.
//
// The persisted `id` rides along so the server updates that row in place and its
// identity survives the save; a repeated save is then idempotent rather than
// duplicating the list. The authoritative "what this editor had loaded" set is
// knownAspectIds, sent separately, because a row the GM DELETED is by definition
// absent from this list.
function toWireAspects(rows: AspectRow[]) {
  return rows
    .filter((r) => r.key.trim() !== "" || r.value.trim() !== "")
    .map((r) => ({ id: r.id, key: r.key.trim(), value: r.value.trim(), gmPrivate: r.gmPrivate }));
}

// EntryEditor is the sticky editor card. In create mode it offers the Type select
// (all seven types) plus Name/Content/GM-private; in edit mode the type is fixed
// (immutable, ADR-0008) so the select is disabled, and a delete button + Cancel
// appear. Remounting on a key change (editing id) resets its fields.
function EntryEditor({
  node,
  pending,
  error,
  onSubmit,
  onDelete,
  onCancel,
}: {
  node: Node | null;
  pending: boolean;
  error: string | null;
  onSubmit: (fields: EditorFields, reset: () => void) => void;
  onDelete?: () => void;
  onCancel: () => void;
}) {
  const isEdit = node != null;
  const [nodeType, setNodeType] = useState<NodeType>(node?.nodeType ?? NodeType.NOTE);
  const [name, setName] = useState(node?.name ?? "");
  const [body, setBody] = useState(node?.body ?? "");
  const [gmPrivate, setGmPrivate] = useState(node?.gmPrivate ?? false);
  const [aspects, setAspects] = useState<AspectRow[]>(() => toAspectRows(node));
  // Captured at mount (the editor remounts per entry via its key), NOT at save
  // time: it must describe what was on screen when the GM started editing.
  const [knownAspectIds] = useState<string[]>(
    () => (node?.aspects ?? []).map((a) => a.id).filter((id) => id !== ""),
  );

  const reset = () => {
    setNodeType(NodeType.NOTE);
    setName("");
    setBody("");
    setGmPrivate(false);
    setAspects([]);
  };
  const submit = () => {
    if (name.trim() === "") return;
    onSubmit({ nodeType, name: name.trim(), body, gmPrivate, aspects, knownAspectIds }, reset);
  };

  return (
    <Card accent className="gx-kg-editor">
      <div className="gx-kg-editor__bar">
        <h2 className="gx-kg-editor__title">{isEdit ? "Edit entry" : "Add entry"}</h2>
        {isEdit && onDelete && (
          <button
            type="button"
            className="gx-kg-iconbtn gx-kg-iconbtn--danger"
            aria-label="Delete entry"
            onClick={onDelete}
            disabled={pending}
          >
            <Trash2 size={15} />
          </button>
        )}
      </div>

      <div className="gx-field">
        <Select
          label="Type"
          options={TYPE_OPTIONS}
          value={String(nodeType)}
          onValueChange={(v) => setNodeType(Number(v) as NodeType)}
          disabled={isEdit}
        />
        <span className="gx-field__hint">{TYPE_HINT}</span>
      </div>

      <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} placeholder="What is it called?" />

      <NodeAspects rows={aspects} onChange={setAspects} disabled={pending} />

      <div className="gx-field">
        <label className="gx-field__label" htmlFor="gx-kg-body">
          Content
        </label>
        <textarea
          id="gx-kg-body"
          className="gx-input gx-textarea"
          rows={4}
          value={body}
          onChange={(e) => setBody(e.target.value)}
        />
        <span className="gx-field__hint">
          What the world knows. Public entries are injected into NPC prompts.
        </span>
      </div>

      <div className="gx-kg-editor__switch">
        <Switch
          label="GM private — never enters an NPC's prompt"
          checked={gmPrivate}
          onCheckedChange={setGmPrivate}
        />
        <span className="gx-field__hint">
          Private entries stay searchable for you and never reach the table.
        </span>
      </div>

      {isEdit && node && <NodeRelations node={node} />}

      <div className="gx-kg-editor__actions">
        <Button
          variant="primary"
          iconStart={isEdit ? undefined : <Plus size={14} />}
          onClick={submit}
          disabled={pending || name.trim() === ""}
        >
          {pending ? "Saving…" : isEdit ? "Save entry" : "Add entry"}
        </Button>
        {isEdit && (
          <Button variant="ghost" onClick={onCancel} disabled={pending}>
            Cancel
          </Button>
        )}
        {error && (
          <span className="gx-editor__status gx-editor__status--error" role="alert">
            {error}
          </span>
        )}
      </div>
    </Card>
  );
}
