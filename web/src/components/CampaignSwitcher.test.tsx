import { describe, it, expect } from "vitest";
import type { ReactNode } from "react";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { useQuery } from "@connectrpc/connect-query";

import {
  CampaignService,
  SessionService,
  CampaignSchema,
  ListCampaignsResponseSchema,
  GetActiveCampaignResponseSchema,
  GetCampaignRosterResponseSchema,
  SetActiveCampaignResponseSchema,
  CreateCampaignResponseSchema,
  GetSessionResponseSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { CampaignSwitcher } from "./CampaignSwitcher";

type Camp = { id: string; name: string; system?: string; language?: string };

// A stateful Connect backend for the switcher: ListCampaigns reflects creates,
// SetActiveCampaign moves the durable selection, and GetActiveCampaign/roster
// resolve it — so an invalidation refetch after a switch/create returns the new
// selection, proving the sweep from the screen's side. `calls` counts each RPC so
// a test can assert the sweep refetched a campaign-scoped read owned by another
// screen (getCampaignRoster). No live session is modelled for resolution;
// `sessionActive` only drives GetSession for the live-notice.
function mockBackend(
  opts: {
    campaigns?: Camp[];
    activeId?: string;
    sessionActive?: boolean;
    calls?: Record<string, number>;
    createError?: string;
    switchError?: string;
  } = {},
) {
  const state = {
    campaigns: (opts.campaigns ?? []).map((c) => ({
      id: c.id,
      name: c.name,
      system: c.system ?? "",
      language: c.language ?? "",
    })),
    activeId: opts.activeId,
    next: 1,
  };
  const bump = (m: string) => {
    if (opts.calls) opts.calls[m] = (opts.calls[m] ?? 0) + 1;
  };
  const resolveActive = () => {
    if (state.campaigns.length === 0) return undefined;
    return state.campaigns.find((c) => c.id === state.activeId) ?? state.campaigns[state.campaigns.length - 1];
  };

  return createRouterTransport(({ service }) => {
    service(CampaignService, {
      listCampaigns: () => {
        bump("listCampaigns");
        return create(ListCampaignsResponseSchema, {
          campaigns: state.campaigns.map((c) => create(CampaignSchema, c)),
        });
      },
      getActiveCampaign: () => {
        bump("getActiveCampaign");
        const a = resolveActive();
        if (!a) throw new ConnectError("no campaign", Code.NotFound);
        return create(GetActiveCampaignResponseSchema, { campaign: create(CampaignSchema, a) });
      },
      getCampaignRoster: () => {
        bump("getCampaignRoster");
        const a = resolveActive();
        if (!a) throw new ConnectError("no campaign", Code.NotFound);
        return create(GetCampaignRosterResponseSchema, { campaign: create(CampaignSchema, a), roster: [] });
      },
      setActiveCampaign: (req) => {
        bump("setActiveCampaign");
        if (opts.switchError) throw new ConnectError(opts.switchError, Code.Internal);
        const c = state.campaigns.find((x) => x.id === req.campaignId);
        if (!c) throw new ConnectError("unknown campaign", Code.NotFound);
        state.activeId = c.id;
        return create(SetActiveCampaignResponseSchema, { campaign: create(CampaignSchema, resolveActive()!) });
      },
      createCampaign: (req) => {
        bump("createCampaign");
        if (opts.createError) throw new ConnectError(opts.createError, Code.Internal);
        const c = { id: `new-${state.next++}`, name: req.name, system: req.system, language: req.language };
        state.campaigns.push(c);
        return create(CreateCampaignResponseSchema, { campaign: create(CampaignSchema, c) });
      },
    });
    service(SessionService, {
      getSession: () => {
        bump("getSession");
        return create(GetSessionResponseSchema, { active: opts.sessionActive ?? false });
      },
    });
  });
}

// RosterProbe stands in for another campaign-scoped screen (the Campaign screen's
// getCampaignRoster read). It shares the switcher's QueryClient + transport, so a
// switch's sweep must refetch it — the "updates every campaign-scoped screen
// without reload" contract.
function RosterProbe() {
  const { data } = useQuery(CampaignService.method.getCampaignRoster, {});
  return <div data-testid="roster-probe">{data?.campaign?.name ?? "…"}</div>;
}

function renderSwitcher(transport = mockBackend(), extra?: ReactNode) {
  return render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <CampaignSwitcher />
      {extra}
    </Providers>,
  );
}

const openPanel = () => fireEvent.click(screen.getByTestId("campaign-switcher-trigger"));

describe("CampaignSwitcher", () => {
  it("lists the campaigns and marks the resolved active one", async () => {
    renderSwitcher(
      mockBackend({
        campaigns: [
          { id: "a", name: "Alpha Quest", system: "dnd5e" },
          { id: "b", name: "Beta Tale", system: "pf2e" },
        ],
        activeId: "b",
      }),
    );

    // The trigger labels the active campaign — including in its accessible name.
    expect(await screen.findByText("Beta Tale")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /switch campaign — active: Beta Tale/i }),
    ).toBeInTheDocument();

    openPanel();
    const list = await screen.findByRole("group", { name: /campaigns/i });
    const beta = within(list).getByRole("button", { name: /Beta Tale/ });
    const alpha = within(list).getByRole("button", { name: /Alpha Quest/ });
    // The active campaign carries aria-current; the other doesn't.
    expect(beta).toHaveAttribute("aria-current", "true");
    expect(alpha).not.toHaveAttribute("aria-current");
    // No live session → no takes-effect notice (the "only while live" half of #266).
    expect(screen.queryByText(/takes effect after this session/i)).not.toBeInTheDocument();
  });

  it("switches the active campaign and sweeps campaign-scoped caches (no reload)", async () => {
    const calls: Record<string, number> = {};
    renderSwitcher(
      mockBackend({
        campaigns: [
          { id: "a", name: "Alpha Quest" },
          { id: "b", name: "Beta Tale" },
        ],
        activeId: "b",
        calls,
      }),
      <RosterProbe />,
    );

    // Both the switcher and the roster probe have loaded against campaign B.
    expect(await screen.findByText("Beta Tale")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByTestId("roster-probe")).toHaveTextContent("Beta Tale"));
    const rosterBefore = calls.getCampaignRoster;

    // Switch to campaign A.
    openPanel();
    const list = await screen.findByRole("group", { name: /campaigns/i });
    fireEvent.click(within(list).getByRole("button", { name: /Alpha Quest/ }));

    // The durable selection moved…
    await waitFor(() => expect(calls.setActiveCampaign).toBe(1));
    // …the switcher's own header refetched (GetActiveCampaign is in the sweep) —
    // asserted via the trigger's accessible name so it can't match the roster probe.
    expect(
      await screen.findByRole("button", { name: /switch campaign — active: Alpha Quest/i }),
    ).toBeInTheDocument();
    // …and the sweep refetched the roster screen's cache without a reload.
    await waitFor(() => expect(calls.getCampaignRoster).toBeGreaterThan(rosterBefore));
    expect(screen.getByTestId("roster-probe")).toHaveTextContent("Alpha Quest");
    // The panel closed on a successful switch.
    expect(screen.queryByRole("group", { name: /campaigns/i })).not.toBeInTheDocument();
  });

  it("creates a campaign from the switcher, pre-filled with the seed defaults, and makes it active", async () => {
    const calls: Record<string, number> = {};
    renderSwitcher(mockBackend({ campaigns: [{ id: "a", name: "Alpha Quest" }], activeId: "a", calls }));

    expect(await screen.findByText("Alpha Quest")).toBeInTheDocument();
    openPanel();
    fireEvent.click(await screen.findByRole("button", { name: /new campaign/i }));

    // The form seeds System/Language with the seeder's defaults.
    const name = await screen.findByLabelText("Name");
    expect((screen.getByLabelText("System") as HTMLInputElement).value).toBe("dnd5e");
    expect((screen.getByLabelText("Language") as HTMLInputElement).value).toBe("en");

    fireEvent.change(name, { target: { value: "Gamma Saga" } });
    fireEvent.click(screen.getByRole("button", { name: /^create campaign$/i }));

    // Create then activate, then the panel closes on the new active campaign.
    await waitFor(() => expect(calls.createCampaign).toBe(1));
    await waitFor(() => expect(calls.setActiveCampaign).toBe(1));
    expect(await screen.findByText("Gamma Saga")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^create campaign$/i })).not.toBeInTheDocument();
  });

  it("requires a name before the create can be submitted", async () => {
    renderSwitcher(mockBackend({ campaigns: [{ id: "a", name: "Alpha Quest" }], activeId: "a" }));
    await screen.findByText("Alpha Quest");
    openPanel();
    fireEvent.click(await screen.findByRole("button", { name: /new campaign/i }));
    // Name empty → the create button is disabled.
    expect(await screen.findByRole("button", { name: /^create campaign$/i })).toBeDisabled();
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Named" } });
    expect(screen.getByRole("button", { name: /^create campaign$/i })).toBeEnabled();
  });

  it("offers a create-first-campaign call to action when there are no campaigns", async () => {
    const calls: Record<string, number> = {};
    renderSwitcher(mockBackend({ campaigns: [], calls }));

    // The trigger becomes a first-run CTA and opens straight into the create form.
    const trigger = await screen.findByRole("button", { name: /create your first campaign/i });
    fireEvent.click(trigger);

    const name = await screen.findByLabelText("Name");
    fireEvent.change(name, { target: { value: "First Campaign" } });
    fireEvent.click(screen.getByRole("button", { name: /^create campaign$/i }));

    await waitFor(() => expect(calls.createCampaign).toBe(1));
    await waitFor(() => expect(calls.setActiveCampaign).toBe(1));
    // The new campaign is active; the trigger now shows it.
    expect(await screen.findByText("First Campaign")).toBeInTheDocument();
  });

  it("shows the takes-effect-after-this-session notice while a session is live", async () => {
    renderSwitcher(
      mockBackend({ campaigns: [{ id: "a", name: "Alpha Quest" }], activeId: "a", sessionActive: true }),
    );
    await screen.findByText("Alpha Quest");
    openPanel();
    expect(await screen.findByText(/takes effect after this session/i)).toBeInTheDocument();
  });

  it("surfaces a create failure inline without closing the form", async () => {
    renderSwitcher(
      mockBackend({ campaigns: [{ id: "a", name: "Alpha Quest" }], activeId: "a", createError: "database is down" }),
    );
    await screen.findByText("Alpha Quest");
    openPanel();
    fireEvent.click(await screen.findByRole("button", { name: /new campaign/i }));
    fireEvent.change(await screen.findByLabelText("Name"), { target: { value: "Doomed" } });
    fireEvent.click(screen.getByRole("button", { name: /^create campaign$/i }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/couldn't create/i);
    expect(alert).toHaveTextContent(/database is down/);
    // The form stays open for a retry.
    expect(screen.getByLabelText("Name")).toBeInTheDocument();
  });

  it("does not leak a prior create failure onto a freshly reopened create form", async () => {
    renderSwitcher(
      mockBackend({ campaigns: [{ id: "a", name: "Alpha Quest" }], activeId: "a", createError: "database is down" }),
    );
    await screen.findByText("Alpha Quest");

    // First attempt fails, leaving the inline alert.
    openPanel();
    fireEvent.click(await screen.findByRole("button", { name: /new campaign/i }));
    fireEvent.change(await screen.findByLabelText("Name"), { target: { value: "Doomed" } });
    fireEvent.click(screen.getByRole("button", { name: /^create campaign$/i }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/couldn't create/i);

    // Close the panel (trigger toggle) and reopen into a fresh create form.
    openPanel(); // closes
    openPanel(); // reopens (list mode)
    fireEvent.click(await screen.findByRole("button", { name: /new campaign/i }));

    // The stale error must be gone and the name field empty — the mutations live on
    // the long-lived switcher, so re-entering create mode resets them.
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect((screen.getByLabelText("Name") as HTMLInputElement).value).toBe("");
  });

  it("retries only activation (no duplicate create) when the create succeeds but activation fails", async () => {
    const calls: Record<string, number> = {};
    renderSwitcher(
      mockBackend({
        campaigns: [{ id: "a", name: "Alpha Quest" }],
        activeId: "a",
        calls,
        switchError: "activate boom",
      }),
    );
    await screen.findByText("Alpha Quest");
    openPanel();
    fireEvent.click(await screen.findByRole("button", { name: /new campaign/i }));
    fireEvent.change(await screen.findByLabelText("Name"), { target: { value: "Gamma Saga" } });
    fireEvent.click(screen.getByRole("button", { name: /^create campaign$/i }));

    // The campaign WAS created; only activation failed — the alert says so (not a
    // false "couldn't create"), and the button re-labels to a pure activation retry.
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/couldn't switch to it/i);
    await waitFor(() => expect(calls.createCampaign).toBe(1));
    expect(calls.setActiveCampaign).toBe(1);

    // Retrying re-runs ONLY the activation — it must not mint a second campaign.
    fireEvent.click(screen.getByRole("button", { name: /retry activation/i }));
    await waitFor(() => expect(calls.setActiveCampaign).toBe(2));
    expect(calls.createCampaign).toBe(1);
  });
});
