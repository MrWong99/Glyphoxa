import React from 'react';

function useGxStyle(id, css) {
  React.useEffect(() => {
    if (document.getElementById(id)) return;
    const el = document.createElement('style');
    el.id = id; el.textContent = css; document.head.appendChild(el);
  }, [id, css]);
}

const CSS = `
.gx-card{
  position:relative;display:flex;flex-direction:column;
  background:var(--surface-card);
  border:1px solid var(--border-subtle);
  border-radius:var(--radius-md);
  box-shadow:var(--shadow-sm), var(--shadow-inset);
  transition:border-color var(--duration-base) var(--ease-standard),
             box-shadow var(--duration-base) var(--ease-standard),
             transform var(--duration-base) var(--ease-standard);
}
.gx-card--interactive{cursor:pointer;}
.gx-card--interactive:hover{
  border-color:var(--border-strong);
  box-shadow:var(--shadow-md), var(--glow-arcane-soft);
  transform:translateY(-2px);
}
.gx-card--live{border-color:rgba(63,225,176,.34);}
.gx-card--live:hover{box-shadow:var(--shadow-md), var(--glow-live);}
.gx-card--flat{box-shadow:none;background:var(--surface-raised);}
/* top accent hairline lit in arcane gradient */
.gx-card--accent::before{
  content:"";position:absolute;inset:0 0 auto 0;height:2px;
  background:var(--gradient-arcane);
  border-radius:var(--radius-md) var(--radius-md) 0 0;
}
.gx-card__header{padding:var(--spacing-md) var(--spacing-lg);border-bottom:1px solid var(--border-subtle);}
.gx-card__title{font-family:var(--font-display);font-size:var(--display-xs);font-weight:600;color:var(--text-strong);margin:0;}
.gx-card__body{padding:var(--spacing-lg);}
`;

/**
 * Glyphoxa Card — the arcane panel. Wraps content on the ink-card surface with
 * Protocol's hairline border + ink-tinted shadow. `interactive` adds a lift +
 * violet glow on hover; `live` tints the border green; `accent` lights a top
 * gradient rule.
 */
export function Card({
  interactive = false,
  live = false,
  flat = false,
  accent = false,
  className = '',
  children,
  ...props
}) {
  useGxStyle('gx-card-styles', CSS);
  const cls = [
    'gx-card',
    interactive ? 'gx-card--interactive' : '',
    live ? 'gx-card--live' : '',
    flat ? 'gx-card--flat' : '',
    accent ? 'gx-card--accent' : '',
    className,
  ].filter(Boolean).join(' ');
  return <div className={cls} {...props}>{children}</div>;
}

export function CardHeader({ className = '', children, ...props }) {
  return <div className={'gx-card__header ' + className} {...props}>{children}</div>;
}

export function CardTitle({ className = '', children, ...props }) {
  return <h3 className={'gx-card__title ' + className} {...props}>{children}</h3>;
}

export function CardBody({ className = '', children, ...props }) {
  return <div className={'gx-card__body ' + className} {...props}>{children}</div>;
}
