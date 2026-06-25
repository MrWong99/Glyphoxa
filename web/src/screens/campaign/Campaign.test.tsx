import { describe, it, expect } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  CampaignService,
  AgentSchema,
  CampaignSchema,
  GetCampaignRosterResponseSchema,
  CreateAgentResponseSchema,
  UpdateAgentResponseSchema,
  DeleteAgentResponseSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { Campaign } from "./Campaign";

// An in-memory campaign store served over a router transport (no network): the
// roster mutates in this closure, so a Save → invalidate → refetch proves the
// edit round-trips and reloads identically (the #71 acceptance), and the Butler
// invariants are exercised against the same handlers the live screen calls.
function mockTransport() {
  const campaign = create(CampaignSchema, {
    id: "c1",
    name: "The Sunless Citadel",
    system: "D&D 5e",
    language: "en",
  });
  const butler = create(AgentSchema, {
    id: "butler-1",
    campaignId: "c1",
    role: "butler",
    name: "Glyphoxa",
    title: "",
    addressOnly: true,
    speakerColor: 0,
  });
  const npcs = [
    create(AgentSchema, {
      id: "npc-1",
      campaignId: "c1",
      role: "character",
      name: "Bart",
      title: "Gruff innkeeper",
      persona: "Grumbles.",
      voice: "rachel",
      addressOnly: false,
      speakerColor: 0,
    }),
  ];
  let nextId = 2;

  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      getCampaignRoster: () =>
        create(GetCampaignRosterResponseSchema, { campaign, roster: [butler, ...npcs] }),
      createAgent: (req) => {
        const agent = create(AgentSchema, {
          id: `npc-${nextId++}`,
          campaignId: "c1",
          role: "character",
          name: req.name,
          title: req.title,
          persona: req.persona,
          voice: req.voice,
          addressOnly: req.addressOnly,
          speakerColor: npcs.length % 6,
        });
        npcs.push(agent);
        return create(CreateAgentResponseSchema, { agent });
      },
      updateAgent: (req) => {
        const target = req.id === butler.id ? butler : npcs.find((n) => n.id === req.id);
        if (!target) throw new Error("not found");
        target.name = req.name;
        target.title = req.title;
        target.persona = req.persona;
        target.voice = req.voice;
        // The Butler stays Address-Only no matter what the client asks (server rule).
        target.addressOnly = target.role === "butler" ? true : req.addressOnly;
        return create(UpdateAgentResponseSchema, { agent: target });
      },
      deleteAgent: (req) => {
        const i = npcs.findIndex((n) => n.id === req.id);
        if (i >= 0) npcs.splice(i, 1);
        return create(DeleteAgentResponseSchema, {});
      },
    });
  });
  return { transport, npcs };
}

function renderScreen() {
  const { transport, npcs } = mockTransport();
  render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <Campaign />
    </Providers>,
  );
  return { npcs };
}

describe("Campaign", () => {
  it("renders the live campaign title and roster", async () => {
    renderScreen();
    expect(await screen.findByRole("heading", { name: "The Sunless Citadel" })).toBeInTheDocument();
    // Both the Butler and the NPC appear in the roster.
    expect(screen.getByText("Glyphoxa")).toBeInTheDocument();
    expect(screen.getByText("Bart")).toBeInTheDocument();
  });

  it("locks the Butler: Address-Only is forced on and its switch is disabled", async () => {
    renderScreen();
    // Select the Butler.
    fireEvent.click(await screen.findByText("Glyphoxa"));
    const sw = screen.getByLabelText(/address only/i);
    expect(sw).toBeDisabled();
    expect(sw).toBeChecked();
    // The Butler is not deletable.
    expect(screen.queryByRole("button", { name: /delete npc/i })).not.toBeInTheDocument();
  });

  it("round-trips an NPC edit to the store and reloads it identically", async () => {
    const { npcs } = renderScreen();
    // Select the NPC and rename it.
    fireEvent.click(await screen.findByText("Bart"));
    const name = screen.getByLabelText("Name") as HTMLInputElement;
    expect(name.value).toBe("Bart");
    fireEvent.change(name, { target: { value: "Bartholomew" } });
    fireEvent.click(screen.getByRole("button", { name: /save changes/i }));

    // The edit persisted to the store…
    expect(await screen.findByText("Bartholomew")).toBeInTheDocument();
    expect(npcs[0].name).toBe("Bartholomew");
    // …and the old name is gone (the roster re-read the store).
    expect(screen.queryByText("Bart")).not.toBeInTheDocument();
  });

  it("adds an NPC and deletes it, keeping the store in sync", async () => {
    const { npcs } = renderScreen();
    await screen.findByText("Bart");
    expect(npcs).toHaveLength(1);

    fireEvent.click(screen.getByRole("button", { name: /add npc/i }));
    expect(await screen.findByText("New NPC")).toBeInTheDocument();
    expect(npcs).toHaveLength(2);

    // Delete the freshly added NPC (it is auto-selected after creation).
    fireEvent.click(screen.getByRole("button", { name: /delete npc/i }));
    // The new NPC leaves the roster once the store re-reads; the original stays.
    await waitFor(() => expect(screen.queryByText("New NPC")).not.toBeInTheDocument());
    expect(npcs).toHaveLength(1);
    expect(screen.getByText("Bart")).toBeInTheDocument();
  });
});
