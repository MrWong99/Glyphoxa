import { useEffect, useRef, useState } from "react";
import { useQuery, useMutation } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { Check, ChevronDown, Plus, Swords } from "lucide-react";

import { CampaignService, SessionService } from "@gen/glyphoxa/management/v1/management_pb";
import { invalidateActiveCampaignScopedQueries } from "@/lib/campaignCache";
import { CreateCampaignForm, useCreateCampaign } from "./CreateCampaignForm";

import "./campaignSwitcher.css";

// The Active-Campaign switcher (#267), placement decided in #266 (option a): a
// topbar control visible on every screen, showing the resolved Active Campaign
// and opening a panel to switch between campaigns or create a new one. Rename /
// archive / delete are out of scope here (#268 / #265).
//
// Switching writes the durable selection (SetActiveCampaign) and runs the
// Active-Campaign cache sweep so every campaign-scoped screen follows without a
// reload. resolveActiveCampaign stays live-first (#222): while a Voice Session is
// live its campaign still wins, so a switch made mid-session shows the "takes
// effect after this session" notice rather than being disabled (#266).

export function CampaignSwitcher() {
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [mode, setMode] = useState<"list" | "create">("list");
  const wrapRef = useRef<HTMLDivElement>(null);

  // The campaigns to switch between, and the resolved Active Campaign that labels
  // the trigger + marks the current row. retry:false so a fresh, unseeded
  // install's CodeNotFound settles at once into the first-run create path rather
  // than backing off first (mirrors AuthGate).
  const listQ = useQuery(CampaignService.method.listCampaigns, {});
  const activeQ = useQuery(CampaignService.method.getActiveCampaign, {}, { retry: false });
  const campaigns = listQ.data?.campaigns ?? [];
  const activeCampaign = activeQ.data?.campaign;
  const activeId = activeCampaign?.id;

  // First run: the operator has zero campaigns. The web — not `glyphoxa seed` —
  // becomes the first-run path, so the trigger is a create call-to-action that
  // opens straight into the create form.
  const firstRun = listQ.isSuccess && campaigns.length === 0;

  // The live-session notice only needs session state while the panel is open, so
  // the read is gated on `open` — the switcher doesn't poll GetSession from every
  // screen just to label a control the operator hasn't touched.
  const sessionQ = useQuery(SessionService.method.getSession, {}, { enabled: open, retry: false });
  const sessionLive = sessionQ.data?.active ?? false;

  const close = () => {
    setOpen(false);
    setMode("list");
  };

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

  // Close on outside click / Escape, like the Combobox popover — and reset to list
  // mode so the next open starts on the campaign list, not a half-filled form.
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (wrapRef.current && !wrapRef.current.contains(e.target as Node)) close();
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") close();
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  // Entering the create form clears any prior attempt's state first: the create/
  // activate mutations live on this long-lived switcher, so a past failure would
  // otherwise render its stale "Couldn't create…" alert on the fresh, empty form.
  const enterCreate = () => {
    createFlow.reset();
    setMode("create");
  };

  const onTriggerClick = () => {
    if (open) {
      close();
      return;
    }
    // First run has no list to show — open directly on a pristine create form.
    if (firstRun) enterCreate();
    else setMode("list");
    setOpen(true);
  };

  const loading = activeQ.isPending || listQ.isPending;
  const triggerLabel = activeCampaign
    ? activeCampaign.name
    : firstRun
      ? "Create your first campaign"
      : loading
        ? null
        : "Select campaign";

  return (
    <div className="gx-campaign-switcher" ref={wrapRef}>
      <button
        type="button"
        className="gx-campaign-switcher__trigger"
        aria-expanded={open}
        // The accessible name carries the active campaign so assistive tech
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
          <span className="gx-campaign-switcher__overline">Campaign</span>
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
          {mode === "create" ? (
            <div className="gx-campaign-switcher__create">
              <div className="gx-campaign-switcher__create-head">New campaign</div>
              <CreateCampaignForm
                onSubmit={createFlow.submit}
                pending={createFlow.pending}
                error={createFlow.error}
                autoFocusName
                // First run has nowhere to go back to; every later create can
                // return to the campaign list.
                onCancel={firstRun ? undefined : () => setMode("list")}
              />
            </div>
          ) : (
            <>
              {/* A labelled group of plain buttons (not a role=listbox): a short,
                  non-filterable list with no roving-tabindex keyboard model, so the
                  announced role matches the actual behaviour. The current campaign
                  carries aria-current. */}
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
                        onClick={() => setActive.mutate({ campaignId: c.id })}
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

              {listQ.isError && (
                <p className="gx-campaign-switcher__error" role="alert">
                  Couldn&apos;t load campaigns: {listQ.error.message}
                </p>
              )}

              {sessionLive && (
                <p className="gx-campaign-switcher__notice" role="note">
                  A session is live — switching takes effect after this session ends.
                </p>
              )}

              <button
                type="button"
                className="gx-campaign-switcher__new"
                onClick={enterCreate}
              >
                <Plus size={15} /> New campaign
              </button>
            </>
          )}
        </div>
      )}
    </div>
  );
}
