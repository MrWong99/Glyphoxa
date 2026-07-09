import { describe, it, expect } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
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
function mockTransport(opts: { members?: boolean } = {}) {
  const characters = [
    create(CharacterSchema, {
      id: "ch-1",
      name: "Aravel",
      aliases: ["the ranger"],
      discordUserId: "111111111111111111",
    }),
  ];
  const members = opts.members
    ? [
        create(DiscordVoiceMemberSchema, {
          discordUserId: "111111111111111111",
          displayName: "aravel_irl",
          avatarUrl: "",
        }),
        create(DiscordVoiceMemberSchema, {
          discordUserId: "222222222222222222",
          displayName: "borin_irl",
          avatarUrl: "",
        }),
      ]
    : [];
  const updateCalls: { id: string; name: string; aliases: string[]; discordUserId: string }[] = [];
  let nextId = 2;

  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      listCharacters: () => create(ListCharactersResponseSchema, { characters }),
      listDiscordVoiceMembers: () => create(ListDiscordVoiceMembersResponseSchema, { members }),
      createCharacter: (req) => {
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
  return { transport, characters, updateCalls };
}

function renderPanel(opts: { members?: boolean } = {}) {
  const ctx = mockTransport(opts);
  render(
    <Providers transport={ctx.transport} queryClient={makeQueryClient()}>
      <PlayersPanel />
    </Providers>,
  );
  return ctx;
}

describe("PlayersPanel", () => {
  it("lists the campaign's characters with their Discord binding", async () => {
    renderPanel({ members: true });
    expect(await screen.findByText("Aravel")).toBeInTheDocument();
    // The live member's display name resolves as the subtitle, not the raw snowflake.
    expect(screen.getByText("aravel_irl")).toBeInTheDocument();
  });

  it("creates a Character bound via the voice-channel member picker", async () => {
    const { characters } = renderPanel({ members: true });
    await screen.findByText("Aravel");

    fireEvent.click(screen.getByRole("button", { name: /add player/i }));
    fireEvent.change(screen.getByLabelText("Character name"), { target: { value: "Borin" } });

    // Open the member picker and pick the unbound member.
    fireEvent.click(screen.getByRole("button", { name: /from voice/i }));
    fireEvent.click(await screen.findByRole("option", { name: /borin_irl/i }));

    fireEvent.click(screen.getByRole("button", { name: /create player/i }));

    await waitFor(() => expect(characters).toHaveLength(2));
    expect(characters[1].name).toBe("Borin");
    expect(characters[1].discordUserId).toBe("222222222222222222");
  });

  it("validates the free-text snowflake fallback as digits-only", async () => {
    renderPanel({ members: false });
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
    const { updateCalls } = renderPanel({ members: false });
    fireEvent.click(await screen.findByText("Aravel"));

    // There is no unbind affordance — discord_user_id is NOT NULL by design.
    expect(screen.queryByRole("button", { name: /unbind/i })).not.toBeInTheDocument();

    const idField = screen.getByLabelText("Discord user ID");
    expect(idField).toHaveValue("111111111111111111");
    fireEvent.change(idField, { target: { value: "999999999999999999" } });
    fireEvent.click(screen.getByRole("button", { name: /save changes/i }));

    await waitFor(() => expect(updateCalls).toHaveLength(1));
    expect(updateCalls[0]).toMatchObject({ id: "ch-1", discordUserId: "999999999999999999" });
  });

  it("saves an edited alias set through UpdateCharacter", async () => {
    const { updateCalls } = renderPanel({ members: false });
    fireEvent.click(await screen.findByText("Aravel"));

    fireEvent.change(screen.getByLabelText("Add alias"), { target: { value: "pathfinder" } });
    fireEvent.click(screen.getByRole("button", { name: /^add$/i }));
    fireEvent.click(screen.getByRole("button", { name: /save changes/i }));

    await waitFor(() => expect(updateCalls).toHaveLength(1));
    expect(updateCalls[0].aliases).toEqual(["the ranger", "pathfinder"]);
  });

  it("deletes a Character behind a confirm gate", async () => {
    const { characters } = renderPanel({ members: false });
    fireEvent.click(await screen.findByText("Aravel"));
    fireEvent.click(screen.getByRole("button", { name: /^delete$/i }));

    // The confirm dialog gates the destructive delete.
    fireEvent.click(await screen.findByRole("button", { name: /delete player/i }));
    await waitFor(() => expect(characters).toHaveLength(0));
  });
});
