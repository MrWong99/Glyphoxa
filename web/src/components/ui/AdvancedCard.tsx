import { useState, type ReactNode } from "react";
import { ChevronRight } from "lucide-react";

import { Card } from "./Card";
import { useI18n } from "@/i18n";

// AdvancedCard tucks power-user settings behind a closed-by-default disclosure
// so first-time GMs see only what they need. The whole header row is the
// toggle (a real <button>, aria-expanded) and the body simply isn't rendered
// while closed — collapsed settings are out of the accessibility tree too, not
// merely hidden. Callers pass their own title/hint when the section is more
// specific than the generic "Advanced settings".

export function AdvancedCard({
  title,
  hint,
  className,
  defaultOpen = false,
  children,
}: {
  title?: string;
  hint?: string;
  className?: string;
  defaultOpen?: boolean;
  children: ReactNode;
}) {
  const { t } = useI18n();
  const [open, setOpen] = useState(defaultOpen);

  return (
    <Card flat className={className ? `gx-advanced ${className}` : "gx-advanced"}>
      <button
        type="button"
        className="gx-advanced__toggle"
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
      >
        <ChevronRight size={15} className="gx-advanced__chevron" data-open={open || undefined} />
        <span className="gx-advanced__title">{title ?? t("common.advancedTitle")}</span>
        <span className="gx-advanced__hint">{hint ?? t("common.advancedHint")}</span>
      </button>
      {open && <div className="gx-advanced__body">{children}</div>}
    </Card>
  );
}
