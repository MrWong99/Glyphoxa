import { describe, it, expect } from "vitest";

import { GuestRight as R } from "@gen/glyphoxa/management/v1/management_pb";
import { GM_CAPABILITIES, capabilitiesFor } from "./capabilities";
import {
  CAMPAIGN_DESTINATIONS,
  SCREEN_DESTINATIONS,
  campaignDestinationsFor,
  landingFor,
  screenDestination,
  screenDestinationsFor,
} from "./destinations";
import { OWN_WORKSPACE_SLUG, guestWorkspaceSlug, slugGuestCampaign, workspaceSlug } from "./workspace";

// What a Campaign Guest is OFFERED (ADR-0065). The sidebar, the palette, the
// route guard and the not-found page all read these two filters, so pinning
// them per Guest Right pins every one of those surfaces at once.

const viewer = (...rights: R[]) => ({ guest: { rights } });
const views = (...rights: R[]) => campaignDestinationsFor(viewer(...rights)).map((d) => d.view);
const screens = (...rights: R[]) => screenDestinationsFor(viewer(...rights)).map((d) => d.screen);

describe("capabilities", () => {
  it("lets the GM do everything — and anyone outside the AuthGate is treated as the GM", () => {
    expect(capabilitiesFor(undefined)).toBe(GM_CAPABILITIES);
    expect(Object.entries(GM_CAPABILITIES).filter(([k]) => k !== "guest").every(([, v]) => v)).toBe(true);
    expect(GM_CAPABILITIES.guest).toBe(false);
  });

  it("gives a guest exactly their rights, and never what no right covers", () => {
    expect(capabilitiesFor({ rights: [R.WIKI_READ, R.MAPS_READ, R.MAPS_EDIT] })).toEqual({
      guest: true,
      manage: false,
      wikiRead: true,
      wikiSecrets: false,
      wikiEdit: false,
      mapsRead: true,
      mapsEdit: true,
      transcriptsRead: false,
      highlightsRead: false,
      planningUse: false,
    });
    // Even a guest with every right is not the GM.
    const everything = capabilitiesFor({
      rights: Object.values(R).filter((v): v is R => typeof v === "number"),
    });
    expect(everything.manage).toBe(false);
    expect(everything.guest).toBe(true);
  });
});

describe("a guest's destinations, right by right", () => {
  it.each([
    ["wiki.read", [R.WIKI_READ], ["knowledge"], []],
    ["wiki.secrets alone (nothing to open)", [R.WIKI_SECRETS], [], []],
    ["wiki.edit (normalized)", [R.WIKI_READ, R.WIKI_SECRETS, R.WIKI_EDIT], ["knowledge"], []],
    ["maps.read", [R.MAPS_READ], ["maps"], []],
    ["maps.edit (normalized)", [R.MAPS_READ, R.MAPS_EDIT], ["maps"], []],
    ["transcripts.read", [R.TRANSCRIPTS_READ], [], ["session"]],
    ["highlights.read", [R.HIGHLIGHTS_READ], [], ["session"]],
    ["planning.use (normalized)", [R.WIKI_READ, R.WIKI_SECRETS, R.PLANNING_USE], ["knowledge", "planning"], []],
  ] as const)("%s", (_name, rights, wantViews, wantScreens) => {
    expect(views(...rights)).toEqual(wantViews);
    expect(screens(...rights)).toEqual(wantScreens);
  });

  it("offers nothing to a guest with no rights", () => {
    expect(views()).toEqual([]);
    expect(screens()).toEqual([]);
    expect(landingFor(viewer())).toBeNull();
  });

  it("never offers a guest the GM's views or screens, whatever they hold", () => {
    const all = Object.values(R).filter((v): v is R => typeof v === "number");
    expect(views(...all)).toEqual(["knowledge", "maps", "planning"]);
    expect(screens(...all)).toEqual(["session"]);
    // …not even the Admin screen to a Platform Admin who is acting as a guest.
    expect(
      screenDestinationsFor({ isPlatformAdmin: true, guest: { rights: all } }).map((d) => d.screen),
    ).toEqual(["session"]);
    expect(screenDestination("configuration", viewer(...all))).toBeUndefined();
    expect(screenDestination("campaigns", viewer(...all))).toBeUndefined();
  });

  it("calls a guest's Session screen what it is: past sessions", () => {
    expect(screenDestination("session", viewer(R.TRANSCRIPTS_READ))?.label).toBe("access.navSessions");
    expect(screenDestination("session", undefined)?.label).toBe("shell.navSession");
  });

  it("leaves the GM every destination", () => {
    expect(campaignDestinationsFor(undefined)).toBe(CAMPAIGN_DESTINATIONS);
    expect(campaignDestinationsFor({ isPlatformAdmin: false })).toBe(CAMPAIGN_DESTINATIONS);
    expect(screenDestinationsFor({ isPlatformAdmin: true })).toEqual(SCREEN_DESTINATIONS);
  });

  it("lands a guest on the first thing shared with them", () => {
    expect(landingFor(viewer(R.MAPS_READ, R.WIKI_READ))).toEqual({ screen: "campaign", view: "knowledge" });
    expect(landingFor(viewer(R.MAPS_READ))).toEqual({ screen: "campaign", view: "maps" });
    expect(landingFor(viewer(R.HIGHLIGHTS_READ))).toEqual({ screen: "session" });
    expect(landingFor(undefined)).toEqual({ screen: "campaign", view: "overview" });
  });
});

describe("workspace slugs", () => {
  it("names the own Workspace `default` and a guest Campaign by its id", () => {
    expect(workspaceSlug({})).toBe(OWN_WORKSPACE_SLUG);
    expect(workspaceSlug(undefined)).toBe("default");
    expect(workspaceSlug({ guest: { campaignId: "c9" } })).toBe("guest-c9");
  });

  it("reads every slug that is not a guest slug as the own Workspace", () => {
    expect(slugGuestCampaign(guestWorkspaceSlug("c9"))).toBe("c9");
    expect(slugGuestCampaign("default")).toBeNull();
    // Old bookmarks carry whatever slug the Tenant had.
    expect(slugGuestCampaign("acme")).toBeNull();
    expect(slugGuestCampaign("guest-")).toBeNull();
  });
});
