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
});
