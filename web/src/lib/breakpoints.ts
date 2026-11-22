// The app's three layout breakpoints, in CSS pixels. All of them are
// max-width: a rule or a matchMedia query written with one applies AT and
// BELOW that width. They mirror the block at the end of styles/tokens.css (a
// CSS custom property cannot be used inside a media query, so the stylesheet
// repeats the numbers; breakpoints.test.ts keeps the two in step).
//
//   stack   1100  two-column editors (list + detail) stack into one column
//   drawer   880  the sidebar becomes an off-canvas drawer; sheets dock to
//                 the bottom edge instead of the right
//   phone    640  tightest page gutters, stacked header actions
export const BREAKPOINTS = {
  stack: 1100,
  drawer: 880,
  phone: 640,
} as const;

export type Breakpoint = keyof typeof BREAKPOINTS;

// atMost returns the media query that is true at and below a breakpoint, in
// the exact spelling the stylesheets use: atMost("drawer") is
// "(max-width: 880px)".
export function atMost(breakpoint: Breakpoint): string {
  return `(max-width: ${BREAKPOINTS[breakpoint]}px)`;
}

// isAtMost reads the breakpoint once, for initial state. It answers false
// where there is no window or no matchMedia (server render, jsdom), which is
// the desktop layout: the same default the shell's drawer already assumes.
export function isAtMost(breakpoint: Breakpoint): boolean {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") return false;
  return window.matchMedia(atMost(breakpoint)).matches;
}

// watchBreakpoint calls onChange whenever the viewport crosses a breakpoint
// and returns the function that stops listening. Use it from an effect; it is
// a no-op (returning a no-op) where matchMedia is absent.
export function watchBreakpoint(
  breakpoint: Breakpoint,
  onChange: (atMost: boolean) => void,
): () => void {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") return () => {};
  const query = window.matchMedia(atMost(breakpoint));
  const listener = (event: MediaQueryListEvent) => onChange(event.matches);
  query.addEventListener("change", listener);
  return () => query.removeEventListener("change", listener);
}
