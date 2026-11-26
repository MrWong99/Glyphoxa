import { useEffect, useRef, useState, useSyncExternalStore } from "react";
import { createClient } from "@connectrpc/connect";
import { useTransport } from "@connectrpc/connect-query";
import { onlineManager, useQueryClient } from "@tanstack/react-query";
import { CloudOff, RefreshCw, WifiOff } from "lucide-react";

import { AuthService } from "@gen/glyphoxa/management/v1/management_pb";
import { Button } from "./ui/Button";
import { Spinner } from "./ui/Spinner";
import { useI18n } from "@/i18n";
import { connectionStore } from "@/lib/connectionStatus";

// ConnectionBanner is the shell's "something is between you and Glyphoxa" bar
// (#695: no offline indication), mounted between the topbar and the screen so
// it is visible from every screen without covering anything. Two states:
//
//   offline      the browser itself has no network (TanStack's onlineManager).
//                Reads are paused, not failed, and resume on their own — there
//                is nothing to retry, so the bar only says so.
//   unreachable  the browser is online but calls come back unanswered: the
//                server is restarting, or the tunnel in front of it is down
//                (lib/connectionStatus.ts decides what counts). The polls the
//                shell runs anyway keep trying; "Retry now" asks at once.
//
// Without it a GM whose connection dropped mid-session saw nothing at all
// until some screen's own read failed — and then a screen-sized error that
// blamed the screen.
//
// The wrapper is always in the DOM (and empty while all is well): a live
// region announces a change reliably only if it existed before the change.

const subscribeOnline = (listener: () => void) => onlineManager.subscribe(listener);
const isOnline = () => onlineManager.isOnline();

export function ConnectionBanner() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const transport = useTransport();
  const store = connectionStore(queryClient);

  const online = useSyncExternalStore(subscribeOnline, isOnline, isOnline);
  const unreachable = useSyncExternalStore(store.subscribe, store.isUnreachable, store.isUnreachable);
  const down = !online || unreachable;

  const [retrying, setRetrying] = useState(false);
  // Set once a "Retry now" came back empty-handed, so the click has a visible
  // (and announced) outcome instead of an unchanged bar.
  const [retryFailed, setRetryFailed] = useState(false);

  // Back up: every read that failed while the connection was down is re-run,
  // so a screen showing its error state recovers without the GM pressing each
  // screen's own "Try again".
  const wasDown = useRef(false);
  useEffect(() => {
    if (wasDown.current && !down) {
      setRetryFailed(false);
      void queryClient.refetchQueries({
        type: "active",
        predicate: (query) => query.state.status === "error",
      });
    }
    wasDown.current = down;
  }, [down, queryClient]);

  const mounted = useRef(true);
  useEffect(
    () => () => {
      mounted.current = false;
    },
    [],
  );

  // One cheap call whose only job is to find out whether anyone answers. It
  // reports to the same store the query cache feeds: a result — or an error
  // the server itself wrote, such as "not signed in" — lowers the flag, and
  // the effect above then re-runs the failed reads (which is also what sends
  // a lapsed session to the login page).
  const retry = async () => {
    if (retrying) return;
    setRetrying(true);
    try {
      await createClient(AuthService, transport).getCurrentUser({});
      store.noteSuccess();
    } catch (err) {
      store.noteError(err);
    }
    if (!mounted.current) return;
    setRetrying(false);
    setRetryFailed(store.isUnreachable());
  };

  return (
    <div className="gx-connection" role="status" data-state={!online ? "offline" : unreachable ? "unreachable" : undefined}>
      {!online ? (
        <>
          <WifiOff size={16} aria-hidden="true" />
          <span className="gx-connection__text">{t("shell.connOffline")}</span>
        </>
      ) : unreachable ? (
        <>
          <CloudOff size={16} aria-hidden="true" />
          <span className="gx-connection__text">
            {retryFailed ? t("shell.connStillUnreachable") : t("shell.connUnreachable")}
          </span>
          {/* Kept focusable while it works (aria-disabled, not disabled): a
              keyboard user who pressed it would otherwise be dropped onto
              <body> for the length of the attempt. */}
          <Button
            variant="secondary"
            size="sm"
            aria-disabled={retrying || undefined}
            aria-busy={retrying || undefined}
            onClick={() => void retry()}
            iconStart={retrying ? <Spinner /> : <RefreshCw size={14} />}
          >
            {t("shell.connRetry")}
          </Button>
        </>
      ) : null}
    </div>
  );
}
