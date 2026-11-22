import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@connectrpc/connect-query";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import { campaignDestination } from "@/app/destinations";
import { useScreenNav } from "@/app/screenNav";
import { Card } from "@/components/ui/Card";
import { FirstCampaignCTA } from "@/components/FirstCampaignCTA";
import { useI18n } from "@/i18n";
import { errorMessage, isNotFound } from "@/lib/connectError";
import { CampaignScreenProvider, rosterRetry, type CampaignScreen } from "./campaignContext";
import { CampaignSettingsPanel } from "./CampaignSettingsPanel";
import { CastPanel } from "./CastPanel";
import { KnowledgePanel } from "./KnowledgePanel";
import { MapsPanel } from "./MapsPanel";
import { OverviewPanel } from "./OverviewPanel";
import { PlanningPanel } from "./PlanningPanel";
import { PlayersPanel } from "./PlayersPanel";
import { ProposalsPanel } from "./ProposalsPanel";
import { DEFAULT_CAMPAIGN_VIEW, type CampaignView } from "./views";

import "./campaign.css";

// The Campaign screen is a thin HOST (ADR-0063 amendment). It does four things
// and nothing else:
//
//   1. resolves WHICH campaign is active, and remounts everything under it when
//      that changes (see Campaign below);
//   2. renders the header — the campaign's name, its System, the view's lede;
//   3. switches on the view the route path names, one panel file per view;
//   4. hands the panels what they share through useCampaignScreen(): the
//      campaign, its roster and the cross-view jumps.
//
// Everything a view does lives in its own panel file (OverviewPanel, CastPanel,
// KnowledgePanel, MapsPanel, PlayersPanel, ProposalsPanel, PlanningPanel,
// CampaignSettingsPanel). The host takes no callbacks from the route: it reaches
// navigation through useScreenNav(), so it — and every panel — stays router-free.

// useCampaignEpoch counts Active Campaign CHANGES: it stays 0 while the first
// campaign id arrives (undefined → id is a load, not a switch) and steps each
// time a different id replaces a known one. It is the remount key for
// everything that holds per-campaign UI state.
function useCampaignEpoch(campaignID: string | undefined): number {
  const [seen, setSeen] = useState<{ id?: string; epoch: number }>({ epoch: 0 });
  if (campaignID && campaignID !== seen.id) {
    // Adjusting state during render (not in an effect) so the stale subtree
    // never paints against the new campaign.
    setSeen({ id: campaignID, epoch: seen.id ? seen.epoch + 1 : seen.epoch });
  }
  return seen.epoch;
}

// Campaign is the screen's entry point: it only resolves WHICH campaign is
// active and keys the real screen on it. The Active Campaign is ambient server
// state (ADR-0039) — no request carries the campaign id — so a switch made in
// the topbar changes what every write below lands in without unmounting
// anything. Left mounted, an open entry editor, an unapplied AI draft, an open
// map or a selected planning thread from the PREVIOUS campaign would survive
// the switch, and "Add entries" would write that draft into the new campaign.
// Remounting on the campaign id drops all of it at once.
export function Campaign({ view = DEFAULT_CAMPAIGN_VIEW }: { view?: CampaignView }) {
  // Same retry rule as the screen's own observer of this query: whichever
  // observer mounts first decides how the fetch retries, and this one does.
  const { data } = useQuery(CampaignService.method.getCampaignRoster, {}, { retry: rosterRetry });
  const epoch = useCampaignEpoch(data?.campaign?.id);

  // A selection held in the URL (?node=, ?agent=, ?map=, ?thread=) names a row
  // of the PREVIOUS campaign once the Active Campaign has changed: drop it with
  // the rest of the per-campaign state. Runs after the keyed subtree below has
  // remounted, so no editor of the old campaign is left to object.
  const { search, setSearch } = useScreenNav();
  const lastEpoch = useRef(epoch);
  useEffect(() => {
    if (epoch === lastEpoch.current) return;
    lastEpoch.current = epoch;
    if (search.node || search.agent || search.map || search.thread) {
      setSearch({ node: undefined, agent: undefined, map: undefined, thread: undefined }, { replace: true });
    }
    // The epoch is the trigger; search and setSearch are read as of the switch.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [epoch]);

  return <CampaignHost key={epoch} view={view} />;
}

function CampaignHost({ view }: { view: CampaignView }) {
  const { t } = useI18n();
  const nav = useScreenNav();
  const { data, status, error } = useQuery(
    CampaignService.method.getCampaignRoster,
    {},
    { retry: rosterRetry },
  );
  const campaign = data?.campaign;
  const roster = useMemo(() => data?.roster ?? [], [data]);

  // Cross-view jumps are navigations, not screen state: the target view lives
  // in the path and the thing to open rides as a search param the target panel
  // consumes (?node= on the wiki, ?agent= on Cast). Nothing is copied into
  // local state on the way, so Back returns to where the jump came from.
  const { goCampaignView, goScreen, setSearch } = nav;
  const screen = useMemo<CampaignScreen>(
    () => ({
      campaign,
      roster,
      openNode: (nodeID) => goCampaignView("knowledge", { node: nodeID }),
      openAgent: (agentID) => goCampaignView("cast", { agent: agentID }),
      openView: (next) => goCampaignView(next),
    }),
    [campaign, roster, goCampaignView],
  );

  // The wiki's focus IS the route's ?node — there is no local copy to drift
  // from the URL. The panel decides when the focus has been honoured (once the
  // entry's row has loaded) and reports it; only then is the param stripped
  // (replace, no history entry), so a deep link survives a slow or retried
  // list load, and a later jump to the same entry fires again.
  const focusNodeID = nav.search.node ?? null;
  const stripNode = useCallback(
    () => setSearch({ node: undefined }, { replace: true }),
    [setSearch],
  );
  // An appearances row jumps to the exact Transcript Line on the Session screen
  // — the session+line pair the palette uses (#591).
  const openTranscriptLine = useCallback(
    (sessionID: string, lineID: string) => goScreen("session", { session: sessionID, line: lineID }),
    [goScreen],
  );

  // Zero campaigns (#267): every campaign-scoped read is CodeNotFound, so the
  // create-first-campaign card stands in for the whole screen — whichever view
  // the URL names — instead of a raw "no active campaign" error in each panel.
  // Creating swaps the live screen in.
  if (!data && status === "error" && isNotFound(error)) {
    return (
      <div className="gx-campaign-screen" data-view={view}>
        <header className="gx-campaign-screen__header">
          <div className="gx-campaign-screen__title-row">
            <h1>{t("campaign.fallbackTitle")}</h1>
          </div>
        </header>
        <Card accent>
          <FirstCampaignCTA />
        </Card>
      </div>
    );
  }

  return (
    <CampaignScreenProvider value={screen}>
      {/* data-view lets a view's stylesheet target the screen root (a view that
          needs the full height, say) without the host knowing about it. */}
      <div className="gx-campaign-screen" data-view={view}>
        <header className="gx-campaign-screen__header">
          {/* The view tabs that used to sit beside the title live in the
              sidebar (ADR-0063); the header keeps the campaign identity and
              the per-view lede. */}
          <div className="gx-campaign-screen__title-row">
            <h1>{campaign?.name ?? t("campaign.fallbackTitle")}</h1>
          </div>
          <div className="gx-campaign-screen__sub">
            {campaign?.system && <span className="gx-campaign-screen__system">{campaign.system}</span>}
            <span className="gx-campaign-screen__lede">{t(campaignDestination(view).lede)}</span>
          </div>
        </header>

        {view === "overview" ? (
          <OverviewPanel />
        ) : view === "cast" ? (
          <CastPanel />
        ) : view === "knowledge" ? (
          <KnowledgePanel
            focusNodeID={focusNodeID}
            onFocusHandled={stripNode}
            onOpenCast={screen.openAgent}
            onOpenLine={openTranscriptLine}
          />
        ) : view === "maps" ? (
          <MapsPanel onOpenNode={screen.openNode} />
        ) : view === "players" ? (
          <PlayersPanel />
        ) : view === "proposals" ? (
          <ProposalsPanel />
        ) : view === "planning" ? (
          // The ChatService RPCs are campaign-scoped (ADR-0062), so the panel
          // waits for the roster read to hand it the campaign id.
          campaign ? (
            <PlanningPanel key={campaign.id} campaignId={campaign.id} />
          ) : status === "error" ? (
            <p className="gx-campaign__error" role="alert">
              {t("campaign.loadError", { message: errorMessage(error) })}
            </p>
          ) : (
            <div className="gx-skeleton" data-testid="planning-loading" />
          )
        ) : (
          <CampaignSettingsPanel />
        )}
      </div>
    </CampaignScreenProvider>
  );
}
