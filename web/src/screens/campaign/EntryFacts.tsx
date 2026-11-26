import { useLayoutEffect, useRef } from "react";
import type { KeyboardEvent } from "react";
import { ChevronDown, ChevronUp, Eye, EyeOff, Plus, X } from "lucide-react";

import type { Node, NodeType } from "@gen/glyphoxa/management/v1/management_pb";
import { useI18n } from "@/i18n";
import { Button } from "@/components/ui/Button";
import { IconButton } from "@/components/ui/IconButton";
import { Input } from "@/components/ui/Input";
import { Textarea } from "@/components/ui/Textarea";
import { FACT_SUGGESTIONS } from "./knowledgeVocab";

// The per-fact editor (#542): an ordered list of (label, fact) rows, each with
// its own Public / GM only choice. This is what lets an NPC keep its public
// facts and lose only its secret while the entry itself stays public.
//
// It is built to be typed through without the mouse:
//
//   Enter in a label          moves to that row's fact
//   Enter in a fact           starts the next row (Shift+Enter is a line break)
//   Backspace in an empty row removes it and returns to the row above
//   Alt+Up / Alt+Down         moves the row, keeping the caret in it
//
// The arrow buttons stay for a touch screen, which has no Alt key.

// AspectRow is one editable fact in the editor's local state. `uid` is a
// client-only React key: a freshly added row has no server id yet, and reusing
// the array index as a key made a reorder remount the wrong inputs and lose
// focus mid-typing.
//
// `id` is the PERSISTED row's id, empty for a row the GM just added. It is sent
// back on save so the server updates that row in place; a row saved without its
// id is inserted again, which is how a second save used to duplicate a new fact.
export type AspectRow = { uid: number; id: string; key: string; value: string; gmPrivate: boolean };

let nextAspectUid = 0;

export const newAspectRow = (seed: Partial<Omit<AspectRow, "uid">> = {}): AspectRow => ({
  uid: ++nextAspectUid,
  id: "",
  key: "",
  value: "",
  gmPrivate: false,
  ...seed,
});

export function toAspectRows(node: Pick<Node, "aspects"> | null): AspectRow[] {
  return (node?.aspects ?? []).map((a) =>
    newAspectRow({ id: a.id, key: a.key, value: a.value, gmPrivate: a.gmPrivate }),
  );
}

const isBlank = (r: AspectRow) => r.key.trim() === "" && r.value.trim() === "";

// toWireAspects drops rows the GM left entirely blank: the editor keeps an
// empty row around for the next fact, and that convenience must not become a
// save error. The persisted `id` rides along so the server updates that row in
// place and a repeated save is idempotent.
export function toWireAspects(rows: AspectRow[]) {
  return rows
    .filter((r) => !isBlank(r))
    .map((r) => ({ id: r.id, key: r.key.trim(), value: r.value.trim(), gmPrivate: r.gmPrivate }));
}

// reseedAspectRows gives the rows on screen the ids the server just assigned,
// after a save that left the editor open. The rows that were sent and the rows
// that came back are the same list in the same order (blank rows are not
// sent), so they pair up by position and the GM's rows, with their focus and
// any blank row waiting for the next fact, stay exactly as they are. If the
// two lists do not line up (the server normalised something, or a proposal
// approval added a fact meanwhile), the server's list replaces the rows: an
// id attached to the wrong fact would be worse than a redraw.
export function reseedAspectRows(rows: AspectRow[], saved: Node["aspects"]): AspectRow[] {
  const sent = rows.filter((r) => !isBlank(r));
  const lined =
    sent.length === saved.length &&
    sent.every((r, i) => r.key.trim() === saved[i].key && r.value.trim() === saved[i].value);
  if (!lined) return toAspectRows({ aspects: saved });
  let i = 0;
  return rows.map((r) => (isBlank(r) ? r : { ...r, id: saved[i++].id }));
}

type FocusTarget = { uid: number; field: "key" | "value" };

export function EntryFacts({
  rows,
  onChange,
  disabled,
  nodeType,
}: {
  rows: AspectRow[];
  onChange: (rows: AspectRow[]) => void;
  disabled: boolean;
  /** Picks the suggested fact labels. */
  nodeType: NodeType;
}) {
  const { t } = useI18n();
  const list = useRef<HTMLUListElement>(null);
  const addButton = useRef<HTMLButtonElement>(null);
  // Where the caret should be after the rows next change: a row that moves in
  // the DOM loses focus, and a row that was just added has no element yet.
  const wantFocus = useRef<FocusTarget | "add" | null>(null);
  useLayoutEffect(() => {
    const want = wantFocus.current;
    if (!want) return;
    wantFocus.current = null;
    if (want === "add") {
      addButton.current?.focus();
      return;
    }
    list.current
      ?.querySelector<HTMLElement>(`[data-fact-uid="${want.uid}"][data-fact-field="${want.field}"]`)
      ?.focus();
  }, [rows]);

  const patch = (uid: number, next: Partial<AspectRow>) =>
    onChange(rows.map((r) => (r.uid === uid ? { ...r, ...next } : r)));

  const remove = (index: number, refocus: boolean) => {
    if (refocus) {
      const neighbour = rows[index - 1] ?? rows[index + 1];
      wantFocus.current = neighbour
        ? { uid: neighbour.uid, field: rows[index - 1] ? "value" : "key" }
        : "add";
    }
    onChange(rows.filter((_, i) => i !== index));
  };

  const move = (index: number, delta: number, field?: FocusTarget["field"]) => {
    const to = index + delta;
    if (to < 0 || to >= rows.length) return;
    const next = [...rows];
    [next[index], next[to]] = [next[to], next[index]];
    if (field) wantFocus.current = { uid: rows[index].uid, field };
    onChange(next);
  };

  const insertAfter = (index: number, seed: Partial<Omit<AspectRow, "uid">> = {}) => {
    const row = newAspectRow(seed);
    // A suggested label is already filled in, so the caret belongs in the fact.
    wantFocus.current = { uid: row.uid, field: seed.key ? "value" : "key" };
    onChange([...rows.slice(0, index + 1), row, ...rows.slice(index + 1)]);
  };

  const onKeyDown = (
    event: KeyboardEvent<HTMLInputElement | HTMLTextAreaElement>,
    index: number,
    field: FocusTarget["field"],
  ) => {
    // An IME confirms a composition with Enter; that is not "next row".
    if (event.nativeEvent.isComposing || disabled) return;
    const row = rows[index];
    if (event.altKey && (event.key === "ArrowUp" || event.key === "ArrowDown")) {
      event.preventDefault();
      move(index, event.key === "ArrowUp" ? -1 : 1, field);
      return;
    }
    if (event.key === "Enter" && !event.shiftKey && !event.ctrlKey && !event.metaKey) {
      event.preventDefault();
      if (field === "key") {
        list.current
          ?.querySelector<HTMLElement>(`[data-fact-uid="${row.uid}"][data-fact-field="value"]`)
          ?.focus();
      } else {
        insertAfter(index);
      }
      return;
    }
    if (event.key === "Backspace" && row.key === "" && row.value === "") {
      event.preventDefault();
      remove(index, true);
    }
  };

  // Offer only the labels this entry does not carry yet.
  const used = new Set(rows.map((r) => r.key.trim().toLowerCase()));
  const suggestions = (FACT_SUGGESTIONS[nodeType] ?? [])
    .map((s) => ({ label: t(s.labelKey), gmPrivate: s.gmPrivate ?? false }))
    .filter((s) => !used.has(s.label.toLowerCase()));

  return (
    <div className="gx-field gx-wiki-facts" role="group" aria-label={t("knowledge.factsLabel")}>
      <span className="gx-field__label">{t("knowledge.factsLabel")}</span>
      <span className="gx-field__hint">{t("knowledge.factsHint")}</span>
      {rows.length > 0 && (
        <ul className="gx-wiki-facts__list" ref={list}>
          {rows.map((row, i) => (
            <li key={row.uid} className="gx-wiki-fact" data-private={row.gmPrivate || undefined}>
              <div className="gx-wiki-fact__fields">
                <Input
                  aria-label={t("knowledge.factKeyAria", { n: i + 1 })}
                  placeholder={t("knowledge.factKeyPlaceholder")}
                  className="gx-wiki-fact__key"
                  value={row.key}
                  disabled={disabled}
                  data-fact-uid={row.uid}
                  data-fact-field="key"
                  onChange={(e) => patch(row.uid, { key: e.target.value })}
                  onKeyDown={(e) => onKeyDown(e, i, "key")}
                />
                <Textarea
                  aria-label={t("knowledge.factTextAria", { n: i + 1 })}
                  placeholder={t("knowledge.factTextPlaceholder")}
                  className="gx-wiki-fact__value"
                  autoGrow
                  rows={1}
                  maxRows={8}
                  value={row.value}
                  disabled={disabled}
                  data-fact-uid={row.uid}
                  data-fact-field="value"
                  onChange={(e) => patch(row.uid, { value: e.target.value })}
                  onKeyDown={(e) => onKeyDown(e, i, "value")}
                />
              </div>
              <div className="gx-wiki-fact__actions">
                {/* A labelled pill, not a bare eye: whether a fact reaches the
                    table is the one thing on this row that must never be a
                    guess. The name states what it IS and what pressing does. */}
                <button
                  type="button"
                  className="gx-wiki-vis"
                  data-private={row.gmPrivate || undefined}
                  disabled={disabled}
                  aria-label={
                    row.gmPrivate
                      ? t("knowledge.factIsGmOnlyAria", { n: i + 1 })
                      : t("knowledge.factIsPublicAria", { n: i + 1 })
                  }
                  onClick={() => patch(row.uid, { gmPrivate: !row.gmPrivate })}
                >
                  {row.gmPrivate ? <EyeOff size={12} aria-hidden /> : <Eye size={12} aria-hidden />}
                  {row.gmPrivate ? t("knowledge.visibilityGmOnly") : t("knowledge.visibilityPublic")}
                </button>
                <IconButton
                  size="sm"
                  label={t("knowledge.factMoveUpAria", { n: i + 1 })}
                  disabled={disabled || i === 0}
                  onClick={() => move(i, -1)}
                >
                  <ChevronUp size={14} />
                </IconButton>
                <IconButton
                  size="sm"
                  label={t("knowledge.factMoveDownAria", { n: i + 1 })}
                  disabled={disabled || i === rows.length - 1}
                  onClick={() => move(i, 1)}
                >
                  <ChevronDown size={14} />
                </IconButton>
                <IconButton
                  size="sm"
                  tone="danger"
                  label={t("knowledge.factRemoveAria", { n: i + 1 })}
                  disabled={disabled}
                  onClick={() => remove(i, false)}
                >
                  <X size={14} />
                </IconButton>
              </div>
            </li>
          ))}
        </ul>
      )}
      <div className="gx-wiki-facts__add">
        <Button
          ref={addButton}
          variant="ghost"
          size="sm"
          iconStart={<Plus size={13} />}
          disabled={disabled}
          onClick={() => insertAfter(rows.length - 1)}
        >
          {t("knowledge.addFact")}
        </Button>
        {suggestions.length > 0 && (
          <ul className="gx-wiki-facts__suggest" aria-label={t("knowledge.factSuggestionsAria")}>
            {suggestions.map((s) => (
              <li key={s.label}>
                <button
                  type="button"
                  className="gx-wiki-suggest"
                  disabled={disabled}
                  aria-label={
                    s.gmPrivate
                      ? t("knowledge.addSuggestedGmOnlyFactAria", { label: s.label })
                      : t("knowledge.addSuggestedFactAria", { label: s.label })
                  }
                  onClick={() => insertAfter(rows.length - 1, { key: s.label, gmPrivate: s.gmPrivate })}
                >
                  <Plus size={11} aria-hidden />
                  {s.label}
                  {s.gmPrivate && <EyeOff size={11} aria-hidden />}
                </button>
              </li>
            ))}
          </ul>
        )}
      </div>
      <span className="gx-field__hint">{t("knowledge.factsKeyboardHint")}</span>
    </div>
  );
}
