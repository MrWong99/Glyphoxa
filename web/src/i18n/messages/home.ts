// Strings for the campaign Overview dashboard and the Suggestions inbox (home.* / inbox.*).
//
// Registered empty with the shell skeleton (ADR-0063 amendment) so the package
// that fills it only ever edits THIS file — never i18n/index.tsx or i18n/de.ts,
// which several packages would otherwise all have to touch. Add each key to
// `en` and `de` together: the `de` annotation makes a missing German string a
// compile error, and i18n.test.tsx checks the assembled catalogs key for key.

export const en = {} as const;

export const de: Record<keyof typeof en, string> = {};
