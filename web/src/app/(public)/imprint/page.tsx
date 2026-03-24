import type { Metadata } from "next";
import Link from "next/link";

export const metadata: Metadata = {
  title: "Imprint — Glyphoxa",
  description: "Legal notice (Impressum) for the Glyphoxa AI voice NPC platform.",
};

export default function ImprintPage() {
  return (
    <div className="mx-auto max-w-3xl px-4 py-12 sm:px-6 lg:px-8">
      <div className="mb-8">
        <Link href="/login" className="text-sm text-muted-foreground hover:text-primary">
          &larr; Back to login
        </Link>
      </div>

      <h1 className="mb-2 text-3xl font-bold tracking-tight text-foreground sm:text-4xl">
        Imprint
      </h1>
      <p className="mb-2 text-lg text-muted-foreground">
        Impressum gem&auml;&szlig; &sect; 5 TMG (Telemediengesetz)
      </p>
      <p className="mb-8 text-sm text-muted-foreground">
        Last updated: March 24, 2026
      </p>

      <div className="mb-10 rounded-lg border border-border/50 bg-card/50 p-4">
        <p className="text-sm text-muted-foreground italic">
          This is a template. The placeholders below must be filled in with actual data before
          publication. An Impressum is legally required in Germany under &sect; 5 TMG.
        </p>
      </div>

      <div className="space-y-8">
        <section>
          <h2 className="mb-3 text-xl font-semibold text-foreground">
            Angaben gem&auml;&szlig; &sect; 5 TMG
          </h2>
          <address className="not-italic space-y-1 text-muted-foreground leading-relaxed">
            <p className="font-medium text-foreground">[COMPANY_NAME]</p>
            <p>[STREET_ADDRESS]</p>
            <p>[POSTAL_CODE] [CITY], Germany</p>
          </address>
        </section>

        <section>
          <h2 className="mb-3 text-xl font-semibold text-foreground">Kontakt / Contact</h2>
          <div className="space-y-1 text-muted-foreground leading-relaxed">
            <p>
              Email:{" "}
              <a href="mailto:[CONTACT_EMAIL]" className="text-primary hover:underline">
                [CONTACT_EMAIL]
              </a>
            </p>
            <p>Phone: [PHONE_NUMBER]</p>
          </div>
        </section>

        <section>
          <h2 className="mb-3 text-xl font-semibold text-foreground">
            Vertreten durch / Represented by
          </h2>
          <p className="text-muted-foreground leading-relaxed">[FULL_NAME]</p>
        </section>

        <section>
          <h2 className="mb-3 text-xl font-semibold text-foreground">
            Umsatzsteuer-Identifikationsnummer / VAT ID
          </h2>
          <p className="text-muted-foreground leading-relaxed">
            Umsatzsteuer-Identifikationsnummer gem&auml;&szlig; &sect; 27a UStG: [VAT_ID]
          </p>
        </section>

        <section>
          <h2 className="mb-3 text-xl font-semibold text-foreground">
            Verantwortlich f&uuml;r den Inhalt / Responsible for Content
          </h2>
          <p className="text-muted-foreground leading-relaxed">
            Verantwortlich gem&auml;&szlig; &sect; 18 Abs. 2 MStV:
          </p>
          <address className="not-italic mt-2 space-y-1 text-muted-foreground leading-relaxed">
            <p className="font-medium text-foreground">[FULL_NAME]</p>
            <p>[STREET_ADDRESS]</p>
            <p>[POSTAL_CODE] [CITY], Germany</p>
          </address>
        </section>

        <section>
          <h2 className="mb-3 text-xl font-semibold text-foreground">
            EU-Streitschlichtung / EU Dispute Resolution
          </h2>
          <div className="space-y-3 text-muted-foreground leading-relaxed">
            <p>
              Die Europ&auml;ische Kommission stellt eine Plattform zur Online-Streitbeilegung
              (OS) bereit:{" "}
              <a
                href="https://ec.europa.eu/consumers/odr"
                target="_blank"
                rel="noopener noreferrer"
                className="text-primary hover:underline"
              >
                https://ec.europa.eu/consumers/odr
              </a>
            </p>
            <p>
              The European Commission provides an Online Dispute Resolution (ODR) platform:{" "}
              <a
                href="https://ec.europa.eu/consumers/odr"
                target="_blank"
                rel="noopener noreferrer"
                className="text-primary hover:underline"
              >
                https://ec.europa.eu/consumers/odr
              </a>
            </p>
          </div>
        </section>

        <section>
          <h2 className="mb-3 text-xl font-semibold text-foreground">
            Verbraucherstreitbeilegung / Consumer Dispute Resolution
          </h2>
          <p className="text-muted-foreground leading-relaxed">
            Wir sind nicht bereit oder verpflichtet, an Streitbeilegungsverfahren vor einer
            Verbraucherschlichtungsstelle teilzunehmen.
          </p>
          <p className="mt-2 text-muted-foreground leading-relaxed">
            We are not willing or obliged to participate in dispute resolution proceedings before
            a consumer arbitration board.
          </p>
        </section>

        <section>
          <h2 className="mb-3 text-xl font-semibold text-foreground">
            Haftungsausschluss / Disclaimer
          </h2>
          <div className="space-y-3 text-muted-foreground leading-relaxed">
            <div>
              <h3 className="mb-1 text-base font-medium text-foreground">Haftung f&uuml;r Inhalte</h3>
              <p>
                Die Inhalte unserer Seiten wurden mit gr&ouml;&szlig;ter Sorgfalt erstellt. F&uuml;r
                die Richtigkeit, Vollst&auml;ndigkeit und Aktualit&auml;t der Inhalte k&ouml;nnen
                wir jedoch keine Gew&auml;hr &uuml;bernehmen.
              </p>
            </div>
            <div>
              <h3 className="mb-1 text-base font-medium text-foreground">Haftung f&uuml;r Links</h3>
              <p>
                Unser Angebot enth&auml;lt Links zu externen Webseiten Dritter, auf deren Inhalte
                wir keinen Einfluss haben. F&uuml;r die Inhalte der verlinkten Seiten ist stets
                der jeweilige Anbieter verantwortlich.
              </p>
            </div>
          </div>
        </section>
      </div>

      <div className="mt-12 flex gap-4 border-t border-border/50 pt-8 text-sm text-muted-foreground">
        <Link href="/terms" className="hover:text-primary">
          Terms of Service
        </Link>
        <span>&middot;</span>
        <Link href="/privacy" className="hover:text-primary">
          Privacy Policy
        </Link>
        <span>&middot;</span>
        <Link href="/login" className="hover:text-primary">
          Back to login
        </Link>
      </div>
    </div>
  );
}
