import { useEffect, useRef, useState, type ReactNode } from "react";
import { useQuery, useMutation } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import {
  MessagesSquare,
  MessageCircle,
  BrainCircuit,
  AudioLines,
  ImagePlus,
  RefreshCw,
} from "lucide-react";

import {
  CampaignService,
  ProviderService,
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
import { CreateCampaignForm, useCreateCampaign } from "@/components/CreateCampaignForm";
import { errorMessage, isNotFound } from "@/lib/connectError";
import { invalidateMethodQueries } from "@/lib/queryClient";
import { useI18n, type MessageKey } from "@/i18n";
import { AddBotLink } from "./AddBotLink";
import { DiscordLinkAutofill } from "./DiscordLinkAutofill";

import "./configuration.css";

// The Configuration screen (the design's "Setup" screen). The campaign
// header reads LIVE GetActiveCampaign; the credential rows drive the write-only
// BYOK flow (#68, ADR-0004/0039): each secret is sealed server-side and never
// read back — the screen shows a masked value + Replace. The status badge starts
// from key-presence and upgrades async (#70) from VoiceService.GetProviderHealth:
// a real test-call (ElevenLabs /v1/voices, a Groq ping, a live Discord login that
// resolves the bot tag) flips it Healthy → Degraded without blocking page load.

// The BYOK secret slots the screen holds. Each renders a SecretRow;
// Groq/ElevenLabs/Gemini save via SaveProviderConfig, the Discord bot token via
// SaveDiscordSettings. `label` is the product name (never translated); an
// optional `nameKey` overrides the displayed name with localized copy (the
// chat slot, whose name is a feature, not a product). `kind` is a MessageKey
// translated at render, so the overline speaks the user's language ("AI
// model", never the internal "LLM").
//
// `slot` is SaveProviderConfigRequest.slot — empty means the slot IS the
// provider (every pre-0062 slot); the planning-chat slot saves as slot "chat"
// with provider "groq" (#592, ADR-0062). `component` is the credential's
// primary Component label, unique per slot (discord, llm, tts, image,
// chat_llm) — the row's credential lookup keys on it, because two slots can
// now share one provider.
type ByokSlot = {
  provider: string;
  slot: string;
  component: string;
  label: string;
  nameKey?: MessageKey;
  descriptionKey?: MessageKey;
  kind: MessageKey;
  icon: ReactNode;
  /** Renders the Groq model combobox (the live catalog + free text, #227). */
  models?: boolean;
  /**
   * Suppresses the row's health badge. The chat slot sets it: the shared groq
   * probe tests the VOICE-LLM credential, not the chat_llm key, so its verdict
   * could contradict this row's actual state. A dedicated chat_llm probe is a
   * follow-up; until then no badge beats a wrong one.
   */
  hideHealth?: boolean;
};
const BYOK_SLOTS: readonly ByokSlot[] = [
  {
    provider: "groq",
    slot: "",
    component: "llm",
    label: "Groq",
    kind: "config.kindAiModel",
    icon: <BrainCircuit size={19} />,
    models: true,
  },
  {
    provider: "elevenlabs",
    slot: "",
    component: "tts",
    label: "ElevenLabs",
    kind: "config.kindVoiceSpeech",
    icon: <AudioLines size={19} />,
  },
  {
    provider: "gemini",
    slot: "",
    component: "image",
    label: "Gemini",
    kind: "config.kindImages",
    icon: <ImagePlus size={19} />,
  },
  // The chat-scoped model slot (ADR-0062): a second selection on the same Groq
  // key surface, chosen for quality over latency, so upgrading the planning
  // chat can never re-price or slow the live voice loop.
  {
    provider: "groq",
    slot: "chat",
    component: "chat_llm",
    label: "Groq",
    nameKey: "chat.configSlotName",
    descriptionKey: "chat.configSlotDesc",
    kind: "config.kindAiModel",
    icon: <MessageCircle size={19} />,
    models: true,
    hideHealth: true,
  },
];

// Rows key their credential on `component`, not `provider`: since the chat
// slot (#592) two slots share provider "groq", but every slot's primary
// component is unique.
function credentialFor(creds: ProviderCredential[], component: string): ProviderCredential | undefined {
  return creds.find((c) => c.component === component);
}

function healthFor(health: ProviderHealth[], provider: string): ProviderHealth | undefined {
  return health.find((h) => h.provider === provider);
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
  const invalidateList = () =>
    invalidateMethodQueries(queryClient, ProviderService.method.listProviderConfigs);

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
  const models = groqModels.data?.models ?? [];

  const saveProvider = useMutation(ProviderService.method.saveProviderConfig, { onSuccess: invalidateList });
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
      <div className="gx-providers__list">
        {BYOK_SLOTS.map((slot) => (
          <SecretRow
            key={slot.component}
            icon={slot.icon}
            kind={t(slot.kind)}
            name={slot.nameKey ? t(slot.nameKey) : slot.label}
            description={slot.descriptionKey ? t(slot.descriptionKey) : undefined}
            // The paste prompt names the PRODUCT whose key it wants, even when
            // the row's display name is a feature ("Planning chat (Groq)").
            placeholder={t("config.pasteApiKey", { name: slot.label })}
            credential={credentialFor(creds, slot.component)}
            // The probe result is per-provider; a hideHealth slot renders no
            // badge at all (see ByokSlot.hideHealth), so it gets none passed.
            health={slot.hideHealth ? undefined : healthFor(health, slot.provider)}
            hideHealth={slot.hideHealth}
            // Groq-backed rows list the live model catalog with free-text entry
            // (#227); the chosen model rides along when the key is saved, or
            // alone via the model-only save once a key exists.
            models={slot.models ? models : undefined}
            onSave={(secret, model) =>
              saveProvider.mutateAsync({ provider: slot.provider, secret, model, slot: slot.slot })
            }
          />
        ))}
      </div>

      {/* Discord connection — Bot token (secret) + non-secret Guild / Voice IDs */}
      <h2 className="gx-section-title">{t("config.discordTitle")}</h2>
      <Card>
        <div className="gx-discord">
          <SecretRow
            icon={<MessagesSquare size={19} />}
            kind={t("config.kindBot")}
            name={t("config.botTokenName")}
            placeholder={t("config.pasteBotToken")}
            credential={credentialFor(creds, "discord")}
            health={healthFor(health, "discord")}
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

// FirstCampaignCTA replaces the active-campaign card on a fresh, unseeded install
// (GetActiveCampaign → CodeNotFound). It runs the shared create-then-activate flow
// (#267): creating here mints the campaign (auto-Butler, ADR-0009) and selects it,
// and the sweep re-resolves GetActiveCampaign so this card swaps itself for the
// live header — no reload, no `glyphoxa seed`.
function FirstCampaignCTA() {
  const { t } = useI18n();
  const create = useCreateCampaign();
  return (
    <div className="gx-first-campaign">
      <span className="gx-overline">{t("config.activeCampaign")}</span>
      <h2 className="gx-first-campaign__title">{t("config.firstCampaignTitle")}</h2>
      <p className="gx-first-campaign__lede">{t("config.firstCampaignLede")}</p>
      <CreateCampaignForm onSubmit={create.submit} pending={create.pending} error={create.error} autoFocusName />
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
          disabled={save.isPending}
          onClick={() => save.mutate({ softUsd: parse(soft), hardUsd: parse(hard) })}
        >
          {t("config.saveSpendingLimits")}
        </Button>
        {save.isError && (
          <span className="gx-spendcaps__error" role="alert">
            {t("common.couldntSave", { message: errorMessage(save.error) })}
          </span>
        )}
      </div>
    </div>
  );
}

// HealthBadge renders the status dot. An unsaved slot is "Key needed" (presence).
// A saved slot shows "Working" instantly (presence) and downgrades to "Having
// trouble" only once GetProviderHealth reports a failed test-call (#70) — the
// page never waits on the live call.
function HealthBadge({ saved, health }: { saved: boolean; health?: ProviderHealth }) {
  const { t } = useI18n();
  if (!saved) {
    return (
      <Badge variant="warning" dot size="sm">
        {t("config.badgeKeyNeeded")}
      </Badge>
    );
  }
  if (health?.status === HealthStatus.DEGRADED) {
    return (
      <Badge variant="danger" dot size="sm" title={health.detail || undefined}>
        {t("config.badgeDegraded")}
      </Badge>
    );
  }
  return (
    <Badge variant="success" dot size="sm">
      {t("config.badgeWorking")}
    </Badge>
  );
}

// IntegrationBadge renders THIS tenant's standing Discord client health (#489):
// the per-tenant registry reports "ok" (client up), "waiting" (no Bot token yet),
// or "failed" (the token was rejected terminally — revoked/invalid) with a
// human detail. Empty state (web-only mode, no standing presence) renders nothing.
function IntegrationBadge({ state, detail }: { state: string; detail: string }) {
  const { t } = useI18n();
  if (state === "" || state === "waiting") {
    return null;
  }
  if (state === "failed") {
    return (
      <Badge variant="danger" dot size="sm" title={detail || undefined}>
        {t("config.discordIntegrationFailed")}
      </Badge>
    );
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
  hideHealth = false,
  onSave,
}: {
  icon: ReactNode;
  kind: string;
  name: string;
  /** Optional one-liner under the name (the chat slot's "voice stays untouched"). */
  description?: string;
  placeholder: string;
  credential?: ProviderCredential;
  health?: ProviderHealth;
  models?: string[];
  /**
   * Renders the row without ANY health badge (chat slot): the shared groq
   * probe tests the voice-LLM credential, whose verdict can contradict this
   * row's chat_llm key. A dedicated chat_llm probe is a follow-up.
   */
  hideHealth?: boolean;
  onSave: (secret: string, model?: string) => Promise<unknown>;
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
  const modelDirty =
    saved && model !== undefined && model !== (credential?.model || undefined);

  // Model-only save (#227): empty secret tells the server to keep the sealed
  // key verbatim and update just the model.
  async function handleSaveModel() {
    if (!selectedModel || busy) return;
    setBusy(true);
    setSaveError(null);
    try {
      await onSave("", selectedModel);
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
      await onSave(value, selectedModel);
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
          <div className="gx-provider-row__name">{name}</div>
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
          {/* Inline failure cue, mirroring the agent editor's save status (#94). */}
          {saveError && (
            <span className="gx-secret__error" role="alert">
              {t("common.couldntSave", { message: saveError })}
            </span>
          )}
        </div>

        {!hideHealth && <HealthBadge saved={saved} health={health} />}
      </div>
    </Card>
  );
}
