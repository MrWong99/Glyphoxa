import { describe, it, expect } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { useRef, useState, type ReactNode } from "react";

import { usePopoverDismiss } from "./usePopoverDismiss";

// A minimal popover on the hook, next to whatever else the test puts on the page.
function Harness({ children }: { children?: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(true);
  usePopoverDismiss(ref, open, () => setOpen(false));
  return (
    <>
      <div ref={ref}>{open && <p>popover open</p>}</div>
      <button>elsewhere</button>
      {children}
    </>
  );
}

const outsideClick = () => fireEvent.mouseDown(screen.getByRole("button", { name: "elsewhere" }));
const isOpen = () => screen.queryByText("popover open") !== null;

describe("usePopoverDismiss", () => {
  it("closes on an outside mousedown and on Escape", () => {
    render(<Harness />);
    outsideClick();
    expect(isOpen()).toBe(false);

    render(<Harness />);
    fireEvent.keyDown(document, { key: "Escape" });
    expect(isOpen()).toBe(false);
  });

  // W6: an inline panel that merely sits on the page is not a modal layer. A
  // bare role="dialog" used to switch dismissal off for every popover.
  it("still dismisses while a non-modal inline panel is on screen", () => {
    render(
      <Harness>
        <div role="dialog" aria-label="inline form" />
      </Harness>,
    );
    outsideClick();
    expect(isOpen()).toBe(false);
  });

  it.each([
    ["a Radix AlertDialog", <div key="a" role="alertdialog" />],
    ["a Radix Dialog", <div key="b" role="dialog" data-state="open" />],
    ["a self-declared modal", <div key="c" role="dialog" aria-modal="true" />],
  ])("defers to %s", (_name, layer) => {
    render(<Harness>{layer}</Harness>);
    outsideClick();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(isOpen()).toBe(true);
  });
});
