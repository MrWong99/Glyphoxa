// App-shell strings: sidebar navigation, topbar. Kept separate from the
// screen fragments because the shell renders on every screen.

export const en = {
  "shell.navSetup": "Setup",
  "shell.navCampaign": "Campaign",
  "shell.navSession": "Session",
  "shell.titleSetup": "Setup",
  "shell.titleCampaign": "Campaign",
  "shell.titleSession": "Session",
  "shell.toggleSidebar": "Toggle sidebar",
} as const;

export const de: Record<keyof typeof en, string> = {
  "shell.navSetup": "Einrichtung",
  "shell.navCampaign": "Kampagne",
  "shell.navSession": "Spielrunde",
  "shell.titleSetup": "Einrichtung",
  "shell.titleCampaign": "Kampagne",
  "shell.titleSession": "Spielrunde",
  "shell.toggleSidebar": "Seitenleiste umschalten",
};
