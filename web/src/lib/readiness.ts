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

/** A step of either checklist: done, not done, or not known yet. */
type Tristate = { done: boolean | undefined };

/** The steps known to be still open. */
export function unmetSteps<S extends Tristate>(steps: readonly S[]): S[] {
  return steps.filter((s) => s.done === false);
}

/** True only when every step is KNOWN to be done. */
export function isReady(steps: readonly Tristate[]): boolean {
  return steps.every((s) => s.done === true);
}

// ---- Setup checklist ---------------------------------------------------------
//
// What the TABLE needs before its first Voice Session, in the order Setup asks
// for it. Same rules as the campaign steps above: an input that has not
// answered makes its step unknown, never a to-do and never done.

export type SetupStepKey = "campaign" | "discord" | "voice" | "aiModel";
export type SetupOptionalKey = "images" | "spendingLimits";

/** One credential slot as ListProviderConfigs reports it. */
type SlotView = {
  component: string;
  provider: string;
  /** A key of the table's own is stored. */
  showMasked: boolean;
  /** The deployment provides the slot for this table (its plan's keys). */
  providedByPlatform: boolean;
  /** The voice LLM slot the table's sessions run on. */
  inEffect: boolean;
};

export type SetupInputs = {
  /**
   * Whether the table has a campaign: true / false once
   * CampaignService.GetActiveCampaign answered (NotFound is false), undefined
   * while it has not, or when it failed for another reason.
   */
  hasCampaign?: boolean;
  /** ProviderService.ListProviderConfigs. */
  providers?: {
    guildId: string;
    /** The table's standing Discord client: "failed" is a rejected own bot token (#489). */
    integrationState?: string;
    credentials: readonly SlotView[];
  };
  /** ProviderService.GetSpendCaps (only the optional item reads it). */
  spendCaps?: { caps?: { softUsd?: number; hardUsd?: number } };
};

export type SetupStep<K extends string = SetupStepKey> = {
  key: K;
  /** true: done. false: still to do. undefined: its read has not answered. */
  done: boolean | undefined;
  label: MessageKey;
  hint: MessageKey;
  /** The DOM id of the Setup section where the step is done. */
  anchor: string;
};

/** The DOM ids of the Setup sections the checklist links to. */
export const SETUP_ANCHORS = {
  campaign: "setup-campaign",
  discord: "setup-discord",
  voice: "setup-voice",
  aiModel: "setup-ai-model",
  images: "setup-images",
  spendingLimits: "setup-spending-limits",
  /** The plan and usage card (the ready banner points there when sessions are paused). */
  plan: "setup-plan",
  /** The key list's stand-in while its read is out or failed: a step link lands there. */
  configStatus: "setup-config-status",
} as const;

/** A slot runs: the table saved a key, or its plan's keys cover it. */
const slotRuns = (slot: SlotView | undefined) => Boolean(slot && (slot.showMasked || slot.providedByPlatform));

/**
 * deriveSetupSteps returns the four required steps, in order: a campaign, a
 * linked Discord server, voice & speech, and an AI model.
 */
export function deriveSetupSteps(inputs: SetupInputs): SetupStep[] {
  const { hasCampaign, providers } = inputs;
  const creds = providers?.credentials;
  const slot = (component: string, provider?: string) =>
    creds?.find((c) => c.component === component && (provider === undefined || c.provider === provider));

  // The AI model a session would run: the voice LLM slot in effect. A
  // deployment default with no slot of its own (Gemini) has no row to report
  // on; it is the operator's configuration, so it counts as running.
  const inEffect = creds?.find((c) => c.component === "llm" && c.inEffect);
  const aiModel = creds === undefined ? undefined : inEffect === undefined ? true : slotRuns(inEffect);
  // A linked server whose bot connection failed cannot carry a session: the
  // step is open again, and its hint names the cause.
  const discordFailed = providers?.integrationState === "failed";

  return [
    {
      key: "campaign",
      done: hasCampaign,
      label: "config.stepCampaign",
      hint: "config.stepCampaignHint",
      anchor: SETUP_ANCHORS.campaign,
    },
    {
      key: "discord",
      done: providers && providers.guildId !== "" && !discordFailed,
      label: "config.stepDiscord",
      hint: discordFailed ? "config.stepDiscordFailedHint" : "config.stepDiscordHint",
      anchor: SETUP_ANCHORS.discord,
    },
    {
      key: "voice",
      done: creds && slotRuns(slot("tts")),
      label: "config.stepVoice",
      hint: "config.stepVoiceHint",
      anchor: SETUP_ANCHORS.voice,
    },
    {
      key: "aiModel",
      done: aiModel,
      label: "config.stepAiModel",
      hint: "config.stepAiModelHint",
      anchor: SETUP_ANCHORS.aiModel,
    },
  ];
}

/** deriveOptionalSetupItems returns what Setup offers beyond the four steps. */
export function deriveOptionalSetupItems(inputs: SetupInputs): SetupStep<SetupOptionalKey>[] {
  const { providers, spendCaps } = inputs;
  const image = providers?.credentials.find((c) => c.component === "image");
  const caps = spendCaps?.caps;
  return [
    {
      key: "images",
      done: providers && slotRuns(image),
      label: "config.optionalImages",
      hint: "config.optionalImagesHint",
      anchor: SETUP_ANCHORS.images,
    },
    {
      key: "spendingLimits",
      done: spendCaps && (caps?.softUsd !== undefined || caps?.hardUsd !== undefined),
      label: "config.optionalSpendingLimits",
      hint: "config.optionalSpendingLimitsHint",
      anchor: SETUP_ANCHORS.spendingLimits,
    },
  ];
}
