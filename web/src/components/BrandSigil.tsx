// Brand sigil — ported by hand from the handoff icons.jsx (ADR-0017: the bespoke
// brand/NPC sigil components are rolled by hand; generic icons use lucide-react).
export function BrandSigil({ size = 18 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <path
        d="M12 2 3 7v10l9 5 9-5V7l-9-5Z"
        stroke="currentColor"
        strokeWidth="1.6"
        strokeLinejoin="round"
      />
      <path
        d="M12 7v10M7.5 9.5 16.5 14.5M16.5 9.5 7.5 14.5"
        stroke="currentColor"
        strokeWidth="1.2"
      />
    </svg>
  );
}
