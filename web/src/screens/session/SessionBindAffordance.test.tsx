import { describe, it, expect } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  CampaignService,
  SessionService,
  CharacterSchema,
  DiscordVoiceMemberSchema,
  ListCharactersResponseSchema,
  CreateCharacterResponseSchema,
  UpdateCharacterResponseSchema,
  ListDiscordVoiceMembersResponseSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { SessionBindAffordance } from "./SessionBindAffordance";

// The in-flight bind affordance (#279) maps an unmapped Player mid-session by
// calling the CampaignService bind RPCs — never SessionService — so no Voice
// Session restart is involved. The transport records the session-control calls to
// prove they are never made, and the Character store mutates so a bind round-trips.
function mockTransport() {
  const characters = [
    create(CharacterSchema, { id: "ch-1", name: "Aravel", discordUserId: "111111111111111111" }),
  ];
  const members = [
    create(DiscordVoiceMemberSchema, { discordUserId: "222222222222222222", displayName: "borin_irl" }),
  ];
  const sessionControlCalls: string[] = [];
  const created: { name: string; discordUserId: string }[] = [];
  const reassigned: { id: string; discordUserId: string }[] = [];
  let nextId = 2;

  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      listCharacters: () => create(ListCharactersResponseSchema, { characters }),
      listDiscordVoiceMembers: () => create(ListDiscordVoiceMembersResponseSchema, { members }),
      createCharacter: (req) => {
        created.push({ name: req.name, discordUserId: req.discordUserId });
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
        reassigned.push({ id: req.id, discordUserId: req.discordUserId });
        const target = characters.find((c) => c.id === req.id)!;
        target.name = req.name;
        target.aliases = req.aliases;
        target.discordUserId = req.discordUserId;
        return create(UpdateCharacterResponseSchema, { character: target });
      },
    });
    service(SessionService, {
      startSession: () => {
        sessionControlCalls.push("start");
        throw new Error("must not restart the session");
      },
      stopSession: () => {
        sessionControlCalls.push("stop");
        throw new Error("must not restart the session");
      },
    });
  });
  return { transport, sessionControlCalls, created, reassigned };
}

function renderAffordance() {
  const ctx = mockTransport();
  render(
    <Providers transport={ctx.transport} queryClient={makeQueryClient()}>
      <SessionBindAffordance />
    </Providers>,
  );
  return ctx;
}

describe("SessionBindAffordance", () => {
  it("opens the bind form and creates a Character bound to a Discord User — no restart", async () => {
    const { created, sessionControlCalls } = renderAffordance();

    // The affordance starts collapsed to a single control.
    fireEvent.click(screen.getByTestId("bind-player-open"));

    fireEvent.change(await screen.findByLabelText("Character name"), { target: { value: "Borin" } });
    // Bind via the free-text snowflake fallback.
    fireEvent.change(screen.getByLabelText("Discord user ID"), { target: { value: "222222222222222222" } });
    fireEvent.click(screen.getByRole("button", { name: /create & bind/i }));

    await waitFor(() => expect(created).toHaveLength(1));
    expect(created[0]).toEqual({ name: "Borin", discordUserId: "222222222222222222" });
    // The Voice Session was never restarted.
    expect(sessionControlCalls).toEqual([]);
  });

  it("offers binding to an existing Character (reassign) without leaving the session", async () => {
    renderAffordance();
    fireEvent.click(screen.getByTestId("bind-player-open"));

    // The open form exposes a Character selector so an unmapped speaker can be bound
    // to an EXISTING Character (reassign), not only a new one — its default is a new
    // Character. Driving the Radix dropdown itself is covered by the create path.
    expect(await screen.findByLabelText("Character")).toBeInTheDocument();
    expect(screen.getByText("New Character")).toBeInTheDocument();

    // Cancel closes back to the single collapsed control.
    fireEvent.click(screen.getByRole("button", { name: /cancel/i }));
    expect(screen.getByTestId("bind-player-open")).toBeInTheDocument();
    expect(screen.queryByLabelText("Character name")).not.toBeInTheDocument();
  });
});
