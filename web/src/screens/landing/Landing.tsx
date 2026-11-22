import { useEffect, useRef, useState, type ReactNode } from "react";
import { ArrowRight, Check, Dices, ShieldCheck, X } from "lucide-react";

import { LegalFooter } from "@/components/LegalFooter";
import { LanguageSwitcher } from "@/components/LanguageSwitcher";
import { IconButton } from "@/components/ui/IconButton";
import { useI18n, type MessageKey } from "@/i18n";
import { useDocumentTitle } from "@/lib/useDocumentTitle";

import "./landing.css";

// The public landing surface (#521): what the root serves a visitor with no
// session. It has to answer, in this order, "what is this?", "what does it
// look like?", "what would I have to do?" and "what does it cost me?" — and
// then ask for the sign-in. So the page is:
//
//   a sticky bar      brand, in-page links, language, Sign in
//   the hero          headline and lede beside the real live-session capture
//   the story         five rows, each a moment of running a campaign, each
//                     with the screen that does it (At the table, After the
//                     session, Your world, Prep, Your keys)
//   How it works      five numbered steps, worded like the app itself
//   the free beta     two plan cards — on an OPEN deployment only
//   Privacy           the transcription honesty note
//   a closing CTA     and a footer
//
// COPY RULE (#258): everything here describes what ships today. No roadmap
// features in the present tense, no invented numbers. The trial allowance is
// described qualitatively ("a small monthly allowance") because it is an
// operator-adjustable chart value (#521's plan catalog) — quoting a figure
// here would make the page lie the moment an operator tunes it.
//
// The SPA ships identically to every operator, but the beta-economics copy is
// only true on the launch posture (open Admission Mode + the beta plan
// catalog). RootEntry probes the public GetAdmissionMode (the same posture
// probe Login uses) and passes `open`: only an explicit OPEN answer renders
// the free-beta section, the signup framing and the hosted lede; the fail-safe
// default (allowlist, probe pending or errored) is a neutral product page with
// a plain sign-in CTA and the self-hosted lede, so a stock self-host install
// never advertises a signup it rejects or an allowance it does not fund.
//
// The CTAs go to /login, never straight to the OAuth start: the login screen
// is where the open-mode AUP acknowledgment (#518) gates it.
//
// The screenshots (#263) are real captures of a staged demo campaign ("Tides
// over Saltmarsh") on a stock local instance — nothing in them is mocked up in
// an image editor. Sources live in web/public/shots/ as 1600x1000 WebP; vite
// copies them into the SPA dist verbatim, so they ship inside the binary.
//
// Copy lives in the i18n catalog (auth.* fragment); this module stores only
// MessageKeys, never translated strings, so the display language can change
// after mount without stale module-level state.

type Shot = { src: string; captionKey: MessageKey };

// Every capture is 1600x1000; stating it lets the browser reserve the box
// before the image arrives, so nothing below jumps.
const SHOT_WIDTH = 1600;
const SHOT_HEIGHT = 1000;

const HERO_SHOT: Shot = { src: "/shots/session-live.webp", captionKey: "auth.shotSessionCaption" };

type Story = {
  id: string;
  overlineKey: MessageKey;
  titleKey: MessageKey;
  bodyKey: MessageKey;
  pointKeys?: MessageKey[];
  shots: Shot[];
};

const STORIES: Story[] = [
  {
    id: "at-the-table",
    overlineKey: "auth.storyTableOverline",
    titleKey: "auth.storyTableTitle",
    bodyKey: "auth.storyTableBody",
    pointKeys: ["auth.storyTablePoint1", "auth.storyTablePoint2", "auth.storyTablePoint3"],
    shots: [{ src: "/shots/cast-persona.webp", captionKey: "auth.shotPersonaCaption" }],
  },
  {
    id: "after-the-session",
    overlineKey: "auth.storyAfterOverline",
    titleKey: "auth.storyAfterTitle",
    bodyKey: "auth.storyAfterBody",
    shots: [
      { src: "/shots/session-highlights.webp", captionKey: "auth.shotHighlightsCaption" },
      { src: "/shots/session-search.webp", captionKey: "auth.shotSearchCaption" },
    ],
  },
  {
    id: "your-world",
    overlineKey: "auth.storyWorldOverline",
    titleKey: "auth.storyWorldTitle",
    bodyKey: "auth.storyWorldBody",
    shots: [
      { src: "/shots/wiki-graph.webp", captionKey: "auth.shotWikiCaption" },
      { src: "/shots/maps.webp", captionKey: "auth.shotMapsCaption" },
    ],
  },
  {
    id: "prep",
    overlineKey: "auth.storyPrepOverline",
    titleKey: "auth.storyPrepTitle",
    bodyKey: "auth.storyPrepBody",
    shots: [{ src: "/shots/planning.webp", captionKey: "auth.shotPlanningCaption" }],
  },
  {
    id: "your-keys",
    overlineKey: "auth.storyKeysOverline",
    titleKey: "auth.storyKeysTitle",
    bodyKey: "auth.storyKeysBody",
    shots: [{ src: "/shots/setup-keys.webp", captionKey: "auth.shotKeysCaption" }],
  },
];

// The steps of "How it works". A step may say one more thing on an open
// deployment (the hosted beta), where signing up founds a table and the trial
// allowance stands in for keys at first.
type Step = { titleKey: MessageKey; bodyKey: MessageKey; openBodyKey?: MessageKey; openReplaces?: boolean };

const STEPS: Step[] = [
  {
    titleKey: "auth.howStep1Title",
    bodyKey: "auth.howStep1Body",
    openBodyKey: "auth.howStep1BodyOpen",
    openReplaces: true,
  },
  { titleKey: "auth.howStep2Title", bodyKey: "auth.howStep2Body" },
  { titleKey: "auth.howStep3Title", bodyKey: "auth.howStep3Body", openBodyKey: "auth.howStep3BodyOpen" },
  { titleKey: "auth.howStep4Title", bodyKey: "auth.howStep4Body" },
  { titleKey: "auth.howStep5Title", bodyKey: "auth.howStep5Body" },
];

const SOURCE_URL = "https://github.com/MrWong99/Glyphoxa";

export function Landing({ open = false }: { open?: boolean }) {
  const { t } = useI18n();
  useDocumentTitle(t("auth.landingDocTitle"));

  // The capture shown large, or null. One lightbox for the whole page.
  const [enlarged, setEnlarged] = useState<Shot | null>(null);

  const cta = open ? t("auth.landingCtaOpen") : t("auth.landingCtaSignIn");
  const ctaNote = open ? t("auth.landingCtaNoteOpen") : t("auth.landingCtaNote");

  return (
    <div className="gx-landing" id="top">
      <header className="gx-landing__bar">
        <div className="gx-landing__wrap gx-landing__bar-row">
          <a className="gx-landing__brand" href="#top" aria-label={t("auth.landingHome")}>
            <span className="gx-landing__sigil">
              <Dices size={18} aria-hidden="true" />
            </span>
            <span className="gx-landing__wordmark gx-gradient-text">Glyphoxa</span>
          </a>
          <nav className="gx-landing__nav" aria-label={t("auth.landingNavAria")}>
            <a href="#story">{t("auth.landingNavStory")}</a>
            <a href="#how">{t("auth.landingNavHow")}</a>
            {open && <a href="#beta">{t("auth.landingNavBeta")}</a>}
            <a href="#privacy">{t("auth.landingNavPrivacy")}</a>
          </nav>
          {/* The language picker rides in the bar next to the sign-in CTA so
              a German visitor can switch BEFORE any session exists. */}
          <LanguageSwitcher className="gx-landing__lang" />
          <a className="gx-btn gx-btn--secondary gx-btn--sm" href="/login">
            {t("auth.landingSignIn")}
          </a>
        </div>
      </header>

      <main>
        <section className="gx-landing__hero">
          <div className="gx-landing__wrap gx-landing__hero-grid">
            <div className="gx-landing__hero-text">
              <h1 className="gx-landing__headline">
                {t("auth.landingHeadlinePre")}
                <span className="gx-gradient-text">{t("auth.landingHeadlineAccent")}</span>
                {t("auth.landingHeadlinePost")}
              </h1>
              <p className="gx-landing__lede">
                {open ? t("auth.landingLedeHosted") : t("auth.landingLedeSelfHosted")}
              </p>
              <div className="gx-landing__cta">
                <a className="gx-btn gx-btn--primary gx-btn--lg" href="/login">
                  {cta}
                </a>
                <a className="gx-landing__cta-how" href="#how">
                  {t("auth.landingCtaHow")}
                  <ArrowRight size={16} aria-hidden="true" />
                </a>
              </div>
              <p className="gx-landing__cta-note">{ctaNote}</p>
            </div>
            {/* The product itself, on the first screen: the real live-session
                capture, not an illustration. */}
            <ShotFigure
              shot={HERO_SHOT}
              className="gx-landing__hero-shot"
              eager
              onEnlarge={setEnlarged}
            />
          </div>
        </section>

        <section className="gx-landing__section" id="story" aria-labelledby="story-heading">
          <div className="gx-landing__wrap">
            <h2 className="gx-landing__section-title" id="story-heading">
              {t("auth.storyHeading")}
            </h2>
            <div className="gx-landing__stories">
              {STORIES.map((story) => (
                <article key={story.id} className="gx-landing__story" id={story.id}>
                  <div className="gx-landing__story-text">
                    <span className="gx-overline">{t(story.overlineKey)}</span>
                    <h3>{t(story.titleKey)}</h3>
                    <p>{t(story.bodyKey)}</p>
                    {story.pointKeys && (
                      <ul className="gx-landing__points">
                        {story.pointKeys.map((key) => (
                          <li key={key}>
                            <Check size={16} aria-hidden="true" />
                            <span>{t(key)}</span>
                          </li>
                        ))}
                      </ul>
                    )}
                  </div>
                  <div className="gx-landing__story-shots" data-count={story.shots.length}>
                    {story.shots.map((shot) => (
                      <ShotFigure key={shot.src} shot={shot} onEnlarge={setEnlarged} />
                    ))}
                  </div>
                </article>
              ))}
            </div>
          </div>
        </section>

        <section className="gx-landing__section gx-landing__section--tinted" id="how" aria-labelledby="how-heading">
          <div className="gx-landing__wrap">
            <h2 className="gx-landing__section-title" id="how-heading">
              {t("auth.howHeading")}
            </h2>
            <p className="gx-landing__section-lede">{t("auth.howLede")}</p>
            <ol className="gx-landing__steps">
              {STEPS.map((step, i) => (
                <li key={step.titleKey} className="gx-landing__step">
                  <span className="gx-landing__step-number" aria-hidden="true">
                    {i + 1}
                  </span>
                  <h3>{t(step.titleKey)}</h3>
                  {!(open && step.openReplaces && step.openBodyKey) && <p>{t(step.bodyKey)}</p>}
                  {open && step.openBodyKey && <p>{t(step.openBodyKey)}</p>}
                </li>
              ))}
            </ol>
          </div>
        </section>

        {open && (
          <section className="gx-landing__section" id="beta" aria-labelledby="beta-heading">
            <div className="gx-landing__wrap">
              <h2 className="gx-landing__section-title" id="beta-heading">
                {t("auth.betaHeading")}
              </h2>
              <div className="gx-landing__plans">
                <article className="gx-landing__plan">
                  <h3>{t("auth.betaTrialTitle")}</h3>
                  <p>{t("auth.betaTrialLede")}</p>
                  <PlanPoints>
                    <li>{t("auth.betaTrialPoint1")}</li>
                    <li>{t("auth.betaTrialPoint2")}</li>
                    {/* The allowance gate is plan-based: adding keys alone does
                        not unpause an exhausted trial, the operator moves the
                        table to the bring-your-own-keys plan. So the copy
                        names the person to ask, and links their contact. */}
                    <li>
                      {t("auth.betaTrialPoint3Pre")}
                      <a href="/imprint">{t("auth.betaAskOperator")}</a>
                      {t("auth.betaTrialPoint3Post")}
                    </li>
                  </PlanPoints>
                </article>
                <article className="gx-landing__plan">
                  <h3>{t("auth.betaByokTitle")}</h3>
                  <p>{t("auth.betaByokLede")}</p>
                  <PlanPoints>
                    <li>{t("auth.betaByokPoint1")}</li>
                    <li>{t("auth.betaByokPoint2")}</li>
                    <li>
                      {t("auth.betaByokPoint3Pre")}
                      <a href="/imprint">{t("auth.betaAskOperator")}</a>
                      {t("auth.betaByokPoint3Post")}
                    </li>
                  </PlanPoints>
                </article>
              </div>
              <p className="gx-landing__beta-note">{t("auth.betaNote")}</p>
            </div>
          </section>
        )}

        <section className="gx-landing__section" id="privacy" aria-labelledby="privacy-heading">
          <div className="gx-landing__wrap">
            <div className="gx-landing__callout">
              <span className="gx-landing__callout-icon">
                <ShieldCheck size={20} aria-hidden="true" />
              </span>
              <div>
                <h2 id="privacy-heading">{t("auth.privacyHeading")}</h2>
                {/* Split around the link: the Datenschutzerklärung's German
                    proper name stays verbatim in both display languages (#518). */}
                <p>
                  {t("auth.transcribedNotePre")}
                  <a href="/privacy">Datenschutzerklärung</a>
                  {t("auth.transcribedNotePost")}
                </p>
              </div>
            </div>
          </div>
        </section>

        <section className="gx-landing__section" aria-labelledby="closing-heading">
          <div className="gx-landing__wrap">
            <div className="gx-landing__closing">
              <h2 id="closing-heading">{t("auth.closingHeading")}</h2>
              <p>{t("auth.closingBody")}</p>
              <a className="gx-btn gx-btn--primary gx-btn--lg" href="/login">
                {cta}
              </a>
              <span className="gx-landing__cta-note">{ctaNote}</span>
            </div>
          </div>
        </section>
      </main>

      <footer className="gx-landing__footer">
        <div className="gx-landing__wrap gx-landing__footer-row">
          <div className="gx-landing__footer-brand">
            <span className="gx-landing__wordmark gx-gradient-text">Glyphoxa</span>
            <p>{t("auth.footerTagline")}</p>
          </div>
          <nav className="gx-landing__footer-nav" aria-label={t("auth.footerNavAria")}>
            <a href="#how">{t("auth.landingNavHow")}</a>
            <a href="/login">{t("auth.landingSignIn")}</a>
            <a href={SOURCE_URL} target="_blank" rel="noreferrer noopener">
              {t("auth.footerSource")}
            </a>
          </nav>
        </div>
        <div className="gx-landing__wrap">
          <p className="gx-landing__footer-note">{t("auth.footerOpenSource")}</p>
          <LegalFooter as="div" />
        </div>
      </footer>

      <Lightbox shot={enlarged} onClose={() => setEnlarged(null)} />
    </div>
  );
}

// PlanPoints is a plan card's list: each line led by a check mark.
function PlanPoints({ children }: { children: ReactNode }) {
  return <ul className="gx-landing__points gx-landing__points--plan">{children}</ul>;
}

// ShotFigure is one capture with its caption. The image is a button: a tile
// is a small window on a dense app screen, and the lightbox shows it at full
// size without leaving the page. (It used to be a link to the bare image file
// in a new tab.)
function ShotFigure({
  shot,
  className = "",
  eager = false,
  onEnlarge,
}: {
  shot: Shot;
  className?: string;
  eager?: boolean;
  onEnlarge: (shot: Shot) => void;
}) {
  const { t } = useI18n();
  const caption = t(shot.captionKey);
  return (
    <figure className={className ? `gx-landing__shot ${className}` : "gx-landing__shot"}>
      <button
        type="button"
        className="gx-landing__shot-button"
        aria-label={t("auth.shotEnlarge", { caption })}
        aria-haspopup="dialog"
        onClick={() => onEnlarge(shot)}
      >
        <img
          src={shot.src}
          alt={caption}
          width={SHOT_WIDTH}
          height={SHOT_HEIGHT}
          loading={eager ? "eager" : "lazy"}
          decoding="async"
        />
      </button>
      <figcaption>{caption}</figcaption>
    </figure>
  );
}

// Lightbox shows one capture large, in a native <dialog>. showModal() gives it
// what a modal needs without a library: the page behind is inert, focus moves
// in, Escape closes it, and focus goes back to the tile that opened it. A
// click on the backdrop closes it too.
//
// The <dialog> stays mounted (closed) so the same element can be reopened;
// only its content follows `shot`.
function Lightbox({ shot, onClose }: { shot: Shot | null; onClose: () => void }) {
  const { t } = useI18n();
  const ref = useRef<HTMLDialogElement>(null);

  useEffect(() => {
    const dialog = ref.current;
    if (!dialog) return;
    if (shot && !dialog.open) dialog.showModal();
    else if (!shot && dialog.open) dialog.close();
  }, [shot]);

  const caption = shot ? t(shot.captionKey) : "";

  return (
    <dialog
      ref={ref}
      className="gx-lightbox"
      aria-label={caption || undefined}
      // Fires for every way the dialog closes natively (Escape included).
      onClose={onClose}
      // Escape, stated: the native handling is the browser's, and this keeps
      // the same result where a dialog is driven by script alone.
      onKeyDown={(e) => {
        if (e.key === "Escape") {
          e.preventDefault();
          onClose();
        }
      }}
      // The backdrop belongs to the dialog element itself; the content sits in
      // the frame below, so a click that lands on the dialog is "outside".
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      {shot && (
        <div className="gx-lightbox__frame">
          <figure>
            <img src={shot.src} alt={caption} width={SHOT_WIDTH} height={SHOT_HEIGHT} />
            <figcaption>{caption}</figcaption>
          </figure>
          <IconButton
            label={t("common.close")}
            tooltip={false}
            className="gx-lightbox__close"
            onClick={onClose}
          >
            <X size={18} />
          </IconButton>
        </div>
      )}
    </dialog>
  );
}
