import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { createRouterTransport, type Interceptor } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import type { ReactNode } from "react";

import {
  CampaignService,
  GetCampaignRosterResponseSchema,
  GetCampaignTagsResponseSchema,
  GetMapViewResponseSchema,
  GuestRight as R,
  HighlightSchema,
  ListHighlightsResponseSchema,
  ListMapsResponseSchema,
  ListNodeEdgesResponseSchema,
  ListNodesResponseSchema,
  ListSessionsResponseSchema,
  NodeType,
  SessionService,
  VoiceSessionSchema,
  type VoiceSession,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { MemoryScreenNav } from "@/app/screenNav";
import { makeQueryClient } from "@/lib/queryClient";
import { AsGuest } from "@/test/guest";
import { KnowledgePanel } from "./campaign/KnowledgePanel";
import { MapsPanel } from "./campaign/MapsPanel";
import { HighlightsStrip } from "./session/HighlightsStrip";
import { SessionArchive } from "./session/SessionArchive";

vi.mock("sonner", () => ({
  toast: Object.assign(vi.fn(), { error: vi.fn(), success: vi.fn(), info: vi.fn(), warning: vi.fn(), dismiss: vi.fn() }),
  Toaster: () => null,
}));

// The screens a Campaign Guest can reach (ADR-0065), each rendered three ways:
// for the GM (no user in context: "everything allowed" — what these screens
// have always shown), for a guest who may only read, and for a guest who may
// also edit. A control whose RPC the viewer lacks the right for must be ABSENT,
// not left to fail — and the GM's control must still be there, so a check that
// hides too much fails as loudly as one that hides too little.
//
// Every transport records the procedures it was asked for, so a test can also
// show that a guest's screen never ASKED for a GM-only read.

function recorder() {
  const calls: string[] = [];
  const log: Interceptor = (next) => (req) => {
    calls.push(req.method.name);
    return next(req);
  };
  return { calls, options: { transport: { interceptors: [log] } } };
}

/** Mounts a screen for the GM (no rights given) or for a guest with exactly these rights. */
function mount(transport: ReturnType<typeof createRouterTransport>, ui: ReactNode, rights?: R[]) {
  const tree = <MemoryScreenNav initial={{ screen: "campaign" }}>{ui}</MemoryScreenNav>;
  return render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      {rights ? <AsGuest rights={rights}>{tree}</AsGuest> : tree}
    </Providers>,
  );
}

// ── World wiki ───────────────────────────────────────────────────────────────

function wiki() {
  const rec = recorder();
  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      listNodes: () =>
        create(ListNodesResponseSchema, {
          nodes: [{ id: "n1", nodeType: NodeType.LOCATION, name: "The Citadel Gate", body: "A rusted portcullis." }],
        }),
      getCampaignTags: () => create(GetCampaignTagsResponseSchema, { entries: [] }),
      listNodeEdges: () => create(ListNodeEdgesResponseSchema, {}),
      getCampaignRoster: () => create(GetCampaignRosterResponseSchema, { roster: [] }),
    });
  }, rec.options);
  return { transport, calls: rec.calls };
}

const modes = () =>
  within(screen.getByRole("group", { name: "View mode" }))
    .getAllByRole("button")
    .map((r) => r.textContent);

describe("World wiki", () => {
  it("the GM creates, edits and deletes, and has every view of the wiki", async () => {
    mount(wiki().transport, <KnowledgePanel />);
    expect(await screen.findByText("The Citadel Gate")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New entry" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Edit The Citadel Gate" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete The Citadel Gate" })).toBeInTheDocument();
    expect(modes()).toEqual(["List", "Relationship map", "Checkup", "Boards"]);
  });

  it("a guest with wiki.read reads: no create, edit, delete, checkup or boards — and no cast is asked for", async () => {
    const { transport, calls } = wiki();
    mount(transport, <KnowledgePanel />, [R.WIKI_READ]);
    expect(await screen.findByText("The Citadel Gate")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "New entry" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Edit / })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Delete / })).not.toBeInTheDocument();
    expect(modes()).toEqual(["List", "Relationship map"]);

    // The entry opens to read, with no way into the editor.
    fireEvent.click(screen.getByRole("button", { name: "The Citadel Gate" }));
    const pane = await screen.findByTestId("entry-view");
    expect(within(pane).getByText("A rusted portcullis.")).toBeInTheDocument();
    expect(within(pane).queryByRole("button", { name: "Edit" })).not.toBeInTheDocument();

    await waitFor(() => expect(calls).toContain("ListNodeEdges"));
    expect(calls).not.toContain("GetCampaignRoster");
    // The server refuses a read-only guest the prep boards.
    expect(calls).not.toContain("ListBoards");
  });

  it("a guest with wiki.edit edits entries and boards, but AI drafting and the checkup stay the GM's", async () => {
    const { transport, calls } = wiki();
    mount(transport, <KnowledgePanel />, [R.WIKI_READ, R.WIKI_SECRETS, R.WIKI_EDIT]);
    expect(await screen.findByText("The Citadel Gate")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New entry" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Edit The Citadel Gate" })).toBeInTheDocument();
    expect(modes()).toEqual(["List", "Relationship map", "Boards"]);

    fireEvent.click(screen.getByRole("button", { name: "The Citadel Gate" }));
    expect(within(await screen.findByTestId("entry-view")).getByRole("button", { name: "Edit" })).toBeInTheDocument();
    expect(calls).not.toContain("GetCampaignRoster");
  });
});

// ── Maps ─────────────────────────────────────────────────────────────────────

function maps() {
  const rec = recorder();
  const saltmarsh = { id: "m1", name: "Saltmarsh", widthPx: 1200, heightPx: 800, hasImage: true };
  const line = { id: "n1", nodeType: NodeType.LOCATION, name: "The Snapping Line", body: "A fishing boat." };
  const inn = { id: "n2", nodeType: NodeType.LOCATION, name: "The Wicker Goat", body: "" };
  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      listMaps: () => create(ListMapsResponseSchema, { maps: [saltmarsh] }),
      listNodes: () => create(ListNodesResponseSchema, { nodes: [line, inn] }),
      getMapView: () =>
        create(GetMapViewResponseSchema, {
          map: saltmarsh,
          pins: [{ id: "p1", mapId: "m1", nodeId: "n1", x: 0.25, y: 0.25, nodeName: line.name, nodeType: line.nodeType }],
          unpinned: [inn],
        }),
    });
  }, rec.options);
  return { transport, calls: rec.calls };
}

const pin = () => screen.findByRole("button", { name: /The Snapping Line \(Location\)/ });

describe("Maps", () => {
  it("the GM adds maps, changes them and places pins", async () => {
    mount(maps().transport, <MapsPanel onOpenNode={() => {}} />);
    await pin();
    expect(screen.getByRole("button", { name: "Add map" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Map settings" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "More map actions" })).toBeInTheDocument();
    expect(screen.getByRole("complementary", { name: "Not on this map yet" })).toBeInTheDocument();

    fireEvent.click(await pin());
    expect(await screen.findByRole("button", { name: "Remove from map" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Open entry" })).toBeInTheDocument();
  });

  it("a guest with maps.read looks: no map or pin controls, and the wiki behind the pins is not asked for", async () => {
    const { transport, calls } = maps();
    mount(transport, <MapsPanel onOpenNode={() => {}} />, [R.MAPS_READ]);
    await pin();
    expect(screen.queryByRole("button", { name: "Add map" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Map settings" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "More map actions" })).not.toBeInTheDocument();
    expect(screen.queryByRole("complementary", { name: "Not on this map yet" })).not.toBeInTheDocument();

    // A pin still opens its card — to read, nothing more. Without wiki.read
    // there is no entry to open either.
    fireEvent.click(await pin());
    expect(await screen.findByText("The Snapping Line", { selector: "strong" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Remove from map" })).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Label on this map")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Open entry" })).not.toBeInTheDocument();
    expect(calls).not.toContain("ListNodes");
  });

  it("a guest with maps.edit places and changes pins, but the map itself stays the GM's", async () => {
    mount(maps().transport, <MapsPanel onOpenNode={() => {}} />, [R.MAPS_READ, R.MAPS_EDIT, R.WIKI_READ]);
    await pin();
    expect(screen.getByRole("complementary", { name: "Not on this map yet" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add map" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Map settings" })).not.toBeInTheDocument();
    // AI pin suggestions are generation: the GM's.
    expect(screen.queryByRole("button", { name: /suggest/i })).not.toBeInTheDocument();

    fireEvent.click(await pin());
    expect(await screen.findByRole("button", { name: "Remove from map" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Open entry" })).toBeInTheDocument();
  });
});

// ── Highlights and the session archive ───────────────────────────────────────

const ENDED = create(VoiceSessionSchema, {
  id: "vs1",
  status: "ended",
  startedAt: timestampFromDate(new Date("2026-07-11T19:00:00Z")),
  endedAt: timestampFromDate(new Date("2026-07-11T22:00:00Z")),
  lineCount: 40,
});

function sessions(statuses: ("promoted" | "candidate")[] = ["promoted"], list: VoiceSession[] = [ENDED]) {
  const rec = recorder();
  const transport = createRouterTransport(({ service }) => {
    service(SessionService, {
      listSessions: () => create(ListSessionsResponseSchema, { sessions: list }),
      listHighlights: () =>
        create(ListHighlightsResponseSchema, {
          highlights: statuses.map((status, i) =>
            create(HighlightSchema, {
              id: `h${i}`,
              voiceSessionId: "vs1",
              status,
              startsAt: timestampFromDate(new Date("2026-07-11T20:15:30Z")),
              endsAt: timestampFromDate(new Date("2026-07-11T20:16:10Z")),
              score: 8.5,
              excerpt: "And then the dragon spoke my true name.",
              reason: "Dramatic reveal.",
            }),
          ),
        }),
    });
  }, rec.options);
  return { transport, calls: rec.calls };
}

describe("Session Highlights", () => {
  it("the GM keeps and deletes highlights", async () => {
    mount(sessions(["candidate"]).transport, <HighlightsStrip sessionId="vs1" live={false} />);
    expect(await screen.findByText("And then the dragon spoke my true name.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Keep" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Delete highlight/ })).toBeInTheDocument();
  });

  it("a guest with highlights.read watches and listens: no keep, delete, sound or share", async () => {
    const { container } = mount(
      sessions(["promoted"]).transport,
      <HighlightsStrip sessionId="vs1" live={false} renderActions={() => <button type="button">Share</button>} />,
      [R.HIGHLIGHTS_READ],
    );
    expect(await screen.findByText("And then the dragon spoke my true name.")).toBeInTheDocument();
    expect(container.querySelector("audio")).toBeInTheDocument();
    expect(within(container.querySelector(".gx-highlight") as HTMLElement).queryByRole("button")).not.toBeInTheDocument();
  });
});

describe("Session Highlights with nothing to show", () => {
  it("tells the GM what the recording did", async () => {
    mount(sessions([]).transport, <HighlightsStrip sessionId="vs1" live={false} />);
    expect(await screen.findByText(/Highlight recording is on, but nothing in this session/)).toBeInTheDocument();
  });

  it("tells a guest only what is true for them: none were kept", async () => {
    mount(sessions([]).transport, <HighlightsStrip sessionId="vs1" live={false} />, [R.HIGHLIGHTS_READ]);
    expect(await screen.findByText("No highlights from this session have been kept.")).toBeInTheDocument();
    expect(screen.queryByText(/Highlight recording is on/)).not.toBeInTheDocument();
  });
});

describe("Session archive (a guest's Session screen)", () => {
  const realFetch = globalThis.fetch;
  afterEach(() => {
    globalThis.fetch = realFetch;
  });

  it("with highlights.read: the past session's highlights, no transcript, and nothing of the GM's live desk", async () => {
    const fetchMock = vi.fn();
    globalThis.fetch = fetchMock as unknown as typeof fetch;
    const { transport, calls } = sessions();
    mount(transport, <SessionArchive />, [R.HIGHLIGHTS_READ]);

    expect(await screen.findByText("And then the dragon spoke my true name.")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Sessions", level: 1 })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /start session/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /recap/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: "Session transcript" })).not.toBeInTheDocument();
    // The live session, the cast and the transcript snapshot were never asked for.
    expect(calls).not.toContain("GetSession");
    expect(calls).not.toContain("GetCampaignRoster");
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("with transcripts.read: the transcript and its search, and no highlights are asked for", async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ lines: [] }), { status: 200 }));
    globalThis.fetch = fetchMock as unknown as typeof fetch;
    const { transport, calls } = sessions();
    mount(transport, <SessionArchive />, [R.TRANSCRIPTS_READ]);

    expect(await screen.findByText("This session has no transcript lines.")).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledWith("/api/v1/sessions/vs1", expect.anything());
    expect(screen.getByRole("heading", { name: "Session transcript" })).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: "Highlights" })).not.toBeInTheDocument();
    expect(calls).not.toContain("ListHighlights");
    expect(calls).not.toContain("GetSession");
  });

  // The server withholds a session's end reason from a guest (it is the host's
  // operational detail): the row keeps its status and says nothing more.
  it("lists a failed and a self-ended session whose reason is withheld: the status, no reason line", async () => {
    const at = (day: number, hour: number) => timestampFromDate(new Date(`2026-07-1${day}T${hour}:00:00Z`));
    const failed = create(VoiceSessionSchema, {
      id: "vs-failed",
      status: "failed",
      startedAt: at(2, 19),
      endedAt: at(2, 20),
      lineCount: 12,
    });
    const ended = create(VoiceSessionSchema, {
      id: "vs-ended",
      status: "ended",
      startedAt: at(3, 19),
      endedAt: at(3, 21),
      lineCount: 30,
    });
    mount(sessions([], [failed, ended]).transport, <SessionArchive />, [R.HIGHLIGHTS_READ]);

    fireEvent.click(await screen.findByTestId("session-history-open"));
    const picker = await screen.findByTestId("session-picker");
    expect(within(picker).getByText("Failed")).toBeInTheDocument();
    expect(within(picker).getByText(/12 lines/)).toBeInTheDocument();
    expect(within(picker).getByText(/30 lines/)).toBeInTheDocument();
    expect(picker.querySelectorAll(".gx-session-history__reason")).toHaveLength(0);
    // No reason, so nothing claims the session stopped by itself.
    expect(within(picker).queryByText("Ended by itself")).not.toBeInTheDocument();
  });

  it("says so when the table has not played yet", async () => {
    const transport = createRouterTransport(({ service }) => {
      service(SessionService, { listSessions: () => create(ListSessionsResponseSchema, { sessions: [] }) });
    });
    mount(transport, <SessionArchive />, [R.TRANSCRIPTS_READ, R.HIGHLIGHTS_READ]);
    expect(await screen.findByText("No sessions yet")).toBeInTheDocument();
  });
});
