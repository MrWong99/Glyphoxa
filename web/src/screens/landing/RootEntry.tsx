import { useEffect } from "react";
import { useQuery } from "@connectrpc/connect-query";
import { useNavigate } from "@tanstack/react-router";

import { AuthService } from "@gen/glyphoxa/management/v1/management_pb";
import { Landing } from "./Landing";

// What the ROOT path serves (#521).
//
// Before: "/" redirected straight into the app, so an unauthenticated visitor
// bounced off the AuthGate into a bare login form — no explanation of what this
// is, no path into signup. Now the root probes the session once:
//
//   - signed in  → straight to the app, exactly as before (no landing detour);
//   - otherwise  → the public landing page, whose CTA goes to /login, leaving
//     the OAuth flow (and #518's open-mode acknowledgment) untouched.
//
// A backend that is down or erroring resolves to the landing page rather than a
// spinner: it is a static document, so the marketing surface stays up even when
// the API is not.

// The cosmetic tenant slug in the app path (ADR-0039 pass-through). Mirrors
// DEFAULT_TENANT in router.tsx, which cannot be imported here without a
// router↔screen cycle.
const DEFAULT_TENANT = "default";

export function RootEntry() {
  const navigate = useNavigate();
  // retry off so an unauthenticated visitor sees the landing page immediately
  // rather than after react-query's default backoff.
  const { data, status } = useQuery(AuthService.method.getCurrentUser, {}, { retry: false });
  const signedIn = status === "success" && Boolean(data?.user);

  useEffect(() => {
    if (signedIn) {
      void navigate({
        to: "/t/$tenantSlug/$screen",
        params: { tenantSlug: DEFAULT_TENANT, screen: "configuration" },
        replace: true,
      });
    }
  }, [signedIn, navigate]);

  if (status === "pending" || signedIn) {
    return <div className="gx-auth-boot" aria-busy="true" aria-label="Loading" />;
  }
  return <Landing />;
}
