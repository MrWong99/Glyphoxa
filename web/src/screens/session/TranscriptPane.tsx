import { Fragment, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { ArrowDown } from "lucide-react";

import type { Agent } from "@gen/glyphoxa/management/v1/management_pb";
import { InlineMarkdown } from "@/components/ui/Markdown";
import { splitAudioTags } from "@/lib/audioTags";
import { useI18n } from "@/i18n";
import { agentIndex, speakerColor, speakerName, tagLabel, typingLabel } from "./speakers";
import {
  formatClock,
  formatClockShort,
  type LineKind,
  type TranscriptLine,
  type TypingState,
} from "./useSessionEvents";

// lineText renders one transcript line's text: Agent lines (npc/butler) are
// model output and get INLINE markdown — a leaked **emphasis** formats instead
// of showing raw asterisks, while block structure (headings, lists) stays
// literal because a spoken line is one sentence-grain row, not a document.
// Human lines (gm/player) are STT output and stay verbatim: markdown is
// disabled there so a spoken "asterisk" transcribed as * can never reformat a
// player's words. Shared by the transcript and the search-hit rows — both
// render the same lines, so they must not diverge.
//
// Agent lines may also carry expressive-TTS audio tags ("[cheerfully]Hier ist…",
// ElevenLabs v3): those render as a subdued stage direction, separated from the
// spoken words by a space, instead of raw bracket text glued to the sentence.
export function lineText(kind: LineKind | string, text: string): ReactNode {
  if (kind !== "npc" && kind !== "butler") return text;
  const segs = splitAudioTags(text);
  if (segs.length === 1 && segs[0].kind === "text") return <InlineMarkdown text={text} />;
  return segs.map((seg, i) => (
    <Fragment key={i}>
      {i > 0 && " "}
      {seg.kind === "tag" ? (
        <span className="gx-line__direction" data-testid="audio-tag">
          {seg.text}
        </span>
      ) : (
        <InlineMarkdown text={seg.text} />
      )}
    </Fragment>
  ));
}

// How close to the end of the pane still counts as "at the tail": within this
// many pixels a new line keeps the view pinned to it.
const TAIL_SLACK_PX = 80;

// TranscriptPane is the transcript itself: a bounded pane with its OWN scroll,
// so the page — and with it the Stop button and the voice rail — stays put
// while the table talks.
//
// Layout: a two-column grid. The left column is the speaker (name, pill, the
// time the run started) and is the same width on every row, so the text column
// starts at one left edge and a long evening reads as a script. A speaker's
// consecutive lines form a RUN: only its first line carries the name and the
// time, the rest sit under it. On a phone the two columns stack.
//
// Follow: while the session is live the pane keeps its newest line in view —
// the GM must not hand-scroll after every line all night. Scrolling up to
// re-read disarms that (and offers "Jump to latest", with a count of what has
// arrived since); returning to the tail re-arms it. A past session never
// auto-scrolls: the deep-link jump owns its position.
export function TranscriptPane({
  lines,
  typing,
  live,
  visible = true,
  roster,
  highlightedLineId,
  label,
  empty,
}: {
  lines: readonly TranscriptLine[];
  typing: TypingState;
  /** The pane shows the running session: follow the tail, show the indicator. */
  live: boolean;
  /** False while the pane's tab is not the selected one (it stays mounted). */
  visible?: boolean;
  roster: readonly Agent[];
  highlightedLineId: string | null;
  /** Accessible name of the pane ("Live transcript"). */
  label: string;
  /** What to show in place of the lines when there are none (and nothing is being said). */
  empty: ReactNode;
}) {
  const { t } = useI18n();
  const paneRef = useRef<HTMLDivElement | null>(null);
  const agents = useMemo(() => agentIndex(roster), [roster]);
  const showTyping = live && typing.active;

  // followRef is the truth the append effect reads; atTail mirrors it into
  // render state so the pill can appear. unseenFrom is how many lines there
  // were when the GM left the tail — the pill counts from it.
  const followRef = useRef(true);
  const [atTail, setAtTail] = useState(true);
  const [unseenFrom, setUnseenFrom] = useState(0);
  const lineCountRef = useRef(lines.length);
  lineCountRef.current = lines.length;

  const onScroll = () => {
    const pane = paneRef.current;
    if (!pane) return;
    const tail = pane.scrollHeight - pane.scrollTop - pane.clientHeight < TAIL_SLACK_PX;
    if (tail === followRef.current) return;
    followRef.current = tail;
    setAtTail(tail);
    if (!tail) setUnseenFrom(lineCountRef.current);
  };

  const toTail = () => {
    const pane = paneRef.current;
    if (pane) pane.scrollTop = pane.scrollHeight;
  };

  // A coalescing Agent reply grows its line in place, so the tail moves without
  // the line count changing: the last line's length is a trigger too.
  const lastLength = lines.length > 0 ? lines[lines.length - 1].text.length : 0;
  useLayoutEffect(() => {
    if (!live || !visible || !followRef.current) return;
    toTail();
  }, [lines.length, lastLength, showTyping, live, visible]);

  // Switching to another session starts at that session's own position (a live
  // one at its tail and following, a past one at its top): the screen keys the
  // pane on the session, so each one mounts fresh.

  const jumpToLatest = () => {
    followRef.current = true;
    setAtTail(true);
    toTail();
    // The pill hides itself once the pane is at the tail; the keyboard focus
    // goes to the pane it scrolled, not to <body>.
    paneRef.current?.focus({ preventScroll: true });
  };

  const unseen = Math.max(0, lines.length - unseenFrom);
  const hasLines = lines.length > 0;

  return (
    <div className="gx-transcript-pane__frame">
      <div
        className="gx-transcript-pane"
        ref={paneRef}
        onScroll={onScroll}
        // Focusable so the pane scrolls from the keyboard (arrows, Page Up/Down).
        tabIndex={0}
        role="region"
        aria-label={label}
        data-testid="transcript-pane"
        data-following={live && atTail ? "true" : undefined}
      >
        {!hasLines && !showTyping ? (
          empty
        ) : (
          <ol className="gx-transcript">
            {lines.map((line, i) => {
              const prev = i > 0 ? lines[i - 1] : undefined;
              const first = !prev || prev.who !== line.who || prev.kind !== line.kind;
              const name = speakerName(line.who, line.kind, t);
              const tag = tagLabel(line.tag, line.kind, t);
              const color = speakerColor(line.who, line.kind, agents);
              const highlighted = highlightedLineId === line.id;
              return (
                <li
                  key={line.id}
                  className={`gx-line${first ? " gx-line--first" : ""}${highlighted ? " gx-line--highlighted" : ""}`}
                  data-line-id={line.id}
                  data-highlighted={highlighted ? "true" : undefined}
                  style={color ? ({ "--gx-speaker": color } as CSSProperties) : undefined}
                >
                  <div className="gx-line__speaker">
                    {first ? (
                      <>
                        <span className="gx-line__who" data-kind={line.kind}>
                          {name}
                        </span>
                        {tag && (
                          <span className="gx-line__tag" data-kind={line.kind}>
                            {tag}
                          </span>
                        )}
                        <time className="gx-line__ts" dateTime={line.ts} title={formatClock(line.ts)}>
                          {formatClockShort(line.ts)}
                        </time>
                      </>
                    ) : (
                      // The run's later lines show no name, but a screen reader
                      // moving line by line still hears whose each one is.
                      <span className="gx-visually-hidden">{name}</span>
                    )}
                  </div>
                  <div className="gx-line__text">{lineText(line.kind, line.text)}</div>
                </li>
              );
            })}
            {showTyping && (
              <li className="gx-typing" data-testid="typing">
                <span className="gx-typing__dots" aria-hidden="true">
                  <i />
                  <i />
                  <i />
                </span>
                <span className="gx-typing__label">{typingLabel(typing, t)}</span>
              </li>
            )}
          </ol>
        )}
      </div>
      {live && !atTail && (
        <button type="button" className="gx-transcript-pane__jump" onClick={jumpToLatest} data-testid="jump-to-latest">
          <ArrowDown size={14} aria-hidden="true" />
          {unseen > 0
            ? unseen === 1
              ? t("session.jumpToLatestOne")
              : t("session.jumpToLatestNew", { n: unseen })
            : t("session.jumpToLatest")}
        </button>
      )}
    </div>
  );
}
