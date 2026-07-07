import { describe, it, expect } from "vitest";

import { parseChannelLink } from "./discordLink";

// A voice channel's right-click "Copy Link" yields discord.com/channels/{guild}/{channel},
// carrying BOTH snowflakes. The parser is pure and client-side (#101, ADR-0039):
// it extracts the two ids or returns null, never touching the network.
const GUILD = "472093001100472093";
const CHANNEL = "987654321098765432";

describe("parseChannelLink", () => {
  it("parses a canonical https channel deep-link into both snowflakes", () => {
    expect(parseChannelLink(`https://discord.com/channels/${GUILD}/${CHANNEL}`)).toEqual({
      guildId: GUILD,
      channelId: CHANNEL,
    });
  });

  it("tolerates scheme, subdomain, trailing-slash and query-string variants to the SAME ids", () => {
    const variants = [
      `https://discord.com/channels/${GUILD}/${CHANNEL}`,
      `http://discord.com/channels/${GUILD}/${CHANNEL}`,
      `discord.com/channels/${GUILD}/${CHANNEL}`,
      `ptb.discord.com/channels/${GUILD}/${CHANNEL}`,
      `https://canary.discord.com/channels/${GUILD}/${CHANNEL}`,
      `https://discord.com/channels/${GUILD}/${CHANNEL}/`,
      `https://discord.com/channels/${GUILD}/${CHANNEL}?foo=bar`,
      `  https://discord.com/channels/${GUILD}/${CHANNEL}  `,
      // Discord's embed-suppressed copy wraps the URL in angle brackets.
      `<https://discord.com/channels/${GUILD}/${CHANNEL}>`,
      // Legacy discordapp.com host — old links still resolve.
      `https://discordapp.com/channels/${GUILD}/${CHANNEL}`,
      `discordapp.com/channels/${GUILD}/${CHANNEL}`,
    ];
    for (const link of variants) {
      expect(parseChannelLink(link)).toEqual({ guildId: GUILD, channelId: CHANNEL });
    }
  });

  it("returns null for a string that is not a channel deep-link", () => {
    expect(parseChannelLink("hello world")).toBeNull();
    expect(parseChannelLink("")).toBeNull();
    expect(parseChannelLink("https://discord.com/channels/@me/123456789012345678")).toBeNull();
    expect(parseChannelLink("https://discord.com/channels/12345/67890")).toBeNull();
    expect(parseChannelLink("https://example.com/channels/" + GUILD + "/" + CHANNEL)).toBeNull();
    expect(parseChannelLink("https://discord.com/channels/" + GUILD)).toBeNull();
  });
});
