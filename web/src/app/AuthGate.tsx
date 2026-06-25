import { useEffect } from "react";
import type { ReactNode } from "react";
import { useQuery } from "@connectrpc/connect-query";
import { useNavigate } from "@tanstack/react-router";
import { ConnectError } from "@connectrpc/connect";

import { AuthService } from "@gen/glyphoxa/management/v1/management_pb";
import type { User } from "@gen/glyphoxa/management/v1/management_pb";
import { isUnauthenticated } from "@/lib/auth";

// AuthGate is the SPA boot gate (ADR-0016 / ADR-0039): it probes
// AuthService.GetCurrentUser and, on CodeUnauthenticated, redirects to /login.
// On success it hands the resolved operator to its render-prop child (the app
// shell), so the sidebar shows the real identity instead of a hardcoded one.
// retry is off so a 401 redirects immediately rather than after react-query's
// default backoff.
export function AuthGate({ children }: { children: (user: User) => ReactNode }) {
  const navigate = useNavigate();
  const { data, status, error } = useQuery(
    AuthService.method.getCurrentUser,
    {},
    { retry: false },
  );

  const unauthenticated = status === "error" && isUnauthenticated(error);

  useEffect(() => {
    if (unauthenticated) {
      void navigate({ to: "/login" });
    }
  }, [unauthenticated, navigate]);

  if (status === "success" && data.user) {
    return <>{children(data.user)}</>;
  }

  // A non-401 error is a real failure (server down, etc.) — surface it rather
  // than bouncing to /login, which would loop.
  if (status === "error" && !unauthenticated) {
    return (
      <div className="gx-providers">
        <p className="gx-campaign__error" role="alert">
          Could not load your account: {ConnectError.from(error).message}
        </p>
      </div>
    );
  }

  // Loading, or mid-redirect after a 401.
  return <div className="gx-auth-boot" aria-busy="true" aria-label="Loading" />;
}
