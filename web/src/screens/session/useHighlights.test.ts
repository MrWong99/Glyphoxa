import { describe, it, expect } from "vitest";
import { create } from "@bufbuild/protobuf";

import { HighlightSchema } from "@gen/glyphoxa/management/v1/management_pb";
import {
  highlightsRefetchInterval,
  HIGHLIGHTS_LIVE_MS,
  HIGHLIGHTS_IMAGE_MS,
} from "./useHighlights";

const hl = (fields: { status: string; imageContentType?: string }) =>
  create(HighlightSchema, {
    status: fields.status,
    imageContentType: fields.imageContentType ?? "",
  });

describe("highlightsRefetchInterval (#309)", () => {
  it("polls the live cadence while the session is live, even with no highlights yet", () => {
    // A live session must surface the FIRST promotion without a reload, so poll
    // even on an empty list.
    expect(highlightsRefetchInterval(true, [])).toBe(HIGHLIGHTS_LIVE_MS);
    expect(highlightsRefetchInterval(true, [hl({ status: "candidate" })])).toBe(
      HIGHLIGHTS_LIVE_MS,
    );
  });

  it("polls the slower image cadence when an ended session has a promoted highlight still lacking its image", () => {
    expect(
      highlightsRefetchInterval(false, [hl({ status: "promoted", imageContentType: "" })]),
    ).toBe(HIGHLIGHTS_IMAGE_MS);
  });

  it("stops polling an ended session once every promoted highlight has its image", () => {
    expect(
      highlightsRefetchInterval(false, [
        hl({ status: "promoted", imageContentType: "image/png" }),
        hl({ status: "candidate", imageContentType: "" }),
      ]),
    ).toBe(false);
  });

  it("does not poll an ended session with an empty highlight list", () => {
    expect(highlightsRefetchInterval(false, [])).toBe(false);
  });
});
