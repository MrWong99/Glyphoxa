import { useState } from "react";
import type { FormEvent } from "react";
import { useQuery, useMutation } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import { CampaignService, type Campaign } from "@gen/glyphoxa/management/v1/management_pb";
import { Input } from "@/components/ui/Input";
import { Select } from "@/components/ui/Select";
import { Switch } from "@/components/ui/Switch";
import { Button } from "@/components/ui/Button";
import { AdvancedCard } from "@/components/ui/AdvancedCard";
import { invalidateActiveCampaignScopedQueries } from "@/lib/campaignCache";
import { invalidateMethodQueries } from "@/lib/queryClient";
import { useI18n, type Lang } from "@/i18n";

import "./createCampaignForm.css";
import { errorMessage } from "@/lib/connectError";

// The per-campaign settings editor (#268): edit an existing campaign's name,
// System, and Campaign Language, rendered in the topbar CampaignSwitcher's edit
// panel (#266). It mirrors CreateCampaignForm's plain-useState shape (ADR-0017),
// but the two curated fields diverge per the recorded product decisions:
//
//   System   — free text with datalist suggestions (no enum, rides the wire
//              opaque server-side, ADR-0039). The suggestions are a static
//              web-side convenience only.
//   Language — a SELECT constrained to the registered phonetic encoders
//              (ListSupportedLanguages → ADR-0024 EncoderRegistry, the sole
//              language-truth source), so a new encoder appears automatically and
//              no language list is hardcoded in the web tier.
//
// A language change mutates NOTHING now — existing Agents' voice settings are
// untouched (ADR-0009, #224); it takes effect on the next Voice Session, which
// the static hint under the select states.

const SYSTEM_SUGGESTIONS = ["D&D 5e", "Pathfinder 2e", "Call of Cthulhu"];
const SYSTEM_DATALIST_ID = "gx-system-suggestions";

// languageLabel renders a code as "<name in the display language> (<code>)" via
// Intl, falling back to the bare code when the runtime can't name it — so the
// option list stays readable without a hardcoded language table, and follows
// the operator's display language rather than always naming languages in
// English.
function languageLabel(lang: Lang, code: string): string {
  try {
    const name = new Intl.DisplayNames([lang], { type: "language" }).of(code);
    return name && name !== code ? `${name} (${code})` : code;
  } catch {
    return code;
  }
}

export function CampaignSettingsForm({
  campaign,
  onSaved,
  onCancel,
}: {
  campaign: Campaign;
  onSaved: () => void;
  onCancel: () => void;
}) {
  const { t, lang } = useI18n();
  const queryClient = useQueryClient();
  const [name, setName] = useState(campaign.name);
  const [system, setSystem] = useState(campaign.system);
  const [language, setLanguage] = useState(campaign.language);
  const [tapeArmed, setTapeArmed] = useState(campaign.tapeArmed);
  // Session-Highlights tuning (#632 follow-up), held as strings like the spend
  // caps: 0 on the wire means "engine default", rendered as an empty field with
  // the default in the placeholder — so clearing a field IS the reset gesture.
  const [highlightBar, setHighlightBar] = useState(
    campaign.highlightBar === 0 ? "" : String(campaign.highlightBar),
  );
  const [highlightConfirm, setHighlightConfirm] = useState(
    campaign.highlightConfirmWindows === 0 ? "" : String(campaign.highlightConfirmWindows),
  );

  // The Campaign Language choices come solely from the registered encoders.
  // retry:false so a failed load settles into the error hint at once rather than
  // backing off first — the registry is a cheap in-process read, so a failure is
  // a real signal to surface, not a transient to hammer through.
  const langQ = useQuery(CampaignService.method.listSupportedLanguages, {}, { retry: false });
  const supported = langQ.data?.languages ?? [];

  const options = supported.map((code) => ({ value: code, label: languageLabel(lang, code) }));
  // A stored language with no registered encoder still rides here as an extra
  // option so the SELECT can't silently coerce it to a supported one on save.
  // Only claim "(unsupported)" once the registry has actually LOADED — while the
  // query is pending or has failed, supported is empty for lack of an answer, not
  // because the stored language is unregistered, so mislabelling it would be a
  // lie (and a false claim on every transient). Until then the stored value rides
  // as a plain option so the SELECT still shows the current selection.
  if (campaign.language && !supported.includes(campaign.language)) {
    const registryKnows = langQ.isSuccess;
    options.push({
      value: campaign.language,
      label: registryKnows
        ? t("components.languageUnsupported", { label: languageLabel(lang, campaign.language) })
        : languageLabel(lang, campaign.language),
    });
  }

  // listCampaigns is campaign-INVARIANT (lib/campaignCache.ts), so the sweep
  // skips it — a name/system edit must invalidate it explicitly or the switcher's
  // picker keeps showing the stale name/system.
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

  const canSubmit = name.trim() !== "" && !update.isPending;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!canSubmit) return;
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
    update.mutate({
      id: campaign.id,
      name: name.trim(),
      system,
      language,
      tapeArmed,
      highlightBar: bar,
      highlightConfirmWindows: confirm,
    });
  };

  return (
    <form className="gx-campaign-create" onSubmit={submit}>
      <Input
        label={t("components.nameLabel")}
        value={name}
        onChange={(e) => setName(e.target.value)}
        placeholder={t("components.namePlaceholder")}
        disabled={update.isPending}
        required
      />
      <div className="gx-campaign-create__row">
        <Input
          label={t("components.gameSystemLabel")}
          value={system}
          onChange={(e) => setSystem(e.target.value)}
          list={SYSTEM_DATALIST_ID}
          hint={t("components.settingsSystemHint")}
          disabled={update.isPending}
        />
        <datalist id={SYSTEM_DATALIST_ID}>
          {SYSTEM_SUGGESTIONS.map((s) => (
            <option key={s} value={s} />
          ))}
        </datalist>
        <div className="gx-campaign-create__lang">
          {/* "Spoken language" — the language the group PLAYS in (drives STT/TTS),
              deliberately distinct from the web tier's display-language picker. */}
          <Select
            label={t("components.spokenLanguageLabel")}
            options={options}
            value={language}
            onValueChange={setLanguage}
            disabled={update.isPending}
          />
          <span className="gx-field__hint">{t("components.spokenLanguageHint")}</span>
          {langQ.isError && (
            <span className="gx-field__hint gx-field__hint--error" role="alert">
              {t("components.couldntLoadLanguages", { message: errorMessage(langQ.error) })}
            </span>
          )}
        </div>
      </div>
      {/* Highlight recording (the rollover tape, #412/ADR-0051) lives behind the
          advanced disclosure: consent-gated capture is a power-user opt-in most
          groups never touch, so it must not crowd the everyday name/system/
          language fields. */}
      <AdvancedCard>
        <div className="gx-field">
          <Switch
            id="gx-tape-armed"
            checked={tapeArmed}
            onCheckedChange={setTapeArmed}
            label={t("components.highlightRecordingLabel")}
            disabled={update.isPending}
          />
          {/* The consent semantics (Discord Consent/Revoke buttons, no GM
              auto-consent) live in internal/wirenpc/tapedisclosure.go; the hint
              keeps only what a GM must know up front. */}
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
            // stepMismatch validation and block the WHOLE form submit.
            step="any"
            inputMode="decimal"
            placeholder={t("components.highlightBarPlaceholder")}
            hint={t("components.highlightBarHint")}
            value={highlightBar}
            onChange={(e) => setHighlightBar(e.target.value)}
            disabled={update.isPending}
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
            disabled={update.isPending}
          />
        </div>
        <span className="gx-field__hint">{t("components.highlightTuningApplyHint")}</span>
      </AdvancedCard>
      <div className="gx-campaign-create__actions">
        <Button type="submit" variant="primary" disabled={!canSubmit}>
          {update.isPending ? t("common.saving") : t("common.saveChanges")}
        </Button>
        <Button type="button" variant="ghost" onClick={onCancel} disabled={update.isPending}>
          {t("common.cancel")}
        </Button>
        {update.error && (
          <span className="gx-campaign-create__error" role="alert">
            {t("common.couldntSave", { message: errorMessage(update.error) })}
          </span>
        )}
      </div>
    </form>
  );
}
