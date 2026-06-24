/* Glyphoxa web UI kit — App shell: sidebar + topbar. */
const { useState: _useStateShell } = React;

const NAV = [
  { id: 'dashboard', label: 'Dashboard', icon: 'LayoutDashboard' },
  { id: 'campaigns', label: 'Campaigns', icon: 'Swords' },
  { id: 'session', label: 'Sessions', icon: 'ScrollText' },
  { id: 'users', label: 'Users', icon: 'Users', role: true },
  { id: 'settings', label: 'Settings', icon: 'Settings' },
];

function AppShell({ page, setPage, title, breadcrumb, liveCount = 1, children }) {
  const Icon = window.GXIcon;
  const { Avatar, Badge } = window.GlyphoxaDesignSystem_55f528;

  return (
    <div style={{ display: 'flex', height: '100%', minHeight: 0, background: 'var(--surface-base)' }}>
      {/* Sidebar */}
      <aside style={{
        width: 248, flex: '0 0 248px', display: 'flex', flexDirection: 'column',
        background: 'var(--surface-raised)', borderRight: '1px solid var(--border-subtle)',
      }}>
        <div style={{ height: 60, display: 'flex', alignItems: 'center', gap: 10, padding: '0 18px', borderBottom: '1px solid var(--border-subtle)' }}>
          <span style={{
            width: 30, height: 30, borderRadius: 8, display: 'inline-flex', alignItems: 'center', justifyContent: 'center',
            background: 'var(--gradient-arcane-diag)', boxShadow: 'var(--glow-arcane-soft)', color: '#fff',
          }}><Icon name="Dices" size={17} /></span>
          <span style={{ fontFamily: 'var(--font-display)', fontWeight: 700, fontSize: 21, letterSpacing: '-0.01em' }}
            className="gx-gradient-text">Glyphoxa</span>
        </div>

        <nav style={{ flex: 1, padding: 12, display: 'flex', flexDirection: 'column', gap: 3 }}>
          {NAV.map((item) => {
            const active = page === item.id;
            return (
              <button key={item.id} onClick={() => setPage(item.id)}
                style={{
                  position: 'relative', display: 'flex', alignItems: 'center', gap: 12, width: '100%',
                  padding: '9px 12px', borderRadius: 'var(--radius-sm)', border: 'none', cursor: 'pointer',
                  fontFamily: 'var(--font-base)', fontSize: 14, fontWeight: 500, textAlign: 'left',
                  background: active ? 'rgba(144,89,255,.14)' : 'transparent',
                  color: active ? 'var(--color-violet-10)' : 'var(--text-muted)',
                  transition: 'background .15s, color .15s',
                }}
                onMouseEnter={(e) => { if (!active) { e.currentTarget.style.background = 'rgba(144,89,255,.07)'; e.currentTarget.style.color = 'var(--text-default)'; } }}
                onMouseLeave={(e) => { if (!active) { e.currentTarget.style.background = 'transparent'; e.currentTarget.style.color = 'var(--text-muted)'; } }}
              >
                {active && <span style={{ position: 'absolute', left: 0, top: '50%', transform: 'translateY(-50%)', height: 20, width: 3, borderRadius: 999, background: 'var(--gradient-arcane)' }} />}
                <Icon name={item.icon} size={18} />
                {item.label}
              </button>
            );
          })}
        </nav>

        <div style={{ padding: 14, borderTop: '1px solid var(--border-subtle)', display: 'flex', alignItems: 'center', gap: 10 }}>
          <Avatar name="Sora Vance" size="sm" status="live" />
          <div style={{ minWidth: 0, flex: 1 }}>
            <div style={{ fontSize: 13, fontWeight: 600, color: 'var(--text-default)', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>Sora Vance</div>
            <div style={{ fontSize: 11, color: 'var(--text-subtle)' }}>Dungeon Master</div>
          </div>
          <Icon name="ChevronsUpDown" size={15} style={{ color: 'var(--text-subtle)' }} />
        </div>
      </aside>

      {/* Main column */}
      <div style={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column' }}>
        <header style={{
          height: 60, flex: '0 0 60px', display: 'flex', alignItems: 'center', gap: 16,
          padding: '0 24px', borderBottom: '1px solid var(--border-subtle)',
          background: 'color-mix(in srgb, var(--surface-base) 86%, transparent)', backdropFilter: 'blur(8px)',
        }}>
          <div style={{ flex: 1, minWidth: 0 }}>
            {breadcrumb && <div style={{ fontSize: 11, color: 'var(--text-subtle)', marginBottom: 1 }}>{breadcrumb}</div>}
            <div style={{ fontFamily: 'var(--font-display)', fontSize: 19, fontWeight: 600, color: 'var(--text-strong)', lineHeight: 1.1 }}>{title}</div>
          </div>
          {liveCount > 0 && (
            <button onClick={() => setPage('session')} style={{ background: 'transparent', border: 'none', cursor: 'pointer', padding: 0 }}>
              <Badge variant="live" dot pulse>{liveCount} live</Badge>
            </button>
          )}
          <span style={{ width: 1, height: 24, background: 'var(--border-subtle)' }} />
          <Icon name="Search" size={18} style={{ color: 'var(--text-muted)' }} />
          <Icon name="Bell" size={18} style={{ color: 'var(--text-muted)' }} />
        </header>

        <main style={{ flex: 1, minHeight: 0, overflowY: 'auto' }}>
          {children}
        </main>
      </div>
    </div>
  );
}

window.GXShell = AppShell;
