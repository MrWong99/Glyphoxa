import { describe, it, expect } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  CampaignService,
  NodeSchema,
  NodeType,
  CreateNodeResponseSchema,
  ListNodesResponseSchema,
  type CreateNodeRequest,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { KnowledgePanel } from "./KnowledgePanel";

// An in-memory KG store served over a router transport (no network): createNode
// pushes into the closure and listNodes reads it back, so an Add → invalidate →
// refetch proves the entry round-trips, and the recorded request proves the
// gm_private switch + fixed NOTE type reach the wire.
function mockTransport() {
  const nodes = [
    create(NodeSchema, {
      id: "n1",
      campaignId: "c1",
      nodeType: NodeType.NOTE,
      name: "The sealed vault",
      body: "Nobody has opened it in a century.",
      gmPrivate: false,
    }),
  ];
  let nextId = 2;
  const createCalls: CreateNodeRequest[] = [];

  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      listNodes: () => create(ListNodesResponseSchema, { nodes }),
      createNode: (req) => {
        createCalls.push(req);
        const node = create(NodeSchema, {
          id: `n${nextId++}`,
          campaignId: "c1",
          nodeType: req.nodeType,
          name: req.name,
          body: req.body,
          gmPrivate: req.gmPrivate,
        });
        nodes.push(node);
        return create(CreateNodeResponseSchema, { node });
      },
    });
  });
  return { transport, nodes, createCalls };
}

function renderPanel() {
  const { transport, nodes, createCalls } = mockTransport();
  render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <KnowledgePanel />
    </Providers>,
  );
  return { nodes, createCalls };
}

describe("KnowledgePanel", () => {
  it("lists the campaign's knowledge entries from ListNodes", async () => {
    renderPanel();
    expect(await screen.findByText("The sealed vault")).toBeInTheDocument();
    expect(screen.getByText(/Nobody has opened it/)).toBeInTheDocument();
  });

  it("adds a gm-private entry that appears in the list, sending NOTE + gm_private", async () => {
    const { nodes, createCalls } = renderPanel();
    await screen.findByText("The sealed vault");

    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Hidden cult" } });
    fireEvent.change(screen.getByLabelText(/content/i), { target: { value: "They meet at midnight." } });
    // Toggle the GM-private switch on.
    fireEvent.click(screen.getByLabelText(/gm private/i));
    fireEvent.click(screen.getByRole("button", { name: /add entry/i }));

    // The new entry round-trips into the list (invalidate → refetch).
    expect(await screen.findByText("Hidden cult")).toBeInTheDocument();
    expect(nodes).toHaveLength(2);

    // The request carried the fixed NOTE type and the gm_private flag.
    expect(createCalls).toHaveLength(1);
    expect(createCalls[0].nodeType).toBe(NodeType.NOTE);
    expect(createCalls[0].name).toBe("Hidden cult");
    expect(createCalls[0].gmPrivate).toBe(true);
  });

  it("marks a gm-private entry with a private badge", async () => {
    const { transport } = (() => {
      const nodes = [
        create(NodeSchema, {
          id: "n1",
          campaignId: "c1",
          nodeType: NodeType.NOTE,
          name: "Secret pact",
          body: "signed in blood",
          gmPrivate: true,
        }),
      ];
      const t = createRouterTransport(({ service }) => {
        service(CampaignService, {
          listNodes: () => create(ListNodesResponseSchema, { nodes }),
        });
      });
      return { transport: t };
    })();
    render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <KnowledgePanel />
      </Providers>,
    );
    expect(await screen.findByText("Secret pact")).toBeInTheDocument();
    // The private badge (exact) — distinct from the create form's switch label.
    expect(screen.getByText("GM private")).toBeInTheDocument();
  });
});
