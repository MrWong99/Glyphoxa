import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { I18nProvider } from "@/i18n";
import { Skeleton } from "./Skeleton";

describe("Skeleton", () => {
  it("is a busy status region named with the shared loading label", () => {
    render(<Skeleton />);
    const status = screen.getByRole("status", { name: "Loading…" });
    expect(status).toHaveAttribute("aria-busy", "true");
  });

  it("renders the requested number of lines, the last one short", () => {
    render(<Skeleton lines={4} />);
    const lines = screen.getByRole("status").querySelectorAll(".gx-skeleton--line");
    expect(lines).toHaveLength(4);
    expect(lines[3]).toHaveAttribute("data-short", "true");
    expect(lines[0]).not.toHaveAttribute("data-short");
    // The bars are decoration: nothing inside the region is exposed.
    for (const line of lines) expect(line).toHaveAttribute("aria-hidden", "true");
  });

  it("does not shorten a single line", () => {
    render(<Skeleton lines={1} />);
    expect(screen.getByRole("status").querySelector("[data-short]")).toBeNull();
  });

  it("Row renders list rows with an optional avatar", () => {
    const { rerender } = render(<Skeleton.Row count={5} />);
    const status = screen.getByRole("status", { name: "Loading…" });
    expect(status.querySelectorAll(".gx-skeleton-row")).toHaveLength(5);
    expect(status.querySelectorAll(".gx-skeleton--avatar")).toHaveLength(5);

    rerender(<Skeleton.Row count={2} avatar={false} />);
    expect(screen.getByRole("status").querySelectorAll(".gx-skeleton--avatar")).toHaveLength(0);
  });

  it("Block takes an aspect ratio or a fixed height", () => {
    const { rerender } = render(<Skeleton.Block ratio="4 / 3" />);
    const block = () => screen.getByRole("status").querySelector<HTMLElement>(".gx-skeleton--block")!;
    expect(block().style.aspectRatio).toBe("4 / 3");

    rerender(<Skeleton.Block height={120} />);
    expect(block().style.height).toBe("120px");
    expect(block().style.aspectRatio).toBe("");
  });

  it("announces once when shapes are composed inside one Skeleton", () => {
    render(
      <Skeleton label="Loading the cast">
        <Skeleton.Block ratio="1" />
        <Skeleton.Row count={2} />
      </Skeleton>,
    );
    expect(screen.getAllByRole("status")).toHaveLength(1);
    expect(screen.getByRole("status", { name: "Loading the cast" })).toBeInTheDocument();
  });

  it("follows the display language", () => {
    localStorage.setItem("gx-lang", "de");
    try {
      render(
        <I18nProvider>
          <Skeleton />
        </I18nProvider>,
      );
      expect(screen.getByRole("status", { name: "Wird geladen…" })).toBeInTheDocument();
    } finally {
      localStorage.removeItem("gx-lang");
    }
  });
});
