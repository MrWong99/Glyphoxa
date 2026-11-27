import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { create } from "@bufbuild/protobuf";
import { useQuery } from "@connectrpc/connect-query";

import { CampaignSchema, CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import { useCan } from "@/app/capabilities";
import { useCurrentUser } from "@/app/currentUser";
import { campaignLede } from "@/app/destinations";
import { useScreenNav } from "@/app/screenNav";
import { Card } from "@/components/ui/Card";
import { Page, PageHeader } from "@/components/ui/Page";
import { QueryBoundary } from "@/components/ui/QueryBoundary";
import { Skeleton } from "@/components/ui/Skeleton";
import { FirstCampaignCTA } from "@/components/FirstCampaignCTA";
import { useI18n } from "@/i18n";
import { isNotFound } from "@/lib/connectError";
import { CampaignScreenProvider, rosterRetry, type CampaignScreen } from "./campaignContext";
import { CampaignSettingsPanel } from "./CampaignSettingsPanel";
import { CastPanel } from "./CastPanel";
import { KnowledgePanel } from "./KnowledgePanel";
import { MapsPanel } from "./MapsPanel";
import { OverviewPanel } from "./OverviewPanel";
import { PlanningPanel } from "./PlanningPanel";
import { stopExchangesOutside } from "./planningStream";
import { PlayersPanel } from "./PlayersPanel";
import { ProposalsPanel } from "./ProposalsPanel";
import { SharingPanel } from "./SharingPanel";
import { DEFAULT_CAMPAIGN_VIEW, type CampaignView } from "./views";

import "./campaign.css";
import "./campaignHost.css";

// The views that read line by line (a queue of sentences, a form) sit on the
// narrow page; the ones with a list and a detail pane take the wide one.
const NARROW_VIEWS: ReadonlySet<CampaignView> = new Set(["proposals", "settings"]);

// The Campaign screen is a thin HOST (ADR-0063 amendment). It does four things
// and nothing else:
//
//   1. resolves WHICH campaign is active, and remounts everything under it when
//      that changes (see Campaign below);
//   2. renders the page and its header — the campaign's name, its System, the
//      view's lede — on the shared Page/PageHeader, so every view sits in the
//      same column under the same header (the Overview brings its own header,
//      which also carries the campaign's language and its actions);
//   3. switches on the view the route path names, one panel file per view;
//   4. hands the panels what they share through useCampaignScreen(): the
//      campaign, its roster and the cross-view jumps.
//
// Everything a view does lives in its own panel file (OverviewPanel, CastPanel,
// KnowledgePanel, MapsPanel, PlayersPanel, ProposalsPanel, PlanningPanel,
// SharingPanel, CampaignSettingsPanel). The host takes no callbacks from the route: it reaches
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
  // A Campaign Guest has no roster to read (the cast is the GM's) and no
  // Active Campaign to switch: their Workspace IS one Campaign, and the
  // WorkspaceGate already remounts everything when it changes.
  const { manage } = useCan();
  const { data } = useQuery(
    CampaignService.method.getCampaignRoster,
    {},
    { retry: rosterRetry, enabled: manage },
  );
  const epoch = useCampaignEpoch(data?.campaign?.id);

  // A planning-chat reply keeps streaming while the GM looks at other views
  // (planningStream.ts) — but not into a campaign they have switched away from.
  const activeCampaignId = data?.campaign?.id;
  useEffect(() => {
    if (activeCampaignId) stopExchangesOutside(activeCampaignId);
  }, [activeCampaignId]);

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
  const can = useCan();
  const guest = useCurrentUser()?.guest;
  const rosterQ = useQuery(
    CampaignService.method.getCampaignRoster,
    {},
    { retry: rosterRetry, enabled: can.manage },
  );
  const { data, status, error } = rosterQ;
  // For a Campaign Guest the campaign is the one their Workspace names — the
  // session probe already said which — and its cast is not theirs to see.
  const guestCampaign = useMemo(
    () => (guest ? create(CampaignSchema, { id: guest.campaignId, name: guest.campaignName }) : undefined),
    [guest],
  );
  const campaign = guestCampaign ?? data?.campaign;
  const roster = useMemo(() => data?.roster ?? [], [data]);

  // Cross-view jumps are navigations, not screen state: the target view lives
  // in the path and the thing to open rides as a search param the target panel
  // consumes (?node= on the wiki, ?agent= on Cast). Nothing is copied into
  // local state on the way, so Back returns to where the jump came from.
  const { goCampaignView, goScreen } = nav;
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

  // The wiki's open entry IS the route's ?node: the panel reads and writes it
  // through useScreenNav() itself, so the host hands it nothing and Back steps
  // through the entries the GM opened.
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
      <Page className="gx-campaign-screen" data-view={view}>
        <PageHeader title={t("campaign.fallbackTitle")} />
        <Card accent>
          <FirstCampaignCTA />
        </Card>
      </Page>
    );
  }

  return (
    <CampaignScreenProvider value={screen}>
      {/* The shared Page: one centred column and one gutter for every view.
          gx-campaign-screen and data-view stay on the root: a view's stylesheet
          targets the screen through them (a view that needs the full height,
          say) without the host knowing about it. */}
      <Page
        className="gx-campaign-screen"
        width={NARROW_VIEWS.has(view) ? "narrow" : "wide"}
        data-view={view}
      >
        {/* The view tabs that used to sit beside the title live in the sidebar
            (ADR-0063); the header keeps the campaign's identity and says what
            the view is for. The Overview renders its own. */}
        {view !== "overview" && (
          <PageHeader
            eyebrow={campaign?.system || undefined}
            title={campaign?.name ?? t("campaign.fallbackTitle")}
            description={t(campaignLede(view, can.guest))}
          />
        )}

        {view === "overview" ? (
          <OverviewPanel />
        ) : view === "cast" ? (
          <CastPanel />
        ) : view === "knowledge" ? (
          <KnowledgePanel
            onOpenCast={screen.openAgent}
            onOpenLine={openTranscriptLine}
          />
        ) : view === "maps" ? (
          <MapsPanel onOpenNode={screen.openNode} />
        ) : view === "players" ? (
          <PlayersPanel />
        ) : view === "proposals" ? (
          <ProposalsPanel />
        ) : view === "sharing" ? (
          <SharingPanel />
        ) : view === "planning" && guestCampaign ? (
          // A guest's campaign id comes with their Workspace: nothing to wait for.
          <PlanningPanel key={guestCampaign.id} campaignId={guestCampaign.id} />
        ) : view === "planning" ? (
          // The ChatService RPCs are campaign-scoped (ADR-0062), so the panel
          // waits for the roster read to hand it the campaign id.
          // Until then: a placeholder, or — when the campaign cannot be read —
          // the reason and a way to try again.
          <QueryBoundary
            query={rosterQ}
            errorTitle={t("home.campaignLoadError")}
            skeleton={<Skeleton.Row count={4} avatar={false} />}
          >
            {(loaded) =>
              loaded.campaign ? (
                <PlanningPanel key={loaded.campaign.id} campaignId={loaded.campaign.id} />
              ) : null
            }
          </QueryBoundary>
        ) : (
          <CampaignSettingsPanel />
        )}
      </Page>
    </CampaignScreenProvider>
  );
}
