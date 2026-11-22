import { useEffect, useRef, type ReactNode } from "react";
import { RotateCw, TriangleAlert } from "lucide-react";

import { useI18n } from "@/i18n";
import { Button } from "./Button";

// ErrorState — what a panel shows when its data could not be LOADED: a plain
// sentence, a way to try again, and the raw error folded away for whoever is
// asked "what did it say?". It takes the place the content would have had, the
// same way EmptyState does, and shares its layout.
//
// The feedback rule (ADR-0017): a failed load is an ErrorState with a retry; a
// failed mutation is a toast (or an Alert inside an editor that stays open).
//
//   <ErrorState message={errorMessage(error)} onRetry={() => query.refetch()} retrying={query.isFetching} />
//
// Usually reached through QueryBoundary, which wires all three from a query.
// The raw message is the server's own (English) sentence: it is shown, but
// folded, so the screen reads in the GM's language first.
//
// Focus through a retry: the busy button is disabled, and a browser takes
// focus off a control that becomes disabled (the focus fixup, one frame
// later), so a keyboard user who pressed "Try again" would be on <body> by the
// time the retry failed again. When the retry was started from the focused
// button, focus is put back on it once the retry settles, unless it has gone
// somewhere else on purpose in the meantime.

export function ErrorState({
  title,
  description,
  message,
  onRetry,
  retrying = false,
  size = "panel",
  className = "",
}: {
  /** Defaults to the shared "Couldn't load this". */
  title?: ReactNode;
  /** Defaults to the shared one-line explanation. Pass null to omit it. */
  description?: ReactNode;
  /** The raw error text, shown folded under "Technical details". */
  message?: string;
  /** When set, a "Try again" button is shown and calls this. */
  onRetry?: () => void;
  /** The retry is in flight: the button shows its loading state. */
  retrying?: boolean;
  size?: "panel" | "inline";
  className?: string;
}) {
  const { t } = useI18n();

  const retryButton = useRef<HTMLButtonElement>(null);
  // The retry in flight was started from the button while it held focus.
  const pressedWithFocus = useRef(false);
  useEffect(() => {
    if (retrying || !pressedWithFocus.current) return;
    pressedWithFocus.current = false;
    const active = document.activeElement;
    if (active === null || active === document.body) retryButton.current?.focus();
  }, [retrying]);

  const cls =["gx-empty", "gx-empty--danger", `gx-empty--${size}`, className].filter(Boolean).join(" ");
  const desc = description === undefined ? t("common.loadFailedDesc") : description;
  return (
    <div className={cls} role="alert">
      <span className="gx-empty__icon" aria-hidden="true">
        <TriangleAlert size={size === "panel" ? 22 : 16} />
      </span>
      <div className="gx-empty__text">
        <p className="gx-empty__title">{title ?? t("common.loadFailedTitle")}</p>
        {desc && <p className="gx-empty__desc">{desc}</p>}
        {message && (
          <details className="gx-error__details">
            <summary>{t("common.errorDetails")}</summary>
            <pre className="gx-error__message">{message}</pre>
          </details>
        )}
      </div>
      {onRetry && (
        <div className="gx-empty__action">
          <Button
            ref={retryButton}
            variant="secondary"
            size="sm"
            iconStart={<RotateCw />}
            loading={retrying}
            onClick={() => {
              pressedWithFocus.current = document.activeElement === retryButton.current;
              onRetry();
            }}
          >
            {t("common.retry")}
          </Button>
        </div>
      )}
    </div>
  );
}
