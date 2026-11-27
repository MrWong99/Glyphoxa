import { describe, it, expect, vi } from "vitest";
import { act, render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { RouterProvider, createMemoryHistory, createRouter } from "@tanstack/react-router";
import { Code, ConnectError, createRouterTransport, type Interceptor } from "@connectrpc/connect";
import { createConnectQueryKey } from "@connectrpc/connect-query";
import { create } from "@bufbuild/protobuf";

import {
  AdminService,
  AdmissionMode,
  GetAccessOverviewResponseSchema,
  AuthService,
  CampaignSchema,
  CampaignService,
  GetActiveCampaignResponseSchema,
  GetCampaignRosterResponseSchema,
  GetCampaignTagsResponseSchema,
  GetCurrentUserResponseSchema,
  GetSessionResponseSchema,
  GuestRight as R,
  GuestWorkspaceSchema,
  ListCampaignsResponseSchema,
  ListKnowledgeProposalsResponseSchema,
  ListMapsResponseSchema,
  ListNodesResponseSchema,
  ListSessionsResponseSchema,
  ListVoicesResponseSchema,
  LogoutResponseSchema,
  NodeSchema,
  NodeType,
  SessionService,
  SwitchWorkspaceResponseSchema,
  UserSchema,
  VoiceService,
  type GuestWorkspace,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { routeTree } from "./router";

vi.mock("sonner", () => ({
  toast: Object.assign(vi.fn(), { error: vi.fn(), success: vi.fn(), info: vi.fn(), warning: vi.fn(), dismiss: vi.fn() }),
  Toaster: () => null,
}));

// Workspaces end to end (ADR-0065): the real route tree and shell against a
// fake server that behaves like the real one where it matters here —
//
//   - the session acts as ONE Workspace (`acting`), SwitchWorkspace changes it,
//     and GetCurrentUser reports it;
//   - while it acts as a Campaign Guest, EVERY procedure outside the guest
//     table is refused with CodePermissionDenied, and the refusal is recorded.
//
// The second point is what makes "the guest shell shows only what the rights
// allow" checkable as a whole: a test that ends with `denied` empty has shown
// that no screen, sidebar, palette or background poll asked for anything a
// guest may not have.

// The procedures a Campaign Guest may call, by the Guest Right that opens them
// ("any": every guest). Mirrors the server's guest table for what the shell
// can reach; anything absent is the GM's alone.
const GUEST_TABLE: Record<string, readonly R[] | "any"> = {
  GetCurrentUser: "any",
  Logout: "any",
  SwitchWorkspace: "any",
  GetAdmissionMode: "any",
  GetActiveCampaign: "any",
  // AdminService ignores the Workspace; its handlers require a Platform Admin
  // themselves (the fake's does too).
  GetAccessOverview: "any",
  ListNodes: [R.WIKI_READ],
  SearchNodes: [R.WIKI_READ],
  ListNodeEdges: [R.WIKI_READ],
  GetKnowledgeGraph: [R.WIKI_READ],
  GetCampaignTags: [R.WIKI_READ],
  // Prep boards are the GM's organization: only a guest who may edit the wiki
  // is shown them, so only they may list them.
  ListBoards: [R.WIKI_EDIT],
  ListMaps: [R.MAPS_READ],
  GetMapView: [R.MAPS_READ],
  ListSessions: [R.TRANSCRIPTS_READ, R.HIGHLIGHTS_READ],
  ListHighlights: [R.HIGHLIGHTS_READ],
  SearchTranscriptLines: [R.TRANSCRIPTS_READ],
  ListPlanningThreads: [R.PLANNING_USE],
  GetPlanningThread: [R.PLANNING_USE],
};

const CITADEL = "c9";
const shared = (rights: R[], over: { id?: string; name?: string } = {}): GuestWorkspace =>
  create(GuestWorkspaceSchema, {
    campaignId: over.id ?? CITADEL,
    campaignName: over.name ?? "The Sunless Citadel",
    tenantName: "Mira's Table",
    rights,
  });

type World = {
  /** The guest Campaign the session acts as; null: the user's own Tenant. */
  acting: string | null;
  hasOwnTenant: boolean;
  shared: GuestWorkspace[];
  platformAdmin?: boolean;
  /** SwitchWorkspace fails this many more times. */
  failSwitch?: number;
  /** Every call, as "<principal>:<Method>". */
  calls: string[];
  /** Calls refused because a guest made them. */
  denied: string[];
  switches: string[];
};

function makeWorld(init: Partial<World> = {}): World {
  return { acting: null, hasOwnTenant: true, shared: [], calls: [], denied: [], switches: [], ...init };
}

function testTransport(world: World) {
  const mine = create(CampaignSchema, { id: "c1", name: "Rin's Homebrew", system: "5e", language: "en" });
  const acting = () => world.shared.find((w) => w.campaignId === world.acting);

  // The guest gate: default-deny, per procedure, before any handler runs.
  const gate: Interceptor = (next) => async (req) => {
    const guest = acting();
    world.calls.push(`${guest ? "guest" : "own"}:${req.method.name}`);
    if (world.acting !== null) {
      const need = GUEST_TABLE[req.method.name];
      const ok = guest !== undefined && (need === "any" || need?.some((r) => guest.rights.includes(r)));
      // A session whose guest row is gone is refused everything but the probe.
      if (!ok && !(guest === undefined && req.method.name === "GetCurrentUser")) {
        world.denied.push(req.method.name);
        throw new ConnectError("not allowed for a campaign guest", Code.PermissionDenied);
      }
    }
    return next(req);
  };

  return createRouterTransport(
    ({ service }) => {
      service(AuthService, {
        getCurrentUser: () => {
          const guest = acting();
          const hasWorkspace = guest !== undefined || world.hasOwnTenant;
          return create(GetCurrentUserResponseSchema, {
            user: create(UserSchema, { id: "u-rin", name: "Rin", role: "operator", avatar: "" }),
            tenantId: guest ? "t-mira" : world.hasOwnTenant ? "t-rin" : "",
            tenantName: guest ? guest.tenantName : hasWorkspace ? "Rin's Table" : "",
            isPlatformAdmin: world.platformAdmin ?? false,
            hasOwnTenant: world.hasOwnTenant,
            guest,
            guestWorkspaces: world.shared,
          });
        },
        switchWorkspace: (req) => {
          world.switches.push(req.campaignId);
          if ((world.failSwitch ?? 0) > 0) {
            world.failSwitch = (world.failSwitch ?? 0) - 1;
            throw new ConnectError("the session store is down", Code.Unavailable);
          }
          world.acting = req.campaignId === "" ? null : req.campaignId;
          return create(SwitchWorkspaceResponseSchema, {});
        },
        logout: () => create(LogoutResponseSchema, {}),
      });
      service(AdminService, {
        getAccessOverview: () => {
          if (!world.platformAdmin) throw new ConnectError("platform admins only", Code.PermissionDenied);
          return create(GetAccessOverviewResponseSchema, { admissionMode: AdmissionMode.ALLOWLIST });
        },
      });
      service(VoiceService, { listVoices: () => create(ListVoicesResponseSchema, { voices: [] }) });
      service(SessionService, {
        getSession: () => create(GetSessionResponseSchema, { active: false }),
        listSessions: () => create(ListSessionsResponseSchema, { sessions: [] }),
      });
      service(CampaignService, {
        getActiveCampaign: () => create(GetActiveCampaignResponseSchema, { campaign: mine }),
        listCampaigns: () => create(ListCampaignsResponseSchema, { campaigns: [mine] }),
        listKnowledgeProposals: () => create(ListKnowledgeProposalsResponseSchema, { proposals: [] }),
        getCampaignRoster: () => create(GetCampaignRosterResponseSchema, { campaign: mine, roster: [] }),
        // Whose wiki it is shows in the entry: the two Workspaces answer the
        // SAME request differently, which is why nothing cached may survive.
        listNodes: () =>
          create(ListNodesResponseSchema, {
            nodes: [
              create(NodeSchema, {
                id: "n1",
                nodeType: NodeType.LOCATION,
                name: world.acting ? "The Citadel Gate" : "Rin's Tavern",
              }),
            ],
          }),
        getCampaignTags: () => create(GetCampaignTagsResponseSchema, { entries: [] }),
        listMaps: () => create(ListMapsResponseSchema, { maps: [] }),
      });
    },
    { transport: { interceptors: [gate] } },
  );
}

function renderAt(path: string, init: Partial<World> = {}) {
  const world = makeWorld(init);
  const queryClient = makeQueryClient();
  const router = createRouter({ routeTree, history: createMemoryHistory({ initialEntries: [path] }) });
  render(
    <Providers transport={testTransport(world)} queryClient={queryClient}>
      <RouterProvider router={router} />
    </Providers>,
  );
  const path_ = () => router.state.location.pathname;
  /** What the server would report now: re-run the session probe (a focus, the guest poll). */
  const reprobe = () =>
    act(async () => {
      await queryClient.invalidateQueries({
        queryKey: createConnectQueryKey({ schema: AuthService.method.getCurrentUser, cardinality: "finite" }),
      });
    });
  /** The procedures something in the cache was fetched with. */
  // (An entry with nothing in it is only an observer that has not asked — a
  // read the shell keeps switched off for this principal.)
  const cached = () =>
    queryClient
      .getQueryCache()
      .findAll()
      .filter((q) => q.state.data !== undefined || q.state.fetchStatus === "fetching")
      .map((q) => (q.queryKey[1] as { methodName?: string } | undefined)?.methodName ?? "");
  return { world, router, queryClient, path: path_, reprobe, cached };
}

const sidebarLinks = () =>
  within(screen.getByRole("navigation", { name: "Main navigation" }))
    .queryAllByRole("link")
    .map((a) => a.textContent);

const guestTrigger = () => screen.findByTestId("workspace-switcher-trigger");

describe("switching to a Campaign shared with me", () => {
  it("lists it under 'Shared with me', switches the session, drops the cache and lands on the first shared view", async () => {
    const { world, path, cached } = renderAt("/t/default/campaign/knowledge", {
      shared: [shared([R.MAPS_READ, R.WIKI_READ])],
    });
    // The GM's own wiki, with the GM's reads in the cache.
    expect(await screen.findByText("Rin's Tavern")).toBeInTheDocument();
    await waitFor(() => expect(cached()).toContain("GetCampaignRoster"));

    fireEvent.click(screen.getByTestId("campaign-switcher-trigger"));
    const group = await screen.findByRole("group", { name: "Shared with me" });
    // "campaign · host workspace".
    const entry = within(group).getByRole("button", { name: /The Sunless Citadel.*Mira's Table/ });
    fireEvent.click(entry);

    await waitFor(() => expect(world.switches).toEqual([CITADEL]));
    await waitFor(() => expect(path()).toBe("/t/guest-c9/campaign/knowledge"));
    // The host's wiki — fetched fresh, as the guest.
    expect(await screen.findByText("The Citadel Gate")).toBeInTheDocument();
    expect(screen.queryByText("Rin's Tavern")).not.toBeInTheDocument();

    // Nothing of the previous principal is left in the cache…
    for (const gmOnly of ["GetCampaignRoster", "ListCampaigns", "GetSession", "ListKnowledgeProposals"]) {
      expect(cached()).not.toContain(gmOnly);
    }
    // …and nothing the guest may not have was asked for.
    expect(world.denied).toEqual([]);
  });

  it("shows who the user is a guest of, on every screen", async () => {
    renderAt("/t/guest-c9/campaign/maps", { acting: CITADEL, shared: [shared([R.MAPS_READ, R.WIKI_READ])] });
    const trigger = await guestTrigger();
    expect(trigger).toHaveAccessibleName(/^Guest of Mira's Table · The Sunless Citadel/);
    expect(within(trigger).getByText("Guest of Mira's Table")).toBeVisible();
    expect(within(trigger).getByText("The Sunless Citadel")).toBeVisible();
    // The account footer calls them a guest, not this table's GM.
    expect(screen.getByRole("button", { name: /account/i })).toHaveTextContent("Guest");
  });

  it("offers the way back to 'My workspace' and switches the session back to the own Tenant", async () => {
    const { world, path } = renderAt("/t/guest-c9/campaign/knowledge", {
      acting: CITADEL,
      shared: [shared([R.WIKI_READ])],
    });
    expect(await screen.findByText("The Citadel Gate")).toBeInTheDocument();

    fireEvent.click(await guestTrigger());
    fireEvent.click(await screen.findByRole("button", { name: /My workspace/ }));

    // An empty campaign id is "my own Tenant".
    await waitFor(() => expect(world.switches).toEqual([""]));
    await waitFor(() => expect(path()).toBe("/t/default/campaign/overview"));
    await waitFor(() => expect(sidebarLinks()).toContain("Cast"));
    expect(screen.queryByTestId("workspace-switcher-trigger")).not.toBeInTheDocument();
  });

  it("does not offer 'My workspace' to someone who has no Tenant of their own", async () => {
    renderAt("/t/guest-c9/campaign/knowledge", {
      acting: CITADEL,
      hasOwnTenant: false,
      shared: [shared([R.WIKI_READ]), shared([R.MAPS_READ], { id: "c7", name: "Saltmarsh" })],
    });
    fireEvent.click(await guestTrigger());
    const group = await screen.findByRole("group", { name: "Shared with me" });
    expect(within(group).getByRole("button", { name: /The Sunless Citadel/ })).toHaveAttribute("aria-current", "true");
    expect(within(group).getByRole("button", { name: /Saltmarsh/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /My workspace/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Set up my table/ })).not.toBeInTheDocument();
  });

  // A Platform Admin who first signed in through a campaign link has a guest
  // session and no table yet; an ordinary sign-in is what founds it.
  it("offers a Platform Admin with no table yet 'Set up my table', a full page load to the sign-in", async () => {
    const { world } = renderAt("/t/guest-c9/campaign/knowledge", {
      acting: CITADEL,
      hasOwnTenant: false,
      platformAdmin: true,
      shared: [shared([R.WIKI_READ])],
    });
    fireEvent.click(await guestTrigger());
    const setUp = await screen.findByRole("link", { name: /Set up my table/ });
    expect(setUp).toHaveAttribute("href", "/login");
    expect(setUp).toHaveTextContent("Sign in again to open a table of your own");
    // Not the switch: the server would refuse it ("no workspace of your own").
    expect(screen.queryByRole("button", { name: /My workspace/ })).not.toBeInTheDocument();
    expect(world.switches).toEqual([]);
  });

  it("offers a Platform Admin who has a table 'My workspace', not 'Set up my table'", async () => {
    renderAt("/t/guest-c9/campaign/knowledge", {
      acting: CITADEL,
      platformAdmin: true,
      shared: [shared([R.WIKI_READ])],
    });
    fireEvent.click(await guestTrigger());
    expect(await screen.findByRole("button", { name: /My workspace/ })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /Set up my table/ })).not.toBeInTheDocument();
  });

  it("says so when the switch fails, and tries again on request", async () => {
    const { world, path } = renderAt("/t/guest-c9/campaign/knowledge", {
      shared: [shared([R.WIKI_READ])],
      failSwitch: 1,
    });
    expect(await screen.findByText("Couldn't open that workspace")).toBeInTheDocument();
    expect(screen.getByText(/the session store is down/)).toBeInTheDocument();
    // Still the own Workspace: nothing was mounted for the other one.
    expect(world.acting).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("The Citadel Gate")).toBeInTheDocument();
    expect(world.switches).toEqual([CITADEL, CITADEL]);
    expect(path()).toBe("/t/guest-c9/campaign/knowledge");
  });
});

describe("the guest shell shows only what the rights allow", () => {
  it.each([
    ["wiki.read", [R.WIKI_READ], ["World wiki"], "/t/guest-c9/campaign/knowledge"],
    ["maps.read", [R.MAPS_READ], ["Maps"], "/t/guest-c9/campaign/maps"],
    ["maps.edit", [R.MAPS_READ, R.MAPS_EDIT], ["Maps"], "/t/guest-c9/campaign/maps"],
    ["transcripts.read", [R.TRANSCRIPTS_READ], ["Sessions"], "/t/guest-c9/session"],
    ["highlights.read", [R.HIGHLIGHTS_READ], ["Sessions"], "/t/guest-c9/session"],
    [
      "planning.use",
      [R.WIKI_READ, R.WIKI_SECRETS, R.PLANNING_USE],
      ["World wiki", "Planning"],
      "/t/guest-c9/campaign/knowledge",
    ],
    [
      "the Reader preset",
      [R.WIKI_READ, R.MAPS_READ, R.TRANSCRIPTS_READ, R.HIGHLIGHTS_READ],
      ["World wiki", "Maps", "Sessions"],
      "/t/guest-c9/campaign/knowledge",
    ],
  ] as const)("%s: exactly its destinations, and its front door", async (_name, rights, links, landing) => {
    const { world, path } = renderAt("/t/guest-c9", { acting: CITADEL, shared: [shared([...rights])] });
    await guestTrigger();
    await waitFor(() => expect(path()).toBe(landing));
    expect(sidebarLinks()).toEqual(links);
    await waitFor(() => expect(world.calls.length).toBeGreaterThan(1));
    expect(world.denied).toEqual([]);
  });

  it("offers a guest none of the GM's chrome: no Setup, Campaigns, Admin, session marker or create CTA", async () => {
    const { world } = renderAt("/t/guest-c9/campaign/knowledge", {
      acting: CITADEL,
      shared: [shared([R.WIKI_READ, R.WIKI_SECRETS, R.WIKI_EDIT, R.MAPS_READ, R.MAPS_EDIT, R.PLANNING_USE])],
    });
    expect(await screen.findByText("The Citadel Gate")).toBeInTheDocument();
    for (const gone of ["Overview", "Cast", "Players", "Suggestions", "Sharing", "Settings", "Campaigns", "Setup", "Admin", "Session"]) {
      expect(sidebarLinks()).not.toContain(gone);
    }
    expect(screen.queryByTestId("campaign-switcher-trigger")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /new campaign/i })).not.toBeInTheDocument();
    expect(world.denied).toEqual([]);
  });

  // Admin is the deployment's, not a Workspace's: a Platform Admin whose session
  // is a guest one must not have to sign out to administer the instance.
  it("keeps Admin for a Platform Admin acting as a guest, and the screen opens in the guest shell", async () => {
    const { world, path } = renderAt("/t/guest-c9/campaign/knowledge", {
      acting: CITADEL,
      platformAdmin: true,
      shared: [shared([R.WIKI_READ])],
    });
    expect(await screen.findByText("The Citadel Gate")).toBeInTheDocument();
    expect(sidebarLinks()).toEqual(["World wiki", "Admin"]);
    for (const gone of ["Campaigns", "Setup", "Session", "Cast"]) expect(sidebarLinks()).not.toContain(gone);

    fireEvent.click(within(screen.getByRole("navigation", { name: "Main navigation" })).getByRole("link", { name: "Admin" }));
    await waitFor(() => expect(path()).toBe("/t/guest-c9/admin"));
    expect(await screen.findByRole("heading", { name: "Add a person" })).toBeInTheDocument();
    expect(world.calls).toContain("guest:GetAccessOverview");
    // Still the guest shell: the session did not move.
    expect(screen.getByTestId("workspace-switcher-trigger")).toBeInTheDocument();
    expect(world.switches).toEqual([]);
    expect(world.denied).toEqual([]);
  });

  it("a Platform Admin acting as a guest still gets the not-found page on Setup and Campaigns", async () => {
    const { world } = renderAt("/t/guest-c9/configuration", {
      acting: CITADEL,
      platformAdmin: true,
      shared: [shared([R.WIKI_READ])],
    });
    expect(await screen.findByRole("heading", { name: "This isn't shared with you" })).toBeInTheDocument();
    const offered = screen.getByRole("navigation", { name: "Go somewhere that exists" });
    expect(within(offered).getAllByRole("link").map((a) => a.textContent)).toEqual(["World wiki", "Admin"]);
    expect(world.denied).toEqual([]);
  });

  it("words the campaign header for a guest, not for the GM whose world it is", async () => {
    renderAt("/t/guest-c9/campaign/knowledge", { acting: CITADEL, shared: [shared([R.WIKI_READ])] });
    expect(await screen.findByText("The Citadel Gate")).toBeInTheDocument();
    expect(
      screen.getByText("What the world knows — the entries this campaign's GM shares with you."),
    ).toBeInTheDocument();
    expect(screen.queryByText(/feed your NPCs/)).not.toBeInTheDocument();
  });

  it("a guest who may only read the wiki never asks for the prep boards", async () => {
    const { world } = renderAt("/t/guest-c9/campaign/knowledge", { acting: CITADEL, shared: [shared([R.WIKI_READ])] });
    expect(await screen.findByText("The Citadel Gate")).toBeInTheDocument();
    await waitFor(() => expect(world.calls).toContain("guest:GetCampaignTags"));
    expect(world.calls).not.toContain("guest:ListBoards");
    expect(world.denied).toEqual([]);
  });

  it("limits the command palette to the same destinations", async () => {
    const { world } = renderAt("/t/guest-c9/campaign/maps", {
      acting: CITADEL,
      shared: [shared([R.MAPS_READ]), shared([R.WIKI_READ], { id: "c7", name: "Saltmarsh" })],
    });
    await guestTrigger();
    fireEvent.click(screen.getByRole("button", { name: /search/i }));
    const palette = within(await screen.findByTestId("command-palette"));
    expect(within(palette.getByRole("group", { name: "Go to" })).getAllByRole("option").map((o) => o.textContent)).toEqual([
      "Maps",
    ]);
    // The other Workspaces are one step away — the user's own, and the other
    // shared campaign (never the one already open). The user's own campaigns
    // and "New campaign" are not here.
    expect(
      within(palette.getByRole("group", { name: "Workspaces" })).getAllByRole("option").map((o) => o.textContent),
    ).toEqual(["My workspace", "SaltmarshMira's Table"]);
    expect(palette.queryByRole("option", { name: "New campaign" })).not.toBeInTheDocument();
    expect(world.denied).toEqual([]);
  });

  it.each([
    ["a Campaign view that is not shared", "/t/guest-c9/campaign/cast"],
    ["the Sharing view", "/t/guest-c9/campaign/sharing"],
    ["Setup", "/t/guest-c9/configuration"],
    ["Campaigns", "/t/guest-c9/campaigns"],
    ["Admin, for someone who is no Platform Admin", "/t/guest-c9/admin"],
    ["Session, without a session right", "/t/guest-c9/session"],
  ])("a deep link to %s is not mounted: it says what IS shared", async (_name, url) => {
    const { world, path } = renderAt(url, { acting: CITADEL, shared: [shared([R.WIKI_READ])] });
    expect(await screen.findByRole("heading", { name: "This isn't shared with you" })).toBeInTheDocument();
    const offered = screen.getByRole("navigation", { name: "Go somewhere that exists" });
    expect(within(offered).getAllByRole("link").map((a) => a.textContent)).toEqual(["World wiki"]);
    expect(path()).toBe(url);
    // The screen behind the link never ran: nothing was refused.
    await waitFor(() => expect(world.calls.length).toBeGreaterThan(0));
    expect(world.denied).toEqual([]);
  });

  it("tells a guest with no rights at all exactly that", async () => {
    const { world } = renderAt("/t/guest-c9", { acting: CITADEL, shared: [shared([])] });
    expect(await screen.findByRole("heading", { name: "Nothing is shared with you yet" })).toBeInTheDocument();
    expect(screen.getByText(/guest of The Sunless Citadel at Mira's Table/)).toBeInTheDocument();
    expect(sidebarLinks()).toEqual([]);
    expect(world.denied).toEqual([]);
  });
});

describe("the tenant slug names the Workspace", () => {
  it("rewrites an own-Workspace URL for a guest-only account instead of switching", async () => {
    const { world, path } = renderAt("/t/default/campaign/maps", {
      acting: CITADEL,
      hasOwnTenant: false,
      shared: [shared([R.MAPS_READ])],
    });
    await waitFor(() => expect(path()).toBe("/t/guest-c9/campaign/maps"));
    await guestTrigger();
    expect(world.switches).toEqual([]);
    expect(world.denied).toEqual([]);
  });

  it("makes the session follow a bookmark into a shared Campaign", async () => {
    const { world, path } = renderAt("/t/guest-c9/campaign/knowledge", { shared: [shared([R.WIKI_READ])] });
    expect(await screen.findByText("The Citadel Gate")).toBeInTheDocument();
    expect(world.switches).toEqual([CITADEL]);
    expect(path()).toBe("/t/guest-c9/campaign/knowledge");
    // Not one call was made for the bookmark's Workspace as the previous principal.
    expect(world.calls.filter((c) => c === "own:ListNodes")).toEqual([]);
  });

  it("makes the session follow an own-Workspace bookmark back out of a guest Campaign", async () => {
    const { world } = renderAt("/t/default/campaign/knowledge", { acting: CITADEL, shared: [shared([R.WIKI_READ])] });
    expect(await screen.findByText("Rin's Tavern")).toBeInTheDocument();
    expect(world.switches).toEqual([""]);
    expect(world.denied).toEqual([]);
  });

  it("says a Campaign is not shared when the URL names one this user is no guest of", async () => {
    const { world, path } = renderAt("/t/guest-nope/campaign/knowledge", { shared: [shared([R.WIKI_READ])] });
    expect(await screen.findByRole("heading", { name: "This campaign isn't shared with you" })).toBeInTheDocument();
    expect(world.switches).toEqual([]);
    // Every way out: the own Workspace, what IS shared, and signing out.
    const exits = screen.getByRole("list", { name: "Other workspaces" });
    expect(within(exits).getByRole("button", { name: "The Sunless Citadel · Mira's Table" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Log out" })).toBeInTheDocument();

    fireEvent.click(within(exits).getByRole("button", { name: "My workspace" }));
    await waitFor(() => expect(path()).toBe("/t/default/campaign/overview"));
  });

  it("offers a Platform Admin with no table yet 'Set up my table' among the ways out", async () => {
    renderAt("/t/guest-nope/campaign/knowledge", {
      acting: CITADEL,
      hasOwnTenant: false,
      platformAdmin: true,
      shared: [shared([R.WIKI_READ])],
    });
    expect(await screen.findByRole("heading", { name: "This campaign isn't shared with you" })).toBeInTheDocument();
    const exits = screen.getByRole("list", { name: "Other workspaces" });
    expect(within(exits).getByRole("link", { name: "Set up my table" })).toHaveAttribute("href", "/login");
    expect(within(exits).queryByRole("button", { name: "My workspace" })).not.toBeInTheDocument();
  });
});

describe("a second tab in another Workspace", () => {
  it("takes the session back when this tab is used again, and never shows the other Workspace's data", async () => {
    const { world, reprobe, path, queryClient } = renderAt("/t/default/campaign/knowledge", {
      shared: [shared([R.WIKI_READ])],
    });
    expect(await screen.findByText("Rin's Tavern")).toBeInTheDocument();

    // Another tab switched this browser's session to the shared Campaign, and
    // a background refetch in THIS tab was answered for it.
    world.acting = CITADEL;
    await act(async () => {
      await queryClient.invalidateQueries({
        queryKey: createConnectQueryKey({ schema: CampaignService.method.listNodes, cardinality: "finite" }),
      });
    });
    await reprobe();

    // This tab is still about the own Workspace: the session follows it back,
    // the URL stays, and the guest shell never appears here.
    await waitFor(() => expect(world.switches).toEqual([""]));
    expect(path()).toBe("/t/default/campaign/knowledge");
    expect(await screen.findByText("Rin's Tavern")).toBeInTheDocument();
    // What was answered for the other Workspace is gone with the cache.
    expect(screen.queryByText("The Citadel Gate")).not.toBeInTheDocument();
    expect(screen.queryByTestId("workspace-switcher-trigger")).not.toBeInTheDocument();
    expect(world.acting).toBeNull();
  });
});

describe("the planning chat as a guest", () => {
  it("asks for the guest's own threads and not for the host table's provider configuration", async () => {
    const { world } = renderAt("/t/guest-c9/campaign/planning", {
      acting: CITADEL,
      shared: [shared([R.WIKI_READ, R.WIKI_SECRETS, R.PLANNING_USE])],
    });
    await guestTrigger();
    await waitFor(() => expect(world.calls).toContain("guest:ListPlanningThreads"));
    expect(world.calls).not.toContain("guest:ListProviderConfigs");
    expect(world.calls).not.toContain("guest:GetCampaignRoster");
    expect(world.denied).toEqual([]);
  });

  it("words the header for a guest: this campaign's Butler, not 'your Butler'", async () => {
    renderAt("/t/guest-c9/campaign/planning", {
      acting: CITADEL,
      shared: [shared([R.WIKI_READ, R.WIKI_SECRETS, R.PLANNING_USE])],
    });
    expect(
      await screen.findByText("Plan with this campaign's Butler. Your conversations stay saved with the campaign."),
    ).toBeInTheDocument();
    expect(screen.queryByText(/your Butler/)).not.toBeInTheDocument();
  });
});

describe("access that changes under a guest", () => {
  it("removed, with a Tenant of their own to fall back to: says the Campaign is gone and offers the way home", async () => {
    const { world, reprobe } = renderAt("/t/guest-c9/campaign/knowledge", {
      acting: CITADEL,
      shared: [shared([R.WIKI_READ])],
    });
    expect(await screen.findByText("The Citadel Gate")).toBeInTheDocument();

    // The GM removes them: the stale selection stops resolving.
    world.shared = [];
    world.acting = null;
    await reprobe();

    expect(
      await screen.findByRole("heading", { name: "The Sunless Citadel is no longer shared with you" }),
    ).toBeInTheDocument();
    // Not a shell full of someone else's — or their own — data under a guest banner.
    expect(screen.queryByText("The Citadel Gate")).not.toBeInTheDocument();
    expect(screen.queryByText("Rin's Tavern")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "My workspace" })).toBeInTheDocument();
    // Nothing switched the session on its own.
    expect(world.switches).toEqual([]);
  });

  it("removed, with nowhere else to be: a refused call re-reads the session and the shell gives way to a clear page", async () => {
    const { world, queryClient } = renderAt("/t/guest-c9/campaign/knowledge", {
      acting: CITADEL,
      hasOwnTenant: false,
      shared: [shared([R.WIKI_READ])],
    });
    expect(await screen.findByText("The Citadel Gate")).toBeInTheDocument();

    // The guest row is gone; the session still points at the Campaign, so the
    // server refuses everything.
    world.shared = [];
    // Whatever the guest does next is refused — here, a refetch of the wiki.
    await act(async () => {
      await queryClient.invalidateQueries({
        queryKey: createConnectQueryKey({ schema: CampaignService.method.listNodes, cardinality: "finite" }),
      });
    });
    expect(world.denied).toContain("ListNodes");

    // No reload, no manual retry: the refusal itself led here.
    expect(await screen.findByRole("heading", { name: "There is nothing here for you right now" })).toBeInTheDocument();
    expect(screen.queryByText("The Citadel Gate")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Log out" })).toBeInTheDocument();
  });

  it("rights reduced while a view is open: the view gives way and the sidebar follows", async () => {
    const { world, reprobe } = renderAt("/t/guest-c9/campaign/maps", {
      acting: CITADEL,
      shared: [shared([R.WIKI_READ, R.MAPS_READ])],
    });
    await guestTrigger();
    await waitFor(() => expect(sidebarLinks()).toEqual(["World wiki", "Maps"]));

    world.shared = [shared([R.WIKI_READ])];
    await reprobe();

    expect(await screen.findByRole("heading", { name: "This isn't shared with you" })).toBeInTheDocument();
    expect(sidebarLinks()).toEqual(["World wiki"]);
  });

  it("an account with no Tenant and nothing shared gets a clear page, not a broken shell", async () => {
    const { world } = renderAt("/t/default", { hasOwnTenant: false });
    expect(await screen.findByRole("heading", { name: "There is nothing here for you right now" })).toBeInTheDocument();
    expect(screen.getByText(/Ask the GM who invited you/)).toBeInTheDocument();
    expect(screen.queryByRole("navigation", { name: "Main navigation" })).not.toBeInTheDocument();
    // Only the probe ran.
    expect(new Set(world.calls)).toEqual(new Set(["own:GetCurrentUser"]));
  });
});
