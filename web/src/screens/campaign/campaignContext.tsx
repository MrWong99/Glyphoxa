import { createContext, useCallback, useContext, useRef, useState, type ReactNode } from "react";

import type { Agent, Campaign } from "@gen/glyphoxa/management/v1/management_pb";
import { isNotFound } from "@/lib/connectError";
import type { CampaignView } from "./views";

// What the Campaign screen's host knows that every panel under it may need
// (ADR-0063 amendment). The host (Campaign.tsx) resolves the campaign and its
// roster once and owns every cross-view jump; a panel reads this context
// instead of growing a prop per thing it wants to open. So a panel is one file
// that depends on useCampaignScreen() and its own RPCs — which is what lets
// several people work on different panels without meeting in Campaign.tsx.
//
// Outside the host (a panel mounted alone in a test) the hook answers with an
// inert default: no campaign, an empty roster, jumps that do nothing. A test
// that cares about a jump mounts CampaignScreenProvider with spies.

export type CampaignScreen = {
  /** The Active Campaign, once the roster read has resolved it. */
  campaign: Campaign | undefined;
  /** The campaign's Agents — the Butler first, then the Character NPCs. */
  roster: Agent[];
  /** Opens the World wiki on one entry. */
  openNode: (nodeID: string) => void;
  /** Opens Cast on one Agent's editor. */
  openAgent: (agentID: string) => void;
  /** Goes to another view of this campaign. */
  openView: (view: CampaignView) => void;
};

const INERT: CampaignScreen = {
  campaign: undefined,
  roster: [],
  openNode: () => {},
  openAgent: () => {},
  openView: () => {},
};

// The panel-state store lives beside the context value rather than in it, so a
// panel writing its remembered state does not re-render every other consumer.
type PanelStore = Map<string, unknown>;

const CampaignScreenContext = createContext<CampaignScreen>(INERT);
const PanelStoreContext = createContext<PanelStore | null>(null);

export function CampaignScreenProvider({
  value,
  children,
}: {
  value: CampaignScreen;
  children: ReactNode;
}) {
  // One store per mounted host. The host is keyed on the Active Campaign's id
  // (see Campaign.tsx), so a campaign switch throws the store away with
  // everything else that is per-campaign.
  const store = useRef<PanelStore>(new Map());
  return (
    <CampaignScreenContext.Provider value={value}>
      <PanelStoreContext.Provider value={store.current}>{children}</PanelStoreContext.Provider>
    </CampaignScreenContext.Provider>
  );
}

/** The campaign, its roster and the cross-view jumps. Inert outside the host. */
export function useCampaignScreen(): CampaignScreen {
  return useContext(CampaignScreenContext);
}

/**
 * useState for UI state a panel wants back when the GM returns to it: which
 * NPC was selected, which mode was open. A panel unmounts when the GM switches
 * view in the sidebar; state held here lives in the host instead, so it
 * survives that switch — and is dropped, with the host, on an Active Campaign
 * switch. Outside the host it is plain useState.
 *
 * `key` names the slot; prefix it with the panel ("cast.selectedId").
 */
export function useCampaignPanelState<T>(key: string, initial: T): [T, (next: T) => void] {
  const store = useContext(PanelStoreContext);
  const [value, setValue] = useState<T>(() =>
    store?.has(key) ? (store.get(key) as T) : initial,
  );
  const set = useCallback(
    (next: T) => {
      store?.set(key, next);
      setValue(next);
    },
    [store, key],
  );
  return [value, set];
}

// CodeNotFound on the roster read is the settled zero-campaign answer, not a
// transient: don't sit through the default retries before showing the
// first-campaign card. Shared by every observer of getCampaignRoster under the
// Campaign screen — whichever mounts first decides how the fetch retries, so
// they must all agree.
export const rosterRetry = (count: number, err: unknown) => !isNotFound(err) && count < 3;
