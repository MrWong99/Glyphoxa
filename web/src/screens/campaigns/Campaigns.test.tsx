import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { toast } from "sonner";

import {
  CampaignSchema,
  CampaignService,
  GetActiveCampaignResponseSchema,
  ListCampaignsResponseSchema,
  ListSupportedLanguagesResponseSchema,
  SetActiveCampaignResponseSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { MemoryScreenNav, type ScreenLocation } from "@/app/screenNav";
import { useDirtyGuard } from "@/components/ui/useDirtyGuard";
import { makeQueryClient } from "@/lib/queryClient";
import { Campaigns } from "./Campaigns";

vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() } }));

beforeEach(() => {
  vi.mocked(toast.success).mockClear();
  vi.mocked(toast.info).mockClear();
});

type Camp = { id: string; name: string; system?: string; language?: string; archived?: boolean };

// `liveOn` models a running Voice Session: resolution is live-first (#222), so
// the resolved Active Campaign stays that one whatever is selected.
function backend(opts: { campaigns?: Camp[]; activeId?: string; liveOn?: string; failLists?: number } = {}) {
  const state = {
    campaigns: opts.campaigns ?? [],
    selected: opts.activeId,
    switches: [] as string[],
    failLists: opts.failLists ?? 0,
    listRequests: [] as boolean[],
  };
  const row = (c: Camp) =>
    create(CampaignSchema, {
      id: c.id,
      name: c.name,
      system: c.system ?? "",
      language: c.language ?? "",
      archivedAt: c.archived ? timestampFromDate(new Date("2026-07-09T00:00:00Z")) : undefined,
    });
  const resolved = () => state.campaigns.find((c) => c.id === (opts.liveOn ?? state.selected));
  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      listCampaigns: (req) => {
        state.listRequests.push(req.includeArchived);
        if (state.failLists > 0) {
          state.failLists--;
          throw new ConnectError("campaign store offline", Code.Unavailable);
        }
        return create(ListCampaignsResponseSchema, {
          campaigns: state.campaigns.filter((c) => req.includeArchived || !c.archived).map(row),
        });
      },
      getActiveCampaign: () => {
        const a = resolved();
        if (!a) throw new ConnectError("no campaign", Code.NotFound);
        return create(GetActiveCampaignResponseSchema, { campaign: row(a) });
      },
      setActiveCampaign: (req) => {
        state.switches.push(req.campaignId);
        state.selected = req.campaignId;
        return create(SetActiveCampaignResponseSchema, { campaign: row(resolved()!) });
      },
      listSupportedLanguages: () => create(ListSupportedLanguagesResponseSchema, { languages: ["de", "en"] }),
    });
  });
  return { transport, state };
}

const TWO: Camp[] = [
  { id: "a", name: "Alpha Quest", system: "D&D 5e", language: "en" },
  { id: "b", name: "Beta Tale", system: "Pathfinder 2e", language: "de" },
];

// An editor elsewhere on screen that can be made dirty — the switch must ask.
function Editor() {
  const edits = useDirtyGuard();
  return (
    <button type="button" onClick={() => edits.markDirty(true)}>
      type something
    </button>
  );
}

function renderScreen(opts: Parameters<typeof backend>[0] = {}) {
  const h = backend(opts);
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
    </Providers>,
  );
  return { ...h, moves };
}

describe("Campaigns", () => {
  it("lists every campaign with its System and language, and marks the active one", async () => {
    const { state } = renderScreen({ campaigns: TWO, activeId: "b" });
    expect(screen.getByRole("heading", { level: 1, name: "Campaigns" })).toBeInTheDocument();

    const list = await screen.findByRole("list", { name: "Your campaigns" });
    const rows = within(list).getAllByRole("listitem");
    expect(rows).toHaveLength(2);
    expect(rows[0]).toHaveTextContent("Alpha Quest");
    expect(rows[0]).toHaveTextContent("D&D 5e · EN");
    expect(rows[1]).toHaveTextContent("Beta Tale");
    expect(rows[1]).toHaveTextContent("Pathfinder 2e · DE");

    // Only the Active Campaign carries the badge.
    await waitFor(() => expect(within(rows[1]).getByText("Active")).toBeInTheDocument());
    expect(within(rows[0]).queryByText("Active")).not.toBeInTheDocument();
    // Each Open says WHICH campaign to a screen reader.
    expect(within(rows[0]).getByRole("button", { name: "Open Alpha Quest" })).toHaveTextContent("Open");
    // The non-archived list — the same request (and cache entry) as the switcher's.
    expect(state.listRequests).toEqual([false]);
  });

  it("leaves archived campaigns out", async () => {
    renderScreen({
      campaigns: [...TWO, { id: "z", name: "Old Campaign", archived: true }],
      activeId: "a",
    });
    const list = await screen.findByRole("list", { name: "Your campaigns" });
    expect(within(list).getAllByRole("listitem")).toHaveLength(2);
    expect(screen.queryByText("Old Campaign")).not.toBeInTheDocument();
  });

  it("Open on another campaign switches to it, says so, and lands on its Overview", async () => {
    const { state, moves } = renderScreen({ campaigns: TWO, activeId: "a" });
    fireEvent.click(await screen.findByRole("button", { name: "Open Beta Tale" }));

    await waitFor(() => expect(state.switches).toEqual(["b"]));
    await waitFor(() => expect(vi.mocked(toast.success)).toHaveBeenCalledWith("Now working in Beta Tale"));
    await waitFor(() => expect(moves).toEqual([{ screen: "campaign", view: "overview", search: {} }]));
  });

  it("Open on the Active Campaign just goes to its Overview — no switch, no toast", async () => {
    const { state, moves } = renderScreen({ campaigns: TWO, activeId: "a" });
    const list = await screen.findByRole("list", { name: "Your campaigns" });
    await within(list).findByText("Active");

    fireEvent.click(screen.getByRole("button", { name: "Open Alpha Quest" }));
    expect(moves).toEqual([{ screen: "campaign", view: "overview", search: {} }]);
    expect(state.switches).toEqual([]);
    expect(vi.mocked(toast.success)).not.toHaveBeenCalled();
  });

  it("asks before a switch would discard unsaved edits", async () => {
    const { state, moves } = renderScreen({ campaigns: TWO, activeId: "a" });
    fireEvent.click(screen.getByRole("button", { name: "type something" }));
    fireEvent.click(await screen.findByRole("button", { name: "Open Beta Tale" }));

    expect(await screen.findByText("Discard unsaved changes?")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByText("Discard unsaved changes?")).not.toBeInTheDocument());
    expect(state.switches).toEqual([]);
    expect(moves).toEqual([]);

    fireEvent.click(screen.getByRole("button", { name: "Open Beta Tale" }));
    fireEvent.click(await screen.findByRole("button", { name: "Discard changes" }));
    await waitFor(() => expect(state.switches).toEqual(["b"]));
    await waitFor(() => expect(moves).toHaveLength(1));
  });

  it("stays on the list when a live Voice Session defers the switch", async () => {
    const { state, moves } = renderScreen({ campaigns: TWO, activeId: "a", liveOn: "a" });
    fireEvent.click(await screen.findByRole("button", { name: "Open Beta Tale" }));

    await waitFor(() => expect(state.switches).toEqual(["b"]));
    await waitFor(() =>
      expect(vi.mocked(toast.info)).toHaveBeenCalledWith(
        "A voice session is live — Beta Tale becomes the active campaign when it ends.",
      ),
    );
    // The Overview would still be the session's campaign: do not pretend.
    expect(moves).toEqual([]);
    expect(vi.mocked(toast.success)).not.toHaveBeenCalled();
  });

  it("shows a loading state, then the list", async () => {
    renderScreen({ campaigns: TWO, activeId: "a" });
    expect(screen.getByTestId("campaigns-loading")).toBeInTheDocument();
    // Named for assistive tech, not just drawn.
    expect(screen.getByRole("status", { name: "Loading…" })).toBeInTheDocument();
    await screen.findByRole("list", { name: "Your campaigns" });
    expect(screen.queryByTestId("campaigns-loading")).not.toBeInTheDocument();
  });

  it("says why the list could not be loaded, and Try again loads it", async () => {
    // The read is retried three times before it settles as failed: fail all four.
    renderScreen({ campaigns: TWO, activeId: "a", failLists: 4 });
    expect(await screen.findByRole("alert")).toHaveTextContent(/couldn't load campaigns.*campaign store offline/i);

    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("list", { name: "Your campaigns" })).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("offers the first-campaign form when there is no campaign yet", async () => {
    renderScreen({ campaigns: [] });
    expect(await screen.findByRole("heading", { name: /create your first campaign/i })).toBeInTheDocument();
    expect(screen.queryByRole("list", { name: "Your campaigns" })).not.toBeInTheDocument();
  });
});
