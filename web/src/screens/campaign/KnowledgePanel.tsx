import { useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import { useQuery, useMutation } from "@connectrpc/connect-query";
import { useQueryClient, keepPreviousData } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  BookOpen,
  ChevronDown,
  ChevronRight,
  ClipboardList,
  EyeOff,
  Link as LinkIcon,
  Lock,
  Network,
  Pencil,
  Plus,
  Rows3,
  Search,
  SearchX,
  Sparkles,
  Stethoscope,
  Tags,
  Trash2,
} from "lucide-react";

import { CampaignService, NodeType } from "@gen/glyphoxa/management/v1/management_pb";
import type { Node } from "@gen/glyphoxa/management/v1/management_pb";
import { useCan } from "@/app/capabilities";
import { useGuardedSearchKeys, useScreenNav } from "@/app/screenNav";
import { errorMessage } from "@/lib/connectError";
import { isAtMost, watchBreakpoint } from "@/lib/breakpoints";
import { useI18n } from "@/i18n";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import { Dialog } from "@/components/ui/Dialog";
import { EmptyState } from "@/components/ui/EmptyState";
import { ErrorState } from "@/components/ui/ErrorState";
import { IconButton } from "@/components/ui/IconButton";
import { Input } from "@/components/ui/Input";
import { stripMarkdown } from "@/components/ui/Markdown";
import { Menu, MenuItem, MenuLabel, MenuSeparator } from "@/components/ui/Menu";
import { QueryBoundary } from "@/components/ui/QueryBoundary";
import { SegmentedControl } from "@/components/ui/SegmentedControl";
import { Select } from "@/components/ui/Select";
import { Skeleton } from "@/components/ui/Skeleton";
import { useDirtyGuard } from "@/components/ui/useDirtyGuard";
import { rosterRetry, useCampaignPanelState, useCampaignScreen } from "./campaignContext";
import { EntryEditor } from "./EntryEditor";
import type { EditorFields } from "./EntryEditor";
import { relativeTime, stampMs, updatedAt } from "./entryFormat";
import { EntryView } from "./EntryView";
import { KnowledgeGraph } from "./graph/KnowledgeGraph";
import { WorldHealthPanel } from "./graph/WorldHealthPanel";
import { invalidateKnowledgeReads, writeNodeToList } from "./knowledgeCache";
import { EMPTY_DRAFT, KnowledgeDraftCard } from "./KnowledgeDraftCard";
import type { DraftState } from "./KnowledgeDraftCard";
import { TYPE_META, TYPE_ORDER, alphaBg, metaOf } from "./knowledgeVocab";
import { portraitURL } from "./NodePortrait";
import { PrepBoards } from "./PrepBoards";
import { TagManager } from "./TagManager";

import "./wiki.css";

// The World wiki (#126, #129) on the live CampaignService node RPCs (ADR-0008
// v1.0). An "entry" is the GM-facing name for a Node (the code keeps the Node
// domain term).
//
// It is read-first. One toolbar sits over four ways of looking at the same
// entries (List, Relationship map, Checkup, Boards); opening an entry from any
// of them shows its page beside the list (EntryView), and "Edit" swaps that
// pane to the editor (EntryEditor), which stays open after a save. Below
// 1100px there is no room beside the list, so the same pane opens as a sheet.
//
// WHICH entry is open is the route's ?node=, not state held here. That is what
// makes an entry linkable, lets a jump from another screen (a roster mark, a
// board, the command palette) open it by navigating, and makes the browser's
// Back button step through the entries the GM followed. The key is registered
// as guarded selection state, so a Back that would replace an editor with
// unsaved typing asks first. Whether the open entry is being read or edited,
// and the "new entry" form, are local: they are not places to link to.

type ViewMode = "list" | "graph" | "health" | "boards";
type SortKey = "name" | "updated" | "created";

/** Past this many tags the tag row folds behind a "Tags (n)" toggle. */
const TAG_ROW_LIMIT = 8;
/** How many tag chips a card shows before "+n". */
const CARD_TAG_LIMIT = 3;

const NO_NODES: Node[] = [];

// useStacked is true at and below the "stack" breakpoint, where the detail
// pane has no column of its own and opens as a sheet instead.
function useStacked(): boolean {
  const [stacked, setStacked] = useState(() => isAtMost("stack"));
  useEffect(() => watchBreakpoint("stack", setStacked), []);
  return stacked;
}

export function KnowledgePanel({
  onOpenCast,
  onOpenLine,
}: {
  /** Hands a cast Agent back to the Cast view, where its fix lives. */
  onOpenCast?: (agentID: string) => void;
  /** Jump to one Transcript Line on the Session screen (mentions, #545). */
  onOpenLine?: (sessionID: string, lineID: string) => void;
} = {}) {
  const { t, lang } = useI18n();
  const queryClient = useQueryClient();
  const nav = useScreenNav();
  const { openAgent, campaign } = useCampaignScreen();
  // What this principal may do here (ADR-0065). The GM: everything, as ever. A
  // Campaign Guest reads the wiki with wiki.read and gets the editing controls
  // only with wiki.edit; AI drafting, the checkup and everything about the
  // cast stay the GM's, whatever the guest's rights.
  const can = useCan();
  useGuardedSearchKeys("node");
  const stacked = useStacked();

  const listQuery = useQuery(CampaignService.method.listNodes, {});
  const allNodes = listQuery.data?.nodes ?? NO_NODES;

  // ── What is open ──────────────────────────────────────────────────────────
  // The open entry is the URL's, resolved against the live list on every
  // render: a portrait upload, a "Voiced by" link or a proposal approval
  // refetches the list and the pane follows.
  const selectedId = nav.search.node ?? null;
  const selected = useMemo(
    () => (selectedId ? (allNodes.find((n) => n.id === selectedId) ?? null) : null),
    [allNodes, selectedId],
  );
  // The entry that is open in the EDITOR. Held by id beside the selection, so
  // moving to another entry (a click, the Back button) lands on its page.
  const [editFor, setEditFor] = useState<string | null>(null);
  // The "new entry" form and the type it starts as. It sits OVER the
  // selection while it is open; cancelling it returns to the entry underneath.
  const [creating, setCreating] = useState<NodeType | null>(null);
  // The last save of an entry that landed, as the server returned it: what the
  // editor that stayed open reseeds its fact ids from.
  const [saved, setSaved] = useState<{ id: string; seq: number; node: Node } | null>(null);
  const saveSeq = useRef(0);

  // The layout the pane is drawn in: beside the list, or as a sheet. It is held
  // while an editor is open. The two are different elements, so a switch would
  // remount the editor and lose everything typed in it, without a question
  // (rotating a tablet, or snapping a window across 1100px, mid-sentence). The
  // pane follows the window again as soon as the editor closes.
  const editorOpen = creating !== null || (editFor !== null && editFor === selectedId);
  const [layoutStacked, setLayoutStacked] = useState(stacked);
  if (!editorOpen && layoutStacked !== stacked) setLayoutStacked(stacked);

  const [pickedMode, setMode] = useCampaignPanelState<ViewMode>("wiki.mode", "list");
  // A mode this principal is not offered (rights that changed under an open
  // panel) falls back to the list rather than rendering a view of refusals.
  const mode: ViewMode =
    (pickedMode === "health" && !can.manage) || (pickedMode === "boards" && !can.wikiEdit)
      ? "list"
      : pickedMode;
  const [sort, setSort] = useCampaignPanelState<SortKey>("wiki.sort", "name");
  const [collapsed, setCollapsed] = useCampaignPanelState<readonly NodeType[]>("wiki.collapsed", []);

  // ── Filters, shared by the list and the map ───────────────────────────────
  // Type chips NARROW: none chosen shows every type, and choosing "Location"
  // shows only locations. One selection drives both views, since "show me the
  // locations" is the same question in each.
  const [types, setTypes] = useState<ReadonlySet<NodeType>>(() => new Set());
  const [activeTags, setActiveTags] = useState<ReadonlySet<string>>(() => new Set());
  const [tagsExpanded, setTagsExpanded] = useState(false);
  const [tagManagerOpen, setTagManagerOpen] = useState(false);
  const tagsQuery = useQuery(CampaignService.method.getCampaignTags, {});
  const tagIndex = useMemo(() => {
    const byNode = new Map<string, string[]>();
    const usage = new Map<string, { tag: string; count: number }>();
    for (const e of tagsQuery.data?.entries ?? []) {
      const list = byNode.get(e.nodeId);
      if (list) list.push(e.tag);
      else byNode.set(e.nodeId, [e.tag]);
      const key = e.tag.toLowerCase();
      const seen = usage.get(key);
      if (seen) seen.count++;
      else usage.set(key, { tag: e.tag, count: 1 });
    }
    return { byNode, vocabulary: [...usage.values()].sort((a, b) => a.tag.localeCompare(b.tag)) };
  }, [tagsQuery.data]);
  // A chosen tag that no longer exists (renamed or merged away in "Manage
  // tags", or its last entry deleted) must not keep filtering to nothing.
  const liveTags = useMemo(() => {
    const known = new Set(tagIndex.vocabulary.map((u) => u.tag.toLowerCase()));
    return new Set([...activeTags].filter((tag) => known.has(tag.toLowerCase())));
  }, [activeTags, tagIndex]);

  // An entry matches when it carries EVERY selected tag: chips narrow, they do
  // not widen. "seafaring AND act two" is the question a GM is asking.
  const matchesTags = useMemo(() => {
    if (liveTags.size === 0) return () => true;
    const want = [...liveTags].map((tag) => tag.toLowerCase());
    return (id: string) => {
      const has = new Set((tagIndex.byNode.get(id) ?? []).map((tag) => tag.toLowerCase()));
      return want.every((tag) => has.has(tag));
    };
  }, [liveTags, tagIndex]);

  // The generator's prompt and its unapplied draft live HERE, not inside the
  // card: a draft is the result of a paid LLM call the GM has not reviewed yet,
  // and nothing the GM does to the view may silently discard it.
  const [draftState, setDraftState] = useState<DraftState>(EMPTY_DRAFT);

  // The whole-graph payload (#534), read only by the map and the checkup: a GM
  // who never opens either pays nothing for it.
  const graphQuery = useQuery(
    CampaignService.method.getKnowledgeGraph,
    {},
    { enabled: mode === "graph" || mode === "health" },
  );
  // The duplicate scan is an exact pairwise comparison the GM waits for, and
  // its result is worked through by opening entries one by one. It is held here
  // so it survives leaving the Checkup to look at one of them.
  const duplicateScan = useMutation(CampaignService.method.findDuplicateEntries);

  // The entry a delete has been requested for; drives the confirm dialog.
  // Delete is a hard, cascading DELETE (ADR-0008), so no DeleteNode fires until
  // the GM confirms here (#209).
  const [confirmNode, setConfirmNode] = useState<Node | null>(null);

  // Live wiki search (#131, ADR-0008 tsvector): the raw box value drives a
  // 200ms-debounced SearchNodes query. The RPC runs only while the debounced
  // query is non-empty (keepPreviousData holds the last matches steady across
  // keystrokes); an empty box falls straight back to the full list.
  const [search, setSearch] = useState("");
  const [debounced, setDebounced] = useState("");
  useEffect(() => {
    const timer = setTimeout(() => setDebounced(search), 200);
    return () => clearTimeout(timer);
  }, [search]);
  const searching = debounced.trim() !== "";
  const searchQuery = useQuery(
    CampaignService.method.searchNodes,
    { query: debounced },
    // retry:false surfaces a failure promptly rather than backing off silently
    // for seconds: a typeahead re-fires on the next keystroke anyway.
    { enabled: searching, placeholderData: keepPreviousData, retry: false },
  );
  // A failed search must NOT silently fall back to the full list: the box
  // still holds a query, so an unfiltered list would look filtered and the GM
  // could act on the wrong entry.
  const searchFailed = searching && searchQuery.isError;
  // The kept matches of the PREVIOUS query (placeholder) are worth showing
  // while the next one is in flight, but an empty set of them is no answer to
  // the query now in the box: "no matches" waits for the server.
  const searchStale = searching && searchQuery.isPlaceholderData;

  // While searching, the matches in the server's relevance order; before the
  // FIRST result lands (nothing to keep) the full list, so the view never
  // flashes empty. An empty match array is a real "no matches".
  const base = searching ? (searchQuery.data?.nodes ?? allNodes) : allNodes;
  const tagged = useMemo(() => base.filter((n) => matchesTags(n.id)), [base, matchesTags]);
  const visible = useMemo(() => {
    const kept = tagged.filter((n) => types.size === 0 || types.has(n.nodeType));
    if (searching) return kept; // relevance order is the server's
    const byName = (a: Node, b: Node) => a.name.localeCompare(b.name, lang);
    const order: Record<SortKey, (a: Node, b: Node) => number> = {
      name: byName,
      updated: (a, b) => stampMs(b.updatedAt) - stampMs(a.updatedAt) || byName(a, b),
      created: (a, b) => stampMs(b.createdAt) - stampMs(a.createdAt) || byName(a, b),
    };
    return [...kept].sort(order[sort]);
  }, [tagged, types, searching, sort, lang]);
  // Counts answer "how many would I see if I chose this type", so they follow
  // the search and the tags but not the type chips themselves.
  const typeCounts = useMemo(() => {
    const counts = new Map<NodeType, number>();
    for (const n of tagged) counts.set(n.nodeType, (counts.get(n.nodeType) ?? 0) + 1);
    return counts;
  }, [tagged]);
  const matchIDs = useMemo(
    () => (searching && searchQuery.data ? new Set(searchQuery.data.nodes.map((n) => n.id)) : null),
    [searching, searchQuery.data],
  );

  const filtersActive = search.trim() !== "" || types.size > 0 || liveTags.size > 0;
  // What the count counts. The map draws every entry the type and tag chips
  // keep and only dims the search's misses, so there the count is of what is
  // drawn: "1 of 40 entries" for a search would claim 39 were taken away.
  const mapDrawn = useMemo(
    () => allNodes.filter((n) => matchesTags(n.id) && (types.size === 0 || types.has(n.nodeType))).length,
    [allNodes, matchesTags, types],
  );
  const countNarrowed = mode === "graph" ? types.size > 0 || liveTags.size > 0 : filtersActive;
  const countShown = mode === "graph" ? mapDrawn : visible.length;
  const clearFilters = () => {
    setSearch("");
    setDebounced("");
    setTypes(new Set());
    setActiveTags(new Set());
  };

  // Every mutation below drops EVERY read of the same data: see
  // invalidateKnowledgeReads. Per-surface subsets are how the graph view went
  // stale on an edge created two components away.
  const invalidate = () => invalidateKnowledgeReads(queryClient);

  const createNode = useMutation(CampaignService.method.createNode);
  const updateNode = useMutation(CampaignService.method.updateNode);
  // A node hard-delete cascades to every edge that touches it (ADR-0008 ON
  // DELETE CASCADE), so it can silently kill edges belonging to OTHER nodes,
  // which is why the whole read set goes, not just this node's.
  const deleteNode = useMutation(CampaignService.method.deleteNode);
  // The three mutations are shared by every entry, so moving to another entry
  // clears their settled status: without this, entry B's editor opened showing
  // entry A's "Couldn't save" as its own.
  const resetMutations = () => {
    createNode.reset();
    updateNode.reset();
    deleteNode.reset();
  };

  // Unsaved typing in the editor is guarded (W10). Every in-panel move that
  // would replace the editor goes through edits.guard(); a move the panel never
  // sees (Back, the sidebar, the palette) is held by the route layer through
  // the same flag and the guarded ?node key.
  const edits = useDirtyGuard();

  // Read at the moment a mutation settles, which may be after the GM moved on.
  const selectedRef = useRef(selectedId);
  selectedRef.current = selectedId;
  // Entries this panel deleted: their id leaving the list is not a broken link.
  const deleted = useRef(new Set<string>());

  // ── Where keyboard focus goes when the pane changes under it ──────────────
  // Swapping the page for the editor, or closing the pane, removes the control
  // that had focus; left alone, focus falls to <body> and the next Tab starts
  // at the top of the app. Each swap names where focus belongs instead, and the
  // effect below puts it there once that element is on screen.
  //
  //   edit  the page's Edit button, after Done or Cancel in the editor
  //   card  the open entry's card in the list, after the pane closed
  //   list  the list itself, when that card is not there to take it
  type FocusTarget = { to: "edit" } | { to: "card"; id: string } | { to: "list" };
  const focusNext = useRef<FocusTarget | null>(null);
  const editButton = useRef<HTMLButtonElement>(null);
  const contentRef = useRef<HTMLDivElement>(null);
  // In the sheet the dialog hands focus back to what opened it; only the
  // side pane, which has no focus scope of its own, needs it placed.
  const closeFocus = (id: string | null): FocusTarget | null =>
    layoutStacked ? null : id ? { to: "card", id } : { to: "list" };

  // openEntry is the ONE way the panel moves to an entry: a card, a map node,
  // a checkup finding, a board chip, a connection's name.
  const openEntry = (id: string, as: "view" | "edit" = "view") => {
    // Already open in the editor: neither its card's name nor its own Edit
    // pencil is a request to throw the edit away. Running the guard here would
    // ask "Discard?", discard nothing (the editor keeps its key and its
    // typing) and leave the guard lifted over an editor that is still dirty.
    if (creating === null && id === selectedId && editFor === id) return;
    edits.guard(() => {
      resetMutations();
      focusNext.current = null;
      setCreating(null);
      setEditFor(as === "edit" ? id : null);
      if (id !== selectedRef.current) nav.setSearch({ node: id });
    });
  };
  const closeEntry = () =>
    edits.guard(() => {
      resetMutations();
      setEditFor(null);
      focusNext.current = closeFocus(selectedRef.current);
      if (selectedRef.current) nav.setSearch({ node: undefined });
    });
  const startCreate = (type: NodeType) => {
    // The form for this type is already open: the same choice again is not a
    // request to start over (and, like the pencil above, would lift the guard
    // over a form that keeps its typing).
    if (creating === type) return;
    edits.guard(() => {
      resetMutations();
      setEditFor(null);
      setCreating(type);
    });
  };
  const leaveCreate = () => edits.guard(() => setCreating(null));

  // The URL moved to another entry (Back, Forward, a jump from elsewhere): the
  // "new entry" form was over the old one and goes with it, and so does an
  // editor left open on another entry (or Forward would reopen that entry in
  // its editor instead of on its page). The editor a move itself asked for
  // (the pencil, a create) was set for the entry the URL now names, and stays.
  const lastSelected = useRef(selectedId);
  useEffect(() => {
    if (lastSelected.current === selectedId) return;
    lastSelected.current = selectedId;
    setCreating(null);
    setEditFor((prev) => (prev === selectedId ? prev : null));
    // A close still waiting to place focus is moot once another entry opened.
    if (selectedId && focusNext.current?.to !== "edit") focusNext.current = null;
  }, [selectedId]);

  // An entry opened from the map, a board or the palette whose type group is
  // folded away would have no card to mark or scroll to: its group opens.
  const collapsedRef = useRef(collapsed);
  collapsedRef.current = collapsed;
  const selectedType = selected?.nodeType;
  useEffect(() => {
    if (selectedType === undefined || !collapsedRef.current.includes(selectedType)) return;
    setCollapsed(collapsedRef.current.filter((x) => x !== selectedType));
  }, [selectedId, selectedType, setCollapsed]);

  // A ?node= that names nothing in this campaign (a stale bookmark, a link
  // from another campaign) says so once and is dropped, instead of leaving a
  // list that looks as if the click did nothing. Decided only once the list
  // has SETTLED: a slow first load or a refetch in flight is not "not found".
  //
  // The sentence names the campaign, so it also waits for the roster read that
  // carries the name (the host's own read, shared through the cache; the same
  // retry rule, since whichever observer mounts first decides how it retries).
  //
  // An entry that WAS open here and then left the list is a different case:
  // it was deleted (here or in another tab), or the Active Campaign was
  // switched underneath the screen. The pane just closes; "not in this
  // campaign" would be noise on top of the switch, and before the roster
  // catches up it would even name the wrong campaign.
  //
  // A list that is settled can still be OLD: it is cached for a while, and an
  // entry made since on another tab or device (a proposal approved on a phone)
  // is not in it, though the palette's server-side search just found it. So
  // an id the list does not know is looked up once more before it is called
  // missing.
  // (A guest's campaign is named by their Workspace; the roster is not theirs.)
  const rosterQuery = useQuery(
    CampaignService.method.getCampaignRoster,
    {},
    { retry: rosterRetry, enabled: can.manage },
  );
  const campaignName = can.manage ? rosterQuery.data?.campaign?.name : campaign?.name;
  const rosterSettled = !can.manage || !rosterQuery.isPending;
  const reported = useRef<string | null>(null);
  // The id a second look was started for, and the id it has come back for.
  // The answer is the refetch's own settling: one that returns the same rows
  // changes nothing else the effect could see.
  const refetchedFor = useRef<string | null>(null);
  const [lookedUp, setLookedUp] = useState<string | null>(null);
  const resolvedOnce = useRef(new Set<string>());
  const { setSearch: setRouteSearch } = nav;
  const { refetch: refetchList } = listQuery;
  useEffect(() => {
    if (!selectedId) {
      // The dead link was dropped (or the GM moved on): following the same one
      // again is a new attempt, and is looked up and reported again.
      reported.current = null;
      refetchedFor.current = null;
      setLookedUp(null);
      return;
    }
    if (!listQuery.isSuccess || listQuery.isFetching) return;
    if (allNodes.some((n) => n.id === selectedId)) {
      resolvedOnce.current.add(selectedId);
      return;
    }
    const wasHere = resolvedOnce.current.has(selectedId) || deleted.current.has(selectedId);
    if (!wasHere) {
      if (refetchedFor.current !== selectedId) {
        refetchedFor.current = selectedId;
        const id = selectedId;
        void refetchList().then(() => setLookedUp(id));
        return;
      }
      if (lookedUp !== selectedId) return;
    }
    if (!wasHere && !rosterSettled) return;
    if (reported.current === selectedId) return;
    reported.current = selectedId;
    if (!wasHere) {
      toast.error(
        campaignName
          ? t("knowledge.entryNotInCampaign", { campaign: campaignName })
          : t("knowledge.entryNotFound"),
      );
    }
    setRouteSearch({ node: undefined }, { replace: true });
  }, [
    selectedId,
    listQuery.isSuccess,
    listQuery.isFetching,
    allNodes,
    campaignName,
    rosterSettled,
    lookedUp,
    refetchList,
    setRouteSearch,
    t,
  ]);

  const removeNode = (n: Node) =>
    deleteNode.mutate(
      { id: n.id },
      {
        onSuccess: () => {
          deleted.current.add(n.id);
          toast.success(t("knowledge.entryDeleted", { name: n.name }));
          if (selectedRef.current === n.id) {
            // The editor it was open in is gone with it; nothing is left to guard.
            edits.markDirty(false);
            setEditFor(null);
            // Its card is gone too: focus goes to the list.
            focusNext.current = closeFocus(null);
            nav.setSearch({ node: undefined }, { replace: true });
          }
          invalidate();
        },
        // The failure names the entry: with the confirm dialog closed there is
        // nothing else on screen saying which delete this is about.
        onError: (err) =>
          toast.error(t("knowledge.deleteNamedError", { name: n.name, message: errorMessage(err) })),
      },
    );

  const submitCreate = (fields: EditorFields) =>
    createNode.mutate(
      {
        nodeType: fields.nodeType,
        name: fields.name,
        body: fields.body,
        gmPrivate: fields.gmPrivate,
        aspects: fields.aspects,
      },
      {
        onSuccess: (res) => {
          invalidate();
          const node = res.node;
          if (!node) {
            edits.markDirty(false);
            setCreating(null);
            return;
          }
          // The new entry opens in the editor, where its tags, boards,
          // portrait and connections are now available. The list has not
          // refetched yet, so the row is written into it first.
          //
          // The create form stays up until the URL names the new entry (the
          // selection effect above then takes it down): clearing it here, a
          // beat before an asynchronous navigation lands, left a frame with
          // no pane at all, which replayed the sheet's opening animation.
          writeNodeToList(queryClient, node);
          edits.markDirty(false);
          setEditFor(node.id);
          nav.setSearch({ node: node.id });
          toast.success(t("knowledge.entryCreated", { name: node.name }));
        },
      },
    );

  const submitUpdate = (id: string, fields: EditorFields) =>
    updateNode.mutate(
      {
        id,
        name: fields.name,
        body: fields.body,
        gmPrivate: fields.gmPrivate,
        aspects: fields.aspects,
        knownAspectIds: fields.knownAspectIds,
      },
      {
        onSuccess: (res) => {
          if (res.node) {
            writeNodeToList(queryClient, res.node);
            setSaved({ id, seq: ++saveSeq.current, node: res.node });
          }
          toast.success(t("knowledge.entrySaved", { name: fields.name }));
          invalidate();
        },
      },
    );

  // ── The detail pane ───────────────────────────────────────────────────────
  const editing = creating === null && selected !== null && editFor === selected.id;
  let detail: ReactNode = null;
  let detailTitle = "";
  if (creating !== null) {
    detailTitle = t("knowledge.newEntryTitle");
    detail = (
      <EntryEditor
        key={`new:${creating}`}
        node={null}
        createType={creating}
        pending={createNode.isPending}
        error={createNode.isError ? t("common.couldntSave", { message: errorMessage(createNode.error) }) : null}
        onSubmit={submitCreate}
        onCancel={() => {
          // Cancel IS the explicit discard: no second question.
          edits.markDirty(false);
          createNode.reset();
          // Back to the entry the form was over, if there is one.
          if (selected) focusNext.current = { to: "edit" };
          setCreating(null);
        }}
        onDirtyChange={edits.markDirty}
      />
    );
  } else if (selected) {
    detailTitle = selected.name;
    detail = editing ? (
      <EntryEditor
        key={selected.id}
        node={selected}
        pending={updateNode.isPending}
        error={updateNode.isError ? t("common.couldntSave", { message: errorMessage(updateNode.error) }) : null}
        saved={saved?.id === selected.id ? saved : null}
        onSubmit={(fields) => submitUpdate(selected.id, fields)}
        onCancel={() => {
          edits.markDirty(false);
          updateNode.reset();
          focusNext.current = { to: "edit" };
          setEditFor(null);
        }}
        onDirtyChange={edits.markDirty}
        onDelete={() => setConfirmNode(selected)}
        onOpenLine={can.transcriptsRead ? onOpenLine : undefined}
        onOpenNode={openEntry}
      />
    ) : (
      <EntryView
        key={selected.id}
        node={selected}
        tags={tagIndex.byNode.get(selected.id) ?? []}
        editRef={editButton}
        onEdit={
          can.wikiEdit
            ? () => {
                resetMutations();
                setEditFor(selected.id);
              }
            : undefined
        }
        onClose={closeEntry}
        onOpenNode={openEntry}
        // The jump lands on a transcript, which is its own right.
        onOpenLine={can.transcriptsRead ? onOpenLine : undefined}
      />
    );
  }
  const dismissDetail = creating !== null ? leaveCreate : closeEntry;

  // Runs after every render, and does nothing unless a swap above named a
  // target. A close waits for the pane to be gone, so focus is not placed and
  // then pulled back out of a pane that is still closing.
  useEffect(() => {
    const want = focusNext.current;
    if (!want) return;
    if (want.to === "edit") {
      focusNext.current = null;
      editButton.current?.focus();
      return;
    }
    if (detail) return;
    focusNext.current = null;
    const card =
      want.to === "card"
        ? [...(contentRef.current?.querySelectorAll<HTMLElement>("[data-entry-id]") ?? [])].find(
            (el) => el.dataset.entryId === want.id,
          )
        : undefined;
    const target = card?.querySelector<HTMLElement>(".gx-wiki-card__open") ?? contentRef.current;
    target?.focus();
  });

  // ── The list ──────────────────────────────────────────────────────────────
  const cardFor = (n: Node, grouped: boolean) => (
    <EntryCard
      key={n.id}
      node={n}
      tags={tagIndex.byNode.get(n.id) ?? []}
      grouped={grouped}
      selected={creating === null && n.id === selectedId}
      onOpen={() => openEntry(n.id)}
      editable={can.wikiEdit}
      onEdit={() => openEntry(n.id, "edit")}
      onDelete={() => setConfirmNode(n)}
      deleting={deleteNode.isPending && deleteNode.variables?.id === n.id}
    />
  );
  // Group by type in enum order; empty groups are dropped so the list only
  // shows the types actually present.
  const groups = TYPE_ORDER.map((type) => ({
    type,
    items: visible.filter((n) => n.nodeType === type),
  })).filter((g) => g.items.length > 0);
  const toggleGroup = (type: NodeType) =>
    setCollapsed(collapsed.includes(type) ? collapsed.filter((x) => x !== type) : [...collapsed, type]);

  const noMatches = (
    <EmptyState
      size="inline"
      icon={<SearchX size={16} />}
      title={
        searching ? t("knowledge.noMatches", { query: debounced.trim() }) : t("knowledge.noFilterMatches")
      }
      action={
        <Button variant="secondary" size="sm" onClick={clearFilters}>
          {t("knowledge.clearFilters")}
        </Button>
      }
    />
  );

  const listContent =
    allNodes.length === 0 ? (
      <EmptyState
        icon={<BookOpen size={22} />}
        titleAs="h2"
        title={t("knowledge.emptyTitle")}
        description={can.wikiEdit ? t("knowledge.emptyList") : t("access.wikiEmptyGuest")}
        action={
          can.wikiEdit && (
            <>
              <Button variant="primary" onClick={() => startCreate(NodeType.NPC)}>
                {t("knowledge.starterNpc")}
              </Button>
              <Button variant="secondary" onClick={() => startCreate(NodeType.LOCATION)}>
                {t("knowledge.starterLocation")}
              </Button>
              {can.manage && (
                <Button
                  variant="secondary"
                  iconStart={<Sparkles size={14} />}
                  onClick={() =>
                    setDraftState({
                      open: true,
                      // A starting point the GM edits, not a prompt sent for them.
                      prompt: draftState.prompt || t("knowledge.starterWorldPrompt"),
                      draft: draftState.draft,
                    })
                  }
                >
                  {t("knowledge.starterWorld")}
                </Button>
              )}
            </>
          )
        }
      />
    ) : searchFailed ? null : visible.length === 0 ? (
      searchStale ? null : noMatches
    ) : searching ? (
      // Search results are ONE ranked list. Grouping by type would bury the
      // best match under whichever type happens to sort first.
      <ul className="gx-wiki-cards" aria-label={t("knowledge.searchResultsAria")}>
        {visible.map((n) => cardFor(n, false))}
      </ul>
    ) : (
      groups.map((g) => {
        const label = t(metaOf(g.type).labelKey);
        const open = !collapsed.includes(g.type);
        return (
          <section key={g.type} className="gx-wiki-typegroup" aria-label={label}>
            <h3 className="gx-wiki-typegroup__head">
              <button
                type="button"
                className="gx-wiki-typegroup__toggle"
                aria-expanded={open}
                onClick={() => toggleGroup(g.type)}
              >
                {open ? <ChevronDown size={14} aria-hidden /> : <ChevronRight size={14} aria-hidden />}
                {label} <span className="gx-wiki-typegroup__count">{g.items.length}</span>
              </button>
            </h3>
            {open && <ul className="gx-wiki-cards">{g.items.map((n) => cardFor(n, true))}</ul>}
          </section>
        );
      })
    );

  const graphNodes = useMemo(
    () => (graphQuery.data?.nodes ?? []).filter((n) => matchesTags(n.id)),
    [graphQuery.data, matchesTags],
  );

  const modeContent =
    mode === "boards" ? (
      <PrepBoards onOpenNode={(id) => openEntry(id)} />
    ) : mode === "list" ? (
      listContent
    ) : (
      <QueryBoundary
        query={graphQuery}
        skeleton={<Skeleton.Block height={360} />}
        errorTitle={t("knowledge.loadMapTitle")}
      >
        {(graph) =>
          mode === "health" ? (
            <WorldHealthPanel
              nodes={graph.nodes}
              edges={graph.edges}
              onOpenNode={(id) => openEntry(id)}
              onOpenCast={onOpenCast ?? openAgent}
              duplicateScan={duplicateScan}
            />
          ) : (
            <KnowledgeGraph
              nodes={graphNodes}
              edges={graph.edges}
              selectedID={creating === null ? selectedId : null}
              types={types}
              matchIDs={searchFailed ? null : matchIDs}
              onSelectNode={(id) => openEntry(id)}
              onGraphChanged={invalidate}
            />
          )
        }
      </QueryBoundary>
    );

  // The search, the type and tag chips and the count belong to the two views
  // they filter. The Checkup always reports on the whole world, and a board is
  // its own shortlist.
  const filterable = (mode === "list" || mode === "graph") && allNodes.length > 0;
  const foldTags = tagIndex.vocabulary.length > TAG_ROW_LIMIT;
  const shownTags =
    foldTags && !tagsExpanded
      ? tagIndex.vocabulary.filter((u) => liveTags.has(u.tag))
      : tagIndex.vocabulary;
  const typeChips = TYPE_ORDER.filter((type) => (typeCounts.get(type) ?? 0) > 0 || types.has(type));

  return (
    <div className="gx-wiki" data-mode={mode} data-detail={detail && !layoutStacked ? "open" : undefined}>
      <QueryBoundary
        query={listQuery}
        skeleton={<Skeleton.Row count={5} />}
        errorTitle={t("knowledge.loadEntriesTitle")}
      >
        <div className="gx-wiki__main">
          <div className="gx-wiki-toolbar">
            <div className="gx-wiki-toolbar__row">
              <SegmentedControl<ViewMode>
                label={t("knowledge.viewModeAria")}
                value={mode}
                onChange={setMode}
                options={[
                  { value: "list", label: t("knowledge.viewList"), icon: <Rows3 size={14} /> },
                  { value: "graph", label: t("knowledge.viewMap"), icon: <Network size={14} /> },
                  // The checkup reads the cast and runs the duplicate scan: the GM's.
                  ...(can.manage
                    ? [
                        {
                          value: "health" as const,
                          label: t("knowledge.viewCheckup"),
                          icon: <Stethoscope size={14} />,
                        },
                      ]
                    : []),
                  // Boards are edited where they are read, so they come with wiki.edit.
                  ...(can.wikiEdit
                    ? [
                        {
                          value: "boards" as const,
                          label: t("knowledge.viewBoards"),
                          icon: <ClipboardList size={14} />,
                        },
                      ]
                    : []),
                ]}
              />
              {can.wikiEdit && (
                <Menu
                  trigger={
                    <Button variant="primary" iconStart={<Plus size={14} />} iconEnd={<ChevronDown size={14} />}>
                      {t("knowledge.newEntry")}
                    </Button>
                  }
                >
                  <MenuLabel>{t("knowledge.newEntryMenuLabel")}</MenuLabel>
                  {TYPE_ORDER.map((type) => {
                    const meta = TYPE_META[type];
                    return (
                      <MenuItem key={type} icon={<meta.Icon />} onSelect={() => startCreate(type)}>
                        {t(meta.labelKey)}
                      </MenuItem>
                    );
                  })}
                  {can.manage && (
                    <>
                      <MenuSeparator />
                      <MenuItem
                        icon={<Sparkles />}
                        onSelect={() => setDraftState({ ...draftState, open: true })}
                      >
                        {t("knowledge.draftOpen")}
                      </MenuItem>
                    </>
                  )}
                </Menu>
              )}
            </div>

            {filterable && (
              <>
                <div className="gx-wiki-toolbar__row">
                  <Input
                    type="search"
                    aria-label={t("knowledge.searchAria")}
                    icon={<Search size={15} />}
                    placeholder={t("knowledge.searchPlaceholder")}
                    value={search}
                    onChange={(e) => setSearch(e.target.value)}
                    className="gx-wiki-toolbar__search"
                  />
                  {mode === "list" && !searching && (
                    <Select
                      aria-label={t("knowledge.sortAria")}
                      value={sort}
                      onValueChange={(v) => setSort(v as SortKey)}
                      options={[
                        { value: "name", label: t("knowledge.sortName") },
                        { value: "updated", label: t("knowledge.sortUpdated") },
                        { value: "created", label: t("knowledge.sortCreated") },
                      ]}
                    />
                  )}
                </div>

                <div className="gx-wiki-chips" role="group" aria-label={t("knowledge.filterByTypeAria")}>
                  {typeChips.map((type) => {
                    const meta = TYPE_META[type];
                    const on = types.has(type);
                    return (
                      <button
                        key={type}
                        type="button"
                        className="gx-wiki-chip"
                        aria-pressed={on}
                        style={on ? { color: meta.color, background: alphaBg(meta.color) } : undefined}
                        onClick={() =>
                          setTypes((prev) => {
                            const next = new Set(prev);
                            if (next.has(type)) next.delete(type);
                            else next.add(type);
                            return next;
                          })
                        }
                      >
                        {t(meta.labelKey)}{" "}
                        <span className="gx-wiki-chip__count">{typeCounts.get(type) ?? 0}</span>
                      </button>
                    );
                  })}
                </div>

                {tagIndex.vocabulary.length > 0 && (
                  <div className="gx-wiki-chips" role="group" aria-label={t("knowledge.filterByTagAria")}>
                    {foldTags && (
                      <button
                        type="button"
                        className="gx-wiki-chip gx-wiki-chip--fold"
                        aria-expanded={tagsExpanded}
                        onClick={() => setTagsExpanded((v) => !v)}
                      >
                        {tagsExpanded ? (
                          <ChevronDown size={12} aria-hidden />
                        ) : (
                          <ChevronRight size={12} aria-hidden />
                        )}
                        {t("knowledge.tagsFold", { n: tagIndex.vocabulary.length })}
                      </button>
                    )}
                    {shownTags.map((u) => (
                      <button
                        key={u.tag}
                        type="button"
                        className="gx-wiki-chip"
                        data-tag
                        aria-pressed={liveTags.has(u.tag)}
                        onClick={() =>
                          setActiveTags(() => {
                            const next = new Set(liveTags);
                            if (next.has(u.tag)) next.delete(u.tag);
                            else next.add(u.tag);
                            return next;
                          })
                        }
                      >
                        {u.tag}
                      </button>
                    ))}
                    {can.wikiEdit && (
                      <Button
                        variant="ghost"
                        size="sm"
                        iconStart={<Tags size={13} />}
                        onClick={() => setTagManagerOpen(true)}
                      >
                        {t("knowledge.manageTags")}
                      </Button>
                    )}
                  </div>
                )}

                <div className="gx-wiki-toolbar__count">
                  {/* Announced, so a screen-reader user hears a filter take effect. */}
                  <span role="status">
                    {countNarrowed
                      ? t("knowledge.countFiltered", { n: countShown, m: allNodes.length })
                      : allNodes.length === 1
                        ? t("knowledge.countOne")
                        : t("knowledge.countAll", { n: allNodes.length })}
                  </span>
                  {filtersActive && (
                    <Button variant="ghost" size="sm" onClick={clearFilters}>
                      {t("knowledge.clearFilters")}
                    </Button>
                  )}
                </div>
              </>
            )}
          </div>

          {can.manage && draftState.open && (
            <KnowledgeDraftCard state={draftState} onStateChange={setDraftState} onApplied={invalidate} />
          )}

          {filterable && searchFailed && (
            <ErrorState
              size="inline"
              title={t("knowledge.searchErrorTitle")}
              description={null}
              message={errorMessage(searchQuery.error)}
              onRetry={() => void searchQuery.refetch()}
              retrying={searchQuery.isFetching}
            />
          )}

          {/* tabIndex -1: the place focus lands when a closed entry's card is
              not on screen (a folded group, the map, a deleted entry). */}
          <div className="gx-wiki__content" ref={contentRef} tabIndex={-1}>
            {modeContent}
          </div>
        </div>

        {detail &&
          (layoutStacked ? (
            // No room beside the list: the same pane, as a sheet. The pane
            // brings its own header (title, actions, close), so the dialog's
            // title is kept for assistive tech only and its own X is left off.
            <Dialog
              open
              onOpenChange={(open) => {
                if (!open) dismissDetail();
              }}
              variant="sheet"
              size="lg"
              title={detailTitle}
              showClose={false}
              // A stray press on the backdrop must not start a "discard?" question.
              closeOnOutsideClick={!editing && creating === null}
              className="gx-wiki-sheet"
            >
              {detail}
            </Dialog>
          ) : (
            <aside className="gx-wiki__detail" aria-label={t("knowledge.detailAria")}>
              {detail}
            </aside>
          ))}
      </QueryBoundary>

      {edits.dialog}

      <TagManager open={tagManagerOpen} onOpenChange={setTagManagerOpen} tags={tagIndex.vocabulary} />

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

// NodeDeleteConfirm is the delete gate for one entry. Mounted only while a delete
// is pending confirmation, it fetches the node's edges so the dialog can name how
// many connections the cascade (ADR-0008 ON DELETE CASCADE) will take with it.
// The count must never be IMPLIED: while the fetch is in flight a raw 0 would
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
  const { t } = useI18n();
  // retry:false so a failed count settles into isError at once: this query
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
    cascade = <> {t("knowledge.deleteCounting")}</>;
  } else if (edgesQuery.isError) {
    cascade = <> {t("knowledge.deleteCountFailed")}</>;
  } else if (edgeCount > 0) {
    cascade = (
      <>
        {" "}
        {t("knowledge.deleteCascadeBefore")}
        <strong>
          {edgeCount === 1
            ? t("knowledge.connectionCountOne")
            : t("knowledge.connectionCountMany", { n: edgeCount })}
        </strong>
        {t("knowledge.deleteCascadeAfter")}
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
      title={t("knowledge.deleteEntryTitle", { name: node.name })}
      description={
        <>
          {t("knowledge.deleteEntryBody")}
          {cascade}
        </>
      }
      confirmLabel={t("knowledge.deleteEntry")}
      // Don't let the operator confirm against an unknown count; a fetch failure
      // is not a reason to block the delete itself.
      confirmDisabled={edgesQuery.isPending}
      onConfirm={onConfirm}
    />
  );
}

// cardSnippet is the card's one line of "what is this": the body, flattened
// (it may be markdown, and a one-line ellipsized preview cannot render it), or,
// for an entry written as facts only, its first two PUBLIC facts. A GM-only
// fact never reaches the card: the list is what is on screen when the GM
// shares it.
function cardSnippet(node: Node): string {
  const prose = stripMarkdown(node.body).trim();
  if (prose !== "") return prose;
  return node.aspects
    .filter((a) => !a.gmPrivate && a.value.trim() !== "")
    .slice(0, 2)
    .map((a) => (a.key.trim() !== "" ? `${a.key}: ${a.value}` : a.value))
    .join(" · ");
}

// EntryCard is one entry in the list. The NAME is the control: a button whose
// hit area is stretched over the whole card, so the card is one click target
// with one accessible name (the entry's), and the two icon actions sit above
// it. Everything else on the card is at-a-glance context.
function EntryCard({
  node,
  tags,
  grouped,
  selected,
  editable,
  onOpen,
  onEdit,
  onDelete,
  deleting,
}: {
  node: Node;
  tags: readonly string[];
  /** Under a type heading: the per-card type badge would only repeat it. */
  grouped: boolean;
  selected: boolean;
  /** The viewer may edit the wiki: the card carries Edit and Delete. */
  editable: boolean;
  onOpen: () => void;
  onEdit: () => void;
  onDelete: () => void;
  deleting: boolean;
}) {
  const { t, lang } = useI18n();
  const meta = metaOf(node.nodeType);
  const snippet = cardSnippet(node);
  const privateFacts = node.aspects.filter((a) => a.gmPrivate).length;
  const changed = updatedAt(node);

  // The open entry's card is brought into view: after a jump from the map, a
  // board or another screen the list would otherwise show some other stretch.
  const card = useRef<HTMLLIElement>(null);
  useEffect(() => {
    if (selected) card.current?.scrollIntoView({ block: "nearest" });
  }, [selected]);

  return (
    <li ref={card} className="gx-card gx-wiki-card" data-entry-id={node.id} data-selected={selected || undefined}>
      {/* The portrait replaces the type-icon tile when one exists (#590). alt is
          empty: the name is right beside it. lazy/async: each portrait is an
          authenticated blob read, and off-screen rows must not stampede. */}
      {node.hasPortrait ? (
        <img className="gx-wiki-card__portrait" src={portraitURL(node)} alt="" loading="lazy" decoding="async" />
      ) : (
        <span
          className="gx-wiki-card__icon"
          style={{ color: meta.color, background: alphaBg(meta.color) }}
          aria-hidden
        >
          <meta.Icon size={16} />
        </span>
      )}
      <div className="gx-wiki-card__meta">
        <div className="gx-wiki-card__head">
          <button
            type="button"
            className="gx-wiki-card__open"
            aria-current={selected ? "true" : undefined}
            onClick={onOpen}
          >
            {node.name}
          </button>
          {!grouped && (
            <Badge size="sm" style={{ color: meta.color, background: alphaBg(meta.color) }}>
              {t(meta.labelKey)}
            </Badge>
          )}
          {node.gmPrivate && (
            <Badge variant="neutral" size="sm">
              <EyeOff size={11} aria-hidden /> {t("knowledge.gmOnlyBadge")}
            </Badge>
          )}
          {node.agentId !== "" && (
            <Badge variant="gold" size="sm">
              <LinkIcon size={11} aria-hidden /> {t("knowledge.voicedBadge")}
            </Badge>
          )}
          {privateFacts > 0 && (
            <span className="gx-wiki-card__lock">
              <Lock size={11} aria-hidden />
              <span aria-hidden>{privateFacts}</span>
              <span className="gx-visually-hidden">
                {privateFacts === 1
                  ? t("knowledge.privateFactsOne")
                  : t("knowledge.privateFactsMany", { n: privateFacts })}
              </span>
            </span>
          )}
        </div>
        {snippet !== "" && <span className="gx-wiki-card__snippet">{snippet}</span>}
        {(tags.length > 0 || changed) && (
          <div className="gx-wiki-card__foot">
            {tags.slice(0, CARD_TAG_LIMIT).map((tag) => (
              <span key={tag} className="gx-wiki-tag">
                {tag}
              </span>
            ))}
            {tags.length > CARD_TAG_LIMIT && (
              <span className="gx-wiki-card__more">
                {t("knowledge.moreTags", { n: tags.length - CARD_TAG_LIMIT })}
              </span>
            )}
            {changed && (
              <span className="gx-wiki-card__when">
                {t("knowledge.updatedAgo", { when: relativeTime(changed, lang) })}
              </span>
            )}
          </div>
        )}
      </div>
      {editable && (
        <div className="gx-wiki-card__actions">
          <IconButton size="sm" label={t("knowledge.editEntryAria", { name: node.name })} onClick={onEdit}>
            <Pencil size={14} />
          </IconButton>
          <IconButton
            size="sm"
            tone="danger"
            label={t("knowledge.deleteNamedEntryAria", { name: node.name })}
            onClick={onDelete}
            disabled={deleting}
          >
            <Trash2 size={14} />
          </IconButton>
        </div>
      )}
    </li>
  );
}
