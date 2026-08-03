import { useMemo, useState } from "react";
import { useMutation, useQuery, createConnectQueryKey } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { Plus, Trash2, X } from "lucide-react";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Input";
import { alphaBg, metaOf } from "./knowledgeVocab";

// Saved session prep boards (#543): a named, ordered shortlist of entries the GM
// pins for one session — the "tonight: the harbour heist" text file they already
// keep outside the tool.
//
// Surfaced on the SESSION screen as well as in prep, because a board is most
// useful during play, when the GM needs the entry they set aside an hour ago and
// cannot go hunting for it.
//
// Boards never enter a prompt. A board is a shortlist, not ownership: deleting one
// leaves every entry untouched.

export function PrepBoards({ onOpenNode }: { onOpenNode?: (nodeID: string) => void }) {
  const queryClient = useQueryClient();
  const boardsQuery = useQuery(CampaignService.method.listBoards, {});
  const nodesQuery = useQuery(CampaignService.method.listNodes, {});
  const [newName, setNewName] = useState("");

  const nodesByID = useMemo(() => {
    const m = new Map<string, { name: string; nodeType: number }>();
    for (const n of nodesQuery.data?.nodes ?? []) m.set(n.id, { name: n.name, nodeType: n.nodeType });
    return m;
  }, [nodesQuery.data]);

  const invalidate = () =>
    void queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: CampaignService.method.listBoards,
        cardinality: "finite",
      }),
    });

  const createBoard = useMutation(CampaignService.method.createBoard, { onSuccess: invalidate });
  const updateBoard = useMutation(CampaignService.method.updateBoard, { onSuccess: invalidate });
  const deleteBoard = useMutation(CampaignService.method.deleteBoard, { onSuccess: invalidate });

  const boards = boardsQuery.data?.boards ?? [];

  return (
    <section className="gx-boards" aria-label="Prep boards">
      <div className="gx-boards__new">
        <Input
          aria-label="New board name"
          placeholder="tonight: the harbour heist"
          value={newName}
          onChange={(e) => setNewName(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && newName.trim() !== "") {
              createBoard.mutate({ name: newName.trim() });
              setNewName("");
            }
          }}
        />
        <Button
          variant="secondary"
          size="sm"
          iconStart={<Plus size={13} />}
          disabled={createBoard.isPending || newName.trim() === ""}
          onClick={() => {
            createBoard.mutate({ name: newName.trim() });
            setNewName("");
          }}
        >
          Add board
        </Button>
      </div>

      {boards.length === 0 ? (
        <p className="gx-kg-empty">
          No boards yet. Make one for tonight and the entries you need are one click away during
          play.
        </p>
      ) : (
        boards.map((b) => (
          <div key={b.id} className="gx-boards__board">
            <div className="gx-boards__head">
              <h4 className="gx-boards__name">{b.name}</h4>
              <button
                type="button"
                className="gx-kg-iconbtn gx-kg-iconbtn--danger"
                aria-label={`Delete board ${b.name}`}
                onClick={() => deleteBoard.mutate({ id: b.id })}
              >
                <Trash2 size={13} />
              </button>
            </div>
            {b.nodeIds.length === 0 ? (
              <p className="gx-field__hint">Empty — add entries from the Knowledge tab.</p>
            ) : (
              <ul className="gx-boards__list">
                {b.nodeIds.map((id) => {
                  const node = nodesByID.get(id);
                  const meta = metaOf(node?.nodeType ?? 0);
                  return (
                    <li key={id}>
                      <button
                        type="button"
                        className="gx-kg-chip"
                        style={{ color: meta.color, background: alphaBg(meta.color) }}
                        onClick={() => onOpenNode?.(id)}
                      >
                        {node?.name ?? "(deleted entry)"}
                      </button>
                      <button
                        type="button"
                        className="gx-kg-iconbtn"
                        aria-label={`Remove ${node?.name ?? "entry"} from ${b.name}`}
                        onClick={() =>
                          updateBoard.mutate({
                            id: b.id,
                            name: b.name,
                            nodeIds: b.nodeIds.filter((x) => x !== id),
                          })
                        }
                      >
                        <X size={12} />
                      </button>
                    </li>
                  );
                })}
              </ul>
            )}
          </div>
        ))
      )}
    </section>
  );
}
