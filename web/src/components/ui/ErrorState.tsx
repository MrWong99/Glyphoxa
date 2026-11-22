import type { ReactNode } from "react";
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
  const cls = ["gx-empty", "gx-empty--danger", `gx-empty--${size}`, className].filter(Boolean).join(" ");
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
            variant="secondary"
            size="sm"
            iconStart={<RotateCw />}
            loading={retrying}
            onClick={onRetry}
          >
            {t("common.retry")}
          </Button>
        </div>
      )}
    </div>
  );
}
