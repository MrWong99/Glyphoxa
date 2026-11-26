import { useEffect } from "react";

// The browser-tab title, written in one shape everywhere:
//
//   "<Page> · <Context> · Glyphoxa"     World wiki · The Sunless Citadel · Glyphoxa
//   "<Page> · Glyphoxa"                 Sign in · Glyphoxa
//   "Glyphoxa"                          nothing more specific to say
//
// The most specific part comes first, because a narrow tab shows only the
// start of its title — with several Glyphoxa tabs open, an all-"Glyphoxa" tab
// strip is a guessing game.
//
// The shell uses it for the app screens, and the public pages (landing, login,
// the legal documents) for themselves: before this hook nothing outside the
// shell wrote the title, so every public page shared one tab name and a
// search result or a shared link showed the same bare word for all of them.

export const PRODUCT_NAME = "Glyphoxa";

/** Joins the parts that are present, most specific first, and ends on the product name. */
export function documentTitle(...parts: (string | null | undefined)[]): string {
  return [...parts.filter(Boolean), PRODUCT_NAME].join(" · ");
}

/**
 * Names the browser tab for as long as the calling component is mounted, and
 * hands it back to the bare product name when it unmounts — so a page that is
 * left (a lapsed session bounced to the login page, say) never leaves its
 * title on whatever renders next.
 */
export function useDocumentTitle(...parts: (string | null | undefined)[]): void {
  const title = documentTitle(...parts);
  useEffect(() => {
    document.title = title;
  }, [title]);
  useEffect(
    () => () => {
      document.title = PRODUCT_NAME;
    },
    [],
  );
}
