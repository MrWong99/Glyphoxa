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
import { Select } from "@/components/ui/Select";
import { Button } from "@/components/ui/Button";

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

  // Groq model allowlist (static; Groq has no list-models API, ADR-0039).
  const groqModels = useQuery(VoiceService.method.listModels, { provider: "groq" });
  const models = groqModels.data?.models ?? [];

  const saveProvider = useMutation(ProviderService.method.saveProviderConfig, { onSuccess: invalidateList });
  const saveDiscord = useMutation(ProviderService.method.saveDiscordSettings, { onSuccess: invalidateList });

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
            // Groq's model select is the static allowlist (#70); the chosen model
            // rides along when the key is saved (SaveProviderConfig carries it).
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
            onSave={(secret) =>
              saveDiscord.mutateAsync({ botToken: secret, guildId, voiceChannelId })
            }
          />
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
              onClick={() => saveDiscord.mutate({ guildId, voiceChannelId })}
              disabled={saveDiscord.isPending}
            >
              Save Discord settings
            </Button>
          </div>
        </div>
      </Card>

      {/* Session defaults — inert placeholders ("coming soon") */}
      <h2 className="gx-section-title">Session defaults</h2>
      <Card>
        <div className="gx-defaults__body">
          <span className="gx-coming-soon">Coming soon</span>
        </div>
      </Card>
    </div>
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
  const [model, setModel] = useState<string | undefined>(undefined);

  const saved = Boolean(credential?.showMasked);
  const masked = saved && !editing;
  // The select shows the saved model, the operator's pick, or the allowlist
  // default (first), in that order.
  const selectedModel = model ?? (credential?.model || undefined) ?? models?.[0];

  async function handleSave() {
    if (!value || busy) return;
    setBusy(true);
    try {
      await onSave(value, selectedModel);
      setValue("");
      setEditing(false);
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

        {models && models.length > 0 && (
          <Select
            aria-label={`${name} model`}
            options={models}
            value={selectedModel}
            onValueChange={setModel}
            placeholder="Model…"
          />
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
                  }}
                >
                  Cancel
                </Button>
              )}
            </div>
          )}
        </div>

        <HealthBadge saved={saved} health={health} />
      </div>
    </Card>
  );
}
