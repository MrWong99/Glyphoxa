import { useState } from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { Textarea } from "./Textarea";

// jsdom lays nothing out, so scrollHeight is always 0. Give the element a
// content height that follows its text: 20px per line.
function mockLayout() {
  vi.spyOn(HTMLTextAreaElement.prototype, "scrollHeight", "get").mockImplementation(function (
    this: HTMLTextAreaElement,
  ) {
    return this.value.split("\n").length * 20;
  });
  // Only the box metrics the component reads are overridden; everything else
  // (Testing Library reads computed styles too) falls through to jsdom.
  const metrics: Record<string, string> = {
    borderTopWidth: "1px",
    borderBottomWidth: "1px",
    paddingTop: "0px",
    paddingBottom: "0px",
    lineHeight: "20px",
    fontSize: "14px",
  };
  const real = window.getComputedStyle.bind(window);
  vi.spyOn(window, "getComputedStyle").mockImplementation((element, pseudo) => {
    const style = real(element, pseudo);
    if (!(element instanceof HTMLTextAreaElement)) return style;
    return new Proxy(style, {
      get: (target, key) => {
        if (typeof key === "string" && key in metrics) return metrics[key];
        const value: unknown = Reflect.get(target, key, target);
        // jsdom's methods check their receiver; call them on the real object.
        return typeof value === "function" ? value.bind(target) : value;
      },
    });
  });
}

afterEach(() => vi.restoreAllMocks());

describe("Textarea", () => {
  it("shares Input's contract: label, hint as description", () => {
    render(<Textarea label="Persona" hint="How this NPC talks." />);
    const field = screen.getByRole("textbox", { name: "Persona" });
    expect(field.tagName).toBe("TEXTAREA");
    expect(field).toHaveClass("gx-input");
    expect(field).toHaveAccessibleDescription("How this NPC talks.");
    expect(field).not.toHaveAttribute("aria-invalid");
  });

  it("replaces the hint with the error and marks the field invalid", () => {
    render(<Textarea label="Persona" hint="How this NPC talks." error="Too long." />);
    const field = screen.getByRole("textbox", { name: "Persona" });
    expect(field).toBeInvalid();
    expect(field).toHaveAccessibleDescription("Too long.");
    expect(field).toHaveClass("gx-input--invalid");
  });

  it("passes textarea attributes and events through, and forwards its ref", () => {
    const onChange = vi.fn();
    const ref = { current: null as HTMLTextAreaElement | null };
    render(<Textarea aria-label="Notes" placeholder="Anything…" maxLength={500} onChange={onChange} ref={ref} />);
    const field = screen.getByRole("textbox", { name: "Notes" });
    expect(field).toHaveAttribute("placeholder", "Anything…");
    expect(field).toHaveAttribute("maxlength", "500");
    fireEvent.change(field, { target: { value: "hello" } });
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(ref.current).toBe(field);
  });

  it("does not manage its height unless asked to", () => {
    mockLayout();
    render(<Textarea aria-label="Notes" defaultValue={"a\nb\nc"} />);
    const field = screen.getByRole("textbox", { name: "Notes" });
    expect(field.style.height).toBe("");
    expect(field).not.toHaveClass("gx-input--autogrow");
  });

  describe("autoGrow", () => {
    function Controlled({ maxRows }: { maxRows?: number }) {
      const [value, setValue] = useState("one");
      return (
        <>
          <Textarea
            aria-label="Message"
            autoGrow
            maxRows={maxRows}
            value={value}
            onChange={(e) => setValue(e.target.value)}
          />
          <button onClick={() => setValue("")}>clear</button>
        </>
      );
    }

    it("starts one row tall and fits its height to the text as it changes", () => {
      mockLayout();
      render(<Controlled />);
      const field = screen.getByRole("textbox", { name: "Message" });
      expect(field).toHaveAttribute("rows", "1");
      expect(field).toHaveClass("gx-input--autogrow");
      // One line of 20px plus the two 1px borders (border-box).
      expect(field.style.height).toBe("22px");

      fireEvent.change(field, { target: { value: "one\ntwo\nthree" } });
      expect(field.style.height).toBe("62px");
    });

    it("shrinks again when the text is removed from outside (a sent message)", () => {
      mockLayout();
      render(<Controlled />);
      const field = screen.getByRole("textbox", { name: "Message" });
      fireEvent.change(field, { target: { value: "one\ntwo\nthree\nfour" } });
      expect(field.style.height).toBe("82px");

      fireEvent.click(screen.getByRole("button", { name: "clear" }));
      expect(field.style.height).toBe("22px");
    });

    it("stops growing at maxRows and lets the box scroll from there", () => {
      mockLayout();
      render(<Controlled maxRows={3} />);
      const field = screen.getByRole("textbox", { name: "Message" });
      fireEvent.change(field, { target: { value: "1\n2\n3" } });
      expect(field.style.height).toBe("62px");
      expect(field).not.toHaveAttribute("data-capped");

      fireEvent.change(field, { target: { value: "1\n2\n3\n4\n5" } });
      expect(field.style.height).toBe("62px");
      expect(field).toHaveAttribute("data-capped", "true");
    });

    it("refits an uncontrolled field on input", () => {
      mockLayout();
      render(<Textarea aria-label="Message" autoGrow defaultValue="one" />);
      const field = screen.getByRole<HTMLTextAreaElement>("textbox", { name: "Message" });
      field.value = "one\ntwo";
      fireEvent.input(field);
      expect(field.style.height).toBe("42px");
    });

    // To measure, the box is collapsed to one row for an instant. In a browser
    // that shortens the page, and a scroll container near its end has its
    // scrollTop clamped by the layout that measuring forces; nothing puts it
    // back. jsdom has no layout, so the clamp is played here by hand: reading
    // scrollHeight while the box is collapsed scrolls the container to the top.
    it("keeps the scroll position of the containers around it while it measures", () => {
      mockLayout();
      render(
        <div data-testid="scroller">
          <div data-testid="inner">
            <Textarea aria-label="Message" autoGrow defaultValue={"one\ntwo"} />
          </div>
        </div>,
      );
      const scroller = screen.getByTestId("scroller");
      const inner = screen.getByTestId("inner");
      const field = screen.getByRole<HTMLTextAreaElement>("textbox", { name: "Message" });
      scroller.scrollTop = 1588;
      inner.scrollTop = 40;
      document.documentElement.scrollTop = 300;

      vi.spyOn(HTMLTextAreaElement.prototype, "scrollHeight", "get").mockImplementation(function (
        this: HTMLTextAreaElement,
      ) {
        if (this.style.height === "auto") {
          scroller.scrollTop = 0;
          inner.scrollTop = 0;
          document.documentElement.scrollTop = 0;
        }
        return this.value.split("\n").length * 20;
      });

      field.value = "one\ntwo\nthree";
      fireEvent.input(field);
      expect(field.style.height).toBe("62px");
      expect(scroller.scrollTop).toBe(1588);
      expect(inner.scrollTop).toBe(40);
      expect(document.documentElement.scrollTop).toBe(300);
      document.documentElement.scrollTop = 0;
    });

    it("leaves the height alone when the element is not laid out", () => {
      // No layout mock: scrollHeight is 0, as in a hidden tab.
      render(<Textarea aria-label="Message" autoGrow defaultValue="one" />);
      expect(screen.getByRole("textbox", { name: "Message" }).style.height).toBe("auto");
    });
  });
});
