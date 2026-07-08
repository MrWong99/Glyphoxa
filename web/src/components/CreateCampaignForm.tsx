import { useRef, useState } from "react";
import type { FormEvent } from "react";
import { useMutation, createConnectQueryKey } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

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
const SEED_SYSTEM = "dnd5e";
const SEED_LANGUAGE = "en";

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
//
// Both legs also toast their failures: the popover form can be dismissed while
// an RPC is in flight, which unmounts the inline alert — without the toast a
// late rejection would be completely silent (mirrors the AgentEditor's paired
// toast + inline-status idiom).
export function useCreateCampaign(onCreated?: () => void) {
  const queryClient = useQueryClient();

  // The flow's imperative spine (#267 review). A double-click lands both clicks
  // in one tick — before any re-render — so render-scoped mutation state can't
  // stop the second CreateCampaign; only a ref can. "done" is terminal until
  // reset(): once a create has succeeded, submit must NEVER re-run it — the
  // campaign exists, so a resubmit re-attempts just the activation leg or no-ops.
  const flow = useRef<"idle" | "busy" | "done">("idle");
  const createdId = useRef<string | undefined>(undefined);

  const invalidateList = () =>
    queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: CampaignService.method.listCampaigns,
        cardinality: "finite",
      }),
    });

  const setActive = useMutation(CampaignService.method.setActiveCampaign, {
    onSuccess: () => {
      flow.current = "done";
      onCreated?.();
    },
    onError: (err) => {
      flow.current = "idle"; // retryable — as a pure activation, via createdId
      toast.error(`Created the campaign, but couldn't switch to it: ${err.message}`);
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
      toast.error(`Couldn't create the campaign: ${err.message}`);
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
        label="Name"
        value={name}
        onChange={(e) => setName(e.target.value)}
        placeholder="e.g. The Sunless Citadel"
        autoFocus={autoFocusName}
        disabled={locked}
        required
      />
      <div className="gx-campaign-create__row">
        <Input
          label="System"
          value={system}
          onChange={(e) => setSystem(e.target.value)}
          hint="Free-text — e.g. dnd5e, pf2e"
          disabled={locked}
        />
        <Input
          label="Language"
          value={language}
          onChange={(e) => setLanguage(e.target.value)}
          hint="BCP-47 tag — e.g. en, de"
          disabled={locked}
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
