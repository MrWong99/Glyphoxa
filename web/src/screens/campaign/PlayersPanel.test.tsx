import { describe, it, expect } from "vitest";
import { act, render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  CampaignService,
  CharacterSchema,
  DiscordVoiceMemberSchema,
  ListCharactersResponseSchema,
  CreateCharacterResponseSchema,
  UpdateCharacterResponseSchema,
  DeleteCharacterResponseSchema,
  ListDiscordVoiceMembersResponseSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { PlayersPanel } from "./PlayersPanel";

// An in-memory Character store served over a router transport (no network),
// mirroring the #276 CampaignService semantics: create/update/delete mutate the
// closure so a save → invalidate → refetch proves the bind round-trips (#279 AC).
// The live voice-channel member picker is fed by ListDiscordVoiceMembers.

const ARAVEL_ID = "111111111111111111";
const BORIN_ID = "222222222222222222";

type Opts = {
  /** Who is in the voice channel right now. Default: nobody (or the Bot is offline). */
  members?: ("aravel" | "borin")[];
  /** Start with no Characters at all. */
  empty?: boolean;
  /** Aravel's player has signed in with Discord. */
  linked?: boolean;
  /** ListCharacters fails until this is flipped. */
  failList?: { on: boolean };
};

function mockTransport(opts: Opts = {}) {
  const characters = opts.empty
    ? []
    : [
        create(CharacterSchema, {
          id: "ch-1",
          name: "Aravel",
          aliases: ["the ranger"],
          discordUserId: ARAVEL_ID,
          linkedUserId: opts.linked ? "user-1" : "",
        }),
      ];
  const everyone = {
    aravel: create(DiscordVoiceMemberSchema, { discordUserId: ARAVEL_ID, displayName: "aravel_irl", avatarUrl: "" }),
    borin: create(DiscordVoiceMemberSchema, { discordUserId: BORIN_ID, displayName: "borin_irl", avatarUrl: "" }),
  };
  const members = (opts.members ?? []).map((who) => everyone[who]);
  const updateCalls: { id: string; name: string; aliases: string[]; discordUserId: string }[] = [];
  const createCalls: { name: string; discordUserId: string }[] = [];
  let nextId = 2;

  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      listCharacters: () => {
        if (opts.failList?.on) throw new ConnectError("db is down", Code.Unavailable);
        return create(ListCharactersResponseSchema, { characters });
      },
      listDiscordVoiceMembers: () => create(ListDiscordVoiceMembersResponseSchema, { members }),
      createCharacter: (req) => {
        createCalls.push({ name: req.name, discordUserId: req.discordUserId });
        const character = create(CharacterSchema, {
          id: `ch-${nextId++}`,
          name: req.name,
          aliases: req.aliases,
          discordUserId: req.discordUserId,
        });
        characters.push(character);
        return create(CreateCharacterResponseSchema, { character });
      },
      updateCharacter: (req) => {
        updateCalls.push({ id: req.id, name: req.name, aliases: req.aliases, discordUserId: req.discordUserId });
        const target = characters.find((c) => c.id === req.id);
        if (!target) throw new Error("not found");
        target.name = req.name;
        target.aliases = req.aliases;
        target.discordUserId = req.discordUserId;
        return create(UpdateCharacterResponseSchema, { character: target });
      },
      deleteCharacter: (req) => {
        const i = characters.findIndex((c) => c.id === req.id);
        if (i >= 0) characters.splice(i, 1);
        return create(DeleteCharacterResponseSchema, {});
      },
    });
  });
  return { transport, characters, updateCalls, createCalls };
}

function renderPanel(opts: Opts = {}) {
  const ctx = mockTransport(opts);
  const queryClient = makeQueryClient();
  if (opts.failList) queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <Providers transport={ctx.transport} queryClient={queryClient}>
      <PlayersPanel />
    </Providers>,
  );
  return ctx;
}

/** The roster row (the <li>) of a Character. */
const rowOf = (name: string) => screen.getByText(name).closest("li") as HTMLElement;

describe("PlayersPanel list", () => {
  it("leads each row with the Character and names the player from the voice channel", async () => {
    renderPanel({ members: ["aravel"] });
    const row = rowOf(await screen.findByText("Aravel").then(() => "Aravel"));
    // The avatar is the CHARACTER's initial, not the Discord account's.
    expect(row.querySelector(".gx-avatar")).toHaveTextContent("A");
    // The live member's display name resolves, with where they are.
    expect(within(row).getByText("aravel_irl")).toBeInTheDocument();
    expect(row).toHaveTextContent("aravel_irl · in voice");
  });

  it("with the voice channel empty, shows the character with a masked id instead of a bare snowflake", async () => {
    renderPanel({ members: [] });
    const row = rowOf(await screen.findByText("Aravel").then(() => "Aravel"));
    expect(row.querySelector(".gx-avatar")).toHaveTextContent("A");
    expect(row).toHaveTextContent("Discord user …1111 · not in voice");
    // The raw 18-digit id is in the editor, not in the list.
    expect(row).not.toHaveTextContent(ARAVEL_ID);
  });

  it("explains its badges on keyboard focus", async () => {
    renderPanel({ members: [] });
    const row = rowOf(await screen.findByText("Aravel").then(() => "Aravel"));
    const badge = within(row).getByText("Discord ID").closest("[tabindex]") as HTMLElement;
    // Beside the row's button, not inside it: it has to take focus on its own.
    expect(badge.closest("button")).toBeNull();
    act(() => badge.focus());
    expect(screen.getByRole("tooltip")).toHaveTextContent(/Known only by their Discord user ID/);
  });

  it("explains the Linked badge too", async () => {
    renderPanel({ members: [], linked: true });
    const row = rowOf(await screen.findByText("Aravel").then(() => "Aravel"));
    const badge = within(row).getByText("Linked").closest("[tabindex]") as HTMLElement;
    act(() => badge.focus());
    expect(screen.getByRole("tooltip")).toHaveTextContent(/signed in to Glyphoxa with their Discord account/);
  });

  it("an empty campaign says what players are for and offers the one action", async () => {
    const { characters } = renderPanel({ empty: true });
    expect(await screen.findByRole("heading", { name: "No players yet" })).toBeInTheDocument();
    expect(screen.getByText(/The transcript then names who is speaking/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Add player" }));
    fireEvent.change(screen.getByLabelText("Character name"), { target: { value: "Nyx" } });
    fireEvent.change(screen.getByLabelText("Discord user ID"), { target: { value: "333333333333333333" } });
    fireEvent.click(screen.getByRole("button", { name: "Create player" }));
    await waitFor(() => expect(characters).toHaveLength(1));
    expect(await screen.findByText("Discord user …3333")).toBeInTheDocument();
  });

  it("a failed load offers a retry that works", async () => {
    const failList = { on: true };
    renderPanel({ failList });
    expect(await screen.findByText("Couldn't load your players")).toBeInTheDocument();
    failList.on = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Aravel")).toBeInTheDocument();
  });
});

describe("PlayersPanel: in voice, not yet linked", () => {
  it("lists a voice member without a character and creates one prefilled with them", async () => {
    const { characters, createCalls } = renderPanel({ members: ["aravel", "borin"] });
    await screen.findByText("Aravel");

    const section = screen.getByRole("region", { name: "In voice, not yet linked" });
    // Borin is in the channel with no character; Aravel is bound, so not here.
    expect(within(section).getByText("borin_irl")).toBeInTheDocument();
    expect(within(section).queryByText("aravel_irl")).toBeNull();

    fireEvent.click(within(section).getByRole("button", { name: "Create character for borin_irl" }));
    // The form opens already bound to that member — the GM only names the character.
    expect(screen.getByLabelText("Discord user ID")).toHaveValue(BORIN_ID);
    expect(screen.getByText("Links to borin_irl.")).toBeInTheDocument();
    expect(screen.getByText("A character for borin_irl")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Character name"), { target: { value: "Borin" } });
    fireEvent.click(screen.getByRole("button", { name: "Create player" }));

    await waitFor(() => expect(characters).toHaveLength(2));
    expect(createCalls).toEqual([{ name: "Borin", discordUserId: BORIN_ID }]);
    // Linked now: out of the section, into the roster.
    await waitFor(() => expect(screen.queryByRole("region", { name: "In voice, not yet linked" })).toBeNull());
    expect(rowOf("Borin")).toHaveTextContent("borin_irl · in voice");
  });

  it("has no such section when everyone in voice has a character", async () => {
    renderPanel({ members: ["aravel"] });
    await screen.findByText("Aravel");
    expect(screen.queryByRole("region", { name: "In voice, not yet linked" })).toBeNull();
  });

  it("shows up beside the empty state too", async () => {
    renderPanel({ empty: true, members: ["borin"] });
    expect(await screen.findByRole("heading", { name: "No players yet" })).toBeInTheDocument();
    expect(
      within(await screen.findByRole("region", { name: "In voice, not yet linked" })).getByText("borin_irl"),
    ).toBeInTheDocument();
  });
});

describe("PlayersPanel bind form", () => {
  it("creates a Character bound via the voice-channel member picker", async () => {
    const { characters } = renderPanel({ members: ["aravel", "borin"] });
    await screen.findByText("Aravel");

    fireEvent.click(screen.getByRole("button", { name: /add player/i }));
    fireEvent.change(screen.getByLabelText("Character name"), { target: { value: "Borin" } });

    // Open the member picker and pick the unbound member.
    fireEvent.click(screen.getByRole("button", { name: /from voice/i }));
    // Plain buttons in a labelled group (the CampaignSwitcher precedent) — the
    // picker does not announce a listbox model it doesn't implement.
    const picker = await screen.findByRole("group", { name: "Voice channel members" });
    fireEvent.click(within(picker).getByRole("button", { name: /borin_irl/i }));

    fireEvent.click(screen.getByRole("button", { name: /create player/i }));

    await waitFor(() => expect(characters).toHaveLength(2));
    expect(characters[1].name).toBe("Borin");
    expect(characters[1].discordUserId).toBe(BORIN_ID);
  });

  it("marks and disables members who already have a character", async () => {
    renderPanel({ members: ["aravel", "borin"] });
    await screen.findByText("Aravel");
    fireEvent.click(screen.getByRole("button", { name: /add player/i }));
    fireEvent.click(screen.getByRole("button", { name: /from voice/i }));

    const picker = await screen.findByRole("group", { name: "Voice channel members" });
    const taken = within(picker).getByRole("button", { name: /aravel_irl/ });
    expect(taken).toBeDisabled();
    expect(taken).toHaveTextContent("plays Aravel");
    expect(within(picker).getByRole("button", { name: /borin_irl/ })).toBeEnabled();
  });

  it("refuses a typed id that is already bound, before submit, naming the character", async () => {
    const { createCalls } = renderPanel({ members: [] });
    await screen.findByText("Aravel");
    fireEvent.click(screen.getByRole("button", { name: /add player/i }));
    fireEvent.change(screen.getByLabelText("Character name"), { target: { value: "Impostor" } });

    const idField = screen.getByLabelText("Discord user ID");
    fireEvent.change(idField, { target: { value: ARAVEL_ID } });
    expect(screen.getByRole("alert")).toHaveTextContent("This Discord user already plays Aravel.");
    expect(idField).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByRole("button", { name: /create player/i })).toBeDisabled();
    // Enter does not sneak past the disabled button either.
    fireEvent.keyDown(idField, { key: "Enter" });
    expect(createCalls).toEqual([]);

    fireEvent.change(idField, { target: { value: "333333333333333333" } });
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.getByRole("button", { name: /create player/i })).toBeEnabled();
  });

  it("does not call a Character's own Discord user 'taken' while editing it", async () => {
    renderPanel({ members: ["aravel"] });
    fireEvent.click(await screen.findByText("Aravel"));
    expect(screen.getByLabelText("Discord user ID")).toHaveValue(ARAVEL_ID);
    expect(screen.queryByText(/already plays/)).toBeNull();
    expect(screen.getByRole("button", { name: /save changes/i })).toBeEnabled();

    // Its own member stays pickable in the picker.
    fireEvent.click(screen.getByRole("button", { name: /from voice/i }));
    const picker = await screen.findByRole("group", { name: "Voice channel members" });
    expect(within(picker).getByRole("button", { name: /aravel_irl/ })).toBeEnabled();
  });

  it("says how to find a Discord user ID", async () => {
    renderPanel({ members: [] });
    await screen.findByText("Aravel");
    fireEvent.click(screen.getByRole("button", { name: /add player/i }));
    expect(screen.getByText("Where do I find a Discord user ID?")).toBeInTheDocument();
    expect(screen.getByText(/turn on Developer Mode/)).toBeInTheDocument();
    expect(screen.getByText(/choose Copy User ID/)).toBeInTheDocument();
    // Nobody in voice: the hint does not point at a picker that is not there.
    expect(screen.getByText(/Paste the player's Discord user ID/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /from voice/i })).toBeNull();
  });

  it("validates the free-text snowflake fallback as digits-only", async () => {
    renderPanel({ members: [] });
    await screen.findByText("Aravel");

    fireEvent.click(screen.getByRole("button", { name: /add player/i }));
    fireEvent.change(screen.getByLabelText("Character name"), { target: { value: "Nyx" } });

    const idField = screen.getByLabelText("Discord user ID");
    // Non-digits are rejected: an error shows and Create stays disabled.
    fireEvent.change(idField, { target: { value: "not-a-snowflake" } });
    expect(screen.getByText(/digits only/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /create player/i })).toBeDisabled();

    // A digits-only id clears the error and enables Create.
    fireEvent.change(idField, { target: { value: "333333333333333333" } });
    expect(screen.queryByText(/digits only/i)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /create player/i })).toBeEnabled();
  });

  it("reassigns a Character to a different Discord User (no unbind control)", async () => {
    const { updateCalls } = renderPanel({ members: [] });
    fireEvent.click(await screen.findByText("Aravel"));

    // There is no unbind affordance — discord_user_id is NOT NULL by design.
    expect(screen.queryByRole("button", { name: /unbind/i })).not.toBeInTheDocument();

    const idField = screen.getByLabelText("Discord user ID");
    expect(idField).toHaveValue(ARAVEL_ID);
    fireEvent.change(idField, { target: { value: "999999999999999999" } });
    fireEvent.click(screen.getByRole("button", { name: /save changes/i }));

    await waitFor(() => expect(updateCalls).toHaveLength(1));
    expect(updateCalls[0]).toMatchObject({ id: "ch-1", discordUserId: "999999999999999999" });
  });

  it("saves an edited alias set through UpdateCharacter", async () => {
    const { updateCalls } = renderPanel({ members: [] });
    fireEvent.click(await screen.findByText("Aravel"));

    fireEvent.change(screen.getByLabelText("Add alias"), { target: { value: "pathfinder" } });
    fireEvent.click(screen.getByRole("button", { name: /^add$/i }));
    fireEvent.click(screen.getByRole("button", { name: /save changes/i }));

    await waitFor(() => expect(updateCalls).toHaveLength(1));
    expect(updateCalls[0].aliases).toEqual(["the ranger", "pathfinder"]);
  });

  it("deletes a Character behind a confirm gate", async () => {
    const { characters } = renderPanel({ members: [] });
    fireEvent.click(await screen.findByText("Aravel"));
    fireEvent.click(screen.getByRole("button", { name: /^delete$/i }));

    // The confirm dialog gates the destructive delete.
    fireEvent.click(await screen.findByRole("button", { name: /delete player/i }));
    await waitFor(() => expect(characters).toHaveLength(0));
    // Back to the empty state, not an empty column.
    expect(await screen.findByRole("heading", { name: "No players yet" })).toBeInTheDocument();
  });
});
