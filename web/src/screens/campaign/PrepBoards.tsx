import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { KeyboardEvent as ReactKeyboardEvent } from "react";
import { useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ArrowDown, ArrowUp, Check, ClipboardList, Copy, MoreVertical, Pencil, Plus, Trash2, X } from "lucide-react";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import type { Board } from "@gen/glyphoxa/management/v1/management_pb";
import { Button } from "@/components/ui/Button";
import { Combobox } from "@/components/ui/Combobox";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import { EmptyState } from "@/components/ui/EmptyState";
import { ErrorState } from "@/components/ui/ErrorState";
import { IconButton } from "@/components/ui/IconButton";
import { Input } from "@/components/ui/Input";
import { Menu, MenuItem, MenuSeparator } from "@/components/ui/Menu";
import { Skeleton } from "@/components/ui/Skeleton";
import { invalidateMethodQueries } from "@/lib/queryClient";
import { useI18n } from "@/i18n";
import { metaOf } from "./knowledgeVocab";
import { errorMessage } from "@/lib/connectError";

import "./boards.css";

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
//
// A board is managed FROM the board: its name is click-to-rename, entries are
// added from a search box on the board itself, reordered with up/down (or
// Alt+Arrow), and a board can be duplicated for next week's session. Before, a
// board could only be created, emptied one entry at a time, and deleted; adding
// meant opening each entry in the wiki, and the order was whatever order that
// happened in.

/** invalidateBoards drops the one board read every surface derives from. */
function invalidateBoards(queryClient: ReturnType<typeof useQueryClient>): Promise<void> {
  return invalidateMethodQueries(queryClient, CampaignService.method.listBoards);
}

/**
 * NodeBoards is the "put this on a board" affordance, shown beside an entry's
 * tags in the editor.
 *
 * Without it the board feature is unreachable from the wiki: PrepBoards can
 * create a board and remove entries from one, but nothing in the editor could
 * ADD the entry being looked at.
 *
 * A board is a shortlist, so membership is a toggle rather than a picker dialog —
 * the GM is answering "is tonight's session about this?", not filling in a form.
 *
 * With no board yet it used to render NOTHING, so a GM who had never made a
 * board never learned the feature existed. Now it offers the one thing that
 * makes sense there: name a new board, and this entry goes on it.
 */
export function NodeBoards({ nodeID }: { nodeID: string }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const boardsQuery = useQuery(CampaignService.method.listBoards, {});
  const boards = boardsQuery.data?.boards ?? [];
  const [newName, setNewName] = useState("");
  // With boards present the input is tucked behind a "New board" chip.
  const [adding, setAdding] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const update = useMutation(CampaignService.method.updateBoard);
  const create = useMutation(CampaignService.method.createBoard);
  const busy = update.isPending || create.isPending;

  const fail = (err: unknown) => setError(t("campaign.boardUpdateError", { message: errorMessage(err) }));

  const toggle = (b: Board, on: boolean) => {
    setError(null);
    update.mutate(
      {
        id: b.id,
        name: b.name,
        // Built from THIS board's server-provided list, so two entries toggled
        // in quick succession cannot drop each other.
        nodeIds: on ? b.nodeIds.filter((x) => x !== nodeID) : [...b.nodeIds, nodeID],
      },
      { onSuccess: () => void invalidateBoards(queryClient), onError: fail },
    );
  };

  // A new board made HERE is made for this entry: create it, then put the
  // entry on it. (CreateBoard makes an empty board; there is no one-call form.)
  const createWithEntry = async () => {
    const name = newName.trim();
    if (name === "" || busy) return;
    setError(null);
    try {
      const res = await create.mutateAsync({ name });
      if (res.board) await update.mutateAsync({ id: res.board.id, name: res.board.name, nodeIds: [nodeID] });
      setNewName("");
      setAdding(false);
    } catch (err) {
      fail(err);
    } finally {
      void invalidateBoards(queryClient);
    }
  };

  // Until the read has answered there is nothing honest to show: not the
  // toggles, and not an invitation to create a "first" board that may not be.
  if (!boardsQuery.data) return null;

  const showInput = boards.length === 0 || adding;

  return (
    <div className="gx-field gx-node-boards">
      <span className="gx-field__label">{t("campaign.boardsLabel")}</span>
      <span className="gx-field__hint">
        {boards.length === 0 ? t("boards.nodeEmptyHint") : t("campaign.boardsHint")}
      </span>
      {boards.length > 0 && (
        <ul className="gx-node-boards__list">
          {boards.map((b) => {
            const on = b.nodeIds.includes(nodeID);
            return (
              <li key={b.id}>
                <button
                  type="button"
                  className="gx-node-boards__chip"
                  aria-pressed={on}
                  // aria-disabled: the chip that was just pressed must keep focus.
                  aria-disabled={busy || undefined}
                  onClick={() => {
                    if (!busy) toggle(b, on);
                  }}
                >
                  {on && <Check size={12} aria-hidden />}
                  {b.name}
                </button>
              </li>
            );
          })}
          {!adding && (
            <li>
              <button type="button" className="gx-node-boards__chip gx-node-boards__chip--new" onClick={() => setAdding(true)}>
                <Plus size={12} aria-hidden /> {t("boards.newBoard")}
              </button>
            </li>
          )}
        </ul>
      )}
      {showInput && (
        <div className="gx-node-boards__new">
          <Input
            aria-label={t("campaign.newBoardAria")}
            placeholder={t("campaign.newBoardPlaceholder")}
            value={newName}
            // Revealed by the chip above: the caret belongs here.
            autoFocus={adding}
            onChange={(e) => setNewName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                void createWithEntry();
              } else if (e.key === "Escape" && adding) {
                e.preventDefault();
                e.stopPropagation();
                setAdding(false);
                setNewName("");
              }
            }}
          />
          <Button
            variant="secondary"
            size="sm"
            iconStart={<Plus size={13} />}
            disabled={newName.trim() === ""}
            loading={busy && newName.trim() !== ""}
            onClick={() => void createWithEntry()}
          >
            {t("boards.newBoard")}
          </Button>
        </div>
      )}
      {error && (
        <span className="gx-field__hint gx-field__hint--error" role="alert">
          {error}
        </span>
      )}
    </div>
  );
}

type NodeLite = { name: string; nodeType: number };

/** What a pending write has changed about a board, shown before the server answers. */
type Pending = { name?: string; nodeIds?: string[] };

export function PrepBoards({ onOpenNode }: { onOpenNode?: (nodeID: string) => void }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const boardsQuery = useQuery(CampaignService.method.listBoards, {});
  const nodesQuery = useQuery(CampaignService.method.listNodes, {});
  const [newName, setNewName] = useState("");

  const nodesByID = useMemo(() => {
    const m = new Map<string, NodeLite>();
    for (const n of nodesQuery.data?.nodes ?? []) m.set(n.id, { name: n.name, nodeType: n.nodeType });
    return m;
  }, [nodesQuery.data]);
  const nodesLoaded = nodesQuery.data !== undefined;

  // Every board write surfaces its failure (ADR-0017: sonner) — a rejected
  // create/reorder/delete otherwise reads as success with nothing changed.
  const fail = (err: unknown) =>
    toast.error(t("campaign.boardUpdateError", { message: errorMessage(err) }));
  const createBoard = useMutation(CampaignService.method.createBoard);
  const updateBoard = useMutation(CampaignService.method.updateBoard);
  const deleteBoard = useMutation(CampaignService.method.deleteBoard);
  // The board a delete is asking about — confirmed in a dialog, like every
  // other destructive act.
  const [deleteTarget, setDeleteTarget] = useState<{ id: string; name: string } | null>(null);

  // UpdateBoard REPLACES a board's name and whole entry list, so two writes
  // that overlap can land out of order and the older list wins. Writes are
  // queued — each starts when the one before has been read back — and until
  // then the board is drawn as it WILL be, so an Alt+Arrow held down moves the
  // row at once and three quick presses mean three steps, not one.
  const [pending, setPending] = useState<Record<string, Pending>>({});
  const pendingRef = useRef(pending);
  pendingRef.current = pending;
  const queue = useRef<Promise<unknown>>(Promise.resolve());

  const write = (board: Board, change: Pending) => {
    const merged: Pending = { ...pendingRef.current[board.id], ...change };
    pendingRef.current = { ...pendingRef.current, [board.id]: merged };
    setPending(pendingRef.current);
    const request = {
      id: board.id,
      name: merged.name ?? board.name,
      // An entry deleted in the wiki a moment ago would have the whole save
      // refused; it is not on the board any more either way.
      nodeIds: (merged.nodeIds ?? board.nodeIds).filter((id) => !nodesLoaded || nodesByID.has(id)),
    };
    queue.current = queue.current
      .then(() => updateBoard.mutateAsync(request))
      .then(() => invalidateBoards(queryClient))
      .catch((err: unknown) => {
        fail(err);
        // Show what the server actually has.
        return invalidateBoards(queryClient);
      })
      .finally(() => {
        // Only this write's snapshot: a later one owns the board's pending state.
        if (pendingRef.current[board.id] !== merged) return;
        const rest = { ...pendingRef.current };
        delete rest[board.id];
        pendingRef.current = rest;
        setPending(rest);
      });
  };

  const addBoard = () => {
    const name = newName.trim();
    if (name === "" || createBoard.isPending) return;
    createBoard.mutate(
      { name },
      {
        onSuccess: () => {
          setNewName("");
          void invalidateBoards(queryClient);
        },
        onError: fail,
      },
    );
  };

  // A copy for next week: a new board with the same entries in the same order.
  const duplicate = async (board: Board, nodeIds: string[]) => {
    try {
      const res = await createBoard.mutateAsync({ name: t("boards.copyName", { name: board.name }) });
      const ids = nodeIds.filter((id) => !nodesLoaded || nodesByID.has(id));
      if (res.board && ids.length > 0) {
        await updateBoard.mutateAsync({ id: res.board.id, name: res.board.name, nodeIds: ids });
      }
      toast.success(t("boards.duplicated", { name: board.name }));
    } catch (err) {
      fail(err);
    } finally {
      void invalidateBoards(queryClient);
    }
  };

  const boards = boardsQuery.data?.boards ?? [];
  // Entries, alphabetical, for every board's "Add entry" box.
  const allEntries = useMemo(
    () =>
      Array.from(nodesByID, ([id, n]) => ({ value: id, label: n.name })).sort((a, b) =>
        a.label.localeCompare(b.label),
      ),
    [nodesByID],
  );

  return (
    <section className="gx-boards" aria-label={t("campaign.boardsAria")}>
      <div className="gx-boards__new">
        <Input
          aria-label={t("campaign.newBoardAria")}
          placeholder={t("campaign.newBoardPlaceholder")}
          value={newName}
          onChange={(e) => setNewName(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") addBoard();
          }}
        />
        <Button
          variant="secondary"
          size="sm"
          iconStart={<Plus size={13} />}
          disabled={newName.trim() === ""}
          loading={createBoard.isPending && newName.trim() !== ""}
          onClick={addBoard}
        >
          {t("campaign.addBoard")}
        </Button>
      </div>

      {!boardsQuery.data ? (
        boardsQuery.isPending ? (
          <Skeleton lines={3} label={t("boards.loading")} />
        ) : (
          <ErrorState
            size="inline"
            title={t("boards.loadErrorTitle")}
            message={errorMessage(boardsQuery.error)}
            onRetry={() => void boardsQuery.refetch()}
            retrying={boardsQuery.isFetching}
          />
        )
      ) : boards.length === 0 ? (
        <EmptyState
          size="inline"
          icon={<ClipboardList size={16} />}
          title={t("boards.emptyTitle")}
          description={t("campaign.boardsEmpty")}
        />
      ) : (
        boards.map((b) => (
          <BoardCard
            key={b.id}
            board={b}
            pending={pending[b.id]}
            nodesByID={nodesByID}
            nodesLoaded={nodesLoaded}
            allEntries={allEntries}
            onOpenNode={onOpenNode}
            onWrite={(change) => write(b, change)}
            onDuplicate={(nodeIds) => void duplicate(b, nodeIds)}
            onDelete={() => setDeleteTarget({ id: b.id, name: b.name })}
          />
        ))
      )}

      {deleteTarget && (
        <ConfirmDialog
          open
          onOpenChange={(open) => {
            if (!open) setDeleteTarget(null);
          }}
          title={t("campaign.deleteBoardTitle", { name: deleteTarget.name })}
          description={t("campaign.deleteBoardDesc")}
          confirmLabel={t("campaign.deleteBoardConfirm")}
          onConfirm={() => {
            deleteBoard.mutate(
              { id: deleteTarget.id },
              { onSuccess: () => void invalidateBoards(queryClient), onError: fail },
            );
            setDeleteTarget(null);
          }}
        />
      )}
    </section>
  );
}

// BoardCard is one board: its name (click to rename), its numbered entries with
// reorder and remove, and the box that adds an entry.
function BoardCard({
  board,
  pending,
  nodesByID,
  nodesLoaded,
  allEntries,
  onOpenNode,
  onWrite,
  onDuplicate,
  onDelete,
}: {
  board: Board;
  pending?: Pending;
  nodesByID: Map<string, NodeLite>;
  nodesLoaded: boolean;
  allEntries: { value: string; label: string }[];
  onOpenNode?: (nodeID: string) => void;
  onWrite: (change: Pending) => void;
  onDuplicate: (nodeIds: string[]) => void;
  onDelete: () => void;
}) {
  const { t } = useI18n();
  const name = pending?.name ?? board.name;
  const nodeIds = pending?.nodeIds ?? board.nodeIds;

  // ── Rename ────────────────────────────────────────────────────────────────
  const [renaming, setRenaming] = useState(false);
  const [draft, setDraft] = useState(name);
  const renameInput = useRef<HTMLInputElement | null>(null);
  const nameButton = useRef<HTMLButtonElement | null>(null);
  // Where focus goes when the rename row closes: back to the name it replaced.
  const refocusName = useRef(false);

  const startRename = () => {
    setDraft(name);
    setRenaming(true);
  };
  const endRename = () => {
    refocusName.current = true;
    setRenaming(false);
  };
  const commitRename = () => {
    const next = draft.trim();
    // An empty name is not a rename; keep the row open on it.
    if (next === "") return;
    if (next !== name) onWrite({ name: next });
    endRename();
  };

  // "Rename" in the menu closes the menu, and the menu hands focus back to its
  // trigger a tick later — so the field takes focus a frame after that, not on
  // mount.
  useEffect(() => {
    if (!renaming) return;
    const frame = requestAnimationFrame(() => {
      renameInput.current?.focus();
      renameInput.current?.select();
    });
    return () => cancelAnimationFrame(frame);
  }, [renaming]);
  useEffect(() => {
    if (renaming || !refocusName.current) return;
    refocusName.current = false;
    nameButton.current?.focus();
  }, [renaming]);

  // ── Reorder ───────────────────────────────────────────────────────────────
  const listRef = useRef<HTMLOListElement | null>(null);
  // The control that moved a row keeps focus as the row moves with it.
  const keepFocus = useRef<{ nodeID: string; control: string } | null>(null);
  useLayoutEffect(() => {
    const want = keepFocus.current;
    if (!want) return;
    keepFocus.current = null;
    const rows = listRef.current?.querySelectorAll<HTMLElement>("[data-node-id]") ?? [];
    const row = Array.from(rows).find((el) => el.dataset.nodeId === want.nodeID);
    const control = row?.querySelector<HTMLElement>(`[data-control="${want.control}"]`);
    if (control && document.activeElement !== control) control.focus();
  }, [nodeIds]);

  const move = (index: number, by: -1 | 1, control: string) => {
    const to = index + by;
    if (to < 0 || to >= nodeIds.length) return;
    const next = [...nodeIds];
    [next[index], next[to]] = [next[to], next[index]];
    keepFocus.current = { nodeID: nodeIds[index], control };
    onWrite({ nodeIds: next });
  };

  // Alt+Arrow moves the row from any control in it.
  const onRowKeyDown = (index: number, ev: ReactKeyboardEvent<HTMLLIElement>) => {
    if (!ev.altKey || (ev.key !== "ArrowUp" && ev.key !== "ArrowDown")) return;
    ev.preventDefault();
    const control = ev.target instanceof HTMLElement ? (ev.target.dataset.control ?? "name") : "name";
    move(index, ev.key === "ArrowUp" ? -1 : 1, control);
  };

  const available = allEntries.filter((e) => !nodeIds.includes(e.value));
  const entryName = (id: string) =>
    // While the entry list still loads a missing name means "not resolved
    // yet", never "deleted".
    nodesByID.get(id)?.name ?? (nodesLoaded ? t("campaign.deletedEntry") : "…");

  return (
    <div className="gx-boards__board">
      <div className="gx-boards__head">
        {renaming ? (
          <div className="gx-boards__rename">
            {/* A bare .gx-input, not <Input>: this field has to be focused from
                an effect, and Input does not forward a ref. */}
            <input
              ref={renameInput}
              className="gx-input"
              aria-label={t("boards.renameAria", { name })}
              value={draft}
              maxLength={200}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter") {
                  e.preventDefault();
                  commitRename();
                } else if (e.key === "Escape") {
                  e.preventDefault();
                  e.stopPropagation();
                  endRename();
                }
              }}
            />
            <IconButton label={t("boards.renameSave")} size="sm" onClick={commitRename}>
              <Check size={14} />
            </IconButton>
            <IconButton label={t("boards.renameCancel")} size="sm" onClick={endRename}>
              <X size={14} />
            </IconButton>
          </div>
        ) : (
          <h4 className="gx-boards__name">
            <button
              ref={nameButton}
              type="button"
              className="gx-boards__name-btn"
              title={t("boards.renameTitle")}
              onClick={startRename}
            >
              <span className="gx-boards__name-text">{name}</span>
              <Pencil size={12} className="gx-boards__name-icon" aria-hidden />
            </button>
          </h4>
        )}
        <Menu
          trigger={
            <IconButton label={t("boards.actionsAria", { name })} size="sm">
              <MoreVertical size={15} />
            </IconButton>
          }
        >
          <MenuItem icon={<Pencil size={14} />} onSelect={startRename}>
            {t("boards.rename")}
          </MenuItem>
          <MenuItem icon={<Copy size={14} />} onSelect={() => onDuplicate(nodeIds)}>
            {t("boards.duplicate")}
          </MenuItem>
          <MenuSeparator />
          <MenuItem tone="danger" icon={<Trash2 size={14} />} onSelect={onDelete}>
            {t("campaign.deleteBoardConfirm")}
          </MenuItem>
        </Menu>
      </div>

      {nodeIds.length === 0 ? (
        <p className="gx-boards__empty">{t("boards.boardEmpty")}</p>
      ) : (
        <ol className="gx-boards__list" ref={listRef}>
          {nodeIds.map((id, index) => {
            const meta = metaOf(nodesByID.get(id)?.nodeType ?? 0);
            const label = nodesByID.get(id)?.name ?? t("campaign.entryWord");
            return (
              <li
                key={id}
                className="gx-boards__entry"
                data-node-id={id}
                onKeyDown={(ev) => onRowKeyDown(index, ev)}
              >
                {/* A button only where it does something: with no onOpenNode
                    (the Session screen's reference view) the entry is plain
                    text, not a control that swallows a click and a tab stop
                    for nothing. */}
                {onOpenNode ? (
                  <button
                    type="button"
                    className="gx-boards__entry-name"
                    data-control="name"
                    style={{ color: meta.color }}
                    onClick={() => onOpenNode(id)}
                  >
                    {entryName(id)}
                  </button>
                ) : (
                  <span className="gx-boards__entry-name" style={{ color: meta.color }}>
                    {entryName(id)}
                  </span>
                )}
                <span className="gx-boards__entry-actions">
                  <IconButton
                    label={t("boards.moveUpAria", { name: label })}
                    size="sm"
                    data-control="up"
                    // aria-disabled, not disabled: the button that carried a row
                    // to the top must still hold focus when it gets there.
                    aria-disabled={index === 0 || undefined}
                    onClick={() => move(index, -1, "up")}
                  >
                    <ArrowUp size={14} />
                  </IconButton>
                  <IconButton
                    label={t("boards.moveDownAria", { name: label })}
                    size="sm"
                    data-control="down"
                    aria-disabled={index === nodeIds.length - 1 || undefined}
                    onClick={() => move(index, 1, "down")}
                  >
                    <ArrowDown size={14} />
                  </IconButton>
                  <IconButton
                    label={t("campaign.removeFromBoardAria", { name: label, board: name })}
                    size="sm"
                    data-control="remove"
                    onClick={() => onWrite({ nodeIds: nodeIds.filter((x) => x !== id) })}
                  >
                    <X size={14} />
                  </IconButton>
                </span>
              </li>
            );
          })}
        </ol>
      )}

      <div className="gx-boards__add">
        <Combobox
          aria-label={t("boards.addEntryAria", { name })}
          options={available}
          // Always "nothing chosen": picking an entry adds it and the box is
          // ready for the next one.
          value=""
          onValueChange={(id) => onWrite({ nodeIds: [...nodeIds, id] })}
          disabled={!nodesLoaded || allEntries.length === 0}
          placeholder={
            nodesLoaded && allEntries.length === 0 ? t("boards.addEntryNoEntries") : t("boards.addEntry")
          }
          searchPlaceholder={t("boards.addEntrySearch")}
          emptyText={available.length === 0 ? t("boards.addEntryAllOn") : undefined}
        />
        {nodeIds.length > 1 && <span className="gx-boards__add-hint">{t("boards.reorderHint")}</span>}
      </div>
    </div>
  );
}
