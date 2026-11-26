import { useEffect, useId, useRef, useState } from "react";
import type { KeyboardEvent } from "react";
import { Trash2 } from "lucide-react";

import { NodeType } from "@gen/glyphoxa/management/v1/management_pb";
import type { Node } from "@gen/glyphoxa/management/v1/management_pb";
import { useI18n } from "@/i18n";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { IconButton } from "@/components/ui/IconButton";
import { Input } from "@/components/ui/Input";
import { Markdown } from "@/components/ui/Markdown";
import { SegmentedControl } from "@/components/ui/SegmentedControl";
import { Select } from "@/components/ui/Select";
import { Switch } from "@/components/ui/Switch";
import { Textarea } from "@/components/ui/Textarea";
import { EntryFacts, reseedAspectRows, toAspectRows, toWireAspects } from "./EntryFacts";
import type { AspectRow } from "./EntryFacts";
import { NodeAppearances } from "./NodeAppearances";
import { NodePortrait } from "./NodePortrait";
import { NodeRelations } from "./NodeRelations";
import { NodeTags } from "./NodeTags";
import { NodeBoards } from "./PrepBoards";
import { TYPE_META, TYPE_ORDER, alphaBg, metaOf } from "./knowledgeVocab";

// The entry editor: the detail pane's edit and create modes.
//
// It is split in two labelled groups because the entry is saved in two ways,
// and a GM who cannot tell which is which either loses typing or looks for a
// Save button that is not needed:
//
//   Entry   type, name, facts, content, GM-only: saved with Save (or Ctrl/Cmd+S)
//   Linked  portrait, tags, boards, voiced by, connections, mentions: each of
//           these saves itself the moment it changes
//
// The editor STAYS OPEN after a save. That is where the GM's place is: they
// save and keep writing. Staying open means the rows on screen must take the
// ids the server just gave them, or the next save would insert the new facts a
// second time. `saved` carries the row as the server returned it, and the
// editor reseeds from it in place (see the effect below) rather than
// remounting, so a Ctrl+S in the middle of a sentence keeps the caret where it
// was and anything typed while the save was in flight is not thrown away.

export type EditorFields = {
  nodeType: NodeType;
  name: string;
  body: string;
  gmPrivate: boolean;
  aspects: ReturnType<typeof toWireAspects>;
  /** The persisted aspect ids this editor has loaded: see UpdateNodeRequest.known_aspect_ids. */
  knownAspectIds: string[];
};

type ContentMode = "write" | "preview";

const aspectIds = (node: Node | null) => (node?.aspects ?? []).map((a) => a.id).filter((id) => id !== "");

// What "changed" is measured on. Row ids are deliberately left out: they change
// when a save lands and that is not an edit.
function fingerprintOf(name: string, body: string, gmPrivate: boolean, aspects: AspectRow[]): string {
  return JSON.stringify([
    name,
    body,
    gmPrivate,
    toWireAspects(aspects).map((a) => [a.key, a.value, a.gmPrivate]),
  ]);
}

export function EntryEditor({
  node,
  createType = NodeType.NOTE,
  pending,
  error,
  saved,
  onSubmit,
  onDelete,
  onCancel,
  onDirtyChange,
  onOpenLine,
  onOpenNode,
}: {
  /** The entry being edited; null creates a new one. */
  node: Node | null;
  /** The type a new entry starts as (chosen in the New entry menu). */
  createType?: NodeType;
  pending: boolean;
  /** A failed save, shown in the header where the Save button is. */
  error: string | null;
  /**
   * The last save of THIS entry that landed: the row as the server returned it.
   * `seq` steps on every save, so two saves in a row are told apart.
   */
  saved?: { seq: number; node: Node } | null;
  onSubmit: (fields: EditorFields) => void;
  onDelete?: () => void;
  /** Leave the editor. With unsaved edits this is the explicit discard. */
  onCancel: () => void;
  /** Reports whether the fields differ from what is saved. */
  onDirtyChange?: (dirty: boolean) => void;
  /** Jump to one Transcript Line on the Session screen (mentions, #545). */
  onOpenLine?: (sessionID: string, lineID: string) => void;
  /** Open another entry (a connection's name). */
  onOpenNode?: (nodeID: string) => void;
}) {
  const { t } = useI18n();
  const isEdit = node != null;
  const [nodeType, setNodeType] = useState<NodeType>(node?.nodeType ?? createType);
  const [name, setName] = useState(node?.name ?? "");
  const [body, setBody] = useState(node?.body ?? "");
  const [gmPrivate, setGmPrivate] = useState(node?.gmPrivate ?? false);
  const [aspects, setAspects] = useState<AspectRow[]>(() => toAspectRows(node));
  // What the editor has LOADED, not what the row holds now: the server deletes
  // only rows this editor knew about, which is what stops a save from wiping a
  // fact a proposal approval appended while the editor was open.
  const [knownAspectIds, setKnownAspectIds] = useState<string[]>(() => aspectIds(node));
  const [contentMode, setContentMode] = useState<ContentMode>("write");

  // Dirtiness is measured against what the editor opened with, or last saved:
  // the node prop follows refetches, and a portrait upload landing mid-edit
  // must not make untouched fields read as edited. The entry type is not part
  // of it: picking a type alone on a blank form is not work worth a question.
  const fingerprint = fingerprintOf(name, body, gmPrivate, aspects);
  const [baseline, setBaseline] = useState(fingerprint);
  const isDirty = fingerprint !== baseline;
  useEffect(() => {
    onDirtyChange?.(isDirty);
  }, [isDirty, onDirtyChange]);
  useEffect(
    () => () => onDirtyChange?.(false), // unmount lifts the guard
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  );

  // A save that landed: take the server's ids onto the rows on screen and make
  // what was SENT the new baseline. Anything typed after the click stays, and
  // stays unsaved.
  const sent = useRef<{ name: string; body: string; gmPrivate: boolean } | null>(null);
  const rows = useRef(aspects);
  rows.current = aspects;
  const seenSave = useRef(saved?.seq ?? 0);
  useEffect(() => {
    if (!saved || saved.seq === seenSave.current) return;
    seenSave.current = saved.seq;
    const reseeded = reseedAspectRows(rows.current, saved.node.aspects);
    setAspects(reseeded);
    setKnownAspectIds(aspectIds(saved.node));
    if (sent.current) {
      setBaseline(fingerprintOf(sent.current.name, sent.current.body, sent.current.gmPrivate, reseeded));
      sent.current = null;
    }
  }, [saved]);

  const canSave = name.trim() !== "" && !pending;
  const submit = () => {
    if (!canSave) return;
    sent.current = { name, body, gmPrivate };
    onSubmit({
      nodeType,
      name: name.trim(),
      body,
      gmPrivate,
      aspects: toWireAspects(aspects),
      knownAspectIds,
    });
  };

  const onKeyDown = (event: KeyboardEvent<HTMLElement>) => {
    // Ctrl+S (Cmd+S on a Mac) saves from anywhere in the editor, and never
    // opens the browser's "save page" dialog over a half-written entry.
    if ((event.ctrlKey || event.metaKey) && !event.altKey && event.key.toLowerCase() === "s") {
      event.preventDefault();
      submit();
    }
  };

  const titleId = useId();
  const entryGroupId = useId();
  const linkedGroupId = useId();
  const meta = metaOf(nodeType);
  // The type vocabulary, in the display language. Built at render (not at
  // module load) so a language switch re-labels the options without a reload.
  const typeOptions = TYPE_ORDER.map((v) => ({ value: String(v), label: t(TYPE_META[v].labelKey) }));
  const heading = isEdit ? t("knowledge.editEntryTitle") : t("knowledge.newEntryTitle");

  return (
    <section
      className="gx-wiki-pane gx-wiki-editor"
      aria-labelledby={titleId}
      data-testid="entry-editor"
      onKeyDown={onKeyDown}
    >
      <header className="gx-wiki-pane__header">
        <div className="gx-wiki-pane__heading">
          <span className="gx-overline">{heading}</span>
          <h2 className="gx-wiki-pane__title" id={titleId}>
            {name.trim() !== "" ? name.trim() : t("knowledge.untitledEntry", { type: t(meta.labelKey) })}
          </h2>
          {/* Always in the tree, so a screen reader hears it appear and go. */}
          <span className="gx-wiki-pane__state" role="status">
            {isDirty && (
              <Badge variant="warning" size="sm">
                {t("common.unsavedChanges")}
              </Badge>
            )}
          </span>
        </div>
        <div className="gx-wiki-pane__actions">
          {/* Not `loading`: that disables the button, and a disabled button
              loses keyboard focus, so a GM who saved with Enter would be on
              <body> afterwards. Busy is announced and extra presses ignored. */}
          <Button
            variant="primary"
            size="sm"
            disabled={name.trim() === ""}
            aria-disabled={pending || undefined}
            aria-busy={pending || undefined}
            aria-keyshortcuts="Control+S Meta+S"
            onClick={submit}
          >
            {pending ? t("common.saving") : isEdit ? t("knowledge.saveEntry") : t("knowledge.createEntry")}
          </Button>
          <Button variant="ghost" size="sm" onClick={onCancel}>
            {isEdit && !isDirty ? t("knowledge.doneEditing") : t("common.cancel")}
          </Button>
          {isEdit && onDelete && (
            <IconButton tone="danger" size="sm" label={t("knowledge.deleteEntry")} onClick={onDelete}>
              <Trash2 size={15} />
            </IconButton>
          )}
        </div>
        {error && (
          <p className="gx-editor__status gx-editor__status--error gx-wiki-pane__error" role="alert">
            {error}
          </p>
        )}
      </header>

      <div className="gx-wiki-pane__body">
        <section className="gx-wiki-group" aria-labelledby={entryGroupId}>
          <div className="gx-wiki-group__head">
            <h3 className="gx-wiki-group__title" id={entryGroupId}>
              {t("knowledge.groupEntry")}
            </h3>
            <span className="gx-wiki-group__how">{t("knowledge.groupEntryHow")}</span>
          </div>

          {isEdit ? (
            // The type is fixed once an entry exists (ADR-0008), so it is
            // stated, not offered as a control that can never be used.
            <div className="gx-field">
              <span className="gx-field__label">{t("knowledge.typeLabel")}</span>
              <div className="gx-wiki-typeline">
                <Badge size="sm" style={{ color: meta.color, background: alphaBg(meta.color) }}>
                  <meta.Icon size={11} aria-hidden /> {t(meta.labelKey)}
                </Badge>
                <span className="gx-field__hint">{t("knowledge.typeFixedHint")}</span>
              </div>
            </div>
          ) : (
            <Select
              label={t("knowledge.typeLabel")}
              options={typeOptions}
              value={String(nodeType)}
              onValueChange={(v) => setNodeType(Number(v) as NodeType)}
            />
          )}

          <Input
            label={t("knowledge.nameLabel")}
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={t("knowledge.namePlaceholder")}
            // The editor starts at the name: the GM chose "New entry" or "Edit"
            // to type. It also keeps focus off <body> when Edit swaps the page
            // (and its focused button) for this editor.
            autoFocus
          />

          <EntryFacts rows={aspects} onChange={setAspects} busy={pending} nodeType={nodeType} />

          <div className="gx-wiki-content">
            <div className="gx-wiki-content__bar">
              <SegmentedControl
                size="sm"
                label={t("knowledge.contentModeAria")}
                value={contentMode}
                onChange={setContentMode}
                options={[
                  { value: "write", label: t("knowledge.contentWrite") },
                  { value: "preview", label: t("knowledge.contentPreview") },
                ]}
              />
            </div>
            {contentMode === "write" ? (
              <Textarea
                label={t("knowledge.contentLabel")}
                hint={t("knowledge.contentHint")}
                autoGrow
                rows={5}
                maxRows={24}
                value={body}
                onChange={(e) => setBody(e.target.value)}
              />
            ) : (
              <div className="gx-field">
                <span className="gx-field__label">{t("knowledge.contentLabel")}</span>
                <div className="gx-wiki-content__preview" data-testid="content-preview">
                  {body.trim() === "" ? (
                    <p className="gx-field__hint">{t("knowledge.contentPreviewEmpty")}</p>
                  ) : (
                    <Markdown text={body} />
                  )}
                </div>
              </div>
            )}
          </div>

          <div className="gx-wiki-switch">
            <Switch label={t("knowledge.gmOnlySwitch")} checked={gmPrivate} onCheckedChange={setGmPrivate} />
            <span className="gx-field__hint">{t("knowledge.gmOnlyHint")}</span>
          </div>
        </section>

        <section className="gx-wiki-group" aria-labelledby={linkedGroupId}>
          <div className="gx-wiki-group__head">
            <h3 className="gx-wiki-group__title" id={linkedGroupId}>
              {t("knowledge.groupLinked")}
            </h3>
            <span className="gx-wiki-group__how">
              {isEdit ? t("knowledge.groupLinkedHow") : t("knowledge.groupLinkedAfterCreate")}
            </span>
          </div>
          {isEdit && node && (
            <>
              <NodePortrait node={node} />
              <NodeTags nodeID={node.id} />
              <NodeBoards nodeID={node.id} />
              <NodeRelations node={node} onOpenNode={onOpenNode} />
              <NodeAppearances nodeID={node.id} onOpenLine={onOpenLine} />
            </>
          )}
        </section>
      </div>
    </section>
  );
}
