import { useState, type ReactNode } from "react";
import { useQuery, useMutation } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { BrainCircuit, AudioLines, ImagePlus, Undo2 } from "lucide-react";

import {
  CampaignService,
  ProviderService,
  SessionService,
  VoiceService,
  type ListProviderConfigsResponse,
  type ProviderCredential,
  type ProviderHealth,
} from "@gen/glyphoxa/management/v1/management_pb";
import { useScreenNav } from "@/app/screenNav";
import { Page, PageHeader } from "@/components/ui/Page";
import { Button } from "@/components/ui/Button";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import { ErrorState } from "@/components/ui/ErrorState";
import { Skeleton } from "@/components/ui/Skeleton";
import { FirstCampaignCTA } from "@/components/FirstCampaignCTA";
import { errorMessage, isNotFound } from "@/lib/connectError";
import { invalidateMethodQueries } from "@/lib/queryClient";
import { deriveOptionalSetupItems, deriveSetupSteps, SETUP_ANCHORS } from "@/lib/readiness";
import { useI18n, type MessageKey } from "@/i18n";
import { ChatModelCard } from "./ChatModelCard";
import { DiscordCard } from "./DiscordCard";
import { llmProviderLabel } from "./llmProviders";
import { PlanUsageCard } from "./PlanUsageCard";
import { SecretRow } from "./SecretRow";
import { jumpTo, SetupChecklist } from "./SetupChecklist";
import { SpendLimitsCard } from "./SpendLimitsCard";
import { TableCard } from "./TableCard";

import "./configuration.css";

// The Setup screen: an ordered path for the whole table (#695). It opens with
// the checklist (a campaign, a linked Discord server, voice & speech, an AI
// model) and the plan and usage card, then the sections those steps link to.
// The credential rows drive the write-only BYOK flow (#68, ADR-0004/0039):
// each secret is sealed server-side and never read back — the screen shows a
// masked value + Replace. The status badge starts from key-presence and
// upgrades async (#70) from VoiceService.GetProviderHealth.
//
// Honest loading: the Discord card and every key row are gated on the one
// ListProviderConfigs read. While it is out they are skeletons (never "Key
// needed"); when it fails there is ONE error with Retry and no editable row,
// because a row drawn from no data would offer to "add" a key the table
// already has.

// The BYOK key slots. `label` is the product name (never translated); `kind`
// is the plain-language overline; `description` says what the service does
// at the table; `getKeyUrl` is where a key is created (checked 2026-11-26).
// The two voice-LLM slots share component "llm" and are told apart by
// provider (#702). The Planning chat is NOT a slot: its model is a choice
// over these providers, rendered by ChatModelCard (ADR-0062 amendment).
type ByokSlot = {
  provider: string;
  component: string;
  label: string;
  kind: MessageKey;
  description: MessageKey;
  getKeyUrl: string;
  icon: ReactNode;
  /** Renders the model combobox (the live catalog + free text, #227). */
  models?: boolean;
  /** Not needed to run a session: an unsaved key reads "Optional", not "Key needed". */
  optional?: boolean;
  /** A voice-LLM slot (#702): one of the providers a table chooses BETWEEN. */
  voiceLlm?: boolean;
  /** The DOM id the checklist links to. */
  anchor?: string;
};

const VOICE_LLM_SLOTS: readonly ByokSlot[] = [
  {
    provider: "groq",
    component: "llm",
    label: llmProviderLabel("groq"),
    kind: "config.kindAiModel",
    description: "config.descGroq",
    getKeyUrl: "https://console.groq.com/keys",
    icon: <BrainCircuit size={19} />,
    models: true,
    voiceLlm: true,
  },
  {
    provider: "anthropic",
    component: "llm",
    label: llmProviderLabel("anthropic"),
    kind: "config.kindAiModel",
    description: "config.descAnthropic",
    getKeyUrl: "https://platform.claude.com/settings/keys",
    icon: <BrainCircuit size={19} />,
    models: true,
    voiceLlm: true,
  },
];

const OTHER_SLOTS: readonly ByokSlot[] = [
  {
    provider: "elevenlabs",
    component: "tts",
    label: "ElevenLabs",
    kind: "config.kindVoiceSpeech",
    description: "config.descElevenLabs",
    getKeyUrl: "https://elevenlabs.io/app/developers/api-keys",
    icon: <AudioLines size={19} />,
    anchor: SETUP_ANCHORS.voice,
  },
  {
    provider: "gemini",
    component: "image",
    label: "Gemini",
    kind: "config.kindImages",
    description: "config.descGemini",
    getKeyUrl: "https://aistudio.google.com/apikey",
    icon: <ImagePlus size={19} />,
    optional: true,
    anchor: SETUP_ANCHORS.images,
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
// in use whatever provider name it carries. A server that predates
// `component` is matched by provider, as before.
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
  const { goScreen } = useScreenNav();
  // retry:false so a fresh install's CodeNotFound settles at once into the
  // create-a-campaign step rather than backing off through three retries.
  const campaignQ = useQuery(CampaignService.method.getActiveCampaign, {}, { retry: false });
  const campaign = campaignQ.data?.campaign;
  const noCampaign = campaignQ.status === "error" && isNotFound(campaignQ.error);

  const queryClient = useQueryClient();
  // A credential or Discord save changes more than the list: the live health
  // probe, the model and voice catalogs (fetched WITH the key) and the linked
  // guild's voice channels / members all answer differently afterwards.
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
  // failureReason is set from the FIRST failed attempt, while the query is
  // still retrying: the screen says the read is failing right away rather
  // than shimmering through the retry backoff.
  const configFailed = !config.data && Boolean(config.error ?? config.failureReason);

  // Async health upgrade (#70): runs the live test-calls off the page-load path.
  const healthQuery = useQuery(VoiceService.method.getProviderHealth, {});
  const health = healthQuery.data?.providers ?? [];
  const healthPending = healthQuery.isPending;

  const capsQuery = useQuery(ProviderService.method.getSpendCaps, {});
  // The same read PlanUsageCard makes (one request): whether sessions are
  // paused by a used-up allowance, and whether the plan has platform keys.
  const usageQuery = useQuery(ProviderService.method.getUsageSummary, {});
  // Bumped by the checklist's Spending limits link: the card opens on arrival.
  const [limitsOpened, setLimitsOpened] = useState(0);

  const steps = deriveSetupSteps({
    hasCampaign: campaign ? true : noCampaign ? false : undefined,
    providers: config.data,
  });
  const optional = deriveOptionalSetupItems({ providers: config.data, spendCaps: capsQuery.data });
  // A read that failed (not one still out) leaves its steps "couldn't check".
  const campaignFailed = campaignQ.isError && !noCampaign;
  const unavailable = {
    campaign: campaignFailed,
    discord: configFailed,
    voice: configFailed,
    aiModel: configFailed,
    images: configFailed,
    spendingLimits: !capsQuery.data && capsQuery.isError,
  };

  return (
    <Page width="narrow" className="gx-setup">
      <PageHeader title={t("config.title")} description={t("config.lede")} />
      {/* Setup is the table's, not a campaign's: say so where the old
          "Active campaign" card used to suggest otherwise. */}
      <p className="gx-setup__scope">{t("config.scopeLine")}</p>

      <SetupChecklist
        steps={steps}
        optional={optional}
        details={{
          campaign: campaign ? t("config.stepCampaignDone", { name: campaign.name }) : undefined,
          discord: config.data?.guildId
            ? t("config.stepDiscordDone", { server: config.data.guildName || config.data.guildId })
            : undefined,
        }}
        bodies={{
          campaign: noCampaign ? (
            <FirstCampaignCTA />
          ) : campaignQ.isError ? (
            <ErrorState
              size="inline"
              title={t("config.couldntLoadCampaignTitle")}
              message={errorMessage(campaignQ.error)}
              onRetry={() => void campaignQ.refetch()}
              retrying={campaignQ.isFetching}
            />
          ) : undefined,
        }}
        go={{
          campaign: () => goScreen("campaigns"),
          spendingLimits: () => {
            setLimitsOpened((n) => n + 1);
            jumpTo(SETUP_ANCHORS.spendingLimits);
          },
        }}
        unavailable={unavailable}
        exhausted={Boolean(usageQuery.data?.exhausted)}
      />

      <PlanUsageCard />

      {configFailed ? (
        // The checklist's Discord and key links land here while there is
        // nothing else to land on: on the error, with its Retry.
        <div id={SETUP_ANCHORS.configStatus} tabIndex={-1} className="gx-setup-section">
          <ErrorState
            title={t("config.loadErrorTitle")}
            description={t("config.loadErrorDesc")}
            message={errorMessage(config.error ?? config.failureReason)}
            onRetry={() => void config.refetch()}
            retrying={config.isFetching}
          />
        </div>
      ) : !config.data ? (
        <SetupSkeleton />
      ) : (
        <>
          <section
            id={SETUP_ANCHORS.discord}
            tabIndex={-1}
            className="gx-setup-section"
            aria-labelledby="setup-discord-title"
          >
            <h2 id="setup-discord-title" className="gx-section-title">
              {t("config.discordTitle")}
            </h2>
            <DiscordCard
              config={config.data}
              botCredential={credentialFor(creds, "discord")}
              botHealth={healthFor(health, { component: "discord", provider: "discord" })}
              healthPending={healthPending}
              onChanged={invalidateList}
            />
          </section>

          <KeysSection
            creds={creds}
            health={health}
            healthPending={healthPending}
            config={config.data}
            // Unknown (still loading, or failed) keeps the way back offered:
            // only a plan known to have no platform keys hides it.
            platformDefault={usageQuery.data?.usesPlatformKeys !== false}
            onChanged={invalidateList}
          />
        </>
      )}

      <SpendLimitsCard anchor={SETUP_ANCHORS.spendingLimits} openSignal={limitsOpened} />

      <TableCard />
    </Page>
  );
}

// The skeleton of the Discord card and the key rows: the shape of what is
// coming, announced once, and nothing a GM could mistake for "Key needed".
function SetupSkeleton() {
  const { t } = useI18n();
  return (
    <div
      id={SETUP_ANCHORS.configStatus}
      tabIndex={-1}
      className="gx-setup-section gx-setup-skeleton"
      data-testid="config-loading"
    >
      <Skeleton.Row count={5} label={t("config.loadingKeys")} />
    </div>
  );
}

function KeysSection({
  creds,
  health,
  healthPending,
  config,
  platformDefault,
  onChanged,
}: {
  creds: ProviderCredential[];
  health: ProviderHealth[];
  healthPending: boolean;
  config: ListProviderConfigsResponse;
  /** The plan has a platform default to go back to (GetUsageSummary.uses_platform_keys). */
  platformDefault: boolean;
  onChanged: () => void;
}) {
  const { t } = useI18n();
  const effectiveLlm = config.effectiveLlm;
  const [confirmDefault, setConfirmDefault] = useState(false);

  // Live model catalogs (#227, #702): fetched with each slot's key; the server
  // degrades to the default on a failed fetch, and the combobox accepts free
  // text either way, so these queries can never wedge the screen.
  const groqModels = useQuery(VoiceService.method.listModels, { provider: "groq" });
  const anthropicModels = useQuery(VoiceService.method.listModels, { provider: "anthropic" });
  const modelsByProvider: Record<string, string[]> = {
    groq: groqModels.data?.models ?? [],
    anthropic: anthropicModels.data?.models ?? [],
  };

  const saveProvider = useMutation(ProviderService.method.saveProviderConfig, { onSuccess: onChanged });
  // The Planning chat model choice (ADR-0062 amendment): its own mutation
  // instances, so a refused chat save paints the chat card and never a key row.
  const saveChatModel = useMutation(ProviderService.method.saveProviderConfig, { onSuccess: onChanged });
  const resetChatModel = useMutation(ProviderService.method.deleteProviderConfig, { onSuccess: onChanged });
  // Removing a voice LLM key (ADR-0004 amendment 2026-11-26): one slot from
  // its row, or both with Back to platform default. Two mutation instances, so
  // a row's removal never spins (or paints its error on) the group's button.
  const removedToast = (what: string, chatModelReset: boolean) =>
    toast.success(what, chatModelReset ? { description: t("config.toastChatModelReset") } : undefined);
  const removeKey = useMutation(ProviderService.method.deleteProviderConfig, {
    onSuccess: (res, req) => {
      removedToast(t("config.toastKeyRemoved", { name: llmProviderLabel(req.slot ?? "") }), res.chatModelReset);
      onChanged();
    },
  });
  const backToDefault = useMutation(ProviderService.method.deleteProviderConfig, {
    onSuccess: (res) => {
      removedToast(t("config.toastBackToDefault"), res.chatModelReset);
      onChanged();
    },
  });

  const row = (slot: ByokSlot) => {
    const credential = credentialFor(creds, slot.component, slot.voiceLlm ? slot.provider : undefined);
    // Until the list answers, assume the code default (Groq) is the voice LLM
    // in use (the list has answered by now; this is the shape for an old server).
    const llmInUse = effectiveLlm ? effectiveLlm.provider === slot.provider : slot.provider === "groq";
    return (
      <SecretRow
        key={`${slot.component}/${slot.provider}`}
        anchor={slot.anchor}
        icon={slot.icon}
        kind={t(slot.kind)}
        name={slot.label}
        description={t(slot.description)}
        getKeyUrl={slot.getKeyUrl}
        placeholder={t("config.pasteApiKey", { name: slot.label })}
        credential={credential}
        health={healthFor(health, slot, llmInUse)}
        healthPending={healthPending}
        // A voice-LLM slot the table is not on needs no key: it is a choice.
        optional={slot.optional || (slot.voiceLlm && !llmInUse)}
        voiceLlm={slot.voiceLlm ? { inEffect: Boolean(credential?.inEffect) } : undefined}
        models={slot.models ? (modelsByProvider[slot.provider] ?? []) : undefined}
        onSave={(secret, model, activate) =>
          saveProvider.mutateAsync({ provider: slot.provider, secret, model, activate })
        }
        onRemove={slot.voiceLlm ? () => removeKey.mutateAsync({ slot: slot.provider }) : undefined}
      />
    );
  };

  return (
    <section className="gx-setup-section" aria-labelledby="setup-keys-title">
      <h2 id="setup-keys-title" className="gx-section-title">
        {t("config.aiServiceKeysTitle")}
      </h2>

      <section id={SETUP_ANCHORS.aiModel} tabIndex={-1} className="gx-voice-llm" aria-labelledby="setup-ai-title">
        <div className="gx-voice-llm__head">
          <h3 id="setup-ai-title" className="gx-voice-llm__title">
            {t("config.voiceLlmTitle")}
          </h3>
          {/* Which LLM a Voice Session started now would run (#702). */}
          {effectiveLlm && (
            <p className="gx-providers__llm-in-use" data-testid="llm-in-use">
              {t(effectiveLlm.platformDefault ? "config.llmInUsePlatform" : "config.llmInUseOwn", {
                provider: llmProviderLabel(effectiveLlm.provider),
                model: effectiveLlm.model,
              })}
            </p>
          )}
          {effectiveLlm && !effectiveLlm.platformDefault && platformDefault && (
            <Button
              variant="ghost"
              size="sm"
              iconStart={<Undo2 size={14} />}
              loading={backToDefault.isPending}
              onClick={() => setConfirmDefault(true)}
            >
              {t("config.backToDefault")}
            </Button>
          )}
        </div>
        {/* A failed single-key removal shows on its row; this is "Back to platform default". */}
        {backToDefault.isError && (
          <p className="gx-secret__error" role="alert">
            {t("config.couldntRemove", { message: errorMessage(backToDefault.error) })}
          </p>
        )}
        <div className="gx-providers__list">{VOICE_LLM_SLOTS.map(row)}</div>
      </section>

      <section className="gx-voice-llm" aria-labelledby="setup-other-title">
        <h3 id="setup-other-title" className="gx-voice-llm__title">
          {t("config.otherServicesTitle")}
        </h3>
        <div className="gx-providers__list">
          {OTHER_SLOTS.map(row)}
          {/* The Planning chat model: a choice over the providers above, with
            the truthful "answers with" line (ADR-0062 amendment). */}
          <ChatModelCard
            effective={config.effectiveChatLlm}
            voice={effectiveLlm}
            onSave={(provider, model, secret) => saveChatModel.mutateAsync({ slot: "chat", provider, model, secret })}
            onReset={() => resetChatModel.mutateAsync({ slot: "chat" })}
          />
        </div>
      </section>

      <ConfirmDialog
        open={confirmDefault}
        onOpenChange={setConfirmDefault}
        title={t("config.backToDefaultTitle")}
        description={t("config.backToDefaultDesc")}
        confirmLabel={t("config.backToDefault")}
        onConfirm={() => {
          setConfirmDefault(false);
          backToDefault.mutate({ slot: "voice_llm" });
        }}
      />
    </section>
  );
}
