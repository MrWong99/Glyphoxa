import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  CampaignService,
  SessionService,
  GetSessionResponseSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
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
