import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  CampaignService,
  GetActiveCampaignResponseSchema,
  CampaignSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { Configuration } from "./Configuration";

// Mount the Configuration screen against a MOCKED connect client (no network):
// createRouterTransport serves a canned GetActiveCampaign in-memory, so the test
// proves the screen renders LIVE RPC data (name / system / language) rather than
// the design mock's hardcoded values.
const CANNED = {
  id: "11111111-1111-1111-1111-111111111111",
  tenantId: "22222222-2222-2222-2222-222222222222",
  name: "The Sunless Citadel",
  system: "D&D 5e",
  language: "en",
};

function mockTransport() {
  return createRouterTransport(({ service }) => {
    service(CampaignService, {
      getActiveCampaign: () =>
        create(GetActiveCampaignResponseSchema, {
          campaign: create(CampaignSchema, CANNED),
        }),
    });
  });
}

describe("Configuration", () => {
  it("renders the active campaign from the RPC", async () => {
    render(
      <Providers transport={mockTransport()} queryClient={makeQueryClient()}>
        <Configuration />
      </Providers>,
    );

    // The campaign name / system / language come from the mocked RPC response.
    expect(await screen.findByText(CANNED.name)).toBeInTheDocument();
    expect(screen.getByText(CANNED.system)).toBeInTheDocument();
    expect(screen.getByText(CANNED.language)).toBeInTheDocument();
  });

  it("marks the not-yet-wired sections as coming soon", async () => {
    render(
      <Providers transport={mockTransport()} queryClient={makeQueryClient()}>
        <Configuration />
      </Providers>,
    );

    await screen.findByText(CANNED.name);
    // Provider keys + voice dropdown render as disabled placeholders.
    expect(screen.getAllByText("coming soon").length).toBeGreaterThanOrEqual(2);
  });
});
