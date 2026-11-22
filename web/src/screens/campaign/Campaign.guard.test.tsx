import { describe, it, expect } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  AgentSchema,
  CampaignSchema,
  CampaignService,
  GetActiveCampaignResponseSchema,
  GetCampaignRosterResponseSchema,
  ListCharactersResponseSchema,
  ListDiscordVoiceMembersResponseSchema,
  ListSupportedLanguagesResponseSchema,
  ListVoicesResponseSchema,
  VoiceService,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { MemoryScreenNav, useScreenNav } from "@/app/screenNav";
import { hasUnsavedEdits } from "@/lib/navigationGuard";
import { makeQueryClient } from "@/lib/queryClient";
import { Campaign } from "./Campaign";

// W10 at the HOST level: whichever view holds an editor, its unsaved edits are
// on the app-wide register the route layer reads (NavigationGuard holds a
// sidebar click or a tab close off it), and they come off again once the view
// is gone. The in-panel guards — roster click, prep chip, Add NPC — are
// CastPanel.guard.test.tsx; the route-level question is routerFallbacks.test.tsx.
function mount(view: "cast" | "settings") {
  const campaign = create(CampaignSchema, { id: "c1", name: "The Sunless Citadel", system: "5e", language: "en" });
  const transport = createRouterTransport(({ service }) => {
    service(VoiceService, { listVoices: () => create(ListVoicesResponseSchema, { voices: [] }) });
    service(CampaignService, {
      getCampaignRoster: () =>
        create(GetCampaignRosterResponseSchema, {
          campaign,
          roster: [
            create(AgentSchema, { id: "b1", campaignId: "c1", role: "butler", name: "Glyphoxa", addressOnly: true }),
            create(AgentSchema, { id: "n1", campaignId: "c1", role: "character", name: "Bart", persona: "Grumbles." }),
          ],
        }),
      getActiveCampaign: () => create(GetActiveCampaignResponseSchema, { campaign }),
      listSupportedLanguages: () => create(ListSupportedLanguagesResponseSchema, { languages: ["de", "en"] }),
      listCharacters: () => create(ListCharactersResponseSchema, {}),
      listDiscordVoiceMembers: () => create(ListDiscordVoiceMembersResponseSchema, {}),
    });
  });
  function Leave() {
    const nav = useScreenNav();
    return (
      <button type="button" onClick={() => nav.goCampaignView("players")}>
        leave for players
      </button>
    );
  }
  render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <MemoryScreenNav initial={{ screen: "campaign", view }}>
        {(loc) => (
          <>
            <Leave />
            {loc.view && <Campaign view={loc.view} />}
          </>
        )}
      </MemoryScreenNav>
    </Providers>,
  );
}

describe("Campaign host unsaved-edit register (W10)", () => {
  it("lists a dirty cast editor, and clears it when the view is left", async () => {
    mount("cast");
    await screen.findByText("Bart");
    expect(hasUnsavedEdits()).toBe(false);

    fireEvent.click(screen.getByText("Bart").closest("button") as HTMLButtonElement);
    const persona = () => screen.getByLabelText("Personality & backstory") as HTMLTextAreaElement;
    await waitFor(() => expect(persona().value).toBe("Grumbles."));
    fireEvent.change(persona(), { target: { value: "Grumbles, but secretly kind." } });
    // This flag is what NavigationGuard reads to hold a sidebar click.
    expect(hasUnsavedEdits()).toBe(true);

    // (No route layer in this harness to ask the question — it just leaves.)
    fireEvent.click(screen.getByRole("button", { name: "leave for players" }));
    await screen.findByRole("button", { name: /add player/i });
    expect(hasUnsavedEdits()).toBe(false);
  });

  it("lists a dirty campaign-settings form, and clears it when the view is left", async () => {
    mount("settings");
    const name = (await screen.findByLabelText("Name")) as HTMLInputElement;
    await waitFor(() => expect(name.value).toBe("The Sunless Citadel"));
    expect(hasUnsavedEdits()).toBe(false);

    fireEvent.change(name, { target: { value: "The Sunless Citadel, Revisited" } });
    await waitFor(() => expect(hasUnsavedEdits()).toBe(true));
    // Typing it back is not an edit any more.
    fireEvent.change(name, { target: { value: "The Sunless Citadel" } });
    await waitFor(() => expect(hasUnsavedEdits()).toBe(false));

    fireEvent.change(name, { target: { value: "Elsewhere" } });
    await waitFor(() => expect(hasUnsavedEdits()).toBe(true));
    fireEvent.click(screen.getByRole("button", { name: "leave for players" }));
    await screen.findByRole("button", { name: /add player/i });
    expect(hasUnsavedEdits()).toBe(false);
  });
});
