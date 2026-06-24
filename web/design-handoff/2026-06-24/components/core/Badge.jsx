import React from 'react';

function useGxStyle(id, css) {
  React.useEffect(() => {
    if (document.getElementById(id)) return;
    const el = document.createElement('style');
    el.id = id; el.textContent = css; document.head.appendChild(el);
  }, [id, css]);
}

const CSS = `
.gx-badge{
  display:inline-flex;align-items:center;gap:6px;
  font-family:var(--font-base);font-size:var(--body-2xs);font-weight:600;
  line-height:1;padding:4px 9px;border-radius:var(--radius-pill);
  border:1px solid transparent;white-space:nowrap;letter-spacing:.01em;
}
.gx-badge--sm{font-size:10px;padding:3px 7px;}
.gx-badge__dot{width:6px;height:6px;border-radius:999px;background:currentColor;flex:0 0 auto;}
.gx-badge__dot--pulse{position:relative;}
.gx-badge__dot--pulse::after{
  content:"";position:absolute;inset:0;border-radius:999px;background:currentColor;
  animation:gx-badge-ping 1.4s var(--ease-out) infinite;
}
@keyframes gx-badge-ping{0%{transform:scale(1);opacity:.7}80%,100%{transform:scale(2.6);opacity:0}}

.gx-badge--arcane{background:rgba(144,89,255,.16);color:var(--color-violet-20);border-color:rgba(144,89,255,.3);}
.gx-badge--live{background:var(--status-live-bg);color:var(--status-live);border-color:rgba(63,225,176,.32);}
.gx-badge--success{background:var(--status-live-bg);color:var(--status-success);border-color:rgba(63,225,176,.32);}
.gx-badge--warning{background:var(--status-warning-bg);color:var(--status-warning);border-color:rgba(255,189,79,.32);}
.gx-badge--danger{background:var(--status-danger-bg);color:var(--status-danger);border-color:rgba(255,79,94,.32);}
.gx-badge--info{background:var(--status-info-bg);color:var(--color-blue-20);border-color:rgba(0,144,237,.32);}
.gx-badge--gold{background:rgba(255,189,79,.14);color:var(--gold);border-color:rgba(255,189,79,.34);}
.gx-badge--neutral{background:rgba(255,255,255,.06);color:var(--text-muted);border-color:var(--border-default);}
.gx-badge--solid{background:var(--arcane);color:#fff;border-color:var(--arcane);}
`;

/**
 * Glyphoxa Badge — pill status/label. Use `live` (with dot pulse) for active
 * sessions, semantic variants for state, `neutral` for metadata tags.
 */
export function Badge({
  variant = 'neutral',
  size = 'md',
  dot = false,
  pulse = false,
  className = '',
  children,
  ...props
}) {
  useGxStyle('gx-badge-styles', CSS);
  const cls = ['gx-badge', `gx-badge--${variant}`, size === 'sm' ? 'gx-badge--sm' : '', className]
    .filter(Boolean).join(' ');
  return (
    <span className={cls} {...props}>
      {dot && <span className={'gx-badge__dot' + (pulse ? ' gx-badge__dot--pulse' : '')} />}
      {children}
    </span>
  );
}
