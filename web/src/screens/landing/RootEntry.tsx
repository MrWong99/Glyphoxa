import { useEffect, useState } from "react";
import { useQuery } from "@connectrpc/connect-query";
import { useNavigate } from "@tanstack/react-router";

import { AuthService, AdmissionMode } from "@gen/glyphoxa/management/v1/management_pb";
import { useI18n } from "@/i18n";
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
// the API is not. A backend that HANGS (accepts, never answers — e.g. a wedged
// origin behind the launch topology's Tunnel) is bounded the same way: after
// PROBE_GRACE_MS of pending, the landing renders anyway, and the signed-in
// redirect still fires if the probe eventually resolves.
//
// The deployment's Admission Mode (ADR-0055) is probed alongside the session —
// the same public GetAdmissionMode Login uses — so the landing only advertises
// the free-beta signup on an explicitly OPEN deployment (fail-safe: neutral
// copy, plain sign-in CTA).

// The cosmetic tenant slug in the app path (ADR-0039 pass-through) — the same
// "default" the AuthGate login flow lands on.
const DEFAULT_TENANT = "default";

// How long the boot placeholder may hold the public root before the static
// landing page wins over a still-pending session probe.
const PROBE_GRACE_MS = 2500;

export function RootEntry() {
  const { t } = useI18n();
  const navigate = useNavigate();
  // retry off so an unauthenticated visitor sees the landing page immediately
  // rather than after react-query's default backoff.
  const { data, status } = useQuery(AuthService.method.getCurrentUser, {}, { retry: false });
  const admission = useQuery(AuthService.method.getAdmissionMode, {}, { retry: false });
  const signedIn = status === "success" && Boolean(data?.user);
  const open = admission.data?.admissionMode === AdmissionMode.OPEN;

  const [graceOver, setGraceOver] = useState(false);
  useEffect(() => {
    const t = setTimeout(() => setGraceOver(true), PROBE_GRACE_MS);
    return () => clearTimeout(t);
  }, []);

  useEffect(() => {
    if (signedIn) {
      void navigate({
        to: "/t/$tenantSlug/$screen",
        params: { tenantSlug: DEFAULT_TENANT, screen: "configuration" },
        replace: true,
      });
    }
  }, [signedIn, navigate]);

  if ((status === "pending" && !graceOver) || signedIn) {
    return <div className="gx-auth-boot" aria-busy="true" aria-label={t("common.loading")} />;
  }
  return <Landing open={open} />;
}
