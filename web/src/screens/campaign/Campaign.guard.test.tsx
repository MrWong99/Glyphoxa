import { describe, it, expect } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  AgentSchema,
  CampaignSchema,
  CampaignService,
  CreateAgentResponseSchema,
  GetCampaignRosterResponseSchema,
  ListVoicesResponseSchema,
  VoiceService,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { hasUnsavedEdits } from "@/lib/navigationGuard";
import { makeQueryClient } from "@/lib/queryClient";
import { Campaign } from "./Campaign";

// W10: the agent editor's unsaved text was only guarded on a roster click. The
// prep chip and "Add NPC" (whose success selects the new NPC) unmounted or
// re-seeded it without a word, and a sidebar click had nothing to ask at all.
function mount() {
  const roster = [
    create(AgentSchema, { id: "b1", campaignId: "c1", role: "butler", name: "Glyphoxa", addressOnly: true }),
    create(AgentSchema, { id: "n1", campaignId: "c1", role: "character", name: "Bart", persona: "Grumbles." }),
    create(AgentSchema, { id: "n2", campaignId: "c1", role: "character", name: "Mira", persona: "Schemes." }),
  ];
  const created: string[] = [];
  const transport = createRouterTransport(({ service }) => {
    service(VoiceService, { listVoices: () => create(ListVoicesResponseSchema, { voices: [] }) });
    service(CampaignService, {
      getCampaignRoster: () =>
        create(GetCampaignRosterResponseSchema, {
          campaign: create(CampaignSchema, { id: "c1", name: "The Sunless Citadel" }),
          roster,
        }),
      createAgent: (req) => {
        created.push(req.name);
        const agent = create(AgentSchema, { id: `n${roster.length}`, campaignId: "c1", role: "character", name: req.name });
        roster.push(agent);
        return create(CreateAgentResponseSchema, { agent });
      },
    });
  });
  render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <Campaign view="cast" />
    </Providers>,
  );
  return { created };
}

const persona = () => screen.getByLabelText("Personality & backstory") as HTMLTextAreaElement;
const rosterRow = (name: string) => screen.getByText(name).closest("button") as HTMLButtonElement;

async function editBart() {
  await screen.findByText("Bart");
  fireEvent.click(rosterRow("Bart"));
  await waitFor(() => expect(persona().value).toBe("Grumbles."));
  fireEvent.change(persona(), { target: { value: "Grumbles, but secretly kind." } });
}

describe("Campaign agent editor unsaved-edit guard (W10)", () => {
  it("asks before another roster member replaces the editor", async () => {
    mount();
    await editBart();
    fireEvent.click(rosterRow("Mira"));

    expect(await screen.findByText("Discard unsaved changes?")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByText("Discard unsaved changes?")).not.toBeInTheDocument());
    expect(persona().value).toBe("Grumbles, but secretly kind.");

    fireEvent.click(rosterRow("Mira"));
    fireEvent.click(await screen.findByRole("button", { name: "Discard changes" }));
    await waitFor(() => expect(persona().value).toBe("Schemes."));
  });

  it("asks before the Session prep view unmounts the editor", async () => {
    mount();
    await editBart();
    const prep = screen.getByRole("button", { name: "Session prep" });
    fireEvent.click(prep);

    expect(await screen.findByText("Discard unsaved changes?")).toBeInTheDocument();
    // Still on the editor while the question is open.
    expect(prep).toHaveAttribute("aria-pressed", "false");
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByText("Discard unsaved changes?")).not.toBeInTheDocument());
    expect(persona().value).toBe("Grumbles, but secretly kind.");

    fireEvent.click(prep);
    fireEvent.click(await screen.findByRole("button", { name: "Discard changes" }));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "Session prep" })).toHaveAttribute("aria-pressed", "true"),
    );
  });

  it("asks before Add NPC creates one and takes the editor away", async () => {
    const { created } = mount();
    await editBart();
    fireEvent.click(screen.getByRole("button", { name: /add npc/i }));

    expect(await screen.findByText("Discard unsaved changes?")).toBeInTheDocument();
    // Nothing is created until the GM has answered.
    expect(created).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByText("Discard unsaved changes?")).not.toBeInTheDocument());
    expect(created).toEqual([]);
    expect(persona().value).toBe("Grumbles, but secretly kind.");

    fireEvent.click(screen.getByRole("button", { name: /add npc/i }));
    fireEvent.click(await screen.findByRole("button", { name: "Discard changes" }));
    await waitFor(() => expect(created).toHaveLength(1));
  });

  it("reports the unsaved edit to the route layer, and clears it once the editor is gone", async () => {
    mount();
    await screen.findByText("Bart");
    expect(hasUnsavedEdits()).toBe(false);
    await editBart();
    // This flag is what NavigationGuard reads to hold a sidebar click.
    expect(hasUnsavedEdits()).toBe(true);

    fireEvent.click(screen.getByRole("button", { name: "Session prep" }));
    fireEvent.click(await screen.findByRole("button", { name: "Discard changes" }));
    await waitFor(() => expect(hasUnsavedEdits()).toBe(false));
  });

  it("moves freely when nothing was edited", async () => {
    mount();
    await screen.findByText("Bart");
    fireEvent.click(rosterRow("Bart"));
    await waitFor(() => expect(persona().value).toBe("Grumbles."));
    fireEvent.click(rosterRow("Mira"));
    await waitFor(() => expect(persona().value).toBe("Schemes."));
    expect(screen.queryByText("Discard unsaved changes?")).not.toBeInTheDocument();
  });
});
