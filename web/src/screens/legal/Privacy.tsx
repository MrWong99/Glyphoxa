import { LegalPage, OperatorValue, Section } from "./LegalPage";
import { OPERATOR } from "./operator";

// Datenschutzerklärung (#518). A TEMPLATE drafted from established German
// boilerplate and the system's actual data flows — not legal advice, and not
// lawyer-reviewed (an accepted beta risk, decided 2026-07-22).
//
// The substance mirrors what the software really does, so keep it in step with
// the code: Discord OAuth (ADR-0016), always-on transcription into Transcript
// Lines/Chunks + embeddings (ADR-0011/0040), the opt-in Rollover Tape
// (ADR-0051), the provider subprocessors (ADR-0004), and the
// GM-as-controller / operator-as-processor framing for table participants.

export function Privacy() {
  return (
    <LegalPage
      title="Datenschutzerklärung"
      lede="Wie diese Instanz personenbezogene Daten verarbeitet — für Spielleitungen, Spielende am Tisch und Besucher:innen."
    >
      <Section heading="1. Verantwortlicher">
        <p>
          Verantwortlich für die Verarbeitung im Sinne der DSGVO ist:
          <br />
          <OperatorValue value={OPERATOR.legalName} />, <OperatorValue value={OPERATOR.street} />,{" "}
          <OperatorValue value={OPERATOR.city} />, <OperatorValue value={OPERATOR.country} />,
          E-Mail: <OperatorValue value={OPERATOR.email} />.
        </p>
        {OPERATOR.dataProtectionOfficer && (
          <p>Datenschutzbeauftragte(r): {OPERATOR.dataProtectionOfficer}</p>
        )}
        <p>
          <strong>Rollenverteilung am Spieltisch:</strong> Die Spielleitung (GM) entscheidet,
          welche Kampagne läuft, wer eingeladen wird, welche Aufnahmefunktionen aktiviert
          sind und wann Daten gelöscht werden. Für die Inhalte ihrer Kampagne ist damit
          die Spielleitung verantwortlich; der Betreiber dieser Instanz stellt die
          technische Plattform bereit und handelt insoweit weisungsgebunden
          (Auftragsverarbeitung, Art. 28 DSGVO). Wenden Sie sich bei Fragen zu den Inhalten
          einer Kampagne zuerst an Ihre Spielleitung — Betroffenenrechte können Sie
          selbstverständlich auch direkt beim Betreiber geltend machen.
        </p>
      </Section>

      <Section heading="2. Anmeldung über Discord">
        <p>
          Die Anmeldung erfolgt ausschließlich über Discord (OAuth 2.0). Dabei
          verarbeiten wir die von Discord übermittelten Angaben Ihres Kontos:
          Discord-Benutzer-ID, Benutzername bzw. Anzeigename und — sofern vorhanden —
          die URL Ihres Profilbilds. Ein Passwort speichern wir nicht.
        </p>
        <p>
          Zweck ist die Authentifizierung und die Zuordnung Ihrer Inhalte zu Ihrem Konto;
          Rechtsgrundlage ist Art. 6 Abs. 1 lit. b DSGVO (Durchführung des
          Nutzungsverhältnisses). Die Sitzung wird über ein technisch notwendiges Cookie
          gehalten (Art. 6 Abs. 1 lit. f DSGVO; § 25 Abs. 2 Nr. 2 TDDDG — kein
          Einwilligungsbanner nötig, weil kein Tracking stattfindet). Wir setzen keine
          Analyse- oder Werbe-Cookies ein.
        </p>
        <p>
          Der Aufruf von Discord selbst unterliegt der Datenschutzerklärung von Discord
          (Discord Netherlands B.V. / Discord Inc.).
        </p>
      </Section>

      <Section heading="3. Sprachsitzungen: Transkription">
        <p>
          <strong>Jede Voice Session wird transkribiert.</strong> Wenn der Bot einem
          Discord-Sprachkanal beitritt, weist er im zugehörigen Textkanal einmalig darauf
          hin. Verarbeitet werden dabei:
        </p>
        <ul>
          <li>
            das gesprochene Audio der Teilnehmenden — <em>ausschließlich transient</em>, zur
            Spracherkennung; es wird nicht dauerhaft gespeichert (Ausnahme: die unter
            Ziffer 4 beschriebene, ausdrücklich einwilligungsbasierte Aufnahmefunktion),
          </li>
          <li>
            der daraus erzeugte <strong>Transkripttext</strong>, gespeichert je Kampagne als
            einzelne Zeilen und als zusammengefasste Abschnitte,
          </li>
          <li>
            <strong>Vektor-Einbettungen (Embeddings)</strong> dieser Abschnitte, damit die
            Agenten sich im Spielverlauf an frühere Szenen erinnern und die Spielleitung
            das Transkript durchsuchen kann,
          </li>
          <li>
            die <strong>Discord-Benutzer-ID der sprechenden Person</strong> zur Zuordnung der
            Zeile (bzw. der zugeordnete Charaktername),
          </li>
          <li>technische Metadaten der Sitzung (Zeitstempel, Kanal, Dauer).</li>
        </ul>
        <p>
          Zweck ist die Kernfunktion des Dienstes: Antworten der Agenten im Spiel,
          Zusammenfassungen und ein durchsuchbares Kampagnengedächtnis. Rechtsgrundlage
          ist Art. 6 Abs. 1 lit. b DSGVO gegenüber der Spielleitung und Art. 6 Abs. 1
          lit. f DSGVO gegenüber den Mitspielenden (berechtigtes Interesse an der
          Protokollierung des gemeinsamen Spiels), über die vor Sitzungsbeginn im
          Textkanal informiert wird. Wer nicht transkribiert werden möchte, sollte der
          Sprachsitzung nicht beitreten und die Spielleitung ansprechen — sie kann die
          Sitzung jederzeit beenden und Transkripte löschen.
        </p>
      </Section>

      <Section heading="4. Session Highlights (Audioaufnahme, freiwillig)">
        <p>
          Optional kann eine Spielleitung „Session Highlights“ aktivieren. Erst dann wird
          Audio überhaupt zwischengespeichert, und zwar so:
        </p>
        <ul>
          <li>
            Der Bot postet im Textkanal eine Einwilligungsabfrage mit den Schaltflächen
            <strong> Consent</strong> und <strong>Revoke</strong>.
          </li>
          <li>
            In einen flüchtigen Ringspeicher von rund 120 Sekunden gelangt ausschließlich
            das Audio derjenigen Personen, die zuvor <strong>Consent</strong> gedrückt haben
            (Art. 6 Abs. 1 lit. a DSGVO). Ohne Einwilligung wird Ihr Audio zu keinem
            Zeitpunkt in diesen Speicher kopiert. Die Einwilligung ist jederzeit mit
            Wirkung für die Zukunft widerrufbar.
          </li>
          <li>Der Ringspeicher wird am Ende der Sprachsitzung vollständig verworfen.</li>
          <li>
            Daraus geschnittene Clip-Kandidaten werden bis zur Durchsicht durch die
            Spielleitung aufbewahrt und spätestens nach 7 Tagen automatisch gelöscht.
          </li>
          <li>
            Nur von der Spielleitung ausdrücklich übernommene Highlights bleiben dauerhaft
            gespeichert, bis sie gelöscht werden. Nichts verlässt diese Instanz ohne eine
            ausdrückliche Aktion der Spielleitung.
          </li>
        </ul>
        <p>Die Stimme von Agenten/NPCs ist synthetisch und enthält keine personenbezogenen Daten der Teilnehmenden.</p>
      </Section>

      <Section heading="5. Empfänger und eingesetzte Dienstleister">
        <p>
          Zur Erbringung des Dienstes werden Inhalte an die folgenden Anbieter übermittelt.
          Welche davon tatsächlich zum Einsatz kommen, hängt von der Konfiguration Ihrer
          Kampagne ab (jede Spielleitung kann eigene Zugangsschlüssel hinterlegen — dann
          besteht das Vertragsverhältnis zwischen ihr und dem jeweiligen Anbieter):
        </p>
        <ul>
          <li>
            <strong>Discord</strong> — Sprach- und Textkanäle, Anmeldung. Ohne Discord ist
            der Dienst nicht nutzbar.
          </li>
          <li>
            <strong>ElevenLabs</strong> (USA) — Spracherkennung und Sprachsynthese:
            erhält Audioausschnitte bzw. den zu sprechenden Text.
          </li>
          <li>
            <strong>Groq</strong> und weitere Sprachmodell-Anbieter (z. B. Google Gemini,
            OpenAI-kompatible Endpunkte) — erhalten den Gesprächskontext (Transkripttext,
            Kampagnennotizen), um Antworten zu erzeugen.
          </li>
          <li>
            <strong>Google Gemini</strong> — Bilderzeugung für Kampagnenillustrationen:
            erhält die jeweilige Bildbeschreibung.
          </li>
          <li>
            <strong>Cloudflare</strong> (soweit eingesetzt) — Tunnel und TLS-Terminierung
            für den Web-Zugang: verarbeitet dabei Verbindungsdaten (u. a. IP-Adresse).
          </li>
        </ul>
        <p>
          Bei Anbietern mit Sitz in den USA erfolgt die Übermittlung auf Grundlage von
          Standardvertragsklauseln bzw. — soweit zertifiziert — des EU-US Data Privacy
          Framework (Art. 45/46 DSGVO). Ein den europäischen Standards entsprechendes
          Datenschutzniveau kann trotz dieser Garantien nicht in jedem Fall zugesichert
          werden; insbesondere sind Zugriffe durch dortige Behörden nicht auszuschließen.
        </p>
      </Section>

      <Section heading="6. Speicherort">
        <p>
          Die Datenbank dieser Instanz — Konten, Kampagnen, Transkripte, Einbettungen und
          gespeicherte Highlights — liegt an folgendem Ort:{" "}
          <OperatorValue value={OPERATOR.hostingLocation} />. Zugangsschlüssel für externe
          Anbieter werden verschlüsselt gespeichert. Eine Übermittlung an die unter Ziffer
          5 genannten Anbieter findet nur zur Erbringung der jeweiligen Funktion statt.
        </p>
      </Section>

      <Section heading="7. Speicherdauer">
        <ul>
          <li>
            <strong>Konto- und Kampagnendaten:</strong> bis zur Löschung durch Sie bzw.
            Ihre Spielleitung oder bis zur Löschung des Kontos.
          </li>
          <li>
            <strong>Transkripte und Einbettungen:</strong> bis die Spielleitung die
            Sitzung oder die Kampagne löscht — das Löschen einer Kampagne entfernt ihre
            Transkripte, Einbettungen und Highlights vollständig.
          </li>
          <li>
            <strong>Audio-Ringspeicher:</strong> maximal ca. 120 Sekunden, Verwerfung
            spätestens am Sitzungsende.
          </li>
          <li>
            <strong>Clip-Kandidaten:</strong> bis zur Durchsicht, spätestens 7 Tage.
          </li>
          <li>
            <strong>Server-Logdateien:</strong> kurzfristig zur Fehlersuche und
            Missbrauchsabwehr (Art. 6 Abs. 1 lit. f DSGVO).
          </li>
        </ul>
      </Section>

      <Section heading="8. Ihre Rechte">
        <p>
          Sie haben das Recht auf Auskunft (Art. 15), Berichtigung (Art. 16), Löschung
          (Art. 17), Einschränkung der Verarbeitung (Art. 18), Datenübertragbarkeit
          (Art. 20) sowie ein <strong>Widerspruchsrecht</strong> gegen Verarbeitungen auf
          Grundlage berechtigter Interessen (Art. 21 DSGVO). Erteilte Einwilligungen —
          etwa für die Aufnahmefunktion — können Sie jederzeit mit Wirkung für die Zukunft
          widerrufen (Art. 7 Abs. 3 DSGVO), im Spiel über die Schaltfläche
          <strong> Revoke</strong>.
        </p>
        <p>
          Wenden Sie sich dafür an <OperatorValue value={OPERATOR.email} />. Unabhängig
          davon steht Ihnen ein Beschwerderecht bei einer Aufsichtsbehörde zu (Art. 77
          DSGVO), zuständig ist:{" "}
          <OperatorValue value={OPERATOR.supervisoryAuthority} />.
        </p>
      </Section>

      <Section heading="9. Änderungen dieser Erklärung">
        <p>
          Diese Instanz befindet sich in einer offenen Beta. Wir passen diese Erklärung an,
          wenn sich die Funktionen oder die eingesetzten Anbieter ändern. Es gilt jeweils
          die hier veröffentlichte Fassung.
        </p>
      </Section>
    </LegalPage>
  );
}
