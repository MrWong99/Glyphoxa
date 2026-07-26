import { LegalPage, OperatorValue, Section } from "./LegalPage";
import { OPERATOR } from "./operator";

// Impressum (#518) — §5 DDG (ex-§5 TMG) and, where editorial content exists,
// §18 Abs. 2 MStV. Every substantive value comes from operator.ts, which the
// OPERATOR fills in; nothing here is invented by the project.

export function Imprint() {
  return (
    <LegalPage title="Impressum" lede="Angaben gemäß § 5 DDG">
      <Section heading="Diensteanbieter">
        <p>
          <OperatorValue value={OPERATOR.legalName} />
          <br />
          <OperatorValue value={OPERATOR.street} />
          <br />
          <OperatorValue value={OPERATOR.city} />
          <br />
          <OperatorValue value={OPERATOR.country} />
        </p>
      </Section>

      <Section heading="Kontakt">
        <p>
          E-Mail: <OperatorValue value={OPERATOR.email} />
          {OPERATOR.phone && (
            <>
              <br />
              Telefon: {OPERATOR.phone}
            </>
          )}
        </p>
        <p>
          Anfragen zu dieser Instanz — einschließlich Auskunfts- und Löschersuchen nach
          DSGVO — richten Sie bitte an diese Adresse. Wir antworten in der Regel innerhalb
          eines Monats (Art. 12 Abs. 3 DSGVO).
        </p>
      </Section>

      {OPERATOR.contentResponsible && (
        <Section heading="Redaktionell verantwortlich (§ 18 Abs. 2 MStV)">
          <p>{OPERATOR.contentResponsible}</p>
        </Section>
      )}

      {OPERATOR.vatId && (
        <Section heading="Umsatzsteuer-Identifikationsnummer">
          <p>{OPERATOR.vatId} (§ 27a UStG)</p>
        </Section>
      )}

      <Section heading="Streitbeilegung">
        <p>
          Wir sind nicht verpflichtet und nicht bereit, an Streitbeilegungsverfahren vor
          einer Verbraucherschlichtungsstelle teilzunehmen (§ 36 VSBG).
        </p>
      </Section>

      <Section heading="Haftung für Inhalte und Links">
        <p>
          Als Diensteanbieter sind wir für eigene Inhalte auf diesen Seiten nach den
          allgemeinen Gesetzen verantwortlich. Für übermittelte oder gespeicherte fremde
          Informationen gelten die Haftungsprivilegien der Art. 4 bis 6 der Verordnung
          (EU) 2022/2065 (Digital Services Act); eine allgemeine Überwachungspflicht
          besteht nicht (Art. 8 DSA). Für die Inhalte verlinkter externer Seiten ist
          stets deren Anbieter verantwortlich; zum Zeitpunkt der Verlinkung waren keine
          Rechtsverstöße erkennbar. Bei Bekanntwerden von Rechtsverletzungen entfernen wir
          derartige Links umgehend.
        </p>
      </Section>

      <Section heading="Diese Instanz">
        <p>
          Glyphoxa ist quelloffene Software (
          <a
            href="https://github.com/MrWong99/Glyphoxa"
            target="_blank"
            rel="noreferrer noopener"
          >
            github.com/MrWong99/Glyphoxa
          </a>
          ). Verantwortlich für <em>diese</em> Instanz — Betrieb, Daten und Inhalte — ist
          ausschließlich der oben genannte Diensteanbieter, nicht das Projekt oder seine
          Mitwirkenden.
        </p>
      </Section>
    </LegalPage>
  );
}
