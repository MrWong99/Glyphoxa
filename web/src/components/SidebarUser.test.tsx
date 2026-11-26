import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { act, render, screen, waitFor, fireEvent, within } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { toast } from "sonner";
import { create } from "@bufbuild/protobuf";

import {
  AuthService,
  SessionService,
  UserSchema,
  LogoutResponseSchema,
  GetCurrentUserResponseSchema,
  GetSessionResponseSchema,
  VoiceSessionSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { SidebarUser } from "./SidebarUser";

const { navigate } = vi.hoisted(() => ({ navigate: vi.fn() }));
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => navigate }));

vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));

beforeEach(() => {
  navigate.mockClear();
  vi.mocked(toast.error).mockClear();
  localStorage.clear();
});
afterEach(() => {
  localStorage.clear();
});

const sora = (role = "operator") => create(UserSchema, { name: "Sora Vance", role, avatar: "" });

// What the account menu talks to: Logout, and the Voice Session read that
// decides whether logging out asks first. `logouts` counts the calls.
function backend(opts: { live?: boolean; logoutFails?: boolean } = {}) {
  const state = { logouts: 0 };
  const transport = createRouterTransport(({ service }) => {
    service(AuthService, {
      getCurrentUser: () => create(GetCurrentUserResponseSchema, { user: create(UserSchema, {}) }),
      logout: () => {
        state.logouts++;
        if (opts.logoutFails) throw new ConnectError("session store unavailable", Code.Unavailable);
        return create(LogoutResponseSchema, {});
      },
    });
    service(SessionService, {
      getSession: () =>
        create(GetSessionResponseSchema, {
          active: opts.live ?? false,
          session: opts.live ? create(VoiceSessionSchema, { id: "s1" }) : undefined,
        }),
    });
  });
  return { transport, state };
}

function renderUser(b = backend(), user = sora()) {
  const queryClient = makeQueryClient();
  const view = render(
    <Providers transport={b.transport} queryClient={queryClient}>
      <SidebarUser user={user} />
    </Providers>,
  );
  return { ...b, ...view, queryClient };
}

const trigger = () => screen.getByRole("button", { name: "Account: Sora Vance" });

// Radix opens a dropdown menu from the keyboard on Enter, Space or ArrowDown.
function openMenu() {
  const button = trigger();
  act(() => button.focus());
  fireEvent.keyDown(button, { key: "Enter" });
  return screen.getByRole("menu");
}

describe("SidebarUser identity", () => {
  it("renders the real signed-in user, with the role in the GM's words", () => {
    renderUser();
    expect(screen.getByText("Sora Vance")).toBeInTheDocument();
    // The server says 'operator'; at the table that person is the GM.
    expect(screen.getByText("GM")).toBeInTheDocument();
    expect(screen.queryByText("operator")).not.toBeInTheDocument();
  });

  it("shows a role it does not know as the server sent it, rather than hiding it", () => {
    renderUser(backend(), sora("archivist"));
    expect(screen.getByText("archivist")).toBeInTheDocument();
  });

  it("shows no presence dot — the avatar is an identity, not a live-session signal", () => {
    // It used to carry a hardcoded green "live" dot on every screen, whether or
    // not a Voice Session was running. The real signal is the shell's
    // (useLiveSession), on the Session nav item and in the topbar.
    const { container } = renderUser();
    expect(container.querySelector(".gx-avatar")).toBeInTheDocument();
    expect(container.querySelector(".gx-avatar__status")).not.toBeInTheDocument();
  });
});

describe("SidebarUser account menu", () => {
  it("opens from the footer row with language, the legal documents and Log out", () => {
    renderUser();
    expect(trigger()).toHaveAttribute("aria-haspopup", "menu");
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();

    const menu = openMenu();
    // Display language: one of two, the current one checked.
    const langs = within(menu).getAllByRole("menuitemradio");
    expect(langs.map((item) => [item.textContent, item.getAttribute("aria-checked")])).toEqual([
      ["English", "true"],
      ["Deutsch", "false"],
    ]);
    // The three legal documents, by their own names, as real links.
    for (const [name, href] of [
      [/^Impressum/, "/imprint"],
      [/^Datenschutz/, "/privacy"],
      [/^Nutzungsbedingungen/, "/terms"],
    ] as const) {
      const link = within(menu).getByRole("menuitem", { name });
      expect(link).toHaveAttribute("href", href);
      // A new tab: the screen the GM is on (a live session, above all) stays.
      expect(link).toHaveAttribute("target", "_blank");
      expect(link).toHaveAccessibleName(/opens in a new tab/);
    }
    expect(within(menu).getByRole("menuitem", { name: "Log out" })).toBeInTheDocument();
  });

  it("closes on Escape and gives focus back to the row", async () => {
    renderUser();
    const menu = openMenu();
    const first = within(menu).getByRole("menuitemradio", { name: "English" });
    await waitFor(() => expect(first).toHaveFocus());
    fireEvent.keyDown(first, { key: "Escape" });
    expect(screen.queryByRole("menu")).not.toBeInTheDocument();
    await waitFor(() => expect(trigger()).toHaveFocus());
  });

  it("switches the display language from the menu and remembers it", () => {
    renderUser();
    fireEvent.click(within(openMenu()).getByRole("menuitemradio", { name: "Deutsch" }));

    expect(localStorage.getItem("gx-lang")).toBe("de");
    // The row itself is now German…
    const button = screen.getByRole("button", { name: "Konto: Sora Vance" });
    // …and so is the menu, with the new language checked.
    act(() => button.focus());
    fireEvent.keyDown(button, { key: "Enter" });
    const menu = screen.getByRole("menu");
    expect(within(menu).getByRole("menuitemradio", { name: "Deutsch" })).toHaveAttribute("aria-checked", "true");
    expect(within(menu).getByRole("menuitem", { name: "Abmelden" })).toBeInTheDocument();
  });
});

describe("SidebarUser log out", () => {
  it("logs out at once while no session is live — calls Logout and goes to /login", async () => {
    const { state } = renderUser();
    fireEvent.click(within(openMenu()).getByRole("menuitem", { name: "Log out" }));

    await waitFor(() => expect(state.logouts).toBe(1));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: "/login" }));
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
  });

  it("asks first while a Voice Session is live, and says what logging out does not do", async () => {
    const b = backend({ live: true });
    const { queryClient } = renderUser(b);
    // The live read has landed (in the app the shell made it long before the
    // GM reaches for the menu).
    await waitFor(() => expect(queryClient.isFetching()).toBe(0));

    fireEvent.click(within(openMenu()).getByRole("menuitem", { name: "Log out" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(dialog).toHaveTextContent("Log out while a session is live?");
    // The session is not stopped by this — only the console goes away.
    expect(dialog).toHaveTextContent(/keeps running/i);
    expect(b.state.logouts).toBe(0);

    // Cancel: nothing happened, the GM is still signed in.
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
    expect(b.state.logouts).toBe(0);
    expect(navigate).not.toHaveBeenCalled();

    // Confirm: now it logs out.
    fireEvent.click(within(openMenu()).getByRole("menuitem", { name: "Log out" }));
    fireEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Log out" }));
    await waitFor(() => expect(b.state.logouts).toBe(1));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: "/login" }));
  });

  it("says so when the logout fails, and stays put (W15)", async () => {
    renderUser(backend({ logoutFails: true }));
    fireEvent.click(within(openMenu()).getByRole("menuitem", { name: "Log out" }));

    await waitFor(() =>
      expect(vi.mocked(toast.error)).toHaveBeenCalledWith(
        expect.stringContaining("session store unavailable"),
      ),
    );
    expect(navigate).not.toHaveBeenCalled();
    // Still signed in, and Log out is there to try again.
    await waitFor(() =>
      expect(within(openMenu()).getByRole("menuitem", { name: "Log out" })).not.toHaveAttribute("aria-disabled"),
    );
  });
});
