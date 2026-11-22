import type { ScreenName } from "@/app/screenNav";
import type { MessageKey } from "@/i18n";
import type { CampaignView } from "@/screens/campaign/views";

// What a campaign still needs before its first good session, derived from
// reads the console already makes. Pure: the campaign Overview renders it as
// its "Next steps" checklist, and Setup can ask the same question of the same
// payloads without the two ever disagreeing.
//
// Every input is optional. An absent input means "that read has not answered"
// (still loading, or failed), and the step that depends on it comes back with
// done === undefined: unknown. An unknown step is never shown as a to-do and
// never counted as done, so a slow or failed read cannot nag the GM about
// something they finished, nor claim a campaign is ready on a guess.

export type NextStepKey = "discord" | "npcVoice" | "wiki" | "players" | "map" | "session";

/**
 * The slices of the query payloads the derivation reads. Structural on
 * purpose: a caller passes the response messages as they are (they satisfy
 * these shapes), a test passes three-field literals.
 */
export type ReadinessInputs = {
  /** ProviderService.ListProviderConfigs. */
  providers?: { guildId: string };
  /** The campaign's Agents, from CampaignService.GetCampaignRoster. */
  roster?: readonly { role: string; voice: string }[];
  /** CampaignService.GetKnowledgeGraph. */
  graph?: { nodes: readonly { agentId: string; bodyLen: number; aspectCount: number }[] };
  /** CampaignService.ListCharacters. */
  characters?: { characters: readonly unknown[] };
  /** CampaignService.ListMaps. */
  maps?: { maps: readonly unknown[] };
  /** SessionService.ListSessions. */
  sessions?: { sessions: readonly unknown[] };
};

/** Where a step is done: a top-level screen, or a view of the campaign. */
export type StepTarget = { screen: ScreenName } | { view: CampaignView };

export type NextStep = {
  key: NextStepKey;
  /** true: done. false: still to do. undefined: its read has not answered. */
  done: boolean | undefined;
  /** What to do, as a message key (this module holds no translated string). */
  label: MessageKey;
  /** Why it matters at the table, as a message key. */
  hint: MessageKey;
  target: StepTarget;
};

const isCharacter = (a: { role: string }) => a.role === "character";

/**
 * deriveNextSteps returns all six steps, in the order a new GM does them,
 * each marked done, not done or unknown.
 */
export function deriveNextSteps(inputs: ReadinessInputs): NextStep[] {
  const { providers, roster, graph, characters, maps, sessions } = inputs;
  const npcs = roster?.filter(isCharacter);

  return [
    {
      key: "discord",
      // A Voice Session joins a voice channel of the linked server; without
      // one, Start is refused.
      done: providers && providers.guildId !== "",
      label: "home.stepDiscord",
      hint: "home.stepDiscordHint",
      target: { screen: "configuration" },
    },
    {
      key: "npcVoice",
      // A Character NPC that can speak. The Butler does not count: it exists
      // from the start, and the step is about the table's first NPC.
      done: npcs && npcs.some((a) => a.voice !== ""),
      // With no NPC at all the thing to do is create one; "give it a voice"
      // would point at a control that is not on screen yet.
      label: npcs && npcs.length === 0 ? "home.stepNpcCreate" : "home.stepNpcVoice",
      hint: "home.stepNpcHint",
      target: { view: "cast" },
    },
    {
      key: "wiki",
      // Any entry the GM has touched. Creating an NPC auto-creates its entry
      // EMPTY (ADR-0008, second amendment), so a linked entry with no text and
      // no facts is the product's doing, not a start on the wiki.
      done:
        graph &&
        graph.nodes.some((n) => n.agentId === "" || n.bodyLen > 0 || n.aspectCount > 0),
      label: "home.stepWiki",
      hint: "home.stepWikiHint",
      target: { view: "knowledge" },
    },
    {
      key: "players",
      done: characters && characters.characters.length > 0,
      label: "home.stepPlayers",
      hint: "home.stepPlayersHint",
      target: { view: "players" },
    },
    {
      key: "map",
      done: maps && maps.maps.length > 0,
      label: "home.stepMap",
      hint: "home.stepMapHint",
      target: { view: "maps" },
    },
    {
      key: "session",
      // The list includes a session that is running right now.
      done: sessions && sessions.sessions.length > 0,
      label: "home.stepSession",
      hint: "home.stepSessionHint",
      target: { screen: "session" },
    },
  ];
}

/** The steps known to be still open. */
export function unmetSteps(steps: readonly NextStep[]): NextStep[] {
  return steps.filter((s) => s.done === false);
}

/** True only when every step is KNOWN to be done. */
export function isReady(steps: readonly NextStep[]): boolean {
  return steps.every((s) => s.done === true);
}
