import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { useQuery } from "@connectrpc/connect-query";
import { toast } from "sonner";

import {
  CampaignSchema,
  CampaignService,
  GetCampaignRosterResponseSchema,
  SetActiveCampaignResponseSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { useDirtyGuard } from "@/components/ui/useDirtyGuard";
import { makeQueryClient } from "@/lib/queryClient";
import {
  isLocalCampaignSwitch,
  lastLocalCampaignSwitch,
  markLocalCampaignSwitch,
  resetLocalCampaignSwitch,
  useCampaignSwitch,
} from "./useCampaignSwitch";

vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() } }));

beforeEach(() => {
  vi.mocked(toast.success).mockClear();
  vi.mocked(toast.info).mockClear();
  vi.mocked(toast.error).mockClear();
  resetLocalCampaignSwitch();
});

const NAMES: Record<string, string> = { a: "Alpha Quest", b: "Beta Tale" };

// A backend with two campaigns. `liveOn` models a running Voice Session: the
// server resolves live-first (#222), so while it is set the RESOLVED Active
// Campaign stays that campaign whatever was just selected.
function backend(opts: { liveOn?: string; switchError?: string } = {}) {
  const state = { selected: "a", switches: [] as string[], rosterReads: 0 };
  const resolved = () => opts.liveOn ?? state.selected;
  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      setActiveCampaign: (req) => {
        if (opts.switchError) throw new ConnectError(opts.switchError, Code.Internal);
        state.switches.push(req.campaignId);
        state.selected = req.campaignId;
        return create(SetActiveCampaignResponseSchema, {
          campaign: create(CampaignSchema, { id: resolved(), name: NAMES[resolved()] }),
        });
      },
      getCampaignRoster: () => {
        state.rosterReads++;
        return create(GetCampaignRosterResponseSchema, {
          campaign: create(CampaignSchema, { id: resolved(), name: NAMES[resolved()] }),
        });
      },
    });
  });
  return { transport, state };
}

// A stand-in screen: a campaign-scoped read (what the sweep must refetch), an
// editor that can be made dirty, and a button that switches to Beta Tale.
function Probe({ onSwitched }: { onSwitched?: (r: { deferred: boolean }) => void }) {
  const roster = useQuery(CampaignService.method.getCampaignRoster, {});
  const edits = useDirtyGuard();
  const campaignSwitch = useCampaignSwitch();
  return (
    <>
      <p data-testid="working-in">{roster.data?.campaign?.name ?? "…"}</p>
      <button type="button" onClick={() => edits.markDirty(true)}>
        type something
      </button>
      <button
        type="button"
        disabled={campaignSwitch.pending}
        onClick={() => campaignSwitch.switchTo({ id: "b", name: "Beta Tale" }, { onSwitched })}
      >
        switch to beta
      </button>
      {campaignSwitch.dialog}
    </>
  );
}

function mount(opts: Parameters<typeof backend>[0] = {}, onSwitched?: (r: { deferred: boolean }) => void) {
  const h = backend(opts);
  render(
    <Providers transport={h.transport} queryClient={makeQueryClient()}>
      <Probe onSwitched={onSwitched} />
    </Providers>,
  );
  return h;
}

describe("useCampaignSwitch", () => {
  it("switches at once when nothing is unsaved: sweeps the caches and names the new campaign", async () => {
    const onSwitched = vi.fn();
    const { state } = mount({}, onSwitched);
    await waitFor(() => expect(screen.getByTestId("working-in")).toHaveTextContent("Alpha Quest"));

    fireEvent.click(screen.getByRole("button", { name: "switch to beta" }));

    // No question asked…
    expect(screen.queryByText("Discard unsaved changes?")).not.toBeInTheDocument();
    await waitFor(() => expect(state.switches).toEqual(["b"]));
    // …the campaign-scoped read follows without a reload…
    await waitFor(() => expect(screen.getByTestId("working-in")).toHaveTextContent("Beta Tale"));
    // …and the GM is told where they are now.
    expect(vi.mocked(toast.success)).toHaveBeenCalledWith("Now working in Beta Tale");
    expect(vi.mocked(toast.info)).not.toHaveBeenCalled();
    expect(onSwitched).toHaveBeenCalledWith({ campaign: { id: "b", name: "Beta Tale" }, deferred: false });
  });

  it("asks before a switch would discard unsaved edits; Cancel changes nothing", async () => {
    const { state } = mount();
    await waitFor(() => expect(screen.getByTestId("working-in")).toHaveTextContent("Alpha Quest"));
    fireEvent.click(screen.getByRole("button", { name: "type something" }));

    fireEvent.click(screen.getByRole("button", { name: "switch to beta" }));
    expect(await screen.findByText("Discard unsaved changes?")).toBeInTheDocument();
    // Nothing is written while the question is open.
    expect(state.switches).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByText("Discard unsaved changes?")).not.toBeInTheDocument());
    expect(state.switches).toEqual([]);
    expect(screen.getByTestId("working-in")).toHaveTextContent("Alpha Quest");
    expect(vi.mocked(toast.success)).not.toHaveBeenCalled();
    // Declined: this tab has not asked for anything.
    expect(lastLocalCampaignSwitch()).toBeNull();
  });

  it("switches once the GM confirms discarding the edits", async () => {
    const { state } = mount();
    await waitFor(() => expect(screen.getByTestId("working-in")).toHaveTextContent("Alpha Quest"));
    fireEvent.click(screen.getByRole("button", { name: "type something" }));

    fireEvent.click(screen.getByRole("button", { name: "switch to beta" }));
    fireEvent.click(await screen.findByRole("button", { name: "Discard changes" }));

    await waitFor(() => expect(state.switches).toEqual(["b"]));
    await waitFor(() => expect(screen.getByTestId("working-in")).toHaveTextContent("Beta Tale"));
    expect(vi.mocked(toast.success)).toHaveBeenCalledWith("Now working in Beta Tale");
  });

  it("says a live Voice Session defers the switch instead of claiming it happened", async () => {
    const onSwitched = vi.fn();
    const { state } = mount({ liveOn: "a" }, onSwitched);
    await waitFor(() => expect(screen.getByTestId("working-in")).toHaveTextContent("Alpha Quest"));

    fireEvent.click(screen.getByRole("button", { name: "switch to beta" }));

    // The selection IS stored…
    await waitFor(() => expect(state.switches).toEqual(["b"]));
    // …but the session's campaign still resolves, and the toast says so.
    await waitFor(() =>
      expect(vi.mocked(toast.info)).toHaveBeenCalledWith(
        "A voice session is live — Beta Tale becomes the active campaign when it ends.",
      ),
    );
    expect(vi.mocked(toast.success)).not.toHaveBeenCalled();
    expect(screen.getByTestId("working-in")).toHaveTextContent("Alpha Quest");
    expect(onSwitched).toHaveBeenCalledWith({ campaign: { id: "b", name: "Beta Tale" }, deferred: true });
  });

  it("reports a refused switch and leaves the caches alone", async () => {
    const onSwitched = vi.fn();
    const { state } = mount({ switchError: "selection store down" }, onSwitched);
    await waitFor(() => expect(screen.getByTestId("working-in")).toHaveTextContent("Alpha Quest"));
    const readsBefore = state.rosterReads;

    fireEvent.click(screen.getByRole("button", { name: "switch to beta" }));

    await waitFor(() =>
      expect(vi.mocked(toast.error)).toHaveBeenCalledWith(
        expect.stringContaining("selection store down"),
      ),
    );
    expect(vi.mocked(toast.success)).not.toHaveBeenCalled();
    expect(onSwitched).not.toHaveBeenCalled();
    expect(state.rosterReads).toBe(readsBefore);
    // The button is usable again for another try.
    expect(screen.getByRole("button", { name: "switch to beta" })).toBeEnabled();
  });
});

describe("the 'this tab did it' flag", () => {
  it("names the campaign this tab asked for, from the moment it asks", async () => {
    const { state } = mount();
    await waitFor(() => expect(screen.getByTestId("working-in")).toHaveTextContent("Alpha Quest"));
    expect(lastLocalCampaignSwitch()).toBeNull();
    expect(isLocalCampaignSwitch("b")).toBe(false);

    fireEvent.click(screen.getByRole("button", { name: "switch to beta" }));
    // Set synchronously with the request, not on its success: the sweep's
    // refetch can be observed before the mutation's onSuccess runs.
    expect(isLocalCampaignSwitch("b")).toBe(true);
    await waitFor(() => expect(state.switches).toEqual(["b"]));
    expect(lastLocalCampaignSwitch()).toBe("b");
    // A change to any OTHER campaign is not this tab's doing.
    expect(isLocalCampaignSwitch("a")).toBe(false);
  });

  it("is taken back when the switch is refused — the campaign never became active through this tab", async () => {
    mount({ switchError: "selection store down" });
    await waitFor(() => expect(screen.getByTestId("working-in")).toHaveTextContent("Alpha Quest"));
    // An earlier switch of this tab's own, which did go through.
    markLocalCampaignSwitch("a");

    fireEvent.click(screen.getByRole("button", { name: "switch to beta" }));
    expect(isLocalCampaignSwitch("b")).toBe(true);
    await waitFor(() => expect(vi.mocked(toast.error)).toHaveBeenCalled());

    // A later change to Beta Tale made elsewhere must not read as ours…
    expect(isLocalCampaignSwitch("b")).toBe(false);
    // …and what this tab did before the failed attempt still stands.
    expect(lastLocalCampaignSwitch()).toBe("a");
  });

  it("can be set by a flow that activates a campaign without the hook", () => {
    markLocalCampaignSwitch("new-1");
    expect(isLocalCampaignSwitch("new-1")).toBe(true);
    resetLocalCampaignSwitch();
    expect(lastLocalCampaignSwitch()).toBeNull();
  });
});
