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
          Die Europäische Kommission stellt eine Plattform zur Online-Streitbeilegung
          bereit:{" "}
          <a href="https://ec.europa.eu/consumers/odr/" target="_blank" rel="noreferrer noopener">
            https://ec.europa.eu/consumers/odr/
          </a>
          . Wir sind nicht verpflichtet und nicht bereit, an Streitbeilegungsverfahren vor
          einer Verbraucherschlichtungsstelle teilzunehmen.
        </p>
      </Section>

      <Section heading="Haftung für Inhalte und Links">
        <p>
          Als Diensteanbieter sind wir für eigene Inhalte auf diesen Seiten nach den
          allgemeinen Gesetzen verantwortlich (§ 7 Abs. 1 DDG). Nach §§ 8 bis 10 DDG sind
          wir jedoch nicht verpflichtet, übermittelte oder gespeicherte fremde
          Informationen zu überwachen. Für die Inhalte verlinkter externer Seiten ist
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
