import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, within, fireEvent, waitFor } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { toast } from "sonner";

import {
  AccessPersonSchema,
  AccessStatus,
  AdminService,
  AdmissionMode,
  CreateAccessInvitationResponseSchema,
  GetAccessOverviewResponseSchema,
  GrantAccessResponseSchema,
  InvitationKind,
  InvitationSchema,
  RevokeAccessInvitationResponseSchema,
  RevokeAccessResponseSchema,
  SetTenantPlatformKeysResponseSchema,
  SetUserSuspendedResponseSchema,
  type AccessPerson,
  type CreateAccessInvitationRequest,
  type GrantAccessRequest,
  type Invitation,
  type SetTenantPlatformKeysRequest,
  type SetUserSuspendedRequest,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { Admin } from "./Admin";

// Toasts are events the screen reports; the tests assert on them directly.
vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() } }));

beforeEach(() => {
  vi.mocked(toast.success).mockClear();
  vi.mocked(toast.error).mockClear();
});

// The Admin screen against a stateful fake AdminService: every write changes
// what the next GetAccessOverview answers, so a test that sees the row change
// has seen the screen re-read after its write — not an optimistic guess.

const DAY = 24 * 60 * 60 * 1000;

const person = (init: MessageInitShape<typeof AccessPersonSchema>): AccessPerson =>
  create(AccessPersonSchema, { hasSignedIn: true, ...init });

const link = (init: MessageInitShape<typeof InvitationSchema>): Invitation =>
  create(InvitationSchema, { kind: InvitationKind.ACCESS, ...init });

// One of each kind of row the screen has to tell apart.
const SORA = "100000000000000001"; // the Platform Admin looking at the screen
const MIRA = "100000000000000002"; // holds an Access Grant, runs a table
const PENDING = "100000000000000003"; // granted by ID, never signed in
const ODO = "100000000000000004"; // access was revoked
const GWEN = "100000000000000005"; // a Campaign Guest only
const RIN = "100000000000000006"; // self-signup from when the instance was open

function people(): AccessPerson[] {
  return [
    person({
      discordUserId: SORA,
      name: "Sora Vance",
      status: AccessStatus.PLATFORM_ADMIN,
      isSelf: true,
      tenantId: "t-sora",
      tenantName: "Sora's Table",
      campaignCount: 3,
      platformKeys: true,
      lastSeenAt: timestampFromDate(new Date()),
    }),
    person({
      discordUserId: MIRA,
      name: "Mira",
      status: AccessStatus.GRANTED,
      note: "Thursday group",
      tenantId: "t-mira",
      tenantName: "Mira's Table",
      campaignCount: 1,
      platformKeys: false,
      lastSeenAt: timestampFromDate(new Date(Date.now() - 3 * DAY)),
    }),
    person({ discordUserId: PENDING, status: AccessStatus.GRANTED, hasSignedIn: false, platformKeys: false }),
    person({
      discordUserId: ODO,
      name: "Odo",
      status: AccessStatus.NONE,
      tenantId: "t-odo",
      tenantName: "Odo's Table",
      campaignCount: 2,
      platformKeys: true,
    }),
    person({ discordUserId: GWEN, name: "Gwen", status: AccessStatus.GUEST, guestCampaignCount: 2 }),
    person({
      discordUserId: RIN,
      name: "Rin",
      status: AccessStatus.SIGNUP,
      tenantId: "t-rin",
      tenantName: "Rin's Table",
      campaignCount: 0,
    }),
  ];
}

function links(): Invitation[] {
  return [
    link({ id: "i-live", label: "Con booth", maxUses: 5, uses: 2, live: true, platformKeys: true }),
    link({
      id: "i-expired",
      label: "Spring newsletter",
      uses: 4,
      expiresAt: timestampFromDate(new Date(Date.now() - DAY)),
    }),
    link({ id: "i-used", label: "For Kell", maxUses: 1, uses: 1, discordUserId: "100000000000000099" }),
    link({ id: "i-revoked", label: "Leaked", revokedAt: timestampFromDate(new Date(Date.now() - DAY)) }),
  ];
}

type Opts = {
  mode?: AdmissionMode;
  people?: AccessPerson[];
  links?: Invitation[];
  /** Fail the next N overview reads. */
  failReads?: number;
  /** Make every write fail with this message. */
  writeError?: string;
  /** The token CreateAccessInvitation hands back. */
  token?: string;
};

function backend(opts: Opts = {}) {
  const state = {
    people: opts.people ?? people(),
    links: opts.links ?? links(),
    reads: 0,
    failReads: opts.failReads ?? 0,
    grants: [] as GrantAccessRequest[],
    revokes: [] as string[],
    suspensions: [] as SetUserSuspendedRequest[],
    tenantKeys: [] as SetTenantPlatformKeysRequest[],
    created: [] as CreateAccessInvitationRequest[],
    revokedLinks: [] as string[],
  };
  const refuse = () => {
    if (opts.writeError) throw new ConnectError(opts.writeError, Code.FailedPrecondition);
  };
  const byId = (id: string) => state.people.filter((p) => p.discordUserId === id);
  const transport = createRouterTransport(({ service }) => {
    service(AdminService, {
      getAccessOverview: () => {
        state.reads++;
        if (state.failReads > 0) {
          state.failReads--;
          throw new ConnectError("overview down", Code.Unavailable);
        }
        return create(GetAccessOverviewResponseSchema, {
          admissionMode: opts.mode ?? AdmissionMode.ALLOWLIST,
          people: state.people,
          invitations: state.links,
        });
      },
      grantAccess: (req) => {
        refuse();
        state.grants.push(req);
        const rows = byId(req.discordUserId);
        if (rows.length === 0) {
          state.people = [
            ...state.people,
            person({
              discordUserId: req.discordUserId,
              status: AccessStatus.GRANTED,
              hasSignedIn: false,
              note: req.note,
              platformKeys: req.platformKeys,
            }),
          ];
        }
        for (const p of rows) {
          p.status = AccessStatus.GRANTED;
          if (!p.tenantId) p.platformKeys = req.platformKeys;
        }
        return create(GrantAccessResponseSchema, {});
      },
      revokeAccess: (req) => {
        refuse();
        state.revokes.push(req.discordUserId);
        for (const p of byId(req.discordUserId)) p.status = AccessStatus.NONE;
        return create(RevokeAccessResponseSchema, {});
      },
      setUserSuspended: (req) => {
        refuse();
        state.suspensions.push(req);
        for (const p of byId(req.discordUserId)) p.suspended = req.suspended;
        return create(SetUserSuspendedResponseSchema, {});
      },
      setTenantPlatformKeys: (req) => {
        refuse();
        state.tenantKeys.push(req);
        for (const p of state.people) if (p.tenantId === req.tenantId) p.platformKeys = req.enabled;
        return create(SetTenantPlatformKeysResponseSchema, {});
      },
      createAccessInvitation: (req) => {
        refuse();
        state.created.push(req);
        const invitation = link({ id: "i-new", label: req.label, maxUses: req.maxUses, live: true });
        state.links = [invitation, ...state.links];
        return create(CreateAccessInvitationResponseSchema, {
          invitation,
          token: opts.token ?? "tok-moonlit-harbor",
        });
      },
      revokeAccessInvitation: (req) => {
        refuse();
        state.revokedLinks.push(req.id);
        for (const l of state.links) {
          if (l.id === req.id) {
            l.live = false;
            l.revokedAt = timestampFromDate(new Date());
          }
        }
        return create(RevokeAccessInvitationResponseSchema, {});
      },
    });
  });
  return { transport, state };
}

async function renderAdmin(opts: Opts = {}) {
  const b = backend(opts);
  render(
    <Providers transport={b.transport} queryClient={makeQueryClient()}>
      <Admin />
    </Providers>,
  );
  await screen.findByRole("heading", { level: 2, name: "People" });
  return b;
}

const section = (name: string) => screen.getByRole("region", { name });
const row = (name: string) => within(section("People")).getByRole("listitem", { name });
const linkRow = (name: string) => within(section("Invitation links")).getByRole("listitem", { name });

describe("Admin: loading, failing, and the Admission Mode line", () => {
  it("shows a skeleton while the one read is out, then the screen", async () => {
    const b = backend();
    render(
      <Providers transport={b.transport} queryClient={makeQueryClient()}>
        <Admin />
      </Providers>,
    );
    expect(screen.getByRole("heading", { level: 1, name: "Admin" })).toBeInTheDocument();
    expect(screen.getByRole("status", { name: "Loading people and invitation links" })).toBeInTheDocument();
    expect(await screen.findByRole("heading", { level: 2, name: "People" })).toBeInTheDocument();
    // One read feeds People AND Invitation links.
    expect(screen.getByRole("heading", { level: 2, name: "Invitation links" })).toBeInTheDocument();
    expect(b.state.reads).toBe(1);
  });

  it("says the read failed, offers a retry, and recovers", async () => {
    const b = backend({ failReads: 4 });
    const queryClient = makeQueryClient();
    queryClient.setDefaultOptions({ queries: { retry: false } });
    render(
      <Providers transport={b.transport} queryClient={queryClient}>
        <Admin />
      </Providers>,
    );
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Couldn't load who has access");
    expect(screen.queryByRole("heading", { level: 2, name: "People" })).not.toBeInTheDocument();

    b.state.failReads = 0;
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("heading", { level: 2, name: "People" })).toBeInTheDocument();
  });

  it("states an invite-only instance in plain words", async () => {
    await renderAdmin({ mode: AdmissionMode.ALLOWLIST });
    expect(screen.getByRole("note")).toHaveTextContent(/This instance is invite-only/);
    expect(screen.getByRole("note")).not.toHaveTextContent(/open sign-up/);
  });

  it("states open sign-up, and that grants and links do not gate sign-in there", async () => {
    await renderAdmin({ mode: AdmissionMode.OPEN });
    const note = screen.getByRole("note");
    expect(note).toHaveTextContent(/This instance has open sign-up/);
    expect(note).toHaveTextContent(/Access Grants and invitation links do not gate sign-in here/);
  });
});

describe("Admin: People", () => {
  it("lists one row per person with who they are, how they are admitted and what they run", async () => {
    await renderAdmin();

    const mira = row("Mira");
    expect(within(mira).getByText("Access granted")).toBeInTheDocument();
    expect(within(mira).getByText(`Discord ID ${MIRA}`)).toBeInTheDocument();
    expect(within(mira).getByText("Thursday group")).toBeInTheDocument();
    expect(within(mira).getByText("Mira's Table · 1 campaign")).toBeInTheDocument();
    expect(within(mira).getByText("3 days ago")).toBeInTheDocument();

    expect(within(row("Odo")).getByText("No access")).toBeInTheDocument();
    expect(within(row("Rin")).getByText("Signed up")).toBeInTheDocument();
    expect(within(row("Rin")).getByText("Rin's Table · 0 campaigns")).toBeInTheDocument();

    const gwen = row("Gwen");
    expect(within(gwen).getByText("Campaign guest")).toBeInTheDocument();
    expect(within(gwen).getByText("Guest in 2 campaigns")).toBeInTheDocument();
    expect(within(gwen).getByText("Never")).toBeInTheDocument();
  });

  it("names a grant nobody has used yet by its Discord ID", async () => {
    await renderAdmin();
    const pending = row(PENDING);
    expect(within(pending).getByText("Has not signed in yet")).toBeInTheDocument();
    expect(within(pending).getByText("No table yet")).toBeInTheDocument();
    // Nobody to suspend yet: Revoke is the tool for an unused grant.
    expect(within(pending).getByRole("button", { name: "Revoke access" })).toBeInTheDocument();
    expect(within(pending).queryByRole("button", { name: "Suspend" })).not.toBeInTheDocument();
  });

  it("keeps a Platform Admin row read-only and says where it is configured", async () => {
    await renderAdmin();
    const sora = row("Sora Vance");
    expect(within(sora).getByText("Platform Admin")).toBeInTheDocument();
    expect(within(sora).getByText("You")).toBeInTheDocument();
    expect(within(sora).queryByRole("button")).not.toBeInTheDocument();
    const keys = within(sora).getByRole("switch", { name: "Platform Keys for Sora Vance" });
    expect(keys).toBeDisabled();
    expect(keys).toHaveAccessibleDescription(/set in the server environment \(GLYPHOXA_OPERATOR_IDS\)/);
  });

  it("offers each row only the actions that make sense for it", async () => {
    await renderAdmin();
    const buttons = (name: string) =>
      within(row(name))
        .queryAllByRole("button")
        .map((b) => b.textContent);
    expect(buttons("Mira")).toEqual(["Revoke access", "Suspend"]);
    expect(buttons("Odo")).toEqual(["Grant access", "Suspend"]);
    // Invite-only: a self-signup or a guest cannot found/keep a table without a grant.
    expect(buttons("Rin")).toEqual(["Grant access", "Suspend"]);
    expect(buttons("Gwen")).toEqual(["Grant access", "Suspend"]);
  });

  it("does not offer a grant to people open sign-up already admits", async () => {
    await renderAdmin({ mode: AdmissionMode.OPEN });
    expect(within(row("Rin")).queryByRole("button", { name: "Grant access" })).not.toBeInTheDocument();
    expect(within(row("Gwen")).queryByRole("button", { name: "Grant access" })).not.toBeInTheDocument();
    expect(within(row("Rin")).getByRole("button", { name: "Suspend" })).toBeInTheDocument();
  });

  it("revokes access only after a confirmation that says what happens", async () => {
    const { state } = await renderAdmin();
    fireEvent.click(within(row("Mira")).getByRole("button", { name: "Revoke access" }));

    const dialog = await screen.findByRole("alertdialog", { name: "Revoke Mira's access?" });
    expect(dialog).toHaveTextContent(
      "They are signed out everywhere and can't sign in again. Their table and its data are kept, and you can grant access again at any time.",
    );
    expect(state.revokes).toEqual([]);

    fireEvent.click(within(dialog).getByRole("button", { name: "Revoke access" }));
    await waitFor(() => expect(state.revokes).toEqual([MIRA]));
    // The row is the server's after the re-read.
    expect(await within(row("Mira")).findByText("No access")).toBeInTheDocument();
    expect(toast.success).toHaveBeenCalledWith("Access revoked for Mira");
  });

  it("revokes nothing when the confirmation is cancelled", async () => {
    const { state } = await renderAdmin();
    fireEvent.click(within(row("Mira")).getByRole("button", { name: "Revoke access" }));
    const dialog = await screen.findByRole("alertdialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
    expect(state.revokes).toEqual([]);
  });

  it("grants access from a row, keeping the row's Platform Keys value", async () => {
    const { state } = await renderAdmin();
    fireEvent.click(within(row("Odo")).getByRole("button", { name: "Grant access" }));
    await waitFor(() => expect(state.grants).toHaveLength(1));
    expect(state.grants[0]).toMatchObject({ discordUserId: ODO, platformKeys: true });
    expect(await within(row("Odo")).findByText("Access granted")).toBeInTheDocument();
    expect(toast.success).toHaveBeenCalledWith("Access granted to Odo");
  });

  it("suspends after a confirmation, and restores with one click", async () => {
    const { state } = await renderAdmin();
    fireEvent.click(within(row("Rin")).getByRole("button", { name: "Suspend" }));
    const dialog = await screen.findByRole("alertdialog", { name: "Suspend Rin?" });
    expect(dialog).toHaveTextContent(/refused on their next request and at sign-in until you restore them/);
    fireEvent.click(within(dialog).getByRole("button", { name: "Suspend" }));

    await waitFor(() => expect(state.suspensions).toHaveLength(1));
    expect(state.suspensions[0]).toMatchObject({ discordUserId: RIN, suspended: true });
    expect(await within(row("Rin")).findByText("Suspended")).toBeInTheDocument();

    fireEvent.click(within(row("Rin")).getByRole("button", { name: "Restore" }));
    await waitFor(() => expect(state.suspensions).toHaveLength(2));
    expect(state.suspensions[1]).toMatchObject({ discordUserId: RIN, suspended: false });
    await waitFor(() => expect(within(row("Rin")).queryByText("Suspended")).not.toBeInTheDocument());
  });

  it("switches Platform Keys on a table that exists through SetTenantPlatformKeys", async () => {
    const { state } = await renderAdmin();
    const keys = within(row("Mira")).getByRole("switch", { name: "Platform Keys for Mira" });
    expect(keys).not.toBeChecked();
    expect(keys).toHaveAccessibleDescription("Brings their own keys");

    fireEvent.click(keys);
    await waitFor(() => expect(state.tenantKeys).toHaveLength(1));
    expect(state.tenantKeys[0]).toMatchObject({ tenantId: "t-mira", enabled: true });
    expect(state.grants).toEqual([]);
    await waitFor(() =>
      expect(within(row("Mira")).getByRole("switch", { name: "Platform Keys for Mira" })).toBeChecked(),
    );
    expect(toast.success).toHaveBeenCalledWith("Mira may now use the platform's API keys");
  });

  it("switches Platform Keys on an unused grant by granting again — there is no table yet", async () => {
    const { state } = await renderAdmin();
    fireEvent.click(within(row(PENDING)).getByRole("switch"));
    await waitFor(() => expect(state.grants).toHaveLength(1));
    expect(state.grants[0]).toMatchObject({ discordUserId: PENDING, platformKeys: true });
    expect(state.tenantKeys).toEqual([]);
  });

  it("disables Platform Keys with the reason for someone with no table and no grant", async () => {
    await renderAdmin();
    const keys = within(row("Gwen")).getByRole("switch", { name: "Platform Keys for Gwen" });
    expect(keys).toBeDisabled();
    expect(keys).toHaveAccessibleDescription("No table and no Access Grant to switch this for.");
  });

  it("disables Platform Keys with the reason on an open sign-up instance", async () => {
    await renderAdmin({ mode: AdmissionMode.OPEN });
    const keys = within(row("Mira")).getByRole("switch", { name: "Platform Keys for Mira" });
    expect(keys).toBeDisabled();
    expect(keys).toHaveAccessibleDescription("With open sign-up, the plan decides who uses the platform's keys.");
  });

  it("reports a refused write as a toast and leaves the row as the server has it", async () => {
    const { state } = await renderAdmin({ writeError: "this deployment admits by plan" });
    fireEvent.click(within(row("Mira")).getByRole("switch"));
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith("That didn't work: this deployment admits by plan"),
    );
    expect(state.tenantKeys).toEqual([]);
    expect(within(row("Mira")).getByRole("switch")).not.toBeChecked();
    expect(toast.success).not.toHaveBeenCalled();
  });

  it("has an empty state", async () => {
    await renderAdmin({ people: [] });
    expect(within(section("People")).getByText("Nobody here yet")).toBeInTheDocument();
  });
});

describe("Admin: Add a person", () => {
  const form = () => within(section("Add a person"));
  const idField = () => form().getByLabelText("Discord User ID");
  const submit = () => form().getByRole("button", { name: "Grant access" });

  it("explains how to copy a Discord User ID", async () => {
    await renderAdmin();
    expect(idField()).toHaveAccessibleDescription(/turn on Developer Mode .* Copy User ID/);
  });

  it("validates the ID inline: digits only, of a snowflake's length", async () => {
    const { state } = await renderAdmin();
    expect(submit()).toBeDisabled();

    fireEvent.change(idField(), { target: { value: "mira#1234" } });
    expect(idField()).toBeInvalid();
    expect(idField()).toHaveAccessibleDescription("A Discord User ID is digits only.");
    expect(submit()).toBeDisabled();

    fireEvent.change(idField(), { target: { value: "12345" } });
    expect(idField()).toHaveAccessibleDescription("A Discord User ID has 17 to 20 digits.");
    expect(submit()).toBeDisabled();

    fireEvent.change(idField(), { target: { value: "100000000000000042" } });
    expect(idField()).not.toBeInvalid();
    expect(submit()).toBeEnabled();
    expect(state.grants).toEqual([]);
  });

  it("grants with Platform Keys OFF unless the admin turns them on", async () => {
    const { state } = await renderAdmin();
    const keys = form().getByRole("switch", { name: "May use the platform's API keys" });
    expect(keys).not.toBeChecked();
    expect(keys).toHaveAccessibleDescription(/run on this instance's keys — and on its bill/);

    fireEvent.change(idField(), { target: { value: " 100000000000000042 " } });
    fireEvent.change(form().getByLabelText("Note (optional)"), { target: { value: "Kell, from the con" } });
    fireEvent.click(submit());

    await waitFor(() => expect(state.grants).toHaveLength(1));
    expect(state.grants[0]).toMatchObject({
      discordUserId: "100000000000000042",
      note: "Kell, from the con",
      platformKeys: false,
    });
    // The new grant is in the list, and the form is ready for the next one.
    expect(await within(section("People")).findByRole("listitem", { name: "100000000000000042" })).toBeInTheDocument();
    expect(idField()).toHaveValue("");
    expect(toast.success).toHaveBeenCalledWith("Access granted to 100000000000000042");
  });

  it("sends Platform Keys on when the admin switched them on", async () => {
    const { state } = await renderAdmin();
    fireEvent.change(idField(), { target: { value: "100000000000000042" } });
    fireEvent.click(form().getByRole("switch", { name: "May use the platform's API keys" }));
    fireEvent.click(submit());
    await waitFor(() => expect(state.grants).toHaveLength(1));
    expect(state.grants[0]).toMatchObject({ platformKeys: true });
  });

  // GrantAccess is an upsert: re-adding a grant holder would blank their note
  // and force Platform Keys to this form's default on their live table.
  it("refuses an ID that already holds a grant, pointing at their row instead of overwriting it", async () => {
    const { state } = await renderAdmin();
    fireEvent.change(idField(), { target: { value: ` ${MIRA} ` } });
    expect(idField()).toBeInvalid();
    expect(idField()).toHaveAccessibleDescription(
      "Mira already has access. Change their Platform Keys, or revoke access, on their row in the list above.",
    );
    expect(submit()).toBeDisabled();
    fireEvent.submit(submit().closest("form") as HTMLFormElement);
    expect(state.grants).toEqual([]);

    // A grant nobody has used yet has no name: it is called by its ID.
    fireEvent.change(idField(), { target: { value: PENDING } });
    expect(idField()).toHaveAccessibleDescription(new RegExp(`^${PENDING} already has access\\.`));
    expect(submit()).toBeDisabled();
  });

  it("refuses a Platform Admin's ID and says where Platform Admins are set", async () => {
    const { state } = await renderAdmin();
    fireEvent.change(idField(), { target: { value: SORA } });
    expect(idField()).toBeInvalid();
    expect(idField()).toHaveAccessibleDescription(
      "Sora Vance is a Platform Admin. Platform Admins are set in the server environment (GLYPHOXA_OPERATOR_IDS) and always have access.",
    );
    expect(submit()).toBeDisabled();
    fireEvent.submit(submit().closest("form") as HTMLFormElement);
    expect(state.grants).toEqual([]);
  });

  it("still adds someone the list knows but who holds no grant: revoked, a guest, a self-signup", async () => {
    const { state } = await renderAdmin();
    for (const id of [ODO, GWEN, RIN]) {
      fireEvent.change(idField(), { target: { value: id } });
      expect(idField()).not.toBeInvalid();
      expect(submit()).toBeEnabled();
    }
    fireEvent.click(submit());
    await waitFor(() => expect(state.grants).toHaveLength(1));
    expect(state.grants[0]).toMatchObject({ discordUserId: RIN });
  });

  it("shows a refused grant in the form and keeps what was typed", async () => {
    await renderAdmin({ writeError: "discord_user_id must be decimal digits" });
    fireEvent.change(idField(), { target: { value: "100000000000000042" } });
    fireEvent.click(submit());
    const alert = await form().findByRole("alert");
    expect(alert).toHaveTextContent("Couldn't grant access");
    expect(alert).toHaveTextContent("discord_user_id must be decimal digits");
    expect(idField()).toHaveValue("100000000000000042");
  });
});

describe("Admin: Invitation links", () => {
  const openCreate = async () => {
    fireEvent.click(within(section("Invitation links")).getByRole("button", { name: "New invitation link" }));
    return screen.findByRole("dialog", { name: "New invitation link" });
  };

  it("lists each link with its uses, expiry and state", async () => {
    await renderAdmin();

    const live = linkRow("Con booth");
    expect(within(live).getByText("Active")).toBeInTheDocument();
    expect(within(live).getByText("2 of 5 uses")).toBeInTheDocument();
    expect(within(live).getByText("Never expires")).toBeInTheDocument();
    expect(within(live).getByText("Includes Platform Keys")).toBeInTheDocument();

    const expired = linkRow("Spring newsletter");
    expect(within(expired).getByText("Expired")).toBeInTheDocument();
    expect(within(expired).getByText("4 uses · unlimited")).toBeInTheDocument();
    expect(within(expired).getByText(/^Expires /)).toBeInTheDocument();

    const used = linkRow("For Kell");
    expect(within(used).getByText("Used up")).toBeInTheDocument();
    expect(within(used).getByText("Only for Discord ID 100000000000000099")).toBeInTheDocument();

    expect(within(linkRow("Leaked")).getByText("Revoked")).toBeInTheDocument();
  });

  it("offers Revoke only on a link that still works", async () => {
    await renderAdmin();
    expect(within(linkRow("Con booth")).getByRole("button", { name: "Revoke Con booth" })).toBeInTheDocument();
    for (const dead of ["Spring newsletter", "For Kell", "Leaked"]) {
      expect(within(linkRow(dead)).queryByRole("button")).not.toBeInTheDocument();
    }
  });

  it("revokes a link after a confirmation that says who keeps access", async () => {
    const { state } = await renderAdmin();
    fireEvent.click(within(linkRow("Con booth")).getByRole("button", { name: "Revoke Con booth" }));
    const dialog = await screen.findByRole("alertdialog", { name: "Revoke this invitation link?" });
    expect(dialog).toHaveTextContent(/People who already joined through it keep their access/);
    expect(state.revokedLinks).toEqual([]);

    fireEvent.click(within(dialog).getByRole("button", { name: "Revoke" }));
    await waitFor(() => expect(state.revokedLinks).toEqual(["i-live"]));
    expect(await within(linkRow("Con booth")).findByText("Revoked")).toBeInTheDocument();
    expect(toast.success).toHaveBeenCalledWith("Invitation link revoked");
  });

  it("creates a link with the limits the admin typed", async () => {
    const { state } = await renderAdmin();
    const dialog = await openCreate();
    const d = within(dialog);

    fireEvent.change(d.getByLabelText("Label (optional)"), { target: { value: "Thursday group" } });
    fireEvent.change(d.getByLabelText("Maximum uses"), { target: { value: "4" } });
    fireEvent.change(d.getByLabelText("Expires after (days)"), { target: { value: "14" } });
    fireEvent.click(d.getByRole("switch", { name: "May use the platform's API keys" }));
    fireEvent.change(d.getByLabelText("Only for this Discord User ID (optional)"), {
      target: { value: "100000000000000042" },
    });
    fireEvent.click(d.getByRole("button", { name: "Create link" }));

    await waitFor(() => expect(state.created).toHaveLength(1));
    expect(state.created[0]).toMatchObject({
      label: "Thursday group",
      maxUses: 4,
      expiresInDays: 14,
      platformKeys: true,
      discordUserId: "100000000000000042",
    });
  });

  it("sends 'unlimited' and 'never' as the wire's zero, with Platform Keys off by default", async () => {
    const { state } = await renderAdmin();
    const dialog = await openCreate();
    const d = within(dialog);
    expect(d.getByLabelText("Maximum uses")).toHaveAttribute("placeholder", "Unlimited");
    expect(d.getByLabelText("Expires after (days)")).toHaveAttribute("placeholder", "Never");
    fireEvent.click(d.getByRole("button", { name: "Create link" }));

    await waitFor(() => expect(state.created).toHaveLength(1));
    expect(state.created[0]).toMatchObject({
      label: "",
      maxUses: 0,
      expiresInDays: 0,
      platformKeys: false,
      discordUserId: "",
    });
  });

  it("refuses limits that are not whole numbers in range before the server sees them", async () => {
    const { state } = await renderAdmin();
    const dialog = await openCreate();
    const d = within(dialog);
    const create = d.getByRole("button", { name: "Create link" });

    fireEvent.change(d.getByLabelText("Maximum uses"), { target: { value: "0" } });
    expect(d.getByLabelText("Maximum uses")).toHaveAccessibleDescription(
      "Enter a whole number of 1 or more, or leave it empty.",
    );
    expect(create).toBeDisabled();
    fireEvent.change(d.getByLabelText("Maximum uses"), { target: { value: "" } });
    expect(create).toBeEnabled();

    fireEvent.change(d.getByLabelText("Expires after (days)"), { target: { value: "366" } });
    expect(d.getByLabelText("Expires after (days)")).toHaveAccessibleDescription(
      "Enter 1 to 365 days, or leave it empty.",
    );
    expect(create).toBeDisabled();
    fireEvent.change(d.getByLabelText("Expires after (days)"), { target: { value: "365" } });
    expect(create).toBeEnabled();

    fireEvent.change(d.getByLabelText("Only for this Discord User ID (optional)"), { target: { value: "kell" } });
    expect(create).toBeDisabled();
    expect(state.created).toEqual([]);
  });

  it("shows the full link once, built from the token, and never again", async () => {
    await renderAdmin({ token: "tok-moonlit-harbor" });
    const dialog = await openCreate();
    fireEvent.click(within(dialog).getByRole("button", { name: "Create link" }));

    const created = await screen.findByRole("dialog", { name: "Invitation link created" });
    const expected = `${window.location.origin}/join/tok-moonlit-harbor`;
    expect(within(created).getByLabelText("Invitation link")).toHaveValue(expected);
    expect(within(created).getByLabelText("Invitation link")).toHaveAttribute("readonly");
    expect(within(created).getByRole("button", { name: "Copy link" })).toBeInTheDocument();
    expect(within(created).getByRole("alert")).toHaveTextContent(
      "you will not be able to see this link again",
    );

    fireEvent.click(within(created).getByRole("button", { name: "Done" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    // The new link is listed — by its label and state, without the token.
    expect(await within(section("Invitation links")).findByRole("listitem", { name: "Untitled link" })).toBeInTheDocument();
    expect(document.body.innerHTML).not.toContain("tok-moonlit-harbor");

    // Opening the dialog again starts a NEW link; the old one is not held anywhere.
    const again = await openCreate();
    expect(within(again).queryByLabelText("Invitation link")).not.toBeInTheDocument();
    expect(document.body.innerHTML).not.toContain("tok-moonlit-harbor");
  });

  it("copies the link to the clipboard", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
    try {
      await renderAdmin({ token: "tok-moonlit-harbor" });
      const dialog = await openCreate();
      fireEvent.click(within(dialog).getByRole("button", { name: "Create link" }));
      const created = await screen.findByRole("dialog", { name: "Invitation link created" });
      fireEvent.click(within(created).getByRole("button", { name: "Copy link" }));
      await waitFor(() =>
        expect(writeText).toHaveBeenCalledWith(`${window.location.origin}/join/tok-moonlit-harbor`),
      );
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("shows a refused create in the dialog", async () => {
    await renderAdmin({ writeError: "expires_in_days must be at most 365" });
    const dialog = await openCreate();
    fireEvent.click(within(dialog).getByRole("button", { name: "Create link" }));
    const alert = await within(dialog).findByRole("alert");
    expect(alert).toHaveTextContent("Couldn't create the link");
    expect(alert).toHaveTextContent("expires_in_days must be at most 365");
  });

  it("has an empty state that offers the first link", async () => {
    await renderAdmin({ links: [] });
    const links = within(section("Invitation links"));
    expect(links.getByText("No invitation links yet")).toBeInTheDocument();
    expect(links.getByRole("button", { name: "New invitation link" })).toBeInTheDocument();
  });
});
