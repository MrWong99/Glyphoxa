import React from 'react';

function useGxStyle(id, css) {
  React.useEffect(() => {
    if (document.getElementById(id)) return;
    const el = document.createElement('style');
    el.id = id; el.textContent = css; document.head.appendChild(el);
  }, [id, css]);
}

const CSS = `
.gx-select-field{display:flex;flex-direction:column;gap:6px;}
.gx-select-field__label{font-size:var(--body-xs);font-weight:600;color:var(--text-default);}
.gx-select-wrap{position:relative;display:flex;align-items:center;}
.gx-select{
  width:100%;box-sizing:border-box;-webkit-appearance:none;appearance:none;
  font-family:var(--font-base);font-size:var(--body-sm);color:var(--text-strong);
  background:var(--surface-inset);border:1px solid var(--border-default);
  border-radius:var(--radius-sm);padding:9px 34px 9px 12px;line-height:1.4;cursor:pointer;
  transition:border-color var(--duration-fast) var(--ease-standard),box-shadow var(--duration-base) var(--ease-standard);
}
.gx-select:hover{border-color:var(--border-strong);}
.gx-select:focus{outline:none;border-color:var(--arcane);box-shadow:0 0 0 3px rgba(144,89,255,.22);}
.gx-select:disabled{opacity:.5;cursor:not-allowed;}
.gx-select option{background:var(--surface-overlay);color:var(--text-strong);}
.gx-select-chevron{position:absolute;right:12px;width:14px;height:14px;pointer-events:none;color:var(--text-muted);}
`;

/**
 * Glyphoxa Select — native select styled to match Input (engine, budget tier,
 * voice provider pickers). Pass `options` as strings or {value,label}, or
 * supply <option> children directly.
 */
export function Select({
  label = null,
  options = null,
  id,
  className = '',
  children,
  ...props
}) {
  useGxStyle('gx-select-styles', CSS);
  const fid = id || React.useId();
  return (
    <div className="gx-select-field">
      {label && <label className="gx-select-field__label" htmlFor={fid}>{label}</label>}
      <div className="gx-select-wrap">
        <select id={fid} className={'gx-select ' + className} {...props}>
          {options
            ? options.map((o) => {
                const v = typeof o === 'string' ? o : o.value;
                const l = typeof o === 'string' ? o : o.label;
                return <option key={v} value={v}>{l}</option>;
              })
            : children}
        </select>
        <svg className="gx-select-chevron" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round"><polyline points="6 9 12 15 18 9" /></svg>
      </div>
    </div>
  );
}
