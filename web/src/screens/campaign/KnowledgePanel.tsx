import { useMemo, useState } from "react";
import { useQuery, useMutation, createConnectQueryKey } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { EyeOff, Plus, StickyNote } from "lucide-react";

import { CampaignService, NodeType } from "@gen/glyphoxa/management/v1/management_pb";
import type { Node } from "@gen/glyphoxa/management/v1/management_pb";
import { Card } from "@/components/ui/Card";
import { Badge } from "@/components/ui/Badge";
import { Input } from "@/components/ui/Input";
import { Switch } from "@/components/ui/Switch";
import { Button } from "@/components/ui/Button";

// The Knowledge panel (#126) backs the Campaign screen's "Knowledge" view on the
// live CampaignService ListNodes/CreateNode RPCs (ADR-0008 v1.0). For this slice
// the only Node type authored from the UI is a Note ("entry", GM-facing copy —
// the code keeps the Node domain term). Public entries are injected into NPC
// prompts; a GM-private entry never reaches the table (the gm_private flag). Full
// 7-type authoring, search and edges arrive in later slices.

// noteColor is the design's Note type-color, applied to the entry icon.
const noteColor = "#8b93a7";

export function KnowledgePanel() {
  const queryClient = useQueryClient();
  const { data, status, error } = useQuery(CampaignService.method.listNodes, {});
  const nodes = useMemo(() => data?.nodes ?? [], [data]);

  const invalidateNodes = () =>
    queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: CampaignService.method.listNodes,
        cardinality: "finite",
      }),
    });

  const createNode = useMutation(CampaignService.method.createNode, {
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

  return (
    <div className="gx-kg-layout">
      <div className="gx-kg-list">
        {nodes.map((n) => (
          <KnowledgeCard key={n.id} node={n} />
        ))}
        {nodes.length === 0 && (
          <p className="gx-kg-empty">
            No entries yet. Add what the world knows and your NPCs will speak to it.
          </p>
        )}
      </div>

      <AddEntry
        pending={createNode.isPending}
        error={createNode.isError ? createNode.error.message : null}
        onAdd={(name, body, gmPrivate, reset) =>
          createNode.mutate(
            { nodeType: NodeType.NOTE, name, body, gmPrivate },
            { onSuccess: reset },
          )
        }
      />
    </div>
  );
}

// KnowledgeCard renders one entry: the Note icon, name, a "Note" type badge, a
// "GM private" badge (EyeOff) when private, and a one-line body snippet.
function KnowledgeCard({ node }: { node: Node }) {
  return (
    <Card className="gx-kg-card">
      <span className="gx-kg-card__icon" style={{ color: noteColor }} aria-hidden>
        <StickyNote size={16} />
      </span>
      <div className="gx-kg-card__meta">
        <div className="gx-kg-card__head">
          <span className="gx-kg-card__name">{node.name}</span>
          <Badge variant="neutral" size="sm">
            Note
          </Badge>
          {node.gmPrivate && (
            <Badge variant="warning" size="sm">
              <EyeOff size={11} /> GM private
            </Badge>
          )}
        </div>
        {node.body && <span className="gx-kg-card__snippet">{node.body}</span>}
      </div>
    </Card>
  );
}

// AddEntry is the sticky create form: Name, Content, and the GM-private switch.
// The type is fixed to Note for this slice.
function AddEntry({
  pending,
  error,
  onAdd,
}: {
  pending: boolean;
  error: string | null;
  onAdd: (name: string, body: string, gmPrivate: boolean, reset: () => void) => void;
}) {
  const [name, setName] = useState("");
  const [body, setBody] = useState("");
  const [gmPrivate, setGmPrivate] = useState(false);

  const reset = () => {
    setName("");
    setBody("");
    setGmPrivate(false);
  };
  const add = () => {
    if (name.trim() === "") return;
    onAdd(name.trim(), body, gmPrivate, reset);
  };

  return (
    <Card accent className="gx-kg-editor">
      <h2 className="gx-kg-editor__title">Add entry</h2>

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

      <div className="gx-kg-editor__actions">
        <Button variant="primary" iconStart={<Plus size={14} />} onClick={add} disabled={pending || name.trim() === ""}>
          {pending ? "Adding…" : "Add entry"}
        </Button>
        {error && (
          <span className="gx-editor__status gx-editor__status--error" role="alert">
            Couldn't add: {error}
          </span>
        )}
      </div>
    </Card>
  );
}
