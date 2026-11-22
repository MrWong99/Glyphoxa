import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { Field } from "./Field";

describe("Field", () => {
  it("labels the control and describes it with the hint", () => {
    render(
      <Field label="Voice" hint="Used for every line.">
        {(control) => <select {...control} />}
      </Field>,
    );
    const control = screen.getByRole("combobox", { name: "Voice" });
    expect(control).toHaveAccessibleDescription("Used for every line.");
    expect(control).not.toHaveAttribute("aria-invalid");
  });

  it("replaces the hint with the error and marks the control invalid", () => {
    render(
      <Field label="Voice" hint="Used for every line." error="Pick a voice.">
        {(control) => <select {...control} />}
      </Field>,
    );
    const control = screen.getByRole("combobox", { name: "Voice" });
    expect(control).toHaveAccessibleDescription("Pick a voice.");
    expect(control).toBeInvalid();
    expect(screen.queryByText("Used for every line.")).toBeNull();
    expect(screen.getByText("Pick a voice.")).toHaveClass("gx-field__hint--error");
  });

  it("has no description when there is neither hint nor error", () => {
    render(<Field label="Voice">{(control) => <select {...control} />}</Field>);
    expect(screen.getByRole("combobox", { name: /Voice/ })).not.toHaveAttribute("aria-describedby");
  });

  it("marks a required field for sighted and screen-reader users", () => {
    render(
      <Field label="Name" required>
        {(control) => <input {...control} />}
      </Field>,
    );
    const control = screen.getByRole("textbox", { name: "Name (required)" });
    expect(control).toBeRequired();
    expect(screen.getByText("*")).toHaveAttribute("aria-hidden", "true");
  });

  it("uses a given id for the control", () => {
    render(
      <Field label="Name" id="npc-name">
        {(control) => <input {...control} />}
      </Field>,
    );
    expect(screen.getByRole("textbox", { name: "Name" })).toHaveAttribute("id", "npc-name");
  });

  it("frames a multi-element control as a named, described group", () => {
    render(
      <Field label="Speaks when" hint="Pick at least one." group>
        {(control) => {
          // A group field hands out no id: there is no single element to label.
          expect(control.id).toBeUndefined();
          return (
            <>
              <button>Addressed</button>
              <button>Always</button>
            </>
          );
        }}
      </Field>,
    );
    const group = screen.getByRole("group", { name: "Speaks when" });
    expect(group).toHaveAccessibleDescription("Pick at least one.");
  });
});
