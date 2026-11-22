import { useMemo, useState } from "react";
import { useQuery } from "@connectrpc/connect-query";
import { BookOpen, ChevronDown, ChevronRight, ClipboardList, ExternalLink, Lock } from "lucide-react";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import type { Board, Node } from "@gen/glyphoxa/management/v1/management_pb";
import { Button } from "@/components/ui/Button";
import { EmptyState } from "@/components/ui/EmptyState";
import { Markdown } from "@/components/ui/Markdown";
import { QueryBoundary } from "@/components/ui/QueryBoundary";
import { Select } from "@/components/ui/Select";
import { Skeleton } from "@/components/ui/Skeleton";
import { useScreenNav } from "@/app/screenNav";
import { useI18n } from "@/i18n";

// SessionBoard is the Session screen's view of a prep board (#543): the
// shortlist of wiki entries the GM set aside for tonight, in the order they
// expect to need them.
//
// It is READ-ONLY on purpose. At the table a board is a reference — the names,
// the order, and what each entry says — not a place to start editing the wiki
// mid-scene. Boards are made and filled from the World wiki; here the GM picks
// which one is "tonight's" (remembered in this browser, per campaign), reads an
// entry in place, and can step out to the wiki for the full page.

// The board last shown for a campaign. Client-side like the last voice channel:
// a per-GM convenience, not shared configuration. Storage can be unavailable,
// so every access is guarded and the picker simply falls back to the first
// board.
const storageKey = (campaignId: string) => `gx-session-board:${campaignId}`;

function readBoard(campaignId: string): string | null {
  if (!campaignId) return null;
  try {
    return localStorage.getItem(storageKey(campaignId)) || null;
  } catch {
    return null;
  }
}

function rememberBoard(campaignId: string, boardId: string): void {
  if (!campaignId || !boardId) return;
  try {
    localStorage.setItem(storageKey(campaignId), boardId);
  } catch {
    // storage unavailable — the pick still applies to this visit
  }
}

export function SessionBoard({ campaignId }: { campaignId: string }) {
  const { t } = useI18n();
  const nav = useScreenNav();
  const boardsQuery = useQuery(CampaignService.method.listBoards, {});

  const openWiki = () => nav.goCampaignView("knowledge");

  return (
    <QueryBoundary
      query={boardsQuery}
      skeleton={<Skeleton lines={4} />}
      errorTitle={t("session.boardLoadFailed")}
      isEmpty={(data) => data.boards.length === 0}
      empty={
        <EmptyState
          icon={<ClipboardList size={22} />}
          title={t("session.boardNoneTitle")}
          description={t("session.boardNoneDesc")}
          action={
            <Button variant="secondary" size="sm" iconStart={<BookOpen size={14} />} onClick={openWiki}>
              {t("session.boardOpenWiki")}
            </Button>
          }
        />
      }
    >
      {(data) => <Boards boards={data.boards} campaignId={campaignId} onOpenWiki={openWiki} />}
    </QueryBoundary>
  );
}

function Boards({
  boards,
  campaignId,
  onOpenWiki,
}: {
  boards: Board[];
  campaignId: string;
  onOpenWiki: () => void;
}) {
  const { t } = useI18n();
  const nav = useScreenNav();
  const nodesQuery = useQuery(CampaignService.method.listNodes, {});
  const nodesByID = useMemo(() => {
    const m = new Map<string, Node>();
    for (const n of nodesQuery.data?.nodes ?? []) m.set(n.id, n);
    return m;
  }, [nodesQuery.data]);

  // What the GM picked on this visit, else what they last picked for this
  // campaign, else the first board. A remembered board that was deleted since
  // simply is not in the list any more.
  const [picked, setPicked] = useState<string | null>(null);
  const remembered = useMemo(() => readBoard(campaignId), [campaignId]);
  const board =
    boards.find((b) => b.id === picked) ?? boards.find((b) => b.id === remembered) ?? boards[0];

  const [openId, setOpenId] = useState<string | null>(null);

  const pick = (id: string) => {
    setPicked(id);
    setOpenId(null);
    rememberBoard(campaignId, id);
  };

  return (
    <div className="gx-session-board" data-testid="session-board">
      <div className="gx-session-board__head">
        {boards.length > 1 ? (
          <Select
            aria-label={t("session.boardPick")}
            options={boards.map((b) => ({ value: b.id, label: b.name }))}
            value={board.id}
            onValueChange={pick}
          />
        ) : (
          <h3 className="gx-session-board__name">{board.name}</h3>
        )}
        <span className="gx-session-board__count">
          {board.nodeIds.length === 1
            ? t("session.boardEntriesOne")
            : t("session.boardEntries", { n: board.nodeIds.length })}
        </span>
      </div>

      {board.nodeIds.length === 0 ? (
        <EmptyState
          size="inline"
          icon={<ClipboardList size={16} />}
          title={t("session.boardEmptyTitle")}
          description={t("session.boardEmptyDesc")}
          action={
            <Button variant="ghost" size="sm" iconStart={<BookOpen size={14} />} onClick={onOpenWiki}>
              {t("session.boardOpenWiki")}
            </Button>
          }
        />
      ) : (
        <ol className="gx-session-board__list">
          {board.nodeIds.map((id, i) => {
            const node = nodesByID.get(id);
            if (!node) {
              // While the entry list still loads a missing entry means "not
              // resolved yet", never "deleted".
              return (
                <li key={id} className="gx-session-board__entry gx-session-board__entry--missing">
                  <span className="gx-session-board__index">{i + 1}</span>
                  {nodesQuery.isPending ? (
                    <Skeleton lines={1} />
                  ) : nodesQuery.isError ? (
                    <span>{t("session.boardEntryUnavailable")}</span>
                  ) : (
                    <span>{t("session.boardEntryDeleted")}</span>
                  )}
                </li>
              );
            }
            const open = openId === id;
            const panelId = `gx-board-peek-${id}`;
            return (
              <li key={id} className="gx-session-board__entry" data-open={open || undefined}>
                <span className="gx-session-board__index">{i + 1}</span>
                <div className="gx-session-board__body">
                  <button
                    type="button"
                    className="gx-session-board__toggle"
                    aria-expanded={open}
                    aria-controls={panelId}
                    onClick={() => setOpenId(open ? null : id)}
                  >
                    {open ? <ChevronDown size={15} aria-hidden="true" /> : <ChevronRight size={15} aria-hidden="true" />}
                    <span className="gx-session-board__entry-name">{node.name}</span>
                    {node.gmPrivate && (
                      <span className="gx-session-board__private">
                        <Lock size={12} aria-hidden="true" />
                        {t("session.boardGmOnly")}
                      </span>
                    )}
                  </button>
                  {open && (
                    <div className="gx-session-board__peek" id={panelId}>
                      {node.body.trim() !== "" ? (
                        <Markdown text={node.body} />
                      ) : (
                        <p className="gx-session-board__nobody">{t("session.boardEntryNoText")}</p>
                      )}
                      {node.aspects.length > 0 && (
                        <dl className="gx-session-board__aspects">
                          {node.aspects.map((a) => (
                            <div key={a.id}>
                              <dt>{a.key}</dt>
                              <dd>{a.value}</dd>
                            </div>
                          ))}
                        </dl>
                      )}
                      <Button
                        variant="ghost"
                        size="sm"
                        iconStart={<ExternalLink size={14} />}
                        onClick={() => nav.goCampaignView("knowledge", { node: id })}
                      >
                        {t("session.boardOpenInWiki")}
                      </Button>
                    </div>
                  )}
                </div>
              </li>
            );
          })}
        </ol>
      )}
    </div>
  );
}
