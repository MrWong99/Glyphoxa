import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { createRouterTransport, ConnectError, Code } from "@connectrpc/connect";
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
  Toaster: () => null,
}));

import {
  SessionService,
  HighlightSchema,
  ListHighlightsResponseSchema,
  PromoteHighlightResponseSchema,
  DeleteHighlightResponseSchema,
  type Highlight,
  type PromoteHighlightRequest,
  type DeleteHighlightRequest,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { HighlightsStrip } from "./HighlightsStrip";

type HighlightInit = MessageInitShape<typeof HighlightSchema>;

// A candidate Highlight (subject to the 7-day purge) with the given overrides.
function candidate(over: HighlightInit = {}): Highlight {
  return create(HighlightSchema, {
    id: "h1",
    voiceSessionId: "vs1",
    status: "candidate",
    startsAt: timestampFromDate(new Date("2026-07-11T20:15:30Z")),
    endsAt: timestampFromDate(new Date("2026-07-11T20:16:10Z")),
    score: 8.5,
    excerpt: "And then the dragon spoke my true name.",
    reason: "Dramatic reveal — party fell silent.",
    clipContentType: "audio/wav",
    clipSizeBytes: 12345n,
    ...over,
  });
}

function promoted(over: HighlightInit = {}): Highlight {
  return candidate({ id: "h2", status: "promoted", ...over });
}

// An in-memory Highlights store over a router transport: listHighlights returns
// the seed, promote/delete mutate the closure and record their requests so the
// tests can prove the id reaches the wire and the list refetches after invalidate.
function mockTransport(
  seed: Highlight[],
  opts: { failList?: boolean; failPromote?: boolean; failDelete?: boolean } = {},
) {
  let highlights = [...seed];
  const promoteCalls: PromoteHighlightRequest[] = [];
  const deleteCalls: DeleteHighlightRequest[] = [];

  const transport = createRouterTransport(({ service }) => {
    service(SessionService, {
      listHighlights: () => {
        if (opts.failList) throw new ConnectError("list boom", Code.Internal);
        return create(ListHighlightsResponseSchema, { highlights });
      },
      promoteHighlight: (req) => {
        promoteCalls.push(req);
        if (opts.failPromote) throw new ConnectError("promote boom", Code.Internal);
        highlights = highlights.map((h) =>
          h.id === req.id ? create(HighlightSchema, { ...h, status: "promoted" }) : h,
        );
        return create(PromoteHighlightResponseSchema, {
          highlight: highlights.find((h) => h.id === req.id),
        });
      },
      deleteHighlight: (req) => {
        deleteCalls.push(req);
        if (opts.failDelete) throw new ConnectError("delete boom", Code.Internal);
        highlights = highlights.filter((h) => h.id !== req.id);
        return create(DeleteHighlightResponseSchema, {});
      },
    });
  });
  return { transport, promoteCalls, deleteCalls };
}

function renderStrip(
  seed: Highlight[],
  props: { sessionId?: string; live?: boolean } = {},
  opts?: { failList?: boolean; failPromote?: boolean; failDelete?: boolean },
) {
  const ctx = mockTransport(seed, opts);
  render(
    <Providers transport={ctx.transport} queryClient={makeQueryClient()}>
      <HighlightsStrip sessionId={props.sessionId ?? "vs1"} live={props.live ?? false} />
    </Providers>,
  );
  return ctx;
}

describe("HighlightsStrip (#309)", () => {
  it("renders each highlight's excerpt, reason, and a native audio element served through the clip blob path", async () => {
    renderStrip([candidate()]);
    expect(await screen.findByText(/the dragon spoke my true name/i)).toBeInTheDocument();
    expect(screen.getByText(/Dramatic reveal/i)).toBeInTheDocument();
    const audio = document.querySelector("audio");
    expect(audio).not.toBeNull();
    expect(audio).toHaveAttribute("src", "/api/v1/highlights/h1/clip");
    expect(audio).toHaveAttribute("controls");
    expect(audio).toHaveAttribute("preload", "none");
  });

  it("shows a candidate's purge hint badge and a Promote action", async () => {
    renderStrip([candidate()]);
    expect(await screen.findByText(/auto-deletes in 7 days/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /promote/i })).toBeInTheDocument();
  });

  it("shows a promoted highlight's Promoted badge and offers no Promote action", async () => {
    renderStrip([promoted()]);
    expect(await screen.findByText(/^Promoted$/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /promote/i })).not.toBeInTheDocument();
    expect(screen.queryByText(/auto-deletes in 7 days/i)).not.toBeInTheDocument();
  });

  it("promotes a candidate: fires PromoteHighlight with the id and the invalidated list shows it promoted", async () => {
    const ctx = renderStrip([candidate()]);
    fireEvent.click(await screen.findByRole("button", { name: /promote/i }));
    // The list refetches after invalidate: the candidate is now promoted, so its
    // purge hint and Promote action are gone.
    await waitFor(() => expect(screen.getByText(/^Promoted$/)).toBeInTheDocument());
    expect(screen.queryByRole("button", { name: /promote/i })).not.toBeInTheDocument();
    expect(ctx.promoteCalls).toHaveLength(1);
    expect(ctx.promoteCalls[0].id).toBe("h1");
  });

  it("deletes only after the confirm dialog is confirmed, and cancel fires nothing", async () => {
    const ctx = renderStrip([promoted()]);
    // Cancel path first: open the dialog, dismiss it, nothing deleted.
    fireEvent.click(await screen.findByRole("button", { name: /delete/i }));
    const dialog = await screen.findByRole("alertdialog");
    fireEvent.click(within(dialog).getByRole("button", { name: /cancel/i }));
    await waitFor(() => expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument());
    expect(ctx.deleteCalls).toHaveLength(0);

    // Confirm path: reopen, confirm, DeleteHighlight fires with the id and the
    // invalidated list drops the row.
    fireEvent.click(screen.getByRole("button", { name: /delete/i }));
    const dialog2 = await screen.findByRole("alertdialog");
    fireEvent.click(within(dialog2).getByRole("button", { name: /^delete/i }));
    await waitFor(() => expect(ctx.deleteCalls).toHaveLength(1));
    expect(ctx.deleteCalls[0].id).toBe("h2");
    await waitFor(() =>
      expect(screen.queryByText(/the dragon spoke my true name/i)).not.toBeInTheDocument(),
    );
  });

  it("renders the AI scene image through the blob path only when a highlight has an image content type", async () => {
    renderStrip([
      promoted({ id: "h2", imageContentType: "image/png" }),
      candidate({ id: "h1", imageContentType: "" }),
    ]);
    await screen.findByText(/^Promoted$/);
    const images = document.querySelectorAll("img");
    expect(images).toHaveLength(1);
    expect(images[0]).toHaveAttribute("src", "/api/v1/highlights/h2/image");
    expect(images[0]).toHaveAttribute("alt", "Dramatic reveal — party fell silent.");
  });

  it("renders the empty state copy when the session has no highlights", async () => {
    renderStrip([]);
    expect(
      await screen.findByText(/No highlights yet — epic moments appear here/i),
    ).toBeInTheDocument();
    expect(document.querySelector("audio")).toBeNull();
  });

  it("surfaces a load failure inline as an error, not the empty state (#270 lesson)", async () => {
    renderStrip([candidate()], {}, { failList: true });
    const err = await screen.findByRole("alert");
    expect(err).toHaveTextContent(/Couldn't load the highlights/i);
    expect(screen.queryByText(/No highlights yet/i)).not.toBeInTheDocument();
  });
});
