// The URL shapes of an invitation link (ADR-0065), in one place. All three are
// a Go↔TS contract with the OAuth carve-out (internal/auth) — the server mints
// tokens for the path, bounces a refused join back to it, and reads `join` and
// `aup` off the OAuth start — so router.test.tsx pins them.

/** The join page of a token: `/join/<token>`. */
export function joinPath(token: string): string {
  return `/join/${encodeURIComponent(token)}`;
}

/**
 * The full link to hand to the invited person, on the origin the minter is
 * looking at. The token is readable exactly once — in the Create* response —
 * so this is built there and nowhere else.
 */
export function invitationLink(token: string, origin: string = window.location.origin): string {
  return origin + joinPath(token);
}

/**
 * The Discord OAuth start that redeems a token. `aup=1` says the visitor
 * acknowledged the Nutzungsbedingungen and the Datenschutzerklärung; the
 * server refuses a join without it in every Admission Mode.
 */
export function joinOAuthStart(token: string): string {
  return `/auth/discord/login?join=${encodeURIComponent(token)}&aup=1`;
}

/** A Discord snowflake as typed: decimal digits, 17 to 20 of them. */
export type SnowflakeProblem = "digits" | "length";

/**
 * Checks a Discord User ID as typed. Returns what is wrong with it, or null
 * when it is well-formed (or empty — whether empty is allowed is the form's
 * call). The server only insists on decimal digits; the length check catches
 * the common slip of pasting a username or half an ID before a round trip.
 */
export function snowflakeProblem(value: string): SnowflakeProblem | null {
  const v = value.trim();
  if (v === "") return null;
  if (!/^\d+$/.test(v)) return "digits";
  if (v.length < 17 || v.length > 20) return "length";
  return null;
}
