import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  BoardSchema,
  CampaignService,
  ListBoardsResponseSchema,
  ListNodesResponseSchema,
  NodeSchema,
  NodeType,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { PrepBoards } from "./PrepBoards";

function mount(onOpenNode?: (id: string) => void) {
  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      listBoards: () =>
        create(ListBoardsResponseSchema, {
          boards: [create(BoardSchema, { id: "b1", name: "Tonight", nodeIds: ["n1"] })],
        }),
      listNodes: () =>
        create(ListNodesResponseSchema, {
          nodes: [create(NodeSchema, { id: "n1", nodeType: NodeType.LOCATION, name: "Oakhurst" })],
        }),
    });
  });
  render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <PrepBoards onOpenNode={onOpenNode} />
    </Providers>,
  );
}

// W15: on the Session screen the boards are a reference list (no onOpenNode),
// but every entry still rendered as a button that did nothing when pressed.
describe("PrepBoards entries", () => {
  it("are plain text where there is nothing to open", async () => {
    mount();
    expect(await screen.findByText("Oakhurst")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Oakhurst" })).not.toBeInTheDocument();
  });

  it("are buttons that open the entry where a handler is wired", async () => {
    const onOpenNode = vi.fn();
    mount(onOpenNode);
    fireEvent.click(await screen.findByRole("button", { name: "Oakhurst" }));
    expect(onOpenNode).toHaveBeenCalledWith("n1");
  });
});
