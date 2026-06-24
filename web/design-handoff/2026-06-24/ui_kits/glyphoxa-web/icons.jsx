/* Glyphoxa web UI kit — Lucide icon helper.
 * Glyphoxa's web app uses lucide-react; we render the same icon set from the
 * official Lucide UMD global (loaded via CDN in index.html). This reads the raw
 * Lucide IconNode data so icons are real React SVGs, not a font scan. */

const _ATTR_MAP = {
  'stroke-width': 'strokeWidth', 'stroke-linecap': 'strokeLinecap',
  'stroke-linejoin': 'strokeLinejoin', 'fill-rule': 'fillRule',
  'clip-rule': 'clipRule', 'stroke-dasharray': 'strokeDasharray',
};
function _camel(attrs) {
  const out = {};
  for (const k in attrs) out[_ATTR_MAP[k] || k] = attrs[k];
  return out;
}

function Icon({ name, size = 18, stroke = 2, className = '', style = {} }) {
  const lib = (window.lucide && (window.lucide.icons || window.lucide)) || {};
  const node = lib[name];
  const base = { display: 'inline-flex', flex: '0 0 auto', verticalAlign: 'middle', ...style };
  // Lucide IconNode shape is ["svg", svgAttrs, childrenArray]; older shapes are
  // a flat array of [tag, attrs] children. Support both.
  let children = null;
  if (Array.isArray(node) && Array.isArray(node[2])) children = node[2];
  else if (Array.isArray(node) && Array.isArray(node[0])) children = node;
  if (!children) {
    return <span className={className} style={{ ...base, width: size, height: size }} />;
  }
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg" width={size} height={size} viewBox="0 0 24 24"
      fill="none" stroke="currentColor" strokeWidth={stroke}
      strokeLinecap="round" strokeLinejoin="round"
      className={className} style={base} aria-hidden="true"
    >
      {children.map(([tag, attrs], i) => React.createElement(tag, { ..._camel(attrs || {}), key: i }))}
    </svg>
  );
}

window.GXIcon = Icon;
