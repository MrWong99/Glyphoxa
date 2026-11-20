import { describe, it, expect } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  CampaignService,
  CharacterSchema,
  ListCharactersResponseSchema,
  ListDiscordVoiceMembersResponseSchema,
  UpdateCharacterResponseSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { PlayersPanel } from "./PlayersPanel";

// Two Characters, and an UpdateCharacter that can be made to fail for one.
function mount(opts: { failUpdateFor?: string } = {}) {
  const characters = [
    create(CharacterSchema, { id: "ch-1", name: "Aravel", discordUserId: "111111111111111111" }),
    create(CharacterSchema, { id: "ch-2", name: "Borin", discordUserId: "222222222222222222" }),
  ];
  const updates: { id: string; aliases: string[] }[] = [];
  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      listCharacters: () => create(ListCharactersResponseSchema, { characters }),
      listDiscordVoiceMembers: () => create(ListDiscordVoiceMembersResponseSchema, {}),
      updateCharacter: (req) => {
        updates.push({ id: req.id, aliases: req.aliases });
        if (req.id === opts.failUpdateFor) throw new ConnectError("that Discord user is taken", Code.AlreadyExists);
        const i = characters.findIndex((c) => c.id === req.id);
        characters[i] = create(CharacterSchema, {
          id: req.id,
          name: req.name,
          aliases: req.aliases,
          discordUserId: req.discordUserId,
        });
        return create(UpdateCharacterResponseSchema, { character: characters[i] });
      },
    });
  });
  render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <PlayersPanel />
    </Providers>,
  );
  return { updates };
}

const nameField = () => screen.getByLabelText("Character name") as HTMLInputElement;
const rosterRow = (name: string) => screen.getByText(name).closest("button") as HTMLButtonElement;

describe("PlayersPanel editor state", () => {
  it("does not carry one Character's 'Saved' line onto the next (W13)", async () => {
    mount();
    fireEvent.click(rosterRow(await screen.findByText("Aravel").then(() => "Aravel")));
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText("Saved")).toBeInTheDocument();

    fireEvent.click(rosterRow("Borin"));
    await waitFor(() => expect(nameField().value).toBe("Borin"));
    expect(screen.queryByText("Saved")).not.toBeInTheDocument();
  });

  it("does not carry one Character's save error onto the next (W13)", async () => {
    mount({ failUpdateFor: "ch-1" });
    await screen.findByText("Aravel");
    fireEvent.click(rosterRow("Aravel"));
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("that Discord user is taken");

    fireEvent.click(rosterRow("Borin"));
    await waitFor(() => expect(nameField().value).toBe("Borin"));
    expect(screen.queryByText(/that Discord user is taken/)).not.toBeInTheDocument();
  });

  it("saves an alias still sitting in the draft box (W15)", async () => {
    const { updates } = mount();
    await screen.findByText("Aravel");
    fireEvent.click(rosterRow("Aravel"));
    // Typed, but neither Enter nor Add pressed — straight to Save.
    fireEvent.change(screen.getByLabelText("Add alias"), { target: { value: "the ranger" } });
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => expect(updates).toEqual([{ id: "ch-1", aliases: ["the ranger"] }]));
    // It also becomes a chip, so the form shows what was sent.
    expect(await screen.findByRole("button", { name: /remove.*the ranger/i })).toBeInTheDocument();
  });

  it("asks before another Character replaces unsaved typing (W10)", async () => {
    mount();
    await screen.findByText("Aravel");
    fireEvent.click(rosterRow("Aravel"));
    fireEvent.change(nameField(), { target: { value: "Aravel the Bold" } });

    fireEvent.click(rosterRow("Borin"));
    expect(await screen.findByText("Discard unsaved changes?")).toBeInTheDocument();
    expect(nameField().value).toBe("Aravel the Bold");

    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByText("Discard unsaved changes?")).not.toBeInTheDocument());
    expect(nameField().value).toBe("Aravel the Bold");

    // "Add player" is the other path that replaces the form.
    fireEvent.click(screen.getByRole("button", { name: /add player/i }));
    fireEvent.click(await screen.findByRole("button", { name: "Discard changes" }));
    await waitFor(() => expect(nameField().value).toBe(""));
  });

  it("switches Characters without asking when nothing was typed (W10)", async () => {
    mount();
    await screen.findByText("Aravel");
    fireEvent.click(rosterRow("Aravel"));
    fireEvent.click(rosterRow("Borin"));
    await waitFor(() => expect(nameField().value).toBe("Borin"));
    expect(screen.queryByText("Discard unsaved changes?")).not.toBeInTheDocument();
  });
});
