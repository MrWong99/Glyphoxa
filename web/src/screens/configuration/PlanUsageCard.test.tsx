import { describe, it, expect } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";

import { ProviderService, GetUsageSummaryResponseSchema } from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { PlanUsageCard } from "./PlanUsageCard";

const RESETS = timestampFromDate(new Date("2026-12-01T00:00:00Z"));

function mount(summary: MessageInitShape<typeof GetUsageSummaryResponseSchema> | (() => never)) {
  const transport = createRouterTransport(({ service }) => {
    service(ProviderService, {
      getUsageSummary: () =>
        typeof summary === "function" ? summary() : create(GetUsageSummaryResponseSchema, summary),
    });
  });
  render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <PlanUsageCard />
    </Providers>,
  );
}

describe("PlanUsageCard", () => {
  it("shows used and included amounts with the reset date", async () => {
    mount({ planName: "Beta trial", usesPlatformKeys: true, includedUsd: 5, monthUsd: 1.25, resetsAt: RESETS });
    expect(await screen.findByRole("heading", { name: "Beta trial" })).toBeInTheDocument();
    expect(screen.getByText(/about \$1\.25 of \$5\.00 included usage used this month/i)).toBeInTheDocument();
    expect(screen.getByText(/resets December 1/)).toBeInTheDocument();
    const meter = screen.getByRole("meter", { name: "Included usage this month" });
    expect(meter).toHaveAttribute("aria-valuenow", "1.25");
    expect(meter).toHaveAttribute("aria-valuemax", "5");
    expect(meter).toHaveAttribute("data-tone", "ok");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("turns amber above 80 % and says when it starts over", async () => {
    mount({ planName: "Beta trial", includedUsd: 5, monthUsd: 4.2, resetsAt: RESETS });
    expect(await screen.findByRole("meter")).toHaveAttribute("data-tone", "warning");
    expect(screen.getByText(/most of this month's included usage is used/i)).toBeInTheDocument();
  });

  it("names the two ways on once the allowance is used up", async () => {
    mount({ planName: "Beta trial", includedUsd: 5, monthUsd: 5.02, exhausted: true, resetsAt: RESETS });
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/included usage is used up/i);
    // A saved key does not lift the gate (ADR-0055): the screen says so.
    expect(alert).toHaveTextContent(/saving your own key does not lift this/i);
    expect(alert).toHaveTextContent(/wait until December 1/i);
    expect(screen.getByRole("link", { name: "ask the operator" })).toHaveAttribute("href", "/imprint");
    expect(screen.getByRole("meter")).toHaveAttribute("data-tone", "danger");
  });

  it("says a table on its own keys is billed by the providers", async () => {
    mount({ planName: "Bring your own keys", usesPlatformKeys: false, resetsAt: RESETS });
    expect(await screen.findByText(/on your own keys — usage is billed to you by the providers/i)).toBeInTheDocument();
    expect(screen.queryByRole("meter")).not.toBeInTheDocument();
  });

  it("says included services carry no allowance on a self-hosted table", async () => {
    mount({ usesPlatformKeys: true, resetsAt: RESETS });
    expect(await screen.findByRole("heading", { name: "No plan" })).toBeInTheDocument();
    expect(screen.getByText(/no monthly allowance applies/i)).toBeInTheDocument();
  });

  it("shows an error with Retry when the plan cannot be read", async () => {
    mount(() => {
      throw new ConnectError("database unavailable", Code.Unavailable);
    });
    expect(await screen.findByText("Couldn't load your plan")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(screen.getByText("Couldn't load your plan")).toBeInTheDocument();
  });
});
