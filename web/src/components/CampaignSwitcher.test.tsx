import { afterEach, beforeEach, describe, expect, it } from "vitest";
import type { ReactNode } from "react";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { useQuery } from "@connectrpc/connect-query";
import { toast, Toaster } from "sonner";

import { CampaignService, SessionService } from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { MemoryScreenNav, type ScreenLocation } from "@/app/screenNav";
import { useDirtyGuard } from "@/components/ui/useDirtyGuard";
import { invalidateActiveCampaignScopedQueries } from "@/lib/campaignCache";
import { invalidateMethodQueries, makeQueryClient } from "@/lib/queryClient";
import { campaignBackend, type Camp } from "@/screens/campaigns/testBackend";
import { CampaignSwitcher } from "./CampaignSwitcher";
import { noteLocalActiveCampaignChange, resetLocalCampaignSwitch } from "./useCampaignSwitch";

beforeEach(() => {
  resetLocalCampaignSwitch();
});

afterEach(() => {
  // The real toast host is mounted (the notice's "Switch back" is a real
  // button); clear it between tests.
  act(() => {
    toast.dismiss();
  });
});

const ALPHA: Camp = { id: "a", name: "Alpha Quest", system: "D&D 5e", created: "2026-01-05T12:00:00Z" };
const BETA: Camp = { id: "b", name: "Beta Tale", system: "Pathfinder 2e", created: "2026-03-01T12:00:00Z" };
const TWO = [ALPHA, BETA];

// RosterProbe stands in for another campaign-scoped screen (the Campaign
// screen's getCampaignRoster read). It shares the switcher's QueryClient and
// transport, so a switch's sweep must refetch it — the "updates every
// campaign-scoped screen without reload" contract.
function RosterProbe() {
  const { data } = useQuery(CampaignService.method.getCampaignRoster, {});
  return <div data-testid="roster-probe">{data?.campaign?.name ?? "…"}</div>;
}

// An editor elsewhere on screen that can be made dirty — a switch must ask.
function DirtyEditor() {
  const edits = useDirtyGuard();
  return (
    <button type="button" onClick={() => edits.markDirty(true)}>
      type something
    </button>
  );
}

function renderSwitcher(opts: Parameters<typeof campaignBackend>[0] = {}, extra?: ReactNode) {
  const h = campaignBackend(opts);
  const queryClient = makeQueryClient();
  const moves: ScreenLocation[] = [];
  render(
    <Providers transport={h.transport} queryClient={queryClient}>
      <MemoryScreenNav initial={{ screen: "campaign", view: "cast" }} onNavigate={(next) => moves.push(next)}>
        <CampaignSwitcher />
        {extra}
      </MemoryScreenNav>
      <Toaster />
    </Providers>,
  );
  return { ...h, queryClient, moves };
}

const trigger = () => screen.getByTestId("campaign-switcher-trigger");
const openPanel = () => fireEvent.click(trigger());
const list = () => screen.findByRole("group", { name: "Campaigns" });
const row = async (name: RegExp) => within(await list()).findByRole("button", { name });

describe("CampaignSwitcher: the quick switch", () => {
  it("names the Active Campaign on its trigger and marks it in the list", async () => {
    renderSwitcher({ campaigns: TWO, activeId: "b" });

    // The trigger labels the active campaign — including in its accessible name.
    expect(await screen.findByText("Beta Tale")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Switch campaign — active: Beta Tale" })).toBe(trigger());
    expect(trigger()).toHaveAttribute("aria-expanded", "false");

    openPanel();
    expect(trigger()).toHaveAttribute("aria-expanded", "true");
    const beta = await row(/Beta Tale/);
    const alpha = await row(/Alpha Quest/);
    // The active campaign carries aria-current; the other doesn't.
    expect(beta).toHaveAttribute("aria-current", "true");
    expect(alpha).not.toHaveAttribute("aria-current");
    expect(alpha).toHaveTextContent("D&D 5e");
    // No live Voice Session → no takes-effect notice.
    expect(screen.queryByText(/takes effect after it ends/i)).not.toBeInTheDocument();
  });

  it("does not fetch the campaign list until the panel opens, and never asks for archived ones", async () => {
    const { callsOf } = renderSwitcher({
      campaigns: [...TWO, { id: "z", name: "Zombie Vault", archived: true }],
      activeId: "a",
    });

    // The trigger label resolves from getActiveCampaign alone — the list stays
    // unfetched while the switcher just sits in the topbar of every screen.
    expect(await screen.findByText("Alpha Quest")).toBeInTheDocument();
    expect(callsOf("listCampaigns")).toEqual([]);

    openPanel();
    await row(/Alpha Quest/);
    expect(callsOf("listCampaigns")).toEqual([{ method: "listCampaigns", includeArchived: false }]);
    expect(within(await list()).queryByText("Zombie Vault")).not.toBeInTheDocument();
  });

  it("is only a quick switch: settings, import, archived campaigns and row menus are not in it", async () => {
    renderSwitcher({ campaigns: TWO, activeId: "a" });
    await screen.findByText("Alpha Quest");
    openPanel();
    await row(/Beta Tale/);

    expect(screen.queryByRole("button", { name: /campaign settings/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /import/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /archived/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /actions/i })).not.toBeInTheDocument();
    // What it does offer under the list.
    expect(screen.getByRole("button", { name: "New campaign" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "All campaigns" })).toBeInTheDocument();
  });

  it("closes without an RPC when the active campaign row is clicked", async () => {
    const { callsOf } = renderSwitcher({ campaigns: TWO, activeId: "b" });
    expect(await screen.findByText("Beta Tale")).toBeInTheDocument();
    openPanel();
    fireEvent.click(await row(/Beta Tale/));

    // Re-selecting the current campaign is a no-op: the panel just closes — no
    // SetActiveCampaign write, no cache sweep.
    expect(screen.queryByRole("group", { name: "Campaigns" })).not.toBeInTheDocument();
    expect(callsOf("setActiveCampaign")).toEqual([]);
  });

  it("switches the active campaign and sweeps campaign-scoped caches (no reload)", async () => {
    const { callsOf } = renderSwitcher({ campaigns: TWO, activeId: "b" }, <RosterProbe />);

    // Both the switcher and the roster probe have loaded against campaign B.
    await waitFor(() => expect(screen.getByTestId("roster-probe")).toHaveTextContent("Beta Tale"));
    openPanel();
    fireEvent.click(await row(/Alpha Quest/));

    // The durable selection moved…
    await waitFor(() => expect(callsOf("setActiveCampaign")).toEqual([{ method: "setActiveCampaign", id: "a" }]));
    // …the switcher's own header refetched (GetActiveCampaign is in the sweep)…
    expect(await screen.findByRole("button", { name: "Switch campaign — active: Alpha Quest" })).toBeInTheDocument();
    // …and the sweep refetched the roster screen's cache without a reload.
    await waitFor(() => expect(screen.getByTestId("roster-probe")).toHaveTextContent("Alpha Quest"));
    expect(await screen.findByText("Now working in Alpha Quest")).toBeInTheDocument();
    // The panel closed on a successful switch, and no "changed elsewhere"
    // notice was raised about this tab's own doing.
    expect(screen.queryByRole("group", { name: "Campaigns" })).not.toBeInTheDocument();
    expect(screen.queryByText(/another tab or Discord/)).not.toBeInTheDocument();
  });

  // The row click goes through the guarded switch (useCampaignSwitch — its own
  // test file covers the hook's branches); these pin that the switcher uses it.
  it("asks before a switch would discard unsaved edits; Cancel leaves the selection alone", async () => {
    const { callsOf } = renderSwitcher({ campaigns: TWO, activeId: "b" }, <DirtyEditor />);
    expect(await screen.findByText("Beta Tale")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "type something" }));

    openPanel();
    fireEvent.click(await row(/Alpha Quest/));

    // The question, and no write while it is open.
    expect(await screen.findByText("Discard unsaved changes?")).toBeInTheDocument();
    expect(callsOf("setActiveCampaign")).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByText("Discard unsaved changes?")).not.toBeInTheDocument());
    expect(callsOf("setActiveCampaign")).toEqual([]);
    expect(screen.getByRole("button", { name: "Switch campaign — active: Beta Tale" })).toBeInTheDocument();
  });

  it("switches once the discard is confirmed", async () => {
    const { callsOf } = renderSwitcher({ campaigns: TWO, activeId: "b" }, <DirtyEditor />);
    expect(await screen.findByText("Beta Tale")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "type something" }));

    openPanel();
    fireEvent.click(await row(/Alpha Quest/));
    fireEvent.click(await screen.findByRole("button", { name: "Discard changes" }));

    await waitFor(() => expect(callsOf("setActiveCampaign")).toHaveLength(1));
    expect(await screen.findByRole("button", { name: "Switch campaign — active: Alpha Quest" })).toBeInTheDocument();
  });

  it("All campaigns goes to the Campaigns page and closes the panel", async () => {
    const { moves } = renderSwitcher({ campaigns: TWO, activeId: "a" });
    await screen.findByText("Alpha Quest");
    openPanel();
    fireEvent.click(await screen.findByRole("button", { name: "All campaigns" }));

    expect(moves).toEqual([{ screen: "campaigns", view: undefined, search: {} }]);
    expect(screen.queryByRole("group", { name: "Campaigns" })).not.toBeInTheDocument();
  });

  it("closes on Escape and hands focus back to the trigger", async () => {
    renderSwitcher({ campaigns: TWO, activeId: "a" });
    await screen.findByText("Alpha Quest");
    openPanel();
    const beta = await row(/Beta Tale/);
    beta.focus();

    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.queryByRole("group", { name: "Campaigns" })).not.toBeInTheDocument();
    expect(trigger()).toHaveFocus();
  });

  it("says why the list could not be loaded", async () => {
    // Four failures: the read is retried three times before it settles.
    const { queryClient } = renderSwitcher({ campaigns: TWO, activeId: "a", failLists: 4 });
    queryClient.setDefaultOptions({ queries: { retryDelay: 0 } });
    await screen.findByText("Alpha Quest");
    openPanel();
    expect(await screen.findByRole("alert")).toHaveTextContent("Couldn't load campaigns: campaign store offline");
  });
});

describe("CampaignSwitcher: New campaign", () => {
  it("creates in a dialog (not in the panel), makes the campaign active and lands on its Overview", async () => {
    const { callsOf, moves } = renderSwitcher({ campaigns: [ALPHA], activeId: "a" });
    expect(await screen.findByText("Alpha Quest")).toBeInTheDocument();
    openPanel();
    fireEvent.click(await screen.findByRole("button", { name: "New campaign" }));

    // The panel made way for the dialog.
    expect(screen.queryByRole("group", { name: "Campaigns" })).not.toBeInTheDocument();
    const dialog = await screen.findByRole("dialog", { name: "New campaign" });
    // The form seeds System with "D&D 5e" (as Campaign settings suggests) and
    // Language with the display language through the shared registry SELECT.
    expect(within(dialog).getByLabelText("Game system")).toHaveValue("D&D 5e");
    await waitFor(() =>
      expect(within(dialog).getByRole("combobox", { name: /spoken language/i })).toHaveTextContent("English (en)"),
    );
    // Name empty → nothing to create yet.
    const create = within(dialog).getByRole("button", { name: "Create campaign" });
    expect(create).toBeDisabled();

    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Gamma Saga" } });
    fireEvent.click(create);

    // Create then activate, then the dialog closes on the new active campaign.
    await waitFor(() => expect(callsOf("createCampaign")).toHaveLength(1));
    await waitFor(() => expect(callsOf("setActiveCampaign")).toEqual([{ method: "setActiveCampaign", id: "new-1" }]));
    expect(await screen.findByRole("button", { name: "Switch campaign — active: Gamma Saga" })).toBeInTheDocument();
    expect(await screen.findByText("Created Gamma Saga — your Butler is ready")).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await waitFor(() => expect(moves).toEqual([{ screen: "campaign", view: "overview", search: {} }]));
    // This tab did it: no "changed elsewhere" notice.
    expect(screen.queryByText(/another tab or Discord/)).not.toBeInTheDocument();
  });

  it("is where the trigger leads when there is no campaign at all", async () => {
    const { callsOf } = renderSwitcher({ campaigns: [] });

    // The trigger is a first-run call to action and opens the dialog directly.
    const cta = await screen.findByRole("button", { name: "Create your first campaign" });
    expect(cta).toHaveAttribute("aria-haspopup", "dialog");
    fireEvent.click(cta);
    expect(screen.queryByRole("group", { name: "Campaigns" })).not.toBeInTheDocument();

    const dialog = await screen.findByRole("dialog", { name: "New campaign" });
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "First Campaign" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Create campaign" }));

    await waitFor(() => expect(callsOf("createCampaign")).toHaveLength(1));
    await waitFor(() => expect(callsOf("setActiveCampaign")).toHaveLength(1));
    // The new campaign is active; the trigger now shows it.
    expect(await screen.findByRole("button", { name: "Switch campaign — active: First Campaign" })).toBeInTheDocument();
  });

  it("starts pristine every time it is opened", async () => {
    renderSwitcher({ campaigns: [ALPHA], activeId: "a", errors: { createCampaign: "database is down" } });
    await screen.findByText("Alpha Quest");

    // First attempt fails, leaving the inline alert.
    openPanel();
    fireEvent.click(await screen.findByRole("button", { name: "New campaign" }));
    let dialog = await screen.findByRole("dialog", { name: "New campaign" });
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Doomed" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Create campaign" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(/couldn't create.*database is down/i);
    // The form stays open for a retry, with what was typed.
    expect(within(dialog).getByLabelText("Name")).toHaveValue("Doomed");

    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

    openPanel();
    fireEvent.click(await screen.findByRole("button", { name: "New campaign" }));
    dialog = await screen.findByRole("dialog", { name: "New campaign" });
    expect(within(dialog).queryByRole("alert")).not.toBeInTheDocument();
    expect(within(dialog).getByLabelText("Name")).toHaveValue("");
  });

  it("retries only the activation (no duplicate create) when the create succeeds but the switch fails", async () => {
    const { callsOf } = renderSwitcher({
      campaigns: [ALPHA],
      activeId: "a",
      errors: { setActiveCampaign: "activate boom" },
    });
    await screen.findByText("Alpha Quest");
    openPanel();
    fireEvent.click(await screen.findByRole("button", { name: "New campaign" }));
    const dialog = await screen.findByRole("dialog", { name: "New campaign" });
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Gamma Saga" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Create campaign" }));

    // The campaign WAS created; only activation failed — the alert says so (not a
    // false "couldn't create"), and the button re-labels to a pure activation retry.
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(/couldn't switch to it.*activate boom/i);
    expect(callsOf("createCampaign")).toHaveLength(1);
    expect(callsOf("setActiveCampaign")).toHaveLength(1);

    // The fields LOCK in the retry state: the campaign already exists with the
    // submitted values, and an edit here would be silently discarded.
    expect(within(dialog).getByLabelText("Name")).toBeDisabled();
    expect(within(dialog).getByLabelText("Game system")).toBeDisabled();

    // Retrying re-runs ONLY the activation — it must not mint a second campaign.
    fireEvent.click(within(dialog).getByRole("button", { name: "Retry activation" }));
    await waitFor(() => expect(callsOf("setActiveCampaign")).toHaveLength(2));
    expect(callsOf("createCampaign")).toHaveLength(1);
  });

  it("asks before creating when the switch at the end would discard unsaved edits", async () => {
    const { callsOf } = renderSwitcher({ campaigns: [ALPHA], activeId: "a" }, <DirtyEditor />);
    await screen.findByText("Alpha Quest");
    fireEvent.click(screen.getByRole("button", { name: "type something" }));
    openPanel();
    fireEvent.click(await screen.findByRole("button", { name: "New campaign" }));
    const dialog = await screen.findByRole("dialog", { name: "New campaign" });
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Gamma Saga" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Create campaign" }));

    // Asked BEFORE the campaign exists: Cancel leaves nothing half done.
    const question = await screen.findByRole("alertdialog", { name: "Discard unsaved changes?" });
    expect(callsOf("createCampaign")).toEqual([]);
    fireEvent.click(within(question).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
    expect(callsOf("createCampaign")).toEqual([]);
    expect(screen.getByLabelText("Name")).toHaveValue("Gamma Saga");

    fireEvent.click(screen.getByRole("button", { name: "Create campaign" }));
    fireEvent.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Discard changes" }),
    );
    await waitFor(() => expect(callsOf("createCampaign")).toHaveLength(1));
    await waitFor(() => expect(callsOf("setActiveCampaign")).toHaveLength(1));
  });
});

describe("CampaignSwitcher: while a Voice Session is live", () => {
  it("marks the trigger live and says in the panel that a switch waits for the session", async () => {
    renderSwitcher({ campaigns: TWO, activeId: "a", liveOn: "a" });
    await screen.findByText("Alpha Quest");
    expect(
      await screen.findByRole("button", { name: "Switch campaign — live voice session in Alpha Quest" }),
    ).toBe(trigger());
    expect(trigger()).toHaveAttribute("data-live", "true");

    openPanel();
    expect(await screen.findByRole("note")).toHaveTextContent(/takes effect after it ends/i);
  });

  it("shows “Next: {name}” for a switch the session is holding back, until the session ends", async () => {
    const { state, queryClient, callsOf } = renderSwitcher({ campaigns: TWO, activeId: "a", liveOn: "a" }, <RosterProbe />);
    await screen.findByRole("button", { name: "Switch campaign — live voice session in Alpha Quest" });
    expect(screen.queryByText(/^Next:/)).not.toBeInTheDocument();

    openPanel();
    fireEvent.click(await row(/Beta Tale/));
    await waitFor(() => expect(callsOf("setActiveCampaign")).toEqual([{ method: "setActiveCampaign", id: "b" }]));

    // The selection is stored; the session's campaign still resolves. The
    // trigger keeps naming it, and says what is waiting.
    expect(await screen.findByText("Next: Beta Tale")).toBeInTheDocument();
    expect(trigger()).toHaveAccessibleName("Switch campaign — live voice session in Alpha Quest, next: Beta Tale");
    expect(within(trigger()).getByText("Alpha Quest")).toBeInTheDocument();
    expect(
      await screen.findByText("A voice session is live — Beta Tale becomes the active campaign when it ends."),
    ).toBeInTheDocument();
    // …and so does its row in the panel.
    openPanel();
    expect(within(await row(/Beta Tale/)).getByText("Next")).toBeInTheDocument();
    openPanel();

    // The session ends: the stored selection resolves, every screen follows.
    state.liveOn = undefined;
    await act(() => invalidateMethodQueries(queryClient, SessionService.method.getSession));

    expect(await screen.findByRole("button", { name: "Switch campaign — active: Beta Tale" })).toBeInTheDocument();
    expect(screen.queryByText(/^Next:/)).not.toBeInTheDocument();
    expect(trigger()).not.toHaveAttribute("data-live");
    await waitFor(() => expect(screen.getByTestId("roster-probe")).toHaveTextContent("Beta Tale"));
    // The change is the session ending, not something done elsewhere.
    expect(screen.queryByText(/another tab or Discord/)).not.toBeInTheDocument();
  });

  it("drops “Next:” when the GM switches back to the session's own campaign", async () => {
    const { callsOf } = renderSwitcher({ campaigns: TWO, activeId: "a", liveOn: "a" });
    await screen.findByRole("button", { name: "Switch campaign — live voice session in Alpha Quest" });
    openPanel();
    fireEvent.click(await row(/Beta Tale/));
    expect(await screen.findByText("Next: Beta Tale")).toBeInTheDocument();

    // Picking the waiting campaign again does not stack a second "Next".
    openPanel();
    fireEvent.click(await row(/Beta Tale/));
    await waitFor(() => expect(callsOf("setActiveCampaign")).toHaveLength(2));
    expect(screen.getAllByText("Next: Beta Tale")).toHaveLength(1);

    // Alpha Quest's row — the session's own campaign — is how the waiting
    // switch is taken back: it writes the selection instead of just closing.
    openPanel();
    fireEvent.click(await row(/Alpha Quest/));
    await waitFor(() => expect(callsOf("setActiveCampaign").at(-1)).toEqual({ method: "setActiveCampaign", id: "a" }));
    await waitFor(() => expect(screen.queryByText("Next: Beta Tale")).not.toBeInTheDocument());
    expect(await screen.findByText("Switch cancelled — Alpha Quest stays the active campaign")).toBeInTheDocument();
    expect(trigger()).toHaveAccessibleName("Switch campaign — live voice session in Alpha Quest");
  });

  it("closes on the Active Campaign's row when nothing is waiting: no RPC", async () => {
    const { callsOf } = renderSwitcher({ campaigns: TWO, activeId: "a" });
    await screen.findByRole("button", { name: "Switch campaign — active: Alpha Quest" });
    openPanel();
    fireEvent.click(await row(/Alpha Quest/));
    await waitFor(() => expect(screen.queryByRole("group", { name: "Campaigns" })).not.toBeInTheDocument());
    expect(callsOf("setActiveCampaign")).toEqual([]);
  });

  it("says when it could not check for a live session", async () => {
    const h = campaignBackend({ campaigns: TWO, activeId: "a", failSession: true });
    const queryClient = makeQueryClient();
    // No backoff: the failing check settles into its error at once.
    queryClient.setDefaultOptions({ queries: { retryDelay: 0 } });
    render(
      <Providers transport={h.transport} queryClient={queryClient}>
        <MemoryScreenNav initial={{ screen: "campaign", view: "cast" }}>
          <CampaignSwitcher />
        </MemoryScreenNav>
      </Providers>,
    );
    await screen.findByRole("button", { name: "Switch campaign — active: Alpha Quest" });
    openPanel();
    expect(await screen.findByRole("note")).toHaveTextContent(
      "Couldn't check for a live voice session — a switch may take effect only after it ends.",
    );
  });
});

describe("CampaignSwitcher: the Active Campaign changed elsewhere (#692, web-only slice)", () => {
  it("raises a notice that stays, refreshes the screens, and Switch back restores the previous campaign", async () => {
    const { state, queryClient, callsOf } = renderSwitcher({ campaigns: TWO, activeId: "a" }, <RosterProbe />);
    await screen.findByRole("button", { name: "Switch campaign — active: Alpha Quest" });
    await waitFor(() => expect(screen.getByTestId("roster-probe")).toHaveTextContent("Alpha Quest"));

    // Another tab (or Discord's /glyphoxa use) selects Beta Tale; this tab
    // learns of it when it next reads the Active Campaign (a window focus).
    state.selected = "b";
    await act(() => invalidateMethodQueries(queryClient, CampaignService.method.getActiveCampaign));

    expect(
      await screen.findByText("Active campaign changed to Beta Tale (another tab or Discord)"),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Switch campaign — active: Beta Tale" })).toBeInTheDocument();
    // Every campaign-scoped read follows: the screen shows what the server
    // will now write to.
    await waitFor(() => expect(screen.getByTestId("roster-probe")).toHaveTextContent("Beta Tale"));
    expect(callsOf("setActiveCampaign")).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Switch back" }));
    await waitFor(() => expect(callsOf("setActiveCampaign")).toEqual([{ method: "setActiveCampaign", id: "a" }]));
    expect(await screen.findByRole("button", { name: "Switch campaign — active: Alpha Quest" })).toBeInTheDocument();
    expect(await screen.findByText("Now working in Alpha Quest")).toBeInTheDocument();
    // Switching back is this tab's own doing: no second notice.
    await waitFor(() =>
      expect(screen.queryByText("Active campaign changed to Alpha Quest (another tab or Discord)")).not.toBeInTheDocument(),
    );
  });

  it("stays quiet about a rename: the campaign is the same one", async () => {
    const { state, queryClient } = renderSwitcher({ campaigns: TWO, activeId: "a" });
    await screen.findByRole("button", { name: "Switch campaign — active: Alpha Quest" });

    state.campaigns[0].name = "Alpha Quest, Book Two";
    await act(() => invalidateMethodQueries(queryClient, CampaignService.method.getActiveCampaign));

    expect(await screen.findByRole("button", { name: "Switch campaign — active: Alpha Quest, Book Two" })).toBeInTheDocument();
    expect(screen.queryByText(/another tab or Discord/)).not.toBeInTheDocument();
  });

  it("stays quiet when this tab archived or deleted the Active Campaign and resolution moved on", async () => {
    const { state, queryClient } = renderSwitcher({ campaigns: TWO, activeId: "a" });
    await screen.findByRole("button", { name: "Switch campaign — active: Alpha Quest" });

    // What the archive / delete flows announce before they write.
    noteLocalActiveCampaignChange();
    state.campaigns[0].archivedAt = "2026-11-22T12:00:00Z";
    state.selected = undefined;
    await act(() => invalidateActiveCampaignScopedQueries(queryClient));

    expect(await screen.findByRole("button", { name: "Switch campaign — active: Beta Tale" })).toBeInTheDocument();
    expect(screen.queryByText(/another tab or Discord/)).not.toBeInTheDocument();
  });

  it("turns into the first-campaign call to action when the last campaign is gone", async () => {
    const { state, queryClient } = renderSwitcher({ campaigns: [ALPHA], activeId: "a" });
    await screen.findByRole("button", { name: "Switch campaign — active: Alpha Quest" });

    // The cache entry still holds Alpha Quest (a failed refetch keeps its
    // data); NotFound outranks it — nothing resolves any more.
    state.campaigns = [];
    await act(() => invalidateMethodQueries(queryClient, CampaignService.method.getActiveCampaign));

    expect(await screen.findByRole("button", { name: "Create your first campaign" })).toBe(trigger());
    expect(within(trigger()).queryByText("Alpha Quest")).not.toBeInTheDocument();
  });
});
