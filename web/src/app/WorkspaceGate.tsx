import { Fragment, useEffect, useRef, useState, type ReactNode } from "react";
import { createConnectQueryKey, useMutation } from "@connectrpc/connect-query";
import { useQueryClient, type Query } from "@tanstack/react-query";
import { useNavigate, useRouterState } from "@tanstack/react-router";

import { AuthService } from "@gen/glyphoxa/management/v1/management_pb";
import { hasNoWorkspace, type CurrentUser } from "@/app/currentUser";
import { guestWorkspaceSlug, slugGuestCampaign } from "@/app/workspace";
import { WorkspaceExits, WorkspaceNotice } from "@/app/WorkspaceNotice";
import { useI18n } from "@/i18n";
import { errorMessage } from "@/lib/connectError";

// WorkspaceGate keeps three things in step (ADR-0065): the Workspace the URL
// names (app/workspace.ts), the Workspace the server-side session acts as
// (GetCurrentUser's `guest`), and the query cache — which must never hold one
// Workspace's answers while another is on screen. Requests carry no Workspace:
// the server resolves it from the session, so two Workspaces' reads share the
// very same cache keys.
//
// The URL is the intent. When it and the session disagree:
//
//   - the URL names a Workspace this user can enter (their own Tenant, or a
//     Campaign they are a guest of) → the session is switched to it
//     (SwitchWorkspace) and the user re-read. This is the workspace switcher
//     (it only navigates), a bookmark, and a tab that was left in another
//     Workspace taking the session back when it is used again;
//   - the URL names the own Workspace but the user has none (a guest-only
//     account on the /t/default link every sign-in lands on) → the URL is
//     rewritten to the Workspace the session does act as;
//   - the URL names a Campaign this user is not a guest of — never was, or was
//     removed while looking at it → a plain page saying so, with the ways out.
//
// The shell is mounted only while the two agree AND the cache is known to
// belong to that Workspace. Whenever they stop agreeing the shell comes down
// first; once they agree again everything but the session probe is cancelled
// and dropped, and only then does a fresh shell mount — so the new principal
// starts from nothing, and no screen ever renders one Workspace's cached rows
// under another's name. (The price: a tab whose session was switched from
// another tab loses what it had open when it takes the session back.)

const currentUserKey = createConnectQueryKey({
  schema: AuthService.method.getCurrentUser,
  cardinality: "finite",
});

/** Everything in the cache except the session probe, which is about the user, not a Workspace. */
function notSessionProbe(query: Query): boolean {
  const meta = query.queryKey[1] as { serviceName?: string; methodName?: string } | undefined;
  return !(
    query.queryKey[0] === "connect-query" &&
    meta?.serviceName === AuthService.typeName &&
    meta.methodName === AuthService.method.getCurrentUser.name
  );
}

function Boot() {
  const { t } = useI18n();
  return <div className="gx-auth-boot" aria-busy="true" aria-label={t("common.loading")} />;
}

export function WorkspaceGate({
  tenantSlug,
  user,
  children,
}: {
  tenantSlug: string;
  user: CurrentUser;
  children: (user: CurrentUser) => ReactNode;
}) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const location = useRouterState({ select: (s) => s.location });
  const { mutateAsync: doSwitch } = useMutation(AuthService.method.switchWorkspace);

  const wanted = slugGuestCampaign(tenantSlug);
  const actual = user.guest?.campaignId ?? null;
  // One key per Workspace a URL can name: every non-guest slug is the own one.
  const key = wanted ?? "";
  const wantedIsShared = wanted !== null && user.guestWorkspaces.some((w) => w.campaignId === wanted);

  const plan: "nowhere" | "show" | "switch" | "rewrite" | "gone" = hasNoWorkspace(user)
    ? "nowhere"
    : wanted === actual
      ? "show"
      : wanted === null
        ? user.hasOwnTenant
          ? "switch"
          : "rewrite"
        : wantedIsShared
          ? "switch"
          : "gone";

  // The Workspace the cache belongs to; null when it is not known to belong to
  // any. A first render that already agrees has nothing to drop.
  const [cleanFor, setCleanFor] = useState<string | null>(() => (plan === "show" ? key : null));
  useEffect(() => {
    if (plan !== "show") {
      // Whatever is fetched from here on was asked as some other principal.
      setCleanFor(null);
      return;
    }
    if (cleanFor === key) return;
    void queryClient.cancelQueries({ predicate: notSessionProbe });
    queryClient.removeQueries({ predicate: notSessionProbe });
    setCleanFor(key);
  }, [plan, cleanFor, key, queryClient]);

  // The name of the guest Campaign last shown for this URL, so "no longer
  // shared with you" can say which one it was.
  const lastShown = useRef<{ key: string; campaign?: string } | null>(null);
  if (plan === "show") lastShown.current = { key, campaign: user.guest?.campaignName };

  // How a switch went, once it is over: failed with an error, or answered
  // without the session following. Each attempt has a number, and only the
  // latest attempt's outcome is ever shown — one that reports after the URL or
  // the session has moved on is about a question nobody is asking any more.
  const [outcome, setOutcome] = useState<{ attempt: number; error?: unknown } | null>(null);
  const [retries, setRetries] = useState(0);
  const attempt = useRef(0);
  const startedFor = useRef<string | null>(null);
  useEffect(() => {
    if (plan !== "switch") {
      startedFor.current = null;
      attempt.current += 1;
      return;
    }
    if (startedFor.current === tenantSlug) return;
    startedFor.current = tenantSlug;
    const mine = ++attempt.current;
    void (async () => {
      try {
        await doSwitch({ campaignId: wanted ?? "" });
        await queryClient.refetchQueries({ queryKey: currentUserKey });
        setOutcome({ attempt: mine });
      } catch (error) {
        setOutcome({ attempt: mine, error });
      }
    })();
  }, [plan, tenantSlug, wanted, doSwitch, queryClient, retries]);

  // A guest-only account on an own-Workspace URL: same page, right slug.
  useEffect(() => {
    if (plan !== "rewrite" || actual === null) return;
    const rest = location.pathname.replace(/^\/t\/[^/]+/, "");
    void navigate({ href: `/t/${guestWorkspaceSlug(actual)}${rest}${location.searchStr}`, replace: true });
  }, [plan, actual, location.pathname, location.searchStr, navigate]);

  // "Not a guest of this Campaign" is only said on a fresh answer: the cached
  // user may predate an invitation redeemed in another tab.
  const [verified, setVerified] = useState<string | null>(null);
  useEffect(() => {
    if (plan !== "gone" || verified === tenantSlug) return;
    let live = true;
    void queryClient.refetchQueries({ queryKey: currentUserKey }).then(() => {
      if (live) setVerified(tenantSlug);
    });
    return () => {
      live = false;
    };
  }, [plan, verified, tenantSlug, queryClient]);

  const exits = (
    <WorkspaceExits
      user={user}
      except={key}
      onOpen={(slug) => void navigate({ to: "/t/$tenantSlug", params: { tenantSlug: slug } })}
    />
  );

  if (plan === "nowhere") {
    return (
      <WorkspaceNotice title={t("access.nowhereTitle")} body={t("access.nowhereBody")}>
        {exits}
      </WorkspaceNotice>
    );
  }

  if (plan === "gone") {
    if (verified !== tenantSlug) return <Boot />;
    const was = lastShown.current?.key === key ? lastShown.current.campaign : undefined;
    return (
      <WorkspaceNotice
        title={was ? t("access.goneTitleNamed", { campaign: was }) : t("access.goneTitle")}
        body={t("access.goneBody")}
      >
        {exits}
      </WorkspaceNotice>
    );
  }

  if (plan === "switch" && outcome?.attempt === attempt.current) {
    return (
      <WorkspaceNotice
        title={t("access.switchFailedTitle")}
        body={
          outcome.error !== undefined
            ? t("access.switchFailedBody", { message: errorMessage(outcome.error) })
            : t("access.switchNoEffectBody")
        }
        onRetry={() => {
          startedFor.current = null;
          setOutcome(null);
          setRetries((n) => n + 1);
        }}
      >
        {exits}
      </WorkspaceNotice>
    );
  }

  if (plan !== "show" || cleanFor !== key) return <Boot />;

  return (
    // Keyed by Workspace: nothing a screen holds in state crosses from one to
    // the other.
    <Fragment key={key}>{children(user)}</Fragment>
  );
}
