import React from 'react';

function useGxStyle(id, css) {
  React.useEffect(() => {
    if (document.getElementById(id)) return;
    const el = document.createElement('style');
    el.id = id; el.textContent = css; document.head.appendChild(el);
  }, [id, css]);
}

const CSS = `
.gx-field{display:flex;flex-direction:column;gap:6px;}
.gx-field__label{font-size:var(--body-xs);font-weight:600;color:var(--text-default);}
.gx-field__hint{font-size:var(--body-xs);color:var(--text-muted);}
.gx-field__hint--error{color:var(--status-danger);}
.gx-field__wrap{position:relative;display:flex;align-items:center;}
.gx-field__icon{position:absolute;left:11px;display:inline-flex;width:16px;height:16px;color:var(--text-muted);pointer-events:none;}
.gx-field__icon svg{width:100%;height:100%;}

.gx-input{
  width:100%;box-sizing:border-box;font-family:var(--font-base);font-size:var(--body-sm);
  color:var(--text-strong);background:var(--surface-inset);
  border:1px solid var(--border-default);border-radius:var(--radius-sm);
  padding:9px 12px;line-height:1.4;
  transition:border-color var(--duration-fast) var(--ease-standard),box-shadow var(--duration-base) var(--ease-standard);
}
.gx-input::placeholder{color:var(--text-subtle);}
.gx-input:hover{border-color:var(--border-strong);}
.gx-input:focus{outline:none;border-color:var(--arcane);box-shadow:0 0 0 3px rgba(144,89,255,.22);}
.gx-input--has-icon{padding-left:34px;}
.gx-input--invalid{border-color:var(--status-danger);}
.gx-input--invalid:focus{box-shadow:0 0 0 3px rgba(255,79,94,.22);}
.gx-input:disabled{opacity:.5;cursor:not-allowed;}
textarea.gx-input{resize:vertical;min-height:84px;line-height:1.5;}
`;

/**
 * Glyphoxa text Input — inked well on the inset surface with an arcane focus
 * ring. Optional `label`, `hint`/`error`, leading `icon`, and `multiline`.
 */
export function Input({
  label = null,
  hint = null,
  error = null,
  icon = null,
  multiline = false,
  id,
  className = '',
  ...props
}) {
  useGxStyle('gx-input-styles', CSS);
  const fid = id || React.useId();
  const Tag = multiline ? 'textarea' : 'input';
  const invalid = Boolean(error);
  const inputCls = [
    'gx-input',
    icon && !multiline ? 'gx-input--has-icon' : '',
    invalid ? 'gx-input--invalid' : '',
    className,
  ].filter(Boolean).join(' ');

  return (
    <div className="gx-field">
      {label && <label className="gx-field__label" htmlFor={fid}>{label}</label>}
      <div className="gx-field__wrap">
        {icon && !multiline && <span className="gx-field__icon">{icon}</span>}
        <Tag id={fid} className={inputCls} aria-invalid={invalid || undefined} {...props} />
      </div>
      {(error || hint) && (
        <span className={'gx-field__hint' + (error ? ' gx-field__hint--error' : '')}>
          {error || hint}
        </span>
      )}
    </div>
  );
}
