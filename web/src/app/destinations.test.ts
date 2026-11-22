import { describe, it, expect } from "vitest";

import { en } from "@/i18n";
import { CAMPAIGN_VIEWS } from "@/screens/campaign/views";
import {
  CAMPAIGN_DESTINATIONS,
  SCREEN_DESTINATIONS,
  campaignDestination,
  screenDestination,
} from "./destinations";
import { SCREENS } from "./screenNav";

// The sidebar, the breadcrumb, the Overview tiles and the not-found page all
// read these lists. If a view or a screen is added to the vocabulary without a
// destination, it would be routable but unreachable — these pin the two in step.
describe("destinations", () => {
  it("has exactly one destination per campaign view, in the views' order", () => {
    expect(CAMPAIGN_DESTINATIONS.map((d) => d.view)).toEqual([...CAMPAIGN_VIEWS]);
    for (const view of CAMPAIGN_VIEWS) expect(campaignDestination(view).view).toBe(view);
  });

  it("has exactly one destination per screen other than the campaign itself", () => {
    expect(SCREEN_DESTINATIONS.map((d) => d.screen).sort()).toEqual(
      SCREENS.filter((s) => s !== "campaign").sort(),
    );
    expect(screenDestination("session")?.title).toBe("shell.titleSession");
    expect(screenDestination("campaign")).toBeUndefined();
    expect(screenDestination(undefined)).toBeUndefined();
  });

  it("gives every destination a label that reads as a name, and every view a lede", () => {
    const labels = [...CAMPAIGN_DESTINATIONS, ...SCREEN_DESTINATIONS].map((d) => en[d.label]);
    for (const label of labels) expect(label).toMatch(/\S/);
    // No two destinations share a name — "Settings" (the campaign's) and
    // "Setup" (the Tenant's) are distinct on purpose.
    expect(new Set(labels).size).toBe(labels.length);
    for (const d of CAMPAIGN_DESTINATIONS) expect(en[d.lede]).toMatch(/\.$/);
  });
});
