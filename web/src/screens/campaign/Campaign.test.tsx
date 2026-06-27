import { describe, it, expect } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  CampaignService,
  VoiceService,
  AgentSchema,
  CampaignSchema,
  GetCampaignRosterResponseSchema,
  CreateAgentResponseSchema,
  UpdateAgentResponseSchema,
  DeleteAgentResponseSchema,
  ListVoicesResponseSchema,
  VoiceSchema,
  PreviewVoiceResponseSchema,
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

  // LIVE voice catalog the select renders (#70). It includes the NPC's persisted
  // "rachel" id so the trigger shows its live label, proving the dropdown is fed
  // by VoiceService rather than a static list.
  const liveVoices = [
    create(VoiceSchema, { provider: "elevenlabs", voiceId: "rachel", name: "Rachel", label: "ElevenLabs · Rachel" }),
    create(VoiceSchema, { provider: "elevenlabs", voiceId: "marcus", name: "Marcus", label: "ElevenLabs · Marcus" }),
  ];
  const previewCalls: string[] = [];

  const transport = createRouterTransport(({ service }) => {
    service(VoiceService, {
      listVoices: () => create(ListVoicesResponseSchema, { voices: liveVoices }),
      previewVoice: (req) => {
        previewCalls.push(req.voiceId);
        return create(PreviewVoiceResponseSchema, {
          audio: new Uint8Array([1, 2, 3, 4]),
          sampleRate: 24000,
          channels: 1,
          mimeType: "audio/wav",
        });
      },
    });
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
  return { transport, npcs, previewCalls };
}

function renderScreen() {
  const { transport, npcs, previewCalls } = mockTransport();
  render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <Campaign />
    </Providers>,
  );
  return { npcs, previewCalls };
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

  it("shows a success cue after Save changes succeeds", async () => {
    renderScreen();
    fireEvent.click(await screen.findByText("Bart"));
    fireEvent.click(screen.getByRole("button", { name: /save changes/i }));
    // A live status region confirms the persist landed.
    expect(await screen.findByText(/^saved$/i)).toBeInTheDocument();
  });

  it("surfaces an error cue when Save changes fails", async () => {
    // A transport whose UpdateAgent rejects, so the editor must show an error.
    const campaign = create(CampaignSchema, { id: "c1", name: "X", system: "5e", language: "en" });
    const butler = create(AgentSchema, { id: "b1", campaignId: "c1", role: "butler", name: "Glyphoxa", addressOnly: true });
    const npc = create(AgentSchema, { id: "n1", campaignId: "c1", role: "character", name: "Bart", voice: "rachel" });
    const transport = createRouterTransport(({ service }) => {
      service(VoiceService, { listVoices: () => create(ListVoicesResponseSchema, { voices: [] }) });
      service(CampaignService, {
        getCampaignRoster: () => create(GetCampaignRosterResponseSchema, { campaign, roster: [butler, npc] }),
        updateAgent: () => {
          throw new Error("boom");
        },
      });
    });
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <Campaign />
      </Providers>,
    );
    fireEvent.click(await screen.findByText("Bart"));
    fireEvent.click(screen.getByRole("button", { name: /save changes/i }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/couldn't save/i);
  });

  it("disables Save while in flight and cannot be double-submitted", async () => {
    // A deferred UpdateAgent holds the mutation pending until we resolve it.
    const campaign = create(CampaignSchema, { id: "c1", name: "X", system: "5e", language: "en" });
    const butler = create(AgentSchema, { id: "b1", campaignId: "c1", role: "butler", name: "Glyphoxa", addressOnly: true });
    const npc = create(AgentSchema, { id: "n1", campaignId: "c1", role: "character", name: "Bart", voice: "rachel" });
    let resolveUpdate: () => void = () => {};
    let updateCalls = 0;
    const transport = createRouterTransport(({ service }) => {
      service(VoiceService, { listVoices: () => create(ListVoicesResponseSchema, { voices: [] }) });
      service(CampaignService, {
        getCampaignRoster: () => create(GetCampaignRosterResponseSchema, { campaign, roster: [butler, npc] }),
        updateAgent: async () => {
          updateCalls += 1;
          await new Promise<void>((r) => (resolveUpdate = r));
          return create(UpdateAgentResponseSchema, { agent: npc });
        },
      });
    });
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <Campaign />
      </Providers>,
    );
    fireEvent.click(await screen.findByText("Bart"));
    const saveBtn = screen.getByRole("button", { name: /save changes/i });
    fireEvent.click(saveBtn);

    // In flight: the button reads "Saving…" and is disabled.
    const pending = await screen.findByRole("button", { name: /saving/i });
    expect(pending).toBeDisabled();
    // A second click while pending is a no-op (disabled → no extra RPC).
    fireEvent.click(pending);
    expect(updateCalls).toBe(1);

    resolveUpdate();
    expect(await screen.findByText(/^saved$/i)).toBeInTheDocument();
  });

  it("renders the voice select from the live ListVoices catalog", async () => {
    renderScreen();
    // Select Bart (persisted voice id "rachel"); the trigger shows the LIVE label
    // resolved from the catalog, not the raw id — proving the dropdown is fed by
    // VoiceService.ListVoices.
    fireEvent.click(await screen.findByText("Bart"));
    expect(await screen.findByText("ElevenLabs · Rachel")).toBeInTheDocument();
  });

  it("previews the selected voice via the PreviewVoice RPC", async () => {
    const { previewCalls } = renderScreen();
    fireEvent.click(await screen.findByText("Bart"));
    fireEvent.click(screen.getByRole("button", { name: /preview voice/i }));
    // The preview RPC fired for the selected voice (audio playback is a no-op in
    // jsdom; the RPC call is the observable behaviour).
    await waitFor(() => expect(previewCalls).toEqual(["rachel"]));
  });
});
