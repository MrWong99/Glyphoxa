import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, within, fireEvent, waitFor } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { toast } from "sonner";

import {
  AddCampaignGuestResponseSchema,
  CampaignGuestSchema,
  CampaignService,
  CreateCampaignInvitationResponseSchema,
  DiscordVoiceMemberSchema,
  GuestRight as R,
  InvitationKind,
  InvitationSchema,
  ListCampaignAccessResponseSchema,
  ListDiscordVoiceMembersResponseSchema,
  RemoveCampaignGuestResponseSchema,
  RevokeCampaignInvitationResponseSchema,
  UpdateCampaignGuestResponseSchema,
  type AddCampaignGuestRequest,
  type CampaignGuest,
  type CreateCampaignInvitationRequest,
  type Invitation,
  type UpdateCampaignGuestRequest,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { normalizeGuestRights } from "@/lib/guestRights";
import { makeQueryClient } from "@/lib/queryClient";
import { SharingPanel } from "./SharingPanel";

// Toasts are events the view reports; the tests assert on them directly.
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }));

beforeEach(() => {
  vi.mocked(toast.success).mockClear();
  vi.mocked(toast.error).mockClear();
});

// The Sharing view against a stateful fake of the six Campaign Guest RPCs:
// every write changes what the next ListCampaignAccess answers, so a test that
// sees a row change has seen the view re-read after its write. The fake
// normalizes rights the way the server does, so a set that the editor showed
// one way and the server stored another would show up as a changed row.

const GWEN = "200000000000000001"; // a guest who has signed in
const PENDING = "200000000000000002"; // added by ID, never signed in
const BORIN = "200000000000000003"; // in the voice channel, not a guest

const guest = (init: MessageInitShape<typeof CampaignGuestSchema>): CampaignGuest =>
  create(CampaignGuestSchema, { hasSignedIn: true, addedAt: timestampFromDate(new Date()), ...init });

const link = (init: MessageInitShape<typeof InvitationSchema>): Invitation =>
  create(InvitationSchema, { kind: InvitationKind.CAMPAIGN, live: true, ...init });

type Opts = {
  guests?: CampaignGuest[];
  invitations?: Invitation[];
  /** Who is in the voice channel. Default: nobody. */
  voice?: { id: string; name: string }[];
  failAdd?: boolean;
};

function backend(opts: Opts = {}) {
  const guests =
    opts.guests ??
    [
      guest({ userId: "u-gwen", discordUserId: GWEN, name: "Gwen", rights: [R.WIKI_READ, R.MAPS_READ] }),
      guest({ userId: "u-pending", discordUserId: PENDING, hasSignedIn: false, rights: [] }),
    ];
  const invitations = opts.invitations ?? [];
  const calls = {
    add: [] as AddCampaignGuestRequest[],
    update: [] as UpdateCampaignGuestRequest[],
    remove: [] as string[],
    create: [] as CreateCampaignInvitationRequest[],
    revoke: [] as string[],
    list: 0,
  };
  let next = 1;

  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      listCampaignAccess: () => {
        calls.list++;
        return create(ListCampaignAccessResponseSchema, { guests: [...guests], invitations: [...invitations] });
      },
      listDiscordVoiceMembers: () =>
        create(ListDiscordVoiceMembersResponseSchema, {
          members: (opts.voice ?? []).map((m) =>
            create(DiscordVoiceMemberSchema, { discordUserId: m.id, displayName: m.name, avatarUrl: "" }),
          ),
        }),
      addCampaignGuest: (req) => {
        calls.add.push(req);
        if (opts.failAdd) throw new ConnectError("that is the campaign's own GM", Code.InvalidArgument);
        const added = guest({
          userId: `u-new-${next++}`,
          discordUserId: req.discordUserId,
          hasSignedIn: false,
          rights: normalizeGuestRights(req.rights),
        });
        guests.push(added);
        return create(AddCampaignGuestResponseSchema, { guest: added });
      },
      updateCampaignGuest: (req) => {
        calls.update.push(req);
        const g = guests.find((x) => x.userId === req.userId)!;
        g.rights = normalizeGuestRights(req.rights);
        return create(UpdateCampaignGuestResponseSchema, { guest: g });
      },
      removeCampaignGuest: (req) => {
        calls.remove.push(req.userId);
        guests.splice(
          guests.findIndex((x) => x.userId === req.userId),
          1,
        );
        return create(RemoveCampaignGuestResponseSchema, {});
      },
      createCampaignInvitation: (req) => {
        calls.create.push(req);
        const inv = link({ id: `inv-${next++}`, label: req.label, rights: normalizeGuestRights(req.rights), maxUses: req.maxUses });
        invitations.unshift(inv);
        return create(CreateCampaignInvitationResponseSchema, { invitation: inv, token: "tok-campaign-1" });
      },
      revokeCampaignInvitation: (req) => {
        calls.revoke.push(req.id);
        const inv = invitations.find((x) => x.id === req.id)!;
        inv.live = false;
        inv.revokedAt = timestampFromDate(new Date());
        return create(RevokeCampaignInvitationResponseSchema, {});
      },
    });
  });
  return { transport, calls };
}

function renderPanel(opts: Opts = {}) {
  const b = backend(opts);
  render(
    <Providers transport={b.transport} queryClient={makeQueryClient()}>
      <SharingPanel />
    </Providers>,
  );
  return b;
}

const section = (name: string) => screen.getByRole("region", { name });
const row = async (name: string) => screen.findByRole("listitem", { name });
/** A checkbox of the rights editor, found through its group's legend. */
const box = (scope: HTMLElement, group: string, name: string) =>
  within(within(scope).getByRole("group", { name: group })).getByRole("checkbox", { name }) as HTMLInputElement;

describe("Sharing: the guests list", () => {
  it("lists each guest with their rights, and a guest who has not signed in by Discord ID", async () => {
    renderPanel();
    const gwen = await row("Gwen");
    expect(within(gwen).getByText(`Discord ID ${GWEN}`)).toBeInTheDocument();
    const rights = within(gwen).getByLabelText("What Gwen can do");
    expect(within(rights).getByText("Read the world wiki")).toBeInTheDocument();
    expect(within(rights).getByText("View maps")).toBeInTheDocument();
    expect(within(gwen).getByText(/^Added /)).toBeInTheDocument();

    const pending = await row(PENDING);
    expect(within(pending).getByText("Has not signed in yet")).toBeInTheDocument();
    expect(within(pending).getByText("Nothing shared yet")).toBeInTheDocument();
  });

  it("says so when the campaign is shared with nobody", async () => {
    renderPanel({ guests: [] });
    expect(await screen.findByText("Not shared with anyone yet")).toBeInTheDocument();
  });

  it("removes a guest only after a confirmation that says it is immediate", async () => {
    const { calls } = renderPanel();
    fireEvent.click(within(await row("Gwen")).getByRole("button", { name: "Remove Gwen" }));

    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText("Remove Gwen from this campaign?")).toBeInTheDocument();
    expect(within(dialog).getByText(/takes effect immediately/i)).toBeInTheDocument();
    expect(calls.remove).toEqual([]);

    fireEvent.click(within(dialog).getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(calls.remove).toEqual(["u-gwen"]));
    await waitFor(() => expect(screen.queryByRole("listitem", { name: "Gwen" })).not.toBeInTheDocument());
    expect(toast.success).toHaveBeenCalledWith("Gwen can no longer open this campaign");
  });
});

describe("Sharing: the rights editor", () => {
  async function openEditor() {
    const b = renderPanel();
    fireEvent.click(within(await row("Gwen")).getByRole("button", { name: "Change what Gwen can do" }));
    const dialog = await screen.findByRole("dialog", { name: "What Gwen can do" });
    return { ...b, dialog };
  }

  it("opens on the guest's current rights, with nothing to save yet", async () => {
    const { dialog } = await openEditor();
    expect(box(dialog, "World wiki", "Read").checked).toBe(true);
    expect(box(dialog, "World wiki", "Edit").checked).toBe(false);
    expect(box(dialog, "Maps", "View").checked).toBe(true);
    expect(box(dialog, "Highlights", "View").checked).toBe(false);
    expect(within(dialog).getByRole("button", { name: "Save rights" })).toBeDisabled();
  });

  it("says what each right reveals or costs — the planning chat most of all", async () => {
    const { dialog } = await openEditor();
    expect(box(dialog, "Planning chat", "Use")).toHaveAccessibleDescription(
      /everything the Butler knows.*use your AI budget/i,
    );
    expect(box(dialog, "World wiki", "Including GM secrets")).toHaveAccessibleDescription(/marked private/i);
  });

  it("ticks and locks what a right implies, and says which right holds it", async () => {
    const { dialog } = await openEditor();

    fireEvent.click(box(dialog, "World wiki", "Edit"));
    const secrets = box(dialog, "World wiki", "Including GM secrets");
    expect(secrets.checked).toBe(true);
    expect(secrets).toBeDisabled();
    expect(box(dialog, "World wiki", "Read")).toBeDisabled();
    expect(secrets).toHaveAccessibleDescription(/Always included with: Edit the world wiki/);

    fireEvent.click(box(dialog, "Maps", "Edit pins"));
    expect(box(dialog, "Maps", "View")).toBeDisabled();

    // Clearing the holder frees the implied boxes without unticking them.
    fireEvent.click(box(dialog, "World wiki", "Edit"));
    expect(secrets.checked).toBe(true);
    expect(secrets).not.toBeDisabled();
  });

  it("the planning chat brings the wiki with its secrets along", async () => {
    const { dialog } = await openEditor();
    fireEvent.click(box(dialog, "Planning chat", "Use"));
    expect(box(dialog, "World wiki", "Including GM secrets").checked).toBe(true);
    expect(box(dialog, "World wiki", "Including GM secrets")).toBeDisabled();
    expect(box(dialog, "World wiki", "Read")).toBeDisabled();
  });

  it("applies a preset in one click and marks it as chosen", async () => {
    const { dialog } = await openEditor();
    const presets = within(dialog).getByRole("group", { name: "Start from a preset" });

    fireEvent.click(within(presets).getByRole("button", { name: /^Reader/ }));
    expect(within(presets).getByRole("button", { name: /^Reader/ })).toHaveAttribute("aria-pressed", "true");
    expect(box(dialog, "Sessions", "Read transcripts").checked).toBe(true);
    expect(box(dialog, "Highlights", "View").checked).toBe(true);
    expect(box(dialog, "World wiki", "Including GM secrets").checked).toBe(false);

    fireEvent.click(within(presets).getByRole("button", { name: /^Co-GM/ }));
    for (const b of within(dialog).getAllByRole("checkbox")) expect(b).toBeChecked();

    // A preset is a starting point: changing a box leaves no preset chosen.
    fireEvent.click(within(presets).getByRole("button", { name: /^Player/ }));
    fireEvent.click(box(dialog, "Highlights", "View"));
    expect(within(presets).queryByRole("button", { pressed: true })).not.toBeInTheDocument();
  });

  it("saves the normalized set for that guest and re-reads the list", async () => {
    const { dialog, calls } = await openEditor();
    fireEvent.click(box(dialog, "World wiki", "Edit"));
    fireEvent.click(box(dialog, "Maps", "View")); // off
    fireEvent.click(within(dialog).getByRole("button", { name: "Save rights" }));

    await waitFor(() => expect(calls.update).toHaveLength(1));
    expect(calls.update[0].userId).toBe("u-gwen");
    expect(calls.update[0].rights).toEqual([R.WIKI_READ, R.WIKI_SECRETS, R.WIKI_EDIT]);
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    const rights = within(await row("Gwen")).getByLabelText("What Gwen can do");
    await waitFor(() => expect(within(rights).getByText("Edit the world wiki")).toBeInTheDocument());
    expect(within(rights).queryByText("View maps")).not.toBeInTheDocument();
  });

  it("lets every right be taken away, and says what that leaves", async () => {
    const { dialog, calls } = await openEditor();
    fireEvent.click(box(dialog, "World wiki", "Read"));
    fireEvent.click(box(dialog, "Maps", "View"));
    expect(within(dialog).getByText(/can open nothing in it/i)).toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole("button", { name: "Save rights" }));
    await waitFor(() => expect(calls.update).toHaveLength(1));
    expect(calls.update[0].rights).toEqual([]);
  });
});

describe("Sharing: add someone", () => {
  const add = () => section("Add someone");

  it("adds a typed Discord User ID with the Player preset unless the GM changes it", async () => {
    const { calls } = renderPanel();
    await row("Gwen");
    const form = add();
    expect(within(within(form).getByRole("group", { name: "Start from a preset" })).getByRole("button", { name: /^Player/ }))
      .toHaveAttribute("aria-pressed", "true");

    fireEvent.change(within(form).getByLabelText("Discord user ID"), { target: { value: BORIN } });
    fireEvent.click(within(form).getByRole("button", { name: "Add to campaign" }));

    await waitFor(() => expect(calls.add).toHaveLength(1));
    expect(calls.add[0].discordUserId).toBe(BORIN);
    expect(calls.add[0].rights).toEqual([R.WIKI_READ, R.MAPS_READ, R.HIGHLIGHTS_READ]);
    // The new guest is in the list, and the form is ready for the next one.
    expect(await row(BORIN)).toBeInTheDocument();
    expect(within(form).getByLabelText("Discord user ID")).toHaveValue("");
  });

  it("picks someone from the voice channel instead of typing", async () => {
    const { calls } = renderPanel({ voice: [{ id: BORIN, name: "borin_irl" }, { id: GWEN, name: "gwen_irl" }] });
    await row("Gwen");
    const form = add();

    fireEvent.click(await within(form).findByRole("button", { name: "From voice" }));
    const members = within(form).getByRole("group", { name: "Voice channel members" });
    // Someone who is a guest already cannot be picked again — it would replace their rights.
    expect(within(members).getByRole("button", { name: /gwen_irl.*Already a guest/ })).toBeDisabled();
    fireEvent.click(within(members).getByRole("button", { name: /borin_irl/ }));

    expect(within(form).getByLabelText("Discord user ID")).toHaveValue(BORIN);
    expect(within(form).getByText("That is borin_irl.")).toBeInTheDocument();

    fireEvent.click(box(form, "Sessions", "Read transcripts"));
    fireEvent.click(within(form).getByRole("button", { name: "Add to campaign" }));
    await waitFor(() => expect(calls.add).toHaveLength(1));
    expect(calls.add[0].discordUserId).toBe(BORIN);
    expect(calls.add[0].rights).toEqual([R.WIKI_READ, R.MAPS_READ, R.TRANSCRIPTS_READ, R.HIGHLIGHTS_READ]);
    expect(toast.success).toHaveBeenCalledWith("borin_irl can now open this campaign");
  });

  it("refuses an ID that is not one, an existing guest, and an empty set of rights — before any call", async () => {
    const { calls } = renderPanel();
    await row("Gwen");
    const form = add();
    const id = within(form).getByLabelText("Discord user ID");
    const submit = within(form).getByRole("button", { name: "Add to campaign" });
    expect(submit).toBeDisabled(); // nothing typed yet

    fireEvent.change(id, { target: { value: "gwen#1234" } });
    expect(within(form).getByRole("alert")).toHaveTextContent("digits only");
    expect(submit).toBeDisabled();

    fireEvent.change(id, { target: { value: GWEN } });
    expect(within(form).getByRole("alert")).toHaveTextContent(/already a guest/i);
    expect(submit).toBeDisabled();

    fireEvent.change(id, { target: { value: BORIN } });
    expect(submit).not.toBeDisabled();
    for (const name of ["Read"]) fireEvent.click(box(form, "World wiki", name));
    fireEvent.click(box(form, "Maps", "View"));
    fireEvent.click(box(form, "Highlights", "View"));
    expect(within(form).getByText("Tick at least one thing to share.")).toBeInTheDocument();
    expect(submit).toBeDisabled();
    expect(calls.add).toEqual([]);
  });

  it("shows the server's reason when the add is refused, and keeps what was typed", async () => {
    renderPanel({ failAdd: true });
    await row("Gwen");
    const form = add();
    fireEvent.change(within(form).getByLabelText("Discord user ID"), { target: { value: BORIN } });
    fireEvent.click(within(form).getByRole("button", { name: "Add to campaign" }));
    expect(await within(form).findByText("that is the campaign's own GM")).toBeInTheDocument();
    expect(within(form).getByLabelText("Discord user ID")).toHaveValue(BORIN);
  });
});

describe("Sharing: invitation links", () => {
  const links = () => section("Invitation links");

  it("creates a link with rights and limits, and shows the full link exactly once", async () => {
    const { calls } = renderPanel();
    await row("Gwen");
    fireEvent.click(within(links()).getByRole("button", { name: "New invitation link" }));

    const dialog = await screen.findByRole("dialog", { name: "New invitation link" });
    fireEvent.change(within(dialog).getByLabelText("Label (optional)"), { target: { value: "Thursday group" } });
    fireEvent.click(within(within(dialog).getByRole("group", { name: "Start from a preset" })).getByRole("button", { name: /^Reader/ }));
    fireEvent.change(within(dialog).getByLabelText("Maximum uses"), { target: { value: "5" } });
    fireEvent.change(within(dialog).getByLabelText("Expires after (days)"), { target: { value: "14" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Create link" }));

    await waitFor(() => expect(calls.create).toHaveLength(1));
    expect(calls.create[0]).toMatchObject({
      label: "Thursday group",
      maxUses: 5,
      expiresInDays: 14,
      discordUserId: "",
      rights: [R.WIKI_READ, R.MAPS_READ, R.TRANSCRIPTS_READ, R.HIGHLIGHTS_READ],
    });

    // Shown once: the full link, a copy button and the warning.
    const created = await screen.findByRole("dialog", { name: "Invitation link created" });
    const field = within(created).getByLabelText("Invitation link") as HTMLInputElement;
    expect(field.value).toBe(`${window.location.origin}/join/tok-campaign-1`);
    expect(within(created).getByRole("button", { name: "Copy link" })).toBeInTheDocument();
    expect(within(created).getByText(/see this link again/i)).toBeInTheDocument();

    fireEvent.click(within(created).getByRole("button", { name: "Done" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    // The list has the link by its label and what it grants — never its token.
    const listed = await within(links()).findByRole("listitem", { name: "Thursday group" });
    expect(within(listed).getByText("Read session transcripts")).toBeInTheDocument();
    expect(document.body.textContent).not.toContain("tok-campaign-1");
  });

  it("does not mint a link that shares nothing", async () => {
    const { calls } = renderPanel();
    await row("Gwen");
    fireEvent.click(within(links()).getByRole("button", { name: "New invitation link" }));
    const dialog = await screen.findByRole("dialog", { name: "New invitation link" });
    fireEvent.click(box(dialog, "World wiki", "Read"));
    fireEvent.click(box(dialog, "Maps", "View"));
    fireEvent.click(box(dialog, "Highlights", "View"));
    expect(within(dialog).getByRole("button", { name: "Create link" })).toBeDisabled();
    expect(calls.create).toEqual([]);
  });

  it("revokes a live link after a confirmation, and keeps it listed as revoked", async () => {
    const { calls } = renderPanel({
      invitations: [link({ id: "inv-9", label: "Discord pin", rights: [R.WIKI_READ], uses: 2 })],
    });
    const listed = await within(await screen.findByRole("region", { name: "Invitation links" })).findByRole(
      "listitem",
      { name: "Discord pin" },
    );
    expect(within(listed).getByText("Active")).toBeInTheDocument();
    fireEvent.click(within(listed).getByRole("button", { name: "Revoke Discord pin" }));

    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText(/Guests who already joined through it stay/i)).toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole("button", { name: "Revoke" }));

    await waitFor(() => expect(calls.revoke).toEqual(["inv-9"]));
    await waitFor(() => expect(within(listed).getByText("Revoked")).toBeInTheDocument());
    expect(within(listed).queryByRole("button", { name: /Revoke/ })).not.toBeInTheDocument();
  });
});
