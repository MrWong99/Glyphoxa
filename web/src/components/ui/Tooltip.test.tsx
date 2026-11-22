import { act, fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { Tooltip, TooltipProvider } from "./Tooltip";

// Radix opens a tooltip at once on KEYBOARD focus (hover waits for the delay),
// which is the path a mouse-less user takes and the one jsdom can drive.
const focusTrigger = (name: string) => {
  const trigger = screen.getByRole("button", { name });
  act(() => trigger.focus());
  return trigger;
};

describe("Tooltip", () => {
  it("shows its content on keyboard focus and describes the trigger with it", () => {
    render(
      <Tooltip content="Mute every NPC">
        <button>Mute all</button>
      </Tooltip>,
    );
    expect(screen.queryByRole("tooltip")).toBeNull();

    const trigger = focusTrigger("Mute all");
    const tooltip = screen.getByRole("tooltip");
    expect(tooltip).toHaveTextContent("Mute every NPC");
    expect(trigger).toHaveAttribute("aria-describedby", tooltip.id);
  });

  it("closes on Escape and on blur", () => {
    render(
      <Tooltip content="Mute every NPC">
        <button>Mute all</button>
      </Tooltip>,
    );
    const trigger = focusTrigger("Mute all");
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("tooltip")).toBeNull();

    act(() => trigger.blur());
    focusTrigger("Mute all");
    expect(screen.getByRole("tooltip")).toBeInTheDocument();
    act(() => trigger.blur());
    expect(screen.queryByRole("tooltip")).toBeNull();
  });

  it("works without a provider and inside a shared one", () => {
    render(
      <TooltipProvider>
        <Tooltip content="First">
          <button>one</button>
        </Tooltip>
        <Tooltip content="Second">
          <button>two</button>
        </Tooltip>
      </TooltipProvider>,
    );
    focusTrigger("two");
    expect(screen.getByRole("tooltip")).toHaveTextContent("Second");
  });

  it("renders the bare child when disabled or given no content", () => {
    render(
      <>
        <Tooltip content="Hidden" disabled>
          <button>off</button>
        </Tooltip>
        <Tooltip content="">
          <button>empty</button>
        </Tooltip>
      </>,
    );
    focusTrigger("off");
    expect(screen.queryByRole("tooltip")).toBeNull();
    focusTrigger("empty");
    expect(screen.queryByRole("tooltip")).toBeNull();
  });
});
