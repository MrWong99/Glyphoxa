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

  it("filters on the label only — an opaque voice id never matches typeahead", () => {
    // Real ElevenLabs ids are 20-char base62 blobs; with cmdk's default filter
    // the item value joins the search haystack, so an id subsequence ("wam" ⊂
    // 21m00Tcm4TlvDq8ikWAM) keeps "Rachel" visible though nothing the operator
    // can see matches. Filtering must score the label alone.
    const opts = [
      { value: "21m00Tcm4TlvDq8ikWAM", label: "Rachel" },
      { value: "AZnzlk1XvdvUeBnXmlld", label: "Domi" },
    ];
    render(
      <Combobox label="Voice" options={opts} value="" onValueChange={() => {}} emptyText="No matches" />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Voice" }));

    // A label substring still filters correctly.
    fireEvent.change(screen.getByPlaceholderText(/search/i), { target: { value: "rach" } });
    expect(screen.getByRole("option", { name: /rachel/i })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /domi/i })).not.toBeInTheDocument();

    // An id-only match must show nothing.
    fireEvent.change(screen.getByPlaceholderText(/search/i), { target: { value: "wam" } });
    expect(screen.queryByRole("option", { name: /rachel/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /domi/i })).not.toBeInTheDocument();
    expect(screen.getByText("No matches")).toBeInTheDocument();
  });

  it("clears the search when dismissed without picking, so reopen shows the full list", () => {
    render(<Combobox label="Voice" options={OPTS} value="" onValueChange={() => {}} />);
    // Open, filter down to Marcus, then dismiss with Escape (no pick).
    fireEvent.click(screen.getByRole("button", { name: "Voice" }));
    fireEvent.change(screen.getByPlaceholderText(/search/i), { target: { value: "marcus" } });
    expect(screen.queryByRole("option", { name: /rachel/i })).not.toBeInTheDocument();
    fireEvent.keyDown(document, { key: "Escape" });

    // Reopen: the input is empty and the list is unfiltered.
    fireEvent.click(screen.getByRole("button", { name: "Voice" }));
    expect(screen.getByPlaceholderText(/search/i)).toHaveValue("");
    expect(screen.getByRole("option", { name: /rachel/i })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: /marcus/i })).toBeInTheDocument();
    expect(screen.getByRole("option", { name: /aria/i })).toBeInTheDocument();
  });

  it("distinguishes options with identical labels: selecting the second dispatches its value", () => {
    // ElevenLabs voice names are not unique — two "Rachel"s must stay separate
    // cmdk identities so keyboard selection lands on the one the operator chose.
    const dupes = [
      { value: "voice-rachel-1", label: "Rachel" },
      { value: "voice-rachel-2", label: "Rachel" },
    ];
    const onChange = vi.fn();
    render(<Combobox label="Voice" options={dupes} value="" onValueChange={onChange} />);
    fireEvent.click(screen.getByRole("button", { name: "Voice" }));

    // Arrow from the first Rachel to the second, then confirm with Enter.
    const input = screen.getByPlaceholderText(/search/i);
    fireEvent.keyDown(input, { key: "ArrowDown" });
    fireEvent.keyDown(input, { key: "Enter" });

    expect(onChange).toHaveBeenCalledWith("voice-rachel-2");
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

describe("Combobox allowCustom (#227)", () => {
  it("offers the typed text as a Use item and picks it verbatim", () => {
    const onChange = vi.fn();
    render(<Combobox label="Model" options={OPTS} value="" onValueChange={onChange} allowCustom />);
    fireEvent.click(screen.getByRole("button", { name: "Model" }));
    fireEvent.change(screen.getByPlaceholderText(/search/i), { target: { value: "my-custom-model " } });
    const useItem = screen.getByRole("option", { name: /use "my-custom-model"/i });
    fireEvent.click(useItem);
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenCalledWith("my-custom-model");
  });

  it("suppresses the Use item on an exact match (case-insensitive) and on empty search", () => {
    render(<Combobox label="Model" options={OPTS} value="" onValueChange={() => {}} allowCustom />);
    fireEvent.click(screen.getByRole("button", { name: "Model" }));
    // Empty search: no phantom item.
    expect(screen.queryByRole("option", { name: /use "/i })).not.toBeInTheDocument();
    // Exact label match, different case: the real item is selectable, no Use item.
    fireEvent.change(screen.getByPlaceholderText(/search/i), { target: { value: "RACHEL" } });
    expect(screen.getByRole("option", { name: /rachel/i })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: /use "/i })).not.toBeInTheDocument();
  });

  it("renders a custom (non-catalog) value verbatim on the trigger", () => {
    render(
      <Combobox label="Model" options={OPTS} value="my-custom-model" onValueChange={() => {}} allowCustom />,
    );
    expect(screen.getByRole("button", { name: "Model" })).toHaveTextContent("my-custom-model");
  });
});
