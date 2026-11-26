import { useState } from "react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

vi.mock("sonner", () => ({
  toast: Object.assign(vi.fn(), { error: vi.fn(), success: vi.fn(), warning: vi.fn(), dismiss: vi.fn() }),
  Toaster: () => null,
}));

import {
  CampaignService,
  CharacterSchema,
  DiscordVoiceMemberSchema,
  ListCharactersResponseSchema,
  CreateCharacterResponseSchema,
  ListDiscordVoiceMembersResponseSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { SessionBindAffordance, type BindPrefill } from "./SessionBindAffordance";
import { UnlinkedVoiceNotice } from "./UnlinkedVoiceNotice";

// "N people in voice are not linked": read once when the session connects,
// each person offered to the bind form prefilled, or set aside as not a player.

const BOUND = "111111111111111111";
const BORIN = "222222222222222222";
const GM = "333333333333333333";

function mockTransport() {
  const characters = [create(CharacterSchema, { id: "ch-1", name: "Aravel", discordUserId: BOUND })];
  const calls = { members: 0 };
  const created: { name: string; discordUserId: string }[] = [];
  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      listCharacters: () => create(ListCharactersResponseSchema, { characters: [...characters] }),
      listDiscordVoiceMembers: () => {
        calls.members++;
        return create(ListDiscordVoiceMembersResponseSchema, {
          members: [
            create(DiscordVoiceMemberSchema, { discordUserId: BOUND, displayName: "aravel_irl" }),
            create(DiscordVoiceMemberSchema, { discordUserId: BORIN, displayName: "borin_irl" }),
            create(DiscordVoiceMemberSchema, { discordUserId: GM, displayName: "the_gm" }),
          ],
        });
      },
      createCharacter: (req) => {
        created.push({ name: req.name, discordUserId: req.discordUserId });
        const character = create(CharacterSchema, { id: "ch-2", name: req.name, discordUserId: req.discordUserId });
        characters.push(character);
        return create(CreateCharacterResponseSchema, { character });
      },
    });
  });
  return { transport, calls, created };
}

// The notice and the bind form wired the way the Session screen wires them.
function Harness({ connected }: { connected: boolean }) {
  const [open, setOpen] = useState(false);
  const [prefill, setPrefill] = useState<BindPrefill | null>(null);
  return (
    <>
      <UnlinkedVoiceNotice
        sessionId="vs1"
        campaignId="c1"
        connected={connected}
        onLink={(person) => {
          setPrefill(person);
          setOpen(true);
        }}
      />
      <SessionBindAffordance open={open} prefill={prefill} onOpenChange={setOpen} />
    </>
  );
}

function renderNotice(connected = true) {
  const ctx = mockTransport();
  const queryClient = makeQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  const view = render(
    <Providers transport={ctx.transport} queryClient={queryClient}>
      <Harness connected={connected} />
    </Providers>,
  );
  return { ...ctx, view, queryClient };
}

describe("UnlinkedVoiceNotice", () => {
  beforeEach(() => {
    localStorage.clear();
    sessionStorage.clear();
  });

  it("asks Discord nothing until the Bot is in the channel", async () => {
    const { calls } = renderNotice(false);
    await new Promise((r) => setTimeout(r, 20));
    expect(calls.members).toBe(0);
    expect(screen.queryByTestId("unlinked-voice")).not.toBeInTheDocument();
  });

  it("lists the people in voice with no Character, leaving out the linked ones", async () => {
    renderNotice();
    expect(await screen.findByText("2 people in voice are not linked to a Character")).toBeInTheDocument();
    const list = screen.getByTestId("unlinked-voice");
    expect(within(list).getByText("borin_irl")).toBeInTheDocument();
    expect(within(list).getByText("the_gm")).toBeInTheDocument();
    expect(within(list).queryByText("aravel_irl")).not.toBeInTheDocument();
  });

  it("opens the bind form prefilled with the person, and linking them takes them off the notice", async () => {
    const { created } = renderNotice();
    fireEvent.click(await screen.findByRole("button", { name: "Link borin_irl to a Character" }));

    // The display name seeds the Character's name; the binding is already made.
    expect(await screen.findByLabelText("Character name")).toHaveValue("borin_irl");
    expect(screen.getByLabelText("Discord user ID")).toHaveValue(BORIN);
    fireEvent.click(screen.getByRole("button", { name: /create & link/i }));

    await waitFor(() => expect(created).toEqual([{ name: "borin_irl", discordUserId: BORIN }]));
    expect(await screen.findByText("1 person in voice is not linked to a Character")).toBeInTheDocument();
    expect(within(screen.getByTestId("unlinked-voice")).queryByText("borin_irl")).not.toBeInTheDocument();
  });

  it("sets aside someone who is not a player, for the campaign, in this browser", async () => {
    const first = renderNotice();
    fireEvent.click(await screen.findByRole("button", { name: "the_gm is not a player — don't ask again" }));
    await waitFor(() => expect(screen.getByText("1 person in voice is not linked to a Character")).toBeInTheDocument());
    first.view.unmount();

    // Next session: still set aside.
    renderNotice();
    expect(await screen.findByText("1 person in voice is not linked to a Character")).toBeInTheDocument();
    expect(screen.queryByText("the_gm")).not.toBeInTheDocument();
  });

  it("can be dismissed for the rest of the session", async () => {
    const first = renderNotice();
    await screen.findByTestId("unlinked-voice");
    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(screen.queryByTestId("unlinked-voice")).not.toBeInTheDocument();
    first.view.unmount();

    // Coming back to the same session does not raise it again.
    renderNotice();
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByTestId("unlinked-voice")).not.toBeInTheDocument();
  });
});
