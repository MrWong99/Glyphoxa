import { describe, it, expect } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";

import {
  CampaignService,
  ChatService,
  ProviderService,
  SessionService,
  VoiceService,
  AgentSchema,
  CampaignSchema,
  CharacterSchema,
  GetActiveCampaignResponseSchema,
  GetCampaignRosterResponseSchema,
  GetCampaignTagsResponseSchema,
  GetKnowledgeGraphResponseSchema,
  GetProviderHealthResponseSchema,
  GetRosterReadinessResponseSchema,
  GetSessionResponseSchema,
  ListCharactersResponseSchema,
  ListDiscordVoiceMembersResponseSchema,
  ListKnowledgeProposalsResponseSchema,
  ListMapsResponseSchema,
  ListNodeAppearancesResponseSchema,
  ListNodesResponseSchema,
  ListPlanningThreadsResponseSchema,
  ListProviderConfigsResponseSchema,
  ListSessionsResponseSchema,
  ListSupportedLanguagesResponseSchema,
  ListVoicesResponseSchema,
  NodeSchema,
  NodeType,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { MemoryScreenNav, useScreenNav, type ScreenLocation } from "@/app/screenNav";
import { makeQueryClient } from "@/lib/queryClient";
import { Campaign } from "./Campaign";
import { CAMPAIGN_VIEWS, type CampaignView } from "./views";

// The Campaign screen is a thin host (ADR-0063 amendment): it resolves the
// campaign, renders the header, and switches on the view. These tests cover
// THAT — the view switch, the header, what survives a view change, and the
// cross-view jumps the host turns into navigations. What a view does is its
// panel's test file (CastPanel.test.tsx, KnowledgePanel.test.tsx, …).

// One campaign with a Butler and one NPC, plus just enough of every panel's
// reads for each view to mount.
function mockTransport() {
  const campaign = create(CampaignSchema, {
    id: "c1",
    name: "The Sunless Citadel",
    system: "D&D 5e",
    language: "en",
  });
  const butler = create(AgentSchema, {
    id: "butler-1",
    campaignId: "c1",
    role: "butler",
    name: "Glyphoxa",
    persona: "Dry and exact.",
    addressOnly: true,
  });
  const bart = create(AgentSchema, {
    id: "npc-1",
    campaignId: "c1",
    role: "character",
    name: "Bart",
    title: "Gruff innkeeper",
    persona: "Grumbles.",
    voice: "rachel",
  });
  return createRouterTransport(({ service }) => {
    service(VoiceService, {
      listVoices: () => create(ListVoicesResponseSchema, { voices: [] }),
      getProviderHealth: () => create(GetProviderHealthResponseSchema, {}),
    });
    // The Overview's reads beside the campaign's own (its blocks are covered in
    // OverviewPanel.test.tsx; here they only have to answer).
    service(ProviderService, {
      listProviderConfigs: () => create(ListProviderConfigsResponseSchema, { guildId: "123456789012345678" }),
    });
    service(SessionService, {
      listSessions: () => create(ListSessionsResponseSchema, {}),
      getSession: () => create(GetSessionResponseSchema, {}),
    });
    service(ChatService, {
      listPlanningThreads: () => create(ListPlanningThreadsResponseSchema, {}),
    });
    service(CampaignService, {
      listMaps: () => create(ListMapsResponseSchema, {}),
      getCampaignRoster: () => create(GetCampaignRosterResponseSchema, { campaign, roster: [butler, bart] }),
      getActiveCampaign: () => create(GetActiveCampaignResponseSchema, { campaign }),
      listSupportedLanguages: () => create(ListSupportedLanguagesResponseSchema, { languages: ["de", "en"] }),
      // Players view (#279).
      listCharacters: () =>
        create(ListCharactersResponseSchema, {
          characters: [create(CharacterSchema, { id: "pc-1", name: "Kira", discordUserId: "111111111111111111" })],
        }),
      listDiscordVoiceMembers: () => create(ListDiscordVoiceMembersResponseSchema, {}),
      // Suggestions view (#300).
      listKnowledgeProposals: () => create(ListKnowledgeProposalsResponseSchema, { proposals: [] }),
      // World wiki: Bart's entry, linked to the NPC but still empty.
      listNodes: () =>
        create(ListNodesResponseSchema, {
          nodes: [create(NodeSchema, { id: "node-bart", nodeType: NodeType.NPC, name: "Bart of the Tankard", body: "" })],
        }),
      getCampaignTags: () => create(GetCampaignTagsResponseSchema, { entries: [] }),
      listNodeAppearances: () =>
        create(ListNodeAppearancesResponseSchema, {
          appearances: [
            {
              voiceSessionId: "s1",
              lineId: "u:7",
              at: timestampFromDate(new Date("2026-08-01T20:15:00Z")),
              who: "Ilva",
              kind: "player",
              text: "we should ask Bart about the ledger",
              sessionStartedAt: timestampFromDate(new Date("2026-08-01T20:00:00Z")),
            },
          ],
        }),
      // Session-prep mode of Cast (#544).
      getKnowledgeGraph: () =>
        create(GetKnowledgeGraphResponseSchema, {
          nodes: [{ id: "node-bart", nodeType: 2, name: "Bart of the Tankard", agentId: "npc-1", bodyLen: 0 }],
          edges: [],
        }),
      getRosterReadiness: () =>
        create(GetRosterReadinessResponseSchema, {
          agents: [{ agentId: "npc-1", linked: true, factCount: 3, chars: 400, maxChars: 4000 }],
        }),
    });
  });
}

// SidebarStandIn offers one "nav:<view>" button per view — the sidebar's part.
// It moves through the same seam the real sidebar's links resolve to.
function SidebarStandIn() {
  const nav = useScreenNav();
  return (
    <nav aria-label="test-sidebar">
      {CAMPAIGN_VIEWS.map((v) => (
        <button key={v} type="button" onClick={() => nav.goCampaignView(v)}>
          {`nav:${v}`}
        </button>
      ))}
    </nav>
  );
}

type Move = { next: ScreenLocation; replace: boolean };

// The harness stands in for the route layer: MemoryScreenNav holds the location
// and applies the router's rules, and the screen is rendered for whatever view
// that location names — exactly what subviewRoute does with the path param.
function renderScreen(initial: { view?: CampaignView; search?: ScreenLocation["search"] } = {}) {
  const moves: Move[] = [];
  const view = render(
    <Providers transport={mockTransport()} queryClient={makeQueryClient()}>
      <MemoryScreenNav
        initial={{ screen: "campaign", view: initial.view ?? "cast", search: initial.search }}
        onNavigate={(next, opts) => moves.push({ next, replace: opts.replace })}
      >
        {(loc) => (
          <>
            <SidebarStandIn />
            {loc.screen === "campaign" && loc.view ? (
              <Campaign view={loc.view} />
            ) : (
              <p data-testid="other-screen">{loc.screen}</p>
            )}
          </>
        )}
      </MemoryScreenNav>
    </Providers>,
  );
  return { moves, container: view.container };
}

function switchView(view: CampaignView) {
  fireEvent.click(screen.getByRole("button", { name: `nav:${view}` }));
}

const persona = () => screen.getByLabelText("Personality & backstory") as HTMLTextAreaElement;
const rosterRow = (name: string) => screen.getByText(name).closest("button") as HTMLButtonElement;

describe("Campaign host", () => {
  it("renders the campaign's name and System with the view's lede, and marks the view on the root", async () => {
    const { container } = renderScreen({ view: "cast" });
    expect(await screen.findByRole("heading", { level: 1, name: "The Sunless Citadel" })).toBeInTheDocument();
    expect(screen.getByText("D&D 5e")).toBeInTheDocument();
    expect(screen.getByText(/one butler is required/i)).toBeInTheDocument();
    // The roster is the Cast panel's, mounted under the header.
    expect(await screen.findByText("Bart")).toBeInTheDocument();
    // data-view is the hook a view's stylesheet targets the screen root with.
    expect(container.querySelector(".gx-campaign-screen")).toHaveAttribute("data-view", "cast");

    switchView("players");
    await waitFor(() =>
      expect(container.querySelector(".gx-campaign-screen")).toHaveAttribute("data-view", "players"),
    );
    expect(screen.getByText(/link each discord user/i)).toBeInTheDocument();
  });

  it("defaults to the Overview: one header, and a tile for every other view of the campaign", async () => {
    render(
      <Providers transport={mockTransport()} queryClient={makeQueryClient()}>
        <Campaign />
      </Providers>,
    );
    // No view named → the Overview, with no router and no provider at all.
    const tiles = await screen.findByRole("list", { name: "Sections of this campaign" });
    for (const label of ["Cast", "World wiki", "Maps", "Players", "Suggestions", "Planning"]) {
      expect(within(tiles).getByRole("button", { name: new RegExp(`^${label}:`) })).toBeInTheDocument();
    }
    // The Overview does not link to itself, and Settings is a header action.
    expect(within(tiles).queryByRole("button", { name: /^Overview/ })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Campaign settings" })).toBeInTheDocument();
    // The Overview brings its own header; the host does not render a second.
    expect(await screen.findAllByRole("heading", { level: 1 })).toHaveLength(1);
    expect(screen.getByRole("heading", { level: 1, name: "The Sunless Citadel" })).toBeInTheDocument();
    expect(screen.getByText("D&D 5e · English")).toBeInTheDocument();
  });

  it("goes to a view from its Overview tile", async () => {
    const { moves } = renderScreen({ view: "overview" });
    const tiles = await screen.findByRole("list", { name: "Sections of this campaign" });
    fireEvent.click(await within(tiles).findByRole("button", { name: "Players: 1 player" }));

    expect(await screen.findByText("Kira")).toBeInTheDocument();
    expect(moves).toEqual([{ next: { screen: "campaign", view: "players", search: {} }, replace: false }]);
  });

  it("jumps from the Overview's attention band to the NPC that is not ready", async () => {
    // Bart's entry is linked but empty, so he is not session-ready.
    const { moves } = renderScreen({ view: "overview" });
    fireEvent.click(await screen.findByRole("button", { name: "Open Bart in Cast" }));
    // The host turns the panel's jump into a navigation carrying the Agent…
    expect(moves[0]).toEqual({
      next: { screen: "campaign", view: "cast", search: { agent: "npc-1" } },
      replace: false,
    });
    // …and Cast opens on him.
    await waitFor(() => expect(persona().value).toBe("Grumbles."));
  });

  it("goes to the Session and Setup screens from the Overview", async () => {
    const { moves } = renderScreen({ view: "overview" });
    fireEvent.click(await screen.findByRole("button", { name: "Start a session" }));
    expect(moves).toEqual([{ next: { screen: "session", view: undefined, search: {} }, replace: false }]);
    expect(await screen.findByTestId("other-screen")).toHaveTextContent("session");
  });

  it("mounts the Players panel on its view and leaves Cast unchanged (#279)", async () => {
    renderScreen();
    expect(await screen.findByText("Bart")).toBeInTheDocument();

    // Players mounts on its own ListCharacters RPC.
    switchView("players");
    expect(await screen.findByText("Kira")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /add player/i })).toBeInTheDocument();
    // The roster (Cast) is no longer mounted.
    expect(screen.queryByText("Bart")).not.toBeInTheDocument();

    switchView("cast");
    expect(await screen.findByText("Bart")).toBeInTheDocument();
  });

  it("mounts the Suggestions review panel on its view (#300)", async () => {
    renderScreen();
    expect(await screen.findByText("Bart")).toBeInTheDocument();

    switchView("proposals");
    expect(await screen.findByText(/No pending suggestions/i)).toBeInTheDocument();
    expect(screen.queryByText("Bart")).not.toBeInTheDocument();
  });

  it("mounts the campaign's settings as a page of their own", async () => {
    renderScreen({ view: "settings" });
    // The form is seeded from the Active Campaign.
    await waitFor(() =>
      expect((screen.getByLabelText("Name") as HTMLInputElement).value).toBe("The Sunless Citadel"),
    );
    expect(screen.getByText(/name, game system, language and highlight recording/i)).toBeInTheDocument();
  });

  it("keeps the selected NPC and the cast mode when the GM leaves Cast and comes back", async () => {
    renderScreen();
    await screen.findByText("Bart");
    // The Butler is selected by default; pick Bart.
    fireEvent.click(rosterRow("Bart"));
    await waitFor(() => expect(persona().value).toBe("Grumbles."));

    // A sidebar switch unmounts the Cast panel…
    switchView("players");
    expect(await screen.findByText("Kira")).toBeInTheDocument();
    // …and coming back finds Bart still selected, not the Butler again.
    switchView("cast");
    await waitFor(() => expect(persona().value).toBe("Grumbles."));

    // Same for the mode within Cast.
    fireEvent.click(screen.getByRole("button", { name: "Session prep" }));
    switchView("players");
    await screen.findByText("Kira");
    switchView("cast");
    expect(await screen.findByRole("button", { name: "Session prep" })).toHaveAttribute("aria-pressed", "true");
  });

  it("offers the first-campaign card on every view while no campaign exists (#267)", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(CampaignService, {
        getCampaignRoster: () => {
          throw new ConnectError("no active campaign", Code.NotFound);
        },
      });
    });
    const { container } = render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <Campaign view="overview" />
      </Providers>,
    );
    // CodeNotFound settles at once (no retries): the create form, not an error.
    expect(await screen.findByRole("heading", { name: /create your first campaign/i })).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(container.querySelector(".gx-campaign-screen")).toHaveAttribute("data-view", "overview");
  });

  it("sits every view on the shared page container", async () => {
    const { container } = renderScreen({ view: "players" });
    await screen.findByText("Kira");
    // One root: the Page primitive, still carrying the hooks views style by.
    const root = container.querySelector(".gx-campaign-screen");
    expect(root).toHaveClass("gx-page");
    expect(container.querySelectorAll(".gx-page")).toHaveLength(1);
  });
});

describe("Campaign host load states", () => {
  // Planning is the one view the host cannot mount before the campaign is
  // known: its reads are keyed by campaign id (ADR-0062).
  type Roster = ReturnType<typeof create<typeof GetCampaignRosterResponseSchema>>;
  function renderPlanning(roster: () => Roster | Promise<Roster>) {
    const transport = createRouterTransport(({ service }) => {
      service(CampaignService, { getCampaignRoster: roster });
      service(ChatService, { listPlanningThreads: () => create(ListPlanningThreadsResponseSchema, {}) });
      service(ProviderService, { listProviderConfigs: () => create(ListProviderConfigsResponseSchema, {}) });
    });
    const queryClient = makeQueryClient();
    // The roster read retries a transient failure; without the backoff the
    // test reaches the settled error at once.
    queryClient.setDefaultOptions({ queries: { retryDelay: 0, staleTime: 30_000 } });
    return render(
      <Providers transport={transport} queryClient={queryClient}>
        <Campaign view="planning" />
      </Providers>,
    );
  }

  it("shows a loading placeholder for Planning until the campaign is known", async () => {
    let release: () => void = () => {};
    const gate = new Promise<void>((resolve) => {
      release = resolve;
    });
    renderPlanning(async () => {
      await gate;
      return create(GetCampaignRosterResponseSchema, {
        campaign: create(CampaignSchema, { id: "c1", name: "The Sunless Citadel" }),
      });
    });
    // A named status region, not a bare shimmer bar.
    expect(await screen.findByRole("status", { name: "Loading…" })).toBeInTheDocument();
    release();
    expect(await screen.findByRole("heading", { level: 1, name: "The Sunless Citadel" })).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole("status", { name: "Loading…" })).not.toBeInTheDocument());
  });

  it("says the campaign could not be loaded and retries on request", async () => {
    let healthy = false;
    let calls = 0;
    renderPlanning(() => {
      calls++;
      if (!healthy) throw new ConnectError("database is restarting", Code.Unavailable);
      return create(GetCampaignRosterResponseSchema, {
        campaign: create(CampaignSchema, { id: "c1", name: "The Sunless Citadel" }),
      });
    });

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Couldn't load the campaign");
    // The server's own sentence is there for whoever is asked what it said.
    expect(alert).toHaveTextContent("database is restarting");

    healthy = true;
    const before = calls;
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("heading", { level: 1, name: "The Sunless Citadel" })).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
    expect(calls).toBeGreaterThan(before);
  });
});

describe("Campaign cross-view jumps", () => {
  it("opens the wiki on the entry the route's ?node= names, then strips the param (#591)", async () => {
    const { moves } = renderScreen({ view: "knowledge", search: { node: "node-bart" } });
    // The editor opens ON that entry once the wiki's list has loaded it — the
    // focus is the URL's, the host keeps no copy of it.
    await waitFor(() =>
      expect((screen.getByLabelText("Name") as HTMLInputElement).value).toBe("Bart of the Tankard"),
    );
    // Then — and only then — the param goes, by REPLACE: the URL does not pin
    // the entry and Back does not step through the deep link.
    await waitFor(() =>
      expect(moves).toEqual([{ next: { screen: "campaign", view: "knowledge", search: {} }, replace: true }]),
    );
  });

  it("keeps ?node= while the entry it names has not been found", async () => {
    const { moves } = renderScreen({ view: "knowledge", search: { node: "no-such-entry" } });
    // The wiki renders its list; the unknown id opens nothing…
    expect(await screen.findByRole("button", { name: "Edit Bart of the Tankard" })).toBeInTheDocument();
    expect((screen.getByLabelText("Name") as HTMLInputElement).value).toBe("");
    // …and the host does not strip a focus the panel never honoured.
    expect(moves).toEqual([]);
  });

  it("jumps from a Session-prep mark to the NPC's wiki entry", async () => {
    const { moves } = renderScreen({ view: "cast" });
    fireEvent.click(await screen.findByRole("button", { name: "Session prep" }));
    fireEvent.click(
      await screen.findByRole("button", { name: "Bart: Entry has content — missing, fix it" }),
    );

    // A real navigation to the wiki carrying the entry (pushed: Back returns to
    // the prep view)…
    expect(moves[0]).toEqual({
      next: { screen: "campaign", view: "knowledge", search: { node: "node-bart" } },
      replace: false,
    });
    // …where the entry opens and the param is consumed.
    await waitFor(() =>
      expect((screen.getByLabelText("Name") as HTMLInputElement).value).toBe("Bart of the Tankard"),
    );
    await waitFor(() => expect(moves).toHaveLength(2));
    expect(moves[1]).toEqual({ next: { screen: "campaign", view: "knowledge", search: {} }, replace: true });
  });

  it("opens Cast on the NPC a ?agent= jump names", async () => {
    const { moves } = renderScreen({ view: "cast", search: { agent: "npc-1" } });
    await waitFor(() => expect(persona().value).toBe("Grumbles."));
    expect(moves).toEqual([{ next: { screen: "campaign", view: "cast", search: {} }, replace: true }]);
  });

  it("jumps from a wiki entry's mention to that Transcript Line on the Session screen (#545)", async () => {
    const { moves } = renderScreen({ view: "knowledge" });
    fireEvent.click(await screen.findByRole("button", { name: "Edit Bart of the Tankard" }));
    fireEvent.click(await screen.findByRole("button", { name: /we should ask Bart/ }));

    // The session+line pair — never the line alone: line ids restart per session.
    expect(moves).toEqual([
      { next: { screen: "session", view: undefined, search: { session: "s1", line: "u:7" } }, replace: false },
    ]);
    expect(await screen.findByTestId("other-screen")).toHaveTextContent("session");
  });
});
