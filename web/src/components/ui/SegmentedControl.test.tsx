import { useState } from "react";
import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { SegmentedControl } from "./SegmentedControl";

type Mode = "list" | "graph" | "health";

const OPTIONS = [
  { value: "list", label: "List" },
  { value: "graph", label: "Map", count: 12 },
  { value: "health", label: "Checkup", disabled: true },
] as const;

function Harness({ onChange }: { onChange?: (mode: Mode) => void }) {
  const [mode, setMode] = useState<Mode>("list");
  return (
    <SegmentedControl<Mode>
      label="Wiki view"
      value={mode}
      options={OPTIONS}
      onChange={(next) => {
        onChange?.(next);
        setMode(next);
      }}
    />
  );
}

describe("SegmentedControl", () => {
  // The contract the screens' existing view switches (and their tests) rely
  // on: a labelled group of buttons, the chosen one aria-pressed.
  it("is a labelled group of toggle buttons with exactly one pressed", () => {
    render(<Harness />);
    const group = screen.getByRole("group", { name: "Wiki view" });
    const buttons = within(group).getAllByRole("button");
    expect(buttons.map((b) => b.getAttribute("aria-pressed"))).toEqual(["true", "false", "false"]);
    for (const button of buttons) expect(button).toHaveAttribute("type", "button");
  });

  it("changes the selection on click", () => {
    const onChange = vi.fn();
    render(<Harness onChange={onChange} />);
    fireEvent.click(screen.getByRole("button", { name: /Map/ }));
    expect(onChange).toHaveBeenCalledWith("graph");
    expect(screen.getByRole("button", { name: /Map/ })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "List" })).toHaveAttribute("aria-pressed", "false");
  });

  it("does not call onChange for the segment that is already chosen", () => {
    const onChange = vi.fn();
    render(<Harness onChange={onChange} />);
    fireEvent.click(screen.getByRole("button", { name: "List" }));
    expect(onChange).not.toHaveBeenCalled();
  });

  it("never selects a disabled segment", () => {
    const onChange = vi.fn();
    render(<Harness onChange={onChange} />);
    const checkup = screen.getByRole("button", { name: "Checkup" });
    expect(checkup).toBeDisabled();
    fireEvent.click(checkup);
    expect(onChange).not.toHaveBeenCalled();
  });

  it("shows a count beside its label", () => {
    render(<Harness />);
    expect(screen.getByRole("button", { name: "Map 12" })).toBeInTheDocument();
  });

  it("carries size and block as classes", () => {
    render(
      <SegmentedControl
        label="Mode"
        size="sm"
        block
        value="a"
        onChange={() => {}}
        options={[
          { value: "a", label: "A" },
          { value: "b", label: "B" },
        ]}
      />,
    );
    expect(screen.getByRole("group", { name: "Mode" })).toHaveClass("gx-seg", "gx-seg--sm", "gx-seg--block");
  });
});
