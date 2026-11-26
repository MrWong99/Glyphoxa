import { describe, it, expect } from "vitest";
import { act, render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { RouterProvider, createBrowserHistory, createRouter, createMemoryHistory } from "@tanstack/react-router";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  AuthService,
  CampaignService,
  SessionService,
  VoiceService,
  AdmissionMode,
  AgentSchema,
  CampaignSchema,
  GetActiveCampaignResponseSchema,
  GetAdmissionModeResponseSchema,
  GetCampaignRosterResponseSchema,
  GetCampaignTagsResponseSchema,
  GetCurrentUserResponseSchema,
  GetSessionResponseSchema,
  ListCampaignsResponseSchema,
  ListKnowledgeProposalsResponseSchema,
  ListNodesResponseSchema,
  ListVoicesResponseSchema,
  NodeSchema,
  NodeType,
  UserSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { routeTree } from "./router";

// Pins the Go↔TS URL contracts owned by internal/auth/oauth.go:
//   - /login?error=not_authorized (notAuthorizedRedirect): the admission
//     denial signal — an allowlist miss (ADR-0041) or an open-mode suspension
//     (ADR-0055) — the login route must surface as the banner.
//   - /onboarding/create-tenant (onboardingRedirect, ADR-0055): where the OAuth
//     callback 302s a fresh open-mode signup; the route must exist top-level and
//     render the name-your-Tenant screen.
// The component tests cover the screens' props/behaviour; these cover the
// URL→screen wiring, so a drift on either side of a contract fails a test.

// A minimal backend for the routes under test: the login screen probes
// GetAdmissionMode (allowlist keeps today's framing), the onboarding screen and
// the AuthGate probe GetCurrentUser (answered with the pre-provisioned Tenant
// name), and the campaign routes read one campaign — a Butler, one NPC and that
// NPC's wiki entry — so a deep link has something real to land on.
function testTransport() {
  const campaign = create(CampaignSchema, { id: "c1", name: "The Sunless Citadel", system: "5e", language: "en" });
  return createRouterTransport(({ service }) => {
    service(AuthService, {
      getAdmissionMode: () =>
        create(GetAdmissionModeResponseSchema, { admissionMode: AdmissionMode.ALLOWLIST }),
      getCurrentUser: () =>
        create(GetCurrentUserResponseSchema, {
          user: create(UserSchema, { name: "Rin", role: "operator", avatar: "" }),
          tenantId: "5b3f7c1e-0000-0000-0000-000000000000",
          tenantName: "Rin's Table",
        }),
    });
    service(VoiceService, { listVoices: () => create(ListVoicesResponseSchema, { voices: [] }) });
    service(SessionService, { getSession: () => create(GetSessionResponseSchema, { active: false }) });
    service(CampaignService, {
      getActiveCampaign: () => create(GetActiveCampaignResponseSchema, { campaign }),
      listCampaigns: () => create(ListCampaignsResponseSchema, { campaigns: [campaign] }),
      listKnowledgeProposals: () => create(ListKnowledgeProposalsResponseSchema, { proposals: [] }),
      getCampaignRoster: () =>
        create(GetCampaignRosterResponseSchema, {
          campaign,
          roster: [
            create(AgentSchema, { id: "butler-1", campaignId: "c1", role: "butler", name: "Glyphoxa", addressOnly: true }),
            create(AgentSchema, { id: "npc-1", campaignId: "c1", role: "character", name: "Bart", persona: "Grumbles." }),
          ],
        }),
      listNodes: () =>
        create(ListNodesResponseSchema, {
          nodes: [create(NodeSchema, { id: "n-bart", nodeType: NodeType.NPC, name: "Bart of the Tankard", body: "" })],
        }),
      getCampaignTags: () => create(GetCampaignTagsResponseSchema, { entries: [] }),
    });
  });
}

function renderAt(path: string) {
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  render(
    <Providers transport={testTransport()} queryClient={makeQueryClient()}>
      <RouterProvider router={router} />
    </Providers>,
  );
  return router;
}

describe("loginRoute ?error=not_authorized contract", () => {
  it("renders the not-authorized banner for /login?error=not_authorized", async () => {
    renderAt("/login?error=not_authorized");
    expect(await screen.findByRole("alert")).toHaveTextContent(
      /isn't authorized here/i,
    );
  });

  it("renders no banner on a plain /login visit", async () => {
    renderAt("/login");
    await screen.findByRole("link", { name: /continue with discord/i });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("renders no banner for an unrecognized error value", async () => {
    renderAt("/login?error=server_flu");
    await screen.findByRole("link", { name: /continue with discord/i });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});

describe("onboardingRoute /onboarding/create-tenant contract", () => {
  it("renders the name-your-Tenant screen at /onboarding/create-tenant", async () => {
    renderAt("/onboarding/create-tenant");
    expect(await screen.findByLabelText(/name your table/i)).toHaveValue("Rin's Table");
  });
});

// Where the app opens (ADR-0063 amendment): a signed-in GM lands on the home of
// their campaign, not on API keys.
describe("campaign-first landing", () => {
  it("opens the tenant index on the campaign's Overview", async () => {
    const router = renderAt("/t/acme/");
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/t/acme/campaign/overview"),
    );
    // The Overview itself renders — campaign header and a tile per view.
    expect(await screen.findByRole("heading", { level: 1, name: "The Sunless Citadel" })).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Sections of this campaign" })).toBeInTheDocument();
  });

  it("the redirect replaces the index entry, so Back does not bounce", async () => {
    const router = renderAt("/t/acme");
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/t/acme/campaign/overview"),
    );
    expect(router.history.length).toBe(1);
  });
});

// The Campaign view lives in the path (ADR-0063): /campaign/:view. A bare
// /campaign visit — and any pre-ADR-0063 ?view=/?node= deep link (#591) — must
// redirect onto the path so old bookmarks and palette links keep working.
describe("campaign view path (ADR-0063)", () => {
  it("redirects a bare /campaign to the Overview", async () => {
    const router = renderAt("/t/acme/campaign");
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/t/acme/campaign/overview"),
    );
  });

  it("redirects an unknown view to the Overview instead of a dead end", async () => {
    const router = renderAt("/t/acme/campaign/npcs");
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/t/acme/campaign/overview"),
    );
    expect(screen.queryByRole("heading", { name: "Not found" })).not.toBeInTheDocument();
  });

  it.each(["overview", "cast", "knowledge", "maps", "players", "proposals", "planning", "settings"])(
    "serves /campaign/%s as itself",
    async (view) => {
      const router = renderAt(`/t/acme/campaign/${view}`);
      expect(await screen.findByRole("heading", { level: 1, name: "The Sunless Citadel" })).toBeInTheDocument();
      expect(router.state.location.pathname).toBe(`/t/acme/campaign/${view}`);
    },
  );

  it("maps a legacy ?view= deep link onto the path", async () => {
    const router = renderAt("/t/acme/campaign?view=players");
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/t/acme/campaign/players"),
    );
    expect(router.state.location.search).toEqual({});
  });

  it("an unknown ?view= value falls back to the Overview", async () => {
    const router = renderAt("/t/acme/campaign?view=nonsense");
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/t/acme/campaign/overview"),
    );
  });

  it("a hand-edited ?node= on another view redirects to knowledge", async () => {
    // ?node= only means something on the knowledge view; subviewRoute's
    // beforeLoad owns the correction. The param rides along: on the wiki it IS
    // the open entry (the panel's controlled selection), so it stays.
    const router = renderAt("/t/acme/campaign/cast?node=n-bart");
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/t/acme/campaign/knowledge"),
    );
    expect(router.state.location.search).toEqual({ node: "n-bart" });
  });

  it("a legacy ?node= link lands on knowledge and opens that entry's page, keeping the param", async () => {
    const router = renderAt("/t/acme/campaign?node=n-bart");
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/t/acme/campaign/knowledge"),
    );
    // The redirect carries ?node= along and the wiki opens that entry's read
    // page from it. The URL keeps naming the entry, so the link is shareable
    // and Back steps through the entries the GM followed. This is the whole
    // chain through the real route layer.
    const page = await screen.findByTestId("entry-view");
    expect(within(page).getByRole("heading", { level: 2, name: "Bart of the Tankard" })).toBeInTheDocument();
    expect(router.state.location.search).toEqual({ node: "n-bart" });
  });

  // The wiki registers ?node= as guarded selection state: browser Back that
  // would replace an editor holding unsaved typing asks first, through the
  // real route layer, and Cancel keeps both the entry and the typing. Back is a
  // real popstate here (browser history): TanStack's memory history consults
  // blockers only on push/replace, so it cannot stand in for the Back button.
  it("browser Back away from an entry with unsaved edits asks first", async () => {
    window.history.replaceState(null, "", "/t/acme/campaign/knowledge");
    const router = createRouter({ routeTree, history: createBrowserHistory() });
    try {
      render(
        <Providers transport={testTransport()} queryClient={makeQueryClient()}>
          <RouterProvider router={router} />
        </Providers>,
      );
      fireEvent.click(await screen.findByRole("button", { name: "Bart of the Tankard" }));
      await waitFor(() => expect(router.state.location.search).toEqual({ node: "n-bart" }));
      const page = await screen.findByTestId("entry-view");
      fireEvent.click(within(page).getByRole("button", { name: "Edit" }));
      fireEvent.change(await screen.findByLabelText("Name"), { target: { value: "Bart the Bold" } });

      act(() => window.history.back());
      expect(await screen.findByText("Discard unsaved changes?")).toBeInTheDocument();
      fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
      await waitFor(() => expect(screen.queryByText("Discard unsaved changes?")).not.toBeInTheDocument());
      expect(router.state.location.search).toEqual({ node: "n-bart" });
      expect((screen.getByLabelText("Name") as HTMLInputElement).value).toBe("Bart the Bold");
    } finally {
      router.history.destroy();
      window.history.replaceState(null, "", "/");
    }
  });

  it("?agent= belongs on Cast: any other view redirects there, and Cast consumes it", async () => {
    const router = renderAt("/t/acme/campaign/overview?agent=npc-1");
    await waitFor(() => expect(router.state.location.pathname).toBe("/t/acme/campaign/cast"));
    // Cast opens on that NPC (not the Butler, the default selection)…
    await waitFor(() =>
      expect((screen.getByLabelText("Personality & backstory") as HTMLTextAreaElement).value).toBe("Grumbles."),
    );
    // …and the param is consumed.
    await waitFor(() => expect(router.state.location.search).toEqual({}));
  });

  it("a URL carrying both ?node= and ?agent= stays on the wiki: the losing ?agent= is dropped, not followed later", async () => {
    // ?node= wins the view. Were ?agent= left in the URL, it would resurface
    // the moment the GM closes the entry (which clears ?node=): beforeLoad
    // would then see only ?agent= and bounce them off the wiki, onto Cast.
    const router = renderAt("/t/acme/campaign/knowledge?node=n-bart&agent=npc-1");
    const page = await screen.findByTestId("entry-view");
    expect(within(page).getByRole("heading", { level: 2, name: "Bart of the Tankard" })).toBeInTheDocument();
    await waitFor(() => expect(router.state.location.search).toEqual({ node: "n-bart" }));

    fireEvent.click(within(page).getByRole("button", { name: "Close entry" }));
    await waitFor(() => expect(router.state.location.search).toEqual({}));
    expect(router.state.location.pathname).toBe("/t/acme/campaign/knowledge");
  });

  it("a bare /campaign?agent= link goes straight to Cast", async () => {
    const router = renderAt("/t/acme/campaign?agent=npc-1");
    await waitFor(() => expect(router.state.location.pathname).toBe("/t/acme/campaign/cast"));
  });
});

describe("campaigns screen", () => {
  it("serves the list of campaigns at /t/:slug/campaigns", async () => {
    renderAt("/t/acme/campaigns");
    expect(await screen.findByRole("heading", { level: 1, name: "Campaigns" })).toBeInTheDocument();
    const list = await screen.findByRole("list", { name: "Your campaigns" });
    expect(list).toHaveTextContent("The Sunless Citadel");
    expect(screen.getByRole("button", { name: "Open The Sunless Citadel" })).toBeInTheDocument();
  });

  it("still answers an unknown screen with the in-shell not-found page", async () => {
    renderAt("/t/acme/nonsense");
    expect(await screen.findByRole("heading", { name: "Not found" })).toBeInTheDocument();
    // …which lists the real destinations.
    expect(screen.getByRole("navigation", { name: "Go somewhere that exists" })).toBeInTheDocument();
  });
});

// The navigation seam in the REAL tree: screens are router-free and move
// through useScreenNav(), which the tenant layout provides (RouterScreenNav).
// screenNav.test.tsx covers the seam's rules on a stand-in tree; these pin that
// the app's own layout actually wires it — a screen that navigated into the
// inert default would do nothing at all.
describe("navigation seam wiring", () => {
  it("a router-free screen navigates: Open on the Campaigns screen lands on the Overview", async () => {
    const router = renderAt("/t/acme/campaigns");
    fireEvent.click(await screen.findByRole("button", { name: "Open The Sunless Citadel" }));
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/t/acme/campaign/overview"),
    );
  });

  it("a panel's cross-view jump navigates: an Overview tile opens its view", async () => {
    const router = renderAt("/t/acme/campaign/overview");
    const tiles = await screen.findByRole("list", { name: "Sections of this campaign" });
    fireEvent.click(within(tiles).getByRole("button", { name: /^World wiki/ }));
    await waitFor(() =>
      expect(router.state.location.pathname).toBe("/t/acme/campaign/knowledge"),
    );
    expect(await screen.findByRole("button", { name: "Edit Bart of the Tankard" })).toBeInTheDocument();
  });
});
