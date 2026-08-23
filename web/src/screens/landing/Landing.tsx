import { Dices, Mic, ScrollText, Sparkles, KeyRound } from "lucide-react";

import { LegalFooter } from "@/components/LegalFooter";
import { LanguageSwitcher } from "@/components/LanguageSwitcher";
import { useI18n, type MessageKey } from "@/i18n";

import "./landing.css";

// The public landing surface (#521). Before this, an unauthenticated visitor hit
// a bare login form — which converts nobody and explains nothing. This screen is
// what the root serves them instead: what Glyphoxa is, what the free beta
// actually gives you, and a CTA into the unchanged Discord OAuth flow (via
// /login, so the open-mode AUP acknowledgment from #518 still gates signup).
//
// COPY RULE (#258): everything here describes what ships today. No roadmap
// features in the present tense, no invented numbers. The trial allowance is
// described qualitatively ("a small monthly allowance") because it is an
// operator-adjustable chart value (#521's plan catalog) — quoting a figure here
// would make the page lie the moment an operator tunes it.
//
// The SPA ships identically to every operator, but the beta-economics copy is
// only true on the launch posture (open Admission Mode + the beta plan
// catalog). RootEntry probes the public GetAdmissionMode (the same posture
// probe Login uses) and passes `open`: only an explicit OPEN answer renders
// the free-beta section and signup framing; the fail-safe default (allowlist,
// probe pending or errored) is a neutral product page with a plain sign-in
// CTA, so a stock self-host install never advertises a signup it rejects or
// an allowance it does not fund.
//
// The screenshots (#263) are real captures of a staged demo campaign ("Tides
// over Saltmarsh") on a stock local instance — nothing in them is mocked up in
// an image editor. Sources live in web/public/shots/ as 1600x1000 WebP; vite
// copies them into the SPA dist verbatim, so they ship inside the binary.
//
// Copy lives in the i18n catalog (auth.* fragment); this module stores only
// MessageKeys, never translated strings, so the display language can change
// after mount without stale module-level state.

type Feature = { icon: typeof Mic; titleKey: MessageKey; bodyKey: MessageKey };

const FEATURES: Feature[] = [
  { icon: Mic, titleKey: "auth.featureVoiceTitle", bodyKey: "auth.featureVoiceBody" },
  { icon: ScrollText, titleKey: "auth.featureTranscriptTitle", bodyKey: "auth.featureTranscriptBody" },
  { icon: Sparkles, titleKey: "auth.featureGmTitle", bodyKey: "auth.featureGmBody" },
  { icon: KeyRound, titleKey: "auth.featureKeysTitle", bodyKey: "auth.featureKeysBody" },
];

// Screenshot slots. A null src falls back to the quiet placeholder frame, so a
// missing capture degrades gracefully instead of breaking the grid.
type Shot = { captionKey: MessageKey; src: string | null };

const SHOTS: Shot[] = [
  { captionKey: "auth.shotSessionCaption", src: "/shots/session-live.webp" },
  { captionKey: "auth.shotHighlightsCaption", src: "/shots/session-highlights.webp" },
  { captionKey: "auth.shotSearchCaption", src: "/shots/session-search.webp" },
  { captionKey: "auth.shotPersonaCaption", src: "/shots/cast-persona.webp" },
  { captionKey: "auth.shotWikiCaption", src: "/shots/wiki-graph.webp" },
  { captionKey: "auth.shotMapsCaption", src: "/shots/maps.webp" },
  { captionKey: "auth.shotPlanningCaption", src: "/shots/planning.webp" },
  { captionKey: "auth.shotKeysCaption", src: "/shots/setup-keys.webp" },
];

export function Landing({ open = false }: { open?: boolean }) {
  const { t } = useI18n();
  return (
    <div className="gx-landing">
      <header className="gx-landing__bar">
        <span className="gx-landing__brand">
          <span className="gx-landing__sigil">
            <Dices size={18} />
          </span>
          <span className="gx-gradient-text">Glyphoxa</span>
        </span>
        {/* The language picker rides in the header next to the sign-in CTA so a
            German visitor can switch BEFORE any session exists. Inline flex
            because the bar's stylesheet only lays out its two edge slots. */}
        <span style={{ display: "inline-flex", alignItems: "center", gap: 10 }}>
          <LanguageSwitcher />
          <a className="gx-btn gx-btn--secondary" href="/login">
            {t("auth.landingSignIn")}
          </a>
        </span>
      </header>

      <section className="gx-landing__hero">
        <h1 className="gx-landing__headline">
          {t("auth.landingHeadlinePre")}
          <span className="gx-gradient-text">{t("auth.landingHeadlineAccent")}</span>
          {t("auth.landingHeadlinePost")}
        </h1>
        <p className="gx-landing__lede">{t("auth.landingLede")}</p>
        <div className="gx-landing__cta">
          <a className="gx-btn gx-btn--primary gx-btn--lg" href="/login">
            {open ? t("auth.landingCtaOpen") : t("auth.landingCtaSignIn")}
          </a>
          <span className="gx-landing__cta-note">
            {open ? t("auth.landingCtaNoteOpen") : t("auth.landingCtaNote")}
          </span>
        </div>
      </section>

      <section className="gx-landing__features" aria-label={t("auth.landingFeaturesAria")}>
        {FEATURES.map(({ icon: Icon, titleKey, bodyKey }) => (
          <article key={titleKey} className="gx-landing__feature">
            <span className="gx-landing__feature-icon">
              <Icon size={18} />
            </span>
            <h2>{t(titleKey)}</h2>
            <p>{t(bodyKey)}</p>
          </article>
        ))}
      </section>

      <section className="gx-landing__shots" aria-label={t("auth.landingShotsAria")}>
        {SHOTS.map((shot) => (
          <figure key={shot.captionKey} className="gx-landing__shot">
            {shot.src ? (
              /* The grid tile is a small window on a dense app screen; the link
                 opens the full 1600px capture in a new tab for a real look. */
              <a href={shot.src} target="_blank" rel="noreferrer">
                <img src={shot.src} alt={t(shot.captionKey)} loading="lazy" />
              </a>
            ) : (
              <div className="gx-landing__shot-placeholder" role="presentation" />
            )}
            <figcaption>{t(shot.captionKey)}</figcaption>
          </figure>
        ))}
      </section>

      {open && (
        <section className="gx-landing__beta" aria-labelledby="beta-heading">
          <h2 id="beta-heading">{t("auth.betaHeading")}</h2>
          <div className="gx-landing__plans">
            <article className="gx-landing__plan">
              <h3>{t("auth.betaTrialTitle")}</h3>
              <p>{t("auth.betaTrialBody")}</p>
            </article>
            <article className="gx-landing__plan">
              <h3>{t("auth.betaByokTitle")}</h3>
              <p>{t("auth.betaByokBody")}</p>
            </article>
          </div>
          <p className="gx-landing__beta-note">{t("auth.betaNote")}</p>
        </section>
      )}

      {/* Split around the link: the Datenschutzerklärung's German proper name
          stays verbatim in both display languages (#518). */}
      <p className="gx-landing__beta-note">
        {t("auth.transcribedNotePre")}
        <a href="/privacy">Datenschutzerklärung</a>
        {t("auth.transcribedNotePost")}
      </p>

      <LegalFooter />
    </div>
  );
}
