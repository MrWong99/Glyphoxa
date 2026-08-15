// The Campaign screen's sub-views, shared by the router (path segment), the
// AppShell (sidebar sub-navigation) and the screen itself. The view lives in
// the PATH (/t/:tenantSlug/campaign/:view) rather than screen state so the
// sidebar can link to and highlight it, and a bookmarked URL restores the view
// (ADR-0063 — the in-page tab bar moved into the sidebar).

export const CAMPAIGN_VIEWS = [
  "cast",
  "knowledge",
  "maps",
  "players",
  "proposals",
  "planning",
] as const;

export type CampaignView = (typeof CAMPAIGN_VIEWS)[number];

export function isCampaignView(v: unknown): v is CampaignView {
  return typeof v === "string" && (CAMPAIGN_VIEWS as readonly string[]).includes(v);
}
