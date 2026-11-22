import { Link, useBlocker, useRouter } from "@tanstack/react-router";
import type { ErrorComponentProps } from "@tanstack/react-router";

import { Button } from "@/components/ui/Button";
import { Card, CardBody } from "@/components/ui/Card";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import { useI18n } from "@/i18n";
import { errorMessage } from "@/lib/connectError";
import { guardedSearchChanged, hasUnsavedEdits } from "@/lib/navigationGuard";

// The router-level fallbacks (ADR-0018): what renders when a route throws,
// when no route matches, while a lazily loaded screen is on its way, and the
// question asked before a navigation discards unsaved edits. Without the first
// two TanStack Router falls back to its own unstyled English defaults — a bare
// "Something went wrong!" / "Not Found" on a white page.

// RoutePending covers the gap while a code-split screen's chunk loads. It is
// the same quiet boot placeholder the AuthGate shows, so a slow chunk reads as
// "still loading", not as a different state.
export function RoutePending() {
  const { t } = useI18n();
  return <div className="gx-auth-boot" aria-busy="true" aria-label={t("common.loading")} />;
}

// RouteError is the app's error boundary surface. "Try again" re-renders the
// failed route (and re-runs its loaders); "Reload page" is the way out of the
// one failure a retry cannot fix — a screen chunk that no longer exists
// because a deploy replaced it while this tab was open.
export function RouteError({ error, reset }: ErrorComponentProps) {
  const { t } = useI18n();
  const router = useRouter();
  return (
    <div className="gx-providers" role="alert">
      <h1>{t("auth.errorTitle")}</h1>
      <p className="gx-providers__lede">{t("auth.errorLede")}</p>
      <Card>
        <CardBody>
          <div className="gx-confirm__actions">
            <Button
              variant="primary"
              onClick={() => {
                reset();
                void router.invalidate();
              }}
            >
              {t("auth.errorRetry")}
            </Button>
            <Button variant="secondary" onClick={() => window.location.reload()}>
              {t("auth.errorReload")}
            </Button>
          </div>
          {/* The raw message stays verbatim (never translated) but folded away:
              it is for a bug report, not for the GM. */}
          <details>
            <summary>{t("auth.errorDetails")}</summary>
            <pre>{errorMessage(error)}</pre>
          </details>
        </CardBody>
      </Card>
    </div>
  );
}

// RouteNotFound renders for a URL no route matches.
export function RouteNotFound() {
  const { t } = useI18n();
  return (
    <div className="gx-providers">
      <h1>{t("auth.notFoundTitle")}</h1>
      <p className="gx-providers__lede">{t("auth.notFoundLede")}</p>
      <Link to="/" className="gx-btn gx-btn--secondary">
        {t("auth.notFoundHome")}
      </Link>
    </div>
  );
}

// NavigationGuard holds a route change (sidebar click, palette jump, browser
// back) and a tab close while any mounted editor reports unsaved edits
// (useDirtyGuard → lib/navigationGuard). Mounted once, inside the tenant
// shell. A change of PATH always counts. A change of QUERY on the same path
// counts only for the keys a mounted screen declared as its selection
// (useGuardedSearchKeys → lib/navigationGuard): the deep-link "consume then
// strip" rewrites unmount nothing and pass, but where the URL carries what the
// open editor is editing, Back to the previous selection would replace it.
export function NavigationGuard() {
  const { t } = useI18n();
  const blocker = useBlocker({
    shouldBlockFn: ({ current, next }) =>
      hasUnsavedEdits() &&
      (current.pathname !== next.pathname ||
        guardedSearchChanged(
          current.search as Record<string, unknown>,
          next.search as Record<string, unknown>,
        )),
    enableBeforeUnload: hasUnsavedEdits,
    withResolver: true,
  });
  if (blocker.status !== "blocked") return null;
  return (
    <ConfirmDialog
      open
      onOpenChange={(open) => {
        if (!open) blocker.reset();
      }}
      title={t("common.discardChangesTitle")}
      description={t("common.discardChangesDesc")}
      confirmLabel={t("common.discardChangesConfirm")}
      onConfirm={() => blocker.proceed()}
    />
  );
}
