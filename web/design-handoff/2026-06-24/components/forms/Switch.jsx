import React from 'react';

function useGxStyle(id, css) {
  React.useEffect(() => {
    if (document.getElementById(id)) return;
    const el = document.createElement('style');
    el.id = id; el.textContent = css; document.head.appendChild(el);
  }, [id, css]);
}

const CSS = `
.gx-switch{display:inline-flex;align-items:center;gap:10px;cursor:pointer;user-select:none;}
.gx-switch input{position:absolute;opacity:0;width:0;height:0;}
.gx-switch__track{
  position:relative;width:38px;height:22px;border-radius:var(--radius-pill);
  background:var(--surface-inset);border:1px solid var(--border-default);
  transition:background var(--duration-base) var(--ease-standard),border-color var(--duration-base) var(--ease-standard),box-shadow var(--duration-base) var(--ease-standard);
  flex:0 0 auto;
}
.gx-switch__thumb{
  position:absolute;top:2px;left:2px;width:16px;height:16px;border-radius:999px;
  background:var(--text-muted);
  transition:transform var(--duration-base) var(--ease-out),background var(--duration-base) var(--ease-standard);
}
.gx-switch input:checked + .gx-switch__track{background:var(--arcane);border-color:var(--arcane);box-shadow:var(--glow-arcane-soft);}
.gx-switch input:checked + .gx-switch__track .gx-switch__thumb{transform:translateX(16px);background:#fff;}
.gx-switch input:focus-visible + .gx-switch__track{box-shadow:var(--focus-ring);}
.gx-switch input:disabled + .gx-switch__track{opacity:.5;}
.gx-switch__label{font-size:var(--body-sm);color:var(--text-default);}
.gx-switch--disabled{cursor:not-allowed;}
`;

/**
 * Glyphoxa Switch — toggle for NPC flags ("Address only", "Knowledge graph
 * enabled"). Lights arcane violet when on.
 */
export function Switch({
  checked,
  defaultChecked,
  onChange,
  label = null,
  disabled = false,
  id,
  className = '',
  ...props
}) {
  useGxStyle('gx-switch-styles', CSS);
  const fid = id || React.useId();
  return (
    <label className={'gx-switch ' + (disabled ? 'gx-switch--disabled ' : '') + className} htmlFor={fid}>
      <input
        id={fid}
        type="checkbox"
        checked={checked}
        defaultChecked={defaultChecked}
        onChange={onChange}
        disabled={disabled}
        {...props}
      />
      <span className="gx-switch__track"><span className="gx-switch__thumb" /></span>
      {label && <span className="gx-switch__label">{label}</span>}
    </label>
  );
}
