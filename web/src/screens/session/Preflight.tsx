import type { ReactNode } from "react";
import { useQuery } from "@connectrpc/connect-query";
import { CircleCheck, CircleX, Info, TriangleAlert } from "lucide-react";

import {
  HealthStatus,
  VoiceService,
} from "@gen/glyphoxa/management/v1/management_pb";
import type { Agent, ProviderHealth } from "@gen/glyphoxa/management/v1/management_pb";
import { Button } from "@/components/ui/Button";
import { Spinner } from "@/components/ui/Spinner";
import { useScreenNav } from "@/app/screenNav";
import { useI18n, type MessageKey, type TFunc } from "@/i18n";

// Pre-flight: what the GM needs to know BEFORE pressing Start, in the idle
// control card. Four rows, each one thing that decides how tonight goes:
//
//   Discord     is a server linked, is there a Bot token, which channel
//   Services    do the speech, voice and language-model keys answer
//   Cast        how many NPCs, and which have no voice of their own
//   Highlights  is highlight recording on for this campaign
//
// Only Discord can BLOCK the start: without a linked server or a Bot token
// there is nowhere to join, and Start would only fail. Everything else is a
// warning — a degraded provider may recover, a voiceless NPC still speaks with
// the default voice, and a GM who knows better must be able to start anyway.
// Every row that can be fixed says where.

export type CheckTone = "ok" | "warn" | "block" | "info" | "pending";

export type DiscordCheck = {
  tone: CheckTone;
  detail: string;
  /** The fix lives on the Setup screen. */
  setup: boolean;
};

// The lister's two known preconditions (internal/rpc/session_channels.go),
// matched on the raw server message. Any other message is the server's own
// next step and is shown as it came.
const LINK_SERVER_FIRST = "link a Discord server first";
const SAVE_TOKEN_FIRST = "save a Discord Bot token first";

/**
 * The Discord row, from the voice-channel list read. `error` is the lister's
 * raw message (null when it loaded, or when the server has no such RPC);
 * `unsupported` is that second case.
 */
export function discordCheck(
  input: {
    loading: boolean;
    error: string | null;
    unsupported: boolean;
    channelCount: number;
    selectedChannelName: string | null;
  },
  t: TFunc,
): DiscordCheck {
  if (input.loading) return { tone: "pending", detail: t("session.preflightDiscordChecking"), setup: false };
  if (input.error === LINK_SERVER_FIRST) {
    return { tone: "block", detail: t("session.hintLinkServerFirst"), setup: true };
  }
  if (input.error === SAVE_TOKEN_FIRST) {
    return { tone: "block", detail: t("session.hintSaveBotTokenFirst"), setup: true };
  }
  if (input.error != null) return { tone: "warn", detail: input.error, setup: true };
  if (input.unsupported) return { tone: "info", detail: t("session.preflightDiscordUnknown"), setup: false };
  if (input.channelCount === 0) return { tone: "warn", detail: t("session.noVoiceChannels"), setup: false };
  if (input.selectedChannelName == null) {
    return { tone: "warn", detail: t("session.preflightDiscordPick"), setup: false };
  }
  return {
    tone: "ok",
    detail: t("session.preflightDiscordOk", { channel: input.selectedChannelName }),
    setup: false,
  };
}

// The health codes the server classifies a degraded provider with; the catalog
// already holds a sentence for each (the Setup screen's own).
const HEALTH_CODE_KEYS: Record<string, MessageKey> = {
  missing_key: "config.healthMissingKey",
  key_rejected: "config.healthKeyRejected",
  quota_exhausted: "config.healthQuotaExhausted",
  unreachable: "config.healthUnreachable",
  plan_excludes_platform_keys: "config.healthNoPlatformKeys",
  check_failed: "config.healthCheckFailed",
};

// Product names — not translated.
const PROVIDER_NAMES: Record<string, string> = {
  groq: "Groq",
  anthropic: "Anthropic",
  elevenlabs: "ElevenLabs",
  gemini: "Gemini",
  discord: "Discord",
};

const COMPONENT_KEYS: Record<string, MessageKey> = {
  llm: "session.preflightComponentLlm",
  tts: "session.preflightComponentTts",
  discord: "session.preflightComponentDiscord",
};

/** "Voices (ElevenLabs)" — what the entry is the health of, and whose it is. */
function providerLabel(h: ProviderHealth, t: TFunc): string {
  const name = PROVIDER_NAMES[h.provider] ?? h.provider;
  const component = COMPONENT_KEYS[h.component];
  return component ? t("session.preflightProviderOf", { component: t(component), provider: name }) : name;
}

/** The degraded providers a Voice Session depends on, each as "who: what". */
export function degradedProviders(providers: readonly ProviderHealth[], t: TFunc): string[] {
  return providers
    // The image slot paints highlight scenes after the fact; a voice session
    // runs without it, so it is not tonight's problem.
    .filter((h) => h.component !== "image" && h.status === HealthStatus.DEGRADED)
    .map((h) => {
      const key = HEALTH_CODE_KEYS[h.code];
      const detail = key ? t(key) : h.detail;
      return detail ? `${providerLabel(h, t)}: ${detail}` : providerLabel(h, t);
    });
}

const TONE_ICON: Record<Exclude<CheckTone, "pending">, typeof Info> = {
  ok: CircleCheck,
  warn: TriangleAlert,
  block: CircleX,
  info: Info,
};

const TONE_KEY: Record<CheckTone, MessageKey> = {
  ok: "session.preflightToneOk",
  warn: "session.preflightToneWarn",
  block: "session.preflightToneBlock",
  info: "session.preflightToneInfo",
  pending: "session.preflightTonePending",
};

function Row({
  id,
  tone,
  title,
  children,
  action,
}: {
  id: string;
  tone: CheckTone;
  title: string;
  children: ReactNode;
  action?: ReactNode;
}) {
  const { t } = useI18n();
  const Icon = tone === "pending" ? null : TONE_ICON[tone];
  return (
    <li className="gx-preflight__row" data-tone={tone} data-testid={`preflight-${id}`}>
      <span className="gx-preflight__icon" aria-hidden="true">
        {Icon ? <Icon size={16} /> : <Spinner />}
      </span>
      <div className="gx-preflight__text">
        <span className="gx-preflight__title">
          {title}
          {/* The icon's meaning, for a reader who cannot see it. */}
          <span className="gx-visually-hidden"> — {t(TONE_KEY[tone])}</span>
        </span>
        <span className="gx-preflight__detail">{children}</span>
      </div>
      {action && <div className="gx-preflight__action">{action}</div>}
    </li>
  );
}

export function Preflight({
  discord,
  roster,
  rosterState,
  tapeArmed,
}: {
  discord: DiscordCheck;
  /** The campaign's Agents (the Butler included). */
  roster: readonly Agent[];
  rosterState: "pending" | "error" | "ready";
  /** The campaign's highlight-recording switch; undefined while the campaign loads. */
  tapeArmed: boolean | undefined;
}) {
  const { t } = useI18n();
  const nav = useScreenNav();

  // The provider test-calls are real calls (a Discord gateway login among
  // them), so one answer is good for a while: the row is a pre-flight check,
  // not a monitor. retry:false — a failed check is itself the answer.
  const healthQuery = useQuery(
    VoiceService.method.getProviderHealth,
    {},
    { staleTime: 5 * 60_000, retry: false, refetchOnWindowFocus: false },
  );
  const degraded = healthQuery.data ? degradedProviders(healthQuery.data.providers, t) : [];

  const npcs = roster.filter((a) => a.role !== "butler");
  const voiceless = npcs.filter((a) => a.voice === "");

  const setupButton = (
    <Button variant="ghost" size="sm" onClick={() => nav.goScreen("configuration")}>
      {t("session.preflightOpenSetup")}
    </Button>
  );
  const castButton = (agentId?: string) => (
    <Button
      variant="ghost"
      size="sm"
      onClick={() => nav.goCampaignView("cast", agentId ? { agent: agentId } : undefined)}
    >
      {t("session.preflightOpenCast")}
    </Button>
  );

  return (
    <ul className="gx-preflight" aria-label={t("session.preflightLabel")} data-testid="preflight">
      <Row
        id="discord"
        tone={discord.tone}
        title={t("session.preflightDiscord")}
        action={discord.setup ? setupButton : undefined}
      >
        {discord.detail}
      </Row>

      {healthQuery.isPending ? (
        <Row id="providers" tone="pending" title={t("session.preflightProviders")}>
          {t("session.preflightProvidersChecking")}
        </Row>
      ) : healthQuery.isError || !healthQuery.data ? (
        <Row
          id="providers"
          tone="info"
          title={t("session.preflightProviders")}
          action={
            <Button variant="ghost" size="sm" onClick={() => void healthQuery.refetch()} loading={healthQuery.isFetching}>
              {t("session.retry")}
            </Button>
          }
        >
          {t("session.preflightProvidersUnknown")}
        </Row>
      ) : degraded.length > 0 ? (
        <Row id="providers" tone="warn" title={t("session.preflightProviders")} action={setupButton}>
          {degraded.length === 1 ? (
            degraded[0]
          ) : (
            <span className="gx-preflight__list">
              {degraded.map((line) => (
                <span key={line}>{line}</span>
              ))}
            </span>
          )}
        </Row>
      ) : (
        <Row id="providers" tone="ok" title={t("session.preflightProviders")}>
          {t("session.preflightProvidersOk")}
        </Row>
      )}

      {rosterState === "pending" ? (
        <Row id="cast" tone="pending" title={t("session.preflightCast")}>
          {t("session.preflightCastChecking")}
        </Row>
      ) : rosterState === "error" ? (
        <Row id="cast" tone="info" title={t("session.preflightCast")}>
          {t("session.preflightCastUnknown")}
        </Row>
      ) : npcs.length === 0 ? (
        <Row id="cast" tone="warn" title={t("session.preflightCast")} action={castButton()}>
          {t("session.preflightCastNone")}
        </Row>
      ) : voiceless.length > 0 ? (
        <Row
          id="cast"
          tone="warn"
          title={t("session.preflightCast")}
          action={castButton(voiceless.length === 1 ? voiceless[0].id : undefined)}
        >
          {voiceless.length === 1
            ? t("session.preflightCastVoicelessOne", { name: voiceless[0].name, total: npcs.length })
            : t("session.preflightCastVoiceless", {
                n: voiceless.length,
                total: npcs.length,
                names: voiceless.map((a) => a.name).join(", "),
              })}
        </Row>
      ) : (
        <Row id="cast" tone="ok" title={t("session.preflightCast")}>
          {npcs.length === 1 ? t("session.preflightCastOkOne") : t("session.preflightCastOk", { n: npcs.length })}
        </Row>
      )}

      {tapeArmed === undefined ? (
        <Row id="highlights" tone="pending" title={t("session.preflightHighlights")}>
          {t("session.preflightHighlightsChecking")}
        </Row>
      ) : tapeArmed ? (
        <Row id="highlights" tone="ok" title={t("session.preflightHighlights")}>
          {t("session.preflightHighlightsOn")}
        </Row>
      ) : (
        <Row
          id="highlights"
          tone="info"
          title={t("session.preflightHighlights")}
          action={
            <Button variant="ghost" size="sm" onClick={() => nav.goCampaignView("settings")}>
              {t("session.openCampaignSettings")}
            </Button>
          }
        >
          {t("session.preflightHighlightsOff")}
        </Row>
      )}
    </ul>
  );
}
