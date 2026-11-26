import { describe, it, expect } from "vitest";
import { create } from "@bufbuild/protobuf";

import { AgentSchema } from "@gen/glyphoxa/management/v1/management_pb";
import {
  agentIndex,
  hashSlot,
  speakerColor,
  speakerName,
  speakerVar,
  speakingAgentName,
  tagLabel,
  typingLabel,
} from "./speakers";
import { tDe, tEn } from "./testT";

const BART = create(AgentSchema, {
  id: "bart",
  role: "character",
  name: "Bart",
  aliases: ["Bartholomew", "the innkeeper"],
  speakerColor: 1,
});
const GRETA = create(AgentSchema, { id: "greta", role: "character", name: "Greta", speakerColor: 4 });

describe("speakerName / tagLabel", () => {
  it("localizes the relay's unnamed-speaker label and leaves real names alone", () => {
    expect(speakerName("Player / DM", "player", tEn)).toBe("Player / GM");
    expect(speakerName("Player / DM", "player", tDe)).toBe("Spieler / GM");
    // An unmapped GM speaker keeps the generic label in the GM lane.
    expect(speakerName("Player / DM", "gm", tDe)).toBe("Spieler / GM");
    expect(speakerName("Anna", "player", tDe)).toBe("Anna");
    expect(speakerName("Bart", "npc", tDe)).toBe("Bart");
  });

  it("localizes the relay's fallback Agent name only on an Agent line", () => {
    expect(speakerName("NPC", "npc", tEn)).toBe("NPC");
    // A player whose Discord name happens to be "NPC" is a name.
    expect(speakerName("NPC", "player", tEn)).toBe("NPC");
  });

  it("localizes the pills by kind", () => {
    expect(tagLabel("NPC", "npc", tEn)).toBe("NPC");
    expect(tagLabel("Butler", "butler", tEn)).toBe("Butler");
    expect(tagLabel(undefined, "player", tEn)).toBe("");
    expect(tagLabel("", "gm", tEn)).toBe("");
  });
});

describe("typingLabel", () => {
  it("renders the structured state in the display language", () => {
    const listening = { active: true, label: "Listening to the table…", state: "listening" };
    expect(typingLabel(listening, tEn)).toBe("Listening to the table…");
    expect(typingLabel(listening, tDe)).toBe("Hört der Runde zu…");

    const speaking = { active: true, label: "Bart is speaking…", state: "speaking", who: "Bart" };
    expect(typingLabel(speaking, tEn)).toBe("Bart is speaking…");
    expect(typingLabel(speaking, tDe)).toBe("Bart spricht…");
  });

  it("falls back to the relay's English label without a state, or with one it does not know", () => {
    expect(typingLabel({ active: true, label: "Bart is speaking…" }, tDe)).toBe("Bart is speaking…");
    expect(typingLabel({ active: true, label: "Thinking…", state: "thinking" }, tDe)).toBe("Thinking…");
    // "speaking" with nobody named is not a sentence the client can write.
    expect(typingLabel({ active: true, label: "Someone is speaking…", state: "speaking" }, tDe)).toBe(
      "Someone is speaking…",
    );
  });
});

describe("speakingAgentName", () => {
  it("names the Agent from the structured state, or from an older relay's label", () => {
    expect(speakingAgentName({ active: true, label: "x", state: "speaking", who: "Bart" }, true)).toBe("Bart");
    expect(speakingAgentName({ active: true, label: "Greta is speaking…" }, true)).toBe("Greta");
  });

  it("names nobody while listening, while inactive, or for a session that is not live", () => {
    expect(speakingAgentName({ active: true, label: "Listening to the table…", state: "listening" }, true)).toBe("");
    expect(speakingAgentName({ active: false, label: "" }, true)).toBe("");
    expect(speakingAgentName({ active: true, label: "x", state: "speaking", who: "Bart" }, false)).toBe("");
  });
});

describe("speaker colours", () => {
  const agents = agentIndex([BART, GRETA]);

  it("maps a palette slot onto the six speaker tokens", () => {
    expect(speakerVar(0)).toBe("var(--speaker-1)");
    expect(speakerVar(5)).toBe("var(--speaker-6)");
    expect(speakerVar(6)).toBe("var(--speaker-1)");
  });

  it("draws an NPC in its voice-rail colour, matched by name or alias, ignoring case", () => {
    expect(speakerColor("Bart", "npc", agents)).toBe("var(--speaker-2)");
    expect(speakerColor("bartholomew", "npc", agents)).toBe("var(--speaker-2)");
    expect(speakerColor("The Innkeeper", "npc", agents)).toBe("var(--speaker-2)");
    expect(speakerColor("Greta", "npc", agents)).toBe("var(--speaker-5)");
  });

  it("leaves an NPC that is not in the cast any more on the default colour", () => {
    expect(speakerColor("Grumnir", "npc", agents)).toBeUndefined();
  });

  it("gives a named player one stable colour and the unnamed lane none", () => {
    const anna = speakerColor("Anna", "player", agents);
    expect(anna).toMatch(/^var\(--speaker-[1-6]\)$/);
    expect(speakerColor("anna ", "player", agents)).toBe(anna);
    expect(hashSlot("Anna")).toBe(hashSlot("ANNA"));
    expect(speakerColor("Player / DM", "player", agents)).toBeUndefined();
  });

  it("keeps the GM and the Butler on their own colours", () => {
    expect(speakerColor("Lukas", "gm", agents)).toBeUndefined();
    expect(speakerColor("Butler", "butler", agents)).toBeUndefined();
  });
});
