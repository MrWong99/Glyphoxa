import { describe, it, expect } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";

import {
  SessionService,
  SearchTranscriptLinesResponseSchema,
  TranscriptLineMatchSchema,
  type SearchTranscriptLinesResponse,
  type TranscriptLineMatch,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { TranscriptSearch } from "./TranscriptSearch";

// The transcript search's empty state. keepPreviousData holds the last answer
// on screen while the next query is in flight, so "No lines match “X”" may only
// be said once the server has answered for X — never borrowed from the previous
// query's empty answer.

const dragonHit = create(TranscriptLineMatchSchema, {
  sessionId: "vs1",
  lineId: "u:1",
  who: "Player / DM",
  kind: "player",
  ts: timestampFromDate(new Date()),
  text: "Where is the dragon?",
});

function response(lines: TranscriptLineMatch[]) {
  return create(SearchTranscriptLinesResponseSchema, { lines });
}

// "xdragon" answers at once with nothing; "dragon" is held open until the test
// releases it, so the in-flight state can be observed.
function renderSearch() {
  let release!: (res: SearchTranscriptLinesResponse) => void;
  const held = new Promise<SearchTranscriptLinesResponse>((resolve) => {
    release = resolve;
  });
  const calls: string[] = [];
  const transport = createRouterTransport(({ service }) => {
    service(SessionService, {
      searchTranscriptLines: (req) => {
        calls.push(req.query);
        return req.query === "dragon" ? held : response([]);
      },
    });
  });
  render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <TranscriptSearch sessions={[]} currentSessionId={null} onOpen={() => {}} />
    </Providers>,
  );
  return { release, calls };
}

// Lands the empty answer for "xdragon", then leaves "dragon" in flight.
async function typeHeldQuery(calls: string[]) {
  const box = screen.getByRole("searchbox");
  fireEvent.change(box, { target: { value: "xdragon" } });
  expect(await screen.findByText("No lines match “xdragon”.")).toBeInTheDocument();

  fireEvent.change(box, { target: { value: "dragon" } });
  await waitFor(() => expect(calls).toContain("dragon"));
}

describe("TranscriptSearch empty state", () => {
  it("does not claim no match for a query that is still in flight", async () => {
    const { release, calls } = renderSearch();
    await typeHeldQuery(calls);

    // The previous query's empty answer is all there is: no claim about "dragon".
    expect(screen.queryByText(/No lines match/)).not.toBeInTheDocument();

    release(response([dragonHit]));
    expect(await screen.findByText("Where is the dragon?")).toBeInTheDocument();
    expect(screen.queryByText(/No lines match/)).not.toBeInTheDocument();
  });

  it("says no lines match once the server has answered with none", async () => {
    const { release, calls } = renderSearch();
    await typeHeldQuery(calls);
    expect(screen.queryByText(/No lines match/)).not.toBeInTheDocument();

    release(response([]));
    expect(await screen.findByText("No lines match “dragon”.")).toBeInTheDocument();
  });
});
