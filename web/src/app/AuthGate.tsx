import { useEffect } from "react";
import type { ReactNode } from "react";
import { useQuery } from "@connectrpc/connect-query";
import { useNavigate } from "@tanstack/react-router";

import { AuthService } from "@gen/glyphoxa/management/v1/management_pb";
import { CurrentUserProvider, toCurrentUser, type CurrentUser } from "@/app/currentUser";
import { errorMessage, isUnauthenticated } from "@/lib/connectError";
import { rememberReturnTo } from "@/lib/returnTo";
import { useI18n } from "@/i18n";

// AuthGate is the SPA boot gate (ADR-0016 / ADR-0039): it probes
// AuthService.GetCurrentUser and, on CodeUnauthenticated, redirects to /login.
// On success it hands the resolved operator to its render-prop child (the app
// shell), so the sidebar shows the real identity instead of a hardcoded one.
// retry is off so a 401 redirects immediately rather than after react-query's
// default backoff.
//
// The user it hands on is a CurrentUser: the identity plus the access facts
// that ride on the same response (ADR-0065: Platform Admin, own Tenant, guest
// Workspaces). The same value is put in context, so a screen deep in the
// Outlet — the Admin route, the not-found page — can ask who is signed in
// without the shell threading it down.
//
// It is also where a session that lapses MID-USE ends up: the query client
// re-probes GetCurrentUser when any other call comes back Unauthenticated
// (lib/queryClient.ts), this gate sees the probe fail and redirects. The path
// being left is parked first so the sign-in lands back on it (lib/returnTo.ts).
export function AuthGate({ children }: { children: (user: CurrentUser) => ReactNode }) {
  const { t } = useI18n();
  const navigate = useNavigate();
  const { data, status, error } = useQuery(
    AuthService.method.getCurrentUser,
    {},
    { retry: false },
  );

  const unauthenticated = status === "error" && isUnauthenticated(error);

  useEffect(() => {
    if (unauthenticated) {
      rememberReturnTo(window.location.pathname + window.location.search);
      void navigate({ to: "/login" });
    }
  }, [unauthenticated, navigate]);

  // `data`, not `status === "success"`: a background re-probe that fails for a
  // NON-auth reason (a blip, a restart) flips the status to error while the
  // operator is still signed in — tearing the whole shell down for that would
  // discard every screen's state over nothing.
  const user = data ? toCurrentUser(data) : undefined;
  if (user && !unauthenticated) {
    return <CurrentUserProvider user={user}>{children(user)}</CurrentUserProvider>;
  }

  // A non-401 error is a real failure (server down, etc.) — surface it rather
  // than bouncing to /login, which would loop.
  if (status === "error" && !unauthenticated) {
    return (
      <div className="gx-providers">
        <p className="gx-campaign__error" role="alert">
          {/* The raw server message stays verbatim, interpolated into the
              localized template (spec rule: never translate the server reason). */}
          {t("auth.gateLoadError", { message: errorMessage(error) })}
        </p>
      </div>
    );
  }

  // Loading, or mid-redirect after a 401.
  return <div className="gx-auth-boot" aria-busy="true" aria-label={t("common.loading")} />;
}
