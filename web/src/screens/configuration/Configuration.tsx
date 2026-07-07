import { useEffect, useRef, useState, type ReactNode } from "react";
import { useQuery, useMutation, createConnectQueryKey } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { MessagesSquare, BrainCircuit, AudioLines, RefreshCw } from "lucide-react";

import {
  CampaignService,
  ProviderService,
  VoiceService,
  HealthStatus,
  type ProviderCredential,
  type ProviderHealth,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Card } from "@/components/ui/Card";
import { Badge } from "@/components/ui/Badge";
import { Avatar } from "@/components/ui/Avatar";
import { Input } from "@/components/ui/Input";
import { Combobox } from "@/components/ui/Combobox";
import { Button } from "@/components/ui/Button";
import { AddBotLink } from "./AddBotLink";
import { DiscordLinkAutofill } from "./DiscordLinkAutofill";

import "./configuration.css";

// The Configuration screen (the design's "Providers" screen). The campaign
// header reads LIVE GetActiveCampaign; the credential rows drive the write-only
// BYOK flow (#68, ADR-0004/0039): each secret is sealed server-side and never
// read back — the screen shows a masked value + Replace. The status badge starts
// from key-presence and upgrades async (#70) from VoiceService.GetProviderHealth:
// a real test-call (ElevenLabs /v1/voices, a Groq ping, a live Discord login that
// resolves the bot tag) flips it Healthy → Degraded without blocking page load.

// The three secret slots the screen holds, keyed by their wire `provider`. Each
// renders a SecretRow; Groq/ElevenLabs save via SaveProviderConfig, the Discord
// bot token via SaveDiscordSettings.
const BYOK_SLOTS = [
  { provider: "groq", label: "Groq", kind: "LLM", icon: <BrainCircuit size={19} />, placeholder: "Paste your Groq API key" },
  { provider: "elevenlabs", label: "ElevenLabs", kind: "Speech", icon: <AudioLines size={19} />, placeholder: "Paste your ElevenLabs API key" },
] as const;

function credentialFor(creds: ProviderCredential[], provider: string): ProviderCredential | undefined {
  return creds.find((c) => c.provider === provider);
}

function healthFor(health: ProviderHealth[], provider: string): ProviderHealth | undefined {
  return health.find((h) => h.provider === provider);
}

export function Configuration() {
  const { data, status, error } = useQuery(CampaignService.method.getActiveCampaign, {});
  const campaign = data?.campaign;

  const queryClient = useQueryClient();
  const invalidateList = () =>
    queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({ schema: ProviderService.method.listProviderConfigs, cardinality: "finite" }),
    });

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

  // Guild / Voice channel IDs are controlled, seeded from the RPC. A `dirty` ref
  // guards the seed so a slow first load (or a post-save refetch) can never
  // clobber what the operator is typing — once edited, the field is the source
  // of truth.
  const [guildId, setGuildId] = useState("");
  const [voiceChannelId, setVoiceChannelId] = useState("");
  const idsDirty = useRef(false);
  useEffect(() => {
    if (config.data && !idsDirty.current) {
      setGuildId(config.data.guildId);
      setVoiceChannelId(config.data.voiceChannelId);
    }
  }, [config.data]);
  const editGuildId = (v: string) => {
    idsDirty.current = true;
    setGuildId(v);
  };
  const editVoiceChannelId = (v: string) => {
    idsDirty.current = true;
    setVoiceChannelId(v);
  };

  // "Paste a Discord link" autofill (#101/#105): a channel Copy-Link fills both
  // IDs locally; an invite link resolves server-side to a voice-channel picker.
  // Either fill goes through the dirty-tracking edit path so a config refetch
  // can't clobber it (raw setState would), and Save picks the values up.
  const fillDiscordIds = (g: string, c: string) => {
    editGuildId(g);
    editVoiceChannelId(c);
  };

  return (
    <div className="gx-providers">
      <h1>Providers</h1>
      <p className="gx-providers__lede">Swap any engine with a config change — not a rewrite.</p>

      {/* Active campaign — LIVE GetActiveCampaign (ADR-0039) */}
      <Card accent className="gx-providers__campaign">
        <div className="gx-campaign">
          {status === "success" && campaign ? (
            <>
              <Avatar name={campaign.name} size="lg" />
              <div className="gx-campaign__meta">
                <span className="gx-overline">Active campaign</span>
                <span className="gx-campaign__name">{campaign.name}</span>
                <div className="gx-campaign__attrs">
                  <span className="gx-campaign__attr">
                    System
                    <span className="gx-campaign__attr-value">{campaign.system}</span>
                  </span>
                  <span className="gx-campaign__attr">
                    Language
                    <span className="gx-campaign__attr-value">{campaign.language}</span>
                  </span>
                </div>
              </div>
            </>
          ) : status === "error" ? (
            <p className="gx-campaign__error" role="alert">
              Could not load the active campaign: {error.message}
            </p>
          ) : (
            <div className="gx-campaign__meta" data-testid="campaign-loading">
              <span className="gx-overline">Active campaign</span>
              <span className="gx-skeleton" />
            </div>
          )}
        </div>
      </Card>

      {/* Provider keys — write-only BYOK (ADR-0004) */}
      <h2 className="gx-section-title">Provider keys</h2>
      <div className="gx-providers__list">
        {BYOK_SLOTS.map((slot) => (
          <SecretRow
            key={slot.provider}
            icon={slot.icon}
            kind={slot.kind}
            name={slot.label}
            placeholder={slot.placeholder}
            credential={credentialFor(creds, slot.provider)}
            health={healthFor(health, slot.provider)}
            // Groq's model combobox lists the live catalog with free-text entry
            // (#227); the chosen model rides along when the key is saved, or
            // alone via the model-only save once a key exists.
            models={slot.provider === "groq" ? models : undefined}
            onSave={(secret, model) => saveProvider.mutateAsync({ provider: slot.provider, secret, model })}
          />
        ))}
      </div>

      {/* Discord connection — Bot token (secret) + non-secret Guild / Voice IDs */}
      <h2 className="gx-section-title">Discord connection</h2>
      <Card>
        <div className="gx-discord">
          <SecretRow
            icon={<MessagesSquare size={19} />}
            kind="Bot"
            name="Bot token"
            placeholder="Paste the Discord bot token"
            credential={credentialFor(creds, "discord")}
            health={healthFor(health, "discord")}
            // Token-only save: the ID fields stay OFF the wire (they have proto3
            // presence), so replacing the token can never clobber the stored IDs —
            // even while the config load is still resolving (#142).
            onSave={(secret) => saveDiscord.mutateAsync({ botToken: secret })}
          />
          <DiscordLinkAutofill onFill={fillDiscordIds} />
          <div className="gx-discord__ids">
            <Input
              label="Guild ID"
              placeholder="e.g. 472093001100"
              hint="The Discord server the bot serves."
              value={guildId}
              onChange={(e) => editGuildId(e.target.value)}
            />
            <Input
              label="Voice channel ID"
              placeholder="472093774421"
              hint="The channel sessions join."
              value={voiceChannelId}
              onChange={(e) => editVoiceChannelId(e.target.value)}
            />
          </div>
          <div className="gx-discord__save">
            <Button
              variant="primary"
              size="sm"
              onClick={() => saveDiscordIds.mutate({ guildId, voiceChannelId })}
              // Locked until BOTH IDs are non-empty: the server rejects
              // present-but-empty IDs (#142), so a half-filled save must not be
              // offered — clicking it used to fail with no visible trace.
              disabled={saveDiscordIds.isPending || !guildId || !voiceChannelId}
            >
              Save Discord settings
            </Button>
            {/* Inline failure cue, mirroring the SecretRow save-error treatment:
                a rejected save must leave visible evidence the IDs were NOT
                stored, or it resurfaces as a session-start failure (#142). */}
            {saveDiscordIds.isError && (
              <span className="gx-discord__error" role="alert">
                Couldn&apos;t save: {saveDiscordIds.error.message}
              </span>
            )}
          </div>
          {/* Authorizing the Bot into the Guild is a separate, prerequisite step
              from saving the IDs — neither pasted-link format joins the Bot (#110).
              Gated on a resolved read so the "no application id" note can't flash
              while the config loads, nor stick on a query error — only a genuine
              empty id (query succeeded) is the disabled fallback. */}
          {config.isSuccess && <AddBotLink applicationId={config.data.discordApplicationId} />}
        </div>
      </Card>

      {/* Per-session spend caps (#130, ADR-0046) */}
      <h2 className="gx-section-title">Spend caps</h2>
      <SpendCapsCard />
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
  const queryClient = useQueryClient();
  const capsQuery = useQuery(ProviderService.method.getSpendCaps, {});
  const invalidate = () =>
    queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({ schema: ProviderService.method.getSpendCaps, cardinality: "finite" }),
    });
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

  return (
    <Card>
      <div className="gx-spendcaps">
        <p className="gx-spendcaps__lede">
          Stop a Voice Session when its estimated provider spend crosses a limit. Figures are estimates,
          not billed amounts. Leave a field blank to disable that cap; changes apply to the next session.
        </p>
        <div className="gx-spendcaps__inputs">
          <Input
            label="Soft cap (USD)"
            type="number"
            min="0"
            step="0.01"
            placeholder="e.g. 5.00"
            hint="No new Agent turns once crossed; in-flight replies finish."
            value={soft}
            onChange={(e) => editSoft(e.target.value)}
          />
          <Input
            label="Hard cap (USD)"
            type="number"
            min="0"
            step="0.01"
            placeholder="e.g. 10.00"
            hint="Ends the session cleanly. Must be ≥ the soft cap."
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
            Save spend caps
          </Button>
          {save.isError && (
            <span className="gx-spendcaps__error" role="alert">
              Couldn&apos;t save: {save.error.message}
            </span>
          )}
        </div>
      </div>
    </Card>
  );
}

// HealthBadge renders the status dot. An unsaved slot is "Key needed" (presence).
// A saved slot shows "Healthy" instantly (presence) and downgrades to "Degraded"
// only once GetProviderHealth reports a failed test-call (#70) — the page never
// waits on the live call.
function HealthBadge({ saved, health }: { saved: boolean; health?: ProviderHealth }) {
  if (!saved) {
    return (
      <Badge variant="warning" dot size="sm">
        Key needed
      </Badge>
    );
  }
  if (health?.status === HealthStatus.DEGRADED) {
    return (
      <Badge variant="danger" dot size="sm" title={health.detail || undefined}>
        Degraded
      </Badge>
    );
  }
  return (
    <Badge variant="success" dot size="sm">
      Healthy
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
  placeholder,
  credential,
  health,
  models,
  onSave,
}: {
  icon: ReactNode;
  kind: string;
  name: string;
  placeholder: string;
  credential?: ProviderCredential;
  health?: ProviderHealth;
  models?: string[];
  onSave: (secret: string, model?: string) => Promise<unknown>;
}) {
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
      setSaveError(err instanceof Error ? err.message : String(err));
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
      setSaveError(err instanceof Error ? err.message : String(err));
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
          {health?.botTag && (
            <div className="gx-provider-row__tag">Connected as {health.botTag}</div>
          )}
        </div>

        {/* Rendered whenever this slot HAS a model concept (groq), even with an
            empty catalog — a hard transport failure must not take free-text
            entry down with it (#227): allowCustom still accepts any id. */}
        {models && (
          <div className="gx-provider-row__model">
            <Combobox
              aria-label={`${name} model`}
              options={models.map((m) => ({ value: m, label: m }))}
              value={selectedModel}
              onValueChange={setModel}
              placeholder="Model…"
              searchPlaceholder="Search or type a model id…"
              allowCustom
            />
            {modelDirty && (
              <Button variant="secondary" size="sm" disabled={busy} onClick={handleSaveModel}>
                Save model
              </Button>
            )}
          </div>
        )}

        <div className="gx-secret">
          {masked ? (
            <div className="gx-secret__saved">
              <span className="gx-secret__mask" aria-label={`${name} saved`}>
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
                Replace
              </Button>
            </div>
          ) : (
            <div className="gx-secret__edit">
              <Input
                type="password"
                placeholder={placeholder}
                aria-label={`${name} key`}
                value={value}
                onChange={(e) => setValue(e.target.value)}
              />
              <Button variant="primary" size="sm" onClick={handleSave} disabled={!value || busy}>
                Save
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
                  Cancel
                </Button>
              )}
            </div>
          )}
          {/* Inline failure cue, mirroring the agent editor's save status (#94). */}
          {saveError && (
            <span className="gx-secret__error" role="alert">
              Couldn't save: {saveError}
            </span>
          )}
        </div>

        <HealthBadge saved={saved} health={health} />
      </div>
    </Card>
  );
}
