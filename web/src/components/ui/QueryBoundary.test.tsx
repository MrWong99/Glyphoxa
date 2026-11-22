import type { ReactNode } from "react";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { EmptyState } from "./EmptyState";
import { QueryBoundary, type QueryLike } from "./QueryBoundary";
import { Skeleton } from "./Skeleton";

type Roster = { agents: string[] };

function withClient(ui: ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={client}>{ui}</QueryClientProvider>;
}

function RosterPanel({
  load,
  enabled = true,
}: {
  load: () => Promise<Roster>;
  enabled?: boolean;
}) {
  const query = useQuery({ queryKey: ["roster"], queryFn: load, enabled });
  return (
    <QueryBoundary
      query={query}
      skeleton={<Skeleton.Row count={2} label="Loading the cast" />}
      idle={<p>pick a campaign first</p>}
      isEmpty={(roster) => roster.agents.length === 0}
      empty={<EmptyState title="No NPCs yet" />}
    >
      {(roster) => (
        <ul>
          {roster.agents.map((name) => (
            <li key={name}>{name}</li>
          ))}
        </ul>
      )}
    </QueryBoundary>
  );
}

describe("QueryBoundary", () => {
  it("shows the skeleton while pending, then the children with the data", async () => {
    let resolve!: (roster: Roster) => void;
    const load = () => new Promise<Roster>((r) => (resolve = r));
    render(withClient(<RosterPanel load={load} />));

    expect(screen.getByRole("status", { name: "Loading the cast" })).toBeInTheDocument();
    expect(screen.queryByRole("list")).toBeNull();

    resolve({ agents: ["Flo", "Mira"] });
    expect(await screen.findByText("Flo")).toBeInTheDocument();
    expect(screen.getByText("Mira")).toBeInTheDocument();
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("renders an ErrorState on failure, and Try again refetches into the content", async () => {
    const load = vi
      .fn<() => Promise<Roster>>()
      .mockRejectedValueOnce(new Error("upstream connect timeout"))
      .mockResolvedValueOnce({ agents: ["Flo"] });
    render(withClient(<RosterPanel load={load} />));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Couldn't load this");
    // The raw error is there, folded.
    expect(screen.getByText("Technical details").closest("details")).toHaveTextContent(
      "upstream connect timeout",
    );
    expect(screen.queryByRole("list")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByText("Flo")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(load).toHaveBeenCalledTimes(2);
  });

  // TanStack Query drops a data-less query back to "pending" when it refetches.
  // Showing the skeleton there would unmount the button that was just pressed
  // and leave a keyboard user on <body>; the panel stays, its button busy.
  it("keeps the error panel, busy, during a retry, and shows the new error if that fails too", async () => {
    let rejectSecond!: (error: Error) => void;
    const load = vi
      .fn<() => Promise<Roster>>()
      .mockRejectedValueOnce(new Error("first"))
      .mockImplementationOnce(() => new Promise<Roster>((_, reject) => (rejectSecond = reject)));
    render(withClient(<RosterPanel load={load} />));

    const button = await screen.findByRole("button", { name: "Try again" });
    button.focus();
    fireEvent.click(button);
    await waitFor(() => expect(button).toHaveAttribute("aria-busy", "true"));
    expect(load).toHaveBeenCalledTimes(2);
    expect(screen.getByRole("alert")).toBeInTheDocument();
    expect(screen.queryByRole("status", { name: "Loading the cast" })).toBeNull();
    expect(screen.getByRole("button", { name: "Try again" })).toBe(button);

    // A browser takes focus off a control that becomes disabled (the focus
    // fixup, on its next frame; jsdom does the same). Every real retry outlasts
    // a frame, so do it here instead of racing one.
    button.blur();
    expect(document.body).toHaveFocus();

    rejectSecond(new Error("second"));
    await waitFor(() =>
      expect(screen.getByText("Technical details").closest("details")).toHaveTextContent("second"),
    );
    // The same button, enabled again, holding focus again.
    expect(screen.getByRole("button", { name: "Try again" })).toBe(button);
    expect(button).toBeEnabled();
    expect(button).toHaveFocus();
  });

  // Only a retry the GM asked for keeps the panel: a query that is simply
  // loading again (a new key, a refetch from elsewhere) reads as loading.
  it("shows the skeleton, not a stale error, for a load it did not start itself", () => {
    const failed: QueryLike<Roster> = {
      data: undefined,
      error: new Error("first"),
      isPending: false,
      isError: true,
      fetchStatus: "idle",
      refetch: () => {},
    };
    const { rerender } = render(<QueryBoundary query={failed}>{() => null}</QueryBoundary>);
    expect(screen.getByRole("alert")).toBeInTheDocument();

    rerender(
      <QueryBoundary query={{ ...failed, error: null, isPending: true, isError: false, fetchStatus: "fetching" }}>
        {() => null}
      </QueryBoundary>,
    );
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.getByRole("status")).toBeInTheDocument();
  });

  // A query-like source that keeps its error while it retries shows the retry
  // as a busy button instead.
  it("marks Try again busy when the source is fetching while still in error", () => {
    const query: QueryLike<Roster> = {
      data: undefined,
      error: new Error("still failing"),
      isPending: false,
      isError: true,
      isFetching: true,
      refetch: () => {},
    };
    render(<QueryBoundary query={query}>{() => null}</QueryBoundary>);
    const button = screen.getByRole("button", { name: "Try again" });
    expect(button).toHaveAttribute("aria-busy", "true");
    expect(button).toBeDisabled();
  });

  it("shows the empty state when the loaded data is empty", async () => {
    render(withClient(<RosterPanel load={async () => ({ agents: [] })} />));
    expect(await screen.findByText("No NPCs yet")).toBeInTheDocument();
    expect(screen.queryByRole("list")).toBeNull();
  });

  it("shows the idle fallback, not a skeleton, for a disabled query", () => {
    const load = vi.fn<() => Promise<Roster>>();
    render(withClient(<RosterPanel load={load} enabled={false} />));
    expect(screen.getByText("pick a campaign first")).toBeInTheDocument();
    expect(screen.queryByRole("status")).toBeNull();
    expect(load).not.toHaveBeenCalled();
  });

  // Stale-while-error: data already on screen survives a failed refetch.
  it("keeps showing data it already has when a later fetch fails", () => {
    const query: QueryLike<Roster> = {
      data: { agents: ["Flo"] },
      error: new Error("refetch failed"),
      isPending: false,
      isError: true,
      isFetching: false,
      refetch: () => {},
    };
    render(<QueryBoundary query={query}>{(roster) => <p>{roster.agents.join(", ")}</p>}</QueryBoundary>);
    expect(screen.getByText("Flo")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("defaults to a plain skeleton and accepts plain-node children", () => {
    const pending: QueryLike<Roster> = {
      data: undefined,
      error: null,
      isPending: true,
      isError: false,
      fetchStatus: "fetching",
      refetch: () => {},
    };
    const { rerender } = render(
      <QueryBoundary query={pending}>
        <p>loaded</p>
      </QueryBoundary>,
    );
    expect(screen.getByRole("status", { name: "Loading…" })).toBeInTheDocument();

    rerender(
      <QueryBoundary query={{ ...pending, data: { agents: [] }, isPending: false }}>
        <p>loaded</p>
      </QueryBoundary>,
    );
    expect(screen.getByText("loaded")).toBeInTheDocument();
  });
});
