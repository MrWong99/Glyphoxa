import { useEffect, useRef, useState, type ReactNode } from "react";
import { Command } from "cmdk";
import { useQuery } from "@connectrpc/connect-query";
import { keepPreviousData } from "@tanstack/react-query";
import { timestampMs } from "@bufbuild/protobuf/wkt";
import { ArrowRightLeft, Lock, Plus, Radio, Search, ScrollText, Sparkles, UserRound } from "lucide-react";
import { toast } from "sonner";
import type { Timestamp } from "@bufbuild/protobuf/wkt";

import { CampaignService, SessionService } from "@gen/glyphoxa/management/v1/management_pb";
import { CAMPAIGN_DESTINATIONS, SCREEN_DESTINATIONS } from "@/app/destinations";
import { useScreenNav, type ScreenNav } from "@/app/screenNav";
import { CreateCampaignForm, useCreateCampaign } from "@/components/CreateCampaignForm";
import { useCampaignSwitch } from "@/components/useCampaignSwitch";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import { Dialog } from "@/components/ui/Dialog";
import { stripMarkdown } from "@/components/ui/Markdown";
import { useI18n, type Lang, type TFunc } from "@/i18n";
import { errorMessage, isNotFound } from "@/lib/connectError";
import { hasUnsavedEdits } from "@/lib/navigationGuard";
import { useLiveSession } from "@/lib/useLiveSession";
import { metaOf, alphaBg } from "@/screens/campaign/knowledgeVocab";

import "./commandPalette.css";

// CommandPalette — Ctrl/Cmd+K: go anywhere, and find anything in the campaign.
//
// Two layers in one list:
//
//   1. NAVIGATION, filtered here in the browser. "Go to" every screen and
//      campaign view (shown in full while the field is empty, so the palette
//      is a map of the app before a single key is pressed), "Switch campaign",
//      the cast by name, and two actions: New campaign and — while one is
//      running — Go to live session.
//   2. CAMPAIGN SEARCH (#591), ranked by the server over a shared debounced
//      query: Knowledge Graph entries (SearchNodes — keyword, name-ranked),
//      transcripts (SearchTranscripts — semantic when the server has
//      embeddings, honestly labelled keyword mode when not) and promoted
//      Highlights (SearchHighlights).
//
// cmdk provides the list/keyboard machinery (the Combobox precedent,
// ADR-0017) with shouldFilter={false}: the server ranks its hits — client-side
// re-filtering would fight ts_rank/ANN order — and the navigation layer is
// filtered by matches() below, so one rule decides what a query shows.
//
// Every move goes through the navigation seam (useScreenNav), so the palette
// knows no URL shapes; deep-link params (?node=, ?agent=, ?session=&line=)
// are consumed and stripped by the screen that owns them. Switching campaign
// goes through useCampaignSwitch — the one guarded way to change the Active
// Campaign — and so asks before unsaved edits are discarded.
//
// The overlay is hand-rolled on the modal layer tokens rather than a Radix
// Dialog: cmdk owns the input's focus and keyboard model.

// ── Shortcut, per platform ────────────────────────────────────────────────

/** True on Apple platforms, where the palette chord is ⌘K rather than Ctrl+K. */
export function isApplePlatform(): boolean {
  if (typeof navigator === "undefined") return false;
  const uaData = (navigator as Navigator & { userAgentData?: { platform?: string } }).userAgentData;
  return /mac|iphone|ipad|ipod/i.test(uaData?.platform ?? navigator.platform ?? "");
}

/** The palette chord as text: "⌘K", "Ctrl+K" or, in German, "Strg+K". */
export function paletteShortcut(t: TFunc): string {
  return isApplePlatform() ? "⌘K" : `${t("palette.keyCtrl")}+K`;
}

/** The palette chord as a key-cap chip, for the topbar's search button. */
export function PaletteShortcut({ className = "" }: { className?: string }) {
  const { t } = useI18n();
  return (
    <kbd className={className ? `gx-palette-kbd ${className}` : "gx-palette-kbd"}>{paletteShortcut(t)}</kbd>
  );
}

// ── Helpers ───────────────────────────────────────────────────────────────

// stamp renders a hit's timestamp in the display language ("Aug 3, 19:30" /
// "3. Aug., 19:30") — the session-date context the issue asks for.
function stamp(ts: Timestamp | undefined, lang: Lang): string | null {
  if (!ts) return null;
  const ms = Number(timestampMs(ts));
  if (!Number.isFinite(ms) || ms <= 0) return null;
  try {
    return new Date(ms).toLocaleString(lang, {
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
    });
  } catch {
    return new Date(ms).toISOString().slice(0, 16);
  }
}

// groupError renders one source's failure inline (role=alert) — a failed source
// must not silently vanish while the others answer (#591's honest-states AC).
function groupError(t: TFunc, group: string, err: Error | null) {
  if (!err) return null;
  return (
    <div className="gx-palette__error" role="alert">
      {t("palette.searchFailed", { group, message: errorMessage(err) })}
    </div>
  );
}

// fold lowercases and strips diacritics, so "ubersicht" finds "Übersicht" and
// "zoe" finds "Zoë".
function fold(text: string): string {
  return text
    .normalize("NFD")
    .replace(/[̀-ͯ]/g, "")
    .toLowerCase();
}

// matches: every word of the query appears somewhere in the haystack. An empty
// query matches everything (that is how the full "Go to" list shows).
function matches(query: string, ...haystack: (string | undefined)[]): boolean {
  const words = fold(query).split(/\s+/).filter(Boolean);
  if (words.length === 0) return true;
  const hay = fold(haystack.filter(Boolean).join(" "));
  return words.every((word) => hay.includes(word));
}

// The roster is ONE cache entry shared with the Cast view, and React Query
// runs a fetch with the options of the observer that starts it. So this
// observer must not be stricter than the view's own: a bare retry:false here
// could put the shared entry in error over one blip and cost the Cast view its
// list. Only the settled "no campaign yet" answer is not retried.
const rosterRetry = (count: number, err: unknown) => !isNotFound(err) && count < 3;

// ── The palette ───────────────────────────────────────────────────────────

export function CommandPalette({
  open: openProp,
  onOpenChange,
}: {
  // Optionally controlled (the AppShell pairs it with a visible topbar search
  // button — a keyboard chord alone is undiscoverable and unreachable on
  // touch); when the pair is absent the palette owns its state, as the unit
  // tests do.
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
}) {
  const { t } = useI18n();
  const [openState, setOpenState] = useState(false);
  const open = openProp ?? openState;
  const setOpen = onOpenChange ?? setOpenState;
  // The keybinding effect attaches once; it reads the live value via a ref so
  // a controlled parent's state is toggled, not a stale closure's.
  const openRef = useRef(open);
  openRef.current = open;

  // Ctrl/Cmd+K toggles anywhere in the app (the palette mounts in AppShell, so
  // "anywhere" is every authenticated screen). preventDefault beats the
  // browser's own address-bar focus shortcut. The chord is EXACT — no
  // shift/alt — so Ctrl+Shift+K (Firefox's web console) and friends stay the
  // browser's (#591 review).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && !e.shiftKey && !e.altKey && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setOpen(!openRef.current);
      }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
    // setOpen is stable in uncontrolled mode and openRef keeps the read live in
    // controlled mode; re-attaching per render would churn the listener.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // What a picked item opens AFTER the palette has closed: the "discard
  // unsaved changes?" question of a campaign switch, or the New campaign
  // dialog. Both live here, outside the list, because they must outlive it.
  const campaignSwitch = useCampaignSwitch();
  const [creating, setCreating] = useState(false);
  const [askingCreate, setAskingCreate] = useState(false);

  // Modal focus contract, hand-rolled like the row-actions menu (#338, no new
  // dependency): remember where keyboard focus was when the palette opened and
  // put it back on close — cmdk's autofocused input would otherwise drop focus
  // to <body> when it unmounts.
  //
  // A follow-up dialog is opened only AFTER that hand-back (afterClose), so it
  // sees the palette's opener as "what had focus" and returns focus there when
  // it closes in turn, instead of leaving a keyboard user on <body>.
  //
  // The opener is read while RENDERING the closed→open change, not in an
  // effect: the input's autoFocus runs as React commits the list, so by the
  // time an effect looks, "what has focus" is already the palette itself.
  const restoreRef = useRef<HTMLElement | null>(null);
  const wasOpen = useRef(false);
  if (open !== wasOpen.current) {
    wasOpen.current = open;
    if (open) {
      const active = document.activeElement;
      restoreRef.current = active instanceof HTMLElement && active !== document.body ? active : null;
    }
  }
  const afterClose = useRef<(() => void) | null>(null);
  useEffect(() => {
    if (open) return;
    if (restoreRef.current?.isConnected) restoreRef.current.focus();
    restoreRef.current = null;
    const next = afterClose.current;
    afterClose.current = null;
    next?.();
  }, [open]);

  const closeThen = (next?: () => void) => {
    afterClose.current = next ?? null;
    setOpen(false);
  };

  // Creating a campaign also makes it the Active Campaign, which remounts the
  // Campaign screen on the new id — so, like a switch, it asks before unsaved
  // edits would be lost.
  const startCreate = () => {
    if (hasUnsavedEdits()) setAskingCreate(true);
    else setCreating(true);
  };

  return (
    <>
      {open && (
        <PaletteDialog
          onClose={() => closeThen()}
          onSwitchCampaign={(campaign) => closeThen(() => campaignSwitch.switchTo(campaign))}
          onNewCampaign={() => closeThen(startCreate)}
        />
      )}
      {campaignSwitch.dialog}
      <ConfirmDialog
        open={askingCreate}
        onOpenChange={setAskingCreate}
        title={t("common.discardChangesTitle")}
        description={t("common.discardChangesDesc")}
        confirmLabel={t("common.discardChangesConfirm")}
        onConfirm={() => {
          setAskingCreate(false);
          setCreating(true);
        }}
      />
      {creating && <NewCampaignDialog onClose={() => setCreating(false)} />}
    </>
  );
}

// PaletteDialog is the open palette. It is mounted only while open, so a
// closed palette runs no query and holds no state — and every open starts on
// an empty field.
function PaletteDialog({
  onClose,
  onSwitchCampaign,
  onNewCampaign,
}: {
  onClose: () => void;
  onSwitchCampaign: (campaign: { id: string; name: string }) => void;
  onNewCampaign: () => void;
}) {
  const { t, lang } = useI18n();
  const nav = useScreenNav();
  const [q, setQ] = useState("");
  const [debounced, setDebounced] = useState("");

  // The 200ms debounce is the KnowledgePanel search convention; the search
  // queries fire only with a non-empty query (the server rejects empty with
  // InvalidArgument). retry:false — a typeahead re-fires on the next keystroke
  // anyway; keepPreviousData — no empty flash between keystrokes.
  useEffect(() => {
    const id = setTimeout(() => setDebounced(q), 200);
    return () => clearTimeout(id);
  }, [q]);
  const searching = debounced.trim() !== "";
  const queryOpts = { enabled: searching, placeholderData: keepPreviousData, retry: false } as const;

  const nodesQ = useQuery(CampaignService.method.searchNodes, { query: debounced }, queryOpts);
  const transcriptsQ = useQuery(SessionService.method.searchTranscripts, { query: debounced }, queryOpts);
  const highlightsQ = useQuery(SessionService.method.searchHighlights, { query: debounced }, queryOpts);

  // The navigation layer's data: three reads the app already holds in its
  // cache on most screens (same requests, same cache entries), so opening the
  // palette is usually free. None of them is required — a read that fails
  // simply leaves its group out.
  const activeQ = useQuery(CampaignService.method.getActiveCampaign, {}, { retry: false });
  const activeId = activeQ.data?.campaign?.id;
  const campaignsQ = useQuery(CampaignService.method.listCampaigns, { includeArchived: false });
  const rosterQ = useQuery(CampaignService.method.getCampaignRoster, {}, { retry: rosterRetry });
  const { live } = useLiveSession();

  // Tab must cycle INSIDE the dialog — without a trap it walks the inert page
  // behind the overlay.
  const dialogRef = useRef<HTMLDivElement>(null);
  const trapTab = (e: React.KeyboardEvent) => {
    if (e.key !== "Tab") return;
    const focusables = dialogRef.current?.querySelectorAll<HTMLElement>(
      'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])',
    );
    if (!focusables || focusables.length === 0) {
      e.preventDefault();
      return;
    }
    const first = focusables[0];
    const last = focusables[focusables.length - 1];
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  };

  // go closes first, then moves. The target screen consumes any deep-link
  // param and strips it from the URL.
  const go = (move: (nav: ScreenNav) => void) => {
    onClose();
    move(nav);
  };

  // ── Navigation layer, filtered by what is typed (not debounced: it is
  //    local, so it follows every keystroke) ────────────────────────────────
  const goToLabel = t("palette.groupGoTo");
  const views = CAMPAIGN_DESTINATIONS.filter((d) => matches(q, goToLabel, t(d.label)));
  const screens = SCREEN_DESTINATIONS.filter((d) => matches(q, goToLabel, t(d.label)));

  // The cast is offered once something is typed: with an empty field the list
  // is the map of the app, and a campaign's whole roster would bury it.
  const typed = q.trim() !== "";
  const cast = typed
    ? (rosterQ.data?.roster ?? []).filter((a) => matches(q, a.name, a.title, ...a.aliases))
    : [];

  const otherCampaigns = (campaignsQ.data?.campaigns ?? []).filter(
    (c) =>
      c.id !== activeId &&
      c.archivedAt === undefined &&
      matches(q, t("palette.switchCampaign", { name: c.name })),
  );

  const showNewCampaign = matches(q, t("components.newCampaign"));
  const showLiveSession = live && matches(q, t("palette.goToLiveSession"));

  const nodes = nodesQ.data?.nodes ?? [];
  const transcriptHits = transcriptsQ.data?.hits ?? [];
  const semantic = transcriptsQ.data?.semantic ?? true; // no data yet ≠ degraded
  const highlightHits = highlightsQ.data?.hits ?? [];

  const settled = !nodesQ.isFetching && !transcriptsQ.isFetching && !highlightsQ.isFetching;
  const anyLocal =
    views.length > 0 ||
    screens.length > 0 ||
    cast.length > 0 ||
    otherCampaigns.length > 0 ||
    showNewCampaign ||
    showLiveSession;
  const anyResult = nodes.length > 0 || transcriptHits.length > 0 || highlightHits.length > 0;
  const anyError = nodesQ.error != null || transcriptsQ.error != null || highlightsQ.error != null;
  // "Nothing" is only said once the typed query is the one the server
  // answered — otherwise it would flash between a keystroke and its debounce.
  const caughtUp = q.trim() === debounced.trim();

  return (
    <div
      className="gx-palette__overlay"
      data-testid="command-palette"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        className="gx-palette"
        role="dialog"
        aria-modal="true"
        aria-label={t("palette.title")}
        ref={dialogRef}
        onKeyDown={trapTab}
      >
        <Command
          shouldFilter={false}
          label={t("palette.title")}
          onKeyDown={(e) => {
            if (e.key !== "Escape") return;
            // Marked as handled: a layer underneath that also listens for
            // Escape (the phone drawer) must not close on the same key.
            e.preventDefault();
            onClose();
          }}
        >
          <div className="gx-palette__search">
            <Search size={16} className="gx-palette__search-icon" aria-hidden="true" />
            <Command.Input
              className="gx-palette__input"
              placeholder={t("palette.placeholder")}
              value={q}
              onValueChange={setQ}
              autoFocus
            />
          </div>
          <Command.List className="gx-palette__list">
            {(views.length > 0 || screens.length > 0) && (
              <Command.Group heading={goToLabel}>
                {views.map((d) => (
                  <Row
                    key={d.view}
                    value={`goto:view:${d.view}`}
                    icon={<d.icon size={14} />}
                    title={t(d.label)}
                    onSelect={() => go((n) => n.goCampaignView(d.view))}
                  />
                ))}
                {screens.map((d) => (
                  <Row
                    key={d.screen}
                    value={`goto:screen:${d.screen}`}
                    icon={<d.icon size={14} />}
                    title={t(d.label)}
                    onSelect={() => go((n) => n.goScreen(d.screen))}
                  />
                ))}
              </Command.Group>
            )}

            {cast.length > 0 && (
              <Command.Group heading={t("campaign.tabCast")}>
                {cast.map((a) => (
                  <Row
                    key={a.id}
                    value={`cast:${a.id}`}
                    icon={<UserRound size={14} />}
                    title={a.name}
                    meta={a.title || undefined}
                    // Lands on Cast with that NPC's editor open (?agent=).
                    onSelect={() => go((n) => n.goCampaignView("cast", { agent: a.id }))}
                  />
                ))}
              </Command.Group>
            )}

            {otherCampaigns.length > 0 && (
              <Command.Group heading={t("shell.navCampaigns")}>
                {otherCampaigns.map((c) => (
                  <Row
                    key={c.id}
                    value={`switch:${c.id}`}
                    icon={<ArrowRightLeft size={14} />}
                    title={t("palette.switchCampaign", { name: c.name })}
                    meta={c.system || undefined}
                    onSelect={() => onSwitchCampaign({ id: c.id, name: c.name })}
                  />
                ))}
              </Command.Group>
            )}

            {(showNewCampaign || showLiveSession) && (
              <Command.Group heading={t("palette.groupActions")}>
                {showLiveSession && (
                  <Row
                    value="action:live-session"
                    icon={<Radio size={14} />}
                    title={t("palette.goToLiveSession")}
                    onSelect={() => go((n) => n.goScreen("session"))}
                  />
                )}
                {showNewCampaign && (
                  <Row
                    value="action:new-campaign"
                    icon={<Plus size={14} />}
                    title={t("components.newCampaign")}
                    onSelect={onNewCampaign}
                  />
                )}
              </Command.Group>
            )}

            {!typed && <div className="gx-palette__hint">{t("palette.hint")}</div>}

            {searching && (
              <>
                {groupError(t, t("palette.groupEntries"), nodesQ.error)}
                {groupError(t, t("palette.groupTranscripts"), transcriptsQ.error)}
                {groupError(t, t("palette.groupHighlights"), highlightsQ.error)}

                {nodes.length > 0 && (
                  <Command.Group heading={t("palette.groupEntries")}>
                    {nodes.map((n) => {
                      const meta = metaOf(n.nodeType);
                      return (
                        <Command.Item
                          key={n.id}
                          value={`node:${n.id}`}
                          className="gx-palette__item"
                          // Entry hits land on the World wiki with ?node= as
                          // the consume-then-strip focus.
                          onSelect={() => go((nv) => nv.goCampaignView("knowledge", { node: n.id }))}
                        >
                          <span
                            className="gx-palette__type-icon"
                            style={{ color: meta.color, background: alphaBg(meta.color) }}
                          >
                            <meta.Icon size={13} />
                          </span>
                          <span className="gx-palette__item-title">{n.name}</span>
                          <span className="gx-palette__item-meta">
                            {t(meta.labelKey)}
                            {n.gmPrivate && (
                              <span className="gx-palette__private" title={t("palette.gmPrivateBadge")}>
                                <Lock size={11} aria-label={t("palette.gmPrivateBadge")} />
                              </span>
                            )}
                          </span>
                        </Command.Item>
                      );
                    })}
                  </Command.Group>
                )}

                {transcriptHits.length > 0 && (
                  <Command.Group heading={t("palette.groupTranscripts")}>
                    {!semantic && (
                      <div className="gx-palette__degraded" data-testid="palette-degraded">
                        {t("palette.transcriptsDegraded")}
                      </div>
                    )}
                    {transcriptHits.map((h, i) => {
                      const when = stamp(h.at, lang);
                      const lead = h.who !== "" ? h.who : t("palette.transcriptAt", { when: when ?? "—" });
                      return (
                        <Command.Item
                          key={`${h.voiceSessionId}:${h.lineId}:${i}`}
                          value={`transcript:${h.voiceSessionId}:${h.lineId}:${i}`}
                          className="gx-palette__item"
                          disabled={h.voiceSessionId === ""}
                          onSelect={() => {
                            if (h.voiceSessionId === "") return;
                            go((nv) =>
                              nv.goScreen("session", {
                                session: h.voiceSessionId,
                                line: h.lineId !== "" ? h.lineId : undefined,
                              }),
                            );
                          }}
                        >
                          <span className="gx-palette__type-icon gx-palette__type-icon--plain">
                            <ScrollText size={13} />
                          </span>
                          <span className="gx-palette__item-body">
                            <span className="gx-palette__item-title">{lead}</span>
                            {/* Transcript snippets include Agent speech, which
                                can leak markdown; a one-line palette row can't
                                render it, so flatten. */}
                            <span className="gx-palette__snippet">{stripMarkdown(h.snippet)}</span>
                          </span>
                          {h.who !== "" && when != null && (
                            <span className="gx-palette__item-meta">{when}</span>
                          )}
                        </Command.Item>
                      );
                    })}
                  </Command.Group>
                )}

                {highlightHits.length > 0 && (
                  <Command.Group heading={t("palette.groupHighlights")}>
                    {highlightHits.map((h) => (
                      <Command.Item
                        key={h.id}
                        value={`highlight:${h.id}`}
                        className="gx-palette__item"
                        onSelect={() =>
                          go((nv) => nv.goScreen("session", { session: h.voiceSessionId, highlight: h.id }))
                        }
                      >
                        <span className="gx-palette__type-icon gx-palette__type-icon--plain">
                          <Sparkles size={13} />
                        </span>
                        <span className="gx-palette__item-body">
                          {/* Same flattening as the transcript snippets above:
                              the excerpt can quote Agent speech and the reason
                              is classifier output — both markdown-prone, and a
                              one-line row can't render it. */}
                          <span className="gx-palette__item-title">{stripMarkdown(h.excerpt)}</span>
                          {h.reason !== "" && (
                            <span className="gx-palette__snippet">{stripMarkdown(h.reason)}</span>
                          )}
                        </span>
                        <span className="gx-palette__item-meta">{stamp(h.startsAt, lang)}</span>
                      </Command.Item>
                    ))}
                  </Command.Group>
                )}
              </>
            )}

            {searching && caughtUp && settled && !anyResult && !anyError && !anyLocal && (
              <div className="gx-palette__empty">{t("palette.noResults", { query: debounced })}</div>
            )}
          </Command.List>

          {/* The keys the list answers to. Hidden on a touch screen (the
              stylesheet), where there are none to press. */}
          <div className="gx-palette__footer">
            <span>
              <kbd className="gx-palette-kbd">↑</kbd>
              <kbd className="gx-palette-kbd">↓</kbd>
              {t("palette.keyHintMove")}
            </span>
            <span>
              <kbd className="gx-palette-kbd">↵</kbd>
              {t("palette.keyHintOpen")}
            </span>
            <span>
              <kbd className="gx-palette-kbd">Esc</kbd>
              {t("palette.keyHintClose")}
            </span>
          </div>
        </Command>
      </div>
    </div>
  );
}

// Row is one navigation-layer item: an icon, a title, an optional quiet note.
function Row({
  value,
  icon,
  title,
  meta,
  onSelect,
}: {
  value: string;
  icon: ReactNode;
  title: string;
  meta?: string;
  onSelect: () => void;
}) {
  return (
    <Command.Item value={value} className="gx-palette__item" onSelect={onSelect}>
      <span className="gx-palette__type-icon gx-palette__type-icon--plain" aria-hidden="true">
        {icon}
      </span>
      <span className="gx-palette__item-title">{title}</span>
      {meta && <span className="gx-palette__item-meta">{meta}</span>}
    </Command.Item>
  );
}

// NewCampaignDialog is the palette's "New campaign" action: the same
// create-then-activate flow the switcher and the first-run card use
// (useCreateCampaign), in a dialog. It is mounted per use, so every open
// starts on a pristine form with no earlier attempt's error.
function NewCampaignDialog({ onClose }: { onClose: () => void }) {
  const { t } = useI18n();
  const nav = useScreenNav();
  const { live } = useLiveSession();
  // Read at the moment the creation lands, not when the dialog rendered.
  const liveRef = useRef(live);
  liveRef.current = live;
  const created = useRef("");

  const create = useCreateCampaign(() => {
    onClose();
    // The new campaign was selected. While a Voice Session is live its
    // campaign still wins (live-first, #222), so the Overview would show the
    // OLD campaign: say that the switch is pending instead of going there.
    if (liveRef.current) {
      toast.info(t("shell.switchDeferred", { name: created.current }));
      return;
    }
    toast.success(t("shell.nowWorkingIn", { name: created.current }));
    nav.goCampaignView("overview");
  });

  return (
    <Dialog
      open
      onOpenChange={(next) => {
        if (!next && !create.pending) onClose();
      }}
      title={t("components.newCampaign")}
      description={live ? t("components.sessionLiveNotice") : t("palette.newCampaignDesc")}
      // A stray click on the backdrop must not throw away a typed name.
      closeOnOutsideClick={false}
    >
      <CreateCampaignForm
        onSubmit={(fields) => {
          created.current = fields.name;
          create.submit(fields);
        }}
        pending={create.pending}
        error={create.error}
        autoFocusName
        onCancel={onClose}
      />
    </Dialog>
  );
}
