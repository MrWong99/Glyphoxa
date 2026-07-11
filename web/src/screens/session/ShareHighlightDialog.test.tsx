import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { createRouterTransport, ConnectError, Code } from "@connectrpc/connect";
import { create, type MessageInitShape } from "@bufbuild/protobuf";

const toastError = vi.fn();
const toastSuccess = vi.fn();
vi.mock("sonner", () => ({
  toast: { error: (m: string) => toastError(m), success: (m: string) => toastSuccess(m) },
  Toaster: () => null,
}));

import {
  SessionService,
  HighlightSchema,
  ListShareChannelsResponseSchema,
  ShareHighlightResponseSchema,
  type Highlight,
  type ShareHighlightRequest,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { ShareHighlightDialog } from "./ShareHighlightDialog";

type HighlightInit = MessageInitShape<typeof HighlightSchema>;

function promoted(over: HighlightInit = {}): Highlight {
  return create(HighlightSchema, {
    id: "h2",
    voiceSessionId: "vs1",
    status: "promoted",
    excerpt: "And then the dragon spoke my true name.",
    clipContentType: "audio/wav",
    clipSizeBytes: 12345n,
    ...over,
  });
}

function mockTransport(opts: { last?: string; shareErr?: string } = {}) {
  const shareCalls: ShareHighlightRequest[] = [];
  const transport = createRouterTransport(({ service }) => {
    service(SessionService, {
      listShareChannels: () =>
        create(ListShareChannelsResponseSchema, {
          channels: [
            { id: "100", name: "general" },
            { id: "200", name: "highlights" },
          ],
          lastShareChannelId: opts.last ?? "",
        }),
      shareHighlight: (req) => {
        shareCalls.push(req);
        if (opts.shareErr) throw new ConnectError(opts.shareErr, Code.FailedPrecondition);
        return create(ShareHighlightResponseSchema, {});
      },
    });
  });
  return { transport, shareCalls };
}

function renderDialog(
  props: { highlight?: Highlight; sessionLive?: boolean } = {},
  opts?: { last?: string; shareErr?: string },
) {
  const ctx = mockTransport(opts);
  render(
    <Providers transport={ctx.transport} queryClient={makeQueryClient()}>
      <ShareHighlightDialog highlight={props.highlight ?? promoted()} sessionLive={props.sessionLive ?? false} />
    </Providers>,
  );
  return ctx;
}

describe("ShareHighlightDialog (#310)", () => {
  it("posts to the campaign's remembered channel by default (pre-selected)", async () => {
    const ctx = renderDialog({ sessionLive: true }, { last: "200" });
    fireEvent.click(screen.getByRole("button", { name: /share/i }));

    // Once the channels load, Post is enabled against the pre-selected last channel.
    const post = await screen.findByRole("button", { name: /post to channel/i });
    await waitFor(() => expect(post).not.toBeDisabled());
    fireEvent.click(post);

    await waitFor(() => expect(ctx.shareCalls).toHaveLength(1));
    const mode = ctx.shareCalls[0].mode;
    expect(mode.case).toBe("textChannelId");
    expect(mode.case === "textChannelId" && mode.value).toBe("200");
    expect(toastSuccess).toHaveBeenCalledWith(expect.stringMatching(/posted/i));
  });

  it("falls back to the first channel when the campaign has no remembered choice", async () => {
    const ctx = renderDialog({ sessionLive: true }, { last: "" });
    fireEvent.click(screen.getByRole("button", { name: /share/i }));
    const post = await screen.findByRole("button", { name: /post to channel/i });
    await waitFor(() => expect(post).not.toBeDisabled());
    fireEvent.click(post);
    await waitFor(() => expect(ctx.shareCalls).toHaveLength(1));
    const mode = ctx.shareCalls[0].mode;
    expect(mode.case === "textChannelId" && mode.value).toBe("100");
  });

  it("surfaces a size refusal (or any share failure) as an error toast", async () => {
    renderDialog({ sessionLive: true }, { last: "200", shareErr: "clip is 12.0 MB; Discord upload limit is 8 MB" });
    fireEvent.click(screen.getByRole("button", { name: /share/i }));
    const post = await screen.findByRole("button", { name: /post to channel/i });
    await waitFor(() => expect(post).not.toBeDisabled());
    fireEvent.click(post);
    await waitFor(() =>
      expect(toastError).toHaveBeenCalledWith(expect.stringMatching(/12\.0 MB; Discord upload limit is 8 MB/)),
    );
  });

  it("disables Replay in voice with a hint when no session is live", async () => {
    renderDialog({ sessionLive: false }, { last: "200" });
    fireEvent.click(screen.getByRole("button", { name: /share/i }));
    const replay = await screen.findByRole("button", { name: /replay in voice/i });
    expect(replay).toBeDisabled();
    expect(screen.getByText(/Start a Voice Session to replay/i)).toBeInTheDocument();
  });

  it("replays into the live voice channel when a session is live", async () => {
    const ctx = renderDialog({ sessionLive: true }, { last: "200" });
    fireEvent.click(screen.getByRole("button", { name: /share/i }));
    const replay = await screen.findByRole("button", { name: /replay in voice/i });
    expect(replay).not.toBeDisabled();
    fireEvent.click(replay);
    await waitFor(() => expect(ctx.shareCalls).toHaveLength(1));
    expect(ctx.shareCalls[0].mode.case).toBe("voiceReplay");
  });

  it("offers a direct download of the clip through the blob path (no RPC)", async () => {
    renderDialog({ sessionLive: false }, { last: "200" });
    fireEvent.click(screen.getByRole("button", { name: /share/i }));
    const link = await screen.findByRole("link", { name: /download/i });
    expect(link).toHaveAttribute("href", "/api/v1/highlights/h2/clip");
    expect(link).toHaveAttribute("download", "highlight.wav");
  });
});
