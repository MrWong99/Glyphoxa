import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, fireEvent, within } from "@testing-library/react";
import type { MouseEventHandler, ReactNode } from "react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  CampaignService,
  SessionService,
  UserSchema,
  CampaignSchema,
  ListCampaignsResponseSchema,
  GetActiveCampaignResponseSchema,
  GetSessionResponseSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";

// The shell is driven by TanStack Router; mock the bits AppShell + SidebarUser
// touch so it renders without a live router (mirrors AuthGate.test.tsx). The
// Link mock keeps onClick (the mobile drawer-close) and data-active/aria-current
// (the sub-navigation highlight) observable; useParams answers as if the route
// were /t/acme/campaign/cast so the Campaign sub-navigation renders (ADR-0063).
vi.mock("@tanstack/react-router", () => ({
  Link: ({
    children,
    className,
    onClick,
    "data-active": dataActive,
    "aria-current": ariaCurrent,
  }: {
    children: ReactNode;
    className?: string;
    onClick?: MouseEventHandler<HTMLAnchorElement>;
    "data-active"?: string;
    "aria-current"?: "page";
  }) => (
    <a className={className} onClick={onClick} data-active={dataActive} aria-current={ariaCurrent}>
      {children}
    </a>
  ),
  Outlet: () => null,
  useParams: () => ({ screen: "campaign", view: "cast" }),
  useNavigate: () => vi.fn(),
}));

import { AppShell } from "./AppShell";

const user = create(UserSchema, { name: "Sora Vance", role: "operator", avatar: "" });

// The topbar now hosts the CampaignSwitcher, which reads ListCampaigns /
// GetActiveCampaign / GetSession — implement them so those queries resolve
// cleanly instead of erroring/retrying under the shell test.
const campaign = create(CampaignSchema, {
  id: "c1",
  name: "The Sunless Citadel",
  system: "dnd5e",
  language: "en",
});
function shellTransport() {
  return createRouterTransport(({ service }) => {
    service(CampaignService, {
      listCampaigns: () => create(ListCampaignsResponseSchema, { campaigns: [campaign] }),
      getActiveCampaign: () => create(GetActiveCampaignResponseSchema, { campaign }),
    });
    service(SessionService, {
      getSession: () => create(GetSessionResponseSchema, { active: false }),
    });
  });
}

function renderShell() {
  return render(
    <Providers transport={shellTransport()} queryClient={makeQueryClient()}>
      <AppShell tenantSlug="acme" user={user} />
    </Providers>,
  );
}

describe("AppShell", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("toggles the sidebar collapsed state from the topbar control", async () => {
    const { container } = renderShell();
    // Let the switcher's initial reads settle so the topbar is stable first.
    expect(await screen.findByText("The Sunless Citadel")).toBeInTheDocument();
    const shell = container.querySelector(".gx-shell") as HTMLElement;
    const toggle = screen.getByRole("button", { name: /toggle sidebar/i });

    // Sidebar starts expanded.
    expect(shell).not.toHaveAttribute("data-collapsed", "true");
    expect(toggle).toHaveAttribute("aria-expanded", "true");

    // Clicking collapses it.
    fireEvent.click(toggle);
    expect(shell).toHaveAttribute("data-collapsed", "true");
    expect(toggle).toHaveAttribute("aria-expanded", "false");

    // Clicking again restores it.
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
  });

  it("renders the Campaign sub-navigation with the active sub-view highlighted (ADR-0063)", async () => {
    renderShell();
    expect(await screen.findByText("The Sunless Citadel")).toBeInTheDocument();

    // The mocked route is /t/acme/campaign/cast, so the sub-items render under
    // the Campaign nav item — one per sub-view, tab labels reused.
    const sub = screen.getByRole("group", { name: /campaign view/i });
    for (const label of ["Cast", "World wiki", "Maps", "Players", "Suggestions", "Planning"]) {
      expect(within(sub).getByText(label)).toBeInTheDocument();
    }
    // The :view path param drives the highlight: cast is active, the rest not.
    expect(within(sub).getByText("Cast").closest("a")).toHaveAttribute("data-active", "true");
    expect(within(sub).getByText("Maps").closest("a")).not.toHaveAttribute("data-active");
  });

  it("closes the mobile drawer when a nav item is tapped", async () => {
    // Narrow viewport: matchMedia matches the drawer breakpoint, so the shell
    // boots collapsed (drawer shut) and a nav tap must re-collapse it.
    vi.stubGlobal(
      "matchMedia",
      vi.fn().mockReturnValue({ matches: true } as MediaQueryList),
    );
    const { container } = renderShell();
    expect(await screen.findByText("The Sunless Citadel")).toBeInTheDocument();
    const shell = container.querySelector(".gx-shell") as HTMLElement;
    expect(shell).toHaveAttribute("data-collapsed", "true");

    // Open the drawer, then tap a sub-navigation item: it navigates (mocked)
    // AND shuts the drawer so it doesn't keep covering the screen.
    fireEvent.click(screen.getByRole("button", { name: /toggle sidebar/i }));
    expect(shell).not.toHaveAttribute("data-collapsed", "true");
    fireEvent.click(screen.getByText("Maps"));
    expect(shell).toHaveAttribute("data-collapsed", "true");
  });
});
