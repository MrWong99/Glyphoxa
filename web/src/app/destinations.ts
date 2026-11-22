import {
  BookOpen,
  LayoutDashboard,
  Library,
  Lightbulb,
  Map as MapIcon,
  MessagesSquare,
  ScrollText,
  Settings,
  SlidersHorizontal,
  Users,
  UsersRound,
  type LucideIcon,
} from "lucide-react";

import type { MessageKey } from "@/i18n";
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

export type CampaignDestination = {
  view: CampaignView;
  label: MessageKey;
  /** One sentence on what the view is for — the Campaign header's lede. */
  lede: MessageKey;
  icon: LucideIcon;
};

/** The Campaign views, in sidebar order. */
export const CAMPAIGN_DESTINATIONS: readonly CampaignDestination[] = [
  { view: "overview", label: "shell.navOverview", lede: "shell.ledeOverview", icon: LayoutDashboard },
  { view: "cast", label: "campaign.tabCast", lede: "campaign.ledeCast", icon: Users },
  { view: "knowledge", label: "campaign.tabWiki", lede: "campaign.ledeWiki", icon: BookOpen },
  { view: "maps", label: "campaign.tabMaps", lede: "campaign.ledeMaps", icon: MapIcon },
  { view: "players", label: "campaign.tabPlayers", lede: "campaign.ledePlayers", icon: UsersRound },
  { view: "proposals", label: "campaign.tabSuggestions", lede: "campaign.ledeSuggestions", icon: Lightbulb },
  { view: "planning", label: "chat.tabPlanning", lede: "chat.ledePlanning", icon: MessagesSquare },
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
};

/** The screens beside the campaign's own views, in sidebar order. */
export const SCREEN_DESTINATIONS: readonly ScreenDestination[] = [
  { screen: "session", label: "shell.navSession", title: "shell.titleSession", icon: ScrollText },
  { screen: "campaigns", label: "shell.navCampaigns", title: "shell.titleCampaigns", icon: Library },
  { screen: "configuration", label: "shell.navSetup", title: "shell.titleSetup", icon: Settings },
];

export function screenDestination(screen: string | undefined): ScreenDestination | undefined {
  return SCREEN_DESTINATIONS.find((d) => d.screen === screen);
}
