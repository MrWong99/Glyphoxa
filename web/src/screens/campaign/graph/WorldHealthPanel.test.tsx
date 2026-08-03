import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { createRouterTransport, ConnectError, Code } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  AgentSchema,
  CampaignService,
  FindDuplicateEntriesResponseSchema,
  GetCampaignRosterResponseSchema,
  GraphNodeSchema,
  NodeType,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { WorldHealthPanel } from "./WorldHealthPanel";

function renderPanel(
  opts: {
    nodes?: ReturnType<typeof create<typeof GraphNodeSchema>>[];
    failRoster?: boolean;
    onOpenNode?: (id: string) => void;
    onOpenCast?: (id: string) => void;
  } = {},
) {
  let duplicateCalls = 0;
  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      getCampaignRoster: () => {
        if (opts.failRoster) throw new ConnectError("roster boom", Code.Internal);
        return create(GetCampaignRosterResponseSchema, {
          roster: [create(AgentSchema, { id: "a1", name: "Bart", role: "character" })],
        });
      },
      findDuplicateEntries: () => {
        duplicateCalls++;
        return create(FindDuplicateEntriesResponseSchema, {
          pairs: [{ aId: "n1", aName: "Bart", bId: "n2", bName: "Bart the innkeeper", similarity: 0.97 }],
          unembedded: 2,
        });
      },
    });
  });
  render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <WorldHealthPanel
        nodes={opts.nodes ?? []}
        edges={[]}
        onOpenNode={opts.onOpenNode ?? (() => {})}
        onOpenCast={opts.onOpenCast ?? (() => {})}
      />
    </Providers>,
  );
  return {
    get duplicateCalls() {
      return duplicateCalls;
    },
  };
}

describe("WorldHealthPanel", () => {
  // The AC is explicit that the embedding sweep must be GM-initiated. Wiring it as
  // a mutation is what guarantees that, and this is the test that fails if someone
  // refactors it to a query.
  it("does not run the duplicate scan on render", async () => {
    const ctx = renderPanel();
    await screen.findByRole("button", { name: "Check for duplicates" });
    expect(ctx.duplicateCalls).toBe(0);
  });

  it("runs the scan on click and shows the pairs with the unindexed caveat", async () => {
    const ctx = renderPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Check for duplicates" }));

    await waitFor(() => expect(ctx.duplicateCalls).toBe(1));
    expect(await screen.findByText(/97% alike/)).toBeInTheDocument();
    // "No duplicates" must never imply a clean bill of health the scan could not give.
    expect(screen.getByText(/2 entries are still being indexed/)).toBeInTheDocument();
  });

  it("opens the entry an entry-row points at", async () => {
    const onOpenNode = vi.fn();
    renderPanel({
      nodes: [create(GraphNodeSchema, { id: "n1", nodeType: NodeType.ITEM, name: "A lost ring" })],
      onOpenNode,
    });
    fireEvent.click(await screen.findByRole("button", { name: "A lost ring" }));
    expect(onOpenNode).toHaveBeenCalledWith("n1");
  });

  // A cast Agent with no entry is fixed on the ROSTER, not in the wiki — so the
  // row has to go there, and it has to be a button at all.
  it("opens the roster for a cast Agent with no entry", async () => {
    const onOpenCast = vi.fn();
    renderPanel({ onOpenCast });
    fireEvent.click(await screen.findByRole("button", { name: "Bart" }));
    expect(onOpenCast).toHaveBeenCalledWith("a1");
  });

  // The cast category depends on the roster read, so a clean bill of health while
  // that read is failing would be a lie the GM has no reason to doubt.
  it("does not claim a clean bill of health when the roster read failed", async () => {
    renderPanel({ failRoster: true });
    expect(await screen.findByText(/Could not check the cast/)).toBeInTheDocument();
    expect(screen.queryByText(/Nothing to flag/)).not.toBeInTheDocument();
  });
});
