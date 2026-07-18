import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { Dices } from "lucide-react";
import { useMutation, useQuery, createConnectQueryKey } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";

import { AuthService } from "@gen/glyphoxa/management/v1/management_pb";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Input";
import { isUnauthenticated } from "@/lib/connectError";

import "@/screens/login/login.css";

// The ADR-0055 name-your-Tenant onboarding step. The OAuth callback 302s a
// FRESH open-mode signup here (onboardingRedirect in internal/auth/oauth.go —
// the path is a Go↔TS contract pinned by router.test.tsx), with the Tenant
// already provisioned under a placeholder name like "Rin's Table". The screen
// lives OUTSIDE the tenant shell (no AppShell, no AuthGate), so it does its own
// session probe and reuses the login card's visual vocabulary — and, like
// Login, it sits outside the Toaster host, so failures surface as inline cues.

// The cosmetic tenant slug in the app path (ADR-0039 pass-through — the server
// scopes every RPC by session, not by this slug). Mirrors DEFAULT_TENANT in
// router.tsx, which cannot be imported here without a router↔screen cycle.
const DEFAULT_TENANT = "default";

export function CreateTenant() {
  const navigate = useNavigate();
  // retry off so a 401 (deep link with no session) bounces to /login
  // immediately rather than after react-query's default backoff.
  const { data, status, error } = useQuery(AuthService.method.getCurrentUser, {}, { retry: false });

  const unauthenticated = status === "error" && isUnauthenticated(error);

  useEffect(() => {
    if (unauthenticated) {
      void navigate({ to: "/login" });
    }
  }, [unauthenticated, navigate]);

  if (status === "success") {
    return <NameTenantCard initialName={data.tenantName} />;
  }

  // A non-401 error is a real failure (server down, etc.) — surface a generic,
  // non-leaky cue rather than bouncing to /login, which would loop.
  if (status === "error" && !unauthenticated) {
    return (
      <div className="gx-login">
        <div className="gx-login__card">
          <p className="gx-login__error" role="alert">
            Couldn&apos;t load your account. Please reload to try again.
          </p>
        </div>
      </div>
    );
  }

  // Loading, or mid-redirect after a 401.
  return <div className="gx-auth-boot" aria-busy="true" aria-label="Loading" />;
}

// NameTenantCard is only mounted once GetCurrentUser has resolved, so the
// field's local state can seed straight from the pre-provisioned Tenant name.
function NameTenantCard({ initialName }: { initialName: string }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const [name, setName] = useState(initialName);

  // Both continue paths land on the app entry; the shell's AuthGate re-probes
  // the session there, so the sidebar shows the (possibly renamed) Tenant.
  const goToApp = () =>
    void navigate({
      to: "/t/$tenantSlug/$screen",
      params: { tenantSlug: DEFAULT_TENANT, screen: "configuration" },
    });

  const rename = useMutation(AuthService.method.renameTenant, {
    onSuccess: () => {
      // Drop the cached probe so the identity (incl. tenantName) is fresh for
      // whichever surface reads it next — nothing renders tenantName today, so
      // this keeps the cache honest rather than serving a known consumer.
      void queryClient.invalidateQueries({
        queryKey: createConnectQueryKey({
          schema: AuthService.method.getCurrentUser,
          cardinality: "finite",
        }),
      });
      goToApp();
    },
  });

  const canSubmit = name.trim() !== "" && !rename.isPending;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (!canSubmit) return;
    rename.mutate({ name: name.trim() });
  };

  return (
    <div className="gx-login">
      <form className="gx-login__card" onSubmit={submit}>
        <span className="gx-login__sigil">
          <Dices size={24} />
        </span>
        <h1 className="gx-login__wordmark gx-gradient-text">Welcome to Glyphoxa</h1>
        <p className="gx-login__lede">Your table is ready — give it a name.</p>

        <div className="gx-login__field">
          <Input
            label="Name your table"
            value={name}
            onChange={(e) => setName(e.target.value)}
            disabled={rename.isPending}
            autoFocus
            required
          />
        </div>

        {rename.isError && (
          <p className="gx-login__error" role="alert">
            Couldn&apos;t save the name — you can rename your table later, or try again now.
          </p>
        )}

        <Button type="submit" variant="primary" size="lg" block disabled={!canSubmit}>
          {rename.isPending ? "Saving…" : "Save and continue"}
        </Button>
        <Button type="button" variant="ghost" block onClick={goToApp} disabled={rename.isPending}>
          Skip for now
        </Button>
      </form>
    </div>
  );
}
