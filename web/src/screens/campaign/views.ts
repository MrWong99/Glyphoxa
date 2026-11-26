// The Campaign screen's sub-views, shared by the router (path segment), the
// AppShell (sidebar navigation) and the screen itself. The view lives in the
// PATH (/t/:tenantSlug/campaign/:view) rather than screen state so the sidebar
// can link to and highlight it, and a bookmarked URL restores the view
// (ADR-0063 — the in-page tab bar moved into the sidebar).
//
// Order is the sidebar's order. `overview` is where a campaign opens; `settings`
// edits the campaign record itself (name, System, Campaign Language, highlight
// recording) — not to be confused with the Tenant-wide Setup screen. `sharing`
// is who else may open the campaign: its Campaign Guests and invitation links
// (ADR-0065).

export const CAMPAIGN_VIEWS = [
  "overview",
  "cast",
  "knowledge",
  "maps",
  "players",
  "proposals",
  "planning",
  "sharing",
  "settings",
] as const;

export type CampaignView = (typeof CAMPAIGN_VIEWS)[number];

/** Where a bare /campaign visit, an unknown :view and the tenant index land. */
export const DEFAULT_CAMPAIGN_VIEW: CampaignView = "overview";

export function isCampaignView(v: unknown): v is CampaignView {
  return typeof v === "string" && (CAMPAIGN_VIEWS as readonly string[]).includes(v);
}
