import { useQuery } from "@connectrpc/connect-query";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import { Select } from "@/components/ui/Select";
import { errorMessage } from "@/lib/connectError";
import { useI18n, type Lang } from "@/i18n";

// The campaign fields both campaign forms share (#268): the System suggestions
// and the "Spoken language" SELECT. The create form (CreateCampaignForm) and the
// edit form (CampaignSettingsForm) render the same control off the same data
// source, so a new campaign and an edited one can never disagree on what a
// valid Campaign Language is.

// System is free text (no enum, rides the wire opaque server-side, ADR-0039);
// the suggestions are a static web-side convenience only. The first entry is
// also the create form's default.
export const SYSTEM_SUGGESTIONS = ["D&D 5e", "Pathfinder 2e", "Call of Cthulhu"];

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

// useSupportedLanguages reads the Campaign Language choices, which come solely
// from the registered encoders (ListSupportedLanguages → ADR-0024
// EncoderRegistry, the sole language-truth source), so a new encoder appears
// automatically and no language list is hardcoded in the web tier.
// retry:false so a failed load settles into the error hint at once rather than
// backing off first — the registry is a cheap in-process read, so a failure is
// a real signal to surface, not a transient to hammer through.
export function useSupportedLanguages() {
  return useQuery(CampaignService.method.listSupportedLanguages, {}, { retry: false });
}

// SpokenLanguageSelect is the "Spoken language" SELECT — the language the group
// PLAYS in (drives STT/TTS), deliberately distinct from the web tier's
// display-language picker.
export function SpokenLanguageSelect({
  value,
  onValueChange,
  disabled = false,
  hint,
}: {
  value: string;
  onValueChange: (code: string) => void;
  disabled?: boolean;
  /** Overrides the default "applies from the next voice session" hint. */
  hint?: string;
}) {
  const { t, lang } = useI18n();
  const langQ = useSupportedLanguages();
  const supported = langQ.data?.languages ?? [];

  const options = supported.map((code) => ({ value: code, label: languageLabel(lang, code) }));
  // A value with no registered encoder still rides here as an extra option so
  // the SELECT can't silently coerce it to a supported one on save. Only claim
  // "(unsupported)" once the registry has actually LOADED — while the query is
  // pending or has failed, supported is empty for lack of an answer, not
  // because the value is unregistered, so mislabelling it would be a lie (and
  // a false claim on every transient). Until then the value rides as a plain
  // option so the SELECT still shows the current selection.
  if (value && !supported.includes(value)) {
    options.push({
      value,
      label: langQ.isSuccess
        ? t("components.languageUnsupported", { label: languageLabel(lang, value) })
        : languageLabel(lang, value),
    });
  }

  return (
    <div className="gx-campaign-create__lang">
      <Select
        label={t("components.spokenLanguageLabel")}
        options={options}
        value={value}
        onValueChange={onValueChange}
        disabled={disabled}
      />
      <span className="gx-field__hint">{hint ?? t("components.spokenLanguageHint")}</span>
      {langQ.isError && (
        <span className="gx-field__hint gx-field__hint--error" role="alert">
          {t("components.couldntLoadLanguages", { message: errorMessage(langQ.error) })}
        </span>
      )}
    </div>
  );
}
