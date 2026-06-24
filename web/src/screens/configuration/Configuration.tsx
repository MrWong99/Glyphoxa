import { useQuery } from "@connectrpc/connect-query";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";

import "./configuration.css";

// The Configuration screen (ported from the handoff settings.jsx). The campaign
// header (name / system / language) reads LIVE data from GetActiveCampaign via
// connect-query (ADR-0018/0039) — NOT the mock's hardcoded arrays. The provider
// keys, voice dropdown, and health/connection badge render as styled DISABLED
// "coming soon" placeholders until their RPCs land in later stages.

// The MVP provider matrix (ADR-0039): Groq (LLM) + ElevenLabs (STT + TTS). These
// placeholder rows mirror the design's shape; they wire to ProviderService later.
const PLACEHOLDER_PROVIDERS = [
  { component: "LLM", provider: "Groq" },
  { component: "STT", provider: "ElevenLabs" },
  { component: "TTS", provider: "ElevenLabs" },
] as const;

export function Configuration() {
  const { data, status, error } = useQuery(CampaignService.method.getActiveCampaign, {});
  const campaign = data?.campaign;

  return (
    <>
      <header className="page-header">
        <div>
          <h1 className="page-title">Configuration</h1>
          <p className="page-subtitle">
            Provider keys, voices, and the active campaign for this self-host.
          </p>
        </div>
        {/* Connection badge — placeholder until the Discord-presence RPC lands. */}
        <span className="tag">
          <span className="live-dot" data-state="idle" />
          Bot offline
        </span>
      </header>

      {/* Active campaign — LIVE GetActiveCampaign */}
      <section className="card">
        <div className="card-header">
          <div>
            <h2 className="card-title">Active campaign</h2>
            <p className="card-desc">Resolved from the seeded tenant.</p>
          </div>
        </div>

        {status === "pending" && (
          <div className="campaign-header" data-testid="campaign-loading">
            <span className="avatar" aria-hidden="true">
              …
            </span>
            <div className="campaign-meta">
              <span className="campaign-name">
                <span className="skeleton" />
              </span>
            </div>
          </div>
        )}

        {status === "error" && (
          <p className="campaign-error" role="alert">
            Could not load the active campaign: {error.message}
          </p>
        )}

        {status === "success" && campaign && (
          <div className="campaign-header">
            <span className="avatar">{campaign.name.charAt(0).toUpperCase()}</span>
            <div className="campaign-meta">
              <span className="campaign-name">{campaign.name}</span>
              <div className="campaign-attrs">
                <span className="campaign-attr">
                  <span className="campaign-attr-label">System</span>
                  <span className="campaign-attr-value">{campaign.system}</span>
                </span>
                <span className="campaign-attr">
                  <span className="campaign-attr-label">Language</span>
                  <span className="campaign-attr-value">{campaign.language}</span>
                </span>
              </div>
            </div>
          </div>
        )}
      </section>

      {/* Provider keys — coming soon (ProviderService, later stage) */}
      <section className="card" data-disabled="true">
        <div className="card-header">
          <div>
            <h2 className="card-title">Provider keys</h2>
            <p className="card-desc">Bring-your-own keys for LLM, STT, and TTS.</p>
          </div>
          <span className="coming-soon">coming soon</span>
        </div>
        {PLACEHOLDER_PROVIDERS.map((p) => (
          <div className="field" key={p.component}>
            <label className="field-label">
              {p.component} — {p.provider}
            </label>
            <div className="field-row">
              <input className="input" placeholder="•••• not yet wired" disabled />
              <button className="btn" data-variant="ghost" disabled>
                Replace
              </button>
            </div>
          </div>
        ))}
      </section>

      {/* NPC voice — coming soon (ElevenLabs ListVoices, later stage) */}
      <section className="card" data-disabled="true">
        <div className="card-header">
          <div>
            <h2 className="card-title">NPC voice</h2>
            <p className="card-desc">Default TTS voice for new NPCs.</p>
          </div>
          <span className="coming-soon">coming soon</span>
        </div>
        <div className="field">
          <label className="field-label">Voice</label>
          <button className="select" disabled>
            Not yet wired
            <span aria-hidden="true">▾</span>
          </button>
        </div>
      </section>
    </>
  );
}
