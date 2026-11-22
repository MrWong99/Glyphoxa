import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { createClient } from "@connectrpc/connect";
import { useQuery, useTransport } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { Check, Lightbulb, TriangleAlert, X } from "lucide-react";
import { toast } from "sonner";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import type { GraphNode, KnowledgeProposal } from "@gen/glyphoxa/management/v1/management_pb";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import { EmptyState } from "@/components/ui/EmptyState";
import { QueryBoundary } from "@/components/ui/QueryBoundary";
import { Skeleton } from "@/components/ui/Skeleton";
import { toastUndo } from "@/components/ui/toast";
import { useI18n, type TFunc } from "@/i18n";
import { errorMessage, failedPreconditionMessage } from "@/lib/connectError";
import { rosterRetry, useCampaignScreen } from "./campaignContext";
import { resolveProposals, type ResolvedProposal } from "./graph/proposalGhosts";
import { invalidateProposalReview } from "./knowledgeCache";
import { edgeLabel } from "./knowledgeVocab";
import { KindBadge, ProposalWrite, SimilarHint, fmtWhen } from "./proposalParts";

import "./inbox.css";

// The Suggestions inbox (#300, ADR-0052): the GM's review queue for the
// Knowledge Proposals an Agent's remember_knowledge call files. Nothing enters
// the World wiki without the GM adding it here (or on the relationship map,
// which reviews the same queue through the same RPCs).
//
// What makes it an inbox a GM can clear after a session, not a list:
//
//   - suggestions are grouped by the entry they are about, resolved against
//     the wiki with the server's own rules (graph/proposalGhosts), so the GM
//     reviews "everything about Bart" together, can open Bart to check, and is
//     told BEFORE clicking when a suggestion will be refused;
//   - filters by kind and author, and a selection with "Add selected" /
//     "Dismiss selected": one confirm, then one call per suggestion in turn,
//     with progress, a by-name summary of what failed, and ONE refetch;
//   - one-click dismiss with an Undo, and J/K/A/X on the keyboard.
//
// A batch is sequential calls on purpose: approval stays the one, unchanged
// server path ADR-0052 fixes (an atomic batch RPC and edit-before-approve are
// deferred). The similarity hint stays opt-in per card, as it embeds the
// subject through a provider call.

/** How long a dismissed suggestion can be taken back. */
const DISMISS_UNDO_MS = 8000;

// One cache entry shared with the shell's count and the Overview; like theirs,
// this observer does not retry the settled "no campaign yet" answer.
const retryUnlessNotFound = rosterRetry;

type Kind = "fact" | "edge" | "node" | "unreadable";
type BatchAction = "approve" | "reject";

type Group = { key: string; title: string; nodeID?: string };

// One pending suggestion, with everything the list derives about it.
type Item = {
  proposal: KnowledgeProposal;
  resolved?: ResolvedProposal;
  kind: Kind;
  author: string;
  /** What it is about, short: names the row in a failure summary. */
  name: string;
  /** The write in one plain line, for the checkbox's accessible name. */
  summary: string;
  group: Group;
  /** Why adding it now would be refused, already translated. */
  blocked?: string;
};

type Failure = { id: string; name: string; reason: string };
type RowError = { message: string; announce: boolean };

const KIND_ORDER: Kind[] = ["fact", "edge", "node", "unreadable"];
const KIND_LABEL = {
  fact: "inbox.kindFacts",
  edge: "inbox.kindConnections",
  node: "inbox.kindEntries",
  unreadable: "inbox.kindUnreadable",
} as const;

export function ProposalsPanel({
  undoWindowMs = DISMISS_UNDO_MS,
}: {
  /** How long a dismissal waits for Undo before it is sent. */
  undoWindowMs?: number;
}) {
  const { t } = useI18n();
  const proposalsQ = useQuery(
    CampaignService.method.listKnowledgeProposals,
    {},
    { retry: retryUnlessNotFound },
  );

  return (
    <QueryBoundary
      query={proposalsQ}
      errorTitle={t("inbox.loadError")}
      skeleton={<Skeleton.Row count={4} avatar={false} />}
      isEmpty={(data) => data.proposals.length === 0}
      empty={<InboxEmpty />}
    >
      {(data) => <Inbox proposals={data.proposals} undoWindowMs={undoWindowMs} />}
    </QueryBoundary>
  );
}

// An empty inbox is where the feature explains itself: where suggestions come
// from, and that the GM is the one who decides.
function InboxEmpty() {
  const { t } = useI18n();
  const { openView } = useCampaignScreen();
  return (
    <EmptyState
      icon={<Lightbulb size={22} />}
      title={t("inbox.emptyTitle")}
      description={t("inbox.emptyDesc")}
      action={
        <Button variant="secondary" onClick={() => openView("planning")}>
          {t("inbox.emptyAction")}
        </Button>
      }
    />
  );
}

function Inbox({ proposals, undoWindowMs }: { proposals: KnowledgeProposal[]; undoWindowMs: number }) {
  const { t } = useI18n();
  const { openNode, openView } = useCampaignScreen();
  const queryClient = useQueryClient();
  const transport = useTransport();
  const client = useMemo(() => createClient(CampaignService, transport), [transport]);

  // The wiki the suggestions are resolved against. Its failure costs the
  // grouping-by-entry, the links and the refusal forecast — not the inbox.
  const graphQ = useQuery(CampaignService.method.getKnowledgeGraph, {});

  // Suggestions already acted on in this view: added (the server has them) or
  // dismissed (sent, or waiting out their Undo). They leave the list at once
  // instead of when the refetch lands.
  const [approved, setApproved] = useState<ReadonlySet<string>>(new Set());
  const [dismissed, setDismissed] = useState<ReadonlySet<string>>(new Set());
  const [busy, setBusy] = useState<ReadonlySet<string>>(new Set());
  // Why a suggestion could not be added, shown on its row. `announce` is false
  // for a batch's failures: the summary above the list reads them out once,
  // and a dozen rows doing the same at the same moment would be noise.
  const [errors, setErrors] = useState<ReadonlyMap<string, RowError>>(new Map());
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const [kindFilter, setKindFilter] = useState<Kind | "all">("all");
  const [authorFilter, setAuthorFilter] = useState<string | "all">("all");
  const [confirm, setConfirm] = useState<BatchAction | null>(null);
  const [batch, setBatch] = useState<{ action: BatchAction; done: number; total: number } | null>(null);
  const [failures, setFailures] = useState<{ action: BatchAction; items: Failure[] } | null>(null);
  // The suggestion J/K/A/X act on.
  const [cursor, setCursor] = useState<string | null>(null);

  const mounted = useRef(true);
  const rows = useRef(new Map<string, HTMLLIElement>());
  // Dismissals waiting out their Undo window, each with the call that sends it.
  const pendingDismissals = useRef(new Map<string, { timer: ReturnType<typeof setTimeout>; send: () => void }>());

  useEffect(() => {
    mounted.current = true;
    const pending = pendingDismissals.current;
    return () => {
      mounted.current = false;
      // Leaving the view ends the Undo window: what the GM dismissed is sent
      // now, not dropped.
      for (const { timer, send } of [...pending.values()]) {
        clearTimeout(timer);
        send();
      }
    };
  }, []);

  // ---- Derivation -----------------------------------------------------------

  const items = useMemo(() => {
    // A dismissed suggestion no longer counts: an edge into an entry it would
    // have created is back to naming nothing.
    const open = proposals.filter((p) => !dismissed.has(p.id));
    return describe(open, graphQ.data?.nodes, t).filter((item) => !approved.has(item.proposal.id));
  }, [proposals, dismissed, approved, graphQ.data, t]);

  const kinds = KIND_ORDER.filter((k) => items.some((i) => i.kind === k));
  const authors = [...new Set(items.map((i) => i.author))];
  // A filter whose last suggestion is gone falls back to "all" rather than
  // leaving an empty list behind a chip that no longer exists.
  const kind = kindFilter !== "all" && kinds.includes(kindFilter) ? kindFilter : "all";
  const author = authorFilter !== "all" && authors.includes(authorFilter) ? authorFilter : "all";
  const shown = items.filter(
    (i) => (kind === "all" || i.kind === kind) && (author === "all" || i.author === author),
  );
  const groups = groupItems(shown);
  // Display order: the order J/K walk.
  const ordered = groups.flatMap((g) => g.items);
  // The selection only ever means what is on screen and checked.
  const chosen = ordered.filter((i) => selected.has(i.proposal.id));
  const current = ordered.find((i) => i.proposal.id === cursor) ?? null;

  // ---- Cursor ---------------------------------------------------------------

  const focusRow = useCallback((id: string | null) => {
    setCursor(id);
    if (id === null) return;
    const row = rows.current.get(id);
    row?.focus();
    // jsdom has no scrollIntoView.
    row?.scrollIntoView?.({ block: "nearest" });
  }, []);

  // After a suggestion leaves the list, the one after it (or, at the end, the
  // one before) takes the cursor, so A A A walks down the queue.
  const focusAfterCommit = useRef<string | null | undefined>(undefined);
  // moveOn runs after an await, so it reads the list as it is NOW, not as it
  // was in the render that started the call.
  const latest = useRef({ ordered, cursor });
  latest.current = { ordered, cursor };
  const moveOn = (id: string) => {
    const { ordered: list, cursor: at } = latest.current;
    const index = list.findIndex((i) => i.proposal.id === id);
    const next = index === -1 ? null : ((list[index + 1] ?? list[index - 1])?.proposal.id ?? null);
    // Keyboard focus follows only when it was on the row that is leaving (or
    // nowhere): a call that settles while the GM is busy in another row must
    // not pull focus out from under them.
    if (holdsFocus(id)) focusAfterCommit.current = next;
    else if (at === id) setCursor(next);
  };
  // True when keyboard focus is inside the row, or has been dropped to the
  // page (a busy button is disabled, and a browser takes focus off a control
  // that becomes disabled).
  const holdsFocus = (id: string) => {
    const active = document.activeElement;
    return active === null || active === document.body || (rows.current.get(id)?.contains(active) ?? false);
  };
  // Runs after every commit: the row is removed by the state change that
  // follows moveOn, and the next row can only take focus once that is on screen.
  useEffect(() => {
    if (focusAfterCommit.current === undefined) return;
    const next = focusAfterCommit.current;
    focusAfterCommit.current = undefined;
    focusRow(next);
  });

  // ---- Single actions -------------------------------------------------------

  const setError = (id: string, message: string | null) =>
    setErrors((prev) => {
      const next = new Map(prev);
      if (message === null) next.delete(id);
      else next.set(id, { message, announce: true });
      return next;
    });
  const withID = (prev: ReadonlySet<string>, id: string, present: boolean) => {
    const next = new Set(prev);
    if (present) next.add(id);
    else next.delete(id);
    return next;
  };

  const approveOne = async (item: Item) => {
    const id = item.proposal.id;
    if (batch || busy.has(id)) return;
    setBusy((prev) => withID(prev, id, true));
    setError(id, null);
    try {
      await client.approveKnowledgeProposal({ id });
      // The write landed: the queue and every read of the wiki are stale.
      invalidateProposalReview(queryClient);
      if (!mounted.current) return;
      moveOn(id);
      setApproved((prev) => withID(prev, id, true));
      setSelected((prev) => withID(prev, id, false));
    } catch (err) {
      if (!mounted.current) return;
      // A refusal carries the server's actionable reason; anything else is a
      // plain failure. Either way the suggestion stays, with the line under it.
      setError(id, failedPreconditionMessage(err) ?? t("knowledge.addToWikiError", { message: errorMessage(err) }));
      // The button was disabled while busy, which drops keyboard focus to the
      // page; put it back on the row the reason is shown in.
      if (document.activeElement === document.body) rows.current.get(id)?.focus();
    } finally {
      if (mounted.current) setBusy((prev) => withID(prev, id, false));
    }
  };

  // One click, no dialog: the suggestion leaves the list and a toast offers
  // Undo. The reject is only sent when the Undo window closes (or the GM
  // leaves the view), because the server has no way to bring one back.
  const dismissOne = (item: Item) => {
    const id = item.proposal.id;
    if (batch || busy.has(id) || pendingDismissals.current.has(id)) return;
    setError(id, null);
    moveOn(id);
    setDismissed((prev) => withID(prev, id, true));
    setSelected((prev) => withID(prev, id, false));

    const restore = () => {
      if (mounted.current) setDismissed((prev) => withID(prev, id, false));
    };
    let toastID: string | number | undefined;
    const send = () => {
      pendingDismissals.current.delete(id);
      // Sent: Undo can no longer do anything, so it is no longer offered.
      if (toastID !== undefined) toast.dismiss(toastID);
      client.rejectKnowledgeProposal({ id }).then(
        () => invalidateProposalReview(queryClient),
        (err: unknown) => {
          // It is still pending on the server: put it back and say why.
          restore();
          toast.error(t("knowledge.dismissError", { message: errorMessage(err) }));
        },
      );
    };
    const timer = setTimeout(send, undoWindowMs);
    pendingDismissals.current.set(id, { timer, send });
    toastID = toastUndo(
      t("inbox.dismissedOne"),
      () => {
        const pending = pendingDismissals.current.get(id);
        if (!pending) return; // already sent
        clearTimeout(pending.timer);
        pendingDismissals.current.delete(id);
        restore();
      },
      // The toast outlives the window by a moment and is closed by `send`, so
      // the two can never disagree about whether Undo still works.
      { description: item.summary, duration: undoWindowMs + 1000 },
    );
  };

  // ---- Batch ----------------------------------------------------------------

  const runBatch = async (action: BatchAction, picked: Item[]) => {
    // New entries first: a fact or connection about an entry suggested in the
    // same queue can only land once that entry exists.
    const queue =
      action === "approve"
        ? [...picked.filter((i) => i.kind === "node"), ...picked.filter((i) => i.kind !== "node")]
        : picked;
    setFailures(null);
    setBatch({ action, done: 0, total: queue.length });
    const landed: string[] = [];
    const failed: Failure[] = [];
    for (const [index, item] of queue.entries()) {
      const id = item.proposal.id;
      try {
        if (action === "approve") await client.approveKnowledgeProposal({ id });
        else await client.rejectKnowledgeProposal({ id });
        landed.push(id);
      } catch (err) {
        failed.push({ id, name: item.name, reason: failedPreconditionMessage(err) ?? errorMessage(err) });
      }
      if (mounted.current) setBatch({ action, done: index + 1, total: queue.length });
    }
    // One invalidation for the whole batch, not one per suggestion.
    if (landed.length > 0) invalidateProposalReview(queryClient);
    if (!mounted.current) return;

    const gone = new Set(landed);
    if (action === "approve") setApproved((prev) => new Set([...prev, ...gone]));
    else setDismissed((prev) => new Set([...prev, ...gone]));
    setSelected((prev) => new Set([...prev].filter((id) => !gone.has(id))));
    setErrors((prev) => {
      const next = new Map(prev);
      for (const id of gone) next.delete(id);
      for (const f of failed) next.set(f.id, { message: f.reason, announce: false });
      return next;
    });
    setBatch(null);
    setFailures(failed.length > 0 ? { action, items: failed } : null);
    if (landed.length > 0) {
      const n = landed.length;
      toast.success(
        action === "approve"
          ? n === 1
            ? t("inbox.addedOne")
            : t("inbox.added", { n })
          : n === 1
            ? t("inbox.dismissedOne")
            : t("inbox.dismissed", { n }),
      );
    }
  };

  // ---- Keyboard -------------------------------------------------------------

  // J/K/A/X are single-character shortcuts, so they are live only while
  // keyboard focus is inside the inbox (WCAG 2.1.4): typed anywhere else on the
  // page they do nothing, and a speech-input user cannot trigger them by
  // accident. Tab reaches the list (the current row is a tab stop); a click on
  // a row puts focus there too.
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey) return;
    const key = e.key.toLowerCase();
    if (key !== "j" && key !== "k" && key !== "a" && key !== "x") return;
    // React bubbles events out of a portal: a key pressed in the confirm
    // dialog is not a key pressed in the inbox.
    if (!(e.target instanceof Node) || !e.currentTarget.contains(e.target)) return;
    if (isTypingTarget(e.target)) return;
    if (batch || confirm || ordered.length === 0) return;
    e.preventDefault();
    if (key === "j" || key === "k") {
      const at = current ? ordered.indexOf(current) : -1;
      const next = at === -1 ? 0 : Math.min(ordered.length - 1, Math.max(0, at + (key === "j" ? 1 : -1)));
      focusRow(ordered[next].proposal.id);
    } else if (current) {
      if (key === "a") void approveOne(current);
      else dismissOne(current);
    }
  };

  // ---- Render ---------------------------------------------------------------

  const allChosen = ordered.length > 0 && chosen.length === ordered.length;
  const selectAll = useRef<HTMLInputElement>(null);
  useEffect(() => {
    // indeterminate is a DOM property, not an attribute.
    if (selectAll.current) selectAll.current.indeterminate = chosen.length > 0 && !allChosen;
  }, [chosen.length, allChosen]);

  const working = batch !== null;
  const count = items.length;

  // The list waits for the wiki's FIRST answer. Rendered before it, every
  // suggestion would sit under the name it carries and then jump under the
  // entry that name resolves to — taking an opened similarity hint and the
  // keyboard's place with it. It waits for one attempt only: once that has
  // failed (or cannot be made) the list is shown as it is, and it stays shown
  // through the retries, because a queue the GM can read by name is worth more
  // than a placeholder.
  const [wikiAnswered, setWikiAnswered] = useState(false);
  if (
    !wikiAnswered &&
    (graphQ.data !== undefined || graphQ.isError || graphQ.failureCount > 0 || graphQ.fetchStatus === "paused")
  ) {
    setWikiAnswered(true);
  }
  if (!wikiAnswered) return <Skeleton.Row count={Math.min(proposals.length, 4)} avatar={false} />;

  // Everything was acted on. (The refetch may not have landed yet, and a
  // dismissal may still be waiting out its Undo: either way nothing is pending
  // on screen, and Undo brings a dismissed one back.)
  if (count === 0 && !working && !failures) return <InboxEmpty />;

  // The row Tab lands on: the cursor's, or the first while there is none.
  const tabStop = current?.proposal.id ?? ordered[0]?.proposal.id;

  return (
    // The handler is for keys that bubble up from the rows and controls
    // inside; the container itself takes no focus.
    <div className="gx-inbox" onKeyDown={onKeyDown}>
      <div className="gx-inbox__head">
        <h2 className="gx-inbox__title">
          {count === 1 ? t("inbox.pendingOne") : t("inbox.pending", { n: count })}
        </h2>
        <p className="gx-inbox__shortcuts">{t("inbox.shortcuts")}</p>
      </div>

      {(kinds.length > 1 || authors.length > 1) && (
        <div className="gx-inbox__filters">
          {kinds.length > 1 && (
            <FilterChips
              label={t("inbox.filterKind")}
              value={kind}
              onChange={setKindFilter}
              options={[
                { value: "all" as const, label: t("inbox.filterAll"), count: items.length },
                ...kinds.map((k) => ({
                  value: k,
                  label: t(KIND_LABEL[k]),
                  count: items.filter((i) => i.kind === k).length,
                })),
              ]}
            />
          )}
          {authors.length > 1 && (
            <FilterChips
              label={t("inbox.filterAuthor")}
              value={author}
              onChange={setAuthorFilter}
              options={[
                { value: "all", label: t("inbox.filterAll"), count: items.length },
                ...authors.map((a) => ({
                  value: a,
                  label: a,
                  count: items.filter((i) => i.author === a).length,
                })),
              ]}
            />
          )}
        </div>
      )}

      {graphQ.data === undefined && (
        // Nothing while the wiki loads; its failure, with a retry, if it fails.
        <QueryBoundary query={graphQ} errorTitle={t("inbox.graphError")} errorSize="inline" skeleton={<></>}>
          {null}
        </QueryBoundary>
      )}

      {ordered.length > 0 && (
        <div className="gx-inbox__bulk">
          <label className="gx-inbox__select-all">
            <input
              ref={selectAll}
              type="checkbox"
              className="gx-inbox__check"
              checked={allChosen}
              disabled={working}
              onChange={(e) =>
                setSelected((prev) => {
                  const next = new Set(prev);
                  for (const i of ordered) {
                    if (e.target.checked) next.add(i.proposal.id);
                    else next.delete(i.proposal.id);
                  }
                  return next;
                })
              }
            />
            <span>{t("inbox.selectAll", { n: ordered.length })}</span>
          </label>
          {/* Always in the tree, so a screen reader hears the count change. */}
          <span className="gx-inbox__selected" role="status">
            {chosen.length > 0 ? t("inbox.selectedCount", { n: chosen.length }) : ""}
          </span>
          <div className="gx-inbox__bulk-actions">
            <Button
              size="sm"
              iconStart={<Check />}
              disabled={chosen.length === 0 || working}
              onClick={() => setConfirm("approve")}
            >
              {t("inbox.addSelected")}
            </Button>
            <Button
              variant="danger"
              size="sm"
              iconStart={<X />}
              disabled={chosen.length === 0 || working}
              onClick={() => setConfirm("reject")}
            >
              {t("inbox.dismissSelected")}
            </Button>
          </div>
        </div>
      )}

      {batch && (
        <div className="gx-inbox__progress" role="status">
          <span>
            {t(batch.action === "approve" ? "inbox.progressAdd" : "inbox.progressDismiss", {
              done: batch.done,
              total: batch.total,
            })}
          </span>
          <span
            className="gx-inbox__bar"
            role="progressbar"
            aria-valuemin={0}
            aria-valuemax={batch.total}
            aria-valuenow={batch.done}
            aria-label={t(batch.action === "approve" ? "inbox.addSelected" : "inbox.dismissSelected")}
          >
            <span style={{ width: `${(batch.done / Math.max(1, batch.total)) * 100}%` }} />
          </span>
        </div>
      )}

      {failures && (
        <Alert
          tone="danger"
          title={failureTitle(failures.action, failures.items.length, t)}
          // What could not be added usually needs an entry created or renamed
          // first: the wiki is where that is done.
          action={
            failures.action === "approve" ? (
              <Button variant="secondary" size="sm" onClick={() => openView("knowledge")}>
                {t("home.attnWorldAction")}
              </Button>
            ) : undefined
          }
          onDismiss={() => setFailures(null)}
        >
          <ul className="gx-inbox__failures">
            {failures.items.map((f) => (
              <li key={f.id}>{t("inbox.failedItem", { name: f.name, reason: f.reason })}</li>
            ))}
          </ul>
        </Alert>
      )}

      {ordered.length === 0 ? (
        count > 0 && (
          // Two filters that no suggestion satisfies together.
          <EmptyState
            size="inline"
            title={t("inbox.noMatchTitle")}
            action={
              <Button
                variant="secondary"
                size="sm"
                onClick={() => {
                  setKindFilter("all");
                  setAuthorFilter("all");
                }}
              >
                {t("inbox.clearFilters")}
              </Button>
            }
          />
        )
      ) : (
        <div className="gx-inbox__groups" aria-label={t("inbox.listLabel")} role="group">
          {groups.map((g) => (
            // A group, not a <section>: a named section is a landmark, and a
            // long queue would bury the page's real ones under one per entry.
            <div key={g.group.key} className="gx-inbox__group" role="group" aria-label={g.group.title}>
              <h3 className="gx-inbox__group-title">
                {g.group.nodeID ? (
                  <button
                    type="button"
                    className="gx-proposal-card__entry"
                    aria-label={t("inbox.openEntry", { name: g.group.title })}
                    onClick={() => openNode(g.group.nodeID as string)}
                  >
                    {g.group.title}
                  </button>
                ) : (
                  g.group.title
                )}
                <span className="gx-inbox__group-count">{g.items.length}</span>
              </h3>
              <ul className="gx-inbox__list">
                {g.items.map((item) => {
                  const id = item.proposal.id;
                  return (
                    <SuggestionRow
                      key={id}
                      item={item}
                      current={cursor === id}
                      tabStop={tabStop === id}
                      selected={selected.has(id)}
                      busy={busy.has(id)}
                      disabled={working}
                      error={errors.get(id)}
                      rowRef={(el) => {
                        if (el) rows.current.set(id, el);
                        else rows.current.delete(id);
                      }}
                      onFocusRow={() => setCursor(id)}
                      onSelect={(on) => setSelected((prev) => withID(prev, id, on))}
                      onApprove={() => void approveOne(item)}
                      onDismiss={() => dismissOne(item)}
                      onOpenNode={openNode}
                    />
                  );
                })}
              </ul>
            </div>
          ))}
        </div>
      )}

      {confirm && (
        <ConfirmDialog
          open
          onOpenChange={(open) => {
            if (!open) setConfirm(null);
          }}
          destructive={confirm === "reject"}
          title={confirmTitle(confirm, chosen.length, t)}
          description={t(confirm === "approve" ? "inbox.confirmAddBody" : "inbox.confirmDismissBody")}
          confirmLabel={t(confirm === "approve" ? "inbox.confirmAdd" : "inbox.confirmDismiss")}
          confirmDisabled={chosen.length === 0}
          onConfirm={() => {
            const action = confirm;
            setConfirm(null);
            void runBatch(action, chosen);
          }}
        />
      )}
    </div>
  );
}

// ---- Row ----------------------------------------------------------------------

function SuggestionRow({
  item,
  current,
  tabStop,
  selected,
  busy,
  disabled,
  error,
  rowRef,
  onFocusRow,
  onSelect,
  onApprove,
  onDismiss,
  onOpenNode,
}: {
  item: Item;
  /** The row J/K/A/X act on. */
  current: boolean;
  /** The one row Tab stops on (the others are reached with J/K). */
  tabStop: boolean;
  selected: boolean;
  busy: boolean;
  /** A batch is running: the row's own controls wait for it. */
  disabled: boolean;
  error?: RowError;
  rowRef: (el: HTMLLIElement | null) => void;
  onFocusRow: () => void;
  onSelect: (on: boolean) => void;
  onApprove: () => void;
  onDismiss: () => void;
  onOpenNode: (nodeID: string) => void;
}) {
  const { t, lang } = useI18n();
  const { proposal } = item;
  return (
    <li
      ref={rowRef}
      className="gx-card gx-proposal-card gx-inbox__row"
      // A roving tab stop: Tab enters the list on one row, J and K move between
      // rows, and Tab again goes on to that row's own controls.
      tabIndex={tabStop ? 0 : -1}
      aria-current={current ? "true" : undefined}
      data-selected={selected ? "true" : undefined}
      onFocus={onFocusRow}
    >
      <div className="gx-proposal-card__head">
        <input
          type="checkbox"
          className="gx-inbox__check"
          checked={selected}
          disabled={disabled}
          aria-label={t("inbox.selectOne", { author: item.author, summary: item.summary })}
          onChange={(e) => onSelect(e.target.checked)}
        />
        <span className="gx-proposal-card__author">{proposal.authoringAgentName}</span>
        <span className="gx-proposal-card__when">{fmtWhen(proposal, lang)}</span>
        <KindBadge proposal={proposal} />
      </div>

      <div className="gx-proposal-card__write">
        <ProposalWrite proposal={proposal} resolved={item.resolved?.write} onOpenNode={onOpenNode} />
      </div>

      {item.blocked && (
        <p className="gx-inbox__blocked">
          <TriangleAlert aria-hidden="true" />
          <span>{item.blocked}</span>
        </p>
      )}

      <SimilarHint proposalId={proposal.id} onOpenNode={onOpenNode} />

      <div className="gx-proposal-card__actions">
        <Button
          size="sm"
          iconStart={<Check />}
          loading={busy}
          disabled={disabled}
          // The shortcut acts on the row the cursor is on, and only there.
          aria-keyshortcuts={current ? "A" : undefined}
          onClick={onApprove}
        >
          {t("knowledge.addToWiki")}
        </Button>
        <Button
          variant="danger"
          size="sm"
          iconStart={<X />}
          disabled={disabled || busy}
          aria-keyshortcuts={current ? "X" : undefined}
          onClick={onDismiss}
        >
          {t("knowledge.dismiss")}
        </Button>
        {error && (
          <span
            className="gx-editor__status gx-editor__status--error"
            role={error.announce ? "alert" : undefined}
          >
            {error.message}
          </span>
        )}
      </div>
    </li>
  );
}

// ---- Filter chips -------------------------------------------------------------

// One choice out of a few, as wrapping chips: the kinds and the authors of a
// queue are too many, and too long, for a segmented control on a phone.
function FilterChips<T extends string>({
  label,
  value,
  onChange,
  options,
}: {
  label: string;
  value: T;
  onChange: (value: T) => void;
  options: { value: T; label: string; count: number }[];
}) {
  return (
    <div className="gx-inbox__chips" role="group" aria-label={label}>
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          className="gx-inbox__chip"
          aria-pressed={option.value === value}
          onClick={() => onChange(option.value)}
        >
          {option.label} <span className="gx-inbox__chip-count">{option.count}</span>
        </button>
      ))}
    </div>
  );
}

// ---- Pure helpers -------------------------------------------------------------

function isTypingTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  if (target.isContentEditable) return true;
  if (target instanceof HTMLTextAreaElement || target instanceof HTMLSelectElement) return true;
  // A checkbox or a button takes no text; every other input does.
  return target instanceof HTMLInputElement && target.type !== "checkbox" && target.type !== "radio";
}

const lower = (name: string) => name.trim().toLowerCase();

/**
 * describe turns the pending queue into list items: what each suggestion is
 * about, which entry it belongs under, and whether adding it would be refused.
 *
 * `nodes` is the whole wiki, or undefined while it has not loaded (or could not
 * be). Without it nothing is resolved: suggestions group by the name they
 * carry, no name is a link and no refusal is forecast — a guess there would
 * tell the GM their Butler had invented an entry that exists.
 */
function describe(
  proposals: readonly KnowledgeProposal[],
  nodes: readonly GraphNode[] | undefined,
  t: TFunc,
): Item[] {
  const resolved = nodes ? new Map(resolveProposals(proposals, nodes).map((r) => [r.id, r])) : undefined;
  const nodeName = new Map((nodes ?? []).map((n) => [n.id, n.name]));

  // The group a named endpoint falls under.
  const groupOf = (anchor: SubjectAnchor | undefined, typed: string): Group => {
    if (anchor?.at === "node") {
      return { key: `node:${anchor.id}`, title: nodeName.get(anchor.id) ?? typed, nodeID: anchor.id };
    }
    if (anchor?.at === "ghost") {
      return { key: `new:${lower(anchor.name)}`, title: t("inbox.groupNewEntry", { name: anchor.name }) };
    }
    return typed.trim() === ""
      ? { key: "name:", title: t("inbox.groupNoName") }
      : { key: `name:${lower(typed)}`, title: typed };
  };
  // Why an endpoint stops the write from landing now, if it does.
  const blockedBy = (anchor: SubjectAnchor | undefined): string | undefined => {
    if (anchor?.at === "unknown") {
      return t("inbox.willBeRefused", { reason: t(anchor.reason.key, anchor.reason.params) });
    }
    if (anchor?.at === "ghost") return t("inbox.needsFirst", { name: anchor.name });
    return undefined;
  };

  return proposals.map((proposal): Item => {
    const r = resolved?.get(proposal.id);
    const base = { proposal, resolved: r, author: proposal.authoringAgentName };
    const w = proposal.write;
    switch (w.case) {
      case "fact": {
        const anchor = r?.write.kind === "fact" ? r.write.anchor : undefined;
        return {
          ...base,
          kind: "fact",
          name: w.value.subject,
          summary: t("inbox.summaryFact", { subject: w.value.subject, fact: w.value.fact }),
          group: groupOf(anchor, w.value.subject),
          blocked: blockedBy(anchor),
        };
      }
      case "edge": {
        const from = r?.write.kind === "edge" ? r.write.from : undefined;
        const to = r?.write.kind === "edge" ? r.write.to : undefined;
        const relation = edgeLabel(t, w.value.relation);
        const summary = t("inbox.summaryEdge", {
          subject: w.value.subject,
          relation,
          target: w.value.target,
        });
        return {
          ...base,
          kind: "edge",
          name: summary,
          summary,
          group: groupOf(from, w.value.subject),
          // A name the wiki cannot resolve is the harder stop; report it before
          // a dependency on another suggestion.
          blocked:
            [from, to].map((a) => (a?.at === "unknown" ? blockedBy(a) : undefined)).find(Boolean) ??
            blockedBy(from) ??
            blockedBy(to),
        };
      }
      case "node":
        return {
          ...base,
          kind: "node",
          name: w.value.name,
          summary: t("inbox.summaryNode", { name: w.value.name }),
          group: { key: `new:${lower(w.value.name)}`, title: t("inbox.groupNewEntry", { name: w.value.name }) },
        };
      default:
        return {
          ...base,
          kind: "unreadable",
          name: t("knowledge.unreadableSuggestion"),
          summary: t("knowledge.unreadableSuggestion"),
          group: { key: "unreadable", title: t("inbox.groupUnreadable") },
        };
    }
  });
}

// The anchor type resolveProposals gives an endpoint (not exported there).
type SubjectAnchor = Extract<ResolvedProposal["write"], { kind: "fact" }>["anchor"];

/** groupItems buckets items by their group, in the order groups first appear. */
function groupItems(items: readonly Item[]): { group: Group; items: Item[] }[] {
  const byKey = new Map<string, { group: Group; items: Item[] }>();
  for (const item of items) {
    const bucket = byKey.get(item.group.key);
    if (bucket) bucket.items.push(item);
    else byKey.set(item.group.key, { group: item.group, items: [item] });
  }
  return [...byKey.values()];
}

function confirmTitle(action: BatchAction, n: number, t: TFunc): string {
  if (action === "approve") return n === 1 ? t("inbox.confirmAddTitleOne") : t("inbox.confirmAddTitle", { n });
  return n === 1 ? t("inbox.confirmDismissTitleOne") : t("inbox.confirmDismissTitle", { n });
}

function failureTitle(action: BatchAction, n: number, t: TFunc): string {
  if (action === "approve") return n === 1 ? t("inbox.failedAddOne") : t("inbox.failedAdd", { n });
  return n === 1 ? t("inbox.failedDismissOne") : t("inbox.failedDismiss", { n });
}
