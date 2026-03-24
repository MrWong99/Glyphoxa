import type { Metadata } from "next";
import Link from "next/link";

export const metadata: Metadata = {
  title: "Privacy Policy — Glyphoxa",
  description: "Privacy Policy for the Glyphoxa AI voice NPC platform. GDPR-compliant.",
};

function TOCLink({ href, children }: { href: string; children: React.ReactNode }) {
  return (
    <li>
      <a href={href} className="text-primary hover:underline">
        {children}
      </a>
    </li>
  );
}

function Section({ id, title, children }: { id: string; title: string; children: React.ReactNode }) {
  return (
    <section id={id} className="scroll-mt-24">
      <h2 className="mb-3 text-xl font-semibold text-foreground">{title}</h2>
      <div className="space-y-3 text-muted-foreground leading-relaxed">{children}</div>
    </section>
  );
}

export default function PrivacyPolicyPage() {
  return (
    <div className="mx-auto max-w-3xl px-4 py-12 sm:px-6 lg:px-8">
      <div className="mb-8">
        <Link href="/login" className="text-sm text-muted-foreground hover:text-primary">
          &larr; Back to login
        </Link>
      </div>

      <h1 className="mb-2 text-3xl font-bold tracking-tight text-foreground sm:text-4xl">
        Privacy Policy
      </h1>
      <p className="mb-8 text-sm text-muted-foreground">
        Last updated: March 24, 2026
      </p>

      <div className="mb-10 rounded-lg border border-border/50 bg-card/50 p-4">
        <p className="text-sm text-muted-foreground italic">
          This document is a template and has not been reviewed by a lawyer. It should be reviewed
          by a qualified legal professional before being relied upon.
        </p>
      </div>

      <nav className="mb-10">
        <h2 className="mb-3 text-sm font-semibold uppercase tracking-wide text-muted-foreground">
          Table of Contents
        </h2>
        <ol className="list-decimal space-y-1 pl-5 text-sm">
          <TOCLink href="#data-controller">Data Controller</TOCLink>
          <TOCLink href="#data-collected">What Data We Collect</TOCLink>
          <TOCLink href="#legal-basis">Legal Basis for Processing</TOCLink>
          <TOCLink href="#data-usage">How We Use Your Data</TOCLink>
          <TOCLink href="#data-retention">Data Retention</TOCLink>
          <TOCLink href="#third-party">Third-Party Services</TOCLink>
          <TOCLink href="#data-transfers">International Data Transfers</TOCLink>
          <TOCLink href="#your-rights">Your Rights (GDPR)</TOCLink>
          <TOCLink href="#cookies">Cookies and Local Storage</TOCLink>
          <TOCLink href="#children">Children&apos;s Privacy</TOCLink>
          <TOCLink href="#changes">Changes to This Policy</TOCLink>
          <TOCLink href="#contact">Contact and DPO</TOCLink>
        </ol>
      </nav>

      <div className="space-y-10">
        <Section id="data-controller" title="1. Data Controller">
          <p>
            The data controller responsible for processing your personal data is:
          </p>
          <address className="not-italic rounded-lg border border-border/50 bg-card/50 p-4">
            <p className="font-medium text-foreground">[COMPANY_NAME]</p>
            <p>[ADDRESS]</p>
            <p>
              Email:{" "}
              <a href="mailto:[CONTACT_EMAIL]" className="text-primary hover:underline">
                [CONTACT_EMAIL]
              </a>
            </p>
          </address>
        </Section>

        <Section id="data-collected" title="2. What Data We Collect">
          <h3 className="text-base font-medium text-foreground">2.1 Discord Account Information</h3>
          <p>
            When you sign in via Discord OAuth2, we receive and store:
          </p>
          <ul className="list-disc space-y-1 pl-5">
            <li>Discord user ID</li>
            <li>Email address (as provided by Discord)</li>
            <li>Username and display name</li>
            <li>Avatar URL</li>
          </ul>
          <p>
            We do not receive or store your Discord password. Authentication is handled entirely
            by Discord&apos;s OAuth2 flow.
          </p>

          <h3 className="mt-4 text-base font-medium text-foreground">2.2 Voice Audio</h3>
          <p>
            During voice sessions, audio from participants is:
          </p>
          <ul className="list-disc space-y-1 pl-5">
            <li>Processed in real-time for speech-to-text conversion.</li>
            <li>
              <strong className="text-foreground">Not permanently stored as audio files</strong> unless
              explicitly enabled by the campaign owner.
            </li>
            <li>Transmitted to third-party STT providers for transcription (see Section 6).</li>
          </ul>

          <h3 className="mt-4 text-base font-medium text-foreground">2.3 Transcripts</h3>
          <p>
            Text transcriptions of voice sessions are stored in our PostgreSQL database. Transcripts
            are scoped per tenant and campaign. Campaign owners can configure retention settings and
            delete transcripts.
          </p>

          <h3 className="mt-4 text-base font-medium text-foreground">2.4 API Keys</h3>
          <p>
            If you provide API keys for third-party AI services:
          </p>
          <ul className="list-disc space-y-1 pl-5">
            <li>Keys are encrypted at rest using HashiCorp Vault Transit encryption.</li>
            <li>Keys are only decrypted at the moment of use for API calls.</li>
            <li>Keys are never logged, displayed in full, or shared.</li>
          </ul>

          <h3 className="mt-4 text-base font-medium text-foreground">2.5 Usage Data</h3>
          <p>
            We collect operational data to run and improve the Service:
          </p>
          <ul className="list-disc space-y-1 pl-5">
            <li>Session duration and frequency</li>
            <li>Token counts (LLM usage per session)</li>
            <li>Error logs (anonymized where possible)</li>
            <li>Feature usage metrics</li>
          </ul>

          <h3 className="mt-4 text-base font-medium text-foreground">2.6 NPC and Campaign Data</h3>
          <p>
            NPC definitions (name, personality, voice settings) and campaign configurations you
            create are stored to provide the Service. This data is scoped to your tenant and is not
            shared with other users.
          </p>
        </Section>

        <Section id="legal-basis" title="3. Legal Basis for Processing">
          <p>
            We process your personal data on the following legal bases under GDPR:
          </p>
          <ul className="list-disc space-y-1 pl-5">
            <li>
              <strong className="text-foreground">Consent</strong> (Art. 6(1)(a) GDPR): When you
              sign in via Discord OAuth2, you consent to the transfer of your Discord profile
              information.
            </li>
            <li>
              <strong className="text-foreground">Contract performance</strong> (Art. 6(1)(b) GDPR):
              Processing necessary to provide the Service you signed up for, including voice session
              processing, transcript storage, and account management.
            </li>
            <li>
              <strong className="text-foreground">Legitimate interest</strong> (Art. 6(1)(f) GDPR):
              Service security, fraud prevention, usage analytics for service improvement, and
              debugging.
            </li>
          </ul>
        </Section>

        <Section id="data-usage" title="4. How We Use Your Data">
          <p>We use your data to:</p>
          <ul className="list-disc space-y-1 pl-5">
            <li>Provide and maintain the Service (voice sessions, NPC interactions, transcripts).</li>
            <li>Authenticate your identity and manage your account.</li>
            <li>Enforce usage quotas and subscription limits.</li>
            <li>Improve the Service based on aggregated, anonymized usage patterns.</li>
            <li>Communicate with you about your account or important Service updates.</li>
            <li>Comply with legal obligations.</li>
          </ul>
          <p>
            We do <strong className="text-foreground">not</strong> sell your personal data. We do{" "}
            <strong className="text-foreground">not</strong> use your data for advertising. We do{" "}
            <strong className="text-foreground">not</strong> use your content to train AI models.
          </p>
        </Section>

        <Section id="data-retention" title="5. Data Retention">
          <ul className="list-disc space-y-1 pl-5">
            <li>
              <strong className="text-foreground">Account data</strong> (Discord profile, email): Retained
              until you delete your account or request deletion.
            </li>
            <li>
              <strong className="text-foreground">Transcripts</strong>: Retained according to campaign-level
              settings configured by the campaign owner. Defaults to indefinite retention unless
              configured otherwise.
            </li>
            <li>
              <strong className="text-foreground">Voice audio</strong>: Processed in real-time and
              discarded immediately after transcription. Not stored permanently.
            </li>
            <li>
              <strong className="text-foreground">API keys</strong>: Retained until you delete them
              or your account is terminated.
            </li>
            <li>
              <strong className="text-foreground">Usage data</strong>: Retained for up to 24 months
              for analytics, then anonymized or deleted.
            </li>
          </ul>
          <p>
            Upon account deletion, your personal data will be removed within 30 days. Some data may
            be retained longer if required by law.
          </p>
        </Section>

        <Section id="third-party" title="6. Third-Party Services">
          <p>
            Glyphoxa integrates with third-party services to provide its functionality. Data shared
            with these services is limited to what is necessary:
          </p>
          <ul className="list-disc space-y-2 pl-5">
            <li>
              <strong className="text-foreground">Discord</strong> — Authentication (OAuth2) and
              voice chat connectivity. Subject to{" "}
              <a
                href="https://discord.com/privacy"
                target="_blank"
                rel="noopener noreferrer"
                className="text-primary hover:underline"
              >
                Discord&apos;s Privacy Policy
              </a>
              .
            </li>
            <li>
              <strong className="text-foreground">ElevenLabs</strong> — Text-to-speech and
              speech-to-text processing. Voice audio snippets are sent for processing.
            </li>
            <li>
              <strong className="text-foreground">Google (Gemini)</strong> — Large language model
              inference and embedding generation. Transcript text is sent for NPC response
              generation.
            </li>
            <li>
              <strong className="text-foreground">Stripe</strong> — Payment processing (if
              applicable). We do not store credit card numbers; Stripe handles all payment data.
              Subject to{" "}
              <a
                href="https://stripe.com/privacy"
                target="_blank"
                rel="noopener noreferrer"
                className="text-primary hover:underline"
              >
                Stripe&apos;s Privacy Policy
              </a>
              .
            </li>
          </ul>
          <p>
            When you provide your own API keys, requests to these services are made with your
            credentials. When using Glyphoxa-provided keys, requests are made with our credentials.
          </p>
        </Section>

        <Section id="data-transfers" title="7. International Data Transfers">
          <p>
            Some of our third-party service providers may process data outside the European Economic
            Area (EEA). In such cases, we ensure appropriate safeguards are in place:
          </p>
          <ul className="list-disc space-y-1 pl-5">
            <li>
              EU Standard Contractual Clauses (SCCs) where applicable.
            </li>
            <li>
              Adequacy decisions by the European Commission (e.g., EU-US Data Privacy Framework).
            </li>
            <li>
              Provider-specific data processing agreements.
            </li>
          </ul>
          <p>
            Our own infrastructure is hosted within the EU.
          </p>
        </Section>

        <Section id="your-rights" title="8. Your Rights (GDPR)">
          <p>
            Under the General Data Protection Regulation (GDPR), you have the following rights
            regarding your personal data:
          </p>
          <ul className="list-disc space-y-2 pl-5">
            <li>
              <strong className="text-foreground">Right of access</strong> (Art. 15) — Request a
              copy of the personal data we hold about you.
            </li>
            <li>
              <strong className="text-foreground">Right to rectification</strong> (Art. 16) —
              Request correction of inaccurate or incomplete personal data.
            </li>
            <li>
              <strong className="text-foreground">Right to erasure</strong> (Art. 17) — Request
              deletion of your personal data (&quot;right to be forgotten&quot;).
            </li>
            <li>
              <strong className="text-foreground">Right to restrict processing</strong> (Art. 18) —
              Request that we limit how we use your data.
            </li>
            <li>
              <strong className="text-foreground">Right to data portability</strong> (Art. 20) —
              Receive your data in a structured, machine-readable format.
            </li>
            <li>
              <strong className="text-foreground">Right to object</strong> (Art. 21) — Object to
              processing based on legitimate interests.
            </li>
            <li>
              <strong className="text-foreground">Right to withdraw consent</strong> (Art. 7(3)) —
              Withdraw consent at any time (does not affect prior processing).
            </li>
          </ul>
          <p>
            To exercise any of these rights, contact us at{" "}
            <a href="mailto:[CONTACT_EMAIL]" className="text-primary hover:underline">
              [CONTACT_EMAIL]
            </a>
            . We will respond within 30 days as required by law.
          </p>
          <p>
            You also have the right to lodge a complaint with your local data protection authority.
            In Germany, this is the relevant state data protection authority
            (Landesdatenschutzbeauftragter).
          </p>
        </Section>

        <Section id="cookies" title="9. Cookies and Local Storage">
          <p>
            Glyphoxa uses only <strong className="text-foreground">functional cookies and local
            storage</strong>. We do not use tracking cookies, analytics cookies, or advertising
            cookies.
          </p>
          <ul className="list-disc space-y-2 pl-5">
            <li>
              <strong className="text-foreground">Authentication token</strong> — Stored in
              localStorage as <code className="rounded bg-muted px-1.5 py-0.5 text-xs">glyphoxa_token</code>.
              This is necessary for keeping you signed in. It is removed when you log out.
            </li>
            <li>
              <strong className="text-foreground">UI preferences</strong> — Settings like sidebar
              state may be stored in localStorage for your convenience. These contain no personal
              data.
            </li>
          </ul>
          <p>
            Since we only use strictly necessary/functional storage, no consent is required under
            the ePrivacy Directive. We still inform you of their use here for transparency.
          </p>
        </Section>

        <Section id="children" title="10. Children&apos;s Privacy">
          <p>
            Glyphoxa is not directed at children under 16 years of age. We do not knowingly collect
            personal data from children under 16. If you believe a child under 16 has provided us
            with personal data, please contact us and we will take steps to delete that information.
          </p>
        </Section>

        <Section id="changes" title="11. Changes to This Policy">
          <p>
            We may update this Privacy Policy from time to time. When we make material changes:
          </p>
          <ul className="list-disc space-y-1 pl-5">
            <li>We will update the &quot;Last updated&quot; date at the top.</li>
            <li>We will provide at least 30 days&apos; notice for significant changes.</li>
            <li>We may notify you via email or in-app notification.</li>
          </ul>
        </Section>

        <Section id="contact" title="12. Contact and DPO">
          <p>
            For any privacy-related questions or to exercise your data protection rights:
          </p>
          <address className="not-italic rounded-lg border border-border/50 bg-card/50 p-4">
            <p className="font-medium text-foreground">[COMPANY_NAME]</p>
            <p>[ADDRESS]</p>
            <p>
              Email:{" "}
              <a href="mailto:[CONTACT_EMAIL]" className="text-primary hover:underline">
                [CONTACT_EMAIL]
              </a>
            </p>
          </address>
          <p className="mt-3">
            If a Data Protection Officer (DPO) has been appointed, their contact details will be
            provided here: [DPO_EMAIL]
          </p>
        </Section>
      </div>

      <div className="mt-12 flex gap-4 border-t border-border/50 pt-8 text-sm text-muted-foreground">
        <Link href="/terms" className="hover:text-primary">
          Terms of Service
        </Link>
        <span>&middot;</span>
        <Link href="/imprint" className="hover:text-primary">
          Imprint
        </Link>
        <span>&middot;</span>
        <Link href="/login" className="hover:text-primary">
          Back to login
        </Link>
      </div>
    </div>
  );
}
