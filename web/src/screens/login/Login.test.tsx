import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";

import { Login } from "./Login";

describe("Login", () => {
  it("offers Continue with Discord pointing at the OAuth start", () => {
    render(<Login />);
    const link = screen.getByRole("link", { name: /continue with discord/i });
    expect(link).toHaveAttribute("href", "/auth/discord/login");
  });

  it("renders Google and GitHub as disabled coming-soon slots", () => {
    render(<Login />);
    expect(screen.getByRole("button", { name: /google/i })).toBeDisabled();
    expect(screen.getByRole("button", { name: /github/i })).toBeDisabled();
    expect(screen.getAllByText(/coming soon/i).length).toBeGreaterThanOrEqual(2);
  });

  it("shows a friendly not-authorized banner when notAuthorized is set", () => {
    render(<Login notAuthorized />);
    const banner = screen.getByRole("alert");
    expect(banner).toHaveTextContent(/allowlist/i);
    // The Discord link stays available so the operator can retry with the right account.
    expect(screen.getByRole("link", { name: /continue with discord/i })).toBeInTheDocument();
  });

  it("renders no banner on a normal first visit", () => {
    render(<Login />);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});
