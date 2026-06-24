import { useQuery } from "@connectrpc/connect-query";
import { Ear, BrainCircuit, AudioLines, Network } from "lucide-react";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import { Card } from "@/components/ui/Card";
import { Badge } from "@/components/ui/Badge";
import { Avatar } from "@/components/ui/Avatar";
import { Select } from "@/components/ui/Select";
import { Input } from "@/components/ui/Input";
import { Switch } from "@/components/ui/Switch";

import "./configuration.css";

// The Configuration screen (the design's "Providers" screen — ported from the
// handoff ui_kits/glyphoxa-web/settings.jsx). The campaign header reads LIVE
// GetActiveCampaign via connect-query (ADR-0018/0039) — real RPC data, not the
// mock's hardcoded arrays. The provider rows + Session-defaults render as inert
// DISABLED placeholders clearly marked "coming soon": their backend (Provider/
// voice/health RPCs) lands in later stages.

// The 4 provider rows mirror the design's shape. The MVP provider matrix
// (ADR-0039) is Groq (LLM) + ElevenLabs (STT + TTS); these rows are inert until
// ProviderService is wired, so the option lists are illustrative placeholders.
const PROVIDERS = [
  { kind: "STT", name: "ElevenLabs", icon: <Ear size={19} />, opts: ["ElevenLabs", "Deepgram", "whisper.cpp (local)"] },
  { kind: "LLM", name: "Groq", icon: <BrainCircuit size={19} />, opts: ["Groq", "Anthropic", "OpenAI"] },
  { kind: "TTS", name: "ElevenLabs", icon: <AudioLines size={19} />, opts: ["ElevenLabs", "OpenAI"] },
  { kind: "Embeddings", name: "OpenAI", icon: <Network size={19} />, opts: ["OpenAI", "Ollama (local)"] },
];

export function Configuration() {
  const { data, status, error } = useQuery(CampaignService.method.getActiveCampaign, {});
  const campaign = data?.campaign;

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

      {/* Provider rows — inert placeholders ("coming soon") */}
      <div className="gx-providers__list">
        {PROVIDERS.map((p) => (
          <Card key={p.kind}>
            <div className="gx-provider-row">
              <span className="gx-provider-row__icon">{p.icon}</span>
              <div className="gx-provider-row__meta">
                <div className="gx-overline">{p.kind}</div>
                <div className="gx-provider-row__name">{p.name}</div>
              </div>
              <div className="gx-provider-row__select">
                <Select options={p.opts} defaultValue={p.name} aria-label={p.kind + " provider"} disabled />
              </div>
              <Badge variant="neutral" dot size="sm">
                coming soon
              </Badge>
            </div>
          </Card>
        ))}
      </div>

      {/* Session defaults — inert placeholders ("coming soon") */}
      <h2 className="gx-section-title">Session defaults</h2>
      <Card>
        <div className="gx-defaults__body">
          <span className="gx-coming-soon">Coming soon</span>
          <div className="gx-defaults__grid">
            <Input label="Latency budget (ms)" defaultValue="1200" disabled />
            <Select label="Default engine" options={["Cascaded", "S2S — Gemini", "S2S — OpenAI"]} disabled />
          </div>
          <Switch label="Continuous live transcription" defaultChecked disabled />
          <Switch label="Speculative sentence cascade (experimental)" disabled />
        </div>
      </Card>
    </div>
  );
}
