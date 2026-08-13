import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { createRouterTransport, ConnectError, Code } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  CampaignService,
  NodeSchema,
  NodeType,
  GenerateNodePortraitResponseSchema,
  SetNodePortraitResponseSchema,
  ClearNodePortraitResponseSchema,
  type SetNodePortraitRequest,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { NodePortrait } from "./NodePortrait";

// Node portraits (#590). The load-bearing behaviours: a generated draft SAVES
// NOTHING until "Use portrait", the applied bytes are exactly the draft's, an
// upload goes through the same door, and a GM-only entry is offered upload only.

const DRAFT_BYTES = new Uint8Array([1, 2, 3, 4]);

function bart(overrides: { hasPortrait?: boolean; gmPrivate?: boolean } = {}) {
  return create(NodeSchema, {
    id: "n1",
    campaignId: "c1",
    nodeType: NodeType.NPC,
    name: "Bart",
    body: "The innkeeper.",
    ...overrides,
  });
}

function mockTransport(opts: { failGenerate?: boolean } = {}) {
  const setCalls: SetNodePortraitRequest[] = [];
  let generateCalls = 0;
  let clearCalls = 0;

  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      generateNodePortrait: () => {
        generateCalls++;
        if (opts.failGenerate) {
          throw new ConnectError(
            "no image provider key is configured — save a Gemini key in Configuration",
            Code.FailedPrecondition,
          );
        }
        return create(GenerateNodePortraitResponseSchema, {
          imageBytes: DRAFT_BYTES,
          contentType: "image/png",
          model: "m",
          prompt: "the full prompt",
        });
      },
      setNodePortrait: (req) => {
        setCalls.push(req);
        return create(SetNodePortraitResponseSchema, {
          node: bart({ hasPortrait: true }),
        });
      },
      clearNodePortrait: () => {
        clearCalls++;
        return create(ClearNodePortraitResponseSchema, { node: bart() });
      },
    });
  });
  return {
    transport,
    setCalls,
    generateCalls: () => generateCalls,
    clearCalls: () => clearCalls,
  };
}

function renderPortrait(node = bart(), t = mockTransport()) {
  render(
    <Providers transport={t.transport} queryClient={makeQueryClient()}>
      <NodePortrait node={node} />
    </Providers>,
  );
  return t;
}

beforeEach(() => {
  // jsdom has no object-URL machinery; the draft preview only needs a stable
  // fake src (the MapsPanel suite's arrangement).
  vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:draft");
  vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
});

describe("NodePortrait", () => {
  it("previews a generated draft without saving, then applies exactly those bytes", async () => {
    const t = renderPortrait();

    fireEvent.click(screen.getByRole("button", { name: "Generate portrait" }));
    const preview = await screen.findByAltText("Generated portrait draft");
    expect(preview).toHaveAttribute("src", "blob:draft");
    // The draft exists only in the browser: nothing reached SetNodePortrait.
    expect(t.setCalls).toHaveLength(0);

    fireEvent.click(screen.getByRole("button", { name: "Use portrait" }));
    await waitFor(() => expect(t.setCalls).toHaveLength(1));
    expect(t.setCalls[0].nodeId).toBe("n1");
    expect(Array.from(t.setCalls[0].imageBytes)).toEqual(Array.from(DRAFT_BYTES));
    expect(t.setCalls[0].contentType).toBe("image/png");
    // The preview is gone once applied.
    await waitFor(() =>
      expect(screen.queryByAltText("Generated portrait draft")).not.toBeInTheDocument(),
    );
  });

  it("discards a draft without ever writing", async () => {
    const t = renderPortrait();

    fireEvent.click(screen.getByRole("button", { name: "Generate portrait" }));
    await screen.findByAltText("Generated portrait draft");
    fireEvent.click(screen.getByRole("button", { name: "Discard" }));

    expect(screen.queryByAltText("Generated portrait draft")).not.toBeInTheDocument();
    expect(t.setCalls).toHaveLength(0);
    expect(t.generateCalls()).toBe(1);
  });

  it("surfaces an actionable generate refusal", async () => {
    renderPortrait(bart(), mockTransport({ failGenerate: true }));

    fireEvent.click(screen.getByRole("button", { name: "Generate portrait" }));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("Configuration");
  });

  it("uploads a file through the same apply door", async () => {
    const t = renderPortrait();

    const file = new File([DRAFT_BYTES], "bart.png", { type: "image/png" });
    // jsdom's File lacks arrayBuffer().
    Object.defineProperty(file, "arrayBuffer", {
      value: async () => DRAFT_BYTES.buffer,
    });
    fireEvent.change(screen.getByLabelText("Upload a portrait image"), {
      target: { files: [file] },
    });

    await waitFor(() => expect(t.setCalls).toHaveLength(1));
    expect(Array.from(t.setCalls[0].imageBytes)).toEqual(Array.from(DRAFT_BYTES));
    expect(t.setCalls[0].contentType).toBe("image/png");
  });

  it("shows the saved portrait with a remove affordance and clears it", async () => {
    const t = renderPortrait(bart({ hasPortrait: true }));

    const img = screen.getByAltText("Portrait of Bart");
    expect(img.getAttribute("src")).toContain("/api/v1/knowledge/nodes/n1/portrait");

    fireEvent.click(screen.getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(t.clearCalls()).toBe(1));
  });

  it("offers a GM-only entry upload but not generation", () => {
    renderPortrait(bart({ gmPrivate: true }));

    expect(screen.queryByRole("button", { name: "Generate portrait" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Upload" })).toBeInTheDocument();
    expect(
      screen.getByText("GM-only entries can't seed a generated portrait — upload a picture instead."),
    ).toBeInTheDocument();
  });
});
