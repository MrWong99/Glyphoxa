import {
  createContext,
  useContext,
  useEffect,
  useMemo,
  useReducer,
  useState,
  type ReactNode,
} from "react";

import * as auth from "./messages/auth";
import * as campaign from "./messages/campaign";
import * as campaigns from "./messages/campaigns";
import * as cast from "./messages/cast";
import * as chat from "./messages/chat";
import * as common from "./messages/common";
import * as components from "./messages/components";
import * as configuration from "./messages/configuration";
import * as home from "./messages/home";
import * as knowledge from "./messages/knowledge";
import * as maps from "./messages/maps";
import * as palette from "./messages/palette";
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
  ...campaigns.en,
  ...home.en,
  ...cast.en,
  ...maps.en,
  ...chat.en,
  ...knowledge.en,
  ...palette.en,
  ...session.en,
  ...auth.en,
} as const;

export type MessageKey = keyof typeof en;
export type Lang = "en" | "de";

type Catalog = Record<MessageKey, string>;

// English is always in the bundle: it is the key source of truth and the
// per-key fallback. Every other catalog is fetched the first time its language
// is used (./de.ts ships as its own chunk), so a visitor only downloads the
// language they read. Each fragment's `de` is compile-checked against its own
// `en`, so the assembled map satisfies Catalog by construction.
const catalogs: Partial<Record<Lang, Catalog>> = { en };

const loaders: Record<Exclude<Lang, "en">, () => Promise<Catalog>> = {
  de: () => import("./de").then((m) => m.de as Catalog),
};

const loading = new Map<Lang, Promise<void>>();

// loadCatalog makes a language's strings available; it resolves at once for
// one that is already in memory and shares one request between concurrent
// callers. A failed fetch (offline, a chunk a deploy replaced) is forgotten so
// the next call retries; until then tFor falls back to English per key.
export function loadCatalog(lang: Lang): Promise<void> {
  if (lang === "en" || catalogs[lang]) return Promise.resolve();
  let pending = loading.get(lang);
  if (!pending) {
    pending = loaders[lang]()
      .then((catalog) => {
        catalogs[lang] = catalog;
      })
      .finally(() => loading.delete(lang));
    loading.set(lang, pending);
  }
  return pending;
}

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
  const catalog = catalogs[lang] ?? en;
  return (key, params) => format(catalog[key] ?? en[key], params);
}

const STORAGE_KEY = "gx-lang";

function isLang(v: unknown): v is Lang {
  return v === "en" || v === "de";
}

// Stored choice wins; otherwise the browser's language picks the initial
// display language (German browsers land in German without touching the
// picker). jsdom reports "en-US", so unit tests render English by default.
// Exported so main.tsx can fetch that language's catalog BEFORE the first
// render — a German visitor never sees an English frame flash by.
export function detectLang(): Lang {
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
  // Steps when a catalog finishes loading, so `t` is rebuilt against it.
  const [catalogEpoch, catalogArrived] = useReducer((n: number) => n + 1, 0);

  // The safety net behind main.tsx's pre-render load (and the only path in a
  // tree mounted without it): if the active language's catalog is not in
  // memory yet, fetch it and re-render when it lands.
  useEffect(() => {
    if (catalogs[lang]) return;
    let live = true;
    loadCatalog(lang).then(
      () => live && catalogArrived(),
      () => {}, // stays on the English fallback; the next language change retries
    );
    return () => {
      live = false;
    };
  }, [lang]);

  useEffect(() => {
    // Keep the document's lang attribute honest for screen readers and
    // browser translation prompts.
    document.documentElement.lang = lang;
  }, [lang]);

  const value = useMemo<I18n>(
    () => ({
      lang,
      setLang: (next: Lang) => {
        try {
          localStorage.setItem(STORAGE_KEY, next);
        } catch {
          // storage unavailable — the choice still applies for this visit
        }
        // Switch once the strings are here, so the screen goes from one
        // language straight to the other instead of through English. Already
        // in memory (the common case after the first switch) is synchronous.
        if (catalogs[next]) {
          setLangState(next);
          return;
        }
        const apply = () => setLangState(next);
        void loadCatalog(next).then(apply, apply);
      },
      t: tFor(lang),
    }),
    // catalogEpoch: tFor closes over the catalog that was in memory when it ran.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [lang, catalogEpoch],
  );

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>;
}

export function useI18n(): I18n {
  return useContext(I18nContext);
}
