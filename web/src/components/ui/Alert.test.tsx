import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { Alert, type AlertTone } from "./Alert";
import { Button } from "./Button";

describe("Alert", () => {
  it.each<[AlertTone, string]>([
    ["danger", "alert"],
    ["warning", "alert"],
    ["info", "status"],
    ["success", "status"],
  ])("tone %s is announced as role=%s and carries its tone class", (tone, role) => {
    render(<Alert tone={tone}>Something happened</Alert>);
    const alert = screen.getByRole(role);
    expect(alert).toHaveTextContent("Something happened");
    expect(alert).toHaveClass("gx-alert", `gx-alert--${tone}`);
  });

  it("defaults to the info tone", () => {
    render(<Alert>FYI</Alert>);
    expect(screen.getByRole("status")).toHaveClass("gx-alert--info");
  });

  it("lets the caller override the role for static page content", () => {
    render(
      <Alert tone="warning" role="none">
        Always-on notice
      </Alert>,
    );
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.getByText("Always-on notice")).toBeInTheDocument();
  });

  it("renders a title, body and action", () => {
    const onRetry = vi.fn();
    render(
      <Alert
        tone="danger"
        title="Couldn't save"
        action={
          <Button size="sm" onClick={onRetry}>
            Try again
          </Button>
        }
      >
        The server refused the change.
      </Alert>,
    );
    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("Couldn't save");
    expect(alert).toHaveTextContent("The server refused the change.");
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it("shows a dismiss button only when it can be dismissed", () => {
    const onDismiss = vi.fn();
    const { rerender } = render(<Alert>Note</Alert>);
    expect(screen.queryByRole("button", { name: "Dismiss" })).toBeNull();

    rerender(<Alert onDismiss={onDismiss}>Note</Alert>);
    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });

  it("keeps its icon out of the accessible text", () => {
    render(<Alert tone="success">Saved</Alert>);
    const icon = screen.getByRole("status").querySelector(".gx-alert__icon");
    expect(icon).toHaveAttribute("aria-hidden", "true");
  });
});
