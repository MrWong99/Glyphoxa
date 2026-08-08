import {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";

import * as auth from "./messages/auth";
import * as campaign from "./messages/campaign";
import * as common from "./messages/common";
import * as components from "./messages/components";
import * as configuration from "./messages/configuration";
import * as knowledge from "./messages/knowledge";
import * as session from "./messages/session";
import * as shell from "./messages/shell";

// The web tier's display-language layer. Deliberately dependency-free: the
// catalog is a flat, typed key → string map assembled from the per-area
// fragments under messages/, so `t("nope")` and a German string missing its
// English key are both COMPILE errors, not runtime fallbacks. The display
// language is a pure web-tier preference (localStorage) — it is NOT the
// Campaign Language, which lives on the campaign record and drives STT/TTS.

export const en = {
  ...common.en,
  ...shell.en,
  ...components.en,
  ...configuration.en,
  ...campaign.en,
  ...knowledge.en,
  ...session.en,
  ...auth.en,
} as const;

export type MessageKey = keyof typeof en;
export type Lang = "en" | "de";

// Each fragment's `de` is already compile-checked against its own `en`; this
// merged map then satisfies Record<MessageKey, string> by construction.
const de: Record<MessageKey, string> = {
  ...common.de,
  ...shell.de,
  ...components.de,
  ...configuration.de,
  ...campaign.de,
  ...knowledge.de,
  ...session.de,
  ...auth.de,
};

const catalogs: Record<Lang, Record<MessageKey, string>> = { en, de };

export const LANGS: readonly Lang[] = ["en", "de"];

// The picker shows each language in ITSELF (never translated), so a user stuck
// in a language they can't read can still find their own.
export const LANG_NAMES: Record<Lang, string> = { en: "English", de: "Deutsch" };

export type MsgParams = Record<string, string | number>;
export type TFunc = (key: MessageKey, params?: MsgParams) => string;

function format(template: string, params?: MsgParams): string {
  if (!params) return template;
  return template.replace(/\{(\w+)\}/g, (match, name: string) =>
    name in params ? String(params[name]) : match,
  );
}

function tFor(lang: Lang): TFunc {
  const catalog = catalogs[lang];
  return (key, params) => format(catalog[key] ?? en[key], params);
}

const STORAGE_KEY = "gx-lang";

function isLang(v: unknown): v is Lang {
  return v === "en" || v === "de";
}

// Stored choice wins; otherwise the browser's language picks the initial
// display language (German browsers land in German without touching the
// picker). jsdom reports "en-US", so unit tests render English by default.
function detectLang(): Lang {
  try {
    const stored = localStorage.getItem(STORAGE_KEY);
    if (isLang(stored)) return stored;
  } catch {
    // storage unavailable (privacy mode) — fall through to the browser hint
  }
  if (typeof navigator !== "undefined" && navigator.language?.toLowerCase().startsWith("de")) {
    return "de";
  }
  return "en";
}

type I18n = { lang: Lang; setLang: (lang: Lang) => void; t: TFunc };

// The default context value is a WORKING English i18n (not null): unit tests
// that mount a single screen without the provider still render real copy, and
// production always mounts the provider so setLang is only a no-op in tests.
const I18nContext = createContext<I18n>({ lang: "en", setLang: () => {}, t: tFor("en") });

export function I18nProvider({ children }: { children: ReactNode }) {
  const [lang, setLangState] = useState<Lang>(detectLang);

  useEffect(() => {
    // Keep the document's lang attribute honest for screen readers and
    // browser translation prompts.
    document.documentElement.lang = lang;
  }, [lang]);

  const value = useMemo<I18n>(
    () => ({
      lang,
      setLang: (next: Lang) => {
        setLangState(next);
        try {
          localStorage.setItem(STORAGE_KEY, next);
        } catch {
          // storage unavailable — the choice still applies for this visit
        }
      },
      t: tFor(lang),
    }),
    [lang],
  );

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18n(): I18n {
  return useContext(I18nContext);
}
