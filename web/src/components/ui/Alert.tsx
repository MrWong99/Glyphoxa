import type { AriaRole, ReactNode } from "react";
import { CircleAlert, CircleCheck, Info, TriangleAlert, X } from "lucide-react";

import { useI18n } from "@/i18n";
import { IconButton } from "./IconButton";

// Alert — an inline message that stays on the page for as long as the state it
// describes: a save that failed inside an open editor, a setup step that is
// missing, a notice that something changed elsewhere. (A toast is for an event
// that has passed; a failed LOAD is an ErrorState, which also offers a retry.)
//
// `tone` picks the tint, the icon and the ARIA role, so the message is
// announced with the urgency it has:
//
//   danger, warning   role=alert   read out at once, interrupting
//   info, success     role=status  read out at the next pause
//
// Pass `role` to override: role="none" for a notice that is part of the page's
// static content and should simply be read in place.
//
//   <Alert tone="danger" title="Couldn't save" action={<Button size="sm" onClick={save}>Try again</Button>}>
//     {message}
//   </Alert>

export type AlertTone = "danger" | "warning" | "info" | "success";

const ICONS: Record<AlertTone, typeof Info> = {
  danger: CircleAlert,
  warning: TriangleAlert,
  info: Info,
  success: CircleCheck,
};

const ROLES: Record<AlertTone, AriaRole> = {
  danger: "alert",
  warning: "alert",
  info: "status",
  success: "status",
};

export function Alert({
  tone = "info",
  title,
  children,
  action,
  onDismiss,
  role,
  className = "",
}: {
  tone?: AlertTone;
  title?: ReactNode;
  children?: ReactNode;
  /** A Button (size="sm") or two, shown at the trailing edge. */
  action?: ReactNode;
  /** When set, a dismiss (X) button is shown and calls this. */
  onDismiss?: () => void;
  role?: AriaRole;
  className?: string;
}) {
  const { t } = useI18n();
  const Icon = ICONS[tone];
  const cls = ["gx-alert", `gx-alert--${tone}`, className].filter(Boolean).join(" ");
  return (
    <div className={cls} role={role ?? ROLES[tone]}>
      <span className="gx-alert__icon" aria-hidden="true">
        <Icon size={16} />
      </span>
      <div className="gx-alert__body">
        {title && <p className="gx-alert__title">{title}</p>}
        {children}
      </div>
      {(action || onDismiss) && (
        <div className="gx-alert__action">
          {action}
          {onDismiss && (
            <IconButton label={t("common.dismiss")} size="sm" onClick={onDismiss}>
              <X size={15} />
            </IconButton>
          )}
        </div>
      )}
    </div>
  );
}
