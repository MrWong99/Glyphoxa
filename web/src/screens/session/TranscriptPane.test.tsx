import { describe, it, expect } from "vitest";
import { render, screen, fireEvent, within } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";

import { AgentSchema } from "@gen/glyphoxa/management/v1/management_pb";
import { TranscriptPane } from "./TranscriptPane";
import type { TranscriptLine, TypingState } from "./useSessionEvents";

const BART = create(AgentSchema, { id: "bart", role: "character", name: "Bart", aliases: ["Barty"], speakerColor: 1 });

const at = (h: number, m: number, s = 0) => new Date(2026, 10, 21, h, m, s).toISOString();
const line = (id: string, who: string, kind: TranscriptLine["kind"], text: string, ts = at(20, 15)): TranscriptLine => ({
  id,
  who,
  kind,
  ts,
  text,
  ...(kind === "npc" ? { tag: "NPC" } : {}),
});

const QUIET: TypingState = { active: false, label: "" };

function renderPane(lines: TranscriptLine[], props: Partial<Parameters<typeof TranscriptPane>[0]> = {}) {
  return render(
    <TranscriptPane
      lines={lines}
      typing={QUIET}
      live={false}
      roster={[BART]}
      highlightedLineId={null}
      label="Session transcript"
      empty={<p>nothing yet</p>}
      {...props}
    />,
  );
}

// jsdom lays nothing out, so the pane's scroll geometry is given by hand: a
// tall content box in a short viewport, with scrollTop a plain settable field.
function geometry(pane: HTMLElement, { scrollHeight, clientHeight }: { scrollHeight: number; clientHeight: number }) {
  let top = 0;
  Object.defineProperty(pane, "scrollHeight", { configurable: true, get: () => scrollHeight });
  Object.defineProperty(pane, "clientHeight", { configurable: true, get: () => clientHeight });
  Object.defineProperty(pane, "scrollTop", {
    configurable: true,
    get: () => top,
    set: (v: number) => {
      top = v;
    },
  });
}

describe("TranscriptPane layout", () => {
  it("is a named, keyboard-scrollable region of its own", () => {
    renderPane([line("u:1", "Anna", "player", "Hello")]);
    const pane = screen.getByRole("region", { name: "Session transcript" });
    expect(pane).toHaveAttribute("tabindex", "0");
    expect(pane).toHaveClass("gx-transcript-pane");
  });

  it("groups a speaker's consecutive lines: the name and HH:MM head the run only", () => {
    renderPane([
      line("a:t1", "Bart", "npc", "Well met.", at(20, 15, 7)),
      line("a:t2", "Bart", "npc", "The road east is closed.", at(20, 15, 40)),
      line("u:1", "Anna", "player", "Where is the dragon?", at(20, 16, 2)),
      line("a:t3", "Bart", "npc", "North.", at(20, 17, 30)),
    ]);
    const rows = screen.getAllByRole("listitem");
    expect(rows).toHaveLength(4);

    // First line of Bart's run: name, pill, the time to the minute.
    expect(rows[0]).toHaveClass("gx-line--first");
    expect(within(rows[0]).getByText("Bart")).toHaveClass("gx-line__who");
    expect(within(rows[0]).getByText("NPC")).toBeInTheDocument();
    expect(within(rows[0]).getByText("20:15")).toBeInTheDocument();
    // The seconds are not lost: they are the time's tooltip.
    expect(within(rows[0]).getByText("20:15")).toHaveAttribute("title", "20:15:07");

    // His second line: no visible name, pill or time — but a screen reader
    // moving line by line still hears whose it is.
    expect(rows[1]).not.toHaveClass("gx-line--first");
    expect(rows[1].querySelector(".gx-line__who")).toBeNull();
    expect(rows[1].querySelector("time")).toBeNull();
    expect(within(rows[1]).getByText("Bart")).toHaveClass("gx-visually-hidden");

    // A new speaker starts a new run; so does Bart coming back after her.
    expect(rows[2]).toHaveClass("gx-line--first");
    expect(within(rows[2]).getByText("20:16")).toBeInTheDocument();
    expect(rows[3]).toHaveClass("gx-line--first");
    expect(within(rows[3]).getByText("20:17")).toBeInTheDocument();
  });

  it("does not merge two different speakers who share a name across kinds", () => {
    renderPane([line("u:1", "Bart", "player", "I am a player called Bart"), line("a:t1", "Bart", "npc", "And I the NPC")]);
    const rows = screen.getAllByRole("listitem");
    expect(rows[0]).toHaveClass("gx-line--first");
    expect(rows[1]).toHaveClass("gx-line--first");
  });

  it("draws an NPC in its voice-rail colour (by name or alias) and a named player in a stable one", () => {
    renderPane([
      line("a:t1", "Bart", "npc", "one"),
      line("u:1", "Anna", "player", "two"),
      line("a:t2", "barty", "npc", "three"),
      line("u:2", "Player / DM", "player", "four"),
      line("u:3", "Anna", "player", "five"),
    ]);
    const rows = screen.getAllByRole("listitem");
    expect(rows[0].style.getPropertyValue("--gx-speaker")).toBe("var(--speaker-2)");
    expect(rows[2].style.getPropertyValue("--gx-speaker")).toBe("var(--speaker-2)");
    const anna = rows[1].style.getPropertyValue("--gx-speaker");
    expect(anna).toMatch(/^var\(--speaker-[1-6]\)$/);
    expect(rows[4].style.getPropertyValue("--gx-speaker")).toBe(anna);
    // The unnamed lane keeps the kind's default; its label is localized.
    expect(rows[3].style.getPropertyValue("--gx-speaker")).toBe("");
    expect(within(rows[3]).getByText("Player / GM")).toBeInTheDocument();
  });

  it("marks the deep-linked line", () => {
    renderPane([line("u:1", "Anna", "player", "one"), line("u:2", "Ben", "player", "two")], {
      highlightedLineId: "u:2",
    });
    expect(document.querySelector('[data-line-id="u:2"]')).toHaveAttribute("data-highlighted", "true");
    expect(document.querySelector('[data-line-id="u:1"]')).not.toHaveAttribute("data-highlighted");
  });

  it("shows the empty content when there are no lines and nothing is being said", () => {
    renderPane([]);
    expect(screen.getByText("nothing yet")).toBeInTheDocument();
    expect(screen.queryByRole("list")).not.toBeInTheDocument();
  });

  it("shows the typing line only for the live session", () => {
    const typing: TypingState = { active: true, label: "Bart is speaking…", state: "speaking", who: "Bart" };
    const { rerender } = renderPane([], { typing, live: true });
    expect(screen.getByTestId("typing")).toHaveTextContent("Bart is speaking…");
    rerender(
      <TranscriptPane
        lines={[]}
        typing={typing}
        live={false}
        roster={[BART]}
        highlightedLineId={null}
        label="Session transcript"
        empty={<p>nothing yet</p>}
      />,
    );
    expect(screen.queryByTestId("typing")).not.toBeInTheDocument();
  });
});

describe("TranscriptPane follow (the pane is the scroller)", () => {
  const many = (n: number) => Array.from({ length: n }, (_, i) => line(`u:${i + 1}`, "Anna", "player", `line ${i + 1}`));

  function rerenderWith(rerender: (ui: React.ReactElement) => void, lines: TranscriptLine[], live = true) {
    rerender(
      <TranscriptPane
        lines={lines}
        typing={QUIET}
        live={live}
        roster={[BART]}
        highlightedLineId={null}
        label="Live transcript"
        empty={<p>nothing yet</p>}
      />,
    );
  }

  it("keeps the newest line in view while the GM is at the tail", () => {
    const { rerender } = renderPane(many(3), { live: true, label: "Live transcript" });
    const pane = screen.getByTestId("transcript-pane");
    geometry(pane, { scrollHeight: 1000, clientHeight: 300 });

    rerenderWith(rerender, many(4));
    // Pinned to the end of ITS OWN scroll box — the page is not touched.
    expect(pane.scrollTop).toBe(1000);
    expect(pane).toHaveAttribute("data-following", "true");
    expect(screen.queryByTestId("jump-to-latest")).not.toBeInTheDocument();
  });

  it("follows a reply that grows in place (same line count, longer text)", () => {
    const base = [line("a:t1", "Bart", "npc", "Well")];
    const { rerender } = renderPane(base, { live: true });
    const pane = screen.getByTestId("transcript-pane");
    geometry(pane, { scrollHeight: 400, clientHeight: 300 });
    pane.scrollTop = 0;

    rerenderWith(rerender, [line("a:t1", "Bart", "npc", "Well met, traveller. Sit down.")]);
    expect(pane.scrollTop).toBe(400);
  });

  it("stops following once the GM scrolls up, and offers the way back with a count", () => {
    const { rerender } = renderPane(many(3), { live: true });
    const pane = screen.getByTestId("transcript-pane");
    geometry(pane, { scrollHeight: 1000, clientHeight: 300 });

    // Scroll up to re-read: 600px from the end.
    pane.scrollTop = 100;
    fireEvent.scroll(pane);
    const pill = screen.getByTestId("jump-to-latest");
    expect(pill).toHaveTextContent("Jump to latest");
    expect(pane).not.toHaveAttribute("data-following");

    // New lines arrive: the view stays where the GM put it, and the pill counts them.
    rerenderWith(rerender, many(4));
    expect(pane.scrollTop).toBe(100);
    expect(screen.getByTestId("jump-to-latest")).toHaveTextContent("Jump to latest · 1 new line");
    rerenderWith(rerender, many(6));
    expect(pane.scrollTop).toBe(100);
    expect(screen.getByTestId("jump-to-latest")).toHaveTextContent("Jump to latest · 3 new lines");

    // One press returns to the tail and re-arms the follow.
    fireEvent.click(screen.getByTestId("jump-to-latest"));
    expect(pane.scrollTop).toBe(1000);
    expect(screen.queryByTestId("jump-to-latest")).not.toBeInTheDocument();
    rerenderWith(rerender, many(7));
    expect(pane.scrollTop).toBe(1000);
  });

  it("re-arms when the GM scrolls back down to the tail by hand", () => {
    const { rerender } = renderPane(many(3), { live: true });
    const pane = screen.getByTestId("transcript-pane");
    geometry(pane, { scrollHeight: 1000, clientHeight: 300 });

    pane.scrollTop = 100;
    fireEvent.scroll(pane);
    expect(screen.getByTestId("jump-to-latest")).toBeInTheDocument();

    // Within the slack of the end counts as the tail.
    pane.scrollTop = 660;
    fireEvent.scroll(pane);
    expect(screen.queryByTestId("jump-to-latest")).not.toBeInTheDocument();
    rerenderWith(rerender, many(4));
    expect(pane.scrollTop).toBe(1000);
  });

  it("never auto-scrolls a past session, and offers no pill there", () => {
    const { rerender } = renderPane(many(3), { live: false });
    const pane = screen.getByTestId("transcript-pane");
    geometry(pane, { scrollHeight: 1000, clientHeight: 300 });

    rerenderWith(rerender, many(4), false);
    expect(pane.scrollTop).toBe(0);
    pane.scrollTop = 100;
    fireEvent.scroll(pane);
    expect(screen.queryByTestId("jump-to-latest")).not.toBeInTheDocument();
  });
});
