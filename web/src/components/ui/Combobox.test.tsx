import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent, within } from "@testing-library/react";

import { Combobox } from "./Combobox";

const OPTS = [
  { value: "r", label: "Rachel" },
  { value: "m", label: "Marcus" },
  { value: "a", label: "Aria" },
];

describe("Combobox", () => {
  it("filters the options as you type", () => {
    render(<Combobox label="Voice" options={OPTS} value="" onValueChange={() => {}} />);
    // Open the popover.
    fireEvent.click(screen.getByRole("button", { name: "Voice" }));
    // All options visible before filtering.
    expect(screen.getByRole("option", { name: /rachel/i })).toBeInTheDocument();
    // Type to narrow: only the matching voice survives the filter.
    fireEvent.change(screen.getByPlaceholderText(/search/i), { target: { value: "marcus" } });
    expect(screen.getByRole("option", { name: /marcus/i })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /rachel/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /aria/i })).not.toBeInTheDocument();
  });

  it("fires onValueChange on pick and shows the chosen label on the trigger", () => {
    const onChange = vi.fn();
    const { rerender } = render(
      <Combobox label="Voice" options={OPTS} value="" onValueChange={onChange} placeholder="Pick…" />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Voice" }));
    fireEvent.click(screen.getByRole("option", { name: /marcus/i }));
    // The picked option's value (not its label) is reported to the parent.
    expect(onChange).toHaveBeenCalledWith("m");

    // The parent persists the new value; the trigger now reads its live label.
    rerender(
      <Combobox label="Voice" options={OPTS} value="m" onValueChange={onChange} placeholder="Pick…" />,
    );
    const trigger = screen.getByRole("button", { name: "Voice" });
    expect(within(trigger).getByText("Marcus")).toBeInTheDocument();
  });
});
