/* Glyphoxa web UI kit — Settings (providers) + Users. */

function SettingsScreen({ page }) {
  const Icon = window.GXIcon;
  const { Card, Badge, Button, Avatar, Select, Switch, Input } = window.GlyphoxaDesignSystem_55f528;

  if (page === 'users') {
    const users = [
      { name: 'Sora Vance', email: 'sora@flagon.gg', role: 'Dungeon Master', rv: 'arcane', status: 'live' },
      { name: 'Petra Quill', email: 'petra@flagon.gg', role: 'Tenant Admin', rv: 'gold', status: 'idle' },
      { name: 'Bram Holt', email: 'bram@flagon.gg', role: 'Player', rv: 'neutral', status: 'offline' },
      { name: 'Ix the Scribe', email: 'ix@flagon.gg', role: 'Player', rv: 'neutral', status: 'offline' },
    ];
    return (
      <div style={{ padding: 28, maxWidth: 920, margin: '0 auto' }}>
        <div style={{ display: 'flex', alignItems: 'center', marginBottom: 22 }}>
          <div style={{ flex: 1 }}>
            <h1 style={{ fontFamily: 'var(--font-display)', fontSize: 28, fontWeight: 600, color: 'var(--text-strong)', margin: 0 }}>Users</h1>
            <p style={{ color: 'var(--text-muted)', margin: '4px 0 0' }}>Everyone with a seat at your table.</p>
          </div>
          <Button variant="primary" iconStart={<Icon name="UserPlus" size={16} />}>Invite</Button>
        </div>
        <Card flat>
          {users.map((u, i) => (
            <div key={u.email} style={{ display: 'flex', alignItems: 'center', gap: 14, padding: '13px 18px', borderTop: i ? '1px solid var(--border-subtle)' : 'none' }}>
              <Avatar name={u.name} size="md" status={u.status} />
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ fontSize: 14, fontWeight: 600, color: 'var(--text-strong)' }}>{u.name}</div>
                <div style={{ fontSize: 12, color: 'var(--text-subtle)' }}>{u.email}</div>
              </div>
              <Badge variant={u.rv} size="sm">{u.role}</Badge>
              <Icon name="EllipsisVertical" size={16} style={{ color: 'var(--text-subtle)' }} />
            </div>
          ))}
        </Card>
      </div>
    );
  }

  const providers = [
    { kind: 'STT', name: 'Deepgram Nova-3', icon: 'Ear', status: 'ok', opts: ['Deepgram Nova-3', 'ElevenLabs', 'whisper.cpp (local)'] },
    { kind: 'LLM', name: 'Anthropic Claude', icon: 'BrainCircuit', status: 'ok', opts: ['Anthropic Claude', 'OpenAI', 'Google Gemini', 'Ollama (local)'] },
    { kind: 'TTS', name: 'ElevenLabs', icon: 'AudioLines', status: 'ok', opts: ['ElevenLabs', 'Coqui XTTS (local)'] },
    { kind: 'Embeddings', name: 'OpenAI', icon: 'Network', status: 'degraded', opts: ['OpenAI', 'Ollama (local)'] },
  ];

  return (
    <div style={{ padding: 28, maxWidth: 820, margin: '0 auto' }}>
      <h1 style={{ fontFamily: 'var(--font-display)', fontSize: 28, fontWeight: 600, color: 'var(--text-strong)', margin: 0 }}>Providers</h1>
      <p style={{ color: 'var(--text-muted)', margin: '4px 0 22px' }}>Swap any engine with a config change — not a rewrite.</p>

      <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
        {providers.map((p) => (
          <Card key={p.kind}>
            <div style={{ padding: 16, display: 'flex', alignItems: 'center', gap: 14 }}>
              <span style={{ width: 40, height: 40, flex: '0 0 40px', borderRadius: 10, display: 'inline-flex', alignItems: 'center', justifyContent: 'center', background: 'var(--surface-inset)', color: 'var(--arcane)' }}><Icon name={p.icon} size={19} /></span>
              <div style={{ width: 96 }}>
                <div className="gx-overline">{p.kind}</div>
                <div style={{ fontSize: 14, fontWeight: 600, color: 'var(--text-strong)', marginTop: 2 }}>{p.name}</div>
              </div>
              <div style={{ flex: 1, maxWidth: 240 }}>
                <Select defaultValue={p.name} options={p.opts} aria-label={p.kind + ' provider'} />
              </div>
              {p.status === 'ok'
                ? <Badge variant="success" dot size="sm">Healthy</Badge>
                : <Badge variant="warning" dot size="sm">Degraded</Badge>}
            </div>
          </Card>
        ))}
      </div>

      <h2 style={{ fontFamily: 'var(--font-display)', fontSize: 18, fontWeight: 600, color: 'var(--text-strong)', margin: '28px 0 12px' }}>Session defaults</h2>
      <Card>
        <div style={{ padding: 18, display: 'flex', flexDirection: 'column', gap: 14 }}>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14 }}>
            <Input label="Latency budget (ms)" defaultValue="1200" />
            <Select label="Default engine" options={['Cascaded', 'S2S — Gemini', 'S2S — OpenAI']} />
          </div>
          <Switch label="Continuous live transcription" defaultChecked />
          <Switch label="Speculative sentence cascade (experimental)" />
        </div>
      </Card>
    </div>
  );
}

window.GXSettings = SettingsScreen;
