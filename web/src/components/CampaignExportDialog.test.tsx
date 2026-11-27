import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";

import { I18nProvider } from "@/i18n";
import { CampaignExportDialog } from "./CampaignExportDialog";

// The dialog against the REAL fetchCampaignExport: what is asserted is the URL
// the browser would request, so a regression anywhere between the switches and
// the query string shows up here. Only the save-to-disk step is stubbed (jsdom
// has no download pipeline).
vi.mock("@/lib/download", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/download")>()),
  downloadBlob: vi.fn(),
}));
import { downloadBlob } from "@/lib/download";
const mockDownload = vi.mocked(downloadBlob);

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const fetchMock = vi.fn<typeof fetch>();

beforeEach(() => {
  mockDownload.mockReset();
  fetchMock.mockReset();
  vi.stubGlobal("fetch", fetchMock);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

function ok() {
  // A plain string body, not a jsdom Blob: jsdom's Blob has no stream(), which
  // Node 22's undici needs to build a Response from one.
  return new Response("gz", {
    status: 200,
    headers: { "Content-Disposition": 'attachment; filename="Citadel.glyphoxa.json.gz"' },
  });
}

function renderDialog(onClose = vi.fn()) {
  render(
    <I18nProvider>
      <CampaignExportDialog campaign={{ id: "c1", name: "The Sunless Citadel" }} onClose={onClose} />
    </I18nProvider>,
  );
  return { onClose, dialog: screen.getByRole("dialog", { name: "Export “The Sunless Citadel”" }) };
}

const requestedUrl = () => String(fetchMock.mock.calls[0][0]);

describe("CampaignExportDialog", () => {
  it("asks for the complete bundle by default: history and map images", async () => {
    fetchMock.mockResolvedValue(ok());
    const { dialog, onClose } = renderDialog();

    expect(within(dialog).getByRole("switch", { name: "Session history" })).toBeChecked();
    expect(within(dialog).getByRole("switch", { name: "Map images" })).toBeChecked();
    fireEvent.click(within(dialog).getByRole("button", { name: "Download bundle" }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledOnce());
    expect(requestedUrl()).toBe("/api/v1/campaigns/c1/export?include_history=true&include_images=true");
    // Matched by size, not `expect.any(Blob)`: which realm's Blob `res.blob()`
    // returns depends on the Node version (Node 22 Node's, Node 24 jsdom's).
    await waitFor(() =>
      expect(mockDownload).toHaveBeenCalledWith(
        expect.objectContaining({ size: "gz".length }),
        "Citadel.glyphoxa.json.gz",
      ),
    );
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });

  it("drops the parameter of a switch that is turned off", async () => {
    fetchMock.mockResolvedValue(ok());
    const { dialog } = renderDialog();

    fireEvent.click(within(dialog).getByRole("switch", { name: "Map images" }));
    expect(within(dialog).getByRole("switch", { name: "Map images" })).not.toBeChecked();
    fireEvent.click(within(dialog).getByRole("button", { name: "Download bundle" }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledOnce());
    expect(requestedUrl()).toBe("/api/v1/campaigns/c1/export?include_history=true");
  });

  it("sends no query at all when both are off", async () => {
    fetchMock.mockResolvedValue(ok());
    const { dialog } = renderDialog();

    fireEvent.click(within(dialog).getByRole("switch", { name: "Session history" }));
    fireEvent.click(within(dialog).getByRole("switch", { name: "Map images" }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Download bundle" }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledOnce());
    expect(requestedUrl()).toBe("/api/v1/campaigns/c1/export");
  });

  it("stays open with a retry hint when the server is busy with another bundle (503)", async () => {
    fetchMock.mockResolvedValue(
      new Response("another campaign import or export is in progress; retry shortly", { status: 503 }),
    );
    const { dialog, onClose } = renderDialog();

    fireEvent.click(within(dialog).getByRole("button", { name: "Download bundle" }));

    expect(
      await within(dialog).findByText("Another import or export is running right now. Wait a few seconds, then try again."),
    ).toBeInTheDocument();
    expect(onClose).not.toHaveBeenCalled();
    expect(mockDownload).not.toHaveBeenCalled();
    // The choices are still there for the retry.
    expect(within(dialog).getByRole("button", { name: "Download bundle" })).toBeInTheDocument();
  });

  it("shows the server's reason for any other failure", async () => {
    fetchMock.mockResolvedValue(new Response("campaign not found", { status: 404 }));
    const { dialog } = renderDialog();

    fireEvent.click(within(dialog).getByRole("button", { name: "Download bundle" }));

    expect(await within(dialog).findByText("campaign not found")).toBeInTheDocument();
  });
});
