import { useEffect, useRef, useState } from "react";
import { useQuery, useMutation } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Check, ChevronDown, Plus, Settings, Swords } from "lucide-react";

import { CampaignService, SessionService } from "@gen/glyphoxa/management/v1/management_pb";
import {
  invalidateActiveCampaignScopedQueries,
  watchVoiceSessionEnd,
} from "@/lib/campaignCache";
import { isNotFound } from "@/lib/connectError";
import { usePopoverDismiss } from "@/components/ui/usePopoverDismiss";
import { CreateCampaignForm, useCreateCampaign } from "./CreateCampaignForm";
import { CampaignSettingsForm } from "./CampaignSettingsForm";

import "./campaignSwitcher.css";

// The Active-Campaign switcher (#267), placement decided in #266 (option a): a
// topbar control visible on every screen, showing the resolved Active Campaign
// and opening a panel to switch between campaigns or create a new one. Rename /
// archive / delete are out of scope here (#268 / #265).
//
// Switching writes the durable selection (SetActiveCampaign) and runs the
// Active-Campaign cache sweep so every campaign-scoped screen follows without a
// reload. resolveActiveCampaign stays live-first (#222): while a Voice Session
// is live its campaign still wins, so a switch made mid-Voice-Session shows the
// "takes effect after it ends" notice rather than being disabled (#266) — and
// the watchVoiceSessionEnd watcher re-runs the sweep at the moment that promise
// comes due, when the Voice Session actually ends.

// The panel is a single three-value state, so "which view" and "is it open" can
// never desync — a stale create form can't survive a close/reopen.
type Panel = "closed" | "list" | "create" | "edit";

export function CampaignSwitcher() {
  const queryClient = useQueryClient();
  const [panel, setPanel] = useState<Panel>("closed");
  const open = panel !== "closed";
  const wrapRef = useRef<HTMLDivElement>(null);

  // The resolved Active Campaign labels the trigger + marks the current row.
  // retry:false so a fresh, unseeded install's CodeNotFound settles at once into
  // the first-run create path rather than backing off first — the SAME option
  // every observer of this key uses (Configuration, Session), keeping retry
  // semantics deterministic on the shared cache entry. The label renders from
  // DATA, which React Query retains through a failed background refetch, so a
  // transient blip never blanks an already-shown campaign name.
  const activeQ = useQuery(CampaignService.method.getActiveCampaign, {}, { retry: false });
  const activeCampaign = activeQ.data?.campaign;
  const activeId = activeCampaign?.id;

  // First run: the operator has zero campaigns — GetActiveCampaign's CodeNotFound
  // is exactly that signal (resolution otherwise falls back to the most-recently-
  // created campaign), and it's the SAME signal Configuration's first-run CTA
  // keys off, so "no campaign exists yet" has one definition in the SPA. The web —
  // not `glyphoxa seed` — becomes the first-run path: the trigger is a create
  // call-to-action that opens straight into the create form.
  const firstRun = activeQ.isError && isNotFound(activeQ.error);

  // The campaign list renders only inside the open panel, so it isn't fetched
  // from every screen on boot/focus — the query wakes when the panel opens (a
  // create's invalidation just marks it stale for the next open).
  const listQ = useQuery(CampaignService.method.listCampaigns, {}, { enabled: open });
  const campaigns = listQ.data?.campaigns ?? [];

  // The live-Voice-Session notice needs Voice Session state only while the panel is
  // open. staleTime:0 (per-observer) so every open refetches — a Voice Session
  // started or ended since the last look must flip the notice, and the
  // client-wide 30s staleTime would otherwise serve the old answer on a quick
  // reopen. Default retries: GetSession answers {active:false} when idle (never
  // NotFound), so there's nothing to settle fast — but a transient blip
  // silently hiding a safety notice is worth retrying through.
  const sessionQ = useQuery(SessionService.method.getSession, {}, { enabled: open, staleTime: 0 });
  const sessionLive = sessionQ.data?.active ?? false;

  // Re-run the campaign sweep when the live Voice Session ends — the moment a
  // mid-Voice-Session switch actually takes effect. Mounted here because the switcher
  // is on every screen and owns the takes-effect promise.
  useEffect(() => watchVoiceSessionEnd(queryClient), [queryClient]);

  const close = () => setPanel("closed");

  const setActive = useMutation(CampaignService.method.setActiveCampaign, {
    onSuccess: () => {
      void invalidateActiveCampaignScopedQueries(queryClient);
      close();
    },
    onError: (err) => toast.error(`Couldn't switch campaign: ${err.message}`),
  });
  const switching = setActive.isPending;

  // Create closes the panel once the new campaign is active; the sweep inside the
  // flow has already refreshed the screens behind it.
  const createFlow = useCreateCampaign(close);

  // Entering the create form clears any prior attempt's state first: the create/
  // activate mutations live on this long-lived switcher, so a past failure would
  // otherwise render its stale "Couldn't create…" alert on the fresh, empty form.
  const enterCreate = () => {
    createFlow.reset();
    setPanel("create");
  };

  usePopoverDismiss(wrapRef, open, close);

  const onTriggerClick = () => {
    if (open) {
      close();
      return;
    }
    // First run has no list to show — open directly on a pristine create form.
    if (firstRun) enterCreate();
    else setPanel("list");
  };

  const triggerLabel = activeCampaign
    ? activeCampaign.name
    : firstRun
      ? "Create your first campaign"
      : activeQ.isPending
        ? null
        : "Select campaign";

  return (
    <div className="gx-campaign-switcher" ref={wrapRef}>
      <button
        type="button"
        className="gx-campaign-switcher__trigger"
        aria-expanded={open}
        // The accessible name carries the Active Campaign so assistive tech
        // perceives the current selection the switcher exists to surface (#266),
        // while the loading/first-run states keep a stable, meaningful name.
        aria-label={
          firstRun
            ? "Create your first campaign"
            : activeCampaign
              ? `Switch campaign — active: ${activeCampaign.name}`
              : "Switch campaign"
        }
        data-testid="campaign-switcher-trigger"
        data-firstrun={firstRun || undefined}
        onClick={onTriggerClick}
      >
        <span className="gx-campaign-switcher__sigil" aria-hidden>
          <Swords size={15} />
        </span>
        <span className="gx-campaign-switcher__label">
          <span className="gx-overline gx-campaign-switcher__overline">Campaign</span>
          {triggerLabel === null ? (
            <span className="gx-skeleton" data-testid="campaign-switcher-loading" />
          ) : (
            <span className="gx-campaign-switcher__name">{triggerLabel}</span>
          )}
        </span>
        <ChevronDown size={14} className="gx-campaign-switcher__chevron" />
      </button>

      {open && (
        <div className="gx-select__content gx-campaign-switcher__panel">
          {panel === "create" ? (
            <div className="gx-campaign-switcher__create">
              <div className="gx-campaign-switcher__create-head">New campaign</div>
              <CreateCampaignForm
                onSubmit={createFlow.submit}
                pending={createFlow.pending}
                error={createFlow.error}
                autoFocusName
                // First run has nowhere to go back to; every later create can
                // return to the campaign list.
                onCancel={firstRun ? undefined : () => setPanel("list")}
              />
            </div>
          ) : panel === "edit" && activeCampaign ? (
            // The per-campaign settings editor (#268), seeded from the resolved
            // Active Campaign. It shares the create form's classes and, like a
            // create, returns to the list on save or cancel.
            <div className="gx-campaign-switcher__create">
              <div className="gx-campaign-switcher__create-head">Campaign settings</div>
              <CampaignSettingsForm
                campaign={activeCampaign}
                onSaved={() => setPanel("list")}
                onCancel={() => setPanel("list")}
              />
            </div>
          ) : (
            <>
              {/* A labelled group of plain buttons (not a role=listbox): a short,
                  non-filterable list with no roving-tabindex keyboard model, so the
                  announced role matches the actual behaviour. The Active Campaign's
                  row carries aria-current; clicking it just closes the panel — no
                  RPC, no sweep, nothing to re-select. */}
              <ul className="gx-campaign-switcher__list" role="group" aria-label="Campaigns">
                {campaigns.map((c) => {
                  const isActive = c.id === activeId;
                  return (
                    <li key={c.id}>
                      <button
                        type="button"
                        aria-current={isActive || undefined}
                        className="gx-select__item gx-campaign-switcher__item"
                        data-active={isActive || undefined}
                        disabled={switching}
                        onClick={() =>
                          isActive ? close() : setActive.mutate({ campaignId: c.id })
                        }
                      >
                        <span className="gx-campaign-switcher__item-meta">
                          <span className="gx-campaign-switcher__item-name">{c.name}</span>
                          {c.system && (
                            <span className="gx-campaign-switcher__item-system">{c.system}</span>
                          )}
                        </span>
                        {isActive && <Check size={14} />}
                      </button>
                    </li>
                  );
                })}
              </ul>

              {listQ.isPending && <div className="gx-skeleton" data-testid="campaign-list-loading" />}

              {listQ.isError && (
                <p className="gx-campaign__error gx-campaign-switcher__error" role="alert">
                  Couldn&apos;t load campaigns: {listQ.error.message}
                </p>
              )}

              <button type="button" className="gx-campaign-switcher__new" onClick={enterCreate}>
                <Plus size={15} /> New campaign
              </button>

              {/* Edit the resolved Active Campaign's name / System / Language
                  (#268). Disabled until a campaign resolves — there is nothing to
                  edit before one exists (first run opens straight into create). */}
              <button
                type="button"
                className="gx-campaign-switcher__new"
                onClick={() => setPanel("edit")}
                disabled={!activeCampaign}
              >
                <Settings size={15} /> Campaign settings
              </button>
            </>
          )}

          {/* The deferral notice renders in BOTH panel views: a create also ends
              in SetActiveCampaign, so mid-Voice-Session it is equally deferred —
              without the cue here, a create would close the panel onto an
              unchanged trigger and read as a silent failure. */}
          {sessionLive && (
            <p className="gx-campaign-switcher__notice" role="note">
              A Voice Session is live — switching takes effect after it ends.
            </p>
          )}
          {/* A safety notice that silently vanishes is worse than a hedge: if the
              Voice Session read failed even after retries, say so instead of implying
              the switch is immediate. */}
          {sessionQ.isError && (
            <p className="gx-campaign-switcher__notice" role="note">
              Couldn&apos;t check for a live Voice Session — a switch may take effect only after it
              ends.
            </p>
          )}
        </div>
      )}
    </div>
  );
}
