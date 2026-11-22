import { describe, it, expect } from "vitest";

import {
  deriveNextSteps,
  isReady,
  unmetSteps,
  type NextStepKey,
  type ReadinessInputs,
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
