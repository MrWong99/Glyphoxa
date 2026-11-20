import { describe, it, expect } from "vitest";
import { act, render, screen, fireEvent, waitFor } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  CampaignService,
  GetCampaignTagsResponseSchema,
  ListBoardsResponseSchema,
  ListNodeAppearancesResponseSchema,
  ListNodeEdgesResponseSchema,
  ListNodesResponseSchema,
  NodeSchema,
  NodeType,
  SetNodeTagsResponseSchema,
  UpdateNodeResponseSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { invalidateKnowledgeReads } from "./knowledgeCache";
import { KnowledgePanel } from "./KnowledgePanel";

// The wiki entry editor: it follows the live row (W7), does not wear another
// entry's mutation status (W13), rolls a rejected tag back (W12) and asks
// before throwing typed text away (W10).
function backend(opts: { failUpdateFor?: string; failTags?: boolean } = {}) {
  const nodes = [
    create(NodeSchema, { id: "n1", nodeType: NodeType.LOCATION, name: "Oakhurst", body: "A village." }),
    create(NodeSchema, { id: "n2", nodeType: NodeType.LOCATION, name: "The Citadel", body: "Sunken." }),
  ];
  const tags: { nodeId: string; tag: string }[] = [{ nodeId: "n1", tag: "act one" }];
  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      listNodes: () => create(ListNodesResponseSchema, { nodes }),
      getCampaignTags: () => create(GetCampaignTagsResponseSchema, { entries: tags }),
      listBoards: () => create(ListBoardsResponseSchema, { boards: [] }),
      listNodeEdges: () => create(ListNodeEdgesResponseSchema, {}),
      listNodeAppearances: () => create(ListNodeAppearancesResponseSchema, {}),
      updateNode: (req) => {
        if (req.id === opts.failUpdateFor) throw new ConnectError("name already taken", Code.AlreadyExists);
        const i = nodes.findIndex((n) => n.id === req.id);
        nodes[i] = create(NodeSchema, { ...nodes[i], name: req.name, body: req.body });
        return create(UpdateNodeResponseSchema, { node: nodes[i] });
      },
      setNodeTags: (req) => {
        if (opts.failTags) throw new ConnectError("too many tags", Code.InvalidArgument);
        return create(SetNodeTagsResponseSchema, { tags: req.tags });
      },
    });
  });
  return { transport, nodes };
}

function mount(opts: Parameters<typeof backend>[0] = {}) {
  const h = backend(opts);
  const queryClient = makeQueryClient();
  render(
    <Providers transport={h.transport} queryClient={queryClient}>
      <KnowledgePanel />
    </Providers>,
  );
  return { ...h, queryClient };
}

const nameField = () => screen.getByLabelText("Name") as HTMLInputElement;

describe("KnowledgePanel entry editor", () => {
  it("follows the live row instead of the snapshot taken when Edit was clicked (W7)", async () => {
    const { nodes, queryClient } = mount();
    fireEvent.click(await screen.findByRole("button", { name: "Edit Oakhurst" }));
    expect(screen.queryByAltText("Portrait of Oakhurst")).not.toBeInTheDocument();

    // A portrait lands on the row (an upload elsewhere, a proposal approval…)
    // and the reads are dropped, exactly as NodePortrait does after applying.
    nodes[0] = create(NodeSchema, { ...nodes[0], hasPortrait: true });
    await act(async () => {
      invalidateKnowledgeReads(queryClient);
    });

    expect(await screen.findByAltText("Portrait of Oakhurst")).toBeInTheDocument();
  });

  it("keeps typed text when the row refetches underneath it (W7)", async () => {
    const { nodes, queryClient } = mount();
    fireEvent.click(await screen.findByRole("button", { name: "Edit Oakhurst" }));
    fireEvent.change(nameField(), { target: { value: "Oakhurst (burned)" } });
    nodes[0] = create(NodeSchema, { ...nodes[0], hasPortrait: true });
    await act(async () => {
      invalidateKnowledgeReads(queryClient);
    });
    await screen.findByAltText("Portrait of Oakhurst");
    expect(nameField().value).toBe("Oakhurst (burned)");
  });

  it("does not show one entry's failed save on the next entry opened (W13)", async () => {
    mount({ failUpdateFor: "n1" });
    fireEvent.click(await screen.findByRole("button", { name: "Edit Oakhurst" }));
    fireEvent.change(nameField(), { target: { value: "The Citadel" } });
    fireEvent.click(screen.getByRole("button", { name: "Save entry" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("name already taken");

    // Move on to the other entry (discarding the rejected edit).
    fireEvent.click(screen.getByRole("button", { name: "Edit The Citadel" }));
    fireEvent.click(await screen.findByRole("button", { name: "Discard changes" }));

    await waitFor(() => expect(nameField().value).toBe("The Citadel"));
    expect(screen.queryByText(/name already taken/)).not.toBeInTheDocument();
  });

  it("asks before another entry replaces unsaved typing, and keeps it on Cancel (W10)", async () => {
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "Edit Oakhurst" }));
    fireEvent.change(screen.getByLabelText("Content"), { target: { value: "A village, now ash." } });

    fireEvent.click(screen.getByRole("button", { name: "Edit The Citadel" }));
    expect(await screen.findByText("Discard unsaved changes?")).toBeInTheDocument();
    // Still on Oakhurst while the question is open.
    expect(nameField().value).toBe("Oakhurst");

    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByText("Discard unsaved changes?")).not.toBeInTheDocument());
    expect((screen.getByLabelText("Content") as HTMLTextAreaElement).value).toBe("A village, now ash.");

    fireEvent.click(screen.getByRole("button", { name: "Edit The Citadel" }));
    fireEvent.click(await screen.findByRole("button", { name: "Discard changes" }));
    await waitFor(() => expect(nameField().value).toBe("The Citadel"));
  });

  it("switches entries without asking when nothing was typed (W10)", async () => {
    mount();
    fireEvent.click(await screen.findByRole("button", { name: "Edit Oakhurst" }));
    fireEvent.click(screen.getByRole("button", { name: "Edit The Citadel" }));
    await waitFor(() => expect(nameField().value).toBe("The Citadel"));
    expect(screen.queryByText("Discard unsaved changes?")).not.toBeInTheDocument();
  });

  it("rolls an optimistic tag back when the server rejects it (W12)", async () => {
    mount({ failTags: true });
    fireEvent.click(await screen.findByRole("button", { name: "Edit Oakhurst" }));
    const input = await screen.findByLabelText("Add a tag");
    expect(await screen.findByRole("button", { name: "Remove tag act one" })).toBeInTheDocument();

    fireEvent.change(input, { target: { value: "act two" } });
    fireEvent.keyDown(input, { key: "Enter" });

    expect(await screen.findByText(/Couldn't save tags: too many tags/)).toBeInTheDocument();
    // The refused tag is not left on screen looking saved; the real one stays.
    await waitFor(() =>
      expect(screen.queryByRole("button", { name: "Remove tag act two" })).not.toBeInTheDocument(),
    );
    expect(screen.getByRole("button", { name: "Remove tag act one" })).toBeInTheDocument();
  });
});
