import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";

import { AddBotLink, botAuthorizeUrl } from "./AddBotLink";

// The bot-authorization URL is a fixed contract (#110): Discord's authorize
// endpoint, scope=bot + applications.commands (so /glyphoxa slash commands
// appear in a fresh guild), and permissions 3146752 = View Channel (0x400) +
// Connect (0x100000) + Speak (0x200000) — the set a voice-join needs.
const PINNED = "https://discord.com/oauth2/authorize?client_id=123&scope=bot%20applications.commands&permissions=3146752";

describe("botAuthorizeUrl", () => {
  it("composes the pinned authorize URL from the application id", () => {
    expect(botAuthorizeUrl("123")).toBe(PINNED);
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

  it("states adding the bot is a separate, prerequisite step", () => {
    render(<AddBotLink applicationId="123" />);
    // Copy must distinguish this from saving the IDs and mark it a prerequisite.
    expect(screen.getByText(/separate step from saving the ids/i)).toBeInTheDocument();
    expect(screen.getByText(/before a voice session can join/i)).toBeInTheDocument();
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
