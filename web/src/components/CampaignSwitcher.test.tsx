import { describe, it, expect } from "vitest";
import type { ReactNode } from "react";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
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
  ArchiveCampaignResponseSchema,
  UnarchiveCampaignResponseSchema,
  DeleteCampaignResponseSchema,
  GetSessionResponseSchema,
  ListSupportedLanguagesResponseSchema,
  UpdateCampaignResponseSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { CampaignSwitcher } from "./CampaignSwitcher";

type Camp = { id: string; name: string; system?: string; language?: string; archived?: boolean };

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
    activeError?: string;
  } = {},
) {
  const state = {
    campaigns: (opts.campaigns ?? []).map((c) => ({
      id: c.id,
      name: c.name,
      system: c.system ?? "",
      language: c.language ?? "",
      // archived campaigns carry an archivedAt timestamp on the wire (#269).
      archivedAt: c.archived ? timestampFromDate(new Date("2026-07-09T00:00:00Z")) : undefined,
    })),
    activeId: opts.activeId,
    next: 1,
  };
  const bump = (m: string) => {
    if (opts.calls) opts.calls[m] = (opts.calls[m] ?? 0) + 1;
  };
  const isArchived = (c: { archivedAt?: unknown }) => c.archivedAt !== undefined;
  const resolveActive = () => {
    const active = state.campaigns.filter((c) => !isArchived(c));
    if (active.length === 0) return undefined;
    return active.find((c) => c.id === state.activeId) ?? active[active.length - 1];
  };

  return createRouterTransport(({ service }) => {
    service(CampaignService, {
      listCampaigns: (req) => {
        bump("listCampaigns");
        const rows = req.includeArchived ? state.campaigns : state.campaigns.filter((c) => !isArchived(c));
        return create(ListCampaignsResponseSchema, {
          campaigns: rows.map((c) => create(CampaignSchema, c)),
        });
      },
      archiveCampaign: (req) => {
        bump("archiveCampaign");
        const c = state.campaigns.find((x) => x.id === req.id);
        if (c) c.archivedAt = timestampFromDate(new Date("2026-07-09T00:00:00Z"));
        return create(ArchiveCampaignResponseSchema, { campaign: c && create(CampaignSchema, c) });
      },
      unarchiveCampaign: (req) => {
        bump("unarchiveCampaign");
        const c = state.campaigns.find((x) => x.id === req.id);
        if (c) c.archivedAt = undefined;
        return create(UnarchiveCampaignResponseSchema, { campaign: c && create(CampaignSchema, c) });
      },
      deleteCampaign: (req) => {
        bump("deleteCampaign");
        state.campaigns = state.campaigns.filter((x) => x.id !== req.id);
        return create(DeleteCampaignResponseSchema, {});
      },
      getActiveCampaign: () => {
        bump("getActiveCampaign");
        // A non-NotFound resolve failure (not first-run): the trigger falls back
        // to "Select campaign" with no resolved active campaign, so any
        // active-campaign-gated control (the settings button) must disable.
        if (opts.activeError) throw new ConnectError(opts.activeError, Code.Internal);
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
        const c = { id: `new-${state.next++}`, name: req.name, system: req.system, language: req.language, archivedAt: undefined };
        state.campaigns.push(c);
        return create(CreateCampaignResponseSchema, { campaign: create(CampaignSchema, c) });
      },
      listSupportedLanguages: () => {
        bump("listSupportedLanguages");
        return create(ListSupportedLanguagesResponseSchema, { languages: ["de", "en"] });
      },
      updateCampaign: (req) => {
        bump("updateCampaign");
        const c = state.campaigns.find((x) => x.id === req.id);
        if (!c) throw new ConnectError("unknown campaign", Code.NotFound);
        c.name = req.name;
        c.system = req.system;
        c.language = req.language;
        return create(UpdateCampaignResponseSchema, { campaign: create(CampaignSchema, c) });
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
    // Rows arrive async: the list is fetched lazily when the panel opens.
    const beta = await within(list).findByRole("button", { name: /Beta Tale/ });
    const alpha = within(list).getByRole("button", { name: /Alpha Quest/ });
    // The active campaign carries aria-current; the other doesn't.
    expect(beta).toHaveAttribute("aria-current", "true");
    expect(alpha).not.toHaveAttribute("aria-current");
    // No live Voice Session → no takes-effect notice (the "only while live" half of #266).
    expect(screen.queryByText(/takes effect after it ends/i)).not.toBeInTheDocument();
  });

  it("does not fetch the campaign list until the panel opens", async () => {
    const calls: Record<string, number> = {};
    renderSwitcher(
      mockBackend({ campaigns: [{ id: "a", name: "Alpha Quest" }], activeId: "a", calls }),
    );

    // The trigger label resolves from getActiveCampaign alone — the list stays
    // unfetched while the switcher just sits in the topbar of every screen.
    expect(await screen.findByText("Alpha Quest")).toBeInTheDocument();
    expect(calls.listCampaigns ?? 0).toBe(0);

    openPanel();
    await screen.findByRole("group", { name: /campaigns/i });
    await waitFor(() => expect(calls.listCampaigns).toBe(1));
  });

  it("closes without an RPC when the active campaign row is clicked", async () => {
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
    );

    expect(await screen.findByText("Beta Tale")).toBeInTheDocument();
    openPanel();
    const list = await screen.findByRole("group", { name: /campaigns/i });
    fireEvent.click(await within(list).findByRole("button", { name: /Beta Tale/ }));

    // Re-selecting the current campaign is a no-op: the panel just closes — no
    // SetActiveCampaign write, no cache sweep.
    expect(screen.queryByRole("group", { name: /campaigns/i })).not.toBeInTheDocument();
    expect(calls.setActiveCampaign ?? 0).toBe(0);
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
    fireEvent.click(await within(list).findByRole("button", { name: /Alpha Quest/ }));

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

  it("shows the takes-effect notice while a Voice Session is live — in list AND create mode", async () => {
    renderSwitcher(
      mockBackend({ campaigns: [{ id: "a", name: "Alpha Quest" }], activeId: "a", sessionActive: true }),
    );
    await screen.findByText("Alpha Quest");
    openPanel();
    expect(await screen.findByText(/takes effect after it ends/i)).toBeInTheDocument();

    // A create also ends in SetActiveCampaign, so the deferral cue must not
    // disappear when the operator moves to the create form — without it, a
    // mid-Voice-Session create closes onto an unchanged trigger and reads as a
    // silent failure.
    fireEvent.click(screen.getByRole("button", { name: /new campaign/i }));
    await screen.findByLabelText("Name");
    expect(screen.getByText(/takes effect after it ends/i)).toBeInTheDocument();
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

  it("toggles the archived campaigns into the list via Show archived", async () => {
    const calls: Record<string, number> = {};
    renderSwitcher(
      mockBackend({
        campaigns: [
          { id: "a", name: "Alpha Quest" },
          { id: "z", name: "Zombie Vault", archived: true },
        ],
        activeId: "a",
        calls,
      }),
    );
    await screen.findByText("Alpha Quest");
    openPanel();

    // Default: only the active campaign shows; the archived one is hidden.
    const list = await screen.findByRole("group", { name: /campaigns/i });
    await within(list).findByRole("button", { name: /Alpha Quest/ });
    expect(within(list).queryByText("Zombie Vault")).not.toBeInTheDocument();

    // Show archived → the request flips includeArchived, the archived row appears
    // with an "Archived" badge and its switch button disabled.
    fireEvent.click(screen.getByRole("button", { name: /show archived/i }));
    await waitFor(() => expect(within(list).queryByText("Zombie Vault")).toBeInTheDocument());
    const archivedRow = within(list).getByText("Zombie Vault").closest("li")!;
    expect(within(archivedRow).getByText("Archived")).toBeInTheDocument();
    // The switch button of the archived row is disabled (can't be made active).
    expect(within(archivedRow).getByRole("button", { name: /Zombie Vault/ })).toBeDisabled();

    // Hide again → the archived row is gone.
    fireEvent.click(screen.getByRole("button", { name: /hide archived/i }));
    await waitFor(() => expect(within(list).queryByText("Zombie Vault")).not.toBeInTheDocument());
  });

  it("archives an active campaign from its row menu", async () => {
    const calls: Record<string, number> = {};
    renderSwitcher(
      mockBackend({ campaigns: [{ id: "a", name: "Alpha Quest" }], activeId: "a", calls }),
    );
    await screen.findByText("Alpha Quest");
    openPanel();
    const list = await screen.findByRole("group", { name: /campaigns/i });
    const row = (await within(list).findByText("Alpha Quest")).closest("li")!;

    // Open the row's action menu and archive.
    fireEvent.click(within(row).getByRole("button", { name: /Campaign actions/i }));
    fireEvent.click(await screen.findByRole("menuitem", { name: /^Archive$/ }));
    await waitFor(() => expect(calls.archiveCampaign).toBe(1));
  });

  it("fires a row-menu action through the real mousedown→click sequence without the panel dismissing the portalled menu", async () => {
    // The menu portals to document.body, OUTSIDE the switcher panel's wrapRef. In a
    // real browser a click is mousedown→mouseup→click: the panel's own
    // usePopoverDismiss sees that mousedown as "outside", closes the panel, and
    // unmounts the portal before mouseup — so onClick never fires (#338 review).
    // fireEvent.click alone (the test above) can't catch this; the mousedown must
    // be dispatched separately.
    const calls: Record<string, number> = {};
    renderSwitcher(
      mockBackend({ campaigns: [{ id: "a", name: "Alpha Quest" }], activeId: "a", calls }),
    );
    await screen.findByText("Alpha Quest");
    openPanel();
    const list = await screen.findByRole("group", { name: /campaigns/i });
    const row = (await within(list).findByText("Alpha Quest")).closest("li")!;

    fireEvent.click(within(row).getByRole("button", { name: /Campaign actions/i }));
    const item = await screen.findByRole("menuitem", { name: /^Archive$/ });
    fireEvent.mouseDown(item);
    // The panel (and thus the portalled menu) must survive the mousedown.
    expect(screen.getByRole("group", { name: /campaigns/i })).toBeInTheDocument();
    fireEvent.click(item);
    await waitFor(() => expect(calls.archiveCampaign).toBe(1));
  });

  it("deletes an archived campaign THROUGH the open panel without the dismiss hook tearing it down mid-flow", async () => {
    const calls: Record<string, number> = {};
    renderSwitcher(
      mockBackend({
        campaigns: [
          { id: "a", name: "Alpha Quest" },
          { id: "z", name: "Zombie Vault", archived: true },
        ],
        activeId: "a",
        calls,
      }),
    );
    await screen.findByText("Alpha Quest");
    openPanel();
    fireEvent.click(screen.getByRole("button", { name: /show archived/i }));

    const list = await screen.findByRole("group", { name: /campaigns/i });
    const row = (await within(list).findByText("Zombie Vault")).closest("li")!;
    fireEvent.click(within(row).getByRole("button", { name: /Campaign actions/i }));
    fireEvent.click(await screen.findByRole("menuitem", { name: /Delete/ }));

    // The confirm dialog is up. Interacting inside it fires mousedown on nodes
    // that live OUTSIDE the panel's ref (Radix portal). The dismiss hook must NOT
    // treat that as an outside click and tear the panel (and this dialog) down.
    const dialog = await screen.findByRole("alertdialog");
    const input = within(dialog).getByTestId("confirm-text-input");
    fireEvent.mouseDown(input);

    // Panel + dialog both survived the mousedown. The panel is aria-hidden by the
    // modal (hidden:true), but crucially still MOUNTED — the bug tore it (and this
    // dialog) out of the DOM entirely, which query would then fail.
    expect(screen.getByRole("group", { name: /campaigns/i, hidden: true })).toBeInTheDocument();
    expect(screen.getByRole("alertdialog")).toBeInTheDocument();

    // Type the exact name and confirm — the delete fires.
    fireEvent.change(input, { target: { value: "Zombie Vault" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete campaign" }));
    await waitFor(() => expect(calls.deleteCampaign).toBe(1));
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

    // The fields LOCK in the retry state: the campaign already exists with the
    // submitted values, and an edit here would be silently discarded by the
    // activation retry (rename is #268's slice).
    expect(screen.getByLabelText("Name")).toBeDisabled();
    expect(screen.getByLabelText("System")).toBeDisabled();

    // Retrying re-runs ONLY the activation — it must not mint a second campaign.
    fireEvent.click(screen.getByRole("button", { name: /retry activation/i }));
    await waitFor(() => expect(calls.setActiveCampaign).toBe(2));
    expect(calls.createCampaign).toBe(1);
  });

  it("opens the settings editor seeded with the active campaign (#268)", async () => {
    renderSwitcher(
      mockBackend({
        campaigns: [{ id: "a", name: "Alpha Quest", system: "D&D 5e", language: "en" }],
        activeId: "a",
      }),
    );
    await screen.findByText("Alpha Quest");
    openPanel();
    await screen.findByRole("group", { name: /campaigns/i });

    // The list footer offers a "Campaign settings" action; it opens the edit form
    // seeded from the resolved Active Campaign.
    fireEvent.click(await screen.findByRole("button", { name: /campaign settings/i }));
    expect(((await screen.findByLabelText("Name")) as HTMLInputElement).value).toBe("Alpha Quest");
    expect((screen.getByLabelText("System") as HTMLInputElement).value).toBe("D&D 5e");
    expect(screen.getByText(/next Voice Session/i)).toBeInTheDocument();
  });

  it("saves a rename and refreshes the picker list (#268, ADR-0018)", async () => {
    const calls: Record<string, number> = {};
    renderSwitcher(
      mockBackend({
        campaigns: [{ id: "a", name: "Alpha Quest", system: "D&D 5e", language: "en" }],
        activeId: "a",
        calls,
      }),
    );
    await screen.findByText("Alpha Quest");
    openPanel();
    await screen.findByRole("group", { name: /campaigns/i });
    const listedBefore = calls.listCampaigns;

    // Rename via the settings editor, then Save.
    fireEvent.click(await screen.findByRole("button", { name: /campaign settings/i }));
    fireEvent.change(await screen.findByLabelText("Name"), { target: { value: "Renamed Quest" } });
    fireEvent.click(screen.getByRole("button", { name: /save/i }));

    // The write happened…
    await waitFor(() => expect(calls.updateCampaign).toBe(1));
    // …the editor closed back to the list…
    const group = await screen.findByRole("group", { name: /campaigns/i });
    // …listCampaigns is campaign-INVARIANT (the switch sweep skips it), so the
    // save must invalidate it explicitly or the picker keeps the stale name.
    await waitFor(() => expect(calls.listCampaigns).toBeGreaterThan(listedBefore));
    expect(await within(group).findByRole("button", { name: /Renamed Quest/ })).toBeInTheDocument();
    expect(within(group).queryByRole("button", { name: /Alpha Quest/ })).not.toBeInTheDocument();
  });

  it("returns to the list when the settings editor is cancelled (#268)", async () => {
    renderSwitcher(mockBackend({ campaigns: [{ id: "a", name: "Alpha Quest" }], activeId: "a" }));
    await screen.findByText("Alpha Quest");
    openPanel();
    fireEvent.click(await screen.findByRole("button", { name: /campaign settings/i }));
    await screen.findByLabelText("Name");
    fireEvent.click(screen.getByRole("button", { name: /cancel/i }));
    // Back to the campaign list; the edit form is gone.
    expect(await screen.findByRole("group", { name: /campaigns/i })).toBeInTheDocument();
    expect(screen.queryByLabelText("Name")).not.toBeInTheDocument();
  });

  it("resets to the list view after the settings editor is left open and the panel is reopened (#268)", async () => {
    renderSwitcher(mockBackend({ campaigns: [{ id: "a", name: "Alpha Quest" }], activeId: "a" }));
    await screen.findByText("Alpha Quest");
    openPanel();
    fireEvent.click(await screen.findByRole("button", { name: /campaign settings/i }));
    await screen.findByLabelText("Name");

    // Close and reopen: the panel is a single state, so it can't reopen onto the
    // stale edit form.
    openPanel(); // closes
    openPanel(); // reopens
    expect(await screen.findByRole("group", { name: /campaigns/i })).toBeInTheDocument();
    expect(screen.queryByLabelText("Name")).not.toBeInTheDocument();
  });

  it("has no settings action on the first-run create form (#268)", async () => {
    renderSwitcher(mockBackend({ campaigns: [], calls: {} }));
    // First run: the trigger opens straight into create; there's no active
    // campaign to edit, so the settings action never offers a broken edit.
    const trigger = await screen.findByRole("button", { name: /create your first campaign/i });
    fireEvent.click(trigger);
    // Create mode has no settings button at all — nothing to edit yet.
    expect(screen.queryByRole("button", { name: /campaign settings/i })).not.toBeInTheDocument();
  });

  it("disables the settings action when no active campaign resolves (#268)", async () => {
    // Campaigns exist but resolution FAILS (non-NotFound): the list still opens,
    // and the settings button renders — but disabled, since there is no resolved
    // Active Campaign to seed the editor with.
    renderSwitcher(mockBackend({ campaigns: [{ id: "a", name: "Alpha Quest" }], activeError: "resolver down" }));
    fireEvent.click(await screen.findByTestId("campaign-switcher-trigger"));
    await screen.findByRole("group", { name: /campaigns/i });
    expect(await screen.findByRole("button", { name: /campaign settings/i })).toBeDisabled();
  });
});
