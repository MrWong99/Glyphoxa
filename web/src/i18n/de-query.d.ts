// A message fragment imported for its German half only (see ./de.ts for why
// the query suffix exists). The suffix hides the real module from the type
// checker, so its shape is restated here.
declare module "*?de" {
  export const de: Record<string, string>;
}
