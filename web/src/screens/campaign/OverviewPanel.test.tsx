import { describe, it, expect, vi } from "vitest";
import { act, render, screen, fireEvent, waitFor, within } from "@testing-library/react";
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
  GetCampaignRosterResponseSchema,
  GetKnowledgeGraphResponseSchema,
  GetProviderHealthResponseSchema,
  GetRosterReadinessResponseSchema,
  GetSessionResponseSchema,
  HealthStatus,
  KnowledgeProposalSchema,
  ListCharactersResponseSchema,
  ListKnowledgeProposalsResponseSchema,
  ListMapsResponseSchema,
  ListPlanningThreadsResponseSchema,
  ListProviderConfigsResponseSchema,
  ListSessionsResponseSchema,
  NodeType,
  ProposedFactSchema,
  VoiceSessionSchema,
  type Agent,
  type KnowledgeProposal,
  type VoiceSession,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { MemoryScreenNav, type ScreenLocation } from "@/app/screenNav";
import { makeQueryClient } from "@/lib/queryClient";
import { CampaignScreenProvider, type CampaignScreen } from "./campaignContext";
import { OverviewPanel } from "./OverviewPanel";

// The Overview is a page of independent blocks over reads the console already
// had. These tests give it a campaign in a known state and check what each
// block says and where it sends the GM.

const campaign = create(CampaignSchema, {
  id: "c1",
  name: "The Sunless Citadel",
  system: "D&D 5e",
  language: "en",
});

const butler = create(AgentSchema, { id: "butler-1", role: "butler", name: "Glyphoxa", persona: "Dry." });
const npc = (id: string, name: string, voice: string): Agent =>
  create(AgentSchema, { id, role: "character", name, persona: "Grumbles.", voice });

const fact = (id: string): KnowledgeProposal =>
  create(KnowledgeProposalSchema, {
    id,
    authoringAgentName: "Glyphoxa",
    write: { case: "fact", value: create(ProposedFactSchema, { subject: "Bart", fact: "Fears the dark." }) },
  });

const pastSession = (
  id: string,
  startISO: string,
  minutes: number,
  extra: { status?: string; endReason?: string; lineCount?: number } = {},
) => {
  const started = new Date(startISO);
  return create(VoiceSessionSchema, {
    id,
    campaignId: "c1",
    status: "ended",
    startedAt: timestampFromDate(started),
    endedAt: timestampFromDate(new Date(started.getTime() + minutes * 60_000)),
    lineCount: 120,
    ...extra,
  });
};

type World = {
  roster: Agent[];
  proposals: KnowledgeProposal[];
  nodes: { id: string; name: string; nodeType?: NodeType; agentId?: string; bodyLen?: number }[];
  /** Agent ids the readiness read reports with facts in reach. */
  withFacts: string[];
  /** Agent ids the readiness read has no row for (it predates them). */
  readinessOmits?: string[];
  characters: number;
  maps: number;
  threads: number;
  sessions: VoiceSession[];
  guildId: string;
  integrationState: string;
  health: { component: string; provider: string; status: HealthStatus; code?: string; detail?: string }[];
  live: VoiceSession | null;
  /** Calls to GetKnowledgeGraph that fail before it answers. */
  graphFailures: number;
  mapsFail: boolean;
  rosterFail?: boolean;
  /** Every ListMaps call waits for this before it answers. */
  mapsHold?: Promise<void>;
};

// A campaign that is fully set up and has nothing waiting.
const prepared = (): World => ({
  roster: [butler, npc("npc-1", "Bart", "rachel")],
  proposals: [],
  nodes: [
    { id: "n-bart", name: "Bart", nodeType: NodeType.NPC, agentId: "npc-1", bodyLen: 200 },
    { id: "n-inn", name: "The Tankard", nodeType: NodeType.LOCATION, bodyLen: 80 },
  ],
  withFacts: ["npc-1"],
  characters: 4,
  maps: 2,
  threads: 3,
  sessions: [pastSession("s1", "2026-08-01T18:00:00Z", 185)],
  guildId: "123456789012345678",
  integrationState: "ok",
  health: [
    { component: "llm", provider: "groq", status: HealthStatus.HEALTHY },
    { component: "tts", provider: "elevenlabs", status: HealthStatus.HEALTHY },
  ],
  live: null,
  graphFailures: 0,
  mapsFail: false,
});

// A campaign straight out of "New campaign": the Butler and nothing else.
const brandNew = (): World => ({
  ...prepared(),
  roster: [butler],
  nodes: [],
  withFacts: [],
  characters: 0,
  maps: 0,
  threads: 0,
  sessions: [],
  guildId: "",
  integrationState: "waiting",
});

function mockTransport(world: World) {
  const calls = { graph: 0, maps: 0 };
  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      getCampaignRoster: () => {
        // NotFound is the settled "no campaign" answer: not retried.
        if (world.rosterFail) throw new ConnectError("no campaign", Code.NotFound);
        return create(GetCampaignRosterResponseSchema, { campaign, roster: world.roster });
      },
      listKnowledgeProposals: () =>
        create(ListKnowledgeProposalsResponseSchema, { proposals: world.proposals }),
      getKnowledgeGraph: () => {
        calls.graph++;
        if (calls.graph <= world.graphFailures) throw new ConnectError("graph is down", Code.Unavailable);
        return create(GetKnowledgeGraphResponseSchema, {
          nodes: world.nodes.map((n) => ({
            id: n.id,
            name: n.name,
            nodeType: n.nodeType ?? NodeType.LOCATION,
            agentId: n.agentId ?? "",
            bodyLen: n.bodyLen ?? 0,
          })),
          // Everything is connected to the first node, so no entry is an orphan.
          edges: world.nodes.slice(1).map((n, i) => ({
            id: `e${i}`,
            fromNodeId: world.nodes[0].id,
            toNodeId: n.id,
            edgeType: 1,
          })),
        });
      },
      getRosterReadiness: () =>
        create(GetRosterReadinessResponseSchema, {
          agents: world.roster
            .filter((a) => a.role === "character" && !world.readinessOmits?.includes(a.id))
            .map((a) => ({
              agentId: a.id,
              linked: true,
              factCount: world.withFacts.includes(a.id) ? 3 : 0,
              chars: 300,
              maxChars: 4000,
            })),
        }),
      listCharacters: () =>
        create(ListCharactersResponseSchema, {
          characters: Array.from({ length: world.characters }, (_, i) => ({ id: `pc-${i}`, name: `PC ${i}` })),
        }),
      listMaps: async () => {
        calls.maps++;
        if (world.mapsHold) await world.mapsHold;
        if (world.mapsFail) throw new ConnectError("maps are down", Code.Unavailable);
        return create(ListMapsResponseSchema, {
          maps: Array.from({ length: world.maps }, (_, i) => ({ id: `m-${i}`, name: `Map ${i}` })),
        });
      },
    });
    service(ChatService, {
      listPlanningThreads: () =>
        create(ListPlanningThreadsResponseSchema, {
          threads: Array.from({ length: world.threads }, (_, i) => ({ id: `t-${i}` })),
        }),
    });
    service(SessionService, {
      listSessions: () => create(ListSessionsResponseSchema, { sessions: world.sessions }),
      getSession: () =>
        create(GetSessionResponseSchema, world.live ? { session: world.live, active: true } : {}),
    });
    service(ProviderService, {
      listProviderConfigs: () =>
        create(ListProviderConfigsResponseSchema, {
          guildId: world.guildId,
          integrationState: world.integrationState,
        }),
    });
    service(VoiceService, {
      getProviderHealth: () => create(GetProviderHealthResponseSchema, { providers: world.health }),
    });
  });
  return { transport, calls };
}

function renderOverview(world: World) {
  const { transport, calls } = mockTransport(world);
  const jumps = { openView: vi.fn(), openAgent: vi.fn(), openNode: vi.fn() };
  const moves: ScreenLocation[] = [];
  const host: CampaignScreen = { campaign, roster: world.roster, ...jumps };
  // Failures are asserted directly, so the client does not sit through the
  // default retries first.
  const queryClient = makeQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false, staleTime: 30_000 } });
  render(
    <Providers transport={transport} queryClient={queryClient}>
      <MemoryScreenNav
        initial={{ screen: "campaign", view: "overview" }}
        onNavigate={(next) => moves.push(next)}
      >
        <CampaignScreenProvider value={host}>
          <OverviewPanel />
        </CampaignScreenProvider>
      </MemoryScreenNav>
    </Providers>,
  );
  return { ...jumps, moves, calls };
}

const tiles = () => screen.findByRole("list", { name: "Sections of this campaign" });
const attention = () => screen.getByRole("region", { name: "Needs your attention" });
// Every block has loaded once the checklist's slowest inputs are on screen.
const settled = async () => {
  await screen.findByRole("button", { name: /^Maps: \d+ maps?$/ });
  await screen.findByRole("region", { name: "Last sessions" });
  await waitFor(() => expect(screen.queryAllByRole("status")).toHaveLength(0));
};

describe("Overview header", () => {
  it("names the campaign, its System and its language, and leads to the session and the settings", async () => {
    const { openView, moves } = renderOverview(prepared());
    expect(await screen.findByRole("heading", { level: 1, name: "The Sunless Citadel" })).toBeInTheDocument();
    expect(screen.getByText("D&D 5e · English")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Campaign settings" }));
    expect(openView).toHaveBeenCalledWith("settings");

    // Idle: the action is to start one, on the Session screen.
    fireEvent.click(screen.getByRole("button", { name: "Start a session" }));
    expect(moves).toEqual([{ screen: "session", view: undefined, search: {} }]);
  });
});

describe("Overview next steps", () => {
  it("shows a brand-new campaign every step, each one a working link, and no error", async () => {
    const { openView, moves } = renderOverview(brandNew());
    const next = await screen.findByRole("region", { name: "Next steps" });
    expect(within(next).getByText("0 of 6 done")).toBeInTheDocument();

    const steps: [string, () => void][] = [
      ["Link your Discord server", () => expect(moves.at(-1)).toMatchObject({ screen: "configuration" })],
      ["Create your first NPC", () => expect(openView).toHaveBeenLastCalledWith("cast")],
      ["Add your first wiki entries", () => expect(openView).toHaveBeenLastCalledWith("knowledge")],
      ["Link your players", () => expect(openView).toHaveBeenLastCalledWith("players")],
      ["Upload or generate a map", () => expect(openView).toHaveBeenLastCalledWith("maps")],
      ["Run your first session", () => expect(moves.at(-1)).toMatchObject({ screen: "session" })],
    ];
    for (const [name, wentThere] of steps) {
      fireEvent.click(within(next).getByRole("button", { name: new RegExp(`^${name}`) }));
      wentThere();
    }

    await settled();
    // Nothing failed, and an empty campaign is not a problem to report.
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(within(attention()).getByText("Nothing needs you right now.")).toBeInTheDocument();
  });

  it("shows no checklist once the campaign is fully prepared", async () => {
    renderOverview(prepared());
    await settled();
    expect(screen.queryByRole("region", { name: "Next steps" })).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("keeps the checklist on screen while a failed read is retried", async () => {
    const world: World = { ...brandNew(), mapsFail: true };
    const { calls } = renderOverview(world);
    const next = await screen.findByRole("region", { name: "Next steps" });
    // The map step's read failed: it is left out, the rest are counted.
    await waitFor(() => expect(within(next).getByText("0 of 5 done")).toBeInTheDocument());

    world.mapsFail = false;
    let release = () => {};
    world.mapsHold = new Promise((r) => (release = r));
    const alert = await screen.findByRole("alert");
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(calls.maps).toBe(2));
    // The retry is on its way, which puts the read back to pending: the
    // checklist stays.
    await act(async () => {
      await new Promise((r) => setTimeout(r, 20));
    });
    expect(screen.getByRole("region", { name: "Next steps" })).toBeInTheDocument();
    release();
    expect(await screen.findByText("0 of 6 done")).toBeInTheDocument();
  });

  it("keeps the finished steps in view, marked done, while others are open", async () => {
    renderOverview({ ...prepared(), maps: 0, sessions: [] });
    const next = await screen.findByRole("region", { name: "Next steps" });
    expect(within(next).getByText("4 of 6 done")).toBeInTheDocument();
    // A finished step is text with a "Done" mark, not a link to redo it.
    expect(within(next).queryByRole("button", { name: /Link your Discord server/ })).not.toBeInTheDocument();
    expect(within(next).getByText("Link your Discord server")).toBeInTheDocument();
    expect(within(next).getAllByText("Done")).toHaveLength(4);
    expect(within(next).getByRole("button", { name: /^Upload or generate a map/ })).toBeInTheDocument();
  });
});

describe("Overview attention band", () => {
  it("counts pending suggestions and NPCs that are not ready, and links each to its view", async () => {
    const { openView, openAgent } = renderOverview({
      ...prepared(),
      proposals: [fact("p1"), fact("p2"), fact("p3")],
      roster: [butler, npc("npc-1", "Bart", "rachel"), npc("npc-2", "Mira", ""), npc("npc-3", "Odo", "")],
      nodes: [
        { id: "n-bart", name: "Bart", nodeType: NodeType.NPC, agentId: "npc-1", bodyLen: 200 },
        { id: "n-mira", name: "Mira", nodeType: NodeType.NPC, agentId: "npc-2", bodyLen: 90 },
        { id: "n-odo", name: "Odo", nodeType: NodeType.NPC, agentId: "npc-3", bodyLen: 90 },
      ],
      withFacts: ["npc-1", "npc-2", "npc-3"],
    });

    const band = await screen.findByRole("region", { name: "Needs your attention" });
    expect(await within(band).findByText("3 suggestions are waiting for your review")).toBeInTheDocument();
    expect(await within(band).findByText("2 NPCs are not ready for a session")).toBeInTheDocument();
    // It says who and what is missing; Bart, who has a voice, is not named.
    expect(within(band).getByText(/Mira/).closest("p")).toHaveTextContent("Mira: no voice · Odo: no voice");
    expect(within(band).queryByText(/Bart/)).not.toBeInTheDocument();

    fireEvent.click(within(band).getByRole("button", { name: "Review suggestions" }));
    expect(openView).toHaveBeenLastCalledWith("proposals");
    fireEvent.click(within(band).getByRole("button", { name: "Open Cast" }));
    expect(openView).toHaveBeenLastCalledWith("cast");
    // A name goes straight to that NPC's editor.
    fireEvent.click(within(band).getByRole("button", { name: "Open Mira in Cast" }));
    expect(openAgent).toHaveBeenCalledWith("npc-2");
  });

  it("does not flag an NPC the readiness read predates, as the Cast view does not", async () => {
    // Nova was created a moment ago: persona and voice are set, but neither the
    // graph nor the readiness read has caught up with her yet.
    renderOverview({
      ...prepared(),
      roster: [butler, npc("npc-1", "Bart", "rachel"), npc("npc-9", "Nova", "rachel")],
      readinessOmits: ["npc-9"],
    });
    await settled();
    expect(screen.queryByText(/not ready for a session/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Nova/)).not.toBeInTheDocument();
  });

  it("reports things to tidy in the World wiki, by kind", async () => {
    const { openView } = renderOverview({
      ...prepared(),
      nodes: [
        { id: "n-bart", name: "Bart", nodeType: NodeType.NPC, agentId: "npc-1", bodyLen: 200 },
        { id: "n-inn", name: "The Tankard", nodeType: NodeType.LOCATION, bodyLen: 80 },
        // An NPC entry nobody voices, connected like the rest.
        { id: "n-ilva", name: "Ilva", nodeType: NodeType.NPC, bodyLen: 40 },
      ],
    });
    const band = await screen.findByRole("region", { name: "Needs your attention" });
    expect(await within(band).findByText("1 thing to tidy up in the World wiki")).toBeInTheDocument();
    expect(within(band).getByText("NPC entries with no voice (1)")).toBeInTheDocument();
    fireEvent.click(within(band).getByRole("button", { name: "Open World wiki" }));
    expect(openView).toHaveBeenLastCalledWith("knowledge");
  });

  it("reports a provider that does not work, in the GM's words, and links to Setup", async () => {
    const { moves } = renderOverview({
      ...prepared(),
      health: [
        { component: "llm", provider: "groq", status: HealthStatus.HEALTHY },
        { component: "tts", provider: "elevenlabs", status: HealthStatus.DEGRADED, code: "key_rejected" },
      ],
    });
    const band = await screen.findByRole("region", { name: "Needs your attention" });
    expect(await within(band).findByText("The voice service needs a look in Setup")).toBeInTheDocument();
    expect(within(band).getByText(/The key was rejected/)).toBeInTheDocument();
    fireEvent.click(within(band).getByRole("button", { name: "Open Setup" }));
    expect(moves).toEqual([{ screen: "configuration", view: undefined, search: {} }]);
  });

  it("does not report what was simply never set up, unless a session cannot run without it", async () => {
    renderOverview({
      ...prepared(),
      health: [
        // No key for the language model: no NPC can answer. That is a problem.
        { component: "llm", provider: "groq", status: HealthStatus.DEGRADED, code: "missing_key" },
        { component: "tts", provider: "elevenlabs", status: HealthStatus.HEALTHY },
        // No image key and no Bot token: optional, or the checklist's business.
        { component: "image", provider: "gemini", status: HealthStatus.DEGRADED, code: "missing_key" },
        { component: "discord", provider: "discord", status: HealthStatus.DEGRADED, code: "missing_key" },
      ],
    });
    const band = await screen.findByRole("region", { name: "Needs your attention" });
    expect(await within(band).findByText("The language model needs a look in Setup")).toBeInTheDocument();
    expect(within(band).getAllByRole("listitem")).toHaveLength(1);
    expect(within(band).queryByText(/Image generation|Discord/)).not.toBeInTheDocument();
  });

  it("reports an image key that exists and fails", async () => {
    renderOverview({
      ...prepared(),
      health: [{ component: "image", provider: "gemini", status: HealthStatus.DEGRADED, code: "key_rejected" }],
    });
    const band = await screen.findByRole("region", { name: "Needs your attention" });
    expect(await within(band).findByText("Image generation needs a look in Setup")).toBeInTheDocument();
  });

  it("reports a rejected Discord bot token once", async () => {
    renderOverview({
      ...prepared(),
      integrationState: "failed",
      health: [{ component: "discord", provider: "discord", status: HealthStatus.DEGRADED, code: "key_rejected" }],
    });
    const band = await screen.findByRole("region", { name: "Needs your attention" });
    expect(await within(band).findByText("The Discord connection failed")).toBeInTheDocument();
    expect(within(band).getAllByRole("button", { name: "Open Setup" })).toHaveLength(1);
  });

  it("offers a retry only in the block whose read failed, and recovers with it", async () => {
    const { calls } = renderOverview({ ...prepared(), proposals: [fact("p1"), fact("p2"), fact("p3")], graphFailures: 1 });
    const band = await screen.findByRole("region", { name: "Needs your attention" });

    // The world source failed…
    const alert = await within(band).findByRole("alert");
    expect(alert).toHaveTextContent("Couldn't check your NPCs and the World wiki");
    // …while the suggestions beside it, and the rest of the page, loaded.
    expect(within(band).getByText("3 suggestions are waiting for your review")).toBeInTheDocument();
    await screen.findByRole("button", { name: /^Maps: 2 maps$/ });
    expect(await screen.findByRole("region", { name: "Last sessions" })).toBeInTheDocument();
    // The wiki tile has no figure, and is still the way into the wiki.
    expect(screen.getByRole("button", { name: "World wiki: count unavailable" })).toBeInTheDocument();
    // One failure, one retry, one alert on the whole page.
    expect(screen.getAllByRole("alert")).toHaveLength(1);
    expect(screen.getAllByRole("button", { name: "Try again" })).toHaveLength(1);

    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
    expect(calls.graph).toBe(2);
    expect(await screen.findByRole("button", { name: "World wiki: 2 entries" })).toBeInTheDocument();
  });
});

describe("Overview stat tiles", () => {
  it("shows a figure for every view and takes the GM there", async () => {
    const { openView } = renderOverview({ ...prepared(), proposals: [fact("p1")] });
    const list = await tiles();
    const names = [
      "Cast: 1 NPC",
      "World wiki: 2 entries",
      "Maps: 2 maps",
      "Players: 4 players",
      "Suggestions: 1 pending",
      "Planning: 3 conversations",
    ];
    for (const name of names) {
      expect(await within(list).findByRole("button", { name })).toBeInTheDocument();
    }
    fireEvent.click(within(list).getByRole("button", { name: "Players: 4 players" }));
    expect(openView).toHaveBeenLastCalledWith("players");
  });

  it("does not show the Planning tile as loading for good when the campaign cannot be read", async () => {
    renderOverview({ ...prepared(), rosterFail: true });
    const list = await tiles();
    // Planning threads are read by campaign id, which the failed roster read
    // never delivered: no figure, like the Cast tile, not a skeleton forever.
    expect(await within(list).findByRole("button", { name: "Planning: count unavailable" })).toBeInTheDocument();
    expect(within(list).getByRole("button", { name: "Cast: count unavailable" })).toBeInTheDocument();
  });

  it("reports a count it could not load under the tiles, with a retry", async () => {
    renderOverview({ ...prepared(), mapsFail: true });
    const list = await tiles();
    expect(await within(list).findByRole("button", { name: "Maps: count unavailable" })).toBeInTheDocument();
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Some counts couldn't be loaded");
    expect(within(alert).getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("Overview sessions", () => {
  it("shows the live card with the elapsed time while a session is running", async () => {
    const started = new Date(Date.now() - 65_000);
    const running = create(VoiceSessionSchema, {
      id: "s-live",
      campaignId: "c1",
      status: "running",
      startedAt: timestampFromDate(started),
    });
    const { moves } = renderOverview({ ...prepared(), live: running, sessions: [running, ...prepared().sessions] });

    expect(await screen.findByText("A voice session is running")).toBeInTheDocument();
    expect(screen.getByRole("timer")).toHaveTextContent(/^00:01:0\d$/);
    // The header's action follows: there is a session to open, not to start.
    expect(screen.queryByRole("button", { name: "Start a session" })).not.toBeInTheDocument();
    fireEvent.click(screen.getAllByRole("button", { name: "Open session" })[0]);
    expect(moves).toEqual([{ screen: "session", view: undefined, search: {} }]);

    // The running session is the live card's; it is not listed as history.
    const last = await screen.findByRole("region", { name: "Last sessions" });
    await waitFor(() => expect(within(last).getAllByRole("listitem")).toHaveLength(1));
  });

  it("lists the last three sessions with duration and line count, and marks how a session ended", async () => {
    const { moves } = renderOverview({
      ...prepared(),
      sessions: [
        pastSession("s4", "2026-08-22T18:00:00Z", 185),
        pastSession("s3", "2026-08-15T18:00:00Z", 12, {
          status: "failed",
          endReason: "gateway rejected the bot token",
          lineCount: 1,
        }),
        pastSession("s2", "2026-08-08T18:00:00Z", 40, {
          endReason: "idle_no_audio: the Voice Session processed no audio for the idle close window",
        }),
        pastSession("s1", "2026-08-01T18:00:00Z", 90),
      ],
    });
    const last = await screen.findByRole("region", { name: "Last sessions" });
    const rows = await within(last).findAllByRole("listitem");
    // Newest first, three of the four.
    expect(rows).toHaveLength(3);

    expect(rows[0]).toHaveTextContent("3h 5m");
    expect(rows[0]).toHaveTextContent("120 lines");
    expect(rows[0]).not.toHaveTextContent(/Failed|Ended automatically/);

    expect(rows[1]).toHaveTextContent("0h 12m");
    expect(rows[1]).toHaveTextContent("1 line");
    expect(rows[1]).toHaveTextContent("Failed");

    expect(rows[2]).toHaveTextContent("Ended automatically");

    // A row opens that session on the Session screen.
    fireEvent.click(within(rows[1]).getByRole("button"));
    expect(moves).toEqual([{ screen: "session", view: undefined, search: { session: "s3" } }]);
  });

  it("says so when no session has run yet", async () => {
    renderOverview({ ...prepared(), sessions: [] });
    const last = await screen.findByRole("region", { name: "Last sessions" });
    expect(await within(last).findByText("No sessions yet")).toBeInTheDocument();
  });
});
