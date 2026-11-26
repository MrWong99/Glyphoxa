import { useEffect, useId, useRef, useState } from "react";
import type { FormEvent } from "react";
import { useMutation } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import { CampaignService, type Campaign } from "@gen/glyphoxa/management/v1/management_pb";
import { Input } from "@/components/ui/Input";
import { Switch } from "@/components/ui/Switch";
import { Button } from "@/components/ui/Button";
import { invalidateActiveCampaignScopedQueries } from "@/lib/campaignCache";
import { invalidateMethodQueries } from "@/lib/queryClient";
import { useI18n } from "@/i18n";
import { SpokenLanguageSelect, SYSTEM_SUGGESTIONS } from "@/components/SpokenLanguageSelect";
import { WorkingButton } from "@/components/WorkingButton";

import "./createCampaignForm.css";
import { errorMessage } from "@/lib/connectError";

// The per-campaign settings editor (#268): a campaign's name, System, Campaign
// Language and its Session Highlights settings. It edits whichever campaign it
// is handed — UpdateCampaign takes an explicit id, so the campaign does not
// have to be the Active one (the "active only" limit was the old topbar
// popover's, not the server's). Two hosts:
//
//   page    the campaign's Settings view: sections under the campaign header,
//           a save row that stays at the bottom of the screen while the form is
//           in view, "Discard changes" to go back to what is saved;
//   dialog  Settings from a card on the Campaigns page: the same sections in a
//           dialog, Cancel to close.
//
// The two curated fields diverge per the recorded product decisions:
//
//   System   — free text with datalist suggestions (no enum, rides the wire
//              opaque server-side, ADR-0039). The suggestions are a static
//              web-side convenience only.
//   Language — a SELECT constrained to the registered phonetic encoders
//              (SpokenLanguageSelect, shared with the create form), so a new
//              encoder appears automatically and no language list is
//              hardcoded in the web tier.
//
// A language change mutates NOTHING now — existing Agents' voice settings are
// untouched (ADR-0009, #224); it takes effect on the next Voice Session, which
// the static hint under the select states.
//
// Session Highlights are shown openly, not behind an "advanced" disclosure:
// arming the recording is a consent decision the whole table is asked about,
// and a GM looking for "why are there no highlights" has to be able to find the
// bar that decides it.

type Values = {
  name: string;
  system: string;
  language: string;
  tapeArmed: boolean;
  highlightBar: number;
  highlightConfirmWindows: number;
};

function valuesOf(campaign: Campaign): Values {
  return {
    name: campaign.name,
    system: campaign.system,
    language: campaign.language,
    tapeArmed: campaign.tapeArmed,
    highlightBar: campaign.highlightBar,
    highlightConfirmWindows: campaign.highlightConfirmWindows,
  };
}

// The knob fields are held as strings, like the spend caps: 0 on the wire means
// "engine default" and is rendered as an empty field with the default in the
// placeholder — so clearing a field IS the reset gesture.
const knobText = (n: number) => (n === 0 ? "" : String(n));

export function CampaignSettingsForm({
  campaign,
  onSaved,
  onCancel,
  onDirtyChange,
  variant = "dialog",
}: {
  campaign: Campaign;
  onSaved: () => void;
  /** Dialog host: closes the dialog. The page host has "Discard changes" instead. */
  onCancel?: () => void;
  /** Reports whether the fields differ from what was last saved. */
  onDirtyChange?: (dirty: boolean) => void;
  variant?: "page" | "dialog";
}) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  // Every id in here is generated: two of these forms on one page (it used to
  // be the Settings view plus the topbar popover) must not share ids, or a
  // label names the other form's control.
  const uid = useId();
  const formRef = useRef<HTMLFormElement>(null);
  const [name, setName] = useState(campaign.name);
  const [system, setSystem] = useState(campaign.system);
  const [language, setLanguage] = useState(campaign.language);
  const [tapeArmed, setTapeArmed] = useState(campaign.tapeArmed);
  const [highlightBar, setHighlightBar] = useState(knobText(campaign.highlightBar));
  const [highlightConfirm, setHighlightConfirm] = useState(knobText(campaign.highlightConfirmWindows));

  // listCampaigns is campaign-INVARIANT (lib/campaignCache.ts), so the sweep
  // skips it — a name/system edit must invalidate it explicitly or the switcher
  // and the Campaigns page keep showing the stale name/system.
  const invalidateList = () =>
    invalidateMethodQueries(queryClient, CampaignService.method.listCampaigns);

  const update = useMutation(CampaignService.method.updateCampaign, {
    onSuccess: () => {
      void invalidateList();
      void invalidateActiveCampaignScopedQueries(queryClient);
      onSaved();
    },
    onError: (err) =>
      toast.error(t("components.couldntSaveCampaignSettings", { message: errorMessage(err) })),
  });

  const nameMissing = name.trim() === "";

  // What Save would send right now. Derived once per render so the save and the
  // unsaved-edits report below can never disagree about what "changed" means.
  //
  // The knob fields are always sent explicitly: an empty field is 0 = "back
  // to the engine default" (a real write), never an omission. The server is
  // the range authority (0–10) — an out-of-range value comes back as the
  // save error, matching the spend-caps posture. The isFinite fallbacks keep
  // a non-numeric value (unreachable through a real number input) from riding
  // as NaN — which an int32 field would serialize as JSON null, dodging the
  // designed server refusal.
  const barNum = Number(highlightBar);
  const bar = highlightBar.trim() === "" || !Number.isFinite(barNum) ? 0 : barNum;
  const confirmNum = Math.round(Number(highlightConfirm));
  const confirm = highlightConfirm.trim() === "" || !Number.isFinite(confirmNum) ? 0 : confirmNum;
  // Name is trimmed (the server rejects empty); System/Language ride opaque.
  const values: Values = {
    name: name.trim(),
    system,
    language,
    tapeArmed,
    highlightBar: bar,
    highlightConfirmWindows: confirm,
  };

  // Unsaved-edits report for a host that guards leaving (both do). The baseline
  // is what was last SAVED — seeded from the campaign, then moved to the
  // submitted values on each successful save — not the campaign prop: that only
  // catches up when the post-save refetch lands, and a form that has just been
  // saved must not read as dirty in between.
  const [saved, setSaved] = useState<Values>(() => valuesOf(campaign));
  // Shown beside Save once this form has saved something, until the next edit.
  const [justSaved, setJustSaved] = useState(false);
  const isDirty = (Object.keys(saved) as (keyof Values)[]).some((k) => values[k] !== saved[k]);
  useEffect(() => {
    onDirtyChange?.(isDirty);
  }, [isDirty, onDirtyChange]);
  useEffect(
    () => () => onDirtyChange?.(false), // unmount lifts the guard
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  );

  const submit = (e: FormEvent) => {
    e.preventDefault();
    // Save is busy, not disabled, while a save is in flight (WorkingButton), so
    // Enter in a field can still get here: a second submit is simply not sent.
    if (nameMissing || update.isPending) return;
    const submitted = values;
    update.mutate(
      { id: campaign.id, ...submitted },
      {
        onSuccess: () => {
          setSaved(submitted);
          setJustSaved(true);
        },
      },
    );
  };

  // Back to what is saved (the page host's way out of an edit it regrets).
  const discard = () => {
    setName(saved.name);
    setSystem(saved.system);
    setLanguage(saved.language);
    setTapeArmed(saved.tapeArmed);
    setHighlightBar(knobText(saved.highlightBar));
    setHighlightConfirm(knobText(saved.highlightConfirmWindows));
    update.reset();
    // The button that was just pressed is disabled again once nothing is
    // unsaved; a disabled button cannot keep focus, so hand it to the first
    // field instead of letting it fall to <body>.
    formRef.current?.querySelector<HTMLInputElement>("input")?.focus();
  };

  // Under the campaign header (an h1) on the page; under the dialog's title
  // (an h2) in a dialog.
  const Heading = variant === "page" ? "h2" : "h3";
  const generalId = `${uid}-general`;
  const highlightsId = `${uid}-highlights`;
  const systemListId = `${uid}-systems`;

  return (
    <form
      ref={formRef}
      className="gx-campaign-settings-form"
      data-variant={variant}
      onSubmit={submit}
      noValidate
    >
      <section className="gx-settings-section" aria-labelledby={generalId}>
        <Heading id={generalId} className="gx-settings-section__title">
          {t("campaigns.sectionGeneral")}
        </Heading>
        <Input
          label={t("components.nameLabel")}
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder={t("components.namePlaceholder")}
          error={nameMissing ? t("campaigns.nameRequired") : null}
          required
        />
        <div className="gx-campaign-create__row">
          <Input
            label={t("components.gameSystemLabel")}
            value={system}
            onChange={(e) => setSystem(e.target.value)}
            list={systemListId}
            hint={t("components.settingsSystemHint")}
          />
          <datalist id={systemListId}>
            {SYSTEM_SUGGESTIONS.map((s) => (
              <option key={s} value={s} />
            ))}
          </datalist>
          <SpokenLanguageSelect value={language} onValueChange={setLanguage} />
        </div>
      </section>

      <section className="gx-settings-section" aria-labelledby={highlightsId}>
        <Heading id={highlightsId} className="gx-settings-section__title">
          {t("campaigns.sectionHighlights")}
        </Heading>
        <p className="gx-settings-section__lede">{t("campaigns.highlightsLede")}</p>
        {/* Highlight recording (the rollover tape, #412/ADR-0051). The consent
            semantics (Discord Consent/Revoke buttons, no GM auto-consent) live
            in internal/wirenpc/tapedisclosure.go; the hint keeps only what a GM
            must know up front. */}
        <div className="gx-field">
          <Switch
            id={`${uid}-tape`}
            checked={tapeArmed}
            onCheckedChange={setTapeArmed}
            label={t("components.highlightRecordingLabel")}
          />
          <span className="gx-field__hint">{t("components.highlightRecordingHint")}</span>
        </div>
        {/* Detector tuning (#632 follow-up) sits beside the arming switch: the
            knobs only act while highlight recording is armed (no tape, no
            detector), so separating them would strand them from their gate. */}
        <div className="gx-campaign-create__row">
          <Input
            label={t("components.highlightBarLabel")}
            type="number"
            min="0"
            max="10"
            // step="any": the server accepts any double in [0,10], and a value
            // stored off-step by a non-web client would otherwise trip native
            // stepMismatch validation.
            step="any"
            inputMode="decimal"
            placeholder={t("components.highlightBarPlaceholder")}
            hint={t("components.highlightBarHint")}
            value={highlightBar}
            onChange={(e) => setHighlightBar(e.target.value)}
          />
          <Input
            label={t("components.highlightConfirmLabel")}
            type="number"
            min="0"
            max="10"
            step="1"
            inputMode="numeric"
            placeholder={t("components.highlightConfirmPlaceholder")}
            hint={t("components.highlightConfirmHint")}
            value={highlightConfirm}
            onChange={(e) => setHighlightConfirm(e.target.value)}
          />
        </div>
        <span className="gx-field__hint">{t("components.highlightTuningApplyHint")}</span>
      </section>

      {/* The save row. Sticky: on the page it stays at the bottom of the
          screen while the form is in view, in a dialog at the bottom of the
          scrolling body — Save is never below the fold. */}
      <div className="gx-settings-savebar">
        <div className="gx-settings-savebar__status">
          {/* One polite live region for the two quiet states, so "Saved" is
              heard without stealing focus. It stays beside a save error: the
              edits are still unsaved, and the error alone does not say so. */}
          <span className="gx-settings-savebar__state" role="status" data-dirty={isDirty || undefined}>
            {isDirty ? t("common.unsavedChanges") : justSaved && !update.error ? t("common.saved") : ""}
          </span>
          {update.error && (
            <span className="gx-campaign-create__error" role="alert">
              {t("common.couldntSave", { message: errorMessage(update.error) })}
            </span>
          )}
        </div>
        <div className="gx-settings-savebar__actions">
          {variant === "page" ? (
            <Button type="button" variant="ghost" onClick={discard} disabled={!isDirty || update.isPending}>
              {t("common.discardChangesConfirm")}
            </Button>
          ) : (
            onCancel && (
              <Button type="button" variant="ghost" onClick={onCancel}>
                {t("common.cancel")}
              </Button>
            )
          )}
          <WorkingButton type="submit" variant="primary" disabled={nameMissing} working={update.isPending}>
            {update.isPending ? t("common.saving") : t("common.saveChanges")}
          </WorkingButton>
        </div>
      </div>
    </form>
  );
}
