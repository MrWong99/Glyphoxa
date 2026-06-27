import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import type { ReactNode } from "react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import { UserSchema } from "@gen/glyphoxa/management/v1/management_pb";
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

function renderShell() {
  return render(
    <Providers transport={createRouterTransport(() => {})} queryClient={makeQueryClient()}>
      <AppShell tenantSlug="acme" user={user} />
    </Providers>,
  );
}

describe("AppShell", () => {
  it("toggles the sidebar collapsed state from the topbar control", () => {
    const { container } = renderShell();
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
});
