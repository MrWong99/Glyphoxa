import { Select } from "./ui/Select";
import { LANGS, LANG_NAMES, useI18n, type Lang } from "@/i18n";

// The display-language picker. Each language is listed in ITSELF (English /
// Deutsch) so a user stuck in a language they can't read can still switch
// back. The choice is a web-tier preference (localStorage) — it never touches
// the Campaign Language, which drives STT/TTS.

export function LanguageSwitcher({ className }: { className?: string }) {
  const { lang, setLang, t } = useI18n();
  return (
    <div className={className}>
      <Select
        aria-label={t("common.displayLanguage")}
        options={LANGS.map((l) => ({ value: l, label: LANG_NAMES[l] }))}
        value={lang}
        onValueChange={(v) => setLang(v as Lang)}
      />
    </div>
  );
}
