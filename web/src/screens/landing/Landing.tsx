import { Dices, Mic, ScrollText, Sparkles, KeyRound } from "lucide-react";

import { LegalFooter } from "@/components/LegalFooter";

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
// The screenshots are placeholders until #263's captures land; each frame keeps
// its caption so the layout (and the honesty of the caption) is already right.

type Feature = { icon: typeof Mic; title: string; body: string };

const FEATURES: Feature[] = [
  {
    icon: Mic,
    title: "NPCs that talk back",
    body: "Your Agents join the Discord voice channel, hear the table, and answer out loud in their own voice — addressed by name, or by whoever the scene is pointing at.",
  },
  {
    icon: ScrollText,
    title: "The session, written down",
    body: "Every session is transcribed and searchable afterwards, and the table's own history feeds back into what the NPCs remember.",
  },
  {
    icon: Sparkles,
    title: "Run by the GM, not by the bot",
    body: "You write the personas, mute an NPC mid-scene, put words in their mouth with /say, and steer the next reply with a directive. Audio recording is opt-in, per player.",
  },
  {
    icon: KeyRound,
    title: "Your keys, your data",
    body: "Bring your own provider keys and the usage is yours alone. Everything lives in one Postgres database on the operator's own server, and deleting a campaign removes all of it.",
  },
];

// Screenshot slots. src stays null until #263's captures land; the frame then
// renders the image in place of the placeholder with no layout change.
type Shot = { caption: string; src: string | null };

const SHOTS: Shot[] = [
  { caption: "The live session screen: transcript, who is speaking, spend so far.", src: null },
  { caption: "An Agent's persona and voice, in the campaign editor.", src: null },
  { caption: "The campaign's knowledge graph, built from play.", src: null },
  { caption: "Provider keys and spend caps, per tenant.", src: null },
];

export function Landing({ open = false }: { open?: boolean }) {
  return (
    <div className="gx-landing">
      <header className="gx-landing__bar">
        <span className="gx-landing__brand">
          <span className="gx-landing__sigil">
            <Dices size={18} />
          </span>
          <span className="gx-gradient-text">Glyphoxa</span>
        </span>
        <a className="gx-btn gx-btn--secondary" href="/login">
          Sign in
        </a>
      </header>

      <section className="gx-landing__hero">
        <h1 className="gx-landing__headline">
          Give your <span className="gx-gradient-text">NPCs a voice</span> at the table
        </h1>
        <p className="gx-landing__lede">
          Glyphoxa is a self-hosted companion for tabletop RPGs played over Discord. It
          voices your campaign&apos;s characters in the voice channel, keeps the transcript,
          and remembers what happened — while the GM stays in charge of all of it.
        </p>
        <div className="gx-landing__cta">
          <a className="gx-btn gx-btn--primary gx-btn--lg" href="/login">
            {open ? "Start with Discord" : "Sign in with Discord"}
          </a>
          <span className="gx-landing__cta-note">
            {open
              ? "Free while in beta · sign in with the Discord account you play on"
              : "Sign in with the Discord account you play on"}
          </span>
        </div>
      </section>

      <section className="gx-landing__features" aria-label="What Glyphoxa does">
        {FEATURES.map(({ icon: Icon, title, body }) => (
          <article key={title} className="gx-landing__feature">
            <span className="gx-landing__feature-icon">
              <Icon size={18} />
            </span>
            <h2>{title}</h2>
            <p>{body}</p>
          </article>
        ))}
      </section>

      <section className="gx-landing__shots" aria-label="Screenshots">
        {SHOTS.map((shot) => (
          <figure key={shot.caption} className="gx-landing__shot">
            {shot.src ? (
              <img src={shot.src} alt={shot.caption} loading="lazy" />
            ) : (
              <div className="gx-landing__shot-placeholder" role="presentation" />
            )}
            <figcaption>{shot.caption}</figcaption>
          </figure>
        ))}
      </section>

      {open && (
        <section className="gx-landing__beta" aria-labelledby="beta-heading">
          <h2 id="beta-heading">How the free beta works</h2>
          <div className="gx-landing__plans">
            <article className="gx-landing__plan">
              <h3>Try it on us</h3>
              <p>
                New tables start with a small monthly allowance of provider usage paid for
                by this instance. No card, nothing to cancel. When the allowance is used
                up, the voice features pause until the next month — or ask the operator to
                move you to the free bring-your-own-keys plan.
              </p>
            </article>
            <article className="gx-landing__plan">
              <h3>Then bring your own keys</h3>
              <p>
                For real campaigns, add your own provider keys (speech, language model,
                images) in the settings and ask to be moved to the BYOK plan. Usage is
                then billed to you by those providers, and this instance charges nothing
                on top.
              </p>
            </article>
          </div>
          <p className="gx-landing__beta-note">
            This is an open beta on a small self-hosted server: expect rough edges and the
            occasional restart, and keep your own notes on anything you would hate to
            lose.
          </p>
        </section>
      )}

      <p className="gx-landing__beta-note">
        Sessions are transcribed — the bot says so in the voice channel before it starts —
        and audio recording is opt-in per player. Details in the{" "}
        <a href="/privacy">Datenschutzerklärung</a>.
      </p>

      <LegalFooter />
    </div>
  );
}
