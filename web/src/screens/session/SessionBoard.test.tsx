import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, within } from "@testing-library/react";
import { createRouterTransport, ConnectError, Code } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  CampaignService,
  BoardSchema,
  ListBoardsResponseSchema,
  ListNodesResponseSchema,
  NodeSchema,
  NodeAspectSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { MemoryScreenNav, type ScreenLocation } from "@/app/screenNav";
import { makeQueryClient } from "@/lib/queryClient";
import { SessionBoard } from "./SessionBoard";

// The Session screen's Board tab: tonight's prep board, read-only. The GM
// picks a board (remembered per campaign), peeks at an entry in place, and
// steps out to the wiki for the full page — no editing mid-scene.

const NODES = [
  create(NodeSchema, {
    id: "n1",
    name: "Captain Xendros",
    body: "Runs the **Sea Ghost**.",
    aspects: [create(NodeAspectSchema, { id: "a1", key: "Wants", value: "The smugglers gone" })],
  }),
  create(NodeSchema, { id: "n2", name: "The Haunted House", body: "", gmPrivate: true }),
];

function transport(opts: { boards?: ReturnType<typeof create<typeof BoardSchema>>[]; fail?: boolean }) {
  return createRouterTransport(({ service }) => {
    service(CampaignService, {
      listBoards: () => {
        if (opts.fail) throw new ConnectError("boom", Code.Internal);
        return create(ListBoardsResponseSchema, { boards: opts.boards ?? [] });
      },
      listNodes: () => create(ListNodesResponseSchema, { nodes: NODES }),
    });
  });
}

function renderBoard(
  opts: { boards?: ReturnType<typeof create<typeof BoardSchema>>[]; fail?: boolean },
  onNavigate?: (next: ScreenLocation) => void,
) {
  const queryClient = makeQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <Providers transport={transport(opts)} queryClient={queryClient}>
      <MemoryScreenNav initial={{ screen: "session", search: {} }} onNavigate={onNavigate}>
        <SessionBoard campaignId="c1" />
      </MemoryScreenNav>
    </Providers>,
  );
}

const TONIGHT = create(BoardSchema, { id: "b1", name: "Tonight", nodeIds: ["n1", "n2", "gone"] });
const LATER = create(BoardSchema, { id: "b2", name: "Act two", nodeIds: [] });

describe("SessionBoard", () => {
  beforeEach(() => localStorage.clear());

  it("lists the board's entries in order, read-only, with deleted ones said so", async () => {
    renderBoard({ boards: [TONIGHT] });

    expect(await screen.findByRole("heading", { name: "Tonight" })).toBeInTheDocument();
    expect(screen.getByText("3 entries")).toBeInTheDocument();
    expect(await screen.findByRole("button", { name: /Captain Xendros/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /The Haunted House/ })).toHaveTextContent("GM only");
    expect(await screen.findByText("Deleted entry")).toBeInTheDocument();
    // Nothing to type into: the board is a reference here.
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
  });

  it("peeks an entry in place, as Markdown, and opens it in the wiki", async () => {
    const onNavigate = vi.fn();
    renderBoard({ boards: [TONIGHT] }, onNavigate);

    const toggle = await screen.findByRole("button", { name: /Captain Xendros/ });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute("aria-expanded", "true");

    const peek = document.getElementById(toggle.getAttribute("aria-controls")!)!;
    expect(within(peek).getByText("Sea Ghost").tagName).toBe("STRONG");
    expect(within(peek).getByText("The smugglers gone")).toBeInTheDocument();

    fireEvent.click(within(peek).getByRole("button", { name: "Open in wiki" }));
    expect(onNavigate).toHaveBeenCalledWith(
      expect.objectContaining({ screen: "campaign", view: "knowledge", search: { node: "n1" } }),
      expect.anything(),
    );
  });

  it("says an entry has no text yet instead of an empty peek", async () => {
    renderBoard({ boards: [TONIGHT] });
    fireEvent.click(await screen.findByRole("button", { name: /The Haunted House/ }));
    expect(screen.getByText("This entry has no text yet.")).toBeInTheDocument();
  });

  it("remembers the picked board for the campaign", async () => {
    localStorage.setItem("gx-session-board:c1", "b2");
    renderBoard({ boards: [TONIGHT, LATER] });
    // The remembered board opens, and an empty one says where to fill it.
    expect(await screen.findByText("This board is empty")).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Board" })).toHaveTextContent("Act two");
  });

  it("with no boards, says what a board is and leads to the wiki", async () => {
    const onNavigate = vi.fn();
    renderBoard({ boards: [] }, onNavigate);
    expect(await screen.findByText("No boards yet")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Open the World wiki" }));
    expect(onNavigate).toHaveBeenCalledWith(
      expect.objectContaining({ screen: "campaign", view: "knowledge" }),
      expect.anything(),
    );
  });

  it("surfaces a load failure as an error, not as no boards", async () => {
    renderBoard({ fail: true });
    expect(await screen.findByText("Couldn't load the boards")).toBeInTheDocument();
    expect(screen.queryByText("No boards yet")).not.toBeInTheDocument();
  });
});
