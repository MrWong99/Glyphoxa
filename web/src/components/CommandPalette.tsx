import { useEffect, useRef, useState } from "react";
import { Command } from "cmdk";
import { useQuery } from "@connectrpc/connect-query";
import { keepPreviousData } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { timestampMs } from "@bufbuild/protobuf/wkt";
import { Lock, Search, ScrollText, Sparkles } from "lucide-react";
import type { Timestamp } from "@bufbuild/protobuf/wkt";

import { CampaignService, SessionService } from "@gen/glyphoxa/management/v1/management_pb";
import { useI18n, type Lang, type TFunc } from "@/i18n";
import { metaOf, alphaBg } from "@/screens/campaign/knowledgeVocab";
import { stripMarkdown } from "@/components/ui/Markdown";

import "./commandPalette.css";
import { errorMessage } from "@/lib/connectError";

// CommandPalette — the Ctrl+K campaign search (#591). One overlay searching the
// three campaign sources with a shared debounced query: Knowledge Graph entries
// (SearchNodes — keyword, name-ranked), transcripts (SearchTranscripts —
// semantic when the server has embeddings, honestly labelled keyword mode when
// not) and promoted Highlights (SearchHighlights). Every result deep-links via
// the router's ScreenSearch params; the target screens consume-and-strip them.
//
// cmdk provides the list/keyboard machinery (the Combobox precedent, ADR-0017);
// shouldFilter={false} because the SERVER ranks — client-side re-filtering
// would fight ts_rank/ANN order. The overlay is hand-rolled on the gx-confirm
// z-ladder (90/91) rather than Radix Dialog — no new dependency.

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

export function CommandPalette({
  tenantSlug,
  open: openProp,
  onOpenChange,
}: {
  tenantSlug: string;
  // Optionally controlled (the AppShell pairs it with a visible topbar search
  // button — Ctrl+K alone is undiscoverable and unreachable on touch); when
  // the pair is absent the palette owns its state, as the unit tests do.
  open?: boolean;
  onOpenChange?: (open: boolean) => void;
}) {
  const { t, lang } = useI18n();
  const navigate = useNavigate();
  const [openState, setOpenState] = useState(false);
  const open = openProp ?? openState;
  const setOpen = onOpenChange ?? setOpenState;
  // The keybinding effect attaches once; it reads the live value via a ref so
  // a controlled parent's state is toggled, not a stale closure's.
  const openRef = useRef(open);
  openRef.current = open;
  const [q, setQ] = useState("");
  const [debounced, setDebounced] = useState("");

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

  // The 200ms debounce is the KnowledgePanel search convention; queries fire
  // only while the palette is open with a non-empty query (the server rejects
  // empty with InvalidArgument). retry:false — a typeahead re-fires on the next
  // keystroke anyway; keepPreviousData — no empty flash between keystrokes.
  useEffect(() => {
    const id = setTimeout(() => setDebounced(q), 200);
    return () => clearTimeout(id);
  }, [q]);
  const searching = open && debounced.trim() !== "";
  const queryOpts = { enabled: searching, placeholderData: keepPreviousData, retry: false } as const;

  const nodesQ = useQuery(CampaignService.method.searchNodes, { query: debounced }, queryOpts);
  const transcriptsQ = useQuery(SessionService.method.searchTranscripts, { query: debounced }, queryOpts);
  const highlightsQ = useQuery(SessionService.method.searchHighlights, { query: debounced }, queryOpts);

  const close = () => {
    setOpen(false);
    setQ("");
    setDebounced("");
  };

  // Modal focus contract, hand-rolled like the row-actions menu (#338, no new
  // dependency): remember where keyboard focus was when the palette opened and
  // put it back on close — cmdk's autofocused input would otherwise drop focus
  // to <body> when it unmounts.
  const dialogRef = useRef<HTMLDivElement>(null);
  const restoreRef = useRef<HTMLElement | null>(null);
  useEffect(() => {
    if (open) {
      restoreRef.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
      return;
    }
    restoreRef.current?.focus();
    restoreRef.current = null;
  }, [open]);

  // Tab must cycle INSIDE the dialog — without a trap it walks the inert page
  // behind the overlay.
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

  // go closes first, then navigates with the deep-link params; the target
  // screen consumes them and strips the URL (router.tsx ScreenSearch).
  const go = (screen: string, search: Record<string, string>) => {
    close();
    void navigate({ to: "/t/$tenantSlug/$screen", params: { tenantSlug, screen }, search });
  };

  // Entry hits land on the World-wiki sub-view directly — the Campaign view is
  // a path segment now (ADR-0063) — with ?node= as the consume-then-strip focus.
  const goNode = (nodeId: string) => {
    close();
    void navigate({
      to: "/t/$tenantSlug/$screen/$view",
      params: { tenantSlug, screen: "campaign", view: "knowledge" },
      search: { node: nodeId },
    });
  };

  if (!open) return null;

  const nodes = nodesQ.data?.nodes ?? [];
  const transcriptHits = transcriptsQ.data?.hits ?? [];
  const semantic = transcriptsQ.data?.semantic ?? true; // no data yet ≠ degraded
  const highlightHits = highlightsQ.data?.hits ?? [];

  const settled = !nodesQ.isFetching && !transcriptsQ.isFetching && !highlightsQ.isFetching;
  const anyResult = nodes.length > 0 || transcriptHits.length > 0 || highlightHits.length > 0;
  const anyError = nodesQ.error != null || transcriptsQ.error != null || highlightsQ.error != null;

  return (
    <div
      className="gx-palette__overlay"
      data-testid="command-palette"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) close();
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
            if (e.key === "Escape") close();
          }}
        >
          <div className="gx-palette__search">
            <Search size={16} className="gx-palette__search-icon" />
            <Command.Input
              className="gx-palette__input"
              placeholder={t("palette.placeholder")}
              value={q}
              onValueChange={setQ}
              autoFocus
            />
          </div>
          <Command.List className="gx-palette__list">
            {!searching && <div className="gx-palette__hint">{t("palette.hint")}</div>}
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
                          onSelect={() => goNode(n.id)}
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
                            const search: Record<string, string> = { session: h.voiceSessionId };
                            if (h.lineId !== "") search.line = h.lineId;
                            go("session", search);
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
                        onSelect={() => go("session", { session: h.voiceSessionId, highlight: h.id })}
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

                {settled && !anyResult && !anyError && (
                  <div className="gx-palette__empty">
                    {t("palette.noResults", { query: debounced })}
                  </div>
                )}
              </>
            )}
          </Command.List>
        </Command>
      </div>
    </div>
  );
}
