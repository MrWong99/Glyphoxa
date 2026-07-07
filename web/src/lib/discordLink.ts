// discordLink — pure, network-free parsing of a pasted Discord link (#101/#105,
// ADR-0039/0047). Two shapes matter to the Configuration screen:
//   - a channel deep-link (discord.com/channels/{guildId}/{channelId}) carries
//     BOTH snowflakes, so it autofills the two ID fields locally;
//   - an invite link (discord.gg/{code} or discord.com/invite/{code}) carries
//     only a code — the guild + its voice channels are resolved server-side by
//     ResolveGuildInvite, because a non-member Bot cannot read them client-side.
// Everything here is client-side and issues no network call.

export interface ChannelLink {
  guildId: string;
  channelId: string;
}

// DiscordLink is the union front door: a resolved channel link, or an invite
// carrying only its code.
export type DiscordLink =
  | { kind: "channel"; guildId: string; channelId: string }
  | { kind: "invite"; code: string };

// Discord snowflakes are 17–20 digit ids (a 64-bit id rendered as decimal).
const SNOWFLAKE = /^\d{17,20}$/;

// An invite code is 2–64 chars of letters, digits, and hyphens (vanity codes
// included) — matching the server's ^[A-Za-z0-9-]{2,64}$ validation (ADR-0047).
const INVITE_CODE = /^[A-Za-z0-9-]{2,64}$/;

// A Discord channel host: discord.com, the legacy discordapp.com, or a client
// subdomain of either (ptb./canary./www.).
function isDiscordHost(host: string): boolean {
  return ["discord.com", "discordapp.com"].some(
    (base) => host === base || host.endsWith(`.${base}`),
  );
}

// The short invite host discord.gg (and any client subdomain of it).
function isInviteHost(host: string): boolean {
  return host === "discord.gg" || host.endsWith(".discord.gg");
}

// toURL normalises a pasted string into a URL: it strips surrounding whitespace,
// unwraps the <…> Discord adds to suppress an embed, adds https:// when the paste
// is scheme-less, and parses. It returns null when the result is not a URL.
function toURL(input: string): URL | null {
  const trimmed = input.trim().replace(/^<(.*)>$/, "$1").trim();
  if (!trimmed) return null;
  const withScheme = /^[a-z][a-z0-9+.-]*:\/\//i.test(trimmed) ? trimmed : `https://${trimmed}`;
  try {
    return new URL(withScheme);
  } catch {
    return null;
  }
}

// parseChannelLink extracts the guild + voice channel snowflakes from a pasted
// channel deep-link, or returns null when the string is not one. It tolerates a
// missing scheme, http/https, the ptb./canary. client subdomains, the legacy
// discordapp.com host, angle brackets (Discord's embed-suppressed <url> copy), a
// trailing slash, a query string, and surrounding whitespace — every variant
// resolves to the same two ids. Anything else (a random string, a @me DM link, a
// non-Discord host, short/invalid ids) yields null so the caller can surface a hint.
export function parseChannelLink(input: string): ChannelLink | null {
  const url = toURL(input);
  if (!url) return null;
  if (!isDiscordHost(url.hostname.toLowerCase())) return null;

  // /channels/{guild}/{channel}[/…] — filter drops the empty segment a trailing
  // slash leaves behind. The query string lives in url.search, so it's ignored.
  const segments = url.pathname.split("/").filter(Boolean);
  if (segments[0] !== "channels") return null;
  const [, guildId, channelId] = segments;
  if (!SNOWFLAKE.test(guildId ?? "") || !SNOWFLAKE.test(channelId ?? "")) return null;

  return { guildId, channelId };
}

// parseInviteCode extracts the bare invite code from a pasted invite link, or
// returns null. It accepts discord.gg/{code} and discord(app).com/invite/{code}
// with the same tolerances as parseChannelLink. A bare host with no code segment
// yields null.
export function parseInviteCode(input: string): string | null {
  const url = toURL(input);
  if (!url) return null;
  const host = url.hostname.toLowerCase();
  const segments = url.pathname.split("/").filter(Boolean);

  let code: string | undefined;
  if (isInviteHost(host)) {
    // discord.gg/{code}, but a share sheet may emit discord.gg/invite/{code} —
    // skip a leading "invite" segment so the code, not the literal "invite", wins.
    code = segments[0] === "invite" ? segments[1] : segments[0];
  } else if (isDiscordHost(host) && segments[0] === "invite") {
    code = segments[1];
  }
  if (!code || !INVITE_CODE.test(code)) return null;
  return code;
}

// parseDiscordLink resolves a paste to either a channel deep-link or an invite,
// or null. The channel branch is checked first: a channel link is fully
// self-describing (both ids), while an invite needs a server round-trip, so a
// paste that is both should take the cheaper local path.
export function parseDiscordLink(input: string): DiscordLink | null {
  const channel = parseChannelLink(input);
  if (channel) return { kind: "channel", ...channel };
  const code = parseInviteCode(input);
  if (code) return { kind: "invite", code };
  return null;
}
