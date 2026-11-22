import { useState } from "react";
import type { ButtonHTMLAttributes, FormEvent } from "react";
import { BookOpen, MapPinOff } from "lucide-react";

import type { Node as PbNode, Pin } from "@gen/glyphoxa/management/v1/management_pb";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Input";
import { stripMarkdown } from "@/components/ui/Markdown";
import { Popover } from "@/components/ui/Popover";
import { Switch } from "@/components/ui/Switch";
import { useI18n } from "@/i18n";
import { alphaBg, metaOf } from "./knowledgeVocab";

// One pin on a map, and the card that opens from it.
//
// A pin used to do exactly two things: navigate away to its entry on click, and
// disappear on its always-visible "×". Neither is what a GM looking at a map
// usually wants — they want to know what the pin IS, rename it for this map,
// hide it from the players, and only then maybe open the entry. So the click
// opens a card (a Popover anchored to the dot) that does all of that in place,
// and leaving the map is one explicit button inside it.

/** Long enough to recognise the entry, short enough to stay a card. */
const EXCERPT_CHARS = 200;

function excerptOf(body: string): string {
  const plain = stripMarkdown(body);
  if (plain.length <= EXCERPT_CHARS) return plain;
  // Cut at a word boundary where there is one near the end.
  const cut = plain.slice(0, EXCERPT_CHARS);
  const space = cut.lastIndexOf(" ");
  return `${(space > EXCERPT_CHARS * 0.6 ? cut.slice(0, space) : cut).trimEnd()}…`;
}

export type PinChange = { labelOverride: string; gmPrivate: boolean };

export function PinGlyph({
  pin,
  at,
  moving = false,
  node,
  open,
  onOpenChange,
  dotProps,
  describedBy,
  updating,
  onChange,
  onOpenEntry,
  onRemove,
}: {
  pin: Pin;
  /** Where to draw it when that differs from the stored position (a drag, a nudge, a pending write). */
  at?: { x: number; y: number };
  /** The pin is mid-move (dragged or nudged) and not yet written. */
  moving?: boolean;
  /** The pin's entry from the already-loaded entry list, for the excerpt. */
  node?: PbNode;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Pointer, keyboard and click handlers of the map's gesture grammar. */
  dotProps: ButtonHTMLAttributes<HTMLButtonElement>;
  /** Id of the element that explains how a pin is moved. */
  describedBy: string;
  updating: boolean;
  onChange: (change: PinChange) => void;
  onOpenEntry?: () => void;
  onRemove: () => void;
}) {
  const { t } = useI18n();
  const meta = metaOf(pin.nodeType);
  const label = pin.labelOverride || pin.nodeName;
  // A pin is withheld from players by its OWN flag or its entry's — knowing
  // "something is here" and what it is called is most of a secret.
  const hidden = pin.gmPrivate || pin.nodeGmPrivate;
  return (
    <div
      className="gx-maps__pin"
      style={{ left: `${(at?.x ?? pin.x) * 100}%`, top: `${(at?.y ?? pin.y) * 100}%` }}
      data-hidden={hidden || undefined}
      data-moving={moving || undefined}
    >
      <Popover
        label={t("maps.pinCardAria", { name: label })}
        open={open}
        onOpenChange={onOpenChange}
        side="top"
        align="start"
        className="gx-maps-card"
        trigger={
          <button
            type="button"
            className="gx-maps__pin-dot"
            data-pin-id={pin.id}
            data-node-id={pin.nodeId}
            style={{ color: meta.color, background: alphaBg(meta.color), borderColor: meta.color }}
            aria-label={
              hidden
                ? t("campaign.pinAriaHidden", { name: label, type: t(meta.labelKey) })
                : t("campaign.pinAria", { name: label, type: t(meta.labelKey) })
            }
            aria-describedby={describedBy}
            {...dotProps}
          />
        }
      >
        <PinCard
          // Re-seed the label draft when the stored label changes under the card.
          key={pin.labelOverride}
          pin={pin}
          node={node}
          updating={updating}
          onChange={onChange}
          onOpenEntry={onOpenEntry}
          onRemove={onRemove}
        />
      </Popover>
      <span className="gx-maps__pin-label">{label}</span>
    </div>
  );
}

function PinCard({
  pin,
  node,
  updating,
  onChange,
  onOpenEntry,
  onRemove,
}: {
  pin: Pin;
  node?: PbNode;
  updating: boolean;
  onChange: (change: PinChange) => void;
  onOpenEntry?: () => void;
  onRemove: () => void;
}) {
  const { t } = useI18n();
  const meta = metaOf(pin.nodeType);
  const Icon = meta.Icon;
  const [labelDraft, setLabelDraft] = useState(pin.labelOverride);
  const labelChanged = labelDraft.trim() !== pin.labelOverride;
  const excerpt = node ? excerptOf(node.body) : "";

  const saveLabel = (ev: FormEvent) => {
    ev.preventDefault();
    if (!labelChanged || updating) return;
    onChange({ labelOverride: labelDraft.trim(), gmPrivate: pin.gmPrivate });
  };

  return (
    <div className="gx-maps-card__body">
      <div className="gx-maps-card__head">
        <span className="gx-maps-card__type" style={{ color: meta.color }}>
          <Icon size={13} aria-hidden /> {t(meta.labelKey)}
        </span>
        <strong className="gx-maps-card__name">{pin.nodeName}</strong>
      </div>

      {excerpt !== "" ? (
        <p className="gx-maps-card__excerpt">{excerpt}</p>
      ) : (
        node && <p className="gx-maps-card__excerpt gx-maps-card__excerpt--none">{t("maps.pinNoBody")}</p>
      )}

      {/* First in the card on purpose: opening a Popover puts focus on its first
          control. Were that the label field, tapping a pin on a tablet would
          throw the on-screen keyboard over the map every time; this way Enter,
          Enter still opens the entry, as one click used to. */}
      {onOpenEntry && (
        <Button
          className="gx-maps-card__open"
          variant="secondary"
          size="sm"
          iconStart={<BookOpen size={13} />}
          onClick={onOpenEntry}
        >
          {t("maps.pinOpenEntry")}
        </Button>
      )}

      <form className="gx-maps-card__label" onSubmit={saveLabel}>
        <Input
          label={t("maps.pinLabelLabel")}
          hint={t("maps.pinLabelHint")}
          value={labelDraft}
          placeholder={pin.nodeName}
          maxLength={200}
          onChange={(e) => setLabelDraft(e.target.value)}
        />
        <Button type="submit" variant="secondary" size="sm" disabled={!labelChanged} loading={updating && labelChanged}>
          {t("maps.pinLabelSave")}
        </Button>
      </form>

      <div className="gx-maps-card__privacy">
        <Switch
          label={t("maps.pinHideLabel")}
          checked={pin.gmPrivate || pin.nodeGmPrivate}
          // An entry that is GM-only hides every pin of it, whatever the pin says.
          disabled={pin.nodeGmPrivate}
          onCheckedChange={(next) => onChange({ labelOverride: pin.labelOverride, gmPrivate: next })}
        />
        {pin.nodeGmPrivate && <span className="gx-field__hint">{t("maps.pinHiddenByEntry")}</span>}
      </div>

      <div className="gx-maps-card__actions">
        <Button variant="ghost" size="sm" iconStart={<MapPinOff size={13} />} onClick={onRemove}>
          {t("maps.pinRemove")}
        </Button>
      </div>
    </div>
  );
}
