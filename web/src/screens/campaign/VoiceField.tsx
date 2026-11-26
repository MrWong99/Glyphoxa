import { useId, useMemo, useState } from "react";
import { ListMusic, Square, Volume2 } from "lucide-react";

import { Button } from "@/components/ui/Button";
import { Combobox } from "@/components/ui/Combobox";
import { useI18n } from "@/i18n";
import { VoicePicker, type VoicesQuery } from "./VoicePicker";
import { useVoicePreview } from "./voicePreview";

import "./cast.css";

// VoiceField is the voice control of a form (the agent editor, the New NPC
// dialog): the Combobox as the collapsed display — it names the chosen voice
// and still lets a GM who knows the name type it — plus a quick Preview of the
// chosen voice and "Browse voices", which opens the picker where every voice
// can be heard before choosing.
//
// The catalog is LIVE ElevenLabs ListVoices data (#70); each option's value is
// the vendor voice id persisted on the agent and its label is "ElevenLabs ·
// Name". The agent's persisted voice id is kept as a bare option even when the
// catalog is empty or stale, so the current selection always shows.
//
// `pickerOnly` (the New NPC dialog) swaps the Combobox for a button that names
// the chosen voice and opens the picker. The Combobox's list opens in place
// under its trigger, and inside a dialog body — which scrolls — it would be
// clipped by that scroll box; the picker is a dialog of its own and is not.
export function VoiceField({
  label,
  voicesQuery,
  value,
  onChange,
  campaignLanguage,
  pickerOpen,
  onPickerOpenChange,
  pickerOnly = false,
}: {
  label: string;
  voicesQuery: VoicesQuery;
  value: string;
  onChange: (voiceId: string) => void;
  campaignLanguage?: string;
  /** Drive the picker from outside (the editor's "No voice" jump link). */
  pickerOpen?: boolean;
  onPickerOpenChange?: (open: boolean) => void;
  /** No in-place list: the field is a button that opens the picker. */
  pickerOnly?: boolean;
}) {
  const { t } = useI18n();
  const triggerId = useId();
  const preview = useVoicePreview();
  const [ownOpen, setOwnOpen] = useState(false);
  const open = pickerOpen ?? ownOpen;
  const setOpen = (next: boolean) => {
    // The picker has its own samples; one voice talks at a time.
    if (next) preview.stop();
    setOwnOpen(next);
    onPickerOpenChange?.(next);
  };

  const voices = voicesQuery.data?.voices;
  const options = useMemo(() => {
    const opts = (voices ?? []).map((v) => ({ value: v.voiceId, label: v.label || v.name || v.voiceId }));
    if (value && !opts.some((o) => o.value === value)) opts.unshift({ value, label: value });
    return opts;
  }, [voices, value]);

  const chosenLabel = value ? (options.find((o) => o.value === value)?.label ?? value) : null;
  const previewing = preview.activeId !== null;

  return (
    <div className="gx-cast-voice">
      <div className="gx-editor__voice">
        {pickerOnly ? (
          <div className="gx-select-field gx-combobox">
            <label className="gx-select-field__label" htmlFor={triggerId}>
              {label}
            </label>
            <button
              type="button"
              id={triggerId}
              className="gx-select gx-combobox__trigger"
              aria-haspopup="dialog"
              onClick={() => setOpen(true)}
            >
              <span className={chosenLabel ? "gx-combobox__value" : "gx-combobox__placeholder"}>
                {chosenLabel ?? t("campaign.voicePlaceholder")}
              </span>
              <ListMusic size={14} className="gx-select-chevron" aria-hidden="true" />
            </button>
          </div>
        ) : (
          <Combobox
            label={label}
            options={options}
            value={value || undefined}
            onValueChange={onChange}
            placeholder={t("campaign.voicePlaceholder")}
            searchPlaceholder={t("campaign.voiceSearch")}
            emptyText={t("campaign.voiceEmpty")}
          />
        )}
        {/* Preview failures — a degraded-TTS RPC rejection or a blocked play()
            — render an inline cue below instead of vanishing (#154). While a
            sample is loading or playing the same button stops it. */}
        <Button
          type="button"
          variant="secondary"
          size="sm"
          iconStart={previewing ? <Square size={14} /> : <Volume2 size={14} />}
          onClick={() => preview.toggle(value)}
          disabled={!value}
        >
          {previewing ? t("cast.voiceStop") : t("campaign.voicePreview")}
        </Button>
        {!pickerOnly && (
          <Button
            type="button"
            variant="secondary"
            size="sm"
            iconStart={<ListMusic size={14} />}
            onClick={() => setOpen(true)}
          >
            {t("cast.voiceBrowse")}
          </Button>
        )}
      </div>
      {preview.error && (
        <span className="gx-editor__status gx-editor__status--error" role="alert">
          {t("campaign.voicePreviewError", { message: preview.error })}
        </span>
      )}
      <VoicePicker
        open={open}
        onOpenChange={setOpen}
        voicesQuery={voicesQuery}
        value={value}
        onSelect={onChange}
        campaignLanguage={campaignLanguage}
      />
    </div>
  );
}
