import { useMemo, useState } from "react";
import { useMutation, useQuery, createConnectQueryKey } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { Plus, Trash2, X } from "lucide-react";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Input";
import { useI18n } from "@/i18n";
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

/** invalidateBoards drops the one board read every surface derives from. */
export function invalidateBoards(queryClient: ReturnType<typeof useQueryClient>): void {
  void queryClient.invalidateQueries({
    queryKey: createConnectQueryKey({
      schema: CampaignService.method.listBoards,
      cardinality: "finite",
    }),
  });
}

/**
 * NodeBoards is the "put this on a board" affordance, shown beside an entry's
 * tags in the editor.
 *
 * Without it the board feature is unreachable: PrepBoards can create a board and
 * remove entries from one, but nothing anywhere could ADD an entry, so every board
 * was permanently empty and its empty state ("add entries from the Knowledge tab")
 * described a control that did not exist.
 *
 * A board is a shortlist, so membership is a toggle rather than a picker dialog —
 * the GM is answering "is tonight's session about this?", not filling in a form.
 */
export function NodeBoards({ nodeID }: { nodeID: string }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const boardsQuery = useQuery(CampaignService.method.listBoards, {});
  const boards = boardsQuery.data?.boards ?? [];

  const update = useMutation(CampaignService.method.updateBoard, {
    onSuccess: () => invalidateBoards(queryClient),
  });

  if (boards.length === 0) return null;

  return (
    <div className="gx-field gx-kg-tags">
      <span className="gx-field__label">{t("campaign.boardsLabel")}</span>
      <span className="gx-field__hint">{t("campaign.boardsHint")}</span>
      <ul className="gx-kg-tags__list">
        {boards.map((b) => {
          const on = b.nodeIds.includes(nodeID);
          return (
            <li key={b.id}>
              <button
                type="button"
                className="gx-kg-chip"
                aria-pressed={on}
                disabled={update.isPending}
                onClick={() =>
                  update.mutate({
                    id: b.id,
                    name: b.name,
                    // Built from THIS board's server-provided list, so two entries
                    // toggled in quick succession cannot drop each other.
                    nodeIds: on
                      ? b.nodeIds.filter((x) => x !== nodeID)
                      : [...b.nodeIds, nodeID],
                  })
                }
              >
                {b.name}
              </button>
            </li>
          );
        })}
      </ul>
      {update.isError && (
        <span className="gx-editor__status gx-editor__status--error" role="alert">
          {t("campaign.boardUpdateError", { message: update.error.message })}
        </span>
      )}
    </div>
  );
}

export function PrepBoards({ onOpenNode }: { onOpenNode?: (nodeID: string) => void }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const boardsQuery = useQuery(CampaignService.method.listBoards, {});
  const nodesQuery = useQuery(CampaignService.method.listNodes, {});
  const [newName, setNewName] = useState("");

  const nodesByID = useMemo(() => {
    const m = new Map<string, { name: string; nodeType: number }>();
    for (const n of nodesQuery.data?.nodes ?? []) m.set(n.id, { name: n.name, nodeType: n.nodeType });
    return m;
  }, [nodesQuery.data]);

  const invalidate = () => invalidateBoards(queryClient);

  const createBoard = useMutation(CampaignService.method.createBoard, { onSuccess: invalidate });
  const updateBoard = useMutation(CampaignService.method.updateBoard, { onSuccess: invalidate });
  const deleteBoard = useMutation(CampaignService.method.deleteBoard, { onSuccess: invalidate });

  const boards = boardsQuery.data?.boards ?? [];

  return (
    <section className="gx-boards" aria-label={t("campaign.boardsAria")}>
      <div className="gx-boards__new">
        <Input
          aria-label={t("campaign.newBoardAria")}
          placeholder={t("campaign.newBoardPlaceholder")}
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
          {t("campaign.addBoard")}
        </Button>
      </div>

      {boards.length === 0 ? (
        <p className="gx-kg-empty">{t("campaign.boardsEmpty")}</p>
      ) : (
        boards.map((b) => (
          <div key={b.id} className="gx-boards__board">
            <div className="gx-boards__head">
              <h4 className="gx-boards__name">{b.name}</h4>
              <button
                type="button"
                className="gx-kg-iconbtn gx-kg-iconbtn--danger"
                aria-label={t("campaign.deleteBoardAria", { name: b.name })}
                onClick={() => deleteBoard.mutate({ id: b.id })}
              >
                <Trash2 size={13} />
              </button>
            </div>
            {b.nodeIds.length === 0 ? (
              <p className="gx-field__hint">{t("campaign.boardEmpty")}</p>
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
                        {node?.name ?? t("campaign.deletedEntry")}
                      </button>
                      <button
                        type="button"
                        className="gx-kg-iconbtn"
                        aria-label={t("campaign.removeFromBoardAria", {
                          name: node?.name ?? t("campaign.entryWord"),
                          board: b.name,
                        })}
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
