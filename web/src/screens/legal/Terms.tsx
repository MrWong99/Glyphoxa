import { LegalPage, OperatorValue, Section } from "./LegalPage";
import { OPERATOR } from "./operator";

// Nutzungsbedingungen / Acceptable Use Policy (#518). The document the open-mode
// signup asks new users to acknowledge before they start the Discord OAuth flow.
// Short by design: a beta-scale AUP that says what the service is, what it is
// not, and what gets an account suspended.

export function Terms() {
  return (
    <LegalPage
      title="Nutzungsbedingungen"
      lede="Die Spielregeln für diese Instanz — bitte vor der Anmeldung lesen."
    >
      <Section heading="1. Was dieser Dienst ist">
        <p>
          Glyphoxa gibt Nicht-Spieler-Charakteren einer Pen-&-Paper-Kampagne eine Stimme
          in einem Discord-Sprachkanal und führt daraus ein durchsuchbares
          Kampagnengedächtnis. Diese Instanz wird von{" "}
          <OperatorValue value={OPERATOR.legalName} /> betrieben und befindet sich in einer{" "}
          <strong>offenen Beta</strong>: Funktionen können sich ändern oder ausfallen, und
          es besteht kein Anspruch auf Verfügbarkeit oder auf den Erhalt gespeicherter
          Inhalte. Legen Sie von Inhalten, die Ihnen wichtig sind, eigene Notizen oder
          Kopien an; eine Kopie Ihrer gespeicherten Daten können Sie beim Betreiber
          anfragen (Art. 15 DSGVO).
        </p>
      </Section>

      <Section heading="2. Konto">
        <p>
          Für die Nutzung ist ein Discord-Konto erforderlich; es gelten zusätzlich die
          Bedingungen von Discord. Sie sind für die Aktivitäten unter Ihrem Konto
          verantwortlich. Der Dienst richtet sich nicht an Kinder unter 16 Jahren.
        </p>
      </Section>

      <Section heading="3. Regeln für die Nutzung">
        <p>Untersagt ist insbesondere:</p>
        <ul>
          <li>
            rechtswidrige Inhalte, Belästigung, Hassrede, sexualisierte Darstellungen
            Minderjähriger sowie Inhalte, die Rechte Dritter verletzen;
          </li>
          <li>
            das Aufzeichnen oder Transkribieren von Gesprächen, in denen Teilnehmende
            dem widersprochen haben — die Spielleitung ist dafür verantwortlich, dass ihr
            Tisch über die Transkription informiert ist (siehe Datenschutzerklärung);
          </li>
          <li>
            das Hochladen personenbezogener Daten Dritter ohne deren Kenntnis, sowie das
            Weiterverbreiten von Aufnahmen ohne Einwilligung der Aufgenommenen;
          </li>
          <li>
            automatisierte Massennutzung, Lasttests, Umgehung von Limits oder
            Weiterverkauf der Rechenleistung dieser Instanz;
          </li>
          <li>
            Versuche, in fremde Konten, Kampagnen oder die Infrastruktur einzudringen,
            sowie das Umgehen von Zugriffsbeschränkungen.
          </li>
        </ul>
      </Section>

      <Section heading="4. Kosten und Zugangsschlüssel">
        <p>
          Für die Beta fallen keine Gebühren an. Die Nutzung externer KI-Anbieter
          verursacht jedoch Kosten: Je nach Tarif nutzen Sie ein begrenztes
          Testguthaben des Betreibers oder hinterlegen eigene Zugangsschlüssel (BYOK). Für
          Kosten, die über eigene Schlüssel bei den jeweiligen Anbietern entstehen, haften
          Sie selbst; ein optional einstellbares Verbrauchslimit je Sprachsitzung hilft
          dabei, sie zu begrenzen. Ist das Testguthaben aufgebraucht, ruhen die
          betroffenen Funktionen.
        </p>
      </Section>

      <Section heading="5. Ihre Inhalte">
        <p>
          Ihre Kampagneninhalte gehören Ihnen. Sie räumen dem Betreiber nur die Rechte
          ein, die für den Betrieb nötig sind: speichern, verarbeiten und an die zur
          Funktionserbringung eingesetzten Anbieter übermitteln (siehe
          Datenschutzerklärung). Inhalte werden nicht zum Training von KI-Modellen des
          Betreibers verwendet und nicht an Dritte verkauft.
        </p>
      </Section>

      <Section heading="6. Sperrung und Beendigung">
        <p>
          Bei Verstößen gegen diese Bedingungen kann der Zugang gesperrt werden — bei
          schwerwiegenden Verstößen ohne Vorankündigung. Ihre Kampagnen können Sie
          jederzeit selbst löschen; die Löschung Ihres Kontos können Sie jederzeit beim
          Betreiber verlangen. Der Betrieb dieser Instanz kann eingestellt werden; wir
          bemühen uns, dies mit angemessener Vorlaufzeit anzukündigen, damit Sie eine
          Kopie Ihrer Daten anfragen und Inhalte sichern können.
        </p>
      </Section>

      <Section heading="7. Haftung">
        <p>
          Der Dienst wird ohne Gewähr für Verfügbarkeit oder Richtigkeit der von KI
          erzeugten Ausgaben bereitgestellt. Die Haftung des Betreibers richtet sich nach
          den gesetzlichen Vorschriften; für leicht fahrlässige Pflichtverletzungen wird
          nur bei Verletzung wesentlicher Vertragspflichten und begrenzt auf den
          vorhersehbaren, vertragstypischen Schaden gehaftet. Die Haftung für Schäden aus
          der Verletzung des Lebens, des Körpers oder der Gesundheit sowie nach dem
          Produkthaftungsgesetz bleibt unberührt.
        </p>
      </Section>

      <Section heading="8. Kontakt und Änderungen">
        <p>
          Fragen und Meldungen: <OperatorValue value={OPERATOR.email} />. Diese Bedingungen
          können angepasst werden; die jeweils hier veröffentlichte Fassung gilt ab
          Veröffentlichung. Wesentliche Änderungen kündigen wir im Dienst an.
        </p>
      </Section>
    </LegalPage>
  );
}
