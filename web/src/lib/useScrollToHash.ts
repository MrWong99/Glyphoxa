import { useEffect } from "react";

// useScrollToHash opens a shared link to a section (/privacy#speicherdauer,
// /#how) AT that section.
//
// The browser (and the router) look for the hash's target as the page loads,
// but the public pages render it later: the legal documents are lazily loaded
// chunks, and the landing waits for the session probe. By then nobody looks
// again, so the visitor lands at the top of a long page. Called from the
// component that renders the sections, this looks once, after the first
// commit, when the target exists. (A sticky bar's height is the target's
// scroll-margin-top, which scrollIntoView honours.)
export function useScrollToHash(): void {
  useEffect(() => {
    const raw = window.location.hash.slice(1);
    if (!raw) return;
    let id = raw;
    try {
      id = decodeURIComponent(raw);
    } catch {
      // A malformed escape: look the raw text up instead.
    }
    document.getElementById(id)?.scrollIntoView();
  }, []);
}
