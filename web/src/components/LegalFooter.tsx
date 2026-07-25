import "./legalFooter.css";

// The legal footer (#518): Impressum, Datenschutzerklärung and
// Nutzungsbedingungen, reachable from EVERY screen — including the ones a
// visitor sees before signing in (login, signup) — because that is exactly
// where German law expects them to be reachable, and where a player deciding
// whether to join a transcribed table needs them.
//
// The pages themselves are unauthenticated SPA routes, so these links work with
// or without a session. They are plain anchors, not router <Link>s: the footer
// renders on screens that live outside a RouterProvider (and inside unit tests
// that mount a single screen), and a full page load into a static document is
// no loss.

export function LegalFooter({ className }: { className?: string }) {
  return (
    <footer className={className ? `gx-legalfooter ${className}` : "gx-legalfooter"}>
      <nav aria-label="Rechtliches">
        <a href="/imprint">Impressum</a>
        <a href="/privacy">Datenschutz</a>
        <a href="/terms">Nutzungsbedingungen</a>
      </nav>
    </footer>
  );
}
