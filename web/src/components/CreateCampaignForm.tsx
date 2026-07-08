import { useCallback, useState } from "react";
import type { FormEvent } from "react";
import { useMutation, createConnectQueryKey } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import { Input } from "@/components/ui/Input";
import { Button } from "@/components/ui/Button";
import { invalidateActiveCampaignScopedQueries } from "@/lib/campaignCache";

import "./createCampaignForm.css";

// The campaign create form + its create-then-activate flow (#267), shared by the
// topbar CampaignSwitcher's create mode and the Configuration first-run CTA.
// Name is required; System/Language are free-text inputs stored EXACTLY as typed
// (no validation, no vocabulary — that curation is #268 and may retrofit),
// pre-filled with the seed defaults so a one-click create yields the same
// campaign `glyphoxa seed` would (ADR-0009 auto-Butler fires server-side).

// The seed defaults CreateCampaign is pre-filled with — the same values the
// seeder writes, so the web becomes a drop-in first-run path (#267).
export const SEED_SYSTEM = "dnd5e";
export const SEED_LANGUAGE = "en";

type CampaignFields = { name: string; system: string; language: string };

// CreateCampaignError distinguishes WHICH leg of the create-then-activate flow
// failed. "create" means the campaign was never made; "activate" means it WAS
// created (and is already in the list) but selecting it failed — a distinction
// the form must surface, since a blanket "couldn't create" would be a lie and a
// blind retry would mint a duplicate (#267 review).
export type CreateCampaignError = { phase: "create" | "activate"; error: Error };

// useCreateCampaign runs the create-then-activate flow: CreateCampaign mints the
// campaign (auto-Butler, ADR-0009), then SetActiveCampaign selects it so its
// Butler is what the Campaign screen shows next (AC), then the Active-Campaign
// cache sweep + a listCampaigns refresh let every campaign surface follow the new
// selection without a reload. `onCreated` fires once the new campaign is active
// (the switcher closes its popover; the first-run CTA needs nothing further — its
// card swaps to the live header when GetActiveCampaign re-resolves).
export function useCreateCampaign(onCreated?: () => void) {
  const queryClient = useQueryClient();

  const invalidateList = () =>
    queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: CampaignService.method.listCampaigns,
        cardinality: "finite",
      }),
    });

  const setActive = useMutation(CampaignService.method.setActiveCampaign, {
    onSuccess: () => {
      void invalidateActiveCampaignScopedQueries(queryClient);
      onCreated?.();
    },
  });

  const create = useMutation(CampaignService.method.createCampaign, {
    onSuccess: (res) => {
      // Refresh the switcher's list as soon as the campaign exists — even if the
      // follow-up activate fails, the new campaign must show up in the picker.
      void invalidateList();
      // A campaign always comes back on success; guard anyway so a malformed
      // response can't fire SetActiveCampaign with an empty id.
      if (res.campaign) setActive.mutate({ campaignId: res.campaign.id });
    },
  });

  // If the campaign was already created and ONLY activation failed, retrying must
  // re-run just the activation — calling CreateCampaign again would mint a
  // duplicate campaign (#267 review). Otherwise this is a fresh create.
  const submit = (fields: CampaignFields) => {
    if (create.isSuccess && create.data?.campaign && setActive.isError) {
      setActive.mutate({ campaignId: create.data.campaign.id });
    } else {
      create.mutate(fields);
    }
  };

  // reset clears both legs so a reopened / re-entered form starts pristine — the
  // mutations live on the long-lived switcher, so their last error would
  // otherwise bleed onto the next, empty form (#267 review). Stable so callers
  // can hold it across renders.
  const reset = useCallback(() => {
    create.reset();
    setActive.reset();
  }, [create.reset, setActive.reset]);

  const error: CreateCampaignError | null = create.error
    ? { phase: "create", error: create.error }
    : setActive.error
      ? { phase: "activate", error: setActive.error }
      : null;

  return {
    submit,
    reset,
    // Pending spans BOTH legs so the form stays locked through the activate step,
    // not just the create RPC.
    pending: create.isPending || setActive.isPending,
    error,
  };
}

export function CreateCampaignForm({
  onSubmit,
  pending,
  error,
  submitLabel = "Create campaign",
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
  const [name, setName] = useState("");
  const [system, setSystem] = useState(SEED_SYSTEM);
  const [language, setLanguage] = useState(SEED_LANGUAGE);

  const canSubmit = name.trim() !== "" && !pending;
  // A created-but-not-activated campaign is retried as a pure activation, so the
  // primary action re-labels — clicking it selects the existing campaign, it does
  // not create a second one.
  const activateFailed = error?.phase === "activate";

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
        label="Name"
        value={name}
        onChange={(e) => setName(e.target.value)}
        placeholder="e.g. The Sunless Citadel"
        autoFocus={autoFocusName}
        required
      />
      <div className="gx-campaign-create__row">
        <Input
          label="System"
          value={system}
          onChange={(e) => setSystem(e.target.value)}
          hint="Free-text — e.g. dnd5e, pf2e"
        />
        <Input
          label="Language"
          value={language}
          onChange={(e) => setLanguage(e.target.value)}
          hint="BCP-47 tag — e.g. en, de"
        />
      </div>
      <div className="gx-campaign-create__actions">
        <Button type="submit" variant="primary" disabled={!canSubmit}>
          {pending ? "Saving…" : activateFailed ? "Retry activation" : submitLabel}
        </Button>
        {onCancel && (
          <Button type="button" variant="ghost" onClick={onCancel} disabled={pending}>
            Cancel
          </Button>
        )}
        {error && (
          <span className="gx-campaign-create__error" role="alert">
            {activateFailed
              ? `Created it, but couldn't switch to it: ${error.error.message}`
              : `Couldn't create: ${error.error.message}`}
          </span>
        )}
      </div>
    </form>
  );
}
