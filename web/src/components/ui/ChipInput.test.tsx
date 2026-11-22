import { useState } from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { ChipInput } from "./ChipInput";
import { Field } from "./Field";

function Aliases({
  initial = [],
  onChange,
  onDraftChange,
  max,
  disabled,
  commitOnBlur,
  error,
}: {
  initial?: string[];
  onChange?: (values: string[]) => void;
  onDraftChange?: (draft: string) => void;
  max?: number;
  disabled?: boolean;
  commitOnBlur?: boolean;
  error?: string;
}) {
  const [values, setValues] = useState(initial);
  return (
    <ChipInput
      label="Aliases"
      hint="Other names the table uses."
      error={error}
      placeholder="Add an alias…"
      values={values}
      max={max}
      disabled={disabled}
      commitOnBlur={commitOnBlur}
      onDraftChange={onDraftChange}
      onChange={(next) => {
        onChange?.(next);
        setValues(next);
      }}
    />
  );
}

const field = () => screen.getByRole<HTMLInputElement>("textbox", { name: "Aliases" });
const chips = () => screen.queryAllByRole("listitem").map((li) => li.textContent);
const type = (text: string) => fireEvent.change(field(), { target: { value: text } });

describe("ChipInput", () => {
  it("is a labelled, described text field with its values as a list", () => {
    render(<Aliases initial={["Flo", "the Butler"]} />);
    expect(field()).toHaveAccessibleDescription("Other names the table uses.");
    expect(field()).toHaveAttribute("placeholder", "Add an alias…");
    expect(chips()).toEqual(["Flo", "the Butler"]);
  });

  it("adds the typed value on Enter and clears the field", () => {
    const onChange = vi.fn();
    render(<Aliases onChange={onChange} />);
    type("  Flo ");
    fireEvent.keyDown(field(), { key: "Enter" });
    expect(onChange).toHaveBeenCalledWith(["Flo"]);
    expect(chips()).toEqual(["Flo"]);
    expect(field().value).toBe("");
  });

  it("adds the typed value on comma, without typing the comma", () => {
    render(<Aliases initial={["Flo"]} />);
    type("Florian");
    const notPrevented = fireEvent.keyDown(field(), { key: "," });
    expect(notPrevented).toBe(false); // the comma itself never reaches the field
    expect(chips()).toEqual(["Flo", "Florian"]);
    expect(field().value).toBe("");
  });

  it("removes the last value on Backspace when nothing is typed", () => {
    const onChange = vi.fn();
    render(<Aliases initial={["Flo", "Florian"]} onChange={onChange} />);
    fireEvent.keyDown(field(), { key: "Backspace" });
    expect(onChange).toHaveBeenCalledWith(["Flo"]);
    expect(chips()).toEqual(["Flo"]);
  });

  it("leaves Backspace alone while there is text to delete", () => {
    const onChange = vi.fn();
    render(<Aliases initial={["Flo"]} onChange={onChange} />);
    type("Fl");
    const notPrevented = fireEvent.keyDown(field(), { key: "Backspace" });
    expect(notPrevented).toBe(true);
    expect(onChange).not.toHaveBeenCalled();
    expect(chips()).toEqual(["Flo"]);
  });

  it("drops an empty or duplicate value without reporting a change", () => {
    const onChange = vi.fn();
    render(<Aliases initial={["Flo"]} onChange={onChange} />);
    type("Flo");
    fireEvent.keyDown(field(), { key: "Enter" });
    type("   ");
    fireEvent.keyDown(field(), { key: "," });
    expect(onChange).not.toHaveBeenCalled();
    expect(chips()).toEqual(["Flo"]);
    expect(field().value).toBe("");
  });

  it("leaves Enter to the surrounding form when nothing is typed", () => {
    const onSubmitKey = vi.fn();
    render(
      <div
        onKeyDown={(e) => {
          if (e.key === "Enter" && !e.defaultPrevented) onSubmitKey();
        }}
      >
        <Aliases initial={["Flo"]} />
      </div>,
    );
    fireEvent.keyDown(field(), { key: "Enter" });
    expect(onSubmitKey).toHaveBeenCalledTimes(1);

    type("Florian");
    fireEvent.keyDown(field(), { key: "Enter" });
    expect(onSubmitKey).toHaveBeenCalledTimes(1); // that Enter added a chip instead
  });

  it("removes a value from its own button and keeps focus in the control", () => {
    render(<Aliases initial={["Flo", "Florian"]} />);
    const remove = screen.getByRole("button", { name: "Remove Flo" });
    remove.focus();
    fireEvent.click(remove);
    expect(chips()).toEqual(["Florian"]);
    expect(field()).toHaveFocus();
  });

  it("adds what is typed when the field loses focus, unless told not to", () => {
    const { unmount } = render(<Aliases />);
    type("Flo");
    fireEvent.blur(field());
    expect(chips()).toEqual(["Flo"]);
    unmount();

    render(<Aliases commitOnBlur={false} />);
    type("Flo");
    fireEvent.blur(field());
    expect(chips()).toEqual([]);
    expect(field().value).toBe("Flo");
  });

  it("splits a pasted list into values", () => {
    render(<Aliases initial={["Flo"]} />);
    fireEvent.paste(field(), { clipboardData: { getData: () => "Florian, the Butler\nFlo" } });
    expect(chips()).toEqual(["Flo", "Florian", "the Butler"]);
  });

  it("lets a single pasted value land in the field as text", () => {
    render(<Aliases />);
    const notPrevented = fireEvent.paste(field(), { clipboardData: { getData: () => "Florian" } });
    expect(notPrevented).toBe(true);
    expect(chips()).toEqual([]);
  });

  it("stops at max and disables the field there", () => {
    render(<Aliases initial={["a"]} max={2} />);
    fireEvent.paste(field(), { clipboardData: { getData: () => "b, c, d" } });
    expect(chips()).toEqual(["a", "b"]);
    expect(field()).toBeDisabled();

    fireEvent.click(screen.getByRole("button", { name: "Remove a" }));
    expect(field()).toBeEnabled();
  });

  it("reports the uncommitted text, and its clearing on add", () => {
    const onDraftChange = vi.fn();
    render(<Aliases onDraftChange={onDraftChange} />);
    type("Fl");
    expect(onDraftChange).toHaveBeenLastCalledWith("Fl");
    fireEvent.keyDown(field(), { key: "Enter" });
    expect(onDraftChange).toHaveBeenLastCalledWith("");
  });

  it("does not add while an IME composition is confirming with Enter", () => {
    render(<Aliases />);
    type("ふろ");
    fireEvent.keyDown(field(), { key: "Enter", isComposing: true });
    expect(chips()).toEqual([]);
  });

  it("disables the field and every remove button when disabled", () => {
    render(<Aliases initial={["Flo"]} disabled />);
    expect(field()).toBeDisabled();
    expect(screen.getByRole("button", { name: "Remove Flo" })).toBeDisabled();
  });

  it("marks the field invalid and describes it with the error", () => {
    render(<Aliases error="Too many aliases." />);
    expect(field()).toBeInvalid();
    expect(field()).toHaveAccessibleDescription("Too many aliases.");
  });

  it("can be named without a visible label", () => {
    render(<ChipInput aria-label="Tags" values={["npc"]} onChange={() => {}} />);
    expect(screen.getByRole("textbox", { name: "Tags" })).toBeInTheDocument();
  });

  // At the cap the text field is disabled, and a disabled field cannot take
  // focus: the focus has to move once the list has shrunk and it is enabled.
  it("keeps focus in the control when a value is removed at max", () => {
    render(<Aliases initial={["a", "b"]} max={2} />);
    expect(field()).toBeDisabled();
    const remove = screen.getByRole("button", { name: "Remove a" });
    remove.focus();
    fireEvent.click(remove);
    expect(chips()).toEqual(["b"]);
    expect(field()).toBeEnabled();
    expect(field()).toHaveFocus();
  });

  it("takes its label, description, invalid and required state from a Field", () => {
    render(
      <Field label="Tags" error="Pick at least one." required>
        {(control) => <ChipInput {...control} values={[]} onChange={() => {}} />}
      </Field>,
    );
    const input = screen.getByRole("textbox", { name: /Tags/ });
    expect(input).toBeInvalid();
    expect(input).toBeRequired();
    expect(input).toHaveAccessibleDescription("Pick at least one.");
    expect(input.closest(".gx-chipinput")).toHaveAttribute("data-invalid", "true");
  });
});
