import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { toast, Toaster } from "sonner";

import { Providers } from "@/app/Providers";
import { MemoryScreenNav, type ScreenLocation } from "@/app/screenNav";
import { resetLocalCampaignSwitch } from "@/components/useCampaignSwitch";
import { useDirtyGuard } from "@/components/ui/useDirtyGuard";
import { makeQueryClient } from "@/lib/queryClient";
import { Campaigns } from "./Campaigns";
import { campaignBackend, type Camp } from "./testBackend";

// The bundle endpoints are plain fetches; mock the three calls and keep the
// real error class and its predicates (what tells a 503 from a 413).
vi.mock("@/lib/download", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/download")>();
  return {
    ...actual,
    fetchCampaignExport: vi.fn(),
    importCampaignBundle: vi.fn(),
    downloadBlob: vi.fn(),
  };
});
import {
  BundleRequestError,
  downloadBlob,
  fetchCampaignExport,
  importCampaignBundle,
} from "@/lib/download";
const mockExport = vi.mocked(fetchCampaignExport);
const mockImport = vi.mocked(importCampaignBundle);
const mockDownload = vi.mocked(downloadBlob);

beforeEach(() => {
  mockExport.mockReset();
  mockImport.mockReset();
  mockDownload.mockReset();
  resetLocalCampaignSwitch();
});

afterEach(() => {
  // The real toast host is mounted (the Undo and Open actions are real
  // buttons); clear it so one test's toast is not the next one's.
  act(() => {
    toast.dismiss();
  });
});

const TWO: Camp[] = [
  { id: "a", name: "Alpha Quest", system: "D&D 5e", language: "en", created: "2026-01-05T12:00:00Z", updated: "2026-02-10T12:00:00Z" },
  { id: "b", name: "Beta Tale", system: "Pathfinder 2e", language: "de", created: "2026-03-01T12:00:00Z", updated: "2026-03-02T12:00:00Z" },
];
const WITH_ARCHIVED: Camp[] = [...TWO, { id: "z", name: "Old Campaign", system: "Call of Cthulhu", archived: true }];

// An editor elsewhere on screen that can be made dirty — a switch must ask.
function Editor() {
  const edits = useDirtyGuard();
  return (
    <button type="button" onClick={() => edits.markDirty(true)}>
      type something
    </button>
  );
}

function renderScreen(opts: Parameters<typeof campaignBackend>[0] = {}) {
  const h = campaignBackend(opts);
  const moves: ScreenLocation[] = [];
  // No backoff between retries, so a test that fails the list reaches the
  // settled error state at once.
  const queryClient = makeQueryClient();
  queryClient.setDefaultOptions({ queries: { retryDelay: 0 } });
  render(
    <Providers transport={h.transport} queryClient={queryClient}>
      <MemoryScreenNav initial={{ screen: "campaigns" }} onNavigate={(next) => moves.push(next)}>
        <Editor />
        <Campaigns />
      </MemoryScreenNav>
      <Toaster />
    </Providers>,
  );
  return { ...h, moves };
}

const grid = () => screen.findByRole("list", { name: "Your campaigns" });
const cardOf = async (name: string) =>
  (await screen.findByRole("heading", { level: 2, name })).closest("li") as HTMLElement;

// Radix opens a dropdown menu from the keyboard on Enter, Space or ArrowDown.
async function openMenu(name: string) {
  const trigger = await screen.findByRole("button", { name: `Actions for ${name}` });
  act(() => trigger.focus());
  fireEvent.keyDown(trigger, { key: "Enter" });
  return trigger;
}
const choose = (label: string | RegExp) => fireEvent.click(screen.getByRole("menuitem", { name: label }));

describe("Campaigns: the list", () => {
  it("shows one card per campaign with its System, language and dates, and marks the active one", async () => {
    const { state } = renderScreen({ campaigns: TWO, activeId: "b" });
    expect(screen.getByRole("heading", { level: 1, name: "Campaigns" })).toBeInTheDocument();

    const cards = within(await grid()).getAllByRole("listitem");
    expect(cards).toHaveLength(2);
    const alpha = within(cards[0]);
    expect(alpha.getByRole("heading", { name: "Alpha Quest" })).toBeInTheDocument();
    expect(alpha.getByText("D&D 5e")).toBeInTheDocument();
    // The Campaign Language by name, in the display language.
    expect(alpha.getByText("English")).toBeInTheDocument();
    expect(alpha.getByText("Created").nextElementSibling).toHaveTextContent("Jan 5, 2026");
    // updated_at is the row's modification time: "Edited", never "last played".
    expect(alpha.getByText("Edited").nextElementSibling).toHaveTextContent("Feb 10, 2026");
    expect(screen.queryByText(/last played/i)).not.toBeInTheDocument();
    expect(within(cards[1]).getByText("German")).toBeInTheDocument();

    // Only the Active Campaign carries the badge.
    await waitFor(() => expect(within(cards[1]).getByText("Active")).toBeInTheDocument());
    expect(alpha.queryByText("Active")).not.toBeInTheDocument();
    // Each Open says WHICH campaign to a screen reader.
    expect(alpha.getByRole("button", { name: "Open Alpha Quest" })).toHaveTextContent("Open");
    // One read for both sections.
    expect(state.calls.filter((c) => c.method === "listCampaigns")).toEqual([
      { method: "listCampaigns", includeArchived: true },
    ]);
  });

  it("keeps archived campaigns in a collapsed section of their own", async () => {
    renderScreen({ campaigns: WITH_ARCHIVED, activeId: "a" });
    expect(within(await grid()).getAllByRole("listitem")).toHaveLength(2);
    expect(screen.queryByText("Old Campaign")).not.toBeInTheDocument();

    const toggle = screen.getByRole("button", { name: "Archived (1)" });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");

    const archived = within(screen.getByRole("list", { name: "Archived campaigns" }));
    expect(archived.getByRole("heading", { name: "Old Campaign" })).toBeInTheDocument();
    expect(archived.getByText("Archived", { selector: ".gx-badge" })).toBeInTheDocument();
    // An archived campaign cannot be opened; the way back in is to unarchive it.
    expect(archived.queryByRole("button", { name: /^Open/ })).not.toBeInTheDocument();
    expect(archived.getByRole("button", { name: "Unarchive Old Campaign" })).toBeInTheDocument();
  });

  it("marks the campaign a Voice Session is live in", async () => {
    renderScreen({ campaigns: TWO, activeId: "a", liveOn: "a" });
    const alpha = await cardOf("Alpha Quest");
    await waitFor(() => expect(within(alpha).getByText("Live")).toBeInTheDocument());
    expect(within(await cardOf("Beta Tale")).queryByText("Live")).not.toBeInTheDocument();
  });

  it("shows a loading state that is named for assistive tech, then the cards", async () => {
    renderScreen({ campaigns: TWO, activeId: "a" });
    expect(screen.getByRole("status", { name: "Loading…" })).toBeInTheDocument();
    await grid();
    expect(screen.queryByRole("status", { name: "Loading…" })).not.toBeInTheDocument();
  });

  it("says why the list could not be loaded, and Try again loads it", async () => {
    // The read is retried three times before it settles as failed: fail all four.
    renderScreen({ campaigns: TWO, activeId: "a", failLists: 4 });
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Couldn't load your campaigns");
    expect(alert).toHaveTextContent("campaign store offline");

    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await grid()).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("says what a campaign is when there is none yet, and offers to create one", async () => {
    renderScreen({ campaigns: [] });
    expect(await screen.findByRole("heading", { level: 2, name: "No campaigns yet" })).toBeInTheDocument();
    expect(screen.getByText(/cast of NPCs, the world wiki, the maps/)).toBeInTheDocument();
    expect(screen.queryByRole("list", { name: "Your campaigns" })).not.toBeInTheDocument();
    // The page's own actions are there from the first visit.
    expect(screen.getByRole("button", { name: "Import bundle" })).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: "New campaign" }).length).toBeGreaterThan(0);
  });

  it("says so when every campaign is archived, and opens nothing by itself", async () => {
    renderScreen({ campaigns: [{ id: "z", name: "Old Campaign", archived: true }] });
    expect(await screen.findByRole("heading", { name: "Every campaign is archived" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Archived (1)" })).toHaveAttribute("aria-expanded", "false");
  });
});

describe("Campaigns: search and sort", () => {
  const MANY: Camp[] = [
    { id: "1", name: "Ashes of Valen", system: "D&D 5e", updated: "2026-01-01T12:00:00Z" },
    { id: "2", name: "Blackwater", system: "Pathfinder 2e", updated: "2026-06-01T12:00:00Z" },
    { id: "3", name: "Cinder Road", system: "D&D 5e", updated: "2026-02-01T12:00:00Z" },
    { id: "4", name: "Deep Halls", system: "D&D 5e", updated: "2026-03-01T12:00:00Z" },
    { id: "5", name: "Ember Coast", system: "Call of Cthulhu", updated: "2026-04-01T12:00:00Z" },
    { id: "6", name: "Frostmere", system: "D&D 5e", updated: "2026-05-01T12:00:00Z" },
    { id: "7", name: "Gloamwood", system: "Call of Cthulhu", archived: true },
  ];
  const names = async () =>
    within(await grid())
      .getAllByRole("heading", { level: 2 })
      .map((h) => h.textContent);

  it("are not offered while the campaigns fit one glance", async () => {
    renderScreen({ campaigns: TWO, activeId: "a" });
    await grid();
    expect(screen.queryByRole("searchbox")).not.toBeInTheDocument();
    expect(screen.queryByRole("group", { name: "Sort campaigns by" })).not.toBeInTheDocument();
  });

  it("filter by name or system, across both sections", async () => {
    renderScreen({ campaigns: MANY, activeId: "1" });
    const search = await screen.findByRole("searchbox", { name: "Search campaigns" });

    fireEvent.change(search, { target: { value: "pathfinder" } });
    expect(await names()).toEqual(["Blackwater"]);

    // A hit that is archived opens the archived section instead of reading as
    // "nothing found".
    fireEvent.change(search, { target: { value: "gloam" } });
    expect(screen.queryByRole("list", { name: "Your campaigns" })).not.toBeInTheDocument();
    expect(
      within(screen.getByRole("list", { name: "Archived campaigns" })).getByRole("heading", { name: "Gloamwood" }),
    ).toBeInTheDocument();

    fireEvent.change(search, { target: { value: "zzz" } });
    expect(screen.getByText("No campaign matches “zzz”")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Clear search" }));
    expect(await names()).toHaveLength(6);
  });

  it("sort by name, or by what was edited most recently", async () => {
    renderScreen({ campaigns: MANY, activeId: "1" });
    expect(await names()).toEqual([
      "Ashes of Valen",
      "Blackwater",
      "Cinder Road",
      "Deep Halls",
      "Ember Coast",
      "Frostmere",
    ]);

    fireEvent.click(screen.getByRole("button", { name: "Recently edited" }));
    expect(await names()).toEqual([
      "Blackwater",
      "Frostmere",
      "Ember Coast",
      "Deep Halls",
      "Cinder Road",
      "Ashes of Valen",
    ]);
    expect(screen.getByRole("button", { name: "Recently edited" })).toHaveAttribute("aria-pressed", "true");
  });
});

describe("Campaigns: Open", () => {
  it("switches to another campaign, says so, and lands on its Overview", async () => {
    const { callsOf, moves } = renderScreen({ campaigns: TWO, activeId: "a" });
    fireEvent.click(await screen.findByRole("button", { name: "Open Beta Tale" }));

    await waitFor(() => expect(callsOf("setActiveCampaign")).toEqual([{ method: "setActiveCampaign", id: "b" }]));
    expect(await screen.findByText("Now working in Beta Tale")).toBeInTheDocument();
    await waitFor(() => expect(moves).toEqual([{ screen: "campaign", view: "overview", search: {} }]));
  });

  it("on the Active Campaign just goes to its Overview — no switch, no toast", async () => {
    const { callsOf, moves } = renderScreen({ campaigns: TWO, activeId: "a" });
    await waitFor(async () => expect(within(await cardOf("Alpha Quest")).getByText("Active")).toBeInTheDocument());

    fireEvent.click(screen.getByRole("button", { name: "Open Alpha Quest" }));
    expect(moves).toEqual([{ screen: "campaign", view: "overview", search: {} }]);
    expect(callsOf("setActiveCampaign")).toEqual([]);
    expect(screen.queryByText(/Now working in/)).not.toBeInTheDocument();
  });

  it("asks before a switch would discard unsaved edits", async () => {
    const { callsOf, moves } = renderScreen({ campaigns: TWO, activeId: "a" });
    fireEvent.click(screen.getByRole("button", { name: "type something" }));
    fireEvent.click(await screen.findByRole("button", { name: "Open Beta Tale" }));

    expect(await screen.findByText("Discard unsaved changes?")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByText("Discard unsaved changes?")).not.toBeInTheDocument());
    expect(callsOf("setActiveCampaign")).toEqual([]);
    expect(moves).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Open Beta Tale" }));
    fireEvent.click(await screen.findByRole("button", { name: "Discard changes" }));
    await waitFor(() => expect(callsOf("setActiveCampaign")).toHaveLength(1));
    await waitFor(() => expect(moves).toHaveLength(1));
  });

  it("stays on the page when a live Voice Session defers the switch", async () => {
    const { callsOf, moves } = renderScreen({ campaigns: TWO, activeId: "a", liveOn: "a" });
    fireEvent.click(await screen.findByRole("button", { name: "Open Beta Tale" }));

    await waitFor(() => expect(callsOf("setActiveCampaign")).toHaveLength(1));
    expect(
      await screen.findByText("A voice session is live — Beta Tale becomes the active campaign when it ends."),
    ).toBeInTheDocument();
    // The Overview would still be the session's campaign: do not pretend.
    expect(moves).toEqual([]);
  });
});

describe("Campaigns: New campaign", () => {
  it("creates in a dialog, then lands on the new campaign's Overview and says its Butler is ready", async () => {
    const { callsOf, moves } = renderScreen({ campaigns: TWO, activeId: "a" });
    await grid();
    fireEvent.click(screen.getByRole("button", { name: "New campaign" }));

    const dialog = await screen.findByRole("dialog", { name: "New campaign" });
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Gamma Saga" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Create campaign" }));

    await waitFor(() => expect(callsOf("createCampaign")).toEqual([{ method: "createCampaign", name: "Gamma Saga" }]));
    // The new campaign is made active through the same guarded switch.
    await waitFor(() => expect(callsOf("setActiveCampaign")).toEqual([{ method: "setActiveCampaign", id: "new-1" }]));
    expect(await screen.findByText("Created Gamma Saga — your Butler is ready")).toBeInTheDocument();
    await waitFor(() => expect(moves).toEqual([{ screen: "campaign", view: "overview", search: {} }]));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "New campaign" })).not.toBeInTheDocument());
  });

  it("keeps the form, and its reason, when the create is refused", async () => {
    const { moves } = renderScreen({ campaigns: TWO, activeId: "a", errors: { createCampaign: "database is down" } });
    await grid();
    fireEvent.click(screen.getByRole("button", { name: "New campaign" }));
    const dialog = await screen.findByRole("dialog", { name: "New campaign" });
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Doomed" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Create campaign" }));

    expect(await within(dialog).findByRole("alert")).toHaveTextContent(/couldn't create.*database is down/i);
    expect(within(dialog).getByLabelText("Name")).toHaveValue("Doomed");
    expect(moves).toEqual([]);
  });

  it("says a live Voice Session defers the new campaign, and stays put", async () => {
    const { moves } = renderScreen({ campaigns: TWO, activeId: "a", liveOn: "a" });
    await grid();
    fireEvent.click(screen.getByRole("button", { name: "New campaign" }));
    const dialog = await screen.findByRole("dialog", { name: "New campaign" });
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Gamma Saga" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Create campaign" }));

    expect(
      await screen.findByText(
        "Created Gamma Saga. A voice session is live — it becomes the active campaign when the session ends.",
      ),
    ).toBeInTheDocument();
    expect(moves).toEqual([]);
  });
});

describe("Campaigns: Settings of any campaign", () => {
  it("renames a campaign that is not the active one, without switching to it", async () => {
    const { callsOf } = renderScreen({ campaigns: TWO, activeId: "a" });
    await openMenu("Beta Tale");
    choose("Settings");

    const dialog = await screen.findByRole("dialog", { name: "Settings — Beta Tale" });
    const name = within(dialog).getByLabelText("Name");
    // Seeded from the full row the list returned.
    expect(name).toHaveValue("Beta Tale");
    expect(within(dialog).getByLabelText("Game system")).toHaveValue("Pathfinder 2e");

    fireEvent.change(name, { target: { value: "Beta Tale, Book Two" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Save changes" }));

    await waitFor(() => expect(callsOf("updateCampaign")).toHaveLength(1));
    expect(callsOf("updateCampaign")[0].req).toMatchObject({ id: "b", name: "Beta Tale, Book Two", system: "Pathfinder 2e" });
    expect(await screen.findByText("Campaign settings saved")).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    // The card follows; the Active Campaign did not move.
    expect(await screen.findByRole("heading", { level: 2, name: "Beta Tale, Book Two" })).toBeInTheDocument();
    expect(callsOf("setActiveCampaign")).toEqual([]);
    expect(within(await cardOf("Alpha Quest")).getByText("Active")).toBeInTheDocument();
  });

  it("asks before closing the dialog with unsaved edits", async () => {
    const { callsOf } = renderScreen({ campaigns: TWO, activeId: "a" });
    await openMenu("Beta Tale");
    choose("Settings");
    const dialog = await screen.findByRole("dialog", { name: "Settings — Beta Tale" });
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Half an idea" } });
    expect(await within(dialog).findByText("Unsaved changes")).toBeInTheDocument();

    // Cancel does not just close…
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    const question = await screen.findByRole("alertdialog", { name: "Discard unsaved changes?" });
    // …and backing out of the question keeps the dialog and the typing.
    fireEvent.click(within(question).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
    expect(screen.getByRole("dialog", { name: "Settings — Beta Tale" })).toBeInTheDocument();
    expect(screen.getByLabelText("Name")).toHaveValue("Half an idea");

    // The close button asks the same question; confirming discards.
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    fireEvent.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Discard changes" }),
    );
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(callsOf("updateCampaign")).toEqual([]);
    expect(await screen.findByRole("heading", { level: 2, name: "Beta Tale" })).toBeInTheDocument();
  });

  it("closes at once when nothing was changed", async () => {
    renderScreen({ campaigns: TWO, activeId: "a" });
    await openMenu("Beta Tale");
    choose("Settings");
    const dialog = await screen.findByRole("dialog", { name: "Settings — Beta Tale" });
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  });
});

describe("Campaigns: Archive", () => {
  it("archives a campaign at once and offers Undo, which restores it", async () => {
    const { callsOf } = renderScreen({ campaigns: TWO, activeId: "a" });
    await openMenu("Beta Tale");
    choose("Archive");

    await waitFor(() => expect(callsOf("archiveCampaign")).toEqual([{ method: "archiveCampaign", id: "b" }]));
    expect(await screen.findByText("Archived Beta Tale")).toBeInTheDocument();
    // It left the cards in use for the archived section.
    await waitFor(async () => expect(within(await grid()).getAllByRole("listitem")).toHaveLength(1));
    expect(screen.getByRole("button", { name: "Archived (1)" })).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Undo" }));
    await waitFor(() => expect(callsOf("unarchiveCampaign")).toEqual([{ method: "unarchiveCampaign", id: "b" }]));
    expect(await screen.findByText("Unarchived Beta Tale")).toBeInTheDocument();
    await waitFor(async () => expect(within(await grid()).getAllByRole("listitem")).toHaveLength(2));
    // It was not the active one, so nothing is re-selected.
    expect(callsOf("setActiveCampaign")).toEqual([]);
  });

  it("asks before archiving the Active Campaign, and names the campaign that becomes active", async () => {
    const { callsOf } = renderScreen({ campaigns: TWO, activeId: "a" });
    await waitFor(async () => expect(within(await cardOf("Alpha Quest")).getByText("Active")).toBeInTheDocument());
    await openMenu("Alpha Quest");
    // The ellipsis: this one asks first.
    choose("Archive…");

    const question = await screen.findByRole("alertdialog", { name: "Archive “Alpha Quest”?" });
    expect(question).toHaveTextContent("“Beta Tale” becomes the active campaign.");
    expect(callsOf("archiveCampaign")).toEqual([]);

    fireEvent.click(within(question).getByRole("button", { name: "Archive" }));
    await waitFor(() => expect(callsOf("archiveCampaign")).toHaveLength(1));
    expect(await screen.findByText("Archived Alpha Quest")).toBeInTheDocument();
    // Resolution moved on: Beta Tale is the active one now.
    await waitFor(async () => expect(within(await cardOf("Beta Tale")).getByText("Active")).toBeInTheDocument());

    // Undo puts it back AND makes it the active campaign again.
    fireEvent.click(screen.getByRole("button", { name: "Undo" }));
    await waitFor(() => expect(callsOf("unarchiveCampaign")).toHaveLength(1));
    await waitFor(() => expect(callsOf("setActiveCampaign")).toEqual([{ method: "setActiveCampaign", id: "a" }]));
    expect(await screen.findByText("Unarchived Alpha Quest — it is your active campaign again")).toBeInTheDocument();
    await waitFor(async () => expect(within(await cardOf("Alpha Quest")).getByText("Active")).toBeInTheDocument());
  });

  it("says no campaign will be active when the last one in use is archived", async () => {
    renderScreen({ campaigns: [TWO[0]], activeId: "a" });
    await waitFor(async () => expect(within(await cardOf("Alpha Quest")).getByText("Active")).toBeInTheDocument());
    await openMenu("Alpha Quest");
    choose("Archive…");
    expect(await screen.findByRole("alertdialog")).toHaveTextContent(
      "no campaign will be active afterwards",
    );
  });

  it("reports the server's refusal to archive the campaign of a live Voice Session", async () => {
    const { callsOf } = renderScreen({ campaigns: TWO, activeId: "a", liveOn: "a" });
    await waitFor(async () => expect(within(await cardOf("Alpha Quest")).getByText("Live")).toBeInTheDocument());
    await openMenu("Alpha Quest");
    choose("Archive…");
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Archive" }));

    await waitFor(() => expect(callsOf("archiveCampaign")).toHaveLength(1));
    expect(
      await screen.findByText("Alpha Quest is in a live voice session. End the session first, then try again."),
    ).toBeInTheDocument();
    // Still in use, and no Undo for something that did not happen.
    expect(within(await grid()).getAllByRole("listitem")).toHaveLength(2);
    expect(screen.queryByRole("button", { name: "Undo" })).not.toBeInTheDocument();
  });

  it("unarchives from the archived section", async () => {
    const { callsOf } = renderScreen({ campaigns: WITH_ARCHIVED, activeId: "a" });
    fireEvent.click(await screen.findByRole("button", { name: "Archived (1)" }));
    fireEvent.click(screen.getByRole("button", { name: "Unarchive Old Campaign" }));

    await waitFor(() => expect(callsOf("unarchiveCampaign")).toEqual([{ method: "unarchiveCampaign", id: "z" }]));
    expect(await screen.findByText("Unarchived Old Campaign")).toBeInTheDocument();
    await waitFor(async () => expect(within(await grid()).getAllByRole("listitem")).toHaveLength(3));
    expect(screen.queryByRole("button", { name: /^Archived \(/ })).not.toBeInTheDocument();
  });
});

describe("Campaigns: Delete", () => {
  it("is offered on a campaign that is not archived, behind the re-typed name, and removes it", async () => {
    const { callsOf } = renderScreen({ campaigns: TWO, activeId: "a" });
    await openMenu("Beta Tale");
    choose("Delete…");

    const dialog = await screen.findByRole("alertdialog", { name: "Delete “Beta Tale”?" });
    expect(dialog).toHaveTextContent("This archives the campaign and then permanently deletes it");
    const confirm = within(dialog).getByRole("button", { name: "Delete campaign" });
    expect(confirm).toBeDisabled();
    fireEvent.change(within(dialog).getByTestId("confirm-text-input"), { target: { value: "Beta" } });
    expect(confirm).toBeDisabled();
    expect(callsOf("deleteCampaign")).toEqual([]);

    fireEvent.change(within(dialog).getByTestId("confirm-text-input"), { target: { value: "Beta Tale" } });
    fireEvent.click(confirm);

    // The server only deletes an archived campaign: archive, then delete.
    await waitFor(() => expect(callsOf("deleteCampaign")).toEqual([{ method: "deleteCampaign", id: "b" }]));
    expect(callsOf("archiveCampaign")).toEqual([{ method: "archiveCampaign", id: "b" }]);
    expect(await screen.findByText("Deleted Beta Tale")).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole("heading", { name: "Beta Tale" })).not.toBeInTheDocument());
    expect(screen.queryByRole("button", { name: /^Archived \(/ })).not.toBeInTheDocument();
  });

  it("deletes an already archived campaign without archiving it again", async () => {
    const { callsOf } = renderScreen({ campaigns: WITH_ARCHIVED, activeId: "a" });
    fireEvent.click(await screen.findByRole("button", { name: "Archived (1)" }));
    await openMenu("Old Campaign");
    choose("Delete…");
    const dialog = await screen.findByRole("alertdialog", { name: "Delete “Old Campaign”?" });
    fireEvent.change(within(dialog).getByTestId("confirm-text-input"), { target: { value: "Old Campaign" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete campaign" }));

    await waitFor(() => expect(callsOf("deleteCampaign")).toHaveLength(1));
    expect(callsOf("archiveCampaign")).toEqual([]);
    expect(await screen.findByText("Deleted Old Campaign")).toBeInTheDocument();
  });

  it("reports the server's refusal for the campaign of a live Voice Session, and keeps it", async () => {
    const { callsOf } = renderScreen({ campaigns: TWO, activeId: "a", liveOn: "a" });
    await waitFor(async () => expect(within(await cardOf("Alpha Quest")).getByText("Live")).toBeInTheDocument());
    await openMenu("Alpha Quest");
    choose("Delete…");
    const dialog = await screen.findByRole("alertdialog", { name: "Delete “Alpha Quest”?" });
    fireEvent.change(within(dialog).getByTestId("confirm-text-input"), { target: { value: "Alpha Quest" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete campaign" }));

    expect(
      await screen.findByText("Alpha Quest is in a live voice session. End the session first, then try again."),
    ).toBeInTheDocument();
    // Refused at the archive step: nothing was deleted, the card is still in use.
    expect(callsOf("deleteCampaign")).toEqual([]);
    expect(within(await grid()).getAllByRole("listitem")).toHaveLength(2);
  });

  it("says the campaign is archived, not gone, when the delete itself fails", async () => {
    renderScreen({ campaigns: TWO, activeId: "a", errors: { deleteCampaign: "blob store offline" } });
    await openMenu("Beta Tale");
    choose("Delete…");
    const dialog = await screen.findByRole("alertdialog");
    fireEvent.change(within(dialog).getByTestId("confirm-text-input"), { target: { value: "Beta Tale" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete campaign" }));

    expect(
      await screen.findByText("Beta Tale is archived now, but couldn't be deleted: blob store offline"),
    ).toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "Archived (1)" })).toBeInTheDocument();
  });

  it("offers a full bundle from inside the delete dialog", async () => {
    mockExport.mockResolvedValue({ blob: new Blob(["gz"]), filename: "Beta Tale.glyphoxa.json.gz" });
    const { callsOf } = renderScreen({ campaigns: TWO, activeId: "a" });
    await openMenu("Beta Tale");
    choose("Delete…");
    const dialog = await screen.findByRole("alertdialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Download full bundle first" }));

    // No options: the complete bundle, history and map images included.
    await waitFor(() => expect(mockExport).toHaveBeenCalledWith("b"));
    await waitFor(() => expect(mockDownload).toHaveBeenCalledWith(expect.anything(), "Beta Tale.glyphoxa.json.gz"));
    expect(await within(dialog).findByText("Bundle downloaded.")).toBeInTheDocument();
    // Downloading is not deleting.
    expect(screen.getByRole("alertdialog")).toBeInTheDocument();
    expect(callsOf("deleteCampaign")).toEqual([]);
  });
});

describe("Campaigns: Duplicate", () => {
  const exported = { blob: new Blob(["gz"]), filename: "Beta Tale.glyphoxa.json.gz" };
  const summary = (campaignId: string) => ({
    campaignId,
    name: "Beta Tale",
    agents: 1,
    nodes: 0,
    edges: 0,
    characters: 0,
    sessions: 0,
    lines: 0,
    chunks: 0,
    droppedParticipantRefs: 0,
  });

  // The importer mints a new campaign under the bundle's name; model that on
  // the backend so the rename has something to rename.
  function importInto(state: ReturnType<typeof campaignBackend>["state"]) {
    return async () => {
      state.campaigns.push({
        id: "copy-1",
        name: "Beta Tale",
        system: "",
        language: "",
        created: "2026-11-22T12:00:00Z",
        updated: "2026-11-22T12:00:00Z",
        tapeArmed: false,
        highlightBar: 0,
        highlightConfirmWindows: 0,
      });
      return summary("copy-1");
    };
  }

  async function confirmDuplicate() {
    await openMenu("Beta Tale");
    choose("Duplicate…");
    const question = await screen.findByRole("alertdialog", { name: "Duplicate “Beta Tale”?" });
    // What is and is not copied is said before anything happens.
    expect(question).toHaveTextContent("Creates “Beta Tale (copy)”");
    expect(question).toHaveTextContent("maps (with their images)");
    expect(question).toHaveTextContent("The session history is not copied.");
    fireEvent.click(within(question).getByRole("button", { name: "Duplicate" }));
  }

  it("copies the campaign as “<name> (copy)” with its system and language, and offers to open it", async () => {
    const h = renderScreen({ campaigns: TWO, activeId: "a" });
    mockExport.mockResolvedValue(exported);
    mockImport.mockImplementation(importInto(h.state));
    await confirmDuplicate();

    // Images on, history off.
    await waitFor(() =>
      expect(mockExport).toHaveBeenCalledWith("b", { includeHistory: false, includeImages: true }),
    );
    await waitFor(() => expect(mockImport).toHaveBeenCalledWith(exported.blob, exported.filename));
    await waitFor(() => expect(h.callsOf("updateCampaign")).toHaveLength(1));
    expect(h.callsOf("updateCampaign")[0].req).toMatchObject({
      id: "copy-1",
      name: "Beta Tale (copy)",
      system: "Pathfinder 2e",
      language: "de",
    });

    expect(await screen.findByRole("heading", { level: 2, name: "Beta Tale (copy)" })).toBeInTheDocument();
    expect(await screen.findByText("Duplicated as Beta Tale (copy)")).toBeInTheDocument();

    // "Open" on the toast is the guarded switch plus the Overview.
    fireEvent.click(screen.getByRole("button", { name: "Open" }));
    await waitFor(() => expect(h.callsOf("setActiveCampaign")).toEqual([{ method: "setActiveCampaign", id: "copy-1" }]));
    await waitFor(() => expect(h.moves).toEqual([{ screen: "campaign", view: "overview", search: {} }]));
  });

  it("retries without the map images when the bundle is too large to upload, and says so", async () => {
    const h = renderScreen({ campaigns: TWO, activeId: "a" });
    mockExport.mockResolvedValue(exported);
    mockImport
      .mockRejectedValueOnce(new BundleRequestError("bundle exceeds maximum upload size", 413))
      .mockImplementation(importInto(h.state));
    await confirmDuplicate();

    await waitFor(() => expect(mockExport).toHaveBeenCalledTimes(2));
    expect(mockExport.mock.calls[1]).toEqual(["b", { includeHistory: false, includeImages: false }]);
    expect(
      await screen.findByText("Duplicated as Beta Tale (copy) — without the map images, which were too large to copy."),
    ).toBeInTheDocument();
  });

  it("shows the retry hint when the server is busy with another bundle", async () => {
    const h = renderScreen({ campaigns: TWO, activeId: "a" });
    mockExport.mockRejectedValue(
      new BundleRequestError("another campaign import or export is in progress; retry shortly", 503),
    );
    await confirmDuplicate();

    expect(
      await screen.findByText("Another import or export is running right now. Wait a few seconds, then try again."),
    ).toBeInTheDocument();
    expect(mockImport).not.toHaveBeenCalled();
    expect(h.callsOf("updateCampaign")).toEqual([]);
  });

  it("names the campaign and the reason for any other failure", async () => {
    renderScreen({ campaigns: TWO, activeId: "a" });
    mockExport.mockRejectedValue(new BundleRequestError("export failed", 500));
    await confirmDuplicate();
    expect(await screen.findByText("Couldn't duplicate Beta Tale: export failed")).toBeInTheDocument();
  });
});

describe("Campaigns: Export", () => {
  it("opens the export dialog from the card menu, for an archived campaign too", async () => {
    renderScreen({ campaigns: WITH_ARCHIVED, activeId: "a" });
    fireEvent.click(await screen.findByRole("button", { name: "Archived (1)" }));
    await openMenu("Old Campaign");
    choose("Export bundle…");
    const dialog = await screen.findByRole("dialog", { name: "Export “Old Campaign”" });
    expect(within(dialog).getByRole("switch", { name: "Session history" })).toBeChecked();
    expect(within(dialog).getByRole("switch", { name: "Map images" })).toBeChecked();
  });
});
