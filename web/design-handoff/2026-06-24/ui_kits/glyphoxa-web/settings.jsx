// Glyphoxa Design System — Configuration screen (ui_kits/glyphoxa-web)
// THIS is the Configuration screen. The campaign header (name / system /
// language) is wired to the live GetActiveCampaign RPC in the SPA. The provider
// keys, voice dropdown, and health/connection badge render as styled DISABLED
// "coming soon" placeholders until their RPCs land (ADR-0039 product decision).

import { AppShell } from "./shell.jsx";
import { mockCampaign, mockProviderKeys, mockVoices, mockConnection } from "./data.jsx";

export function SettingsScreen() {
  const campaign = mockCampaign;

  return (
    <AppShell activeId="configuration">
      <header className="page-header">
        <div>
          <h1 className="page-title">Configuration</h1>
          <p className="page-subtitle">
            Provider keys, voices, and the active campaign for this self-host.
          </p>
        </div>
        <span className="tag" data-tone={mockConnection.connected ? "success" : undefined}>
          <span className="live-dot" data-state={mockConnection.connected ? "live" : "idle"} />
          {mockConnection.connected ? mockConnection.bot : "Bot offline"}
        </span>
      </header>

      {/* Campaign header — LIVE in the SPA */}
      <section className="card">
        <div className="card-header">
          <div>
            <h2 className="card-title">Active campaign</h2>
            <p className="card-desc">Resolved from the seeded tenant.</p>
          </div>
        </div>
        <div className="campaign-header">
          <span className="avatar">{campaign.name.charAt(0)}</span>
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
      </section>

      {/* Provider keys — coming soon */}
      <section className="card" data-disabled="true">
        <div className="card-header">
          <div>
            <h2 className="card-title">Provider keys</h2>
            <p className="card-desc">Bring-your-own keys for LLM, STT, and TTS.</p>
          </div>
          <span className="coming-soon">coming soon</span>
        </div>
        {mockProviderKeys.map((k) => (
          <div className="field" key={k.component}>
            <label className="field-label">
              {k.component.toUpperCase()} — {k.provider}
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

      {/* Voice dropdown — coming soon */}
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
            {mockVoices[0].name}
            <span aria-hidden="true">▾</span>
          </button>
        </div>
      </section>
    </AppShell>
  );
}
