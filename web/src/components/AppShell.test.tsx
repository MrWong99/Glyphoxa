import { describe, it, expect, vi, afterEach, beforeEach } from "vitest";
import { act, render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import type { AnchorHTMLAttributes, ReactNode } from "react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { createConnectQueryKey } from "@connectrpc/connect-query";
import { focusManager } from "@tanstack/react-query";

import {
  CampaignService,
  SessionService,
  UserSchema,
  CampaignSchema,
  ListCampaignsResponseSchema,
  GetActiveCampaignResponseSchema,
  GetSessionResponseSchema,
  KnowledgeProposalSchema,
  ListKnowledgeProposalsResponseSchema,
  VoiceSessionSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";

// The shell is driven by TanStack Router; mock the bits AppShell + SidebarUser
// touch so it renders without a live router (mirrors AuthGate.test.tsx). The
// Link mock resolves `to` + `params` into a real href and passes the anchor
// attributes through, so a test can read where a link goes, its active state
// and its accessible name. useParams answers from `route`, which a test sets
// to put the shell on a given screen.
const { route } = vi.hoisted(() => ({
  route: { screen: "campaign" as string | undefined, view: "cast" as string | undefined },
}));
vi.mock("@tanstack/react-router", () => ({
  Link: ({
    to,
    params,
    children,
    ...rest
  }: {
    to: string;
    params?: Record<string, string>;
    children: ReactNode;
  } & AnchorHTMLAttributes<HTMLAnchorElement>) => (
    <a href={to.replace(/\$(\w+)/g, (_, name: string) => params?.[name] ?? "")} {...rest}>
      {children}
    </a>
  ),
  Outlet: () => <div data-testid="outlet" />,
  useParams: () => ({ ...route }),
  useNavigate: () => vi.fn(),
}));

import { ProposalsPanel } from "@/screens/campaign/ProposalsPanel";
import { AppShell } from "./AppShell";

const user = create(UserSchema, { name: "Sora Vance", role: "operator", avatar: "" });

const campaign = create(CampaignSchema, {
  id: "c1",
  name: "The Sunless Citadel",
  system: "dnd5e",
  language: "en",
});

// What the shell reads on every screen: the Active Campaign (section label,
// breadcrumb, tab title), the Voice Session (live signals) and the pending
// Knowledge Proposals (the Suggestions count). `live` and `pending` are mutable
// so a test can change the server's answer under a mounted shell;
// `failProposalReads` makes the next N proposal reads fail the way a network
// blip does, and `proposalReads` counts them.
function shellBackend(opts: { live?: boolean; pending?: number; noCampaign?: boolean } = {}) {
  const state = {
    live: opts.live ?? false,
    pending: opts.pending ?? 0,
    startedAt: new Date(Date.now() - (3600 + 12 * 60 + 40) * 1000), // 01:12:40 ago
    proposalReads: 0,
    failProposalReads: 0,
  };
  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      listCampaigns: () => create(ListCampaignsResponseSchema, { campaigns: opts.noCampaign ? [] : [campaign] }),
      getActiveCampaign: () => {
        if (opts.noCampaign) throw new ConnectError("no campaign", Code.NotFound);
        return create(GetActiveCampaignResponseSchema, { campaign });
      },
      listKnowledgeProposals: () => {
        state.proposalReads++;
        if (opts.noCampaign) throw new ConnectError("no campaign", Code.NotFound);
        if (state.failProposalReads > 0) {
          state.failProposalReads--;
          throw new ConnectError("blip", Code.Unavailable);
        }
        return create(ListKnowledgeProposalsResponseSchema, {
          proposals: Array.from({ length: state.pending }, (_, i) =>
            create(KnowledgeProposalSchema, { id: `p${i}` }),
          ),
        });
      },
    });
    service(SessionService, {
      getSession: () =>
        create(GetSessionResponseSchema, {
          active: state.live,
          session: state.live
            ? create(VoiceSessionSchema, { id: "s1", startedAt: timestampFromDate(state.startedAt) })
            : undefined,
        }),
    });
  });
  return { transport, state };
}

function renderShell(opts: Parameters<typeof shellBackend>[0] = {}) {
  const h = shellBackend(opts);
  const queryClient = makeQueryClient();
  const view = render(
    <Providers transport={h.transport} queryClient={queryClient}>
      <AppShell tenantSlug="acme" user={user} />
    </Providers>,
  );
  return { ...h, queryClient, container: view.container, unmount: view.unmount };
}

const sidebar = (container: HTMLElement) => container.querySelector(".gx-sidebar") as HTMLElement;
const topbar = (container: HTMLElement) => container.querySelector(".gx-topbar") as HTMLElement;
// Scoped to the sidebar: the topbar switcher also shows the campaign's name.
const settled = (container: HTMLElement) =>
  within(sidebar(container)).findByText("The Sunless Citadel");

beforeEach(() => {
  route.screen = "campaign";
  route.view = "cast";
});

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
  // Back to "ask the document" for the one test that fakes a tab refocus.
  focusManager.setFocused(undefined);
});

describe("AppShell chrome", () => {
  it("toggles the sidebar collapsed state from the topbar control", async () => {
    const { container } = renderShell();
    await settled(container);
    const shell = container.querySelector(".gx-shell") as HTMLElement;
    const toggle = screen.getByRole("button", { name: /toggle sidebar/i });

    // Sidebar starts expanded.
    expect(shell).not.toHaveAttribute("data-collapsed", "true");
    expect(toggle).toHaveAttribute("aria-expanded", "true");

    fireEvent.click(toggle);
    expect(shell).toHaveAttribute("data-collapsed", "true");
    expect(toggle).toHaveAttribute("aria-expanded", "false");

    fireEvent.click(toggle);
    expect(shell).not.toHaveAttribute("data-collapsed", "true");
    expect(toggle).toHaveAttribute("aria-expanded", "true");
  });

  it("renders interactive shell chrome with a dead backend (every RPC failing)", async () => {
    // Backend unreachable at app load: the shell — sidebar, toggle, topbar —
    // must stay functional, and the campaign switcher must settle into its
    // non-blocking fallback label instead of wedging the topbar.
    const { container } = render(
      <Providers transport={createRouterTransport(() => {})} queryClient={makeQueryClient()}>
        <AppShell tenantSlug="acme" user={user} />
      </Providers>,
    );

    const toggle = screen.getByRole("button", { name: /toggle sidebar/i });
    fireEvent.click(toggle);
    expect(container.querySelector(".gx-shell")).toHaveAttribute("data-collapsed", "true");

    expect(await screen.findByText("Select campaign")).toBeInTheDocument();
    // Every destination is still there to click; no count, no live marker.
    const nav = screen.getByRole("navigation", { name: "Main navigation" });
    expect(within(nav).getAllByRole("link")).toHaveLength(11);
    expect(screen.queryByTestId("nav-live")).not.toBeInTheDocument();
    expect(container.querySelector(".gx-nav__count")).not.toBeInTheDocument();
  });

  it("closes the mobile drawer when a nav item is tapped", async () => {
    // Narrow viewport: matchMedia matches the drawer breakpoint, so the shell
    // boots collapsed (drawer shut) and a nav tap must re-collapse it.
    vi.stubGlobal("matchMedia", vi.fn().mockReturnValue({ matches: true } as MediaQueryList));
    const { container } = renderShell();
    await settled(container);
    const shell = container.querySelector(".gx-shell") as HTMLElement;
    expect(shell).toHaveAttribute("data-collapsed", "true");

    // Open the drawer, then tap a campaign view: it navigates (mocked) AND
    // shuts the drawer so it doesn't keep covering the screen.
    const toggle = screen.getByRole("button", { name: /toggle sidebar/i });
    fireEvent.click(toggle);
    expect(shell).not.toHaveAttribute("data-collapsed", "true");
    fireEvent.click(screen.getByRole("link", { name: "Maps" }));
    expect(shell).toHaveAttribute("data-collapsed", "true");

    // Same for the pinned items and the brand.
    for (const name of ["Setup", /campaign overview/i]) {
      fireEvent.click(toggle);
      expect(shell).not.toHaveAttribute("data-collapsed", "true");
      fireEvent.click(screen.getByRole("link", { name }));
      expect(shell).toHaveAttribute("data-collapsed", "true");
    }
  });

  it("links the brand to the tenant index — the way home", async () => {
    const { container } = renderShell();
    await settled(container);
    expect(screen.getByRole("link", { name: /glyphoxa — campaign overview/i })).toHaveAttribute("href", "/t/acme");
  });

  it("shows no presence dot on the signed-in user (it used to be a hardcoded 'live')", async () => {
    const { container } = renderShell({ live: false });
    await settled(container);
    expect(container.querySelector(".gx-sidebar__user .gx-avatar__status")).not.toBeInTheDocument();
  });

  it("scrolls the content back to the top when the screen or the view changes", async () => {
    const h = shellBackend();
    const queryClient = makeQueryClient();
    // A fresh element per render: the mocked useParams is re-read only when
    // the shell actually re-renders.
    const tree = () => (
      <Providers transport={h.transport} queryClient={queryClient}>
        <AppShell tenantSlug="acme" user={user} />
      </Providers>
    );
    const { container, rerender } = render(tree());
    await settled(container);
    const content = container.querySelector(".gx-content") as HTMLElement;

    content.scrollTop = 480;
    route.view = "knowledge";
    rerender(tree());
    expect(content.scrollTop).toBe(0);

    content.scrollTop = 480;
    route.screen = "session";
    route.view = undefined;
    rerender(tree());
    expect(content.scrollTop).toBe(0);

    // Re-rendering on the SAME page (a poll landing, a clock tick) leaves the
    // GM's scroll position alone.
    content.scrollTop = 480;
    rerender(tree());
    expect(content.scrollTop).toBe(480);
  });
});

describe("AppShell sidebar (ADR-0063 amendment)", () => {
  const VIEWS: [string, string][] = [
    ["Overview", "/t/acme/campaign/overview"],
    ["Cast", "/t/acme/campaign/cast"],
    ["World wiki", "/t/acme/campaign/knowledge"],
    ["Maps", "/t/acme/campaign/maps"],
    ["Players", "/t/acme/campaign/players"],
    ["Suggestions", "/t/acme/campaign/proposals"],
    ["Planning", "/t/acme/campaign/planning"],
    ["Settings", "/t/acme/campaign/settings"],
  ];

  it("labels the campaign section with the Active Campaign and lists its eight views", async () => {
    const { container } = renderShell();
    await settled(container);

    // One group, named by its heading: "Campaign" + the campaign's name.
    const section = screen.getByRole("group", { name: /campaign.*the sunless citadel/i });
    const links = within(section).getAllByRole("link");
    expect(links.map((a) => [a.textContent, a.getAttribute("href")])).toEqual(VIEWS);

    // The :view path param drives the highlight: Cast is the current page.
    const cast = within(section).getByRole("link", { name: "Cast" });
    expect(cast).toHaveAttribute("data-active", "true");
    expect(cast).toHaveAttribute("aria-current", "page");
    const maps = within(section).getByRole("link", { name: "Maps" });
    expect(maps).not.toHaveAttribute("data-active");
    expect(maps).not.toHaveAttribute("aria-current");
  });

  it.each([
    ["session", "Session"],
    ["configuration", "Setup"],
    ["campaigns", "Campaigns"],
  ])("still shows every campaign view from the %s screen — World wiki is one click away", async (screenName, label) => {
    route.screen = screenName;
    route.view = undefined;
    const { container } = renderShell();
    await settled(container);

    const section = screen.getByRole("group", { name: /campaign.*the sunless citadel/i });
    expect(within(section).getAllByRole("link").map((a) => a.textContent)).toEqual(VIEWS.map(([l]) => l));
    // A direct link to the wiki — not to Cast first, not through a parent item.
    expect(within(section).getByRole("link", { name: "World wiki" })).toHaveAttribute(
      "href",
      "/t/acme/campaign/knowledge",
    );
    // None of the campaign's views is the current page; this screen's item is.
    for (const a of within(section).getAllByRole("link")) expect(a).not.toHaveAttribute("aria-current");
    const nav = screen.getByRole("navigation", { name: "Main navigation" });
    expect(within(nav).getByRole("link", { name: label })).toHaveAttribute("aria-current", "page");
  });

  it("puts Session after the campaign's views and pins Campaigns and Setup at the bottom", async () => {
    const { container } = renderShell();
    await settled(container);
    const nav = screen.getByRole("navigation", { name: "Main navigation" });

    const main = nav.querySelector(".gx-nav__main") as HTMLElement;
    const foot = nav.querySelector(".gx-nav__foot") as HTMLElement;
    // The scrolling list: the eight views, then Session.
    expect(within(main).getAllByRole("link").map((a) => a.getAttribute("href"))).toEqual([
      ...VIEWS.map(([, href]) => href),
      "/t/acme/session",
    ]);
    // The pinned foot: the two Tenant-wide destinations.
    expect(within(foot).getAllByRole("link").map((a) => [a.textContent, a.getAttribute("href")])).toEqual([
      ["Campaigns", "/t/acme/campaigns"],
      ["Setup", "/t/acme/configuration"],
    ]);
  });

  it("says so in the section label when no campaign exists yet", async () => {
    const { container } = renderShell({ noCampaign: true });
    expect(await within(sidebar(container)).findByText("No campaign yet")).toBeInTheDocument();
    // The views are still listed: each leads to the create-first-campaign card.
    const section = screen.getByRole("group", { name: /campaign.*no campaign yet/i });
    expect(within(section).getAllByRole("link")).toHaveLength(8);
  });

  it("is localized", async () => {
    localStorage.setItem("gx-lang", "de");
    try {
      const { container } = renderShell();
      await settled(container);
      const nav = screen.getByRole("navigation", { name: "Hauptnavigation" });
      for (const label of ["Übersicht", "Besetzung", "Welt-Wiki", "Einstellungen", "Spielrunde", "Kampagnen", "Einrichtung"]) {
        expect(within(nav).getByRole("link", { name: label })).toBeInTheDocument();
      }
    } finally {
      localStorage.removeItem("gx-lang");
    }
  });
});

describe("AppShell topbar", () => {
  it("shows '<Campaign> / <View>' on a campaign view and names the browser tab '<View> · <Campaign> · Glyphoxa'", async () => {
    route.view = "knowledge";
    const { container } = renderShell();
    await settled(container);

    const crumbs = screen.getByRole("navigation", { name: "You are here" });
    const items = within(crumbs).getAllByRole("listitem");
    expect(items).toHaveLength(2);
    // The campaign crumb leads to its Overview…
    expect(within(items[0]).getByRole("link", { name: "The Sunless Citadel" })).toHaveAttribute(
      "href",
      "/t/acme/campaign/overview",
    );
    // …and the view is the current page.
    expect(items[1]).toHaveAttribute("aria-current", "page");
    expect(items[1]).toHaveTextContent("World wiki");

    expect(document.title).toBe("World wiki · The Sunless Citadel · Glyphoxa");
  });

  it("does not link the campaign crumb to the page it is already on", async () => {
    route.view = "overview";
    const { container } = renderShell();
    await settled(container);
    const crumbs = screen.getByRole("navigation", { name: "You are here" });
    expect(within(crumbs).queryByRole("link")).not.toBeInTheDocument();
    expect(crumbs).toHaveTextContent("The Sunless Citadel");
    expect(document.title).toBe("Overview · The Sunless Citadel · Glyphoxa");
  });

  it("names the campaign on the Session screen too — a session is one campaign's", async () => {
    route.screen = "session";
    route.view = undefined;
    const { container } = renderShell();
    await settled(container);
    expect(screen.getByRole("navigation", { name: "You are here" })).toHaveTextContent(
      /The Sunless Citadel.*Session/,
    );
    expect(document.title).toBe("Session · The Sunless Citadel · Glyphoxa");
  });

  it.each([
    ["configuration", "Setup"],
    ["campaigns", "Campaigns"],
  ])("gives the Tenant-wide %s screen a plain title, without a campaign", async (screenName, title) => {
    route.screen = screenName;
    route.view = undefined;
    const { container } = renderShell();
    await settled(container);
    expect(screen.queryByRole("navigation", { name: "You are here" })).not.toBeInTheDocument();
    expect(topbar(container).querySelector(".gx-topbar__title")).toHaveTextContent(title);
    expect(document.title).toBe(`${title} · Glyphoxa`);
  });

  it("falls back to the bare view name until the campaign is known", async () => {
    renderShell({ noCampaign: true });
    await screen.findByText("No campaign yet");
    expect(screen.queryByRole("navigation", { name: "You are here" })).not.toBeInTheDocument();
    expect(document.title).toBe("Cast · Glyphoxa");
  });

  // Nothing else in the app writes the tab title, and a lapsed session leaves
  // the shell by an in-app navigation to /login — which would otherwise keep
  // "<View> · <Campaign> · Glyphoxa" on a public page.
  it("hands the browser tab back to the product name when the shell unmounts", async () => {
    route.view = "knowledge";
    const { container, unmount } = renderShell();
    await settled(container);
    await waitFor(() => expect(document.title).toBe("World wiki · The Sunless Citadel · Glyphoxa"));

    unmount();
    expect(document.title).toBe("Glyphoxa");
  });

  it("titles an unknown screen with the product name alone", async () => {
    route.screen = "nonsense";
    route.view = undefined;
    const { container } = renderShell();
    await settled(container);
    expect(document.title).toBe("Glyphoxa");
  });
});

describe("AppShell live-session signals", () => {
  it("marks the Session item live with the elapsed time, and offers a topbar pill to the Session screen from the wiki", async () => {
    route.view = "knowledge";
    const { container } = renderShell({ live: true });
    await settled(container);

    // Sidebar: the Session item carries the marker — "Live" for screen readers,
    // the running clock for everyone.
    const session = await screen.findByRole("link", { name: /^Session\s*Live\s*01:12:4\d$/ });
    expect(session).toHaveAttribute("href", "/t/acme/session");
    expect(within(session).getByTestId("nav-live")).toBeInTheDocument();

    // Topbar: "Live 01:12:40 · Open session", linking to the Session screen.
    const pill = within(topbar(container)).getByRole("link", { name: /Live\s*01:12:4\d.*Open session/ });
    expect(pill).toHaveAttribute("href", "/t/acme/session");
  });

  it("drops the topbar pill on the Session screen itself (the screen's own controls are right there)", async () => {
    route.screen = "session";
    route.view = undefined;
    const { container } = renderShell({ live: true });
    await settled(container);
    expect(await screen.findByTestId("nav-live")).toBeInTheDocument();
    expect(within(topbar(container)).queryByRole("link", { name: /open session/i })).not.toBeInTheDocument();
  });

  it("shows neither marker nor pill while no session is live", async () => {
    const { container } = renderShell({ live: false });
    await settled(container);
    expect(screen.queryByTestId("nav-live")).not.toBeInTheDocument();
    expect(within(topbar(container)).queryByRole("link", { name: /open session/i })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Session" })).toBeInTheDocument();
  });

  it("clears the signals when the session ends", async () => {
    const { container, state, queryClient } = renderShell({ live: true });
    await settled(container);
    expect(await screen.findByTestId("nav-live")).toBeInTheDocument();

    state.live = false;
    await act(async () => {
      await queryClient.invalidateQueries();
    });
    await waitFor(() => expect(screen.queryByTestId("nav-live")).not.toBeInTheDocument());
    expect(within(topbar(container)).queryByRole("link", { name: /open session/i })).not.toBeInTheDocument();
  });
});

describe("AppShell pending-suggestions count", () => {
  it("shows the count on Suggestions and carries it in the link's accessible name", async () => {
    const { container } = renderShell({ pending: 3 });
    await settled(container);
    const link = await screen.findByRole("link", { name: "Suggestions, 3 pending" });
    expect(link).toHaveAttribute("href", "/t/acme/campaign/proposals");
    // The pill is the visual twin of the name — hidden from assistive tech so
    // the count is not read twice.
    const pill = link.querySelector(".gx-nav__count") as HTMLElement;
    expect(pill).toHaveTextContent("3");
    expect(pill).toHaveAttribute("aria-hidden", "true");
  });

  it("shows no pill for an empty queue", async () => {
    const { container } = renderShell({ pending: 0 });
    await settled(container);
    expect(screen.getByRole("link", { name: "Suggestions" })).toBeInTheDocument();
    expect(container.querySelector(".gx-nav__count")).not.toBeInTheDocument();
  });

  it("caps a long queue at 99+ without lying in the accessible name", async () => {
    const { container } = renderShell({ pending: 140 });
    await settled(container);
    const link = await screen.findByRole("link", { name: "Suggestions, 140 pending" });
    expect(link.querySelector(".gx-nav__count")).toHaveTextContent("99+");
  });

  it("follows the queue when a suggestion is reviewed elsewhere in the app", async () => {
    const { container, state, queryClient } = renderShell({ pending: 2 });
    await settled(container);
    await screen.findByRole("link", { name: "Suggestions, 2 pending" });

    // The Suggestions view invalidates this same read after an approve/dismiss.
    state.pending = 1;
    await act(async () => {
      await queryClient.invalidateQueries();
    });
    expect(await screen.findByRole("link", { name: "Suggestions, 1 pending" })).toBeInTheDocument();
  });

  // The shell's read and the Suggestions view's read are ONE cache entry, and
  // React Query runs a refetch with the options of the observer that starts it
  // — on a window refocus that is the first one subscribed: the shell's, which
  // mounts before any screen. With retry:false there, a single failed refetch
  // put the query in error, and the view (which renders an error as a
  // replacement of the list) swapped the GM's whole review queue for "Couldn't
  // load suggestions".
  it("does not cost the Suggestions view its list when one refetch fails", async () => {
    const h = shellBackend({ pending: 2 });
    const queryClient = makeQueryClient();
    const ui = (withPanel: boolean) => (
      <Providers transport={h.transport} queryClient={queryClient}>
        <AppShell tenantSlug="acme" user={user} />
        {withPanel && <ProposalsPanel />}
      </Providers>
    );
    // The shell first, as in the app; the GM opens Suggestions afterwards.
    const view = render(ui(false));
    await screen.findByRole("link", { name: "Suggestions, 2 pending" });
    view.rerender(ui(true));
    const cards = () => view.container.querySelectorAll(".gx-proposal-card");
    await waitFor(() => expect(cards()).toHaveLength(2));
    expect(h.state.proposalReads).toBe(1);

    // The GM comes back to the tab after the read has gone stale, and that
    // one refetch hits a network blip.
    h.state.failProposalReads = 1;
    await queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: CampaignService.method.listKnowledgeProposals,
        cardinality: "finite",
      }),
      refetchType: "none",
    });
    act(() => focusManager.setFocused(true));
    await waitFor(() => expect(h.state.proposalReads).toBe(2));

    // It is retried like any other read (the third read), and meanwhile the
    // list and the count stay as they were.
    await waitFor(() => expect(h.state.proposalReads).toBe(3), { timeout: 3000 });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(cards()).toHaveLength(2);
    expect(screen.getByRole("link", { name: "Suggestions, 2 pending" })).toBeInTheDocument();
  });
});
