import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { act, render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { createRouterTransport, ConnectError, Code } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { toast } from "sonner";

import {
  AgentSchema,
  CampaignSchema,
  CampaignService,
  CreateCampaignResponseSchema,
  GetActiveCampaignResponseSchema,
  GetCampaignRosterResponseSchema,
  GetSessionResponseSchema,
  HighlightSearchHitSchema,
  ListCampaignsResponseSchema,
  NodeSchema,
  NodeType,
  SearchHighlightsResponseSchema,
  SearchNodesResponseSchema,
  SearchTranscriptsResponseSchema,
  SessionService,
  SetActiveCampaignResponseSchema,
  TranscriptSearchHitSchema,
  VoiceSessionSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { MemoryScreenNav, type ScreenLocation } from "@/app/screenNav";
import { registerUnsavedEditsProbe } from "@/lib/navigationGuard";
import { makeQueryClient } from "@/lib/queryClient";
import { CommandPalette, paletteShortcut } from "./CommandPalette";
import { resetLocalCampaignSwitch } from "./useCampaignSwitch";

vi.mock("sonner", () => ({
  toast: Object.assign(vi.fn(), { error: vi.fn(), success: vi.fn(), info: vi.fn() }),
}));

// Ctrl/Cmd+K palette: a navigation layer filtered in the browser ("Go to",
// "Switch campaign", the cast, two actions) above the campaign search the
// server ranks (#591). Every move goes through the navigation seam, so these
// tests mount MemoryScreenNav and read where the palette went — no router.

const citadel = create(CampaignSchema, { id: "c1", name: "The Sunless Citadel", system: "D&D 5e", language: "en" });
const saltmarsh = create(CampaignSchema, { id: "c2", name: "Tides over Saltmarsh", system: "D&D 5e", language: "en" });

type Opts = {
  semantic?: boolean;
  empty?: boolean;
  transcriptsFail?: boolean;
  live?: boolean;
  noCampaign?: boolean;
  /** Mount the palette for a Platform Admin (ADR-0065). */
  platformAdmin?: boolean;
};

function backend(opts: Opts = {}) {
  const semantic = opts.semantic ?? true;
  const state = { calls: [] as string[], active: "c1", created: [] as string[] };
  const byId = (id: string) => (id === "c2" ? saltmarsh : citadel);
  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      getActiveCampaign: () => {
        if (opts.noCampaign) throw new ConnectError("no campaign", Code.NotFound);
        return create(GetActiveCampaignResponseSchema, { campaign: byId(state.active) });
      },
      listCampaigns: () =>
        create(ListCampaignsResponseSchema, { campaigns: opts.noCampaign ? [] : [citadel, saltmarsh] }),
      getCampaignRoster: () => {
        if (opts.noCampaign) throw new ConnectError("no campaign", Code.NotFound);
        return create(GetCampaignRosterResponseSchema, {
          campaign: citadel,
          roster: [
            create(AgentSchema, { id: "a-butler", role: "butler", name: "Butler", title: "Game assistant" }),
            create(AgentSchema, {
              id: "a-flo",
              role: "character",
              name: "Flo",
              title: "Gruff innkeeper",
              aliases: ["Florentine"],
            }),
            create(AgentSchema, { id: "a-zoe", role: "character", name: "Zoë", title: "Harbour master" }),
          ],
        });
      },
      setActiveCampaign: (req) => {
        state.calls.push(`setActive:${req.campaignId}`);
        // Live-first resolution (#222): while a session runs, its campaign
        // still answers as the resolved Active Campaign.
        if (!opts.live) state.active = req.campaignId;
        return create(SetActiveCampaignResponseSchema, { campaign: byId(state.active) });
      },
      createCampaign: (req) => {
        state.created.push(req.name);
        return create(CreateCampaignResponseSchema, {
          campaign: create(CampaignSchema, { id: "c-new", name: req.name, system: req.system, language: req.language }),
        });
      },
      searchNodes: (req) => {
        state.calls.push(`nodes:${req.query}`);
        if (opts.empty) return create(SearchNodesResponseSchema, { nodes: [] });
        return create(SearchNodesResponseSchema, {
          nodes: [
            create(NodeSchema, {
              id: "n-bart",
              campaignId: "c1",
              nodeType: NodeType.NPC,
              name: "Bart",
              body: "Gruff innkeeper",
              gmPrivate: true,
            }),
          ],
        });
      },
    });
    service(SessionService, {
      getSession: () =>
        create(GetSessionResponseSchema, {
          active: opts.live ?? false,
          session: opts.live ? create(VoiceSessionSchema, { id: "s1" }) : undefined,
        }),
      searchTranscripts: (req) => {
        state.calls.push(`transcripts:${req.query}`);
        if (opts.transcriptsFail) throw new ConnectError("boom", Code.Internal);
        if (opts.empty) return create(SearchTranscriptsResponseSchema, { semantic, hits: [] });
        return create(SearchTranscriptsResponseSchema, {
          semantic,
          hits: [
            create(TranscriptSearchHitSchema, {
              voiceSessionId: "vs1",
              lineId: "u:3",
              who: semantic ? "" : "Lena / Vex",
              kind: semantic ? "" : "player",
              at: timestampFromDate(new Date("2026-08-03T19:30:00Z")),
              snippet: "we promised Bart fifty gold",
            }),
          ],
        });
      },
      searchHighlights: (req) => {
        state.calls.push(`highlights:${req.query}`);
        if (opts.empty) return create(SearchHighlightsResponseSchema, { hits: [] });
        return create(SearchHighlightsResponseSchema, {
          hits: [
            create(HighlightSearchHitSchema, {
              id: "hl-1",
              voiceSessionId: "vs1",
              excerpt: "the barbarian ate the contract",
              reason: "table erupted",
              startsAt: timestampFromDate(new Date("2026-07-19T20:45:00Z")),
            }),
          ],
        });
      },
    });
  });
  return { transport, state };
}

function renderPalette(opts: Opts = {}) {
  const b = backend(opts);
  const moves: ScreenLocation[] = [];
  const queryClient = makeQueryClient();
  render(
    <Providers transport={b.transport} queryClient={queryClient}>
      <MemoryScreenNav initial={{ screen: "configuration" }} onNavigate={(next) => moves.push(next)}>
        <button type="button">somewhere on the page</button>
        <CommandPalette platformAdmin={opts.platformAdmin} />
      </MemoryScreenNav>
    </Providers>,
  );
  return { ...b, moves, queryClient };
}

function openPalette() {
  fireEvent.keyDown(document, { key: "k", ctrlKey: true });
}

const input = () => screen.getByPlaceholderText("Search or jump to…");
const palette = () => within(screen.getByTestId("command-palette"));
const options = () => palette().getAllByRole("option").map((o) => o.textContent);

function type(text: string) {
  fireEvent.change(input(), { target: { value: text } });
}

async function searchFor(text: string) {
  openPalette();
  type(text);
}

let stopProbe: (() => void) | undefined;
beforeEach(() => {
  vi.mocked(toast.success).mockClear();
  vi.mocked(toast.info).mockClear();
  resetLocalCampaignSwitch();
});
afterEach(() => {
  stopProbe?.();
  stopProbe = undefined;
  vi.unstubAllGlobals();
});

describe("CommandPalette shell", () => {
  it("opens on Ctrl+K, closes on Escape, and renders nothing while closed", () => {
    renderPalette();
    expect(screen.queryByTestId("command-palette")).not.toBeInTheDocument();

    openPalette();
    expect(screen.getByRole("dialog", { name: "Search and jump" })).toBeInTheDocument();
    expect(input()).toHaveFocus();

    fireEvent.keyDown(input(), { key: "Escape" });
    expect(screen.queryByTestId("command-palette")).not.toBeInTheDocument();

    // Cmd+K (macOS) toggles too.
    fireEvent.keyDown(document, { key: "k", metaKey: true });
    expect(screen.getByTestId("command-palette")).toBeInTheDocument();
  });

  it("leaves modified chords to the browser (Ctrl+Shift+K stays the devtools console)", () => {
    renderPalette();
    fireEvent.keyDown(document, { key: "K", ctrlKey: true, shiftKey: true });
    expect(screen.queryByTestId("command-palette")).not.toBeInTheDocument();
    fireEvent.keyDown(document, { key: "k", ctrlKey: true, altKey: true });
    expect(screen.queryByTestId("command-palette")).not.toBeInTheDocument();
  });

  it("runs no query while closed", async () => {
    const { queryClient } = renderPalette();
    // Nothing fetched, nothing cached: a closed palette costs every screen
    // (and every screen test) nothing.
    await act(async () => {});
    expect(queryClient.getQueryCache().getAll()).toHaveLength(0);
  });

  it("gives focus back to where it was when it closes", () => {
    renderPalette();
    const origin = screen.getByRole("button", { name: "somewhere on the page" });
    origin.focus();
    openPalette();
    expect(input()).toHaveFocus();
    fireEvent.keyDown(input(), { key: "Escape" });
    expect(origin).toHaveFocus();
  });

  it("starts every open on an empty field", () => {
    renderPalette();
    openPalette();
    type("wiki");
    fireEvent.keyDown(input(), { key: "Escape" });
    openPalette();
    expect(input()).toHaveValue("");
  });

  it("shows the keys the list answers to", () => {
    renderPalette();
    openPalette();
    const footer = screen.getByTestId("command-palette").querySelector(".gx-palette__footer") as HTMLElement;
    expect(footer).toHaveTextContent(/↑\s*↓\s*move/);
    expect(footer).toHaveTextContent(/↵\s*open/);
    expect(footer).toHaveTextContent(/Esc\s*close/);
  });

  it("names its chord for the platform: Ctrl+K, Strg+K in German, ⌘K on a Mac", () => {
    const en = (key: string) => (key === "palette.keyCtrl" ? "Ctrl" : key);
    const de = (key: string) => (key === "palette.keyCtrl" ? "Strg" : key);
    expect(paletteShortcut(en as never)).toBe("Ctrl+K");
    expect(paletteShortcut(de as never)).toBe("Strg+K");
    vi.stubGlobal("navigator", { platform: "MacIntel", language: "en-US" });
    expect(paletteShortcut(en as never)).toBe("⌘K");
  });
});

describe("CommandPalette navigation layer", () => {
  it("lists every destination on an empty query — the palette is a map of the app", async () => {
    renderPalette();
    openPalette();

    const goTo = palette().getByRole("group", { name: "Go to" });
    expect(within(goTo).getAllByRole("option").map((o) => o.textContent)).toEqual([
      "Overview",
      "Cast",
      "World wiki",
      "Maps",
      "Players",
      "Suggestions",
      "Planning",
      "Sharing",
      "Settings",
      "Session",
      "Campaigns",
      "Setup",
    ]);
    // …and says what typing will do. No search has run.
    expect(palette().getByText(/type to search this campaign/i)).toBeInTheDocument();
    expect(palette().queryByText("Entries")).not.toBeInTheDocument();
  });

  it.each([
    ["World wiki", { screen: "campaign", view: "knowledge", search: {} }],
    ["Settings", { screen: "campaign", view: "settings", search: {} }],
    ["Session", { screen: "session", view: undefined, search: {} }],
    ["Campaigns", { screen: "campaigns", view: undefined, search: {} }],
    ["Setup", { screen: "configuration", view: undefined, search: {} }],
  ])("goes to %s and closes", (label, location) => {
    const { moves } = renderPalette();
    openPalette();
    fireEvent.click(palette().getByRole("option", { name: label }));
    expect(moves).toEqual([location]);
    expect(screen.queryByTestId("command-palette")).not.toBeInTheDocument();
  });

  // The Admin screen is offered to a Platform Admin only (ADR-0065); the list
  // above — the default, a GM — pins that nobody else is shown it.
  it("adds Admin to 'Go to' for a Platform Admin, and goes there", () => {
    const { moves } = renderPalette({ platformAdmin: true });
    openPalette();
    const goTo = palette().getByRole("group", { name: "Go to" });
    expect(within(goTo).getAllByRole("option").map((o) => o.textContent).slice(-2)).toEqual(["Setup", "Admin"]);

    fireEvent.click(palette().getByRole("option", { name: "Admin" }));
    expect(moves).toEqual([{ screen: "admin", view: undefined, search: {} }]);
  });

  it("never finds Admin for a GM, however it is typed", () => {
    renderPalette();
    openPalette();
    expect(palette().queryByRole("option", { name: "Admin" })).not.toBeInTheDocument();
    type("admin");
    expect(palette().queryByRole("option", { name: "Admin" })).not.toBeInTheDocument();
  });

  it("narrows the destinations as the GM types, ignoring case and accents", () => {
    renderPalette();
    openPalette();
    type("WIKI");
    const goTo = palette().getByRole("group", { name: "Go to" });
    expect(within(goTo).getAllByRole("option").map((o) => o.textContent)).toEqual(["World wiki"]);
  });

  it("opens the first match on Enter — type, Enter, there", () => {
    const { moves } = renderPalette();
    openPalette();
    type("maps");
    fireEvent.keyDown(input(), { key: "Enter" });
    expect(moves).toEqual([{ screen: "campaign", view: "maps", search: {} }]);
  });

  it("offers an NPC by name, alias or accent-less spelling, and opens Cast on that NPC", async () => {
    const { moves } = renderPalette();
    openPalette();
    // The whole roster is not listed on an empty field…
    expect(palette().queryByRole("group", { name: "Cast" })).not.toBeInTheDocument();

    // …a name finds its NPC, with the role subtitle beside it.
    type("flo");
    const cast = await palette().findByRole("group", { name: "Cast" });
    expect(within(cast).getByRole("option", { name: /Flo.*Gruff innkeeper/ })).toBeInTheDocument();

    // So does an alias, and "zoe" finds Zoë.
    type("florentine");
    expect(within(palette().getByRole("group", { name: "Cast" })).getAllByRole("option")).toHaveLength(1);
    type("zoe");
    fireEvent.click(palette().getByRole("option", { name: /Zoë/ }));

    expect(moves).toEqual([{ screen: "campaign", view: "cast", search: { agent: "a-zoe" } }]);
    expect(screen.queryByTestId("command-palette")).not.toBeInTheDocument();
  });

  it("offers 'Switch campaign' for the other campaigns, never for the one already active", async () => {
    renderPalette();
    openPalette();
    expect(await palette().findByRole("option", { name: /Switch campaign: Tides over Saltmarsh/ })).toBeInTheDocument();
    expect(palette().queryByRole("option", { name: /Switch campaign: The Sunless Citadel/ })).not.toBeInTheDocument();
  });

  it("switches campaign through the guarded switch and says what happened", async () => {
    const { state, moves } = renderPalette();
    openPalette();
    fireEvent.click(await palette().findByRole("option", { name: /Switch campaign: Tides over Saltmarsh/ }));

    expect(screen.queryByTestId("command-palette")).not.toBeInTheDocument();
    await waitFor(() => expect(state.calls).toContain("setActive:c2"));
    await waitFor(() => expect(toast.success).toHaveBeenCalledWith("Now working in Tides over Saltmarsh"));
    // The GM stays on the page they were on; the screen follows the campaign.
    expect(moves).toEqual([]);
  });

  it("asks before a campaign switch discards unsaved edits", async () => {
    stopProbe = registerUnsavedEditsProbe(() => true);
    const { state } = renderPalette();
    openPalette();
    fireEvent.click(await palette().findByRole("option", { name: /Switch campaign: Tides over Saltmarsh/ }));

    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("Discard unsaved changes?");
    expect(state.calls).not.toContain("setActive:c2");

    // Cancel keeps the edits and the campaign.
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
    expect(state.calls).not.toContain("setActive:c2");

    // Discard switches.
    openPalette();
    fireEvent.click(await palette().findByRole("option", { name: /Switch campaign: Tides over Saltmarsh/ }));
    fireEvent.click(
      within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Discard changes" }),
    );
    await waitFor(() => expect(state.calls).toContain("setActive:c2"));
  });

  it("offers 'Go to live session' only while a Voice Session is live", async () => {
    const idle = renderPalette();
    openPalette();
    await waitFor(() => expect(idle.queryClient.isFetching()).toBe(0));
    expect(palette().queryByRole("option", { name: "Go to live session" })).not.toBeInTheDocument();
  });

  it("goes to the live session", async () => {
    const { moves } = renderPalette({ live: true });
    openPalette();
    fireEvent.click(await palette().findByRole("option", { name: "Go to live session" }));
    expect(moves).toEqual([{ screen: "session", view: undefined, search: {} }]);
  });

  it("creates a campaign from the 'New campaign' action and lands on its Overview", async () => {
    const { state, moves } = renderPalette();
    openPalette();
    type("new camp");
    fireEvent.click(palette().getByRole("option", { name: "New campaign" }));
    expect(screen.queryByTestId("command-palette")).not.toBeInTheDocument();

    const dialog = await screen.findByRole("dialog", { name: "New campaign" });
    const name = within(dialog).getByRole("textbox", { name: /name/i });
    await waitFor(() => expect(name).toHaveFocus());
    fireEvent.change(name, { target: { value: "Curse of the Crimson Throne" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Create campaign" }));

    await waitFor(() => expect(state.created).toEqual(["Curse of the Crimson Throne"]));
    await waitFor(() => expect(state.calls).toContain("setActive:c-new"));
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "New campaign" })).not.toBeInTheDocument());
    expect(toast.success).toHaveBeenCalledWith("Now working in Curse of the Crimson Throne");
    expect(moves).toEqual([{ screen: "campaign", view: "overview", search: {} }]);
  });

  it("does not pretend a campaign created mid-session is active: it says the switch is pending and stays", async () => {
    const { state, moves, queryClient } = renderPalette({ live: true });
    openPalette();
    await waitFor(() => expect(queryClient.isFetching()).toBe(0));
    fireEvent.click(palette().getByRole("option", { name: "New campaign" }));

    const dialog = await screen.findByRole("dialog", { name: "New campaign" });
    // The dialog already says so before anything is created.
    await waitFor(() => expect(dialog).toHaveTextContent(/a voice session is live/i));
    fireEvent.change(within(dialog).getByRole("textbox", { name: /name/i }), { target: { value: "One-shot" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Create campaign" }));

    await waitFor(() => expect(state.created).toEqual(["One-shot"]));
    await waitFor(() => expect(toast.info).toHaveBeenCalledWith(expect.stringContaining("One-shot")));
    expect(toast.success).not.toHaveBeenCalled();
    expect(moves).toEqual([]);
  });

  it("asks before 'New campaign' would discard unsaved edits, and cancelling changes nothing", async () => {
    stopProbe = registerUnsavedEditsProbe(() => true);
    renderPalette();
    openPalette();
    fireEvent.click(palette().getByRole("option", { name: "New campaign" }));

    const ask = await screen.findByRole("alertdialog");
    expect(ask).toHaveTextContent("Discard unsaved changes?");
    expect(screen.queryByRole("dialog", { name: "New campaign" })).not.toBeInTheDocument();
    fireEvent.click(within(ask).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
    expect(screen.queryByRole("dialog", { name: "New campaign" })).not.toBeInTheDocument();
  });

  it("still lists every destination when the Tenant has no campaign yet", async () => {
    const { queryClient } = renderPalette({ noCampaign: true });
    openPalette();
    await waitFor(() => expect(queryClient.isFetching()).toBe(0));
    expect(within(palette().getByRole("group", { name: "Go to" })).getAllByRole("option")).toHaveLength(12);
    expect(palette().getByRole("option", { name: "New campaign" })).toBeInTheDocument();
    expect(palette().queryByRole("alert")).not.toBeInTheDocument();
  });

  it("is localized", () => {
    localStorage.setItem("gx-lang", "de");
    try {
      renderPalette();
      openPalette();
      expect(screen.getByRole("dialog", { name: "Suchen und springen" })).toBeInTheDocument();
      const goTo = palette().getByRole("group", { name: "Gehe zu" });
      expect(within(goTo).getByRole("option", { name: "Welt-Wiki" })).toBeInTheDocument();
      expect(palette().getByRole("option", { name: "Neue Kampagne" })).toBeInTheDocument();
    } finally {
      localStorage.removeItem("gx-lang");
    }
  });
});

describe("CommandPalette campaign search (#591)", () => {
  it("searches all three sources (debounced) and renders the grouped results under the navigation hits", async () => {
    const { state } = renderPalette();
    await searchFor("bart");

    // All three groups render their hits.
    expect(await screen.findByText("Bart")).toBeInTheDocument();
    expect(await screen.findByText("we promised Bart fifty gold")).toBeInTheDocument();
    expect(await screen.findByText("the barbarian ate the contract")).toBeInTheDocument();
    expect(screen.getByText("Entries")).toBeInTheDocument();
    expect(screen.getByText("Transcripts")).toBeInTheDocument();
    expect(screen.getByText("Highlights")).toBeInTheDocument();

    // One debounced RPC per source — not one per keystroke.
    expect(state.calls.filter((c) => c.startsWith("nodes:"))).toEqual(["nodes:bart"]);

    // Semantic mode shows NO degraded notice.
    expect(screen.queryByTestId("palette-degraded")).not.toBeInTheDocument();
    // Nothing in the navigation layer is called "bart", so only search hits show.
    expect(options()).toHaveLength(3);
  });

  it("deep-links a node hit to the World wiki and closes", async () => {
    const { moves } = renderPalette();
    await searchFor("bart");
    fireEvent.click(await screen.findByText("Bart"));

    expect(moves).toEqual([{ screen: "campaign", view: "knowledge", search: { node: "n-bart" } }]);
    expect(screen.queryByTestId("command-palette")).not.toBeInTheDocument();
  });

  it("deep-links a transcript hit with the {session, line} pair", async () => {
    const { moves } = renderPalette();
    await searchFor("gold");
    fireEvent.click(await screen.findByText("we promised Bart fifty gold"));

    expect(moves).toEqual([{ screen: "session", view: undefined, search: { session: "vs1", line: "u:3" } }]);
  });

  it("deep-links a highlight hit with {session, highlight}", async () => {
    const { moves } = renderPalette();
    await searchFor("contract");
    fireEvent.click(await screen.findByText("the barbarian ate the contract"));

    expect(moves).toEqual([{ screen: "session", view: undefined, search: { session: "vs1", highlight: "hl-1" } }]);
  });

  it("labels keyword transcript mode honestly (semantic=false → degraded notice, speaker shown)", async () => {
    renderPalette({ semantic: false });
    await searchFor("gold");

    expect(await screen.findByTestId("palette-degraded")).toBeInTheDocument();
    expect(screen.getByText(/keyword matches — semantic search is unavailable/i)).toBeInTheDocument();
    // The keyword hit leads with its speaker.
    expect(screen.getByText("Lena / Vex")).toBeInTheDocument();
  });

  it("shows the honest empty state when nothing matches anywhere", async () => {
    renderPalette({ empty: true });
    await searchFor("nothingmatchesthis");

    expect(await screen.findByText(/no results for “nothingmatchesthis”/i)).toBeInTheDocument();
  });

  it("does not say 'no results' while a destination still matches", async () => {
    const { state } = renderPalette({ empty: true });
    await searchFor("wiki");
    await waitFor(() => expect(state.calls).toContain("highlights:wiki"));
    await act(async () => {});
    expect(palette().getByRole("option", { name: "World wiki" })).toBeInTheDocument();
    expect(screen.queryByText(/no results for/i)).not.toBeInTheDocument();
  });

  it("surfaces a failed source inline while the others still answer", async () => {
    renderPalette({ transcriptsFail: true });
    await searchFor("bart");

    // The failing source announces itself…
    expect(await screen.findByRole("alert")).toHaveTextContent(/transcripts: search failed/i);
    // …and the healthy sources still render.
    expect(await screen.findByText("Bart")).toBeInTheDocument();
    expect(await screen.findByText("the barbarian ate the contract")).toBeInTheDocument();
    // The all-quiet empty state must NOT show beside an error.
    await waitFor(() => expect(screen.queryByText(/no results for/i)).not.toBeInTheDocument());
  });
});
