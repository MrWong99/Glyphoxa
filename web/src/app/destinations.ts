import {
  BookOpen,
  LayoutDashboard,
  Library,
  Lightbulb,
  Map as MapIcon,
  MessagesSquare,
  ScrollText,
  Settings,
  Share2,
  ShieldCheck,
  SlidersHorizontal,
  Users,
  UsersRound,
  type LucideIcon,
} from "lucide-react";

import type { GuestWorkspace } from "@gen/glyphoxa/management/v1/management_pb";
import type { MessageKey } from "@/i18n";
import { capabilitiesFor, type Capabilities } from "./capabilities";
import type { CampaignView } from "@/screens/campaign/views";
import type { ScreenName } from "./screenNav";

// Every place the app can take a GM, named once (ADR-0063 amendment). The
// sidebar, the topbar breadcrumb, the browser-tab title, the campaign Overview
// tiles and the not-found page all read these lists, so a destination's label
// and icon cannot drift between them.
//
// Labels are catalog keys, not strings: the Campaign views keep the labels
// their in-page tabs had (campaign.tab* / chat.tabPlanning); the internal
// "Knowledge Graph" and "Knowledge Proposals" never face the GM — they read
// "World wiki" and "Suggestions".
//
// The lists are the whole vocabulary. What one viewer is OFFERED is narrower —
// the Admin screen exists for a Platform Admin only, and a Campaign Guest is
// offered exactly what their Guest Rights open (ADR-0065) — and that narrowing
// is campaignDestinationsFor() / screenDestinationsFor(), which the sidebar,
// the command palette, the route guard and the not-found page all share.

/** A capability a single Guest Right grants (never `manage`, the GM's alone). */
type GuestCapability = Exclude<keyof Capabilities, "guest" | "manage">;

export type CampaignDestination = {
  view: CampaignView;
  label: MessageKey;
  /** One sentence on what the view is for — the Campaign header's lede. */
  lede: MessageKey;
  icon: LucideIcon;
  /**
   * The capability that opens this view to a Campaign Guest. A view without
   * one is the GM's alone — the default, so a new view is never shown to
   * guests by accident.
   */
  guestNeeds?: GuestCapability;
};

/** The Campaign views, in sidebar order. */
export const CAMPAIGN_DESTINATIONS: readonly CampaignDestination[] = [
  { view: "overview", label: "shell.navOverview", lede: "shell.ledeOverview", icon: LayoutDashboard },
  { view: "cast", label: "campaign.tabCast", lede: "campaign.ledeCast", icon: Users },
  {
    view: "knowledge",
    label: "campaign.tabWiki",
    lede: "campaign.ledeWiki",
    icon: BookOpen,
    guestNeeds: "wikiRead",
  },
  { view: "maps", label: "campaign.tabMaps", lede: "campaign.ledeMaps", icon: MapIcon, guestNeeds: "mapsRead" },
  { view: "players", label: "campaign.tabPlayers", lede: "campaign.ledePlayers", icon: UsersRound },
  { view: "proposals", label: "campaign.tabSuggestions", lede: "campaign.ledeSuggestions", icon: Lightbulb },
  {
    view: "planning",
    label: "chat.tabPlanning",
    lede: "chat.ledePlanning",
    icon: MessagesSquare,
    guestNeeds: "planningUse",
  },
  { view: "sharing", label: "access.tabSharing", lede: "access.ledeSharing", icon: Share2 },
  { view: "settings", label: "shell.navCampaignSettings", lede: "shell.ledeCampaignSettings", icon: SlidersHorizontal },
];

export function campaignDestination(view: CampaignView): CampaignDestination {
  // CAMPAIGN_DESTINATIONS covers every CampaignView (pinned by destinations.test.ts).
  return CAMPAIGN_DESTINATIONS.find((d) => d.view === view) ?? CAMPAIGN_DESTINATIONS[0];
}

export type ScreenDestination = {
  screen: Exclude<ScreenName, "campaign">;
  label: MessageKey;
  /** The topbar / browser-tab title. */
  title: MessageKey;
  icon: LucideIcon;
  /** Exists only for a Platform Admin (ADR-0065); nobody else is shown it. */
  platformAdminOnly?: boolean;
  /**
   * Any one of these capabilities opens the screen to a Campaign Guest; a
   * screen without the list is the GM's alone.
   */
  guestNeedsAny?: readonly GuestCapability[];
  /** What a guest's version of the screen is called, when it differs. */
  guestLabel?: MessageKey;
};

/**
 * Every screen beside the campaign's own views, in sidebar order. This is the
 * whole vocabulary; what one viewer is SHOWN is screenDestinationsFor().
 */
export const SCREEN_DESTINATIONS: readonly ScreenDestination[] = [
  {
    screen: "session",
    label: "shell.navSession",
    title: "shell.titleSession",
    icon: ScrollText,
    // A guest gets the read-only archive of past sessions, not the live desk.
    guestNeedsAny: ["transcriptsRead", "highlightsRead"],
    guestLabel: "access.navSessions",
  },
  { screen: "campaigns", label: "shell.navCampaigns", title: "shell.titleCampaigns", icon: Library },
  { screen: "configuration", label: "shell.navSetup", title: "shell.titleSetup", icon: Settings },
  {
    screen: "admin",
    label: "shell.navAdmin",
    title: "shell.titleAdmin",
    icon: ShieldCheck,
    platformAdminOnly: true,
  },
];

/** What decides which destinations a viewer is offered. */
export type DestinationViewer = {
  isPlatformAdmin?: boolean;
  /** Set while the session acts as a Campaign Guest. */
  guest?: Pick<GuestWorkspace, "rights">;
};

/** The Campaign views this viewer is offered: all of them, or a guest's share. */
export function campaignDestinationsFor(viewer: DestinationViewer | undefined): readonly CampaignDestination[] {
  if (!viewer?.guest) return CAMPAIGN_DESTINATIONS;
  const can = capabilitiesFor(viewer.guest);
  return CAMPAIGN_DESTINATIONS.filter((d) => d.guestNeeds !== undefined && can[d.guestNeeds]);
}

/**
 * The screens this viewer is offered — the one filter the sidebar, the command
 * palette, the topbar title and the not-found page share, so a screen cannot
 * be hidden in one of them and named in another. While a session acts as a
 * Campaign Guest the shell is that Campaign's and nothing else: Campaigns,
 * Setup and — whoever holds it — Admin belong to the user's own Workspace,
 * one switch away.
 */
export function screenDestinationsFor(viewer: DestinationViewer | undefined): readonly ScreenDestination[] {
  if (viewer?.guest) {
    const can = capabilitiesFor(viewer.guest);
    return SCREEN_DESTINATIONS.filter((d) => d.guestNeedsAny?.some((need) => can[need])).map((d) =>
      d.guestLabel ? { ...d, label: d.guestLabel, title: d.guestLabel } : d,
    );
  }
  return SCREEN_DESTINATIONS.filter((d) => !d.platformAdminOnly || viewer?.isPlatformAdmin === true);
}

export function screenDestination(
  screen: string | undefined,
  viewer?: DestinationViewer,
): ScreenDestination | undefined {
  return screenDestinationsFor(viewer).find((d) => d.screen === screen);
}

/** A place the app can open on: a Campaign view, or a screen. */
export type Landing = { screen: "campaign"; view: CampaignView } | { screen: ScreenDestination["screen"] };

/**
 * Where this viewer lands when nothing more specific is asked for: the first
 * Campaign view they are offered, else the first screen — or null for a guest
 * nothing is shared with yet.
 */
export function landingFor(viewer: DestinationViewer | undefined): Landing | null {
  const view = campaignDestinationsFor(viewer)[0];
  if (view) return { screen: "campaign", view: view.view };
  const screen = screenDestinationsFor(viewer)[0];
  return screen ? { screen: screen.screen } : null;
}
