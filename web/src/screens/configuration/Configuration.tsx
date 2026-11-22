import { useEffect, useRef, useState, type ReactNode } from "react";
import { useQuery, useMutation } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import {
  MessagesSquare,
  BrainCircuit,
  AudioLines,
  ImagePlus,
  RefreshCw,
} from "lucide-react";

import {
  CampaignService,
  ProviderService,
  SessionService,
  VoiceService,
  HealthStatus,
  type ProviderCredential,
  type ProviderHealth,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Card } from "@/components/ui/Card";
import { AdvancedCard } from "@/components/ui/AdvancedCard";
import { Badge } from "@/components/ui/Badge";
import { Avatar } from "@/components/ui/Avatar";
import { Input } from "@/components/ui/Input";
import { Combobox } from "@/components/ui/Combobox";
import { Button } from "@/components/ui/Button";
import { FirstCampaignCTA } from "@/components/FirstCampaignCTA";
import { errorMessage, isNotFound } from "@/lib/connectError";
import { invalidateMethodQueries } from "@/lib/queryClient";
import { useI18n, type MessageKey } from "@/i18n";
import { AddBotLink } from "./AddBotLink";
import { ChatModelCard } from "./ChatModelCard";
import { DiscordLinkAutofill } from "./DiscordLinkAutofill";
import { llmProviderLabel } from "./llmProviders";

import "./configuration.css";

// The Configuration screen (the design's "Setup" screen). The campaign
// header reads LIVE GetActiveCampaign; the credential rows drive the write-only
// BYOK flow (#68, ADR-0004/0039): each secret is sealed server-side and never
// read back — the screen shows a masked value + Replace. The status badge starts
// from key-presence and upgrades async (#70) from VoiceService.GetProviderHealth:
// a real test-call (ElevenLabs /v1/voices, a Groq ping, a live Discord login that
// resolves the bot tag) flips it Healthy → Degraded without blocking page load.

// The BYOK secret slots the screen holds. Each renders a SecretRow;
// Groq/Anthropic/ElevenLabs/Gemini save via SaveProviderConfig, the Discord bot
// token via SaveDiscordSettings. `label` is the product name (never
// translated). `kind` is a MessageKey translated at render, so the overline
// speaks the user's language ("AI model", never the internal "LLM").
// `component` is the credential's primary Component label (discord, llm, tts,
// image); the two voice-LLM slots share "llm" and are told apart by provider.
//
// The Planning chat is NOT a slot: its model is a choice over these providers,
// rendered by ChatModelCard below the rows (ADR-0062 amendment).
type ByokSlot = {
  provider: string;
  component: string;
  label: string;
  kind: MessageKey;
  icon: ReactNode;
  /** Renders the Groq model combobox (the live catalog + free text, #227). */
  models?: boolean;
  /** Not needed to run a session: an unsaved key reads "Optional", not "Key needed". */
  optional?: boolean;
  /**
   * A voice-LLM slot (#702): one of the providers a tenant chooses BETWEEN for
   * its Voice Sessions. Exactly one is in use at a time — the tenant's newest
   * LLM row, else the platform default — and only that one is health-probed
   * and required; the others are a choice the tenant has not made.
   */
  voiceLlm?: boolean;
};
const BYOK_SLOTS: readonly ByokSlot[] = [
  {
    provider: "groq",
    component: "llm",
    label: llmProviderLabel("groq"),
    kind: "config.kindAiModel",
    icon: <BrainCircuit size={19} />,
    models: true,
    voiceLlm: true,
  },
  {
    provider: "anthropic",
    component: "llm",
    label: llmProviderLabel("anthropic"),
    kind: "config.kindAiModel",
    icon: <BrainCircuit size={19} />,
    models: true,
    voiceLlm: true,
  },
  {
    provider: "elevenlabs",
    component: "tts",
    label: "ElevenLabs",
    kind: "config.kindVoiceSpeech",
    icon: <AudioLines size={19} />,
  },
  {
    provider: "gemini",
    component: "image",
    label: "Gemini",
    kind: "config.kindImages",
    icon: <ImagePlus size={19} />,
    optional: true,
  },
];

// Rows key their credential on `component`. The voice-LLM slots (#702) share
// component "llm", so those also match on `provider`.
function credentialFor(
  creds: ProviderCredential[],
  component: string,
  provider?: string,
): ProviderCredential | undefined {
  return creds.find((c) => c.component === component && (provider === undefined || c.provider === provider));
}

// The health entry of one row. Entries are keyed by `component` — what the
// entry is the health OF — because a provider name is not unique: Gemini backs
// the image slot and can be the deployment's default LLM too (#702). There is
// exactly one "llm" entry, the LLM in use, so it belongs to the voice-LLM row
// in use whatever provider name it carries (a probe that timed out before the
// tenant's row was read can only name the deployment default). A server that
// predates `component` is matched by provider, as before.
function healthFor(
  health: ProviderHealth[],
  slot: { component: string; provider: string },
  llmInUse = false,
): ProviderHealth | undefined {
  if (!health.some((h) => h.component)) return health.find((h) => h.provider === slot.provider);
  if (slot.component === "llm" && !llmInUse) return undefined;
  return health.find((h) => h.component === slot.component);
}

export function Configuration() {
  const { t } = useI18n();
  // retry:false so a fresh, unseeded install's CodeNotFound settles at once into
  // the first-run create flow rather than backing off through three retries first.
  const { data, status, error } = useQuery(
    CampaignService.method.getActiveCampaign,
    {},
    { retry: false },
  );
  const campaign = data?.campaign;

  const queryClient = useQueryClient();
  // A credential or Discord save changes more than the list: the live health
  // probe, the model and voice catalogs (fetched WITH the key) and the linked
  // guild's voice channels / members all answer differently afterwards. Only
  // the list used to be dropped, so the badge kept its pre-save verdict and
  // the Cast and Session screens kept an empty catalog until a reload.
  const invalidateList = () =>
    invalidateMethodQueries(
      queryClient,
      ProviderService.method.listProviderConfigs,
      VoiceService.method.getProviderHealth,
      VoiceService.method.listModels,
      VoiceService.method.listVoices,
      SessionService.method.listSessionVoiceChannels,
      CampaignService.method.listDiscordVoiceMembers,
    );

  const config = useQuery(ProviderService.method.listProviderConfigs, {});
  const creds = config.data?.credentials ?? [];

  // Async health upgrade (#70): runs the live test-calls off the page-load path.
  // Until it resolves the badge stays on key-presence; then it flips per provider.
  const healthQuery = useQuery(VoiceService.method.getProviderHealth, {});
  const health = healthQuery.data?.providers ?? [];

  // Live Groq model catalog (#227): fetched with the tenant key; the server
  // degrades to just the default on a failed fetch, and the combobox accepts
  // free text either way, so this query can never wedge the screen.
  const groqModels = useQuery(VoiceService.method.listModels, { provider: "groq" });
  // The Anthropic slot's own catalog (#702), fetched with that slot's key.
  const anthropicModels = useQuery(VoiceService.method.listModels, { provider: "anthropic" });
  const modelsByProvider: Record<string, string[]> = {
    groq: groqModels.data?.models ?? [],
    anthropic: anthropicModels.data?.models ?? [],
  };
  // The voice LLM in use (#702): the tenant's own row, or the platform default.
  const effectiveLlm = config.data?.effectiveLlm;

  const saveProvider = useMutation(ProviderService.method.saveProviderConfig, { onSuccess: invalidateList });
  // The Planning chat model choice (ADR-0062 amendment): its own mutation
  // instances, so a refused chat save paints the chat card and never a key row.
  // A chat save changes the chat catalog's key and the "answers with" line, so
  // it drops the same queries a key save does.
  const saveChatModel = useMutation(ProviderService.method.saveProviderConfig, { onSuccess: invalidateList });
  const resetChatModel = useMutation(ProviderService.method.deleteProviderConfig, { onSuccess: invalidateList });
  const saveDiscord = useMutation(ProviderService.method.saveDiscordSettings, { onSuccess: invalidateList });
  // Separate mutation instance for the IDs form so its error/pending state is
  // its own — a bot-token failure paints the SecretRow, not this form (#142).
  const saveDiscordIds = useMutation(ProviderService.method.saveDiscordSettings, { onSuccess: invalidateList });
  // Unlink (#504): frees this tenant's guild binding so another tenant can
  // claim it (legit transfer: we release, they save with proof). The request
  // echoes the bound guild_id — the server compare-and-clears atomically.
  const releaseGuild = useMutation(ProviderService.method.releaseDiscordGuild, {
    onSuccess: () => {
      // The binding is gone server-side: clear the local field and drop the
      // dirty guard so the refetch reseeds the (now empty) stored state.
      idsDirty.current = false;
      setGuildId("");
      setConfirmUnlink(false);
      invalidateList();
    },
  });
  // Two-step unlink: the first click only arms the confirm — releasing a guild
  // hands it to whoever binds next, so it must never ride a single misclick.
  const [confirmUnlink, setConfirmUnlink] = useState(false);

  // The Guild ID is controlled, seeded from the RPC. A `dirty` ref guards the
  // seed so a slow first load (or a post-save refetch) can never clobber what
  // the operator is typing — once edited, the field is the source of truth.
  // The voice channel no longer lives on this screen: sessions pick their
  // channel on the Session screen, where the Default Voice Channel is also set.
  const [guildId, setGuildId] = useState("");
  const idsDirty = useRef(false);
  useEffect(() => {
    if (config.data && !idsDirty.current) {
      setGuildId(config.data.guildId);
    }
  }, [config.data]);
  const editGuildId = (v: string) => {
    idsDirty.current = true;
    setGuildId(v);
  };

  return (
    <div className="gx-providers">
      <h1>{t("config.title")}</h1>
      <p className="gx-providers__lede">{t("config.lede")}</p>

      {/* Active campaign — LIVE GetActiveCampaign (ADR-0039). Data-first: React
          Query retains the last data through a failed background refetch, so an
          already-loaded header rides out a transient blip instead of flashing the
          error card. With no data, a CodeNotFound means no campaign exists yet:
          the create-first-campaign flow replaces the error card so the web is the
          first-run path, not `glyphoxa seed` (#267). */}
      <Card accent className="gx-providers__campaign">
        {campaign ? (
          <div className="gx-campaign">
            <Avatar name={campaign.name} size="lg" />
            <div className="gx-campaign__meta">
              <span className="gx-overline">{t("config.activeCampaign")}</span>
              <span className="gx-campaign__name">{campaign.name}</span>
              <div className="gx-campaign__attrs">
                <span className="gx-campaign__attr">
                  {t("config.campaignSystem")}
                  <span className="gx-campaign__attr-value">{campaign.system}</span>
                </span>
                <span className="gx-campaign__attr">
                  {t("common.language")}
                  <span className="gx-campaign__attr-value">{campaign.language}</span>
                </span>
              </div>
            </div>
          </div>
        ) : status === "error" && isNotFound(error) ? (
          <FirstCampaignCTA />
        ) : status === "error" ? (
          <div className="gx-campaign">
            <p className="gx-campaign__error" role="alert">
              {t("config.couldntLoadCampaign", { message: errorMessage(error) })}
            </p>
          </div>
        ) : (
          <div className="gx-campaign">
            <div className="gx-campaign__meta" data-testid="campaign-loading">
              <span className="gx-overline">{t("config.activeCampaign")}</span>
              <span className="gx-skeleton" />
            </div>
          </div>
        )}
      </Card>

      {/* AI service keys — write-only BYOK (ADR-0004) */}
      <h2 className="gx-section-title">{t("config.aiServiceKeysTitle")}</h2>
      {/* Which LLM a Voice Session started now would run (#702) — the answer
          the two AI-model rows below cannot give on their own. */}
      {effectiveLlm && (
        <p className="gx-providers__llm-in-use" data-testid="llm-in-use">
          {t(effectiveLlm.platformDefault ? "config.llmInUsePlatform" : "config.llmInUseOwn", {
            provider: llmProviderLabel(effectiveLlm.provider),
            model: effectiveLlm.model,
          })}
        </p>
      )}
      <div className="gx-providers__list">
        {BYOK_SLOTS.map((slot) => {
          const credential = credentialFor(creds, slot.component, slot.voiceLlm ? slot.provider : undefined);
          // Until the list answers, assume the code default (Groq) is the
          // voice LLM in use, so the common case paints its final badges at
          // once instead of flipping when the read lands.
          const llmInUse = effectiveLlm ? effectiveLlm.provider === slot.provider : slot.provider === "groq";
          return (
            <SecretRow
              key={`${slot.component}/${slot.provider}`}
              icon={slot.icon}
              kind={t(slot.kind)}
              name={slot.label}
              placeholder={t("config.pasteApiKey", { name: slot.label })}
              credential={credential}
              health={healthFor(health, slot, llmInUse)}
              // A voice-LLM slot the tenant is not on needs no key: it is a
              // choice, so it reads "Optional" where the one in use reads "Key
              // needed".
              optional={slot.optional || (slot.voiceLlm && !llmInUse)}
              voiceLlm={slot.voiceLlm ? { inEffect: Boolean(credential?.inEffect) } : undefined}
              // Groq-backed rows list the live model catalog with free-text entry
              // (#227); the chosen model rides along when the key is saved, or
              // alone via the model-only save once a key exists.
              models={slot.models ? (modelsByProvider[slot.provider] ?? []) : undefined}
              onSave={(secret, model, activate) =>
                saveProvider.mutateAsync({ provider: slot.provider, secret, model, activate })
              }
            />
          );
        })}
        {/* The Planning chat model: a choice over the providers above, with
            the truthful "answers with" line. Its key field never touches the
            voice model (ADR-0062 amendment). */}
        <ChatModelCard
          effective={config.data?.effectiveChatLlm}
          voice={effectiveLlm}
          // failureReason is set from the FIRST failed attempt, while the query
          // is still retrying: the card says the read is failing right away
          // rather than shimmering through the retry backoff, and the error
          // gives way to the control the moment a retry succeeds.
          loadError={config.data ? undefined : (config.error ?? config.failureReason ?? undefined)}
          onSave={(provider, model, secret) =>
            saveChatModel.mutateAsync({ slot: "chat", provider, model, secret })
          }
          onReset={() => resetChatModel.mutateAsync({ slot: "chat" })}
        />
      </div>

      {/* Discord connection — Bot token (secret) + non-secret Guild / Voice IDs */}
      <h2 className="gx-section-title">{t("config.discordTitle")}</h2>
      <Card>
        <div className="gx-discord">
          <SecretRow
            icon={<MessagesSquare size={19} />}
            kind={t("config.kindBot")}
            name={t("config.botTokenName")}
            // Central-Bot tables never need a token (ADR-0057); only a BYOK
            // Bot does — say so before a first-time GM goes hunting for one.
            description={t("config.botTokenHint")}
            optional
            placeholder={t("config.pasteBotToken")}
            credential={credentialFor(creds, "discord")}
            health={healthFor(health, { component: "discord", provider: "discord" })}
            // Token-only save: the ID fields stay OFF the wire (they have proto3
            // presence), so replacing the token can never clobber the stored IDs —
            // even while the config load is still resolving (#142).
            onSave={(secret) => saveDiscord.mutateAsync({ botToken: secret })}
          />
          {config.isSuccess && (
            <IntegrationBadge
              state={config.data.integrationState}
              detail={config.data.integrationDetail}
            />
          )}
          <DiscordLinkAutofill onFill={editGuildId} />
          <div className="gx-discord__ids">
            <Input
              // The wire field stays guild_id; users only ever see "Server ID".
              label={t("config.serverIdLabel")}
              placeholder={t("config.serverIdPlaceholder")}
              hint={t("config.serverIdHint")}
              value={guildId}
              // A snowflake is digits — the numeric keyboard on touch, same as
              // the player bind form's Discord ID field.
              inputMode="numeric"
              onChange={(e) => editGuildId(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && guildId && !saveDiscordIds.isPending) {
                  e.preventDefault();
                  saveDiscordIds.mutate({ guildId });
                }
              }}
            />
          </div>
          <div className="gx-discord__save">
            <Button
              variant="primary"
              size="sm"
              onClick={() => saveDiscordIds.mutate({ guildId })}
              // Locked until the ID is non-empty: the server rejects a
              // present-but-empty ID (#142), so an empty save must not be
              // offered — clicking it used to fail with no visible trace.
              disabled={saveDiscordIds.isPending || !guildId}
            >
              {t("config.saveDiscordSettings")}
            </Button>
            {/* Inline failure cue, mirroring the SecretRow save-error treatment:
                a rejected save must leave visible evidence the IDs were NOT
                stored, or it resurfaces as a session-start failure (#142). */}
            {saveDiscordIds.isError && (
              <span className="gx-discord__error" role="alert">
                {t("common.couldntSave", { message: errorMessage(saveDiscordIds.error) })}
              </span>
            )}
          </div>
          {/* Unlink (#504): offered only while a guild is actually bound
              (stored state, not the form fields). Releasing frees the guild for
              another tenant — the supported transfer path — so it takes an
              explicit confirm, and a failed release stays visible. */}
          {config.isSuccess && config.data.guildId !== "" && (
            <div className="gx-discord__unlink">
              {confirmUnlink ? (
                <>
                  <span className="gx-discord__unlink-note">{t("config.unlinkNote")}</span>
                  <Button
                    variant="danger"
                    size="sm"
                    disabled={releaseGuild.isPending}
                    onClick={() => releaseGuild.mutate({ guildId: config.data.guildId })}
                  >
                    {t("config.confirmUnlink")}
                  </Button>
                  <Button variant="ghost" size="sm" onClick={() => setConfirmUnlink(false)}>
                    {t("common.cancel")}
                  </Button>
                </>
              ) : (
                <Button variant="secondary" size="sm" onClick={() => setConfirmUnlink(true)}>
                  {t("config.unlinkServer")}
                </Button>
              )}
              {releaseGuild.isError && (
                <span className="gx-discord__error" role="alert">
                  {t("config.couldntUnlink", { message: errorMessage(releaseGuild.error) })}
                </span>
              )}
            </div>
          )}
          {/* Authorizing the Bot into the Guild is a separate, prerequisite step
              from saving the IDs — neither pasted-link format joins the Bot (#110).
              Gated on a resolved read so the "no application id" note can't flash
              while the config loads, nor stick on a query error — only a genuine
              empty id (query succeeded) is the disabled fallback. */}
          {config.isSuccess && <AddBotLink applicationId={config.data.discordApplicationId} />}
        </div>
      </Card>

      {/* Per-session spend caps (#130, ADR-0046), shown as "Spending limits".
          Tucked behind a closed-by-default AdvancedCard: optional cost
          guardrails most groups never touch, so no section heading of its own —
          the card title carries it. */}
      <AdvancedCard title={t("config.spendingLimitsTitle")} hint={t("config.spendingLimitsHint")}>
        <SpendCapsCard />
      </AdvancedCard>
    </div>
  );
}

// SpendCapsCard is the minimal soft/hard per-session spend-cap editor (#130,
// ADR-0046): two USD inputs, blank = that cap unset. A soft cap refuses new Agent
// turns once estimated spend crosses it (in-flight replies finish); a hard cap ends
// the session. Both figures are ESTIMATES. The server validates (negative or
// hard < soft => rejected) and caps snapshot at the NEXT session start. The `dirty`
// ref guards the seed so a slow load / post-save refetch can't clobber typing.
function SpendCapsCard() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const capsQuery = useQuery(ProviderService.method.getSpendCaps, {});
  const invalidate = () => invalidateMethodQueries(queryClient, ProviderService.method.getSpendCaps);
  const save = useMutation(ProviderService.method.setSpendCaps, { onSuccess: invalidate });

  const [soft, setSoft] = useState("");
  const [hard, setHard] = useState("");
  const dirty = useRef(false);
  useEffect(() => {
    if (capsQuery.data && !dirty.current) {
      const caps = capsQuery.data.caps;
      setSoft(caps?.softUsd != null ? String(caps.softUsd) : "");
      setHard(caps?.hardUsd != null ? String(caps.hardUsd) : "");
    }
  }, [capsQuery.data]);
  const editSoft = (v: string) => {
    dirty.current = true;
    setSoft(v);
  };
  const editHard = (v: string) => {
    dirty.current = true;
    setHard(v);
  };

  // A blank field is "unset" (omit it, presence-absent → clear); anything else is
  // sent as-is and the server enforces non-negative + hard >= soft.
  const parse = (v: string): number | undefined => {
    const t = v.trim();
    return t === "" ? undefined : Number(t);
  };

  // No Card wrapper of its own: the section now lives inside the "Spending
  // limits" AdvancedCard, whose body already provides the card chrome.
  return (
    <div className="gx-spendcaps">
      <p className="gx-spendcaps__lede">{t("config.spendCapsLede")}</p>
      <div className="gx-spendcaps__inputs">
        <Input
          label={t("config.softLimitLabel")}
          type="number"
          min="0"
          step="0.01"
          placeholder={t("config.softLimitPlaceholder")}
          hint={t("config.softLimitHint")}
          value={soft}
          onChange={(e) => editSoft(e.target.value)}
        />
        <Input
          label={t("config.hardLimitLabel")}
          type="number"
          min="0"
          step="0.01"
          placeholder={t("config.hardLimitPlaceholder")}
          hint={t("config.hardLimitHint")}
          value={hard}
          onChange={(e) => editHard(e.target.value)}
        />
      </div>
      <div className="gx-spendcaps__save">
        <Button
          variant="primary"
          size="sm"
          // Held until the stored caps have actually been read: blank means
          // "clear that cap", so a Save clicked before the seed lands (or after
          // a failed read) would silently wipe limits the GM never saw.
          disabled={save.isPending || !capsQuery.isSuccess}
          onClick={() => save.mutate({ softUsd: parse(soft), hardUsd: parse(hard) })}
        >
          {t("config.saveSpendingLimits")}
        </Button>
        {capsQuery.isError && (
          <span className="gx-spendcaps__error" role="alert">
            {t("config.spendCapsLoadError", { message: errorMessage(capsQuery.error) })}
          </span>
        )}
        {save.isError && (
          <span className="gx-spendcaps__error" role="alert">
            {t("common.couldntSave", { message: errorMessage(save.error) })}
          </span>
        )}
      </div>
    </div>
  );
}

// HealthBadge renders the status dot. An unsaved slot is "Key needed"
// (presence), or a neutral "Optional" when the slot isn't required to run a
// session. A saved slot shows "Saved" instantly (presence) and upgrades to
// "Working" or downgrades to "Having trouble" once GetProviderHealth reports
// the test-call (#70) — the page never waits on the live call, and never
// claims "Working" before a probe said so.
//
// An unsaved slot the deployment provides for this tenant (W9 — a trial
// tenant on the Platform Keys, a central-Bot table) reads "Included": the
// probe runs on the platform's key there too, so a failed probe still
// downgrades it to "Having trouble".
function HealthBadge({
  saved,
  health,
  optional = false,
  platform = false,
}: {
  saved: boolean;
  health?: ProviderHealth;
  optional?: boolean;
  platform?: boolean;
}) {
  const { t } = useI18n();
  if (!saved && platform && health?.status !== HealthStatus.DEGRADED) {
    return (
      <Badge variant="info" dot size="sm">
        {t("config.badgeIncluded")}
      </Badge>
    );
  }
  if (!saved && !platform) {
    return optional ? (
      <Badge variant="neutral" dot size="sm">
        {t("config.badgeOptional")}
      </Badge>
    ) : (
      <Badge variant="warning" dot size="sm">
        {t("config.badgeKeyNeeded")}
      </Badge>
    );
  }
  if (health?.status === HealthStatus.DEGRADED) {
    // The failure reason renders VISIBLY under the badge — a title-attr
    // tooltip is unreachable by keyboard and on touch, and this detail is the
    // one clue to what's wrong with the key.
    return (
      <span className="gx-health">
        <Badge variant="danger" dot size="sm">
          {t("config.badgeDegraded")}
        </Badge>
        {healthDetail(health, t) && (
          <span className="gx-health__detail">{healthDetail(health, t)}</span>
        )}
      </span>
    );
  }
  if (health?.status !== HealthStatus.HEALTHY) {
    return (
      <Badge variant="neutral" dot size="sm">
        {t("config.badgeSaved")}
      </Badge>
    );
  }
  return (
    <Badge variant="success" dot size="sm">
      {t("config.badgeWorking")}
    </Badge>
  );
}

// The stable failure classes GetProviderHealth reports (ProviderHealth.code),
// each with its own translated sentence. An unknown code (a newer server)
// falls back to the server's English detail.
const HEALTH_CODE_KEYS: Record<string, MessageKey> = {
  missing_key: "config.healthMissingKey",
  key_rejected: "config.healthKeyRejected",
  quota_exhausted: "config.healthQuotaExhausted",
  unreachable: "config.healthUnreachable",
  plan_excludes_platform_keys: "config.healthNoPlatformKeys",
  check_failed: "config.healthCheckFailed",
};

function healthDetail(health: ProviderHealth, t: (key: MessageKey) => string): string {
  const key = HEALTH_CODE_KEYS[health.code];
  return key ? t(key) : health.detail;
}

// IntegrationBadge renders THIS tenant's standing Discord client health (#489):
// the per-tenant registry reports "ok" (client up), "waiting" (no Bot token yet),
// "reconnecting" (a token is set but the client is down and being re-opened,
// #642) or "failed" (the token was rejected terminally — revoked/invalid), the
// last two with a human detail. Empty state (web-only mode, no standing
// presence) and any state this build does not know render nothing — only an
// explicit "ok" may claim the Bot is connected.
function IntegrationBadge({ state, detail }: { state: string; detail: string }) {
  const { t } = useI18n();
  if (state === "reconnecting") {
    return (
      <span className="gx-health">
        <Badge variant="warning" dot size="sm">
          {t("config.discordIntegrationReconnecting")}
        </Badge>
        {detail && <span className="gx-health__detail">{detail}</span>}
      </span>
    );
  }
  if (state === "failed") {
    return (
      <span className="gx-health">
        <Badge variant="danger" dot size="sm">
          {t("config.discordIntegrationFailed")}
        </Badge>
        {detail && <span className="gx-health__detail">{detail}</span>}
      </span>
    );
  }
  if (state !== "ok") {
    return null;
  }
  return (
    <Badge variant="success" dot size="sm">
      {t("config.botConnected")}
    </Badge>
  );
}

// SecretRow renders one write-only credential: an editable key field with Save
// when unsaved (or being replaced), a masked value + Replace once saved, and the
// status badge. When `models` is supplied (Groq) it also renders the static
// model allowlist select; the chosen model is passed to onSave. A resolved
// Discord bot tag (from the live login) is shown under the row.
function SecretRow({
  icon,
  kind,
  name,
  description,
  placeholder,
  credential,
  health,
  models,
  optional = false,
  voiceLlm,
  onSave,
}: {
  icon: ReactNode;
  kind: string;
  name: string;
  /** Optional one-liner under the name (the Bot token's "only for your own bot"). */
  description?: string;
  placeholder: string;
  credential?: ProviderCredential;
  health?: ProviderHealth;
  models?: string[];
  /** An unsaved key reads "Optional" rather than "Key needed". */
  optional?: boolean;
  /**
   * Set on a voice-LLM slot (#702): whether this is the LLM in use. The row
   * then says so, and a slot that is NOT in use but has a key to run on — the
   * tenant's saved one, or the platform's — offers "Use for voice".
   */
  voiceLlm?: { inEffect: boolean };
  /**
   * activate (voice-LLM slots only) makes the slot the LLM in use without a
   * new key: the server keeps the stored key, or rides the platform's.
   */
  onSave: (secret: string, model?: string, activate?: boolean) => Promise<unknown>;
}) {
  const { t } = useI18n();
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState("");
  const [busy, setBusy] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [model, setModel] = useState<string | undefined>(undefined);

  const saved = Boolean(credential?.showMasked);
  const masked = saved && !editing;
  // The combobox shows the operator's pick, the saved model, or the catalog
  // default (first), in that order.
  const selectedModel = model ?? (credential?.model || undefined) ?? models?.[0];
  // "Save model" appears only when a saved key exists AND the operator actively
  // picked something different from the stored model (#227) — never for the
  // passive catalog-default display, which stores nothing.
  // A voice-LLM slot the platform provides has a model to choose too, with no
  // key of the tenant's own behind it.
  const platform = Boolean(credential?.providedByPlatform);
  const canRun = saved || (Boolean(voiceLlm) && platform);
  // "Use for voice" (#702): this slot could run the tenant's sessions and does
  // not. It carries the picked model along, so it replaces "Save model" here.
  const canUse = Boolean(voiceLlm) && !voiceLlm?.inEffect && canRun;
  const modelDirty =
    canRun && !canUse && model !== undefined && model !== (credential?.model || undefined);

  // Switch the voice LLM to this slot (#702): no secret on the wire, the
  // stored (or platform) key stays; an actively picked or stored model rides
  // along, the passive catalog default does not.
  async function handleUse() {
    if (busy) return;
    setBusy(true);
    setSaveError(null);
    try {
      await onSave("", model ?? (credential?.model || ""), true);
      setModel(undefined);
    } catch (err) {
      setSaveError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  // Model-only save (#227): empty secret tells the server to keep the sealed
  // key verbatim and update just the model.
  async function handleSaveModel() {
    if (!selectedModel || busy) return;
    setBusy(true);
    setSaveError(null);
    try {
      // A platform-provided voice-LLM slot has no row to update until the
      // tenant makes a choice: activate creates it (and is a no-op otherwise).
      if (voiceLlm && !saved) await onSave("", selectedModel, true);
      else await onSave("", selectedModel);
      setModel(undefined); // the refreshed credential now carries it
    } catch (err) {
      setSaveError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  async function handleSave() {
    if (!value || busy) return;
    setBusy(true);
    setSaveError(null);
    try {
      // Only an actively picked or already stored model rides along: the
      // combobox's passive first entry is a display default, and storing it
      // would pin the row to today's default (#702).
      await onSave(value, models ? (model ?? (credential?.model || "")) : undefined);
      setValue("");
      setEditing(false);
    } catch (err) {
      // A rejected save (e.g. FailedPrecondition when the sealing secret is
      // unset) must leave visible evidence — the key was NOT stored (#154).
      setSaveError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <div className="gx-provider-row">
        <span className="gx-provider-row__icon">{icon}</span>
        <div className="gx-provider-row__meta">
          <div className="gx-overline">{kind}</div>
          <div className="gx-provider-row__name">
            {name}
            {voiceLlm?.inEffect && (
              <Badge variant="info" size="sm">
                {t("config.badgeInUse")}
              </Badge>
            )}
          </div>
          {description && <div className="gx-provider-row__desc">{description}</div>}
          {health?.botTag && (
            <div className="gx-provider-row__tag">
              {t("config.connectedAs", { tag: health.botTag })}
            </div>
          )}
        </div>

        {/* Rendered whenever this slot HAS a model concept (groq), even with an
            empty catalog — a hard transport failure must not take free-text
            entry down with it (#227): allowCustom still accepts any id. */}
        {models && (
          <div className="gx-provider-row__model">
            <Combobox
              aria-label={t("config.modelAria", { name })}
              options={models.map((m) => ({ value: m, label: m }))}
              value={selectedModel}
              onValueChange={setModel}
              placeholder={t("config.modelPlaceholder")}
              searchPlaceholder={t("config.modelSearchPlaceholder")}
              allowCustom
            />
            {modelDirty && (
              <Button variant="secondary" size="sm" disabled={busy} onClick={handleSaveModel}>
                {t("config.saveModel")}
              </Button>
            )}
            {canUse && (
              <Button variant="secondary" size="sm" disabled={busy} onClick={handleUse}>
                {t("config.useForVoice")}
              </Button>
            )}
          </div>
        )}

        <div className="gx-secret">
          {masked ? (
            <div className="gx-secret__saved">
              <span className="gx-secret__mask" aria-label={t("config.secretSavedAria", { name })}>
                ••••••••
              </span>
              <Button
                variant="secondary"
                size="sm"
                iconStart={<RefreshCw size={13} />}
                onClick={() => {
                  setEditing(true);
                  setValue("");
                  setSaveError(null); // fresh edit starts without a stale cue
                }}
              >
                {t("config.replace")}
              </Button>
            </div>
          ) : (
            <div className="gx-secret__edit">
              <Input
                type="password"
                placeholder={placeholder}
                aria-label={t("config.secretKeyAria", { name })}
                value={value}
                onChange={(e) => setValue(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" && value && !busy) {
                    e.preventDefault();
                    void handleSave();
                  }
                }}
              />
              <Button variant="primary" size="sm" onClick={handleSave} disabled={!value || busy}>
                {t("common.save")}
              </Button>
              {editing && saved && (
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => {
                    setEditing(false);
                    setValue("");
                    // Back to masked+Healthy — a stale "Couldn't save" alert
                    // would contradict that state (#154).
                    setSaveError(null);
                  }}
                >
                  {t("common.cancel")}
                </Button>
              )}
            </div>
          )}
          {/* A key saved into the voice-LLM slot NOT in use makes it the newest
              LLM row, i.e. the voice model: say so before the save (#702). */}
          {voiceLlm && !voiceLlm.inEffect && !masked && (
            <span className="gx-secret__hint">{t("config.keySwitchesVoice", { name })}</span>
          )}
          {/* Inline failure cue, mirroring the agent editor's save status (#94). */}
          {saveError && (
            <span className="gx-secret__error" role="alert">
              {t("common.couldntSave", { message: saveError })}
            </span>
          )}
        </div>

        <HealthBadge
          saved={saved}
          health={health}
          optional={optional}
          platform={Boolean(credential?.providedByPlatform)}
        />
      </div>
    </Card>
  );
}
