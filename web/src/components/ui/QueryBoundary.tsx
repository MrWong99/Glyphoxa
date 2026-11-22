import { useEffect, useState, type ReactNode } from "react";

import { errorMessage } from "@/lib/connectError";
import { ErrorState } from "./ErrorState";
import { Skeleton } from "./Skeleton";

// QueryBoundary — the three states of a read, decided in one place so every
// screen loads, fails and empties the same way:
//
//   pending, nothing to show yet   the skeleton (shape it like the content)
//   failed, nothing to show        ErrorState with a working "Try again"
//   otherwise                      the children, given the data
//
//   <QueryBoundary query={roster} skeleton={<Skeleton.Row count={4} />}
//                  isEmpty={(r) => r.agents.length === 0}
//                  empty={<EmptyState title="No NPCs yet" … />}>
//     {(r) => <RosterList agents={r.agents} />}
//   </QueryBoundary>
//
// A retry started from "Try again" keeps the ErrorState in place, its button
// busy, until the retry settles. TanStack Query drops a data-less query back
// to "pending" when it refetches; swapping to the skeleton there unmounted the
// button the GM had just pressed and dropped keyboard focus to <body>, and a
// second failure brought back a new button nobody was on. Keeping the same
// button mounted is what lets ErrorState hand focus back to it afterwards.
//
// "Nothing to show" is the point: once a query HAS data, a failed background
// refetch does not tear the screen down to an error. The data on screen stays
// (TanStack Query keeps it), and the next refetch can still succeed.
//
// It takes any TanStack Query result (useQuery from @tanstack/react-query or
// @connectrpc/connect-query); QueryLike names only the fields it reads.

export type QueryLike<T> = {
  data: T | undefined;
  error: unknown;
  isPending: boolean;
  isError: boolean;
  /** A fetch is in flight (first load, retry or background refetch). */
  isFetching?: boolean;
  /** "idle" while pending means the query is disabled, not loading. */
  fetchStatus?: "fetching" | "paused" | "idle";
  refetch: () => unknown;
};

export function QueryBoundary<T>({
  query,
  children,
  skeleton,
  idle = null,
  errorTitle,
  errorSize = "panel",
  isEmpty,
  empty = null,
}: {
  query: QueryLike<T>;
  /** The loaded content: a node, or a function of the data. */
  children: ReactNode | ((data: T) => ReactNode);
  /** Shown while pending. Defaults to three text lines; pass a shape that matches the content. */
  skeleton?: ReactNode;
  /** Shown while the query is disabled (enabled: false) and has no data. Default nothing. */
  idle?: ReactNode;
  /** Title of the ErrorState; defaults to the shared "Couldn't load this". */
  errorTitle?: ReactNode;
  errorSize?: "panel" | "inline";
  /** With `empty`: when it returns true for the loaded data, `empty` is shown instead of children. */
  isEmpty?: (data: T) => boolean;
  /** Usually an EmptyState. */
  empty?: ReactNode;
}) {
  const { data } = query;

  // The failure whose "Try again" is in flight. Cleared when that retry
  // settles: a new error (the effect runs when isError comes back) or data.
  const [retried, setRetried] = useState<{ error: unknown } | null>(null);
  useEffect(() => {
    if (query.isError || data !== undefined) setRetried(null);
  }, [query.isError, query.error, data]);

  if (data === undefined) {
    const retrying =
      retried !== null && !query.isError && query.isPending && query.fetchStatus !== "idle";
    if (query.isError || retrying) {
      return (
        <ErrorState
          title={errorTitle}
          message={errorMessage(query.isError ? query.error : retried?.error)}
          onRetry={() => {
            setRetried({ error: query.error });
            void query.refetch();
          }}
          retrying={retrying || (query.isFetching ?? false)}
          size={errorSize}
        />
      );
    }
    if (query.isPending) {
      // A disabled query is "pending" forever; a skeleton there would promise
      // content that is not on its way.
      if (query.fetchStatus === "idle") return <>{idle}</>;
      return <>{skeleton ?? <Skeleton />}</>;
    }
  }

  // Loaded (or a query whose result type is void).
  const loaded = data as T;
  if (isEmpty?.(loaded)) return <>{empty}</>;
  return <>{typeof children === "function" ? children(loaded) : children}</>;
}
