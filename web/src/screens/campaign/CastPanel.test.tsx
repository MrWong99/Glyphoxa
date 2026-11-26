import { describe, it, expect, vi } from "vitest";
import { act, render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
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
  ToolGrantSchema,
  ListToolGrantsResponseSchema,
  UpdateToolGrantResponseSchema,
  GeneratePersonaResponseSchema,
  GetKnowledgeGraphResponseSchema,
  GetRosterReadinessResponseSchema,
  NodeType,
} from "@gen/glyphoxa/management/v1/management_pb";
import type { Agent } from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { MemoryScreenNav, useScreenNav, type ScreenLocation } from "@/app/screenNav";
import { makeQueryClient } from "@/lib/queryClient";
import { CampaignScreenProvider, type CampaignScreen } from "./campaignContext";
import { CastPanel } from "./CastPanel";

type AgentInit = Parameters<typeof create<typeof AgentSchema>>[1];
type GraphInit = Parameters<typeof create<typeof GetKnowledgeGraphResponseSchema>>[1];
type ReadinessInit = Parameters<typeof create<typeof GetRosterReadinessResponseSchema>>[1];

const BART: AgentInit = {
  id: "npc-1",
  campaignId: "c1",
  role: "character",
  name: "Bart",
  title: "Gruff innkeeper",
  persona: "Grumbles.",
  voice: "rachel",
  addressOnly: false,
  speakerColor: 0,
};

// An in-memory campaign store served over a router transport (no network): the
// roster mutates in this closure, so a Save → invalidate → refetch proves the
// edit round-trips and reloads identically (the #71 acceptance), and the Butler
// invariants are exercised against the same handlers the live screen calls.
function mockTransport(
  opts: {
    npcs?: AgentInit[];
    /** The Knowledge Graph and readiness reads behind the roster markers. */
    graph?: GraphInit;
    readiness?: ReadinessInit;
  } = {},
) {
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
  const npcs: Agent[] = (opts.npcs ?? [BART]).map((init) => create(AgentSchema, init));
  let nextId = npcs.length + 1;

  // LIVE voice catalog the select renders (#70). It includes the NPC's persisted
  // "rachel" id so the trigger shows its live label, proving the dropdown is fed
  // by VoiceService rather than a static list.
  const liveVoices = [
    create(VoiceSchema, { provider: "elevenlabs", voiceId: "rachel", name: "Rachel", language: "en", label: "ElevenLabs · Rachel" }),
    create(VoiceSchema, { provider: "elevenlabs", voiceId: "marcus", name: "Marcus", language: "en", label: "ElevenLabs · Marcus" }),
  ];
  const previewCalls: string[] = [];
  const createCalls: { name: string; title: string; persona: string; voice: string }[] = [];
  const draftCalls: { agentId: string; prompt: string; name?: string; title?: string }[] = [];

  // Per-agent Tool grant state (#117). The catalog is dice-only (matching the
  // server's built-in Registry): the Butler comes granted, Bart ungranted. An
  // unknown agent (a freshly added NPC) defaults to dice-ungranted so the section
  // always renders the catalog. dice supports no scope, so no scope editor shows.
  const grants: Record<string, { granted: boolean; config: string }> = {
    "butler-1": { granted: true, config: "" },
    "npc-1": { granted: false, config: "" },
  };
  const grantEntry = (agentId: string) => {
    const state = grants[agentId] ?? { granted: false, config: "" };
    return create(ToolGrantSchema, {
      toolName: "dice",
      description: "Roll dice.",
      granted: state.granted,
      config: state.config,
      supportsScope: false,
    });
  };

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
        createCalls.push({ name: req.name, title: req.title, persona: req.persona, voice: req.voice });
        const agent = create(AgentSchema, {
          id: `npc-${nextId++}`,
          campaignId: "c1",
          role: "character",
          name: req.name,
          title: req.title,
          persona: req.persona,
          voice: req.voice,
          addressOnly: req.addressOnly,
          aliases: req.aliases,
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
        target.aliases = [...req.aliases];
        // The Butler stays Address-Only no matter what the client asks (server rule).
        target.addressOnly = target.role === "butler" ? true : req.addressOnly;
        return create(UpdateAgentResponseSchema, { agent: create(AgentSchema, { ...target }) });
      },
      deleteAgent: (req) => {
        const i = npcs.findIndex((n) => n.id === req.id);
        if (i >= 0) npcs.splice(i, 1);
        return create(DeleteAgentResponseSchema, {});
      },
      generatePersona: (req) => {
        draftCalls.push({ agentId: req.agentId, prompt: req.prompt, name: req.name, title: req.title });
        return create(GeneratePersonaResponseSchema, { persona: `**You are ${req.name ?? "?"}**` });
      },
      listToolGrants: (req) =>
        create(ListToolGrantsResponseSchema, { grants: [grantEntry(req.agentId)] }),
      updateToolGrant: (req) => {
        grants[req.agentId] = { granted: req.granted, config: req.granted ? req.config : "" };
        return create(UpdateToolGrantResponseSchema, { grant: grantEntry(req.agentId) });
      },
      // The readiness reads (#544) behind the roster markers and the Readiness
      // view. The default is a world where Bart is ready.
      getKnowledgeGraph: () =>
        create(
          GetKnowledgeGraphResponseSchema,
          opts.graph ?? {
            nodes: [{ id: "node-bart", nodeType: NodeType.NPC, name: "Bart", agentId: "npc-1", bodyLen: 40 }],
            edges: [],
          },
        ),
      getRosterReadiness: () =>
        create(
          GetRosterReadinessResponseSchema,
          opts.readiness ?? {
            agents: [{ agentId: "npc-1", linked: true, factCount: 3, chars: 400, maxChars: 4000 }],
          },
        ),
    });
  });
  return { transport, npcs, previewCalls, createCalls, draftCalls, grants };
}

// The panel mounts on nothing but its own RPCs: no host, no router. (The host's
// side — the view switch, the campaign header, selection surviving a view
// change — is Campaign.test.tsx.)
function renderScreen(opts: Parameters<typeof mockTransport>[0] = {}) {
  const mock = mockTransport(opts);
  render(
    <Providers transport={mock.transport} queryClient={makeQueryClient()}>
      <CastPanel />
    </Providers>,
  );
  return mock;
}

// The Tools section lives inside a closed-by-default AdvancedCard whose body is
// unmounted while closed — every assertion against it must open the card first.
// The editor remounts (keyed) when the selected agent changes, so the card must
// be re-opened after each selection change.
function openAdvanced() {
  fireEvent.click(screen.getByRole("button", { name: /advanced: tools/i }));
}

const rosterList = () => screen.getByRole("list", { name: "Cast members" });
const rosterRow = (name: string) => within(rosterList()).getByText(name).closest("button") as HTMLButtonElement;
const rosterNames = () =>
  within(rosterList())
    .getAllByRole("listitem")
    .map((li) => li.querySelector(".gx-roster__name")?.textContent);
const nameField = () => screen.getByLabelText("Name") as HTMLInputElement;
const saveButton = () => screen.getByRole("button", { name: /save changes/i });
const newNpcDialog = () => screen.getByRole("dialog", { name: "New NPC" });

// scopedTransport serves a single scope-supporting Tool (remember_knowledge)
// whose grant state mutates in-closure and records every UpdateToolGrant call, so
// a test can prove what the grant Switch versus the Save-scope button actually
// send (#215). It mirrors the handler: a granted=true call with an empty config
// stores no scope (a fresh grant), and granted=false clears it.
function scopedTransport(initial: { granted: boolean; config: string }) {
  const campaign = create(CampaignSchema, { id: "c1", name: "X", system: "5e", language: "en" });
  const butler = create(AgentSchema, { id: "b1", campaignId: "c1", role: "butler", name: "Glyphoxa", addressOnly: true });
  const npc = create(AgentSchema, { id: "n1", campaignId: "c1", role: "character", name: "Bart", voice: "rachel" });
  let state = { ...initial };
  const calls: { granted: boolean; config: string }[] = [];
  const entry = () =>
    create(ToolGrantSchema, {
      toolName: "remember_knowledge",
      description: "Remember a fact.",
      granted: state.granted,
      config: state.config,
      supportsScope: true,
    });
  const transport = createRouterTransport(({ service }) => {
    service(VoiceService, { listVoices: () => create(ListVoicesResponseSchema, { voices: [] }) });
    service(CampaignService, {
      getCampaignRoster: () => create(GetCampaignRosterResponseSchema, { campaign, roster: [butler, npc] }),
      listToolGrants: () => create(ListToolGrantsResponseSchema, { grants: [entry()] }),
      updateToolGrant: (req) => {
        calls.push({ granted: req.granted, config: req.config });
        state = { granted: req.granted, config: req.granted ? req.config : "" };
        return create(UpdateToolGrantResponseSchema, { grant: entry() });
      },
    });
  });
  return { transport, calls, state: () => state };
}

describe("CastPanel", () => {
  it("renders the roster on its own RPCs — no host, no router", async () => {
    renderScreen();
    // Both the Butler and the NPC appear in the roster.
    expect(await screen.findByText("Bart")).toBeInTheDocument();
    expect(screen.getByText("Glyphoxa")).toBeInTheDocument();
    // The campaign header is the host's, not the panel's.
    expect(screen.queryByRole("heading", { name: "The Sunless Citadel" })).not.toBeInTheDocument();
  });

  it("shows a loading placeholder while the roster loads, then the roster", async () => {
    renderScreen();
    expect(screen.getByRole("status", { name: "Loading…" })).toBeInTheDocument();
    expect(await screen.findByText("Bart")).toBeInTheDocument();
    expect(screen.queryByRole("status", { name: "Loading…" })).not.toBeInTheDocument();
  });

  it("says why when the roster cannot be loaded, and offers to try again", async () => {
    let failures = 4; // the first read and its three retries
    const transport = createRouterTransport(({ service }) => {
      service(VoiceService, { listVoices: () => create(ListVoicesResponseSchema, { voices: [] }) });
      service(CampaignService, {
        getCampaignRoster: () => {
          if (failures-- > 0) throw new ConnectError("roster store offline", Code.Unavailable);
          return create(GetCampaignRosterResponseSchema, {
            campaign: create(CampaignSchema, { id: "c1", name: "X" }),
            roster: [create(AgentSchema, { id: "b1", role: "butler", name: "Glyphoxa", addressOnly: true })],
          });
        },
      });
    });
    // A failed roster read is retried (it is only CodeNotFound that settles at
    // once); no backoff here so the test reaches the settled error quickly.
    const queryClient = makeQueryClient();
    queryClient.setDefaultOptions({ queries: { retryDelay: 0 } });
    render(
      <Providers transport={transport} queryClient={queryClient}>
        <CastPanel />
      </Providers>,
    );
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Couldn't load the cast");
    expect(alert).toHaveTextContent(/roster store offline/);

    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Glyphoxa")).toBeInTheDocument();
  });

  it("locks the Butler: Address-Only is forced on and its switch is disabled", async () => {
    renderScreen();
    // The Butler is selected by default; the addressing switch sits with the
    // name fields.
    await screen.findByText("Bart");
    const sw = screen.getByLabelText(/only answers when called by name/i);
    expect(sw).toBeDisabled();
    expect(sw).toBeChecked();
    // The Butler is not deletable.
    expect(screen.queryByRole("button", { name: /delete npc/i })).not.toBeInTheDocument();
  });

  it("round-trips an NPC edit to the store and reloads it identically", async () => {
    const { npcs } = renderScreen();
    // Select the NPC and rename it.
    fireEvent.click(await screen.findByText("Bart"));
    expect(nameField().value).toBe("Bart");
    fireEvent.change(nameField(), { target: { value: "Bartholomew" } });
    fireEvent.click(saveButton());

    // The edit persisted to the store…
    expect(await screen.findByText("Bartholomew")).toBeInTheDocument();
    expect(npcs[0].name).toBe("Bartholomew");
    // …and the old name is gone (the roster re-read the store).
    expect(screen.queryByText("Bart")).not.toBeInTheDocument();
    // The editor reads saved, with nothing left to save.
    expect(await screen.findByText("Saved")).toBeInTheDocument();
    expect(saveButton()).toBeDisabled();
  });

  it("teaches an NPC a nickname: the alias round-trips through UpdateAgent", async () => {
    const { npcs } = renderScreen();
    fireEvent.click(await screen.findByText("Bart"));
    const aliases = screen.getByRole("textbox", { name: "Also answers to" });
    fireEvent.change(aliases, { target: { value: "Barty" } });
    fireEvent.keyDown(aliases, { key: "Enter" });
    expect(screen.getByText("Unsaved changes")).toBeInTheDocument();

    fireEvent.click(saveButton());
    expect(await screen.findByText("Saved")).toBeInTheDocument();
    expect(npcs[0].aliases).toEqual(["Barty"]);
    // Still there after the roster re-read.
    expect(screen.getByRole("button", { name: "Remove Barty" })).toBeInTheDocument();
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
        <CastPanel />
      </Providers>,
    );
    fireEvent.click(await screen.findByText("Bart"));
    fireEvent.change(nameField(), { target: { value: "Bartholomew" } });
    fireEvent.click(saveButton());
    expect(await screen.findByRole("alert")).toHaveTextContent(/couldn't save/i);
  });

  it("renders the voice select from the live ListVoices catalog", async () => {
    renderScreen();
    // Select Bart (persisted voice id "rachel"); the trigger shows the LIVE label
    // resolved from the catalog, not the raw id — proving the dropdown is fed by
    // VoiceService.ListVoices.
    fireEvent.click(await screen.findByText("Bart"));
    expect(await screen.findByText("ElevenLabs · Rachel")).toBeInTheDocument();
  });

  it("surfaces an error cue when the voice preview fails", async () => {
    // A degraded TTS (PreviewVoice rejects) must render, not just stop the spinner.
    const campaign = create(CampaignSchema, { id: "c1", name: "X", system: "5e", language: "en" });
    const butler = create(AgentSchema, { id: "b1", campaignId: "c1", role: "butler", name: "Glyphoxa", addressOnly: true });
    const npc = create(AgentSchema, { id: "n1", campaignId: "c1", role: "character", name: "Bart", voice: "rachel" });
    const transport = createRouterTransport(({ service }) => {
      service(VoiceService, {
        listVoices: () => create(ListVoicesResponseSchema, { voices: [] }),
        previewVoice: () => {
          throw new Error("tts unavailable");
        },
      });
      service(CampaignService, {
        getCampaignRoster: () => create(GetCampaignRosterResponseSchema, { campaign, roster: [butler, npc] }),
      });
    });
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <CastPanel />
      </Providers>,
    );
    fireEvent.click(await screen.findByText("Bart"));
    fireEvent.click(screen.getByRole("button", { name: /preview voice/i }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/couldn't preview/i);
  });

  it("previews the selected voice via the PreviewVoice RPC", async () => {
    const { previewCalls } = renderScreen();
    fireEvent.click(await screen.findByText("Bart"));
    fireEvent.click(screen.getByRole("button", { name: /preview voice/i }));
    // The preview RPC fired for the selected voice (audio playback is a no-op in
    // jsdom; the RPC call is the observable behaviour).
    await waitFor(() => expect(previewCalls).toEqual(["rachel"]));
  });

  it("shows the per-agent Tools section with each grant's current state (#117)", async () => {
    renderScreen();
    // Bart (npc-1) is dice-ungranted; the toggle renders unchecked.
    fireEvent.click(await screen.findByText("Bart"));
    openAdvanced();
    const dice = await screen.findByLabelText("Dice rolls");
    expect(dice).not.toBeChecked();

    // The Butler (butler-1) is dice-granted; its toggle renders checked. The
    // editor remounted on selection, so the advanced card is closed again.
    fireEvent.click(screen.getByText("Glyphoxa"));
    openAdvanced();
    expect(await screen.findByLabelText("Dice rolls")).toBeChecked();
  });

  it("toggles a grant on and it persists across a reload (#117 AC2)", async () => {
    const { grants } = renderScreen();
    fireEvent.click(await screen.findByText("Bart"));
    openAdvanced();
    const dice = await screen.findByLabelText("Dice rolls");
    expect(dice).not.toBeChecked();

    fireEvent.click(dice);

    // The mutation persisted to the store and the invalidated query re-read it as
    // granted — the toggle sticks after the reload the invalidation triggers.
    await waitFor(() => expect(screen.getByLabelText("Dice rolls")).toBeChecked());
    expect(grants["npc-1"].granted).toBe(true);
    // A grant writes at once; it is not one of the form's unsaved edits.
    expect(screen.queryByText("Unsaved changes")).not.toBeInTheDocument();
  });

  it("disables the grant toggle while its mutation is in flight (#117)", async () => {
    // A deferred UpdateToolGrant holds the toggle pending until we resolve it.
    const campaign = create(CampaignSchema, { id: "c1", name: "X", system: "5e", language: "en" });
    const butler = create(AgentSchema, { id: "b1", campaignId: "c1", role: "butler", name: "Glyphoxa", addressOnly: true });
    const npc = create(AgentSchema, { id: "n1", campaignId: "c1", role: "character", name: "Bart", voice: "rachel" });
    let resolveUpdate: () => void = () => {};
    let updateCalls = 0;
    let granted = false;
    const transport = createRouterTransport(({ service }) => {
      service(VoiceService, { listVoices: () => create(ListVoicesResponseSchema, { voices: [] }) });
      service(CampaignService, {
        getCampaignRoster: () => create(GetCampaignRosterResponseSchema, { campaign, roster: [butler, npc] }),
        listToolGrants: () =>
          create(ListToolGrantsResponseSchema, {
            grants: [create(ToolGrantSchema, { toolName: "dice", description: "Roll dice.", granted })],
          }),
        updateToolGrant: async (req) => {
          updateCalls += 1;
          await new Promise<void>((r) => (resolveUpdate = r));
          granted = req.granted;
          return create(UpdateToolGrantResponseSchema, {
            grant: create(ToolGrantSchema, { toolName: "dice", granted }),
          });
        },
      });
    });
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <CastPanel />
      </Providers>,
    );
    fireEvent.click(await screen.findByText("Bart"));
    openAdvanced();
    const dice = await screen.findByLabelText("Dice rolls");
    fireEvent.click(dice);

    // In flight: the toggle is disabled, so a second click can't double-submit.
    await waitFor(() => expect(screen.getByLabelText("Dice rolls")).toBeDisabled());
    fireEvent.click(screen.getByLabelText("Dice rolls"));
    expect(updateCalls).toBe(1);

    resolveUpdate();
    await waitFor(() => expect(screen.getByLabelText("Dice rolls")).toBeChecked());
  });

  it("renders a scope editor only for a Tool that supports one, and round-trips its config (#117 AC3)", async () => {
    // A scope-supporting Tool exercises the editor half of AC3; dice (no scope)
    // proves the editor is absent for a Tool without one.
    const campaign = create(CampaignSchema, { id: "c1", name: "X", system: "5e", language: "en" });
    const butler = create(AgentSchema, { id: "b1", campaignId: "c1", role: "butler", name: "Glyphoxa", addressOnly: true });
    const npc = create(AgentSchema, { id: "n1", campaignId: "c1", role: "character", name: "Bart", voice: "rachel" });
    const configs: string[] = [];
    const transport = createRouterTransport(({ service }) => {
      service(VoiceService, { listVoices: () => create(ListVoicesResponseSchema, { voices: [] }) });
      service(CampaignService, {
        getCampaignRoster: () => create(GetCampaignRosterResponseSchema, { campaign, roster: [butler, npc] }),
        listToolGrants: () =>
          create(ListToolGrantsResponseSchema, {
            grants: [
              create(ToolGrantSchema, { toolName: "dice", description: "Roll dice.", granted: true, supportsScope: false }),
              create(ToolGrantSchema, {
                toolName: "remember_knowledge",
                description: "Remember a fact.",
                granted: true,
                supportsScope: true,
                config: '{"scope":"self"}',
              }),
            ],
          }),
        updateToolGrant: (req) => {
          configs.push(req.config);
          return create(UpdateToolGrantResponseSchema, {
            grant: create(ToolGrantSchema, { toolName: req.toolName, granted: req.granted, config: req.config, supportsScope: true }),
          });
        },
      });
    });
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <CastPanel />
      </Providers>,
    );
    fireEvent.click(await screen.findByText("Bart"));
    openAdvanced();

    // dice supports no scope → no scope field; remember_knowledge does → one
    // shows, under friendly labels (never the wire names) and tucked behind
    // the closed "Advanced" disclosure.
    await screen.findByLabelText("Dice rolls");
    expect(screen.queryByText("dice")).not.toBeInTheDocument();
    expect(screen.queryByLabelText(/dice rolls scope/i)).not.toBeInTheDocument();
    const advanced = screen.getByText(/advanced: scope \(json\)/i).closest("details") as HTMLDetailsElement;
    expect(advanced.open).toBe(false);
    fireEvent.click(screen.getByText(/advanced: scope \(json\)/i));
    const scope = (await screen.findByLabelText("Remember facts scope")) as HTMLInputElement;
    expect(scope.value).toBe('{"scope":"self"}');

    // Edit the scope and save it: the new config round-trips through UpdateToolGrant.
    fireEvent.change(scope, { target: { value: '{"scope":"campaign"}' } });
    fireEvent.click(screen.getByRole("button", { name: /save scope/i }));
    await waitFor(() => expect(configs).toContain('{"scope":"campaign"}'));
  });

  it("the grant Switch never persists an unsaved scope edit; only Save scope does (#215)", async () => {
    const { transport, calls } = scopedTransport({ granted: true, config: '{"scope":"self"}' });
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <CastPanel />
      </Providers>,
    );
    fireEvent.click(await screen.findByText("Bart"));
    openAdvanced();
    const scope = (await screen.findByLabelText("Remember facts scope")) as HTMLInputElement;
    expect(scope.value).toBe('{"scope":"self"}');

    // Edit the scope but do NOT click Save scope, then flip the grant Switch off.
    fireEvent.change(scope, { target: { value: '{"scope":"campaign"}' } });
    fireEvent.click(screen.getByLabelText("Remember facts"));

    // The toggle mutation fired for the revoke and carried NO config — the unsaved
    // draft was never sent, so a stray edit can't silently persist on a toggle.
    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0].granted).toBe(false);
    expect(calls[0].config).toBe("");
  });

  it("toggling a grant off then on does not resurrect the pre-revoke scope (#215)", async () => {
    const { transport, calls, state } = scopedTransport({ granted: true, config: '{"scope":"self"}' });
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <CastPanel />
      </Providers>,
    );
    fireEvent.click(await screen.findByText("Bart"));
    openAdvanced();
    const sw = await screen.findByLabelText("Remember facts");
    expect(sw).toBeChecked();

    // Revoke (deletes the row), then re-grant.
    fireEvent.click(sw);
    await waitFor(() => expect(screen.getByLabelText("Remember facts")).not.toBeChecked());
    fireEvent.click(screen.getByLabelText("Remember facts"));
    await waitFor(() => expect(screen.getByLabelText("Remember facts")).toBeChecked());

    // The re-grant is a FRESH grant with no scope — the deleted config is gone, not
    // resurrected from local state — and the reappeared editor shows the server
    // value (empty), not the stale pre-revoke draft.
    expect(state().config).toBe("");
    const rescope = (await screen.findByLabelText("Remember facts scope")) as HTMLInputElement;
    expect(rescope.value).toBe("");
    const onCall = calls[calls.length - 1];
    expect(onCall.granted).toBe(true);
    expect(onCall.config).toBe("");
  });
});

describe("CastPanel New NPC", () => {
  it("creates nothing on Add NPC; the dialog's submit sends the real values and selects her", async () => {
    const { npcs, createCalls } = renderScreen();
    await screen.findByText("Bart");

    fireEvent.click(screen.getByRole("button", { name: /add npc/i }));
    // A dialog, and no "New NPC" placeholder row behind it.
    expect(newNpcDialog()).toBeInTheDocument();
    expect(createCalls).toEqual([]);
    expect(npcs).toHaveLength(1);

    fireEvent.change(within(newNpcDialog()).getByLabelText("Name"), { target: { value: "Mira" } });
    fireEvent.change(within(newNpcDialog()).getByLabelText("Role shown under the name"), {
      target: { value: "Harbormaster" },
    });
    fireEvent.click(within(newNpcDialog()).getByRole("button", { name: "Create NPC" }));

    await waitFor(() => expect(createCalls).toEqual([{ name: "Mira", title: "Harbormaster", persona: "", voice: "" }]));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    // She is in the roster and her editor is the one open.
    await waitFor(() => expect(nameField().value).toBe("Mira"));
    expect(rosterNames()).toEqual(["Glyphoxa", "Bart", "Mira"]);
    expect(rosterRow("Mira")).toHaveAttribute("aria-current", "true");
    // No concept was given, so nothing was drafted and nothing is unsaved.
    expect(screen.queryByText("Unsaved changes")).not.toBeInTheDocument();
  });

  // The readiness read on screen predates her: she has no row in it. She is
  // graded on what the roster itself knows, not told "no wiki entry" by a read
  // that has never heard of her — and nobody is called "all ready" meanwhile.
  it("does not grade the new NPC against a readiness read that predates her", async () => {
    renderScreen();
    await screen.findByText("all session-ready");
    fireEvent.click(screen.getByRole("button", { name: /add npc/i }));
    fireEvent.change(within(newNpcDialog()).getByLabelText("Name"), { target: { value: "Mira" } });
    fireEvent.click(within(newNpcDialog()).getByRole("button", { name: "Create NPC" }));

    await waitFor(() => expect(nameField().value).toBe("Mira"));
    await waitFor(() =>
      expect(rosterRow("Mira")).toHaveTextContent("Needs attention: No personality written, No voice"),
    );
    expect(rosterRow("Mira")).not.toHaveTextContent(/wiki entry/i);
    expect(screen.queryByText("all session-ready")).not.toBeInTheDocument();
  });

  it("puts the Add button above the roster", async () => {
    renderScreen();
    await screen.findByText("Bart");
    const add = screen.getByRole("button", { name: /add npc/i });
    expect(add.compareDocumentPosition(rosterList()) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("with a concept, drafts a personality into the new NPC's editor as an unsaved draft", async () => {
    const { npcs, draftCalls } = renderScreen();
    await screen.findByText("Bart");
    fireEvent.click(screen.getByRole("button", { name: /add npc/i }));
    fireEvent.change(within(newNpcDialog()).getByLabelText("Name"), { target: { value: "Mira" } });
    fireEvent.change(within(newNpcDialog()).getByLabelText("Role shown under the name"), {
      target: { value: "Harbormaster" },
    });
    fireEvent.change(within(newNpcDialog()).getByLabelText("Concept (optional)"), {
      target: { value: "a stern harbourmaster" },
    });
    fireEvent.click(within(newNpcDialog()).getByRole("button", { name: "Create NPC" }));

    // GeneratePersona ran once, for the NEW agent, with her real name and role.
    await waitFor(() => expect(draftCalls).toHaveLength(1));
    expect(draftCalls[0]).toEqual({
      agentId: npcs[1].id,
      prompt: "a stern harbourmaster",
      name: "Mira",
      title: "Harbormaster",
    });
    // The draft is in her editor, unsaved: the store still has no personality.
    await waitFor(() =>
      expect((screen.getByLabelText("Personality & backstory") as HTMLTextAreaElement).value).toBe("**You are Mira**"),
    );
    expect(nameField().value).toBe("Mira");
    expect(screen.getByText("Unsaved changes")).toBeInTheDocument();
    expect(npcs[1].persona).toBe("");
    await act(() => new Promise((resolve) => setTimeout(resolve, 20)));
    expect(draftCalls).toHaveLength(1);
  });

  it("deletes an NPC behind a confirmation, keeping the store in sync", async () => {
    const { npcs } = renderScreen({
      npcs: [BART, { id: "npc-2", campaignId: "c1", role: "character", name: "Mira", persona: "Schemes.", voice: "rachel" }],
    });
    fireEvent.click(await screen.findByText("Mira"));
    await waitFor(() => expect(nameField().value).toBe("Mira"));

    // The destructive act is gated behind a ConfirmDialog — nothing mutates until
    // the dialog's own confirm is pressed.
    fireEvent.click(screen.getByRole("button", { name: /delete npc/i }));
    const dialog = await screen.findByRole("alertdialog");
    expect(npcs).toHaveLength(2);
    fireEvent.click(within(dialog).getByRole("button", { name: /delete npc/i }));
    // She leaves the roster once the store re-reads; the other one stays.
    await waitFor(() => expect(screen.queryByText("Mira")).not.toBeInTheDocument());
    expect(npcs).toHaveLength(1);
    expect(screen.getByText("Bart")).toBeInTheDocument();
  });

  it("offers the first NPC from an empty state when the cast is just the Butler", async () => {
    const { createCalls } = renderScreen({ npcs: [], graph: { nodes: [], edges: [] }, readiness: { agents: [] } });
    expect(await screen.findByText("No NPCs yet")).toBeInTheDocument();
    expect(screen.getByText(/characters your players talk to/i)).toBeInTheDocument();
    // One action, the same one: no second "Add NPC" above an empty list.
    const add = screen.getAllByRole("button", { name: /add npc/i });
    expect(add).toHaveLength(1);
    // No count line for a cast of nobody.
    expect(screen.queryByText(/\bNPCs?\b ·/)).not.toBeInTheDocument();

    fireEvent.click(add[0]);
    expect(newNpcDialog()).toBeInTheDocument();
    expect(createCalls).toEqual([]);
  });
});

describe("CastPanel roster readiness", () => {
  // Bart is ready. Mira has a personality and a linked, filled entry — but no voice.
  const oneWithoutVoice = {
    npcs: [BART, { id: "npc-2", campaignId: "c1", role: "character", name: "Mira", persona: "Schemes." } as AgentInit],
    graph: {
      nodes: [
        { id: "node-bart", nodeType: NodeType.NPC, name: "Bart", agentId: "npc-1", bodyLen: 40 },
        { id: "node-mira", nodeType: NodeType.NPC, name: "Mira", agentId: "npc-2", bodyLen: 40 },
      ],
      edges: [],
    },
    readiness: {
      agents: [
        { agentId: "npc-1", linked: true, factCount: 3, chars: 400, maxChars: 4000 },
        { agentId: "npc-2", linked: true, factCount: 2, chars: 300, maxChars: 4000 },
      ],
    },
  };

  it("marks an NPC with no voice, and the marker's tooltip names the missing check", async () => {
    renderScreen(oneWithoutVoice);
    await screen.findByText("Mira");
    // The marker is on Mira's row only, and a screen reader hears why.
    await waitFor(() => expect(rosterRow("Mira")).toHaveTextContent("Needs attention: No voice"));
    expect(rosterRow("Bart")).not.toHaveTextContent(/needs attention/i);
    expect(rosterRow("Glyphoxa")).not.toHaveTextContent(/needs attention/i);

    // Keyboard focus on the row opens the tooltip.
    act(() => rosterRow("Mira").focus());
    const tooltip = screen.getByRole("tooltip");
    expect(tooltip).toHaveTextContent("Not session-ready");
    expect(tooltip).toHaveTextContent("No voice");
    expect(tooltip).not.toHaveTextContent("No personality written");

    // A ready row has nothing to say.
    act(() => rosterRow("Bart").focus());
    expect(screen.queryByRole("tooltip")).not.toBeInTheDocument();
  });

  it("sums the roster up, and the summary toggles a needs-attention filter", async () => {
    renderScreen(oneWithoutVoice);
    await screen.findByText("Mira");
    expect(screen.getByText("2 NPCs")).toBeInTheDocument();
    const toggle = await screen.findByRole("button", { name: "1 needs attention" });
    expect(toggle).toHaveAttribute("aria-pressed", "false");
    expect(rosterNames()).toEqual(["Glyphoxa", "Bart", "Mira"]);

    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-pressed", "true");
    expect(rosterNames()).toEqual(["Mira"]);

    fireEvent.click(toggle);
    expect(rosterNames()).toEqual(["Glyphoxa", "Bart", "Mira"]);
  });

  it("clears the marker and the filter once the missing voice is saved", async () => {
    renderScreen(oneWithoutVoice);
    fireEvent.click(await screen.findByText("Mira"));
    fireEvent.click(await screen.findByRole("button", { name: "1 needs attention" }));
    expect(rosterNames()).toEqual(["Mira"]);

    // Pick a voice in her editor and save.
    fireEvent.click(screen.getByRole("button", { name: "Voice" }));
    fireEvent.click(await screen.findByText("ElevenLabs · Marcus"));
    fireEvent.click(saveButton());

    // Nobody needs attention any more: the toggle is gone and with it the
    // filter, so the roster is whole again and says so.
    await waitFor(() => expect(screen.queryByRole("button", { name: /needs? attention/ })).not.toBeInTheDocument());
    expect(rosterNames()).toEqual(["Glyphoxa", "Bart", "Mira"]);
    expect(screen.getByText("all session-ready")).toBeInTheDocument();
    expect(rosterRow("Mira")).not.toHaveTextContent(/needs attention/i);
  });

  it("shows the count of NPCs that are not ready on the Readiness segment", async () => {
    renderScreen(oneWithoutVoice);
    await screen.findByText("Mira");
    expect(await screen.findByRole("button", { name: "Readiness 1 not ready" })).toHaveAttribute("aria-pressed", "false");
    expect(screen.getByRole("button", { name: "Roster" })).toHaveAttribute("aria-pressed", "true");
  });

  it("shows no count on the Readiness segment when everyone is ready", async () => {
    renderScreen();
    await screen.findByText("Bart");
    await screen.findByText("all session-ready");
    expect(screen.getByRole("button", { name: "Readiness" })).toBeInTheDocument();
  });

  // Until the graph and readiness reads land (here: never), the roster only
  // grades what its own payload answers — it must not invent wiki findings.
  it("grades on personality and voice alone while the wiki reads are unavailable", async () => {
    const campaign = create(CampaignSchema, { id: "c1", name: "X" });
    const butler = create(AgentSchema, { id: "b1", campaignId: "c1", role: "butler", name: "Glyphoxa", addressOnly: true });
    const set = create(AgentSchema, { id: "n1", campaignId: "c1", role: "character", name: "Bart", persona: "Grumbles.", voice: "rachel" });
    const blank = create(AgentSchema, { id: "n2", campaignId: "c1", role: "character", name: "Mira" });
    const transport = createRouterTransport(({ service }) => {
      service(VoiceService, { listVoices: () => create(ListVoicesResponseSchema, { voices: [] }) });
      service(CampaignService, {
        getCampaignRoster: () => create(GetCampaignRosterResponseSchema, { campaign, roster: [butler, set, blank] }),
      });
    });
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <CastPanel />
      </Providers>,
    );
    await screen.findByText("Mira");
    expect(rosterRow("Mira")).toHaveTextContent("Needs attention: No personality written, No voice");
    expect(rosterRow("Bart")).not.toHaveTextContent(/needs attention/i);
    // And it does not claim everyone else is ready on half the picture.
    expect(screen.queryByText("all session-ready")).not.toBeInTheDocument();
  });

  it("shows each member's voice by name on its row", async () => {
    renderScreen();
    await screen.findByText("Bart");
    await waitFor(() => expect(rosterRow("Bart")).toHaveTextContent("Voice: Rachel"));
    // The Butler has no voice set: no voice line, and no raw id anywhere.
    expect(rosterRow("Glyphoxa")).not.toHaveTextContent(/voice:/i);
  });

  it("offers a filter once the roster is longer than eight", async () => {
    const many: AgentInit[] = Array.from({ length: 8 }, (_, i) => ({
      id: `npc-${i + 1}`,
      campaignId: "c1",
      role: "character",
      name: `Guard ${i + 1}`,
      title: i === 3 ? "Captain of the watch" : "",
      aliases: i === 5 ? ["Sarge"] : [],
      persona: "Stands.",
      voice: "rachel",
    }));
    renderScreen({ npcs: many, graph: { nodes: [], edges: [] }, readiness: { agents: [] } });
    await screen.findByText("Guard 1");
    const filter = screen.getByRole("searchbox", { name: "Filter the cast" });

    fireEvent.change(filter, { target: { value: "guard 7" } });
    expect(rosterNames()).toEqual(["Guard 7"]);
    // Roles and nicknames match too.
    fireEvent.change(filter, { target: { value: "captain" } });
    expect(rosterNames()).toEqual(["Guard 4"]);
    fireEvent.change(filter, { target: { value: "sarge" } });
    expect(rosterNames()).toEqual(["Guard 6"]);

    fireEvent.change(filter, { target: { value: "nobody" } });
    expect(screen.getByText("Nobody in the cast matches “nobody”.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Clear filter" }));
    expect(rosterNames()).toHaveLength(9);
  });

  it("offers no filter for a roster of eight or fewer", async () => {
    renderScreen();
    await screen.findByText("Bart");
    expect(screen.queryByRole("searchbox", { name: "Filter the cast" })).not.toBeInTheDocument();
  });
});

const persona = () => screen.getByLabelText("Personality & backstory") as HTMLTextAreaElement;

describe("CastPanel ?agent= deep link", () => {
  // The route guarantees ?agent= only ever arrives on the cast view
  // (router.test.tsx); the panel's job is the consume-then-strip.
  it("opens the editor on that NPC once, then strips the param without a history entry", async () => {
    const { transport } = mockTransport();
    const moves: { next: ScreenLocation; replace: boolean }[] = [];
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <MemoryScreenNav
          initial={{ screen: "campaign", view: "cast", search: { agent: "npc-1" } }}
          onNavigate={(next, opts) => moves.push({ next, replace: opts.replace })}
        >
          <CastPanel />
        </MemoryScreenNav>
      </Providers>,
    );
    // Bart's editor, not the Butler's (the default selection).
    await waitFor(() => expect(persona().value).toBe("Grumbles."));
    // Stripped by REPLACE, on the same view: the URL does not pin the selection
    // and Back does not step through the deep link.
    expect(moves).toEqual([
      { next: { screen: "campaign", view: "cast", search: {} }, replace: true },
    ]);

    // Once: picking someone else afterwards is not undone by the consumed param.
    fireEvent.click(screen.getByText("Glyphoxa").closest("button") as HTMLButtonElement);
    await waitFor(() => expect(persona().value).toBe(""));
    expect(moves).toHaveLength(1);
  });

  it("asks before a jump replaces an editor with unsaved edits", async () => {
    const { transport } = mockTransport();
    let jump: (agentID: string) => void = () => {};
    function Jump() {
      // Stands in for a palette hit landing on ?agent= while Cast is open.
      const nav = useScreenNav();
      jump = (agentID) => nav.setSearch({ agent: agentID });
      return null;
    }
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <MemoryScreenNav initial={{ screen: "campaign", view: "cast" }}>
          <Jump />
          <CastPanel />
        </MemoryScreenNav>
      </Providers>,
    );
    // The Butler is selected by default; type into its editor without saving.
    await screen.findByText("Bart");
    fireEvent.change(persona(), { target: { value: "Dry, exact, a little tired." } });

    act(() => jump("npc-1"));
    expect(await screen.findByText("Discard unsaved changes?")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByText("Discard unsaved changes?")).not.toBeInTheDocument());
    // Cancelled: still the Butler's editor, the typing intact.
    expect(persona().value).toBe("Dry, exact, a little tired.");
  });
});

describe("CastPanel cross-view jumps", () => {
  // The panel takes no navigation props: a fix that lives in the World wiki
  // goes through the host (useCampaignScreen).
  function renderInHost() {
    const openNode = vi.fn();
    const openView = vi.fn();
    const host: CampaignScreen = {
      campaign: undefined,
      roster: [],
      openNode,
      openAgent: vi.fn(),
      openView,
    };
    const campaign = create(CampaignSchema, { id: "c1", name: "X" });
    const butler = create(AgentSchema, { id: "b1", campaignId: "c1", role: "butler", name: "Glyphoxa", addressOnly: true });
    // Bart has a linked but EMPTY wiki entry; Mira has none at all.
    const bart = create(AgentSchema, { id: "n1", campaignId: "c1", role: "character", name: "Bart", persona: "Grumbles.", voice: "rachel" });
    const mira = create(AgentSchema, { id: "n2", campaignId: "c1", role: "character", name: "Mira", persona: "Schemes.", voice: "rachel" });
    const transport = createRouterTransport(({ service }) => {
      service(VoiceService, { listVoices: () => create(ListVoicesResponseSchema, { voices: [] }) });
      service(CampaignService, {
        getCampaignRoster: () =>
          create(GetCampaignRosterResponseSchema, { campaign, roster: [butler, bart, mira] }),
        getKnowledgeGraph: () =>
          create(GetKnowledgeGraphResponseSchema, {
            nodes: [{ id: "node-bart", nodeType: 2, name: "Bart", agentId: "n1", bodyLen: 0 }],
            edges: [],
          }),
        getRosterReadiness: () =>
          create(GetRosterReadinessResponseSchema, {
            // A row for every Character NPC, linked or not — as the server answers.
            agents: [
              { agentId: "n1", linked: true, factCount: 3, chars: 400, maxChars: 4000 },
              { agentId: "n2", linked: false, maxChars: 4000 },
            ],
          }),
      });
    });
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <CampaignScreenProvider value={host}>
          <CastPanel />
        </CampaignScreenProvider>
      </Providers>,
    );
    return { openNode, openView };
  }

  const openReadiness = async () => fireEvent.click(await screen.findByRole("button", { name: /^Readiness/ }));

  it("opens the NPC's wiki entry from an unmet 'Entry has content' mark", async () => {
    const { openNode, openView } = renderInHost();
    await openReadiness();
    fireEvent.click(
      await screen.findByRole("button", { name: "Bart: Entry has content — missing, fix it" }),
    );
    expect(openNode).toHaveBeenCalledWith("node-bart");
    expect(openView).not.toHaveBeenCalled();
  });

  it("opens the World wiki from an unmet 'Wiki entry linked' mark", async () => {
    const { openNode, openView } = renderInHost();
    await openReadiness();
    fireEvent.click(
      await screen.findByRole("button", { name: "Mira: Wiki entry linked — missing, fix it" }),
    );
    expect(openView).toHaveBeenCalledWith("knowledge");
    expect(openNode).not.toHaveBeenCalled();
  });

  it("opens the NPC's wiki entry from the editor's callout", async () => {
    const { openNode } = renderInHost();
    fireEvent.click(await screen.findByText("Bart"));
    const callout = await screen.findByRole("note");
    expect(callout).toHaveTextContent("Bart is not session-ready yet");
    fireEvent.click(within(callout).getByRole("button", { name: "Wiki entry is empty" }));
    expect(openNode).toHaveBeenCalledWith("node-bart");
  });

  it("returns from the Readiness view to the editor of the NPC whose name is pressed", async () => {
    renderInHost();
    await openReadiness();
    const todo = await screen.findByRole("group", { name: "NPCs that need attention" });
    fireEvent.click(within(todo).getByRole("button", { name: "Mira" }));
    await waitFor(() => expect(persona().value).toBe("Schemes."));
    expect(screen.getByRole("button", { name: "Roster" })).toHaveAttribute("aria-pressed", "true");
  });
});
