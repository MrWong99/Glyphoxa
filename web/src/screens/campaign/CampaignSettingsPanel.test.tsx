import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { toast } from "sonner";

import {
  CampaignSchema,
  CampaignService,
  GetActiveCampaignResponseSchema,
  ListSupportedLanguagesResponseSchema,
  UpdateCampaignResponseSchema,
  type UpdateCampaignRequest,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { hasUnsavedEdits } from "@/lib/navigationGuard";
import { makeQueryClient } from "@/lib/queryClient";
import { CampaignScreenProvider, type CampaignScreen } from "./campaignContext";
import { CampaignSettingsPanel } from "./CampaignSettingsPanel";

vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() } }));

beforeEach(() => {
  vi.mocked(toast.success).mockClear();
  vi.mocked(toast.error).mockClear();
});

// A one-campaign backend whose UpdateCampaign writes through, so the refetch
// after a save returns what was saved.
function backend(opts: { failLoads?: number; updateError?: string } = {}) {
  const state = {
    campaign: { id: "c1", name: "The Sunless Citadel", system: "D&D 5e", language: "en", tapeArmed: false },
    failLoads: opts.failLoads ?? 0,
    updates: [] as UpdateCampaignRequest[],
  };
  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      getActiveCampaign: () => {
        if (state.failLoads > 0) {
          state.failLoads--;
          throw new ConnectError("campaign store offline", Code.Unavailable);
        }
        return create(GetActiveCampaignResponseSchema, { campaign: create(CampaignSchema, state.campaign) });
      },
      listSupportedLanguages: () => create(ListSupportedLanguagesResponseSchema, { languages: ["de", "en"] }),
      updateCampaign: (req) => {
        if (opts.updateError) throw new ConnectError(opts.updateError, Code.InvalidArgument);
        state.updates.push(req);
        state.campaign = { ...state.campaign, name: req.name, system: req.system, language: req.language, tapeArmed: req.tapeArmed ?? false };
        return create(UpdateCampaignResponseSchema, { campaign: create(CampaignSchema, state.campaign) });
      },
    });
  });
  return { transport, state };
}

function renderPanel(opts: Parameters<typeof backend>[0] = {}) {
  const h = backend(opts);
  const openView = vi.fn();
  const host: CampaignScreen = {
    campaign: undefined,
    roster: [],
    openNode: vi.fn(),
    openAgent: vi.fn(),
    openView,
  };
  render(
    <Providers transport={h.transport} queryClient={makeQueryClient()}>
      <CampaignScreenProvider value={host}>
        <CampaignSettingsPanel />
      </CampaignScreenProvider>
    </Providers>,
  );
  return { ...h, openView };
}

const nameField = () => screen.getByLabelText("Name") as HTMLInputElement;

describe("CampaignSettingsPanel", () => {
  it("shows a skeleton, then the Active Campaign's settings", async () => {
    renderPanel();
    expect(screen.getByTestId("campaign-settings-loading")).toBeInTheDocument();
    await waitFor(() => expect(nameField().value).toBe("The Sunless Citadel"));
    expect((screen.getByLabelText("Game system") as HTMLInputElement).value).toBe("D&D 5e");
    expect(screen.queryByTestId("campaign-settings-loading")).not.toBeInTheDocument();
  });

  it("saves a rename, says so with a toast, and stays open and clean", async () => {
    const { state } = renderPanel();
    await waitFor(() => expect(nameField().value).toBe("The Sunless Citadel"));

    fireEvent.change(nameField(), { target: { value: "The Sunless Citadel, Revisited" } });
    await waitFor(() => expect(hasUnsavedEdits()).toBe(true));
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => expect(state.updates).toHaveLength(1));
    expect(state.updates[0]).toMatchObject({ id: "c1", name: "The Sunless Citadel, Revisited", system: "D&D 5e" });
    await waitFor(() => expect(vi.mocked(toast.success)).toHaveBeenCalledWith("Campaign settings saved"));

    // A page, not a popover: the form is still there with what was saved…
    expect(nameField().value).toBe("The Sunless Citadel, Revisited");
    // …and a saved form no longer holds navigation up.
    await waitFor(() => expect(hasUnsavedEdits()).toBe(false));
  });

  it("keeps the edits and the guard when a save is refused", async () => {
    const { state } = renderPanel({ updateError: "name already in use" });
    await waitFor(() => expect(nameField().value).toBe("The Sunless Citadel"));

    fireEvent.change(nameField(), { target: { value: "Taken" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/couldn't save.*name already in use/i);
    expect(vi.mocked(toast.success)).not.toHaveBeenCalled();
    expect(state.updates).toEqual([]);
    expect(nameField().value).toBe("Taken");
    expect(hasUnsavedEdits()).toBe(true);
  });

  it("leaves for the Overview on Cancel", async () => {
    const { openView } = renderPanel();
    await waitFor(() => expect(nameField().value).toBe("The Sunless Citadel"));
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(openView).toHaveBeenCalledWith("overview");
  });

  it("says why the settings could not be loaded, and Try again loads them", async () => {
    renderPanel({ failLoads: 1 });
    expect(await screen.findByRole("alert")).toHaveTextContent(/couldn't load the campaign.*campaign store offline/i);

    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(nameField().value).toBe("The Sunless Citadel"));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("lifts the guard when the page is left with edits still in it", async () => {
    const h = backend();
    const view = render(
      <Providers transport={h.transport} queryClient={makeQueryClient()}>
        <CampaignSettingsPanel />
      </Providers>,
    );
    await waitFor(() => expect(nameField().value).toBe("The Sunless Citadel"));
    fireEvent.change(nameField(), { target: { value: "Half an idea" } });
    await waitFor(() => expect(hasUnsavedEdits()).toBe(true));
    view.unmount();
    expect(hasUnsavedEdits()).toBe(false);
  });
});
