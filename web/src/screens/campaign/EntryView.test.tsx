import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent, within, waitFor } from "@testing-library/react";
import { createRouterTransport, ConnectError, Code } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  AgentSchema,
  CampaignService,
  EdgeSchema,
  EdgeType,
  ListNodeAppearancesResponseSchema,
  ListNodeEdgesResponseSchema,
  NodeAspectSchema,
  NodeSchema,
  NodeType,
} from "@gen/glyphoxa/management/v1/management_pb";
import type { Node } from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { CampaignScreenProvider } from "./campaignContext";
import { EntryView } from "./EntryView";

// The entry page (read side of the wiki): what the GM reads at the table.
// These pin what it shows and that every way out of it is a real control.

const BART = create(NodeSchema, {
  id: "bart",
  nodeType: NodeType.NPC,
  name: "Bart",
  body: "Runs **the Tankard**.",
  agentId: "npc-1",
  aspects: [
    create(NodeAspectSchema, { id: "a1", key: "Role", value: "Innkeeper" }),
    create(NodeAspectSchema, { id: "a2", key: "Secret", value: "Smuggles rum", gmPrivate: true }),
  ],
});

const EDGES = create(ListNodeEdgesResponseSchema, {
  outgoing: [
    create(EdgeSchema, {
      id: "e1",
      fromNodeId: "bart",
      toNodeId: "town",
      toNodeName: "Saltmarsh",
      toNodeType: NodeType.LOCATION,
      edgeType: EdgeType.RESIDES_IN,
      note: "since the flood",
    }),
  ],
  incoming: [
    create(EdgeSchema, {
      id: "e2",
      fromNodeId: "ilva",
      fromNodeName: "Ilva",
      fromNodeType: NodeType.CHARACTER,
      toNodeId: "bart",
      edgeType: EdgeType.KNOWS,
      disposition: 1,
    }),
  ],
});

function renderView(
  node: Node = BART,
  opts: { edgesFail?: boolean; mentionsFail?: boolean; tags?: string[] } = {},
) {
  const onEdit = vi.fn();
  const onClose = vi.fn();
  const onOpenNode = vi.fn();
  const openAgent = vi.fn();
  let edgeCalls = 0;
  let mentionCalls = 0;
  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      listNodeEdges: () => {
        edgeCalls++;
        if (opts.edgesFail && edgeCalls === 1) throw new ConnectError("edges down", Code.Unavailable);
        return EDGES;
      },
      listNodeAppearances: () => {
        mentionCalls++;
        if (opts.mentionsFail && mentionCalls === 1) throw new ConnectError("search down", Code.Unavailable);
        return create(ListNodeAppearancesResponseSchema, { appearances: [] });
      },
    });
  });
  const queryClient = makeQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <Providers transport={transport} queryClient={queryClient}>
      <CampaignScreenProvider
        value={{
          campaign: undefined,
          roster: [create(AgentSchema, { id: "npc-1", role: "character", name: "Bart the voice" })],
          openNode: () => {},
          openAgent,
          openView: () => {},
        }}
      >
        <EntryView
          node={node}
          tags={opts.tags ?? []}
          onEdit={onEdit}
          onClose={onClose}
          onOpenNode={onOpenNode}
        />
      </CampaignScreenProvider>
    </Providers>,
  );
  return { onEdit, onClose, onOpenNode, openAgent };
}

describe("EntryView", () => {
  it("reads the entry: name, type, the body as markdown and the facts as a definition list", async () => {
    renderView(BART, { tags: ["harbour", "act two"] });
    const page = screen.getByTestId("entry-view");
    expect(within(page).getByRole("heading", { level: 2, name: "Bart" })).toBeInTheDocument();
    expect(within(page).getByText("NPC")).toBeInTheDocument();
    // Markdown is rendered, not shown as asterisks.
    expect(within(page).getByText("the Tankard").tagName).toBe("STRONG");

    const terms = within(page).getAllByRole("term").map((el) => el.textContent);
    expect(terms).toEqual(["Role", "Secret"]);
    expect(within(page).getByText("Innkeeper")).toBeInTheDocument();
    expect(within(page).getByText("harbour")).toBeInTheDocument();
    expect(within(page).getByText("act two")).toBeInTheDocument();
  });

  it("marks the GM-only facts, in words and not only by styling", () => {
    renderView();
    const secret = screen.getByText("Smuggles rum").closest(".gx-wiki-dl__row")!;
    expect(secret).toHaveAttribute("data-private");
    expect(within(secret as HTMLElement).getByText("GM only")).toBeInTheDocument();
    const role = screen.getByText("Innkeeper").closest(".gx-wiki-dl__row")!;
    expect(role).not.toHaveAttribute("data-private");
    expect(screen.getByText("1 GM-only fact")).toBeInTheDocument();
  });

  it("lists connections as links in both directions, each opening that entry", async () => {
    const { onOpenNode } = renderView();
    expect(await screen.findByRole("heading", { name: "Connections · 2" })).toBeInTheDocument();

    const out = screen.getByRole("list", { name: "Outgoing connections" });
    expect(out).toHaveTextContent("lives in");
    expect(out).toHaveTextContent("since the flood");
    fireEvent.click(within(out).getByRole("button", { name: "Saltmarsh" }));
    expect(onOpenNode).toHaveBeenLastCalledWith("town");

    // Incoming reads as the sentence it asserts: "Ilva knows Bart".
    const inc = screen.getByRole("list", { name: "Incoming connections" });
    expect(inc).toHaveTextContent(/Ilva\s*knows\s*Bart/);
    expect(inc).toHaveTextContent("warm");
    fireEvent.click(within(inc).getByRole("button", { name: "Ilva" }));
    expect(onOpenNode).toHaveBeenLastCalledWith("ilva");
  });

  it("a failed connections read says so and retries", async () => {
    renderView(BART, { edgesFail: true });
    expect(await screen.findByText("Couldn't load the connections")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("list", { name: "Outgoing connections" })).toBeInTheDocument();
  });

  it("a failed mentions read shows a retry line instead of nothing", async () => {
    renderView(BART, { mentionsFail: true });
    expect(await screen.findByText("Couldn't load where this was mentioned.")).toBeInTheDocument();
    const retry = screen
      .getAllByRole("button", { name: "Try again" })
      .find((b) => b.closest(".gx-kg-appearances"))!;
    fireEvent.click(retry);
    expect(await screen.findByText(/Not mentioned yet/)).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.queryByText("Couldn't load where this was mentioned.")).not.toBeInTheDocument(),
    );
  });

  it("names the NPC that voices the entry, and that name opens it on Cast", () => {
    const { openAgent } = renderView();
    fireEvent.click(screen.getByRole("button", { name: "Voiced by Bart the voice" }));
    expect(openAgent).toHaveBeenCalledWith("npc-1");
  });

  it("Edit and Close are the ways out", () => {
    const { onEdit, onClose } = renderView();
    fireEvent.click(screen.getByRole("button", { name: "Edit" }));
    expect(onEdit).toHaveBeenCalledTimes(1);
    fireEvent.click(screen.getByRole("button", { name: "Close entry" }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("an entry with nothing written offers to write it, and a GM-only entry says so", () => {
    const { onEdit } = renderView(
      create(NodeSchema, { id: "x", nodeType: NodeType.NOTE, name: "Blank", gmPrivate: true }),
    );
    expect(screen.getByText("Nothing written yet")).toBeInTheDocument();
    expect(screen.getByText("This whole entry is GM-only. Your NPCs never see it.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Write it up" }));
    expect(onEdit).toHaveBeenCalledTimes(1);
  });
});
