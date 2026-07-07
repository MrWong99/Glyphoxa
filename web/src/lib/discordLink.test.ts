import { describe, it, expect } from "vitest";

import { parseChannelLink, parseDiscordLink } from "./discordLink";

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

// parseDiscordLink is the union front door (#105): a channel deep-link OR an
// invite link. An invite carries only a code (discord.gg/{code} or
// discord.com/invite/{code}); the guild + channels are resolved server-side.
const INVITE_CODE = "abc123XYZ-";

describe("parseDiscordLink", () => {
  it("routes a channel deep-link to the channel branch (checked first)", () => {
    expect(parseDiscordLink(`https://discord.com/channels/${GUILD}/${CHANNEL}`)).toEqual({
      kind: "channel",
      guildId: GUILD,
      channelId: CHANNEL,
    });
    // The channel regression suite's tolerances still hold through the union.
    expect(parseDiscordLink(`ptb.discord.com/channels/${GUILD}/${CHANNEL}/?jump=1`)).toEqual({
      kind: "channel",
      guildId: GUILD,
      channelId: CHANNEL,
    });
  });

  it("parses invite links to the bare code across scheme/host/wrapper variants", () => {
    const variants = [
      `https://discord.gg/${INVITE_CODE}`,
      `http://discord.gg/${INVITE_CODE}`,
      `discord.gg/${INVITE_CODE}`,
      `https://discord.gg/${INVITE_CODE}/`,
      `https://discord.gg/${INVITE_CODE}?utm=x`,
      `  discord.gg/${INVITE_CODE}  `,
      `<https://discord.gg/${INVITE_CODE}>`,
      `https://discord.com/invite/${INVITE_CODE}`,
      `discord.com/invite/${INVITE_CODE}`,
      `https://discordapp.com/invite/${INVITE_CODE}`,
      `https://ptb.discord.com/invite/${INVITE_CODE}`,
    ];
    for (const link of variants) {
      expect(parseDiscordLink(link)).toEqual({ kind: "invite", code: INVITE_CODE });
    }
  });

  it("skips a leading 'invite' path segment on the discord.gg host", () => {
    // A share sheet sometimes yields discord.gg/invite/{code}; segment[0] is the
    // literal 'invite', not the code. The parser must reach past it (or the whole
    // valid invite gets resolved as the code 'invite' → a false invalid/expired).
    expect(parseDiscordLink(`https://discord.gg/invite/${INVITE_CODE}`)).toEqual({
      kind: "invite",
      code: INVITE_CODE,
    });
    expect(parseDiscordLink(`discord.gg/invite/${INVITE_CODE}`)).toEqual({
      kind: "invite",
      code: INVITE_CODE,
    });
    // A bare discord.gg/invite with no trailing code is not a link.
    expect(parseDiscordLink("https://discord.gg/invite/")).toBeNull();
  });

  it("returns null for a bare invite host with no code", () => {
    expect(parseDiscordLink("discord.gg/")).toBeNull();
    expect(parseDiscordLink("https://discord.gg/")).toBeNull();
    expect(parseDiscordLink("https://discord.com/invite/")).toBeNull();
  });

  it("returns null for a non-Discord string", () => {
    expect(parseDiscordLink("hello world")).toBeNull();
    expect(parseDiscordLink("")).toBeNull();
    expect(parseDiscordLink("https://example.com/invite/abc123")).toBeNull();
  });
});
