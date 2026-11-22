import { Children, Fragment, isValidElement, useId, type ReactNode } from "react";
import { Dices } from "lucide-react";

import { LegalFooter } from "@/components/LegalFooter";
import { LanguageSwitcher } from "@/components/LanguageSwitcher";
import { useI18n } from "@/i18n";
import { useDocumentTitle } from "@/lib/useDocumentTitle";
import { OPERATOR, isPlaceholder, operatorTodos } from "./operator";

import "./legal.css";

// The shared chrome of the three legal pages (#518): Impressum,
// Datenschutzerklärung and Nutzungsbedingungen. They are plain static documents
// served UNAUTHENTICATED — a visitor who has not signed in (and a player who
// never will) must be able to read them, which is the whole point of the footer
// that links here from every screen.
//
// No app shell, no AuthGate, no RPCs: these pages must render even when the
// backend is unhappy.
//
// Around the document: the brand (back to the start page), the display
// language, the three documents cross-linked with the open one marked, a
// one-line notice that the German text is authoritative (when the display
// language is not German), and an "Inhalt" list of the document's sections.
// The sections carry stable ids derived from their headings, so a link into a
// document (#speicherdauer) survives renumbering.

/** The three documents, in the order the footer names them. */
export type LegalDoc = "imprint" | "privacy" | "terms";

// The documents' German proper names stay verbatim in every display language
// (#518), so this list holds names, not message keys.
const DOCS: { doc: LegalDoc; href: string; name: string }[] = [
  { doc: "imprint", href: "/imprint", name: "Impressum" },
  { doc: "privacy", href: "/privacy", name: "Datenschutzerklärung" },
  { doc: "terms", href: "/terms", name: "Nutzungsbedingungen" },
];

export function LegalPage({
  doc,
  title,
  lede,
  children,
}: {
  doc: LegalDoc;
  title: string;
  lede?: string;
  children: ReactNode;
}) {
  // Only the page CHROME localizes; the documents themselves (title, lede,
  // body, the "Inhalt" list, and the operator TODO banner below) are authored
  // German legal text and stay verbatim regardless of the display language —
  // and are marked lang="de", so a screen reader reads them in German.
  const { t, lang } = useI18n();
  const todos = operatorTodos();
  useDocumentTitle(title);
  const sections = collectSections(children);
  const tocId = useId();

  return (
    <div className="gx-legal">
      <article className="gx-legal__doc">
        <div className="gx-legal__bar">
          <a className="gx-legal__brand" href="/" aria-label={t("auth.legalHome")}>
            <span className="gx-legal__sigil" aria-hidden="true">
              <Dices size={17} />
            </span>
            <span className="gx-gradient-text">Glyphoxa</span>
          </a>
          <LanguageSwitcher className="gx-legal__lang" />
        </div>

        <nav className="gx-legal__docs" aria-label={t("auth.legalDocsNav")}>
          {DOCS.map((d) => (
            <a key={d.doc} href={d.href} lang="de" aria-current={d.doc === doc ? "page" : undefined}>
              {d.name}
            </a>
          ))}
        </nav>

        {lang !== "de" && <p className="gx-legal__notice">{t("auth.legalGermanNotice")}</p>}

        <h1 className="gx-legal__title" lang="de">
          {title}
        </h1>
        {lede && (
          <p className="gx-legal__lede" lang="de">
            {lede}
          </p>
        )}

        {todos.length > 0 && (
          <p className="gx-legal__todo" role="alert" lang="de">
            <strong>Hinweis an den Betreiber:</strong> Diese Seite enthält noch
            unausgefüllte Platzhalter ({todos.join(", ")}). Sie müssen ausgefüllt werden,
            bevor die Instanz öffentlich erreichbar ist — siehe
            <code> web/src/screens/legal/operator.ts</code> und
            <code> docs/deploy/legal-pages.md</code>.
          </p>
        )}

        <p className="gx-legal__updated">
          {t("auth.legalUpdatedLabel")} <OperatorValue value={OPERATOR.lastUpdated} />
        </p>

        {sections.length > 1 && (
          <nav className="gx-legal__toc" aria-labelledby={tocId} lang="de">
            <p className="gx-legal__toc-title" id={tocId}>
              Inhalt
            </p>
            <ol>
              {sections.map((s) => (
                <li key={s.id}>
                  <a href={`#${s.id}`}>{s.heading}</a>
                </li>
              ))}
            </ol>
          </nav>
        )}

        <div className="gx-legal__body" lang="de">
          {children}
        </div>

        <LegalFooter className="gx-legal__footer" />
      </article>
    </div>
  );
}

/**
 * OperatorValue renders one operator-supplied field, marking it visibly when it
 * is still a template placeholder — an unfilled Impressum should look wrong,
 * not merely read wrong.
 */
export function OperatorValue({ value }: { value: string }) {
  if (isPlaceholder(value)) {
    return <span className="gx-legal__placeholder">{value}</span>;
  }
  return <>{value}</>;
}

/**
 * sectionId turns a heading into the section's anchor: German letters spelled
 * out (ä → ae, ß → ss), the leading number dropped ("7. Speicherdauer" →
 * "speicherdauer"), everything else that is not a letter or digit folded into
 * single hyphens. Derived from the words, so a renumbered section keeps its
 * address.
 */
export function sectionId(heading: string): string {
  return heading
    .toLowerCase()
    .replace(/ä/g, "ae")
    .replace(/ö/g, "oe")
    .replace(/ü/g, "ue")
    .replace(/ß/g, "ss")
    .normalize("NFD")
    .replace(/[̀-ͯ]/g, "")
    .replace(/^\s*\d+\.\s*/, "")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}

/** Section is one numbered block of a legal document. */
export function Section({ heading, id, children }: { heading: string; id?: string; children: ReactNode }) {
  return (
    <section className="gx-legal__section" id={id ?? sectionId(heading)}>
      <h2>{heading}</h2>
      {children}
    </section>
  );
}

// collectSections finds the document's Sections among the page's children —
// directly, or inside fragments and conditionals (the Impressum shows some only
// when the operator filled the field) — in document order. That is how the
// "Inhalt" list is in the very first render, not patched in after mount.
function collectSections(children: ReactNode): { id: string; heading: string }[] {
  const found: { id: string; heading: string }[] = [];
  const walk = (nodes: ReactNode) => {
    Children.forEach(nodes, (node) => {
      if (!isValidElement(node)) return;
      if (node.type === Section) {
        const props = node.props as { heading: string; id?: string };
        found.push({ id: props.id ?? sectionId(props.heading), heading: props.heading });
      } else if (node.type === Fragment) {
        walk((node.props as { children?: ReactNode }).children);
      }
    });
  };
  walk(children);
  return found;
}
