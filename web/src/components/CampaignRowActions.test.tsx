import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { useQuery } from "@connectrpc/connect-query";

import {
  CampaignService,
  ListCampaignsResponseSchema,
  ArchiveCampaignResponseSchema,
  UnarchiveCampaignResponseSchema,
  DeleteCampaignResponseSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { CampaignRowActions } from "./CampaignRowActions";

// Export runs through the plain-fetch bundle helpers; mock them so the row's
// Export flow is observable without a real network/download pipeline.
vi.mock("@/lib/download", () => ({
  fetchCampaignExport: vi.fn(),
  downloadBlob: vi.fn(),
}));
import { fetchCampaignExport, downloadBlob } from "@/lib/download";
const mockFetchExport = vi.mocked(fetchCampaignExport);
const mockDownloadBlob = vi.mocked(downloadBlob);

beforeEach(() => {
  mockFetchExport.mockReset();
  mockDownloadBlob.mockReset();
});

// A minimal Connect backend recording each lifecycle call so the tests assert the
// right RPC fired. ListCampaigns is served so the post-mutation invalidation has a
// key to refetch (a ListProbe observes it).
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
      archiveCampaign: () => {
        bump("archiveCampaign");
        return create(ArchiveCampaignResponseSchema, {});
      },
      unarchiveCampaign: () => {
        bump("unarchiveCampaign");
        return create(UnarchiveCampaignResponseSchema, {});
      },
      deleteCampaign: () => {
        bump("deleteCampaign");
        return create(DeleteCampaignResponseSchema, {});
      },
    });
  });
}

// ListProbe observes the listCampaigns cache so a mutation's invalidation
// (refetch) is provable from the screen's side.
function ListProbe() {
  const { data } = useQuery(CampaignService.method.listCampaigns, {});
  return <div data-testid="list-probe">{data ? "loaded" : "…"}</div>;
}

function renderActions(campaign: { id: string; name: string; archived: boolean }, calls?: Record<string, number>) {
  return render(
    <Providers transport={mockBackend(calls)} queryClient={makeQueryClient()}>
      <CampaignRowActions campaign={campaign} />
      <ListProbe />
    </Providers>,
  );
}

const openMenu = () => fireEvent.click(screen.getByRole("button", { name: /Campaign actions/i }));

describe("CampaignRowActions", () => {
  it("an active row offers only Archive", () => {
    renderActions({ id: "a", name: "Alpha Quest", archived: false });
    openMenu();
    expect(screen.getByRole("menuitem", { name: /^Archive$/ })).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: /Unarchive/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: /Delete/ })).not.toBeInTheDocument();
  });

  it("an archived row offers Unarchive and Delete", () => {
    renderActions({ id: "a", name: "Alpha Quest", archived: true });
    openMenu();
    expect(screen.getByRole("menuitem", { name: /Unarchive/ })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: /Delete/ })).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: /^Archive$/ })).not.toBeInTheDocument();
  });

  it("archiving an active campaign fires archiveCampaign and refreshes the list", async () => {
    const calls: Record<string, number> = {};
    renderActions({ id: "a", name: "Alpha Quest", archived: false }, calls);
    await waitFor(() => expect(calls.listCampaigns).toBeGreaterThanOrEqual(1));
    const listBefore = calls.listCampaigns;

    openMenu();
    fireEvent.click(screen.getByRole("menuitem", { name: /^Archive$/ }));

    await waitFor(() => expect(calls.archiveCampaign).toBe(1));
    // The list refetched (invalidation) so the row moves between groups.
    await waitFor(() => expect(calls.listCampaigns).toBeGreaterThan(listBefore));
  });

  it("unarchiving an archived campaign fires unarchiveCampaign", async () => {
    const calls: Record<string, number> = {};
    renderActions({ id: "a", name: "Alpha Quest", archived: true }, calls);
    openMenu();
    fireEvent.click(screen.getByRole("menuitem", { name: /Unarchive/ }));
    await waitFor(() => expect(calls.unarchiveCampaign).toBe(1));
  });

  it("renders the open menu outside the row wrapper so the scrollable list can't clip it", () => {
    // The row sits inside .gx-campaign-switcher__list (overflow-y:auto), so an
    // in-flow absolute menu gets clipped near the bottom of the list (#338). The
    // menu must portal out of the anchor's subtree to escape that overflow.
    renderActions({ id: "a", name: "Alpha Quest", archived: true });
    openMenu();
    const menu = screen.getByRole("menu");
    const wrapper = document.querySelector(".gx-campaign-row-actions");
    expect(wrapper).not.toBeNull();
    expect(wrapper?.contains(menu)).toBe(false);
  });

  it("moves focus into the menu on open and back to the trigger on close (#338)", () => {
    // Portalled to document.body, the menu sits at the end of the tab order, so
    // Tab from the trigger would skip it. Focus must land on the first item on
    // open and return to the trigger on dismissal.
    renderActions({ id: "a", name: "Alpha Quest", archived: false });
    const trigger = screen.getByRole("button", { name: /Campaign actions/i });
    fireEvent.click(trigger);
    // Export now leads the menu, so it's the first enabled item focus lands on.
    expect(screen.getByRole("menuitem", { name: /Export/ })).toHaveFocus();

    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  });

  it("closes when the surrounding list scrolls so it can't float over unrelated UI (#338)", () => {
    renderActions({ id: "a", name: "Alpha Quest", archived: false });
    openMenu();
    expect(screen.getByRole("menu")).toBeInTheDocument();
    fireEvent.scroll(window);
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
  });

  it("delete requires the re-typed campaign name before it fires", async () => {
    const calls: Record<string, number> = {};
    renderActions({ id: "a", name: "Lost Mine", archived: true }, calls);
    openMenu();
    fireEvent.click(screen.getByRole("menuitem", { name: /Delete/ }));

    // The confirm dialog opens with the delete button disabled until the exact
    // name is typed.
    const confirm = await screen.findByRole("button", { name: "Delete campaign" });
    expect(confirm).toBeDisabled();
    fireEvent.click(confirm);
    expect(calls.deleteCampaign ?? 0).toBe(0);

    fireEvent.change(screen.getByTestId("confirm-text-input"), { target: { value: "Lost Mine" } });
    expect(confirm).toBeEnabled();
    fireEvent.click(confirm);
    await waitFor(() => expect(calls.deleteCampaign).toBe(1));
  });

  it("offers Export on an active row and downloads the fetched bundle", async () => {
    mockFetchExport.mockResolvedValue({
      blob: new Blob(["gz"]),
      filename: "Alpha Quest.glyphoxa.json.gz",
    });
    renderActions({ id: "a", name: "Alpha Quest", archived: false });
    openMenu();
    fireEvent.click(screen.getByRole("menuitem", { name: /Export/ }));

    await waitFor(() => expect(mockFetchExport).toHaveBeenCalledWith("a"));
    await waitFor(() =>
      expect(mockDownloadBlob).toHaveBeenCalledWith(expect.any(Blob), "Alpha Quest.glyphoxa.json.gz"),
    );
  });

  it("offers Export on an archived row too (bundles are a backup)", () => {
    renderActions({ id: "z", name: "Zombie Vault", archived: true });
    openMenu();
    expect(screen.getByRole("menuitem", { name: /Export/ })).toBeInTheDocument();
  });

  it("surfaces an export failure and does not download", async () => {
    mockFetchExport.mockRejectedValue(new Error("export exploded"));
    renderActions({ id: "a", name: "Alpha Quest", archived: false });
    openMenu();
    fireEvent.click(screen.getByRole("menuitem", { name: /Export/ }));

    await waitFor(() => expect(mockFetchExport).toHaveBeenCalled());
    expect(mockDownloadBlob).not.toHaveBeenCalled();
  });
});
