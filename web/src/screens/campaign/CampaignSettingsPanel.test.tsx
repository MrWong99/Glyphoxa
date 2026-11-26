import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { toast, Toaster } from "sonner";

import { Providers } from "@/app/Providers";
import { MemoryScreenNav, type ScreenLocation } from "@/app/screenNav";
import { resetLocalCampaignSwitch } from "@/components/useCampaignSwitch";
import { hasUnsavedEdits } from "@/lib/navigationGuard";
import { makeQueryClient } from "@/lib/queryClient";
import { campaignBackend, type Camp } from "@/screens/campaigns/testBackend";
import { CampaignSettingsPanel } from "./CampaignSettingsPanel";

// The bundle endpoints are plain fetches; mock the calls and keep the real
// error class and its predicates.
vi.mock("@/lib/download", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/download")>();
  return { ...actual, fetchCampaignExport: vi.fn(), importCampaignBundle: vi.fn(), downloadBlob: vi.fn() };
});
import { downloadBlob, fetchCampaignExport } from "@/lib/download";
const mockExport = vi.mocked(fetchCampaignExport);
const mockDownload = vi.mocked(downloadBlob);

beforeEach(() => {
  mockExport.mockReset();
  mockDownload.mockReset();
  resetLocalCampaignSwitch();
});

afterEach(() => {
  // The real toast host is mounted; clear it between tests.
  act(() => {
    toast.dismiss();
  });
});

const CITADEL: Camp = {
  id: "c1",
  name: "The Sunless Citadel",
  system: "D&D 5e",
  language: "en",
  created: "2026-01-05T12:00:00Z",
};
const SALTMARSH: Camp = {
  id: "c2",
  name: "Tides over Saltmarsh",
  system: "D&D 5e",
  language: "en",
  created: "2026-03-01T12:00:00Z",
};

function renderPanel(opts: Parameters<typeof campaignBackend>[0] = {}) {
  const h = campaignBackend({ campaigns: [CITADEL, SALTMARSH], activeId: "c1", ...opts });
  const moves: ScreenLocation[] = [];
  const view = render(
    <Providers transport={h.transport} queryClient={makeQueryClient()}>
      <MemoryScreenNav initial={{ screen: "campaign", view: "settings" }} onNavigate={(next) => moves.push(next)}>
        <CampaignSettingsPanel />
      </MemoryScreenNav>
      <Toaster />
    </Providers>,
  );
  return { ...h, moves, view };
}

const nameField = () => screen.getByLabelText("Name") as HTMLInputElement;
const loaded = () => waitFor(() => expect(nameField().value).toBe("The Sunless Citadel"));

describe("CampaignSettingsPanel", () => {
  it("shows a skeleton, then the Active Campaign's settings in four sections", async () => {
    renderPanel();
    // Named for assistive tech, not just drawn.
    expect(screen.getByRole("status", { name: "Loading…" })).toBeInTheDocument();
    await loaded();
    expect(screen.queryByRole("status", { name: "Loading…" })).not.toBeInTheDocument();
    expect((screen.getByLabelText("Game system") as HTMLInputElement).value).toBe("D&D 5e");

    expect(screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent)).toEqual([
      "General",
      "Session Highlights",
      "Data",
      "Danger zone",
    ]);
    // Recording and its two settings are in the open, not behind "Advanced".
    const highlights = within(screen.getByRole("region", { name: "Session Highlights" }));
    expect(highlights.getByRole("switch", { name: "Highlight recording" })).not.toBeChecked();
    expect(highlights.getByLabelText(/highlight score bar/i)).toBeInTheDocument();
    expect(highlights.getByLabelText(/confirmation windows/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /advanced settings/i })).not.toBeInTheDocument();
  });

  it("saves a rename, says so, and stays open and clean", async () => {
    const { callsOf } = renderPanel();
    await loaded();

    fireEvent.change(nameField(), { target: { value: "The Sunless Citadel, Revisited" } });
    // The unsaved marker, and the route guard, both follow the edit.
    expect(screen.getByText("Unsaved changes")).toBeInTheDocument();
    await waitFor(() => expect(hasUnsavedEdits()).toBe(true));
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => expect(callsOf("updateCampaign")).toHaveLength(1));
    expect(callsOf("updateCampaign")[0].req).toMatchObject({
      id: "c1",
      name: "The Sunless Citadel, Revisited",
      system: "D&D 5e",
    });
    expect(await screen.findByText("Campaign settings saved")).toBeInTheDocument();

    // A page, not a popover: the form is still there with what was saved…
    expect(nameField().value).toBe("The Sunless Citadel, Revisited");
    expect(screen.queryByText("Unsaved changes")).not.toBeInTheDocument();
    // …and a saved form no longer holds navigation up.
    await waitFor(() => expect(hasUnsavedEdits()).toBe(false));
  });

  it("keeps the edits and the guard when a save is refused", async () => {
    const { callsOf } = renderPanel({ errors: { updateCampaign: "name already in use" } });
    await loaded();

    fireEvent.change(nameField(), { target: { value: "Taken" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/couldn't save.*name already in use/i);
    expect(screen.queryByText("Campaign settings saved")).not.toBeInTheDocument();
    expect(callsOf("updateCampaign")).toEqual([]);
    expect(nameField().value).toBe("Taken");
    expect(hasUnsavedEdits()).toBe(true);
  });

  it("Discard changes goes back to what is saved and lifts the guard", async () => {
    renderPanel();
    await loaded();
    fireEvent.change(nameField(), { target: { value: "Half an idea" } });
    await waitFor(() => expect(hasUnsavedEdits()).toBe(true));

    fireEvent.click(screen.getByRole("button", { name: "Discard changes" }));
    expect(nameField().value).toBe("The Sunless Citadel");
    await waitFor(() => expect(hasUnsavedEdits()).toBe(false));
  });

  it("says why the settings could not be loaded, and Try again loads them", async () => {
    renderPanel({ failActive: 1 });
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Couldn't load this campaign's settings");
    expect(alert).toHaveTextContent("campaign store offline");

    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await loaded();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("lifts the guard when the page is left with edits still in it", async () => {
    const { view } = renderPanel();
    await loaded();
    fireEvent.change(nameField(), { target: { value: "Half an idea" } });
    await waitFor(() => expect(hasUnsavedEdits()).toBe(true));
    view.unmount();
    expect(hasUnsavedEdits()).toBe(false);
  });
});

describe("CampaignSettingsPanel: Data", () => {
  it("exports this campaign's bundle, complete by default", async () => {
    mockExport.mockResolvedValue({ blob: new Blob(["gz"]), filename: "The Sunless Citadel.glyphoxa.json.gz" });
    renderPanel();
    await loaded();

    fireEvent.click(
      within(screen.getByRole("region", { name: "Data" })).getByRole("button", { name: "Export bundle…" }),
    );
    const dialog = await screen.findByRole("dialog", { name: "Export “The Sunless Citadel”" });
    fireEvent.click(within(dialog).getByRole("button", { name: "Download bundle" }));

    await waitFor(() =>
      expect(mockExport).toHaveBeenCalledWith("c1", { includeHistory: true, includeImages: true }),
    );
    await waitFor(() =>
      expect(mockDownload).toHaveBeenCalledWith(expect.anything(), "The Sunless Citadel.glyphoxa.json.gz"),
    );
    expect(await screen.findByText("Bundle of The Sunless Citadel downloaded")).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });
});

describe("CampaignSettingsPanel: Danger zone", () => {
  const danger = () => within(screen.getByRole("region", { name: "Danger zone" }));

  it("asks before archiving, names the campaign that becomes active, then leaves for the Campaigns page", async () => {
    const { callsOf, moves } = renderPanel();
    await loaded();
    fireEvent.click(danger().getByRole("button", { name: "Archive…" }));

    const question = await screen.findByRole("alertdialog", { name: "Archive “The Sunless Citadel”?" });
    // Saltmarsh is the most recently created campaign left: the server's fallback.
    expect(question).toHaveTextContent("“Tides over Saltmarsh” becomes the active campaign.");
    expect(callsOf("archiveCampaign")).toEqual([]);

    fireEvent.click(within(question).getByRole("button", { name: "Archive" }));
    await waitFor(() => expect(callsOf("archiveCampaign")).toEqual([{ method: "archiveCampaign", id: "c1" }]));
    // This page is about a campaign that is no longer the active one.
    await waitFor(() => expect(moves).toEqual([{ screen: "campaigns", view: undefined, search: {} }]));
    // …and the archive can still be taken back.
    expect(await screen.findByText("Archived The Sunless Citadel")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Undo" })).toBeInTheDocument();
  });

  it("backing out of the archive question changes nothing", async () => {
    const { callsOf, moves } = renderPanel();
    await loaded();
    fireEvent.click(danger().getByRole("button", { name: "Archive…" }));
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
    expect(callsOf("archiveCampaign")).toEqual([]);
    expect(moves).toEqual([]);
  });

  it("deletes behind the re-typed name: archives, deletes, and leaves for the Campaigns page", async () => {
    const { callsOf, moves } = renderPanel();
    await loaded();
    fireEvent.click(danger().getByRole("button", { name: "Delete…" }));

    const dialog = await screen.findByRole("alertdialog", { name: "Delete “The Sunless Citadel”?" });
    expect(dialog).toHaveTextContent("“Tides over Saltmarsh” becomes the active campaign.");
    const confirm = within(dialog).getByRole("button", { name: "Delete campaign" });
    expect(confirm).toBeDisabled();
    fireEvent.change(within(dialog).getByTestId("confirm-text-input"), {
      target: { value: "The Sunless Citadel" },
    });
    fireEvent.click(confirm);

    await waitFor(() => expect(callsOf("deleteCampaign")).toEqual([{ method: "deleteCampaign", id: "c1" }]));
    expect(callsOf("archiveCampaign")).toEqual([{ method: "archiveCampaign", id: "c1" }]);
    expect(await screen.findByText("Deleted The Sunless Citadel")).toBeInTheDocument();
    await waitFor(() => expect(moves).toEqual([{ screen: "campaigns", view: undefined, search: {} }]));
  });

  it("leaves for the Campaigns page when the campaign got archived but the delete failed", async () => {
    const { callsOf, moves } = renderPanel({ errors: { deleteCampaign: "blob store offline" } });
    await loaded();
    fireEvent.click(danger().getByRole("button", { name: "Delete…" }));
    const dialog = await screen.findByRole("alertdialog");
    fireEvent.change(within(dialog).getByTestId("confirm-text-input"), {
      target: { value: "The Sunless Citadel" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete campaign" }));

    expect(
      await screen.findByText("The Sunless Citadel is archived now, but couldn't be deleted: blob store offline"),
    ).toBeInTheDocument();
    expect(callsOf("archiveCampaign")).toHaveLength(1);
    // It is no longer the Active Campaign: staying would show the next one's
    // settings under this campaign's Danger zone.
    await waitFor(() => expect(moves).toEqual([{ screen: "campaigns", view: undefined, search: {} }]));
  });

  it("reports the server's refusal while a Voice Session is live in the campaign, and stays", async () => {
    const { callsOf, moves } = renderPanel({ liveOn: "c1" });
    await loaded();
    fireEvent.click(danger().getByRole("button", { name: "Delete…" }));
    const dialog = await screen.findByRole("alertdialog");
    fireEvent.change(within(dialog).getByTestId("confirm-text-input"), {
      target: { value: "The Sunless Citadel" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete campaign" }));

    expect(
      await screen.findByText(
        "The Sunless Citadel is in a live voice session. End the session first, then try again.",
      ),
    ).toBeInTheDocument();
    expect(callsOf("deleteCampaign")).toEqual([]);
    expect(moves).toEqual([]);
    expect(nameField().value).toBe("The Sunless Citadel");
  });
});
