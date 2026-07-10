import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { useQuery } from "@connectrpc/connect-query";

import {
  CampaignService,
  ListCampaignsResponseSchema,
  SetActiveCampaignResponseSchema,
  CampaignSchema,
  GetCampaignRosterResponseSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { ImportCampaignButton } from "./ImportCampaignButton";

// The bundle upload rides the plain-fetch helper; mock it so the import flow is
// observable without a network/multipart round-trip.
vi.mock("@/lib/download", () => ({ importCampaignBundle: vi.fn() }));
import { importCampaignBundle, type ImportSummary } from "@/lib/download";
const mockImport = vi.mocked(importCampaignBundle);

// Toasts are the unmount-proof feedback channel (they render outside the panel),
// so assert them directly rather than only the inline alert/dialog.
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
import { toast } from "sonner";
const mockToast = vi.mocked(toast);

const summaryFixture: ImportSummary = {
  campaignId: "imported-1",
  name: "The Prancing Pony",
  agents: 2,
  nodes: 4,
  edges: 2,
  characters: 1,
  sessions: 0,
  lines: 0,
  chunks: 0,
};

// A stateful backend: SetActiveCampaign records the switch; ListCampaigns +
// getCampaignRoster let probes prove the post-import invalidation and the switch
// sweep from the screen's side.
function mockBackend(calls: Record<string, number> = {}) {
  const bump = (m: string) => {
    calls[m] = (calls[m] ?? 0) + 1;
  };
  return createRouterTransport(({ service }) => {
    service(CampaignService, {
      listCampaigns: () => {
        bump("listCampaigns");
        return create(ListCampaignsResponseSchema, { campaigns: [] });
      },
      setActiveCampaign: (req) => {
        bump("setActiveCampaign");
        return create(SetActiveCampaignResponseSchema, {
          campaign: create(CampaignSchema, { id: req.campaignId, name: "The Prancing Pony" }),
        });
      },
      getCampaignRoster: () => {
        bump("getCampaignRoster");
        return create(GetCampaignRosterResponseSchema, { roster: [] });
      },
    });
  });
}

function ListProbe() {
  const { data } = useQuery(CampaignService.method.listCampaigns, {});
  return <div data-testid="list-probe">{data ? "loaded" : "…"}</div>;
}

function RosterProbe() {
  const { data } = useQuery(CampaignService.method.getCampaignRoster, {});
  return <div data-testid="roster-probe">{data ? "loaded" : "…"}</div>;
}

function renderButton(calls?: Record<string, number>, onSwitched?: () => void) {
  return render(
    <Providers transport={mockBackend(calls)} queryClient={makeQueryClient()}>
      <ImportCampaignButton onSwitched={onSwitched} />
      <ListProbe />
      <RosterProbe />
    </Providers>,
  );
}

// pickFile fires a change on the hidden file input with a bundle File.
function pickFile(name = "bundle.glyphoxa.json.gz") {
  const input = document.querySelector<HTMLInputElement>('input[type="file"]')!;
  const file = new File(["{}"], name, { type: "application/gzip" });
  Object.defineProperty(input, "files", { value: [file], configurable: true });
  fireEvent.change(input);
}

beforeEach(() => {
  mockImport.mockReset();
  mockToast.success.mockReset();
  mockToast.error.mockReset();
});

describe("ImportCampaignButton", () => {
  it("confirms, uploads, then offers to switch to the imported campaign", async () => {
    mockImport.mockResolvedValue(summaryFixture);
    renderButton();

    // Pick a file → a confirm dialog gates the upload (no fire-on-pick).
    pickFile();
    const confirm = await screen.findByRole("alertdialog");
    expect(within(confirm).getByText(/bundle\.glyphoxa\.json\.gz/)).toBeInTheDocument();
    expect(mockImport).not.toHaveBeenCalled();

    // Confirm → the upload fires with the picked file.
    fireEvent.click(within(confirm).getByRole("button", { name: /^Import$/ }));
    await waitFor(() => expect(mockImport).toHaveBeenCalledOnce());
    expect(mockImport.mock.calls[0][0]).toBeInstanceOf(File);

    // Success dialog names the campaign and offers an explicit Switch (ADR-0053 d7:
    // import never auto-activates).
    const success = await screen.findByRole("alertdialog");
    expect(within(success).getByRole("heading", { name: /Imported.*The Prancing Pony/ })).toBeInTheDocument();
    expect(within(success).getByRole("button", { name: /Switch/ })).toBeInTheDocument();
    // A toast fires too — unmount-proof feedback if the panel closes mid-flow.
    await waitFor(() => expect(mockToast.success).toHaveBeenCalledWith(expect.stringMatching(/The Prancing Pony/)));
  });

  it("toasts the failure so feedback survives a mid-upload panel dismissal", async () => {
    mockImport.mockRejectedValue(new Error("import failed"));
    renderButton();
    pickFile();
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: /^Import$/ }));
    // The confirm dialog is already closed by Radix; without a toast a failure
    // after a panel dismissal would report nowhere.
    await waitFor(() => expect(mockToast.error).toHaveBeenCalledWith(expect.stringMatching(/import failed/)));
  });

  it("surfaces the server error message when the import fails", async () => {
    mockImport.mockRejectedValue(new Error("bundle too large (limit 32 MiB)"));
    renderButton();

    pickFile();
    const confirm = await screen.findByRole("alertdialog");
    fireEvent.click(within(confirm).getByRole("button", { name: /^Import$/ }));

    expect(await screen.findByText(/bundle too large/)).toBeInTheDocument();
  });

  it("refreshes the campaign list on import even when the switch is declined", async () => {
    mockImport.mockResolvedValue(summaryFixture);
    const calls: Record<string, number> = {};
    renderButton(calls);
    // Let the ListProbe seed the campaign-list cache first.
    await waitFor(() => expect(calls.listCampaigns).toBeGreaterThanOrEqual(1));
    const listedBefore = calls.listCampaigns;

    pickFile();
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: /^Import$/ }));

    // Success prompt is up; decline the switch (Not now).
    const success = await screen.findByRole("alertdialog");
    fireEvent.click(within(success).getByRole("button", { name: /Not now/ }));

    // listCampaigns is sweep-INVARIANT, so the import must invalidate it explicitly
    // or the freshly imported campaign is invisible in the picker until a reload.
    await waitFor(() => expect(calls.listCampaigns).toBeGreaterThan(listedBefore));
    // Declining did NOT switch.
    expect(calls.setActiveCampaign ?? 0).toBe(0);
  });

  it("switches to the imported campaign and sweeps campaign-scoped caches", async () => {
    mockImport.mockResolvedValue(summaryFixture);
    const calls: Record<string, number> = {};
    const onSwitched = vi.fn();
    renderButton(calls, onSwitched);
    // Seed the roster probe (a campaign-scoped read the sweep must refetch).
    await waitFor(() => expect(calls.getCampaignRoster).toBeGreaterThanOrEqual(1));
    const rosterBefore = calls.getCampaignRoster;

    pickFile();
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: /^Import$/ }));
    const success = await screen.findByRole("alertdialog");
    fireEvent.click(within(success).getByRole("button", { name: /Switch/ }));

    // The switch fired with the imported campaign's id…
    await waitFor(() => expect(calls.setActiveCampaign).toBe(1));
    // …its success ran the campaign-scoped sweep, refetching the roster probe…
    await waitFor(() => expect(calls.getCampaignRoster).toBeGreaterThan(rosterBefore));
    // …and told the parent so it can close the switcher panel.
    await waitFor(() => expect(onSwitched).toHaveBeenCalled());
  });
});
