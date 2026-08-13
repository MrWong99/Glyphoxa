import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { createRouterTransport, ConnectError, Code } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";

import {
  CampaignService,
  SessionService,
  NodeSchema,
  NodeType,
  SearchNodesResponseSchema,
  SearchTranscriptsResponseSchema,
  SearchHighlightsResponseSchema,
  TranscriptSearchHitSchema,
  HighlightSearchHitSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";

// The palette navigates with the router; the shell mocks the same surface in
// AppShell.test.tsx. navigateMock is module-level so tests can assert the
// deep-link search params a picked result produced.
const navigateMock = vi.fn();
vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => navigateMock,
}));

import { CommandPalette } from "./CommandPalette";

// Ctrl+K palette (#591): opens on the hotkey, debounce-searches the three
// campaign sources, renders grouped results with honest degraded/empty states,
// and deep-links via the ScreenSearch params.

function searchTransport(opts?: {
  semantic?: boolean;
  empty?: boolean;
  transcriptsFail?: boolean;
  calls?: string[];
}) {
  const semantic = opts?.semantic ?? true;
  return createRouterTransport(({ service }) => {
    service(CampaignService, {
      searchNodes: (req) => {
        opts?.calls?.push(`nodes:${req.query}`);
        if (opts?.empty) return create(SearchNodesResponseSchema, { nodes: [] });
        return create(SearchNodesResponseSchema, {
          nodes: [
            create(NodeSchema, {
              id: "n-bart",
              campaignId: "c1",
              nodeType: NodeType.NPC,
              name: "Bart",
              body: "Gruff innkeeper",
              gmPrivate: true,
            }),
          ],
        });
      },
    });
    service(SessionService, {
      searchTranscripts: (req) => {
        opts?.calls?.push(`transcripts:${req.query}`);
        if (opts?.transcriptsFail) throw new ConnectError("boom", Code.Internal);
        if (opts?.empty) return create(SearchTranscriptsResponseSchema, { semantic, hits: [] });
        return create(SearchTranscriptsResponseSchema, {
          semantic,
          hits: [
            create(TranscriptSearchHitSchema, {
              voiceSessionId: "vs1",
              lineId: "u:3",
              who: semantic ? "" : "Lena / Vex",
              kind: semantic ? "" : "player",
              at: timestampFromDate(new Date("2026-08-03T19:30:00Z")),
              snippet: "we promised Bart fifty gold",
            }),
          ],
        });
      },
      searchHighlights: (req) => {
        opts?.calls?.push(`highlights:${req.query}`);
        if (opts?.empty) return create(SearchHighlightsResponseSchema, { hits: [] });
        return create(SearchHighlightsResponseSchema, {
          hits: [
            create(HighlightSearchHitSchema, {
              id: "hl-1",
              voiceSessionId: "vs1",
              excerpt: "the barbarian ate the contract",
              reason: "table erupted",
              startsAt: timestampFromDate(new Date("2026-07-19T20:45:00Z")),
            }),
          ],
        });
      },
    });
  });
}

function renderPalette(transport = searchTransport()) {
  return render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <CommandPalette tenantSlug="acme" />
    </Providers>,
  );
}

function openPalette() {
  fireEvent.keyDown(document, { key: "k", ctrlKey: true });
}

async function searchFor(text: string) {
  openPalette();
  fireEvent.change(screen.getByPlaceholderText(/search entries, transcripts/i), {
    target: { value: text },
  });
}

beforeEach(() => {
  navigateMock.mockClear();
});

describe("CommandPalette (#591)", () => {
  it("opens on Ctrl+K, closes on Escape, and renders nothing while closed", () => {
    renderPalette();
    expect(screen.queryByTestId("command-palette")).not.toBeInTheDocument();

    openPalette();
    expect(screen.getByTestId("command-palette")).toBeInTheDocument();
    expect(screen.getByPlaceholderText(/search entries, transcripts/i)).toBeInTheDocument();

    fireEvent.keyDown(screen.getByPlaceholderText(/search entries, transcripts/i), { key: "Escape" });
    expect(screen.queryByTestId("command-palette")).not.toBeInTheDocument();

    // Cmd+K (macOS) toggles too.
    fireEvent.keyDown(document, { key: "k", metaKey: true });
    expect(screen.getByTestId("command-palette")).toBeInTheDocument();
  });

  it("searches all three sources (debounced) and renders the grouped results", async () => {
    const calls: string[] = [];
    renderPalette(searchTransport({ calls }));
    await searchFor("bart");

    // All three groups render their hits.
    expect(await screen.findByText("Bart")).toBeInTheDocument();
    expect(await screen.findByText("we promised Bart fifty gold")).toBeInTheDocument();
    expect(await screen.findByText("the barbarian ate the contract")).toBeInTheDocument();
    expect(screen.getByText("Entries")).toBeInTheDocument();
    expect(screen.getByText("Transcripts")).toBeInTheDocument();
    expect(screen.getByText("Highlights")).toBeInTheDocument();

    // One debounced RPC per source — not one per keystroke.
    expect(calls.filter((c) => c.startsWith("nodes:"))).toEqual(["nodes:bart"]);

    // Semantic mode shows NO degraded notice.
    expect(screen.queryByTestId("palette-degraded")).not.toBeInTheDocument();
  });

  it("deep-links a node hit to the Knowledge panel and closes", async () => {
    renderPalette();
    await searchFor("bart");
    fireEvent.click(await screen.findByText("Bart"));

    expect(navigateMock).toHaveBeenCalledWith(
      expect.objectContaining({
        to: "/t/$tenantSlug/$screen",
        params: { tenantSlug: "acme", screen: "campaign" },
        search: { view: "knowledge", node: "n-bart" },
      }),
    );
    expect(screen.queryByTestId("command-palette")).not.toBeInTheDocument();
  });

  it("deep-links a transcript hit with the {session, line} pair", async () => {
    renderPalette();
    await searchFor("gold");
    fireEvent.click(await screen.findByText("we promised Bart fifty gold"));

    expect(navigateMock).toHaveBeenCalledWith(
      expect.objectContaining({
        params: { tenantSlug: "acme", screen: "session" },
        search: { session: "vs1", line: "u:3" },
      }),
    );
  });

  it("deep-links a highlight hit with {session, highlight}", async () => {
    renderPalette();
    await searchFor("contract");
    fireEvent.click(await screen.findByText("the barbarian ate the contract"));

    expect(navigateMock).toHaveBeenCalledWith(
      expect.objectContaining({
        params: { tenantSlug: "acme", screen: "session" },
        search: { session: "vs1", highlight: "hl-1" },
      }),
    );
  });

  it("labels keyword transcript mode honestly (semantic=false → degraded notice, speaker shown)", async () => {
    renderPalette(searchTransport({ semantic: false }));
    await searchFor("gold");

    expect(await screen.findByTestId("palette-degraded")).toBeInTheDocument();
    expect(screen.getByText(/keyword matches — semantic search is unavailable/i)).toBeInTheDocument();
    // The keyword hit leads with its speaker.
    expect(screen.getByText("Lena / Vex")).toBeInTheDocument();
  });

  it("shows the honest empty state when nothing matches anywhere", async () => {
    renderPalette(searchTransport({ empty: true }));
    await searchFor("nothingmatchesthis");

    expect(await screen.findByText(/no results for “nothingmatchesthis”/i)).toBeInTheDocument();
  });

  it("surfaces a failed source inline while the others still answer", async () => {
    renderPalette(searchTransport({ transcriptsFail: true }));
    await searchFor("bart");

    // The failing source announces itself…
    expect(await screen.findByRole("alert")).toHaveTextContent(/transcripts: search failed/i);
    // …and the healthy sources still render.
    expect(await screen.findByText("Bart")).toBeInTheDocument();
    expect(await screen.findByText("the barbarian ate the contract")).toBeInTheDocument();
    // The all-quiet empty state must NOT show beside an error.
    await waitFor(() =>
      expect(screen.queryByText(/no results for/i)).not.toBeInTheDocument(),
    );
  });
});
