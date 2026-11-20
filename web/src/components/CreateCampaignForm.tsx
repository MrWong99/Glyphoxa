import { useRef, useState } from "react";
import type { FormEvent } from "react";
import { useMutation } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import { Input } from "@/components/ui/Input";
import { Button } from "@/components/ui/Button";
import { invalidateActiveCampaignScopedQueries } from "@/lib/campaignCache";
import { invalidateMethodQueries } from "@/lib/queryClient";
import { useI18n } from "@/i18n";
import { SpokenLanguageSelect, SYSTEM_SUGGESTIONS, useSupportedLanguages } from "@/components/SpokenLanguageSelect";

import "./createCampaignForm.css";
import { errorMessage } from "@/lib/connectError";

// The campaign create form + its create-then-activate flow (#267), shared by the
// topbar CampaignSwitcher's create mode and the Configuration first-run CTA.
// Name is required; System is free text with the same suggestions Campaign
// settings offers, pre-filled "D&D 5e"; Language is the same registry-backed
// SELECT Campaign settings uses (SpokenLanguageSelect), defaulting to the
// current display language when the registry supports it — so a one-click
// create yields a campaign the settings form would show unchanged (ADR-0009
// auto-Butler fires server-side).

const DEFAULT_SYSTEM = SYSTEM_SUGGESTIONS[0];
const SYSTEM_DATALIST_ID = "gx-create-system-suggestions";

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
type CreateCampaignError = { phase: "create" | "activate"; error: Error };

// useCreateCampaign runs the create-then-activate flow: CreateCampaign mints the
// campaign (auto-Butler, ADR-0009), then SetActiveCampaign selects it so its
// Butler is what the Campaign screen shows next (AC), then the Active-Campaign
// cache sweep + a listCampaigns refresh let every campaign surface follow the new
// selection without a reload. `onCreated` fires once the new campaign is active
// (the switcher closes its popover; the first-run CTA needs nothing further — its
// card swaps to the live header when GetActiveCampaign re-resolves).
//
// Both legs also toast their failures: the popover form can be dismissed while
// an RPC is in flight, which unmounts the inline alert — without the toast a
// late rejection would be completely silent (mirrors the AgentEditor's paired
// toast + inline-status idiom).
export function useCreateCampaign(onCreated?: () => void) {
  const { t } = useI18n();
  const queryClient = useQueryClient();

  // The flow's imperative spine (#267 review). A double-click lands both clicks
  // in one tick — before any re-render — so render-scoped mutation state can't
  // stop the second CreateCampaign; only a ref can. "done" is terminal until
  // reset(): once a create has succeeded, submit must NEVER re-run it — the
  // campaign exists, so a resubmit re-attempts just the activation leg or no-ops.
  const flow = useRef<"idle" | "busy" | "done">("idle");
  const createdId = useRef<string | undefined>(undefined);

  const invalidateList = () =>
    invalidateMethodQueries(queryClient, CampaignService.method.listCampaigns);

  const setActive = useMutation(CampaignService.method.setActiveCampaign, {
    onSuccess: () => {
      flow.current = "done";
      onCreated?.();
    },
    onError: (err) => {
      flow.current = "idle"; // retryable — as a pure activation, via createdId
      toast.error(t("components.createdCampaignCouldntSwitchToast", { message: errorMessage(err) }));
    },
    // The sweep runs on SETTLED, not just success: even a failed activation must
    // refetch resolution truth. On first run the just-created campaign already
    // resolves active via the most-recently-created fallback (#222), so the CTA
    // swaps to the live header instead of sticking on a stale error state; the
    // failure itself survives in the toast + inline alert.
    onSettled: () => void invalidateActiveCampaignScopedQueries(queryClient),
  });

  const create = useMutation(CampaignService.method.createCampaign, {
    onSuccess: (res) => {
      // Refresh the switcher's list as soon as the campaign exists — even if the
      // follow-up activate fails, the new campaign must show up in the picker.
      void invalidateList();
      // A campaign always comes back on success; guard anyway so a malformed
      // response can't fire SetActiveCampaign with an empty id — and park the
      // flow as done so a resubmit can't re-create.
      if (res.campaign) {
        createdId.current = res.campaign.id;
        setActive.mutate({ campaignId: res.campaign.id });
      } else {
        flow.current = "done";
      }
    },
    onError: (err) => {
      flow.current = "idle"; // retryable — nothing was created
      toast.error(t("components.couldntCreateCampaignToast", { message: errorMessage(err) }));
    },
  });

  const submit = (fields: CampaignFields) => {
    if (flow.current !== "idle") return; // in flight or complete — never double-fire
    flow.current = "busy";
    if (createdId.current) setActive.mutate({ campaignId: createdId.current });
    else create.mutate(fields);
  };

  // reset clears both legs so a reopened / re-entered form starts pristine — the
  // mutations live on the long-lived switcher, so their last error would
  // otherwise bleed onto the next, empty form (#267 review).
  const reset = () => {
    flow.current = "idle";
    createdId.current = undefined;
    create.reset();
    setActive.reset();
  };

  const error: CreateCampaignError | null = create.error
    ? { phase: "create", error: create.error }
    : setActive.error
      ? { phase: "activate", error: setActive.error }
      : null;

  return {
    submit,
    reset,
    // Pending spans BOTH legs — and holds after full success: the first-run CTA
    // stays mounted until the swept GetActiveCampaign refetch swaps the card, and
    // the form must read as busy (not re-submittable) through that window.
    pending: create.isPending || setActive.isPending || setActive.isSuccess,
    error,
  };
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
  // the activation retry (renaming is #268's slice).
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
          list={SYSTEM_DATALIST_ID}
          hint={t("components.settingsSystemHint")}
          disabled={locked}
        />
        <datalist id={SYSTEM_DATALIST_ID}>
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
        <Button type="submit" variant="primary" disabled={!canSubmit}>
          {pending
            ? t("common.saving")
            : activateFailed
              ? t("components.retryActivation")
              : (submitLabel ?? t("components.createCampaign"))}
        </Button>
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
