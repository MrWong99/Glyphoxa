import type { ReactNode } from "react";
import { Dices } from "lucide-react";

import { LegalFooter } from "@/components/LegalFooter";
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

export function LegalPage({
  title,
  lede,
  children,
}: {
  title: string;
  lede?: string;
  children: ReactNode;
}) {
  const todos = operatorTodos();

  return (
    <div className="gx-legal">
      <article className="gx-legal__doc">
        <a className="gx-legal__brand" href="/login">
          <span className="gx-legal__sigil">
            <Dices size={17} />
          </span>
          <span className="gx-gradient-text">Glyphoxa</span>
        </a>

        <h1 className="gx-legal__title">{title}</h1>
        {lede && <p className="gx-legal__lede">{lede}</p>}

        {todos.length > 0 && (
          <p className="gx-legal__todo" role="alert">
            <strong>Hinweis an den Betreiber:</strong> Diese Seite enthält noch
            unausgefüllte Platzhalter ({todos.join(", ")}). Sie müssen ausgefüllt werden,
            bevor die Instanz öffentlich erreichbar ist — siehe
            <code> web/src/screens/legal/operator.ts</code> und
            <code> docs/deploy/legal-pages.md</code>.
          </p>
        )}

        <p className="gx-legal__updated">
          Stand: <OperatorValue value={OPERATOR.lastUpdated} />
        </p>

        {children}

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

/** Section is one numbered block of a legal document. */
export function Section({ heading, children }: { heading: string; children: ReactNode }) {
  return (
    <section className="gx-legal__section">
      <h2>{heading}</h2>
      {children}
    </section>
  );
}
