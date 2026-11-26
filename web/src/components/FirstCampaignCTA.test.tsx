import { describe, it, expect } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { Toaster } from "sonner";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  CampaignService,
  SessionService,
  GetSessionResponseSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { MemoryScreenNav, type ScreenLocation } from "@/app/screenNav";
import { campaignBackend } from "@/screens/campaigns/testBackend";
import { FirstCampaignCTA } from "./FirstCampaignCTA";
import { makeQueryClient } from "@/lib/queryClient";
import { Campaign } from "@/screens/campaign/Campaign";
import { Session } from "@/screens/session/Session";

// zeroCampaignTransport models a fresh tenant with no campaign: every
// campaign-scoped read is CodeNotFound "no active campaign" (#267).
function zeroCampaignTransport() {
  const notFound = () => {
    throw new ConnectError("no active campaign", Code.NotFound);
  };
  return createRouterTransport(({ service }) => {
    service(CampaignService, {
      getActiveCampaign: notFound,
      getCampaignRoster: notFound,
    });
    service(SessionService, {
      getSession: () => create(GetSessionResponseSchema, { active: false }),
      listSessions: notFound,
    });
  });
}

describe("zero-campaign state (#267)", () => {
  it("shows the create-first-campaign card on the Campaign screen, not a raw error", async () => {
    render(
      <Providers transport={zeroCampaignTransport()} queryClient={makeQueryClient()}>
        <Campaign />
      </Providers>,
    );
    expect(await screen.findByText(/create your first campaign/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^create campaign$/i })).toBeInTheDocument();
    expect(screen.queryByText(/no active campaign/i)).not.toBeInTheDocument();
  });

  it("shows the create-first-campaign card on the Session screen, not a raw error", async () => {
    render(
      <Providers transport={zeroCampaignTransport()} queryClient={makeQueryClient()}>
        <Session />
      </Providers>,
    );
    expect(await screen.findByText(/create your first campaign/i)).toBeInTheDocument();
    expect(screen.queryByText(/no active campaign/i)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /start session/i })).not.toBeInTheDocument();
  });
});

describe("FirstCampaignCTA: how a first create ends", () => {
  it("makes the campaign active, says its Butler is ready and lands on its Overview", async () => {
    const h = campaignBackend({ campaigns: [] });
    const moves: ScreenLocation[] = [];
    render(
      <Providers transport={h.transport} queryClient={makeQueryClient()}>
        <MemoryScreenNav initial={{ screen: "configuration" }} onNavigate={(next) => moves.push(next)}>
          <FirstCampaignCTA />
        </MemoryScreenNav>
        <Toaster />
      </Providers>,
    );

    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Curse of Strahd" } });
    fireEvent.click(screen.getByRole("button", { name: /^create campaign$/i }));

    await waitFor(() => expect(h.callsOf("createCampaign")).toEqual([{ method: "createCampaign", name: "Curse of Strahd" }]));
    await waitFor(() => expect(h.callsOf("setActiveCampaign")).toEqual([{ method: "setActiveCampaign", id: "new-1" }]));
    expect(await screen.findByText("Created Curse of Strahd — your Butler is ready")).toBeInTheDocument();
    await waitFor(() => expect(moves).toEqual([{ screen: "campaign", view: "overview", search: {} }]));
  });
});
