import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { I18nProvider } from "@/i18n";
import { ErrorState } from "./ErrorState";

describe("ErrorState", () => {
  it("announces itself with the shared title and explanation", () => {
    render(<ErrorState />);
    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("Couldn't load this");
    expect(alert).toHaveTextContent("Something went wrong while loading. Try again in a moment.");
  });

  it("takes its own title and description, and can drop the description", () => {
    const { rerender } = render(<ErrorState title="Couldn't load the cast" description="The roster is unavailable." />);
    expect(screen.getByRole("alert")).toHaveTextContent("Couldn't load the cast");
    expect(screen.getByRole("alert")).toHaveTextContent("The roster is unavailable.");

    rerender(<ErrorState title="Couldn't load the cast" description={null} />);
    expect(screen.getByRole("alert").querySelector(".gx-empty__desc")).toBeNull();
  });

  it("folds the raw message away under a disclosure", () => {
    render(<ErrorState message="rpc error: upstream connect timeout" />);
    const details = screen.getByText("Technical details").closest("details")!;
    // Folded by default: the text is in the DOM for whoever opens it, but the
    // disclosure starts closed.
    expect(details.open).toBe(false);
    expect(details).toHaveTextContent("rpc error: upstream connect timeout");
  });

  it("has no disclosure without a message and no button without onRetry", () => {
    render(<ErrorState />);
    expect(screen.queryByText("Technical details")).toBeNull();
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("calls onRetry from Try again", () => {
    const onRetry = vi.fn();
    render(<ErrorState onRetry={onRetry} />);
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it("shows the retry in flight as a busy, disabled button", () => {
    const onRetry = vi.fn();
    render(<ErrorState onRetry={onRetry} retrying />);
    const button = screen.getByRole("button", { name: "Try again" });
    expect(button).toHaveAttribute("aria-busy", "true");
    expect(button).toBeDisabled();
  });

  // The busy button is disabled, and a browser takes focus off a disabled
  // control (the focus fixup). When the retry fails again the keyboard user
  // gets the button back instead of being left on <body>.
  it("returns focus to Try again when a retry started from it fails again", () => {
    const { rerender } = render(<ErrorState onRetry={() => {}} />);
    const button = screen.getByRole("button", { name: "Try again" });
    button.focus();
    fireEvent.click(button);

    rerender(<ErrorState onRetry={() => {}} retrying />);
    button.blur(); // what the browser does to the now-disabled button
    expect(document.body).toHaveFocus();

    rerender(<ErrorState onRetry={() => {}} />);
    expect(button).toBeEnabled();
    expect(button).toHaveFocus();
  });

  it("leaves focus alone when it moved somewhere else during the retry", () => {
    const { rerender } = render(
      <>
        <input aria-label="search" />
        <ErrorState onRetry={() => {}} />
      </>,
    );
    const button = screen.getByRole("button", { name: "Try again" });
    button.focus();
    fireEvent.click(button);

    rerender(
      <>
        <input aria-label="search" />
        <ErrorState onRetry={() => {}} retrying />
      </>,
    );
    screen.getByLabelText("search").focus();

    rerender(
      <>
        <input aria-label="search" />
        <ErrorState onRetry={() => {}} />
      </>,
    );
    expect(screen.getByLabelText("search")).toHaveFocus();
  });

  it("does not take focus after a retry that was not started from a focused button", () => {
    const { rerender } = render(<ErrorState onRetry={() => {}} />);
    // A pointer press that does not focus the button (Safari), or a retry the
    // caller started itself.
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));

    rerender(<ErrorState onRetry={() => {}} retrying />);
    rerender(<ErrorState onRetry={() => {}} />);
    expect(document.body).toHaveFocus();
  });

  it("speaks German", () => {
    localStorage.setItem("gx-lang", "de");
    try {
      render(
        <I18nProvider>
          <ErrorState onRetry={() => {}} message="boom" />
        </I18nProvider>,
      );
      expect(screen.getByRole("alert")).toHaveTextContent("Das konnte nicht geladen werden");
      expect(screen.getByRole("button", { name: "Erneut versuchen" })).toBeInTheDocument();
      expect(screen.getByText("Technische Details")).toBeInTheDocument();
    } finally {
      localStorage.removeItem("gx-lang");
    }
  });
});
