import type { Metadata } from "next";
import Link from "next/link";

export const metadata: Metadata = {
  title: "Terms of Service — Glyphoxa",
  description: "Terms of Service for the Glyphoxa AI voice NPC platform.",
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

export default function TermsOfServicePage() {
  return (
    <div className="mx-auto max-w-3xl px-4 py-12 sm:px-6 lg:px-8">
      <div className="mb-8">
        <Link href="/login" className="text-sm text-muted-foreground hover:text-primary">
          &larr; Back to login
        </Link>
      </div>

      <h1 className="mb-2 text-3xl font-bold tracking-tight text-foreground sm:text-4xl">
        Terms of Service
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
          <TOCLink href="#service-description">Service Description</TOCLink>
          <TOCLink href="#account-terms">Account Terms</TOCLink>
          <TOCLink href="#acceptable-use">Acceptable Use</TOCLink>
          <TOCLink href="#subscriptions-billing">Subscriptions and Billing</TOCLink>
          <TOCLink href="#intellectual-property">Intellectual Property</TOCLink>
          <TOCLink href="#api-keys">API Key Handling</TOCLink>
          <TOCLink href="#service-availability">Service Availability</TOCLink>
          <TOCLink href="#termination">Termination</TOCLink>
          <TOCLink href="#limitation-of-liability">Limitation of Liability</TOCLink>
          <TOCLink href="#governing-law">Governing Law</TOCLink>
          <TOCLink href="#changes">Changes to These Terms</TOCLink>
          <TOCLink href="#contact">Contact</TOCLink>
        </ol>
      </nav>

      <div className="space-y-10">
        <Section id="service-description" title="1. Service Description">
          <p>
            Glyphoxa (&quot;the Service&quot;) is an AI-powered voice NPC platform for tabletop
            role-playing games. It is operated by [COMPANY_NAME], [ADDRESS] (&quot;we&quot;,
            &quot;us&quot;, &quot;our&quot;).
          </p>
          <p>
            The Service allows game masters to create AI-driven non-player characters (NPCs) with
            distinct voices, personalities, and persistent memory. NPCs participate in live voice
            chat sessions via supported platforms (e.g., Discord, WebRTC).
          </p>
          <p>
            By accessing or using Glyphoxa, you agree to be bound by these Terms of Service. If you
            do not agree to these terms, do not use the Service.
          </p>
        </Section>

        <Section id="account-terms" title="2. Account Terms">
          <p>
            To use Glyphoxa, you must sign in with a Discord account or an API key provided by a
            tenant administrator. You must be at least 16 years old to use the Service.
          </p>
          <ul className="list-disc space-y-1 pl-5">
            <li>One account per person. Do not create multiple accounts.</li>
            <li>You are responsible for keeping your login credentials and API keys secure.</li>
            <li>
              You are responsible for all activity that occurs under your account.
            </li>
            <li>
              Notify us immediately if you suspect unauthorized access to your account.
            </li>
          </ul>
        </Section>

        <Section id="acceptable-use" title="3. Acceptable Use">
          <p>You agree not to use Glyphoxa to:</p>
          <ul className="list-disc space-y-1 pl-5">
            <li>Violate any applicable law or regulation.</li>
            <li>
              Generate, distribute, or facilitate content that is illegal, harmful, threatening,
              abusive, harassing, defamatory, or otherwise objectionable.
            </li>
            <li>Impersonate any person or entity, or misrepresent your affiliation.</li>
            <li>
              Attempt to gain unauthorized access to the Service, other accounts, or connected
              systems.
            </li>
            <li>
              Interfere with or disrupt the Service or servers/networks connected to it.
            </li>
            <li>Use the Service for any purpose that is not related to tabletop RPGs or gaming.</li>
            <li>
              Use automated tools (bots, scrapers) to access the Service without prior written
              consent.
            </li>
          </ul>
          <p>
            We reserve the right to suspend or terminate accounts that violate these terms, at our
            sole discretion.
          </p>
        </Section>

        <Section id="subscriptions-billing" title="4. Subscriptions and Billing">
          <p>
            Glyphoxa offers tiered subscription plans. Details of available plans, pricing, and
            included features are available on the Service&apos;s pricing page.
          </p>
          <ul className="list-disc space-y-1 pl-5">
            <li>
              Subscriptions are billed in advance on a monthly or annual basis, depending on the
              plan selected.
            </li>
            <li>
              You may cancel your subscription at any time. Cancellation takes effect at the end of
              the current billing period. No partial refunds are provided for unused time.
            </li>
            <li>
              We reserve the right to change pricing with 30 days&apos; notice. Existing
              subscriptions will honor the current price until the next renewal.
            </li>
            <li>
              Free tiers and trials may be offered at our discretion and can be modified or
              discontinued at any time.
            </li>
          </ul>
        </Section>

        <Section id="intellectual-property" title="5. Intellectual Property">
          <p>
            <strong className="text-foreground">Your content:</strong> You retain ownership of all
            content you create through Glyphoxa, including NPC definitions, campaign settings,
            transcripts, and any creative material you provide. By using the Service, you grant us a
            limited license to process and store your content solely for the purpose of providing the
            Service.
          </p>
          <p>
            <strong className="text-foreground">Our service:</strong> Glyphoxa, its code, design,
            branding, and documentation are the intellectual property of [COMPANY_NAME]. You may not
            copy, modify, distribute, or reverse-engineer any part of the Service without prior
            written consent.
          </p>
          <p>
            <strong className="text-foreground">AI-generated content:</strong> Content generated by
            AI during voice sessions (NPC dialogue, narration) is produced in real-time and is
            considered part of your creative output as the game master. We make no claim of ownership
            over AI-generated content produced during your sessions.
          </p>
        </Section>

        <Section id="api-keys" title="6. API Key Handling">
          <p>
            Glyphoxa allows you to provide your own API keys for third-party AI services (e.g., LLM,
            TTS, STT providers). These keys are:
          </p>
          <ul className="list-disc space-y-1 pl-5">
            <li>Encrypted at rest using industry-standard encryption (HashiCorp Vault Transit).</li>
            <li>Used solely for making API calls on your behalf during voice sessions.</li>
            <li>Never shared with other users or third parties.</li>
            <li>Deletable at any time through your account settings.</li>
          </ul>
          <p>
            You are responsible for the costs incurred through your own API keys. We are not liable
            for charges from third-party providers resulting from your use of the Service.
          </p>
        </Section>

        <Section id="service-availability" title="7. Service Availability">
          <p>
            We aim to provide reliable service, but Glyphoxa is currently in beta. As such:
          </p>
          <ul className="list-disc space-y-1 pl-5">
            <li>
              We do not guarantee any specific uptime or service level agreement (SLA).
            </li>
            <li>
              The Service may be temporarily unavailable due to maintenance, updates, or
              circumstances beyond our control.
            </li>
            <li>
              Features may change, be added, or be removed as the Service evolves.
            </li>
            <li>We will make reasonable efforts to notify users of planned downtime.</li>
          </ul>
        </Section>

        <Section id="termination" title="8. Termination">
          <p>
            Either party may terminate this agreement at any time:
          </p>
          <ul className="list-disc space-y-1 pl-5">
            <li>
              <strong className="text-foreground">You</strong> may delete your account or stop using
              the Service at any time.
            </li>
            <li>
              <strong className="text-foreground">We</strong> may suspend or terminate your access if
              you violate these terms, or for any other reason with reasonable notice.
            </li>
          </ul>
          <p>
            Upon termination, your right to use the Service ceases immediately. We may retain your
            data for a reasonable period to comply with legal obligations, after which it will be
            deleted.
          </p>
        </Section>

        <Section id="limitation-of-liability" title="9. Limitation of Liability">
          <p>
            To the maximum extent permitted by applicable law:
          </p>
          <ul className="list-disc space-y-1 pl-5">
            <li>
              The Service is provided &quot;as is&quot; and &quot;as available&quot; without
              warranties of any kind, whether express or implied.
            </li>
            <li>
              We are not liable for any indirect, incidental, special, consequential, or punitive
              damages arising from your use of the Service.
            </li>
            <li>
              Our total liability is limited to the amount you paid us in the 12 months preceding the
              claim.
            </li>
            <li>
              We are not responsible for the accuracy, quality, or appropriateness of AI-generated
              content.
            </li>
          </ul>
        </Section>

        <Section id="governing-law" title="10. Governing Law">
          <p>
            These Terms are governed by the laws of the Federal Republic of Germany. Any disputes
            arising from or related to these Terms shall be subject to the exclusive jurisdiction of
            the courts in [CITY], Germany.
          </p>
          <p>
            If you are a consumer within the European Union, you also enjoy the protection of the
            mandatory provisions of consumer protection law in your country of residence. You may
            also use the{" "}
            <a
              href="https://ec.europa.eu/consumers/odr"
              target="_blank"
              rel="noopener noreferrer"
              className="text-primary hover:underline"
            >
              EU Online Dispute Resolution platform
            </a>
            .
          </p>
        </Section>

        <Section id="changes" title="11. Changes to These Terms">
          <p>
            We may update these Terms of Service from time to time. When we do, we will:
          </p>
          <ul className="list-disc space-y-1 pl-5">
            <li>Update the &quot;Last updated&quot; date at the top of this page.</li>
            <li>Provide at least 30 days&apos; notice for material changes (via email or in-app notification).</li>
            <li>
              Your continued use of the Service after the notice period constitutes acceptance of the
              updated terms.
            </li>
          </ul>
        </Section>

        <Section id="contact" title="12. Contact">
          <p>
            If you have any questions about these Terms of Service, contact us at:
          </p>
          <ul className="list-none space-y-1 pl-0">
            <li>
              Email:{" "}
              <a href="mailto:[CONTACT_EMAIL]" className="text-primary hover:underline">
                [CONTACT_EMAIL]
              </a>
            </li>
            <li>Address: [ADDRESS]</li>
          </ul>
        </Section>
      </div>

      <div className="mt-12 flex gap-4 border-t border-border/50 pt-8 text-sm text-muted-foreground">
        <Link href="/privacy" className="hover:text-primary">
          Privacy Policy
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
