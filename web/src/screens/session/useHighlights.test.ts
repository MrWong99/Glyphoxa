import { describe, it, expect } from "vitest";
import { create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";

import { HighlightSchema } from "@gen/glyphoxa/management/v1/management_pb";
import {
  highlightsRefetchInterval,
  HIGHLIGHTS_LIVE_MS,
  HIGHLIGHTS_IMAGE_MS,
  IMAGE_WAIT_MAX_MS,
} from "./useHighlights";

const NOW = 1_800_000_000_000;

const hl = (fields: { status: string; imageContentType?: string; promotedMs?: number }) =>
  create(HighlightSchema, {
    status: fields.status,
    imageContentType: fields.imageContentType ?? "",
    promotedAt: fields.promotedMs != null ? timestampFromMs(fields.promotedMs) : undefined,
  });

describe("highlightsRefetchInterval (#309)", () => {
  it("polls the live cadence while the session is live, even with no highlights yet", () => {
    // A live session must surface the FIRST promotion without a reload, so poll
    // even on an empty list.
    expect(highlightsRefetchInterval(true, [], NOW)).toBe(HIGHLIGHTS_LIVE_MS);
    expect(highlightsRefetchInterval(true, [hl({ status: "candidate" })], NOW)).toBe(
      HIGHLIGHTS_LIVE_MS,
    );
  });

  it("polls the image cadence when an ended session has a freshly-promoted image-less highlight", () => {
    expect(
      highlightsRefetchInterval(
        false,
        [hl({ status: "promoted", imageContentType: "", promotedMs: NOW - 30_000 })],
        NOW,
      ),
    ).toBe(HIGHLIGHTS_IMAGE_MS);
  });

  it("stops polling once the image-wait window has elapsed (unconfigured enricher never fills the image)", () => {
    // Promoted-but-image-less past the window: the enricher is clearly not coming,
    // so stop rather than poll at 15s forever.
    expect(
      highlightsRefetchInterval(
        false,
        [hl({ status: "promoted", imageContentType: "", promotedMs: NOW - IMAGE_WAIT_MAX_MS - 1 })],
        NOW,
      ),
    ).toBe(false);
  });

  it("does not wait on an image-less promotion with no promotedAt", () => {
    expect(
      highlightsRefetchInterval(false, [hl({ status: "promoted", imageContentType: "" })], NOW),
    ).toBe(false);
  });

  it("stops polling an ended session once every promoted highlight has its image", () => {
    expect(
      highlightsRefetchInterval(
        false,
        [
          hl({ status: "promoted", imageContentType: "image/png", promotedMs: NOW - 30_000 }),
          hl({ status: "candidate", imageContentType: "" }),
        ],
        NOW,
      ),
    ).toBe(false);
  });

  it("does not poll an ended session with an empty highlight list", () => {
    expect(highlightsRefetchInterval(false, [], NOW)).toBe(false);
  });
});
