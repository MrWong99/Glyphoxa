import { describe, it, expect } from "vitest";

import {
  deriveNextSteps,
  deriveOptionalSetupItems,
  deriveSetupSteps,
  isReady,
  SETUP_ANCHORS,
  unmetSteps,
  type NextStepKey,
  type ReadinessInputs,
  type SetupInputs,
  type SetupStepKey,
} from "./readiness";

// A campaign with everything in place: each step's table row below removes
// exactly one thing from it.
const PREPARED: ReadinessInputs = {
  providers: { guildId: "123456789012345678" },
  roster: [
    { role: "butler", voice: "" },
    { role: "character", voice: "rachel" },
  ],
  graph: { nodes: [{ agentId: "", bodyLen: 120, aspectCount: 0 }] },
  characters: { characters: [{}] },
  maps: { maps: [{}] },
  sessions: { sessions: [{}] },
};

const step = (inputs: ReadinessInputs, key: NextStepKey) => {
  const found = deriveNextSteps(inputs).find((s) => s.key === key);
  if (!found) throw new Error(`no step ${key}`);
  return found;
};

describe("deriveNextSteps", () => {
  it("lists the six steps in the order a new GM does them", () => {
    expect(deriveNextSteps(PREPARED).map((s) => s.key)).toEqual([
      "discord",
      "npcVoice",
      "wiki",
      "players",
      "map",
      "session",
    ]);
  });

  // One row per step and per answer: done, not done, and "the read has not
  // answered" — which is neither, and must never be shown as a to-do.
  const cases: {
    name: string;
    key: NextStepKey;
    inputs: Partial<ReadinessInputs>;
    done: boolean | undefined;
  }[] = [
    { name: "Discord linked", key: "discord", inputs: {}, done: true },
    { name: "no Discord server linked", key: "discord", inputs: { providers: { guildId: "" } }, done: false },
    { name: "Setup not read", key: "discord", inputs: { providers: undefined }, done: undefined },

    { name: "an NPC with a voice", key: "npcVoice", inputs: {}, done: true },
    {
      name: "NPCs but none with a voice",
      key: "npcVoice",
      inputs: { roster: [{ role: "butler", voice: "onyx" }, { role: "character", voice: "" }] },
      done: false,
    },
    {
      // The Butler's voice does not count: the step is about a Character NPC.
      name: "only the Butler",
      key: "npcVoice",
      inputs: { roster: [{ role: "butler", voice: "onyx" }] },
      done: false,
    },
    { name: "roster not read", key: "npcVoice", inputs: { roster: undefined }, done: undefined },

    { name: "a written entry", key: "wiki", inputs: {}, done: true },
    { name: "an empty wiki", key: "wiki", inputs: { graph: { nodes: [] } }, done: false },
    {
      // Creating an NPC auto-creates its entry, empty (ADR-0008): that is not
      // the GM having started the wiki.
      name: "only an NPC's untouched auto-created entry",
      key: "wiki",
      inputs: { graph: { nodes: [{ agentId: "npc-1", bodyLen: 0, aspectCount: 0 }] } },
      done: false,
    },
    {
      name: "an NPC's entry the GM filled in",
      key: "wiki",
      inputs: { graph: { nodes: [{ agentId: "npc-1", bodyLen: 0, aspectCount: 2 }] } },
      done: true,
    },
    {
      // An entry the GM made by hand counts even while it is only a name.
      name: "a hand-made entry with no text yet",
      key: "wiki",
      inputs: { graph: { nodes: [{ agentId: "", bodyLen: 0, aspectCount: 0 }] } },
      done: true,
    },
    { name: "wiki not read", key: "wiki", inputs: { graph: undefined }, done: undefined },

    { name: "a linked player", key: "players", inputs: {}, done: true },
    { name: "no players", key: "players", inputs: { characters: { characters: [] } }, done: false },
    { name: "players not read", key: "players", inputs: { characters: undefined }, done: undefined },

    { name: "a map", key: "map", inputs: {}, done: true },
    { name: "no maps", key: "map", inputs: { maps: { maps: [] } }, done: false },
    { name: "maps not read", key: "map", inputs: { maps: undefined }, done: undefined },

    { name: "a session on record", key: "session", inputs: {}, done: true },
    { name: "no session yet", key: "session", inputs: { sessions: { sessions: [] } }, done: false },
    { name: "sessions not read", key: "session", inputs: { sessions: undefined }, done: undefined },
  ];

  it.each(cases)("$key: $name → $done", ({ key, inputs, done }) => {
    expect(step({ ...PREPARED, ...inputs }, key).done).toBe(done);
  });

  it("names the NPC step for what is actually missing", () => {
    // No Character NPC at all: the step is to create one.
    expect(step({ ...PREPARED, roster: [{ role: "butler", voice: "" }] }, "npcVoice").label).toBe(
      "home.stepNpcCreate",
    );
    // NPCs exist, none can speak: the step is the voice.
    expect(
      step({ ...PREPARED, roster: [{ role: "character", voice: "" }] }, "npcVoice").label,
    ).toBe("home.stepNpcVoice");
  });

  it("sends each step where it is done", () => {
    const targets = Object.fromEntries(deriveNextSteps(PREPARED).map((s) => [s.key, s.target]));
    expect(targets).toEqual({
      discord: { screen: "configuration" },
      npcVoice: { view: "cast" },
      wiki: { view: "knowledge" },
      players: { view: "players" },
      map: { view: "maps" },
      session: { screen: "session" },
    });
  });
});

describe("unmetSteps / isReady", () => {
  it("a fully prepared campaign has nothing unmet and is ready", () => {
    const steps = deriveNextSteps(PREPARED);
    expect(unmetSteps(steps)).toEqual([]);
    expect(isReady(steps)).toBe(true);
  });

  it("a brand-new campaign has every step unmet", () => {
    const steps = deriveNextSteps({
      providers: { guildId: "" },
      roster: [{ role: "butler", voice: "" }],
      graph: { nodes: [] },
      characters: { characters: [] },
      maps: { maps: [] },
      sessions: { sessions: [] },
    });
    expect(unmetSteps(steps).map((s) => s.key)).toEqual([
      "discord",
      "npcVoice",
      "wiki",
      "players",
      "map",
      "session",
    ]);
    expect(isReady(steps)).toBe(false);
  });

  it("a step whose read has not answered is neither unmet nor ready", () => {
    const steps = deriveNextSteps({ ...PREPARED, maps: undefined });
    expect(unmetSteps(steps)).toEqual([]);
    // Not known to be ready either: nothing may claim "all set" on a guess.
    expect(isReady(steps)).toBe(false);
  });
});

// ---- Setup checklist ---------------------------------------------------------

type Slot = {
  component: string;
  provider: string;
  showMasked: boolean;
  providedByPlatform: boolean;
  inEffect: boolean;
};
const slot = (component: string, provider: string, over: Partial<Slot> = {}): Slot => ({
  component,
  provider,
  showMasked: false,
  providedByPlatform: false,
  inEffect: false,
  ...over,
});

const LINKED = "123456789012345678";

// A trial table: a campaign, a linked server, and every service on the plan's keys.
const TRIAL: SetupInputs = {
  hasCampaign: true,
  providers: {
    guildId: LINKED,
    credentials: [
      slot("discord", "discord", { providedByPlatform: true }),
      slot("llm", "groq", { providedByPlatform: true, inEffect: true }),
      slot("llm", "anthropic", { providedByPlatform: true }),
      slot("tts", "elevenlabs", { providedByPlatform: true }),
      slot("image", "gemini"),
    ],
  },
  spendCaps: { caps: {} },
};

const setupStep = (inputs: SetupInputs, key: SetupStepKey) => {
  const found = deriveSetupSteps(inputs).find((s) => s.key === key);
  if (!found) throw new Error(`no step ${key}`);
  return found;
};

const withCreds = (credentials: Slot[]): SetupInputs => ({
  ...TRIAL,
  providers: { guildId: LINKED, credentials },
});

describe("deriveSetupSteps", () => {
  it("lists the four steps in the order Setup asks for them, each with its section", () => {
    expect(deriveSetupSteps(TRIAL).map((s) => [s.key, s.anchor])).toEqual([
      ["campaign", SETUP_ANCHORS.campaign],
      ["discord", SETUP_ANCHORS.discord],
      ["voice", SETUP_ANCHORS.voice],
      ["aiModel", SETUP_ANCHORS.aiModel],
    ]);
  });

  it("a trial table with a linked server and a campaign is ready", () => {
    const steps = deriveSetupSteps(TRIAL);
    expect(steps.every((s) => s.done === true)).toBe(true);
    expect(isReady(steps)).toBe(true);
  });

  const cases: { name: string; key: SetupStepKey; inputs: SetupInputs; done: boolean | undefined }[] = [
    { name: "no campaign yet", key: "campaign", inputs: { ...TRIAL, hasCampaign: false }, done: false },
    { name: "campaign read pending", key: "campaign", inputs: { ...TRIAL, hasCampaign: undefined }, done: undefined },

    {
      name: "no server linked",
      key: "discord",
      inputs: { ...TRIAL, providers: { guildId: "", credentials: [] } },
      done: false,
    },
    { name: "Setup not read", key: "discord", inputs: { ...TRIAL, providers: undefined }, done: undefined },

    {
      name: "an own ElevenLabs key",
      key: "voice",
      inputs: withCreds([slot("tts", "elevenlabs", { showMasked: true })]),
      done: true,
    },
    { name: "no voice key and none included", key: "voice", inputs: withCreds([slot("tts", "elevenlabs")]), done: false },
    { name: "voice not read", key: "voice", inputs: { ...TRIAL, providers: undefined }, done: undefined },

    {
      name: "the model in use has an own key",
      key: "aiModel",
      inputs: withCreds([slot("llm", "anthropic", { showMasked: true, inEffect: true }), slot("llm", "groq")]),
      done: true,
    },
    {
      // A key on the OTHER slot does not run the sessions.
      name: "only the slot not in use has a key",
      key: "aiModel",
      inputs: withCreds([slot("llm", "groq", { inEffect: true }), slot("llm", "anthropic", { showMasked: true })]),
      done: false,
    },
    {
      name: "a deployment default with no slot of its own",
      key: "aiModel",
      inputs: withCreds([slot("llm", "groq"), slot("llm", "anthropic")]),
      done: true,
    },
    { name: "AI model not read", key: "aiModel", inputs: { ...TRIAL, providers: undefined }, done: undefined },
  ];

  it.each(cases)("$key: $name → $done", ({ key, inputs, done }) => {
    expect(setupStep(inputs, key).done).toBe(done);
  });

  it("a pending read is never a to-do", () => {
    const steps = deriveSetupSteps({});
    expect(unmetSteps(steps)).toEqual([]);
    expect(isReady(steps)).toBe(false);
  });
});

describe("deriveOptionalSetupItems", () => {
  it("lists images and spending limits as optional extras", () => {
    expect(deriveOptionalSetupItems(TRIAL).map((i) => [i.key, i.done])).toEqual([
      ["images", false],
      ["spendingLimits", false],
    ]);
  });

  it("ticks images on a key or the plan's, and limits once one is set", () => {
    const items = deriveOptionalSetupItems({
      providers: { guildId: "", credentials: [slot("image", "gemini", { providedByPlatform: true })] },
      spendCaps: { caps: { hardUsd: 10 } },
    });
    expect(items.map((i) => i.done)).toEqual([true, true]);
  });

  it("is unknown while its reads are out", () => {
    expect(deriveOptionalSetupItems({}).map((i) => i.done)).toEqual([undefined, undefined]);
  });
});
