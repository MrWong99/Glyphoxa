import type { Agent } from "@gen/glyphoxa/management/v1/management_pb";
import type { TFunc } from "@/i18n";
import type { LineKind, TypingState } from "./useSessionEvents";

// Who is speaking, as the Session screen shows it: the localized form of the
// few labels the relay invents itself, and one colour per speaker.
//
// The relay (internal/transcript/relay.go) writes three English labels that are
// not names: the anonymous human lane "Player / DM", and the pills "NPC" and
// "Butler" (#695). Every other `who` is data — a Character, an Agent, a Discord
// display name — and is never translated. They are matched on their exact wire
// spelling, so a player who really is called "NPC" in Discord is unaffected
// unless the line is also an Agent's.

/** The relay's label for a human it could not name (resolveHuman). */
export const UNKNOWN_SPEAKER = "Player / DM";
/** The relay's fallback name for an Agent reply with no routed name. */
const UNNAMED_AGENT = "NPC";

/** A line's speaker name, with the relay's own labels localized. */
export function speakerName(who: string, kind: LineKind | string, t: TFunc): string {
  if (who === UNKNOWN_SPEAKER) return t("session.speakerUnknown");
  if (who === UNNAMED_AGENT && kind === "npc") return t("session.tagNpc");
  return who;
}

/** The pill beside an Agent's name ("NPC" / "Butler"), localized. */
export function tagLabel(tag: string | undefined, kind: LineKind | string, t: TFunc): string {
  if (!tag) return "";
  if (kind === "butler" || tag === "Butler") return t("session.tagButler");
  if (kind === "npc" || tag === "NPC") return t("session.tagNpc");
  return tag;
}

/**
 * The typing indicator's label. A relay that sends the structured state gets
 * a localized sentence; one that predates it (or a state this client does not
 * know) falls back to the English label it also sends.
 */
export function typingLabel(typing: TypingState, t: TFunc): string {
  if (typing.state === "listening") return t("session.typingListening");
  if (typing.state === "speaking" && typing.who) {
    return t("session.typingSpeaking", { name: speakerName(typing.who, "npc", t) });
  }
  return typing.label;
}

/** The name of the Agent the relay says is speaking right now, or "". */
export function speakingAgentName(typing: TypingState, live: boolean): string {
  if (!live || !typing.active) return "";
  if (typing.state === "speaking" && typing.who) return typing.who;
  // An older relay: recover the name from its English label.
  const m = /^(.*) is speaking…$/.exec(typing.label);
  return m ? m[1] : "";
}

// ---- colours ---------------------------------------------------------------

const PALETTE_SIZE = 6;

/** A palette slot (0-based) as its --speaker-N token. */
export function speakerVar(slot: number): string {
  return `var(--speaker-${(((slot % PALETTE_SIZE) + PALETTE_SIZE) % PALETTE_SIZE) + 1})`;
}

const fold = (s: string) => s.trim().toLocaleLowerCase();

/**
 * Looks an Agent up by what a transcript line calls it: its name or any alias,
 * ignoring case. The Line grain records the speaker's label, not an Agent id
 * (#695), so a name is all a persisted line has.
 */
export function agentIndex(roster: readonly Agent[]): Map<string, Agent> {
  const index = new Map<string, Agent>();
  for (const agent of roster) {
    for (const label of [agent.name, ...agent.aliases]) {
      const key = fold(label);
      if (key !== "" && !index.has(key)) index.set(key, agent);
    }
  }
  return index;
}

/** A stable palette slot for a name: the same player is the same colour in every session. */
export function hashSlot(name: string): number {
  let h = 0;
  for (const ch of fold(name)) h = (h * 31 + (ch.codePointAt(0) ?? 0)) >>> 0;
  return h % PALETTE_SIZE;
}

/**
 * The colour a line's speaker is drawn in, or undefined to keep the kind's
 * default (the GM, the Butler and the unnamed lane each have one in the
 * stylesheet). A Character NPC gets the colour its row has in the voice rail
 * (Agent.speaker_color); a named player gets a stable one from their name.
 */
export function speakerColor(
  who: string,
  kind: LineKind | string,
  agents: Map<string, Agent>,
): string | undefined {
  if (kind === "npc") {
    const agent = agents.get(fold(who));
    return agent ? speakerVar(agent.speakerColor) : undefined;
  }
  if (kind === "player" && who !== UNKNOWN_SPEAKER) return speakerVar(hashSlot(who));
  return undefined;
}
