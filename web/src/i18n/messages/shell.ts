// App-shell strings: sidebar navigation, topbar, and the screen skeleton the
// shell navigates to (the campaign Overview and Settings views, the Campaigns
// list, the in-shell not-found page). Kept separate from the screen fragments
// because the shell renders on every screen.

export const en = {
  // ── Sidebar ──────────────────────────────────────────────────────────────
  "shell.navLabel": "Main navigation",
  "shell.navCampaign": "Campaign",
  "shell.navOverview": "Overview",
  "shell.navCampaignSettings": "Settings",
  "shell.navSession": "Session",
  "shell.navCampaigns": "Campaigns",
  "shell.navSetup": "Setup",
  "shell.noCampaign": "No campaign yet",
  "shell.home": "Glyphoxa — campaign overview",
  "shell.toggleSidebar": "Toggle sidebar",
  // The count rides in the link's accessible name; the pill itself is decorative.
  "shell.suggestionsPending": "{label}, {count} pending",

  // ── Topbar ───────────────────────────────────────────────────────────────
  "shell.titleSetup": "Setup",
  "shell.titleSession": "Session",
  "shell.titleCampaigns": "Campaigns",
  "shell.breadcrumb": "You are here",
  "shell.live": "Live",
  "shell.openSession": "Open session",

  // ── Campaign views added with the shell ──────────────────────────────────
  "shell.ledeOverview": "Everything in this campaign, one step away.",
  "shell.ledeCampaignSettings": "Name, game system, language and highlight recording of this campaign.",
  "shell.overviewSections": "Sections of this campaign",
  "shell.campaignSettingsSaved": "Campaign settings saved",

  // ── Campaign switch feedback ─────────────────────────────────────────────
  "shell.nowWorkingIn": "Now working in {name}",
  "shell.switchDeferred": "A voice session is live — {name} becomes the active campaign when it ends.",

  // ── Campaigns list ───────────────────────────────────────────────────────
  "shell.campaignsLede": "Every campaign at your table. Open one to work in it.",
  "shell.campaignsList": "Your campaigns",
  "shell.campaignActive": "Active",
  "shell.campaignOpen": "Open",
  "shell.campaignOpenNamed": "Open {name}",

  // ── In-shell not-found page ──────────────────────────────────────────────
  "shell.placeholderGoTo": "Go somewhere that exists",
} as const;

export const de: Record<keyof typeof en, string> = {
  "shell.navLabel": "Hauptnavigation",
  "shell.navCampaign": "Kampagne",
  "shell.navOverview": "Übersicht",
  "shell.navCampaignSettings": "Einstellungen",
  "shell.navSession": "Spielrunde",
  "shell.navCampaigns": "Kampagnen",
  "shell.navSetup": "Einrichtung",
  "shell.noCampaign": "Noch keine Kampagne",
  "shell.home": "Glyphoxa — Kampagnenübersicht",
  "shell.toggleSidebar": "Seitenleiste umschalten",
  "shell.suggestionsPending": "{label}, {count} offen",

  "shell.titleSetup": "Einrichtung",
  "shell.titleSession": "Spielrunde",
  "shell.titleCampaigns": "Kampagnen",
  "shell.breadcrumb": "Du bist hier",
  "shell.live": "Live",
  "shell.openSession": "Spielrunde öffnen",

  "shell.ledeOverview": "Alles in dieser Kampagne, einen Schritt entfernt.",
  "shell.ledeCampaignSettings": "Name, Spielsystem, Sprache und Highlight-Aufnahme dieser Kampagne.",
  "shell.overviewSections": "Bereiche dieser Kampagne",
  "shell.campaignSettingsSaved": "Kampagnen-Einstellungen gespeichert",

  "shell.nowWorkingIn": "Du arbeitest jetzt in {name}",
  "shell.switchDeferred": "Gerade läuft eine Session — {name} wird zur aktiven Kampagne, sobald sie endet.",

  "shell.campaignsLede": "Alle Kampagnen an deinem Tisch. Öffne eine, um darin zu arbeiten.",
  "shell.campaignsList": "Deine Kampagnen",
  "shell.campaignActive": "Aktiv",
  "shell.campaignOpen": "Öffnen",
  "shell.campaignOpenNamed": "{name} öffnen",

  "shell.placeholderGoTo": "Geh an einen Ort, den es gibt",
};
