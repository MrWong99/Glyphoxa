import { useEffect, useMemo, useState } from "react";
import { useQuery, useMutation, createConnectQueryKey } from "@connectrpc/connect-query";
import { useQueryClient, keepPreviousData } from "@tanstack/react-query";
import {
  EyeOff,
  Plus,
  Pencil,
  Search,
  Trash2,
  User,
  VenetianMask,
  MapPin,
  Flag,
  Gem,
  GitBranch,
  StickyNote,
  Link as LinkIcon,
  type LucideIcon,
} from "lucide-react";

import { CampaignService, NodeType } from "@gen/glyphoxa/management/v1/management_pb";
import type { Node } from "@gen/glyphoxa/management/v1/management_pb";
import { Card } from "@/components/ui/Card";
import { Badge } from "@/components/ui/Badge";
import { Input } from "@/components/ui/Input";
import { Switch } from "@/components/ui/Switch";
import { Button } from "@/components/ui/Button";
import { Select } from "@/components/ui/Select";
import { NodeRelations } from "./NodeRelations";

// The Knowledge panel (#126, #129) backs the Campaign screen's "Knowledge" view
// on the live CampaignService node RPCs (ADR-0008 v1.0). An "entry" is the
// GM-facing name for a Node (the code keeps the Node domain term). This slice
// authors all seven Node types (type chosen once on create — immutable per
// ADR-0008), edits and deletes entries, and groups the list by type. Public
// entries are injected into NPC prompts; a gm_private entry never reaches the
// table. Type-filter chips + fulltext search arrive in #131.

type TypeMeta = { label: string; color: string; Icon: LucideIcon };

// Per-type label, design color, and lucide icon (approved #129 design). The Note
// entry's neutral grey doubles as the defensive fallback for an unknown type.
const TYPE_META: Record<number, TypeMeta> = {
  [NodeType.CHARACTER]: { label: "Character", color: "#4fa9ff", Icon: User },
  [NodeType.NPC]: { label: "NPC", color: "#9059ff", Icon: VenetianMask },
  [NodeType.LOCATION]: { label: "Location", color: "#35c48d", Icon: MapPin },
  [NodeType.FACTION]: { label: "Faction", color: "#ffbd4f", Icon: Flag },
  [NodeType.ITEM]: { label: "Item", color: "#ff7139", Icon: Gem },
  [NodeType.PLOT_THREAD]: { label: "Plot thread", color: "#ff4f5e", Icon: GitBranch },
  [NodeType.NOTE]: { label: "Note", color: "#8b93a7", Icon: StickyNote },
};

// The authorable types in enum order; UNSPECIFIED (0) is never offered, and this
// order also drives the grouped list and the type-select options.
const TYPE_ORDER: NodeType[] = [
  NodeType.CHARACTER,
  NodeType.NPC,
  NodeType.LOCATION,
  NodeType.FACTION,
  NodeType.ITEM,
  NodeType.PLOT_THREAD,
  NodeType.NOTE,
];

const TYPE_OPTIONS = TYPE_ORDER.map((t) => ({ value: String(t), label: TYPE_META[t].label }));
const TYPE_HINT = TYPE_ORDER.map((t) => TYPE_META[t].label).join(" · ");

function metaOf(t: NodeType): TypeMeta {
  return TYPE_META[t] ?? TYPE_META[NodeType.NOTE];
}

// alphaBg tints a type color to the design's 14%-alpha tile background (0x24 ≈ 14%).
function alphaBg(color: string): string {
  return `${color}24`;
}

export function KnowledgePanel() {
  const queryClient = useQueryClient();
  const listQuery = useQuery(CampaignService.method.listNodes, {});
  const [editing, setEditing] = useState<Node | null>(null);

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

  const createNode = useMutation(CampaignService.method.createNode, {
    onSuccess: () => void invalidateNodes(),
  });
  const updateNode = useMutation(CampaignService.method.updateNode, {
    onSuccess: () => {
      setEditing(null);
      void invalidateNodes();
    },
  });
  const deleteNode = useMutation(CampaignService.method.deleteNode, {
    onSuccess: () => void invalidateNodes(),
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

  return (
    <div className="gx-kg-layout">
      <div className="gx-kg-list">
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
                    onDelete={() => removeNode(n)}
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
      </div>

      <EntryEditor
        key={editing?.id ?? "new"}
        node={editing}
        pending={editing ? updateNode.isPending : createNode.isPending}
        error={editorError}
        onCancel={() => setEditing(null)}
        onDelete={editing ? () => removeNode(editing) : undefined}
        onSubmit={(fields, reset) => {
          if (editing) {
            updateNode.mutate({
              id: editing.id,
              name: fields.name,
              body: fields.body,
              gmPrivate: fields.gmPrivate,
            });
          } else {
            createNode.mutate(
              { nodeType: fields.nodeType, name: fields.name, body: fields.body, gmPrivate: fields.gmPrivate },
              { onSuccess: reset },
            );
          }
        }}
      />
    </div>
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

type EditorFields = { nodeType: NodeType; name: string; body: string; gmPrivate: boolean };

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

  const reset = () => {
    setNodeType(NodeType.NOTE);
    setName("");
    setBody("");
    setGmPrivate(false);
  };
  const submit = () => {
    if (name.trim() === "") return;
    onSubmit({ nodeType, name: name.trim(), body, gmPrivate }, reset);
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
