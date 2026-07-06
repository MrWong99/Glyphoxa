// discordLink — pure, network-free parsing of a Discord channel deep-link (#101,
// ADR-0039). A voice channel's right-click "Copy Link" yields
// discord.com/channels/{guildId}/{channelId}, carrying BOTH snowflakes; the
// Configuration screen pastes that here to autofill the two ID fields.

export interface ChannelLink {
  guildId: string;
  channelId: string;
}

// Discord snowflakes are 17–20 digit ids (a 64-bit id rendered as decimal).
const SNOWFLAKE = /^\d{17,20}$/;

// parseChannelLink extracts the guild + voice channel snowflakes from a pasted
// channel deep-link, or returns null when the string is not one. It tolerates a
// missing scheme, http/https, the ptb./canary. client subdomains, a trailing
// slash, a query string, and surrounding whitespace — every variant resolves to
// the same two ids. Anything else (a random string, a @me DM link, a non-Discord
// host, short/invalid ids) yields null so the caller can surface a hint.
export function parseChannelLink(input: string): ChannelLink | null {
  const trimmed = input.trim();
  if (!trimmed) return null;

  // A scheme-less paste (the common "discord.com/channels/…") needs one to parse
  // as a URL; add https:// only when no scheme is present.
  const withScheme = /^[a-z][a-z0-9+.-]*:\/\//i.test(trimmed) ? trimmed : `https://${trimmed}`;

  let url: URL;
  try {
    url = new URL(withScheme);
  } catch {
    return null;
  }

  // Host must be discord.com or one of its client subdomains (ptb./canary./www.).
  const host = url.hostname.toLowerCase();
  if (host !== "discord.com" && !host.endsWith(".discord.com")) return null;

  // /channels/{guild}/{channel}[/…] — filter drops the empty segment a trailing
  // slash leaves behind. The query string lives in url.search, so it's ignored.
  const segments = url.pathname.split("/").filter(Boolean);
  if (segments[0] !== "channels") return null;
  const [, guildId, channelId] = segments;
  if (!SNOWFLAKE.test(guildId ?? "") || !SNOWFLAKE.test(channelId ?? "")) return null;

  return { guildId, channelId };
}
