import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";

import { AddBotLink, botAuthorizeUrl } from "./AddBotLink";

// The bot-authorization URL is a fixed contract (#110): Discord's authorize
// endpoint, scope=bot + applications.commands (so /glyphoxa slash commands
// appear in a fresh server), and permissions 3181568 = View Channel (0x400) +
// Send Messages (0x800) + Attach Files (0x8000) + Connect (0x100000) + Speak
// (0x200000): the voice join, the session-start transcription/consent notice and
// Butler text (Send Messages), and shared highlight clips (Attach Files).
const PINNED = "https://discord.com/oauth2/authorize?client_id=123&scope=bot%20applications.commands&permissions=3181568";

describe("botAuthorizeUrl", () => {
  it("composes the pinned authorize URL from the application id", () => {
    expect(botAuthorizeUrl("123")).toBe(PINNED);
  });

  it("grants exactly View Channel, Send Messages, Attach Files, Connect and Speak", () => {
    const perms = BigInt(new URL(botAuthorizeUrl("123")).searchParams.get("permissions") ?? "0");
    const bits = (1n << 10n) | (1n << 11n) | (1n << 15n) | (1n << 20n) | (1n << 21n);
    expect(perms).toBe(bits);
  });

  it("url-encodes the application id", () => {
    expect(botAuthorizeUrl("a b")).toContain("client_id=a%20b");
  });
});

describe("AddBotLink", () => {
  it("renders an authorize anchor opening in a new tab", () => {
    render(<AddBotLink applicationId="123" />);
    const link = screen.getByRole("link", { name: /add glyphoxa to your server/i });
    expect(link).toHaveAttribute("href", PINNED);
    expect(link).toHaveAttribute("target", "_blank");
    expect(link.getAttribute("rel")).toContain("noopener");
  });

  it("states adding the bot is a prerequisite for voice", () => {
    render(<AddBotLink applicationId="123" />);
    // It is step 1 of the Discord card: the bot must be a member before voice can join.
    expect(screen.getByText(/before a voice session can join/i)).toBeInTheDocument();
  });

  it("is secondary by default and primary as step 1", () => {
    const { unmount } = render(<AddBotLink applicationId="123" />);
    expect(screen.getByRole("link", { name: /add glyphoxa to your server/i })).toHaveClass("gx-btn--secondary");
    unmount();
    render(<AddBotLink applicationId="123" variant="primary" />);
    expect(screen.getByRole("link", { name: /add glyphoxa to your server/i })).toHaveClass("gx-btn--primary");
  });

  it("disables the action with a note when no application id is configured", () => {
    render(<AddBotLink applicationId="" />);
    // No broken link: the anchor is absent, a disabled button stands in.
    expect(screen.queryByRole("link", { name: /add glyphoxa to your server/i })).not.toBeInTheDocument();
    const btn = screen.getByRole("button", { name: /add glyphoxa to your server/i });
    expect(btn).toBeDisabled();
    expect(screen.getByText(/no discord application id is configured/i)).toBeInTheDocument();
  });
});
