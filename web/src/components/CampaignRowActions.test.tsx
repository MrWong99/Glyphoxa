import { describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";

import { CampaignSchema } from "@gen/glyphoxa/management/v1/management_pb";
import { CampaignRowActions } from "./CampaignRowActions";
import type { CampaignLifecycle } from "./useCampaignLifecycle";

// The menu on its own, against a stand-in lifecycle: which actions it offers
// for which campaign, and that each item hands the WHOLE campaign row to the
// right action. What the actions then do is covered where they run (the
// Campaigns page tests).

function campaign(opts: { archived?: boolean } = {}) {
  return create(CampaignSchema, {
    id: "c1",
    name: "Alpha Quest",
    system: "D&D 5e",
    language: "en",
    archivedAt: opts.archived ? timestampFromDate(new Date("2026-07-09T12:00:00Z")) : undefined,
  });
}

function lifecycle(busy: string[] = [], ready = true): CampaignLifecycle {
  return {
    openSettings: vi.fn(),
    exportBundle: vi.fn(),
    duplicate: vi.fn(),
    archive: vi.fn(),
    unarchive: vi.fn(),
    remove: vi.fn(),
    busy: new Set(busy),
    ready,
    dialogs: null,
  };
}

// Radix opens a dropdown menu from the keyboard on Enter, Space or ArrowDown.
function openMenu() {
  const trigger = screen.getByRole("button", { name: "Actions for Alpha Quest" });
  act(() => trigger.focus());
  fireEvent.keyDown(trigger, { key: "Enter" });
  return trigger;
}
const items = () => screen.getAllByRole("menuitem").map((item) => item.textContent);

describe("CampaignRowActions", () => {
  it("names its trigger after the campaign, so a page of cards is not a page of identical buttons", () => {
    render(<CampaignRowActions campaign={campaign()} lifecycle={lifecycle()} />);
    const trigger = screen.getByRole("button", { name: "Actions for Alpha Quest" });
    expect(trigger).toHaveAttribute("aria-haspopup", "menu");
  });

  it("offers every action on a campaign in use — Delete included, without a detour through Archive", () => {
    render(<CampaignRowActions campaign={campaign()} lifecycle={lifecycle()} />);
    openMenu();
    expect(items()).toEqual(["Settings", "Export bundle…", "Duplicate…", "Archive", "Delete…"]);
    expect(screen.getByRole("menuitem", { name: "Delete…" })).toHaveClass("gx-menu__item--danger");
  });

  it("marks Archive as asking first on the Active Campaign", () => {
    render(<CampaignRowActions campaign={campaign()} lifecycle={lifecycle()} isActive />);
    openMenu();
    expect(items()).toContain("Archive…");
    expect(items()).not.toContain("Archive");
  });

  it("offers Unarchive instead of Archive on an archived campaign, and still Export and Delete", () => {
    render(<CampaignRowActions campaign={campaign({ archived: true })} lifecycle={lifecycle()} />);
    openMenu();
    expect(items()).toEqual(["Settings", "Export bundle…", "Duplicate…", "Unarchive", "Delete…"]);
  });

  it("hands the campaign row to the action that was chosen", () => {
    const row = campaign();
    const actions = lifecycle();
    render(<CampaignRowActions campaign={row} lifecycle={actions} />);

    for (const [label, action] of [
      ["Settings", actions.openSettings],
      ["Export bundle…", actions.exportBundle],
      ["Duplicate…", actions.duplicate],
      ["Archive", actions.archive],
      ["Delete…", actions.remove],
    ] as const) {
      openMenu();
      fireEvent.click(screen.getByRole("menuitem", { name: label }));
      expect(action).toHaveBeenCalledExactlyOnceWith(row);
      // Choosing closes the menu.
      expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    }
  });

  it("holds the actions that would collide with a flow already running on the campaign", () => {
    const actions = lifecycle(["c1"]);
    render(<CampaignRowActions campaign={campaign()} lifecycle={actions} />);
    openMenu();
    for (const label of ["Duplicate…", "Archive", "Delete…"]) {
      expect(screen.getByRole("menuitem", { name: label })).toHaveAttribute("aria-disabled", "true");
    }
    fireEvent.click(screen.getByRole("menuitem", { name: "Delete…" }));
    expect(actions.remove).not.toHaveBeenCalled();
    // Settings and Export do not touch what the flow is doing.
    expect(screen.getByRole("menuitem", { name: "Settings" })).not.toHaveAttribute("aria-disabled");
    expect(screen.getByRole("menuitem", { name: "Export bundle…" })).not.toHaveAttribute("aria-disabled");
  });

  it("holds Archive and Delete until the Active Campaign is known", () => {
    const actions = lifecycle([], false);
    render(<CampaignRowActions campaign={campaign()} lifecycle={actions} />);
    openMenu();
    for (const label of ["Archive", "Delete…"]) {
      expect(screen.getByRole("menuitem", { name: label })).toHaveAttribute("aria-disabled", "true");
    }
    fireEvent.click(screen.getByRole("menuitem", { name: "Archive" }));
    expect(actions.archive).not.toHaveBeenCalled();
    expect(screen.getByRole("menuitem", { name: "Duplicate…" })).not.toHaveAttribute("aria-disabled");
  });

  it("is keyboard operable: opens on the first item, Escape closes and returns focus to the trigger", async () => {
    render(<CampaignRowActions campaign={campaign()} lifecycle={lifecycle()} />);
    const trigger = openMenu();
    const first = screen.getByRole("menuitem", { name: "Settings" });
    await waitFor(() => expect(first).toHaveFocus());

    fireEvent.keyDown(first, { key: "Escape" });
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    await waitFor(() => expect(trigger).toHaveFocus());
  });
});
