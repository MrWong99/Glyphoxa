import React from 'react';

function useGxStyle(id, css) {
  React.useEffect(() => {
    if (document.getElementById(id)) return;
    const el = document.createElement('style');
    el.id = id; el.textContent = css; document.head.appendChild(el);
  }, [id, css]);
}

const CSS = `
.gx-avatar{
  position:relative;display:inline-flex;align-items:center;justify-content:center;
  flex:0 0 auto;border-radius:var(--radius-pill);overflow:hidden;
  font-family:var(--font-display);font-weight:600;color:#fff;
  background:var(--gradient-arcane-diag);
  border:1px solid rgba(255,255,255,.14);user-select:none;
}
.gx-avatar--rounded{border-radius:var(--radius-md);}
.gx-avatar img{width:100%;height:100%;object-fit:cover;display:block;}
.gx-avatar--xs{width:24px;height:24px;font-size:10px;}
.gx-avatar--sm{width:32px;height:32px;font-size:12px;}
.gx-avatar--md{width:40px;height:40px;font-size:15px;}
.gx-avatar--lg{width:56px;height:56px;font-size:20px;}
.gx-avatar--xl{width:80px;height:80px;font-size:28px;}

/* speaking — arcane ring pulse for the NPC currently voicing */
.gx-avatar--speaking{box-shadow:var(--glow-rune);animation:gx-avatar-pulse 1.5s var(--ease-out) infinite;}
@keyframes gx-avatar-pulse{
  0%,100%{box-shadow:0 0 0 1px rgba(0,221,255,.5),0 0 12px rgba(0,221,255,.35);}
  50%{box-shadow:0 0 0 2px rgba(0,221,255,.7),0 0 22px rgba(0,221,255,.6);}
}
.gx-avatar__status{
  position:absolute;right:-1px;bottom:-1px;width:30%;height:30%;min-width:8px;min-height:8px;
  border-radius:999px;border:2px solid var(--surface-card);
}
`;

const HUES = [
  'linear-gradient(135deg,#3a8ee6,#9059ff)',
  'linear-gradient(135deg,#9059ff,#c139e6)',
  'linear-gradient(135deg,#00ddff,#0090ed)',
  'linear-gradient(135deg,#3fe1b0,#0090ed)',
  'linear-gradient(135deg,#ff7139,#e31587)',
  'linear-gradient(135deg,#ffbd4f,#ff7139)',
];

function hueFor(name = '') {
  let h = 0;
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) >>> 0;
  return HUES[h % HUES.length];
}

function initials(name = '') {
  return name.trim().split(/\s+/).slice(0, 2).map((w) => w[0] || '').join('').toUpperCase();
}

/**
 * Glyphoxa Avatar — NPC / player portrait. Image when `src` is set, otherwise
 * deterministic arcane-gradient initials. `speaking` lights a pulsing rune ring
 * for the NPC currently voicing; `status` shows a presence dot.
 */
export function Avatar({
  name = '',
  src = null,
  size = 'md',
  shape = 'circle',
  speaking = false,
  status = null,      // 'live' | 'idle' | 'offline' | null
  className = '',
  ...props
}) {
  useGxStyle('gx-avatar-styles', CSS);
  const cls = [
    'gx-avatar',
    `gx-avatar--${size}`,
    shape === 'rounded' ? 'gx-avatar--rounded' : '',
    speaking ? 'gx-avatar--speaking' : '',
    className,
  ].filter(Boolean).join(' ');

  const statusColor = status === 'live' ? 'var(--status-live)'
    : status === 'idle' ? 'var(--status-warning)'
    : status === 'offline' ? 'var(--text-subtle)' : null;

  return (
    <span className={cls} style={src ? undefined : { background: hueFor(name) }} {...props}>
      {src ? <img src={src} alt={name} /> : initials(name)}
      {statusColor && <span className="gx-avatar__status" style={{ background: statusColor }} />}
    </span>
  );
}
