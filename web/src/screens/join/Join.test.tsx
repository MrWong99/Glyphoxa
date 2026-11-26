import { describe, it, expect } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create, type MessageInitShape } from "@bufbuild/protobuf";

import {
  AuthService,
  GetInvitationResponseSchema,
  GuestRight,
  InvitationKind,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { Join } from "./Join";

// The join page against a fake AuthService.GetInvitation — the one read it
// makes, unauthenticated. `seen` records the tokens asked about; `fail` makes
// the next reads fail the way a network blip does.

const ACCESS: MessageInitShape<typeof GetInvitationResponseSchema> = {
  valid: true,
  kind: InvitationKind.ACCESS,
  inviterName: "Sora Vance",
};

const CAMPAIGN: MessageInitShape<typeof GetInvitationResponseSchema> = {
  valid: true,
  kind: InvitationKind.CAMPAIGN,
  inviterName: "Mira",
  campaignName: "Tides over Saltmarsh",
  tenantName: "Mira's Table",
  rights: [GuestRight.MAPS_READ, GuestRight.WIKI_READ, GuestRight.PLANNING_USE, GuestRight.WIKI_SECRETS],
};

function backend(answer: MessageInitShape<typeof GetInvitationResponseSchema>, opts: { fail?: number } = {}) {
  const state = { seen: [] as string[], fail: opts.fail ?? 0 };
  const transport = createRouterTransport(({ service }) => {
    service(AuthService, {
      getInvitation: (req) => {
        state.seen.push(req.token);
        if (state.fail > 0) {
          state.fail--;
          throw new ConnectError("blip", Code.Unavailable);
        }
        return create(GetInvitationResponseSchema, answer);
      },
    });
  });
  return { transport, state };
}

function renderJoin(
  answer: MessageInitShape<typeof GetInvitationResponseSchema>,
  props: { refused?: boolean; aupRequired?: boolean } = {},
  opts: { fail?: number } = {},
) {
  const b = backend(answer, opts);
  render(
    <Providers transport={b.transport} queryClient={makeQueryClient()}>
      <Join token="tok-moonlit-harbor" {...props} />
    </Providers>,
  );
  return b;
}

const START = "/auth/discord/login?join=tok-moonlit-harbor&aup=1";

describe("Join: an access invitation", () => {
  it("says who invited you and what you get", async () => {
    const { state } = renderJoin(ACCESS);
    expect(await screen.findByRole("heading", { name: "Sora Vance invited you to Glyphoxa" })).toBeInTheDocument();
    expect(
      screen.getByText("You get a workspace of your own: a table for your campaigns, NPCs and Discord server."),
    ).toBeInTheDocument();
    // The preview is asked about exactly the token in the path.
    expect(state.seen).toEqual(["tok-moonlit-harbor"]);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("keeps the Discord start a disabled button until the terms are acknowledged", async () => {
    renderJoin(ACCESS);
    await screen.findByRole("heading", { name: /invited you to Glyphoxa/ });

    // A real disabled button — an aria-disabled link would still navigate.
    expect(screen.queryByRole("link", { name: "Continue with Discord" })).not.toBeInTheDocument();
    const blocked = screen.getByRole("button", { name: "Continue with Discord" });
    expect(blocked).toBeDisabled();
    expect(blocked).toHaveAccessibleDescription("Tick the box above to continue.");

    const box = screen.getByRole("checkbox");
    expect(box).not.toBeChecked();
    fireEvent.click(box);

    // The start carries the token and aup=1 (Go↔TS contract with internal/auth).
    expect(screen.getByRole("link", { name: "Continue with Discord" })).toHaveAttribute("href", START);
    expect(screen.queryByRole("button", { name: "Continue with Discord" })).not.toBeInTheDocument();

    // Unticking takes the link away again.
    fireEvent.click(box);
    expect(screen.queryByRole("link", { name: "Continue with Discord" })).not.toBeInTheDocument();
  });

  it("asks with the login screen's acknowledgment: the same two documents, by name", async () => {
    renderJoin(ACCESS);
    await screen.findByRole("heading", { name: /invited you to Glyphoxa/ });
    const label = screen.getByRole("checkbox").closest("label") as HTMLElement;
    expect(label).toHaveTextContent("I agree to the Nutzungsbedingungen and have read the Datenschutzerklärung.");
    expect(within(label).getByRole("link", { name: "Nutzungsbedingungen" })).toHaveAttribute("href", "/terms");
    expect(within(label).getByRole("link", { name: "Datenschutzerklärung" })).toHaveAttribute("href", "/privacy");
  });

  it("does not invent an inviter the server did not name", async () => {
    renderJoin({ ...ACCESS, inviterName: "" });
    expect(await screen.findByRole("heading", { name: "You're invited to Glyphoxa" })).toBeInTheDocument();
  });

  it("keeps the wordmark as the way home, the legal footer and the language picker", async () => {
    renderJoin(ACCESS);
    await screen.findByRole("heading", { name: /invited you to Glyphoxa/ });
    expect(screen.getByRole("link", { name: "Glyphoxa — start page" })).toHaveAttribute("href", "/");
    expect(screen.getByRole("link", { name: "Impressum" })).toHaveAttribute("href", "/imprint");
    expect(screen.getByRole("combobox", { name: "Display language" })).toBeInTheDocument();
  });
});

describe("Join: a campaign invitation", () => {
  it("names the campaign, whose table it is, and the rights in plain words", async () => {
    renderJoin(CAMPAIGN);
    expect(
      await screen.findByRole("heading", { name: "Mira invited you to Tides over Saltmarsh" }),
    ).toBeInTheDocument();
    expect(screen.getByText("A campaign at Mira's Table.")).toBeInTheDocument();

    // In reading order — the wiki, maps, then the planning chat — whatever
    // order the server listed them in, each with its sentence.
    const items = within(screen.getByRole("list")).getAllByRole("listitem");
    expect(items.map((li) => li.firstElementChild?.textContent)).toEqual([
      "Read the world wiki",
      "See GM-only wiki entries",
      "View maps",
      "Use the planning chat",
    ]);
    expect(items[0]).toHaveTextContent("The public entries. GM-only entries and private notes stay hidden.");
    expect(screen.queryByText("Edit maps")).not.toBeInTheDocument();
    // Not an access invitation: no table of their own is promised.
    expect(screen.queryByText(/workspace of your own/)).not.toBeInTheDocument();
  });

  it("requires the acknowledgment here too, and starts OAuth with the same link", async () => {
    renderJoin(CAMPAIGN);
    await screen.findByRole("heading", { name: /invited you to Tides over Saltmarsh/ });
    expect(screen.getByRole("button", { name: "Continue with Discord" })).toBeDisabled();
    fireEvent.click(screen.getByRole("checkbox"));
    expect(screen.getByRole("link", { name: "Continue with Discord" })).toHaveAttribute("href", START);
  });

  it("says so when the invitation grants nothing yet", async () => {
    renderJoin({ ...CAMPAIGN, rights: [] });
    await screen.findByRole("heading", { name: /invited you to Tides over Saltmarsh/ });
    expect(screen.getByText(/Nothing in it is shared with guests yet/)).toBeInTheDocument();
    expect(screen.queryByRole("list")).not.toBeInTheDocument();
  });
});

describe("Join: a dead link", () => {
  it("says calmly that the link no longer works, with the way to the start page", async () => {
    renderJoin({ valid: false });
    expect(await screen.findByRole("heading", { name: "This invitation link no longer works" })).toBeInTheDocument();
    expect(
      screen.getByText("It may have expired, been used up or revoked. Ask whoever sent it for a new one."),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Go to the start page" })).toHaveAttribute("href", "/");
    // Calm: not an error banner, and nothing to sign in with.
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    expect(screen.queryByText("Continue with Discord")).not.toBeInTheDocument();
  });

  it("believes `valid = false` over anything else the answer carries", async () => {
    // The contract says the other fields are empty then; if they are not, the
    // verdict still decides — a dead link never grows a sign-in button.
    renderJoin({ ...CAMPAIGN, valid: false });
    expect(await screen.findByRole("heading", { name: "This invitation link no longer works" })).toBeInTheDocument();
    expect(screen.queryByText(/invited you to/)).not.toBeInTheDocument();
    expect(screen.queryByText("Continue with Discord")).not.toBeInTheDocument();
  });

  it("treats a kind it does not know as a link it cannot honour", async () => {
    renderJoin({ valid: true, kind: InvitationKind.UNSPECIFIED });
    expect(await screen.findByRole("heading", { name: "This invitation link no longer works" })).toBeInTheDocument();
    expect(screen.queryByText("Continue with Discord")).not.toBeInTheDocument();
  });
});

describe("Join: bounced back by the server", () => {
  it("?error=invalid on a dead link names both reasons: no longer works, or another Discord account", async () => {
    renderJoin({ valid: false }, { refused: true });
    expect(await screen.findByRole("heading", { name: "This invitation link no longer works" })).toBeInTheDocument();
    expect(screen.getByText(/or it is for a different Discord account/)).toBeInTheDocument();
  });

  it("?error=invalid on a link that still previews says so and lets the visitor try again", async () => {
    renderJoin(ACCESS, { refused: true });
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "That didn't work: the link no longer works, or it is for a different Discord account.",
    );
    fireEvent.click(screen.getByRole("checkbox"));
    expect(screen.getByRole("link", { name: "Continue with Discord" })).toHaveAttribute("href", START);
  });

  it("?error=aup_required says the acknowledgment is needed", async () => {
    renderJoin(ACCESS, { aupRequired: true });
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Please accept the Nutzungsbedingungen below before joining.",
    );
    expect(screen.getByRole("checkbox")).not.toBeChecked();
  });
});

describe("Join: the preview itself", () => {
  it("names the browser tab", async () => {
    document.title = "Glyphoxa";
    renderJoin(ACCESS);
    await screen.findByRole("heading", { name: /invited you to Glyphoxa/ });
    expect(document.title).toBe("Invitation · Glyphoxa");
  });

  it("says it could not check the link when the read fails, and retries", async () => {
    const { state } = renderJoin(ACCESS, {}, { fail: 1 });
    expect(await screen.findByRole("alert")).toHaveTextContent("Couldn't check this invitation link.");
    // Not mistaken for a dead link: the read failed, the link may be fine.
    expect(screen.queryByText("This invitation link no longer works")).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("heading", { name: "Sora Vance invited you to Glyphoxa" })).toBeInTheDocument();
    await waitFor(() => expect(state.seen).toHaveLength(2));
  });

  it("is localized", async () => {
    localStorage.setItem("gx-lang", "de");
    try {
      renderJoin(CAMPAIGN);
      expect(
        await screen.findByRole("heading", { name: "Mira hat dich zu Tides over Saltmarsh eingeladen" }),
      ).toBeInTheDocument();
      expect(screen.getByText("Karten ansehen")).toBeInTheDocument();
    } finally {
      localStorage.removeItem("gx-lang");
    }
  });
});
