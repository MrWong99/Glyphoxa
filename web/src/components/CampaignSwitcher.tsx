import { createElement, useEffect, useRef, useState } from "react";
import { useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Check, ChevronDown, LayoutGrid, Plus, Swords } from "lucide-react";

import { CampaignService, SessionService } from "@gen/glyphoxa/management/v1/management_pb";
import { useCurrentUser } from "@/app/currentUser";
import { useScreenNav } from "@/app/screenNav";
import { invalidateActiveCampaignScopedQueries, watchVoiceSessionEnd } from "@/lib/campaignCache";
import { useI18n } from "@/i18n";
import { errorMessage, isNotFound } from "@/lib/connectError";
import { useLiveSession } from "@/lib/useLiveSession";
import { usePopoverDismiss } from "@/components/ui/usePopoverDismiss";
import { Badge } from "@/components/ui/Badge";
import { SharedWithMe } from "./GuestWorkspaceSwitcher";
import { NewCampaignDialog } from "./NewCampaignDialog";
import {
  clearPendingCampaignSwitch,
  expectsLocalActiveCampaignChange,
  isLocalCampaignSwitch,
  useCampaignSwitch,
  usePendingCampaignSwitch,
} from "./useCampaignSwitch";

import "./campaignSwitcher.css";

// The Active-Campaign QUICK switcher (#267), a topbar control visible on every
// screen (placement decided in #266, option a): it names the resolved Active
// Campaign and opens a short panel to switch to another one. That is all it
// does now — everything else it used to carry in a 300px popover (campaign
// settings, import, archived campaigns, archive / delete per row) lives on the
// Campaigns page, one click away through "All campaigns". "New campaign" stays
// here, as a dialog.
//
// Switching goes through useCampaignSwitch: it asks before unsaved edits would
// be lost, writes the durable selection (SetActiveCampaign), runs the
// Active-Campaign cache sweep so every campaign-scoped screen follows without a
// reload, and toasts the result. resolveActiveCampaign stays live-first (#222):
// while a Voice Session is live its campaign still wins, so a switch made
// mid-session is stored and takes effect when the session ends — and the
// trigger says so for as long as that is true: a live marker, and "Next:
// {name}" for the campaign that is waiting. The watchVoiceSessionEnd watcher
// re-runs the sweep at the moment that promise comes due.
//
// Being on every screen, it is also where a change of the Active Campaign that
// this tab did NOT make is noticed (the web-only slice of #692) — see the
// "changed elsewhere" effect below.

type Target = { id: string; name: string };

// One id, so a second change replaces the notice instead of stacking another.
const CHANGED_ELSEWHERE_TOAST = "gx-active-campaign-changed";

// How long after a Voice Session ends a change of the Active Campaign is put
// down to that: the session's campaign stops winning and the stored selection
// resolves again. Nobody did that "elsewhere".
const SESSION_END_WINDOW_MS = 15_000;

// A component, so the label follows the display language while the notice is
// up (the pattern toastUndo uses for "Undo").
function SwitchBackLabel() {
  return useI18n().t("campaigns.switchBack");
}

// The notice itself, for the same reason: it can stay up for a long time.
function ChangedElsewhereMessage({ name }: { name: string }) {
  return useI18n().t("campaigns.changedElsewhere", { name });
}

export function CampaignSwitcher() {
  const { t } = useI18n();
  const nav = useScreenNav();
  const queryClient = useQueryClient();
  // The Campaigns other GMs share with this user (ADR-0065): another Workspace
  // each, listed under the user's own campaigns.
  const sharedWithMe = useCurrentUser()?.guestWorkspaces ?? [];
  const [open, setOpen] = useState(false);
  const [creating, setCreating] = useState(false);
  const wrapRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);

  // The resolved Active Campaign labels the trigger + marks the current row.
  // retry:false so a fresh, unseeded install's CodeNotFound settles at once into
  // the first-run create path rather than backing off first — the SAME option
  // every observer of this key uses (Configuration, Session), keeping retry
  // semantics deterministic on the shared cache entry. The label renders from
  // DATA, which React Query retains through a failed background refetch, so a
  // transient blip never blanks an already-shown campaign name.
  const activeQ = useQuery(CampaignService.method.getActiveCampaign, {}, { retry: false });

  // First run: the operator has zero campaigns — GetActiveCampaign's CodeNotFound
  // is exactly that signal (resolution otherwise falls back to the most-recently-
  // created campaign), and it's the SAME signal Setup's first-run card keys off,
  // so "no campaign exists yet" has one definition in the SPA. The trigger then
  // is a create call-to-action.
  //
  // It outranks retained data: NotFound is not a blip but the answer "nothing
  // resolves" (the last campaign was archived or deleted), and the name still
  // sitting on the cache entry is of a campaign that is gone.
  const firstRun = activeQ.isError && isNotFound(activeQ.error);
  const activeCampaign = firstRun ? undefined : activeQ.data?.campaign;
  const activeId = activeCampaign?.id;
  const activeName = activeCampaign?.name;

  // Live and "what comes next", on the trigger. useLiveSession observes the
  // cache entry the shell already polls on every screen, so this costs no
  // request of its own.
  const { live } = useLiveSession();
  const pending = usePendingCampaignSwitch();
  // Whether that answer could be had at all: the same cache entry, observed
  // without fetching (useLiveSession polls it). A failed check means "live"
  // above may be stale, and a switch may be deferred without the panel saying.
  const sessionCheckFailed = useQuery(SessionService.method.getSession, {}, { enabled: false }).isError;

  // The campaign list renders only inside the open panel, so it isn't fetched
  // from every screen on boot/focus — the query wakes when the panel opens (a
  // create's invalidation just marks it stale for the next open).
  const listQ = useQuery(
    CampaignService.method.listCampaigns,
    { includeArchived: false },
    { enabled: open },
  );
  const campaigns = listQ.data?.campaigns ?? [];

  // Re-run the campaign sweep when the live Voice Session ends — the moment a
  // mid-session switch actually takes effect. Mounted here because the switcher
  // is on every screen and owns the takes-effect promise.
  useEffect(() => watchVoiceSessionEnd(queryClient), [queryClient]);

  // Closing hands focus back to the trigger when it was inside the panel (a
  // row that was just picked, Escape from a row); a click elsewhere on the page
  // keeps the focus it took.
  const close = () => {
    if (wrapRef.current?.contains(document.activeElement)) triggerRef.current?.focus();
    setOpen(false);
  };

  // A row click goes through the guarded switch (useCampaignSwitch): it asks
  // before a switch would discard unsaved edits, runs the sweep, and toasts
  // what happened — the new campaign's name, or that a live Voice Session
  // defers it. The panel closes once the selection is written.
  const campaignSwitch = useCampaignSwitch({ onSwitched: close });
  const switching = campaignSwitch.pending;
  const { switchTo } = campaignSwitch;
  // The "changed elsewhere" notice below outlives the render that raised it;
  // its "Switch back" must call the hook as it is when pressed.
  const switchToRef = useRef(switchTo);
  useEffect(() => {
    switchToRef.current = switchTo;
  });

  usePopoverDismiss(wrapRef, open, close);

  // ── Session edges ────────────────────────────────────────────────────────
  const { refetch: refetchActive } = activeQ;
  const wasLive = useRef(live);
  const sessionEndedAt = useRef(0);
  useEffect(() => {
    if (wasLive.current && !live) {
      // The session is over: whatever was waiting has either become active or
      // been overtaken; either way "Next:" is no longer true.
      sessionEndedAt.current = Date.now();
      clearPendingCampaignSwitch();
    }
    if (!wasLive.current && live) {
      // A session went live — possibly started from Discord, possibly in
      // another campaign, which is then the Active Campaign (live-first).
      // Ask, instead of showing the old one until the next window focus.
      void refetchActive();
    }
    wasLive.current = live;
  }, [live, refetchActive]);

  // The waiting campaign became the active one: nothing is waiting any more.
  useEffect(() => {
    if (pending && pending.id === activeId) clearPendingCampaignSwitch();
  }, [pending, activeId]);

  // ── Changed elsewhere (#692, web-only slice) ─────────────────────────────
  // The Active Campaign is ambient server state: another tab, another device
  // or a Discord /glyphoxa use can change it under this tab, and every write
  // on screen then lands in a different campaign. The full fix (the campaign
  // id on the URL and on requests) is #692; until then this at least SAYS so:
  // when the resolved campaign changes and this tab did not ask for it, every
  // campaign-scoped read is refreshed so the screen shows what the server will
  // write to, and a notice that stays until dismissed names the new campaign
  // and offers the way back.
  const seen = useRef<Target | null>(null);
  const noticeUp = useRef(false);
  useEffect(() => {
    if (firstRun) {
      seen.current = null;
      return;
    }
    if (activeId === undefined || activeName === undefined) return;
    const previous = seen.current;
    seen.current = { id: activeId, name: activeName };
    if (!previous || previous.id === activeId) return;

    const ours =
      isLocalCampaignSwitch(activeId) ||
      expectsLocalActiveCampaignChange() ||
      Date.now() - sessionEndedAt.current < SESSION_END_WINDOW_MS;
    if (ours) {
      // A notice about an earlier change is overtaken by this tab's own move.
      if (noticeUp.current) {
        noticeUp.current = false;
        toast.dismiss(CHANGED_ELSEWHERE_TOAST);
      }
      return;
    }

    void invalidateActiveCampaignScopedQueries(queryClient);
    noticeUp.current = true;
    toast.info(createElement(ChangedElsewhereMessage, { name: activeName }), {
      id: CHANGED_ELSEWHERE_TOAST,
      duration: Infinity,
      onDismiss: () => {
        noticeUp.current = false;
      },
      action: {
        label: createElement(SwitchBackLabel),
        onClick: () => {
          noticeUp.current = false;
          // The guarded switch: it asks about unsaved edits, marks the change
          // as this tab's own and says where the GM is now.
          switchToRef.current(previous);
        },
      },
    });
  }, [firstRun, activeId, activeName, queryClient]);

  const onTriggerClick = () => {
    if (open) {
      close();
      return;
    }
    // First run has no list to show — go straight to the create dialog.
    if (firstRun) setCreating(true);
    else setOpen(true);
  };

  const openCreate = () => {
    close();
    setCreating(true);
  };

  const triggerLabel = activeCampaign
    ? activeCampaign.name
    : firstRun
      ? t("components.createFirstCampaign")
      : activeQ.isPending
        ? null
        : t("components.selectCampaign");

  // The accessible name carries what the trigger shows: the Active Campaign
  // the switcher exists to surface (#266), that a session is live in it, and
  // which campaign is waiting — while the loading/first-run states keep a
  // stable, meaningful name.
  const triggerName = firstRun
    ? t("components.createFirstCampaign")
    : !activeCampaign
      ? t("components.switchCampaign")
      : live && pending
        ? t("campaigns.switcherLiveNext", { name: activeCampaign.name, next: pending.name })
        : live
          ? t("campaigns.switcherLive", { name: activeCampaign.name })
          : t("components.switchCampaignActive", { name: activeCampaign.name });

  return (
    <div className="gx-campaign-switcher" ref={wrapRef}>
      <button
        ref={triggerRef}
        type="button"
        className="gx-campaign-switcher__trigger"
        // First run opens a dialog, not this panel.
        aria-expanded={firstRun ? undefined : open}
        aria-haspopup={firstRun ? "dialog" : undefined}
        aria-label={triggerName}
        data-testid="campaign-switcher-trigger"
        data-firstrun={firstRun || undefined}
        data-live={(live && !!activeCampaign) || undefined}
        onClick={onTriggerClick}
      >
        <span className="gx-campaign-switcher__sigil" aria-hidden>
          <Swords size={15} />
          {/* The live marker: a Voice Session is running in this campaign, so
              a switch made now waits for it to end. */}
          {live && activeCampaign && <span className="gx-campaign-switcher__live" />}
        </span>
        <span className="gx-campaign-switcher__label">
          {pending && activeCampaign ? (
            // title: like the name below, it ellipsizes in a fixed-width trigger.
            <span
              className="gx-campaign-switcher__next"
              title={t("campaigns.next", { name: pending.name })}
            >
              {t("campaigns.next", { name: pending.name })}
            </span>
          ) : (
            <span className="gx-overline gx-campaign-switcher__overline">
              {t("components.campaignOverline")}
            </span>
          )}
          {triggerLabel === null ? (
            <span className="gx-skeleton" data-testid="campaign-switcher-loading" />
          ) : (
            // title: the name ellipsizes in a fixed-width trigger, and a
            // truncated campaign name with no hover reveal is a guessing game.
            <span className="gx-campaign-switcher__name" title={triggerLabel}>
              {triggerLabel}
            </span>
          )}
        </span>
        <ChevronDown size={14} className="gx-campaign-switcher__chevron" aria-hidden />
      </button>

      {open && (
        <div className="gx-select__content gx-campaign-switcher__panel">
          {/* A labelled group of plain buttons (not a role=listbox): a short,
              non-filterable list with no roving-tabindex keyboard model, so the
              announced role matches the actual behaviour. The Active Campaign's
              row carries aria-current; clicking it just closes the panel — no
              RPC, no sweep, nothing to re-select — unless a switch is waiting
              for the live session to end: then picking the session's own
              campaign is how that switch is taken back. */}
          <ul className="gx-campaign-switcher__list" role="group" aria-label={t("components.campaignsGroupLabel")}>
            {campaigns.map((c) => {
              const isActive = c.id === activeId;
              const isNext = pending?.id === c.id;
              return (
                <li key={c.id} className="gx-campaign-switcher__row">
                  <button
                    type="button"
                    aria-current={isActive || undefined}
                    className="gx-select__item gx-campaign-switcher__item"
                    data-active={isActive || undefined}
                    // Not `disabled` while a switch is being written: the row
                    // that was just pressed would drop focus to <body>.
                    aria-disabled={switching || undefined}
                    onClick={() => {
                      if (isActive && !pending) close();
                      else if (!switching) switchTo({ id: c.id, name: c.name });
                    }}
                  >
                    <span className="gx-campaign-switcher__item-meta">
                      <span className="gx-campaign-switcher__item-name">{c.name}</span>
                      {c.system && <span className="gx-campaign-switcher__item-system">{c.system}</span>}
                    </span>
                    {isNext && (
                      <Badge variant="info" size="sm">
                        {t("campaigns.nextBadge")}
                      </Badge>
                    )}
                    {isActive && <Check size={14} aria-hidden />}
                  </button>
                </li>
              );
            })}
          </ul>

          {listQ.isPending && <div className="gx-skeleton" data-testid="campaign-list-loading" />}

          {/* Opening one leaves this table for the host's: a navigation to
              that Workspace, which the WorkspaceGate turns into the switch. */}
          <SharedWithMe
            workspaces={sharedWithMe}
            onPick={(w) => {
              close();
              nav.goWorkspace(w.campaignId);
            }}
          />

          {listQ.isError && (
            <p className="gx-campaign__error gx-campaign-switcher__error" role="alert">
              {t("components.couldntLoadCampaigns", { message: errorMessage(listQ.error) })}
            </p>
          )}

          <div className="gx-campaign-switcher__foot">
            <button type="button" className="gx-campaign-switcher__new" onClick={openCreate}>
              <Plus size={15} aria-hidden /> {t("components.newCampaign")}
            </button>
            {/* Everything else about campaigns — settings of any campaign,
                export, duplicate, archive, delete, import, the archived ones —
                is on the Campaigns page. */}
            <button
              type="button"
              className="gx-campaign-switcher__new"
              onClick={() => {
                close();
                nav.goScreen("campaigns");
              }}
            >
              <LayoutGrid size={15} aria-hidden /> {t("campaigns.allCampaigns")}
            </button>
          </div>

          {/* The deferral notice: a switch made now is stored, and takes effect
              when the session ends. */}
          {live ? (
            <p className="gx-campaign-switcher__notice" role="note">
              {t("components.sessionLiveNotice")}
            </p>
          ) : (
            sessionCheckFailed && (
              <p className="gx-campaign-switcher__notice" role="note">
                {t("components.sessionCheckFailedNotice")}
              </p>
            )
          )}
        </div>
      )}

      <NewCampaignDialog open={creating} onOpenChange={setCreating} />
      {/* Outside the panel on purpose: the "Discard unsaved changes?" question
          must outlive a panel that closes underneath it. */}
      {campaignSwitch.dialog}
    </div>
  );
}
