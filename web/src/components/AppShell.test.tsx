import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import type { ReactNode } from "react";
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
// touch so it renders without a live router (mirrors AuthGate.test.tsx).
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children: ReactNode }) => <a>{children}</a>,
  Outlet: () => null,
  useParams: () => ({ screen: "campaign" }),
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
});
