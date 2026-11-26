import { useId, useRef, useState, type ReactNode } from "react";
import type { FormEvent } from "react";
import { useMutation } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import { useScreenNav } from "@/app/screenNav";
import { Input } from "@/components/ui/Input";
import { Button } from "@/components/ui/Button";
import { invalidateActiveCampaignScopedQueries } from "@/lib/campaignCache";
import { invalidateMethodQueries } from "@/lib/queryClient";
import { useI18n } from "@/i18n";
import { SpokenLanguageSelect, SYSTEM_SUGGESTIONS, useSupportedLanguages } from "@/components/SpokenLanguageSelect";
import { afterEditorsSettle, useCampaignSwitch, type SwitchResult } from "@/components/useCampaignSwitch";
import { WorkingButton } from "@/components/WorkingButton";

import "./createCampaignForm.css";
import { errorMessage } from "@/lib/connectError";

// The campaign create form + its create-then-activate flow (#267), shared by
// the "New campaign" dialog (Campaigns page, topbar switcher) and the
// first-campaign card. Name is required; System is free text with the same
// suggestions Campaign settings offers, pre-filled "D&D 5e"; Language is the
// same registry-backed SELECT Campaign settings uses (SpokenLanguageSelect),
// defaulting to the current display language when the registry supports it —
// so a one-click create yields a campaign the settings form would show
// unchanged (ADR-0009 auto-Butler fires server-side).

const DEFAULT_SYSTEM = SYSTEM_SUGGESTIONS[0];

// defaultLanguage picks the create form's starting Campaign Language: the
// display language if the registry supports it, else English if supported,
// else the first registered language. Before the registry answers (or when it
// failed) the display language rides as-is — the SELECT shows it as a plain
// option and the server stays the validator.
function defaultLanguage(display: string, supported: readonly string[]): string {
  if (supported.length === 0 || supported.includes(display)) return display;
  if (supported.includes("en")) return "en";
  return supported[0];
}

type CampaignFields = { name: string; system: string; language: string };

// CreateCampaignError distinguishes WHICH leg of the create-then-activate flow
// failed. "create" means the campaign was never made; "activate" means it WAS
// created (and is already in the list) but selecting it failed — a distinction
// the form must surface, since a blanket "couldn't create" would be a lie and a
// blind retry would mint a duplicate (#267 review).
type CreateCampaignError = { phase: "create" | "activate"; error: unknown };

// useCreateCampaign runs the create-then-activate flow:
//
//   1. asks first if a switch would discard unsaved edits (the create ENDS in a
//      switch — asking after the campaign exists would leave a half-done flow
//      behind a Cancel);
//   2. CreateCampaign mints the campaign (auto-Butler, ADR-0009);
//   3. the guarded switch (useCampaignSwitch) makes it the Active Campaign and
//      runs the Active-Campaign cache sweep;
//   4. it says so — "Created {name} — your Butler is ready" — and lands on the
//      new campaign's Overview. While a Voice Session is live the switch is
//      deferred: the toast says that instead, and nothing navigates (the
//      Overview would still be the session's campaign).
//
// `onCreated` fires once the selection is written (the dialog closes itself).
//
// Both legs also toast their failures: the form can be dismissed while an RPC
// is in flight, which unmounts the inline alert — without the toast a late
// rejection would be completely silent.
export function useCreateCampaign(onCreated?: (result: SwitchResult) => void): {
  submit: (fields: CampaignFields) => void;
  reset: () => void;
  /** Busy from the first click until the new campaign is on screen. */
  pending: boolean;
  error: CreateCampaignError | null;
  /** Render this once — it is the "Discard unsaved changes?" question. */
  dialog: ReactNode;
} {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const nav = useScreenNav();
  const campaignSwitch = useCampaignSwitch();

  // The flow's imperative spine (#267 review). A double-click lands both clicks
  // in one tick — before any re-render — so render-scoped state can't stop the
  // second CreateCampaign; only a ref can. "done" is terminal until reset():
  // once a create has succeeded, submit must NEVER re-run it — the campaign
  // exists, so a resubmit re-attempts just the activation leg or no-ops.
  const flow = useRef<"idle" | "busy" | "done">("idle");
  const created = useRef<{ id: string; name: string } | undefined>(undefined);
  // What the form renders from. `phase` holds at "done" after full success: the
  // first-campaign card stays mounted until the swept GetActiveCampaign refetch
  // swaps it out, and must read as busy (not re-submittable) through that
  // window.
  const [phase, setPhase] = useState<"idle" | "busy" | "done">("idle");
  const [error, setError] = useState<CreateCampaignError | null>(null);

  const invalidateList = () =>
    invalidateMethodQueries(queryClient, CampaignService.method.listCampaigns);

  const activate = (campaign: { id: string; name: string }) => {
    campaignSwitch.activate(campaign, {
      announce: ({ deferred }) => {
        if (deferred) toast.info(t("campaigns.createdDeferred", { name: campaign.name }));
        else toast.success(t("campaigns.created", { name: campaign.name }));
      },
      onSwitched: (result) => {
        flow.current = "done";
        setPhase("done");
        onCreated?.(result);
        if (!result.deferred) afterEditorsSettle(() => nav.goCampaignView("overview"));
      },
      onError: (err) => {
        flow.current = "idle"; // retryable — as a pure activation, via `created`
        setPhase("idle");
        setError({ phase: "activate", error: err });
        toast.error(t("components.createdCampaignCouldntSwitchToast", { message: errorMessage(err) }));
        // Even a failed activation must refetch resolution truth. On first run
        // the just-created campaign already resolves active via the
        // most-recently-created fallback (#222), so the first-campaign card
        // swaps to the live screen instead of sticking on a stale error state;
        // the failure itself survives in the toast + inline alert.
        void invalidateActiveCampaignScopedQueries(queryClient);
      },
    });
  };

  const create = useMutation(CampaignService.method.createCampaign, {
    onSuccess: (res) => {
      // Refresh the campaign lists as soon as the campaign exists — even if the
      // follow-up activate fails, the new campaign must show up in them.
      void invalidateList();
      // A campaign always comes back on success; guard anyway so a malformed
      // response can't fire SetActiveCampaign with an empty id — and park the
      // flow as done so a resubmit can't re-create.
      if (res.campaign) {
        created.current = { id: res.campaign.id, name: res.campaign.name };
        activate(created.current);
      } else {
        flow.current = "done";
        setPhase("done");
      }
    },
    onError: (err) => {
      flow.current = "idle"; // retryable — nothing was created
      setPhase("idle");
      setError({ phase: "create", error: err });
      toast.error(t("components.couldntCreateCampaignToast", { message: errorMessage(err) }));
    },
  });

  const submit = (fields: CampaignFields) => {
    if (flow.current !== "idle") return; // in flight or complete — never double-fire
    const run = () => {
      if (flow.current !== "idle") return;
      flow.current = "busy";
      setPhase("busy");
      setError(null);
      if (created.current) activate(created.current);
      else create.mutate(fields);
    };
    // A retry of the activation leg was already confirmed the first time.
    if (created.current) run();
    else campaignSwitch.confirmDiscard(run);
  };

  // reset clears both legs so a re-entered form starts pristine.
  const reset = () => {
    flow.current = "idle";
    created.current = undefined;
    setPhase("idle");
    setError(null);
    create.reset();
  };

  return { submit, reset, pending: phase !== "idle", error, dialog: campaignSwitch.dialog };
}

export function CreateCampaignForm({
  onSubmit,
  pending,
  error,
  submitLabel,
  onCancel,
  autoFocusName = false,
}: {
  onSubmit: (fields: CampaignFields) => void;
  pending: boolean;
  error: CreateCampaignError | null;
  submitLabel?: string;
  onCancel?: () => void;
  autoFocusName?: boolean;
}) {
  const { t, lang } = useI18n();
  // Generated: the first-campaign card and the "New campaign" dialog can be on
  // the page together, and two datalists must not share an id.
  const systemListId = useId();
  const [name, setName] = useState("");
  const [system, setSystem] = useState(DEFAULT_SYSTEM);
  // undefined until the operator picks: the default follows the display
  // language and the registry answer until then.
  const [pickedLanguage, setPickedLanguage] = useState<string | undefined>(undefined);
  const supported = useSupportedLanguages().data?.languages ?? [];
  const language = pickedLanguage ?? defaultLanguage(lang, supported);

  // A created-but-not-activated campaign is retried as a pure activation, so the
  // primary action re-labels and the fields LOCK: the campaign already exists
  // with the submitted values, and an edit here would be silently discarded by
  // the activation retry (renaming is Campaign settings' job).
  const activateFailed = error?.phase === "activate";
  const locked = pending || activateFailed;
  const canSubmit = name.trim() !== "" && !pending;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!canSubmit) return;
    // System/Language ride the wire exactly as stored today (opaque free-text);
    // only Name is trimmed, since it drives the required check.
    onSubmit({ name: name.trim(), system, language });
  };

  return (
    <form className="gx-campaign-create" onSubmit={submit}>
      <Input
        label={t("components.nameLabel")}
        value={name}
        onChange={(e) => setName(e.target.value)}
        placeholder={t("components.namePlaceholder")}
        autoFocus={autoFocusName}
        disabled={locked}
        required
      />
      <div className="gx-campaign-create__row">
        <Input
          label={t("components.gameSystemLabel")}
          value={system}
          onChange={(e) => setSystem(e.target.value)}
          list={systemListId}
          hint={t("components.settingsSystemHint")}
          disabled={locked}
        />
        <datalist id={systemListId}>
          {SYSTEM_SUGGESTIONS.map((s) => (
            <option key={s} value={s} />
          ))}
        </datalist>
        <SpokenLanguageSelect
          value={language}
          onValueChange={setPickedLanguage}
          hint={t("components.createLanguageHint")}
          disabled={locked}
        />
      </div>
      <div className="gx-campaign-create__actions">
        {/* Busy, not disabled, while the flow runs (WorkingButton): a failure
            must find the GM still on the button that retries it. */}
        <WorkingButton type="submit" variant="primary" disabled={name.trim() === ""} working={pending}>
          {pending
            ? t("campaigns.creating")
            : activateFailed
              ? t("components.retryActivation")
              : (submitLabel ?? t("components.createCampaign"))}
        </WorkingButton>
        {onCancel && (
          <Button type="button" variant="ghost" onClick={onCancel} disabled={pending}>
            {t("common.cancel")}
          </Button>
        )}
        {error && (
          <span className="gx-campaign-create__error" role="alert">
            {activateFailed
              ? t("components.createdCouldntSwitchInline", { message: errorMessage(error.error) })
              : t("components.couldntCreateInline", { message: errorMessage(error.error) })}
          </span>
        )}
      </div>
    </form>
  );
}
