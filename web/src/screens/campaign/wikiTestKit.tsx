import { useMemo, useRef, useState, type ReactNode } from "react";
import { act, fireEvent, screen } from "@testing-library/react";

import { ScreenNavProvider, compactSearch, type ScreenNav, type ScreenSearch } from "@/app/screenNav";

// Test kit for the World wiki's panel tests.
//
// The wiki keeps its open entry in the route's ?node=, so a test needs a
// ScreenNav that behaves like the router's: setSearch PUSHES a history entry
// unless asked to replace, and Back pops one. MemoryScreenNav (app/screenNav)
// holds only the current location; this one keeps the stack, so a test can
// press Back and assert where the wiki lands.

export type WikiHistory = {
  /** Every search the wiki navigated to, with how. */
  moves: { search: ScreenSearch; replace: boolean }[];
  /** The browser's Back button. */
  back: () => void;
  /** A navigation from outside the panel: the palette, another screen. */
  go: (search: ScreenSearch) => void;
  /** The current search. */
  current: () => ScreenSearch;
};

export function makeWikiHistory(): WikiHistory {
  return {
    moves: [],
    back: () => {},
    go: () => {},
    current: () => ({}),
  };
}

export function WikiNav({
  history,
  initial = {},
  children,
}: {
  history: WikiHistory;
  initial?: ScreenSearch;
  children: ReactNode;
}) {
  const [stack, setStack] = useState<ScreenSearch[]>(() => [compactSearch(initial)]);
  const search = stack[stack.length - 1];
  // The live stack, so two navigations issued in one tick both land (the
  // router-bound provider merges against the live search the same way).
  const live = useRef(stack);
  live.current = stack;
  const commit = (next: ScreenSearch[]) => {
    live.current = next;
    setStack(next);
  };

  history.back = () => act(() => commit(live.current.length > 1 ? live.current.slice(0, -1) : live.current));
  history.go = (next) => act(() => commit([...live.current, compactSearch(next)]));
  history.current = () => search;

  const nav = useMemo<ScreenNav>(
    () => ({
      screen: "campaign",
      view: "knowledge",
      search,
      setSearch: (patch, opts) => {
        const replace = opts?.replace ?? false;
        const s = live.current;
        const next = compactSearch({ ...s[s.length - 1], ...patch });
        history.moves.push({ search: next, replace });
        commit(replace ? [...s.slice(0, -1), next] : [...s, next]);
      },
      goScreen: () => {},
      goCampaignView: () => {},
      goWorkspace: () => {},
    }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [search, history],
  );

  return <ScreenNavProvider value={nav}>{children}</ScreenNavProvider>;
}

/** Opens the "New entry" menu and picks one of its items. */
export function pickNewEntry(item: string | RegExp) {
  // Radix opens a dropdown menu from the keyboard on Enter.
  const trigger = screen.getByRole("button", { name: "New entry" });
  act(() => trigger.focus());
  fireEvent.keyDown(trigger, { key: "Enter" });
  fireEvent.click(screen.getByRole("menuitem", { name: item }));
}
