import React from 'react';

/* Injects a component's CSS once, keyed by id. Keeps primitives self-contained
 * (no external class dependencies) while still supporting :hover/:focus/:active. */
function useGxStyle(id, css) {
  React.useEffect(() => {
    if (document.getElementById(id)) return;
    const el = document.createElement('style');
    el.id = id;
    el.textContent = css;
    document.head.appendChild(el);
  }, [id, css]);
}

const CSS = `
.gx-btn{
  -webkit-appearance:none;appearance:none;box-sizing:border-box;
  display:inline-flex;align-items:center;justify-content:center;gap:0.5ch;
  font-family:var(--font-base);font-weight:700;line-height:1.25;
  border:2px solid transparent;border-radius:var(--radius-sm);
  cursor:pointer;text-decoration:none;white-space:nowrap;
  transition:background-color var(--duration-fast) var(--ease-standard),
             border-color var(--duration-fast) var(--ease-standard),
             color var(--duration-fast) var(--ease-standard),
             box-shadow var(--duration-base) var(--ease-standard),
             transform var(--duration-fast) var(--ease-standard);
}
.gx-btn:focus-visible{outline:none;box-shadow:var(--focus-ring);}
.gx-btn:disabled{opacity:.5;pointer-events:none;}
.gx-btn:active{transform:translateY(1px);}
.gx-btn--sm{font-size:var(--body-xs);padding:5px 12px;}
.gx-btn--md{font-size:var(--body-sm);padding:8px 16px;}
.gx-btn--lg{font-size:var(--body-md);padding:11px 22px;}
.gx-btn--block{display:flex;width:100%;}

/* primary — cast the spell */
.gx-btn--primary{background:var(--arcane);border-color:var(--arcane);color:var(--text-on-arcane);}
.gx-btn--primary:hover{background:var(--arcane-hover);border-color:var(--arcane-hover);box-shadow:var(--glow-arcane-soft);}
.gx-btn--primary:active{background:var(--arcane-active);}

/* secondary — Protocol ghost-border that fills on hover */
.gx-btn--secondary{background:transparent;border-color:var(--border-strong);color:var(--text-default);}
.gx-btn--secondary:hover{border-color:var(--arcane);color:var(--text-strong);background:rgba(144,89,255,.1);}

/* ghost — chrome / toolbar */
.gx-btn--ghost{background:transparent;border-color:transparent;color:var(--text-muted);}
.gx-btn--ghost:hover{background:rgba(144,89,255,.12);color:var(--text-strong);}

/* gold — rare, legendary CTA */
.gx-btn--gold{background:var(--gold);border-color:var(--gold);color:#241541;}
.gx-btn--gold:hover{box-shadow:var(--glow-gold);}

/* danger — stop session / destructive */
.gx-btn--danger{background:transparent;border-color:var(--status-danger);color:var(--status-danger);}
.gx-btn--danger:hover{background:var(--status-danger);color:#2a0509;}

.gx-btn__icon{display:inline-flex;width:1.05em;height:1.05em;flex:0 0 auto;}
.gx-btn__icon svg{width:100%;height:100%;}
`;

/**
 * Glyphoxa Button — Protocol button mechanics (2px border, bold, invert/glow
 * on hover) in arcane dress.
 */
export function Button({
  variant = 'primary',
  size = 'md',
  block = false,
  iconStart = null,
  iconEnd = null,
  as = 'button',
  className = '',
  children,
  ...props
}) {
  useGxStyle('gx-button-styles', CSS);
  const Tag = as;
  const cls = [
    'gx-btn',
    `gx-btn--${variant}`,
    `gx-btn--${size}`,
    block ? 'gx-btn--block' : '',
    className,
  ].filter(Boolean).join(' ');

  return (
    <Tag className={cls} {...props}>
      {iconStart && <span className="gx-btn__icon">{iconStart}</span>}
      {children}
      {iconEnd && <span className="gx-btn__icon">{iconEnd}</span>}
    </Tag>
  );
}
