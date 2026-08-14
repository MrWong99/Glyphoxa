import { useEffect, useMemo, useRef, useState } from "react";
import type { MouseEvent as ReactMouseEvent } from "react";
import { useMutation, useQuery } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { ChevronRight, EyeOff, Plus, Sparkles, Trash2, Upload } from "lucide-react";

import { CampaignService, NodeType } from "@gen/glyphoxa/management/v1/management_pb";
import type { Map as PbMap, Node as PbNode, Pin } from "@gen/glyphoxa/management/v1/management_pb";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Input";
import { Switch } from "@/components/ui/Switch";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import { errorMessage } from "@/lib/connectError";
import { invalidateMethodQueries } from "@/lib/queryClient";
import { useI18n } from "@/i18n";
import { alphaBg, metaOf } from "./knowledgeVocab";

// The Maps tab (#538, ADR-0060): world Maps with Knowledge Graph Nodes pinned onto
// them. `resides_in` is a pointer; this is a position.
//
// Pan and zoom are a CSS transform on a wrapper — no mapping library, no tiles.
// Pins are absolutely-positioned buttons at NORMALIZED coordinates, so the whole
// layer is resolution-independent and a re-uploaded image keeps every pin.

/** The image byte route (a plain guarded mount — an <img> cannot speak Connect). */
const mapImageURL = (map: PbMap) =>
  `/api/v1/maps/${map.id}/image?v=${map.updatedAt ? Number(map.updatedAt.seconds) : 0}`;

/**
 * How far the pointer must travel before a press on a pin counts as a drag rather
 * than a click, in client pixels. Small enough that a deliberate nudge still
 * registers; large enough to absorb the hand tremor in an ordinary click.
 */
const DRAG_SLOP_PX = 4;

export function MapsPanel({ onOpenNode }: { onOpenNode?: (nodeID: string) => void }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const listQuery = useQuery(CampaignService.method.listMaps, {});
  // The Location entries a generated map can be seeded from (#541).
  const nodesQuery = useQuery(CampaignService.method.listNodes, {});
  const [openID, setOpenID] = useState<string | null>(null);

  const maps = useMemo(() => listQuery.data?.maps ?? [], [listQuery.data]);
  const currentID = openID ?? maps[0]?.id ?? null;

  // Both reads of the same data: the map list and any open map view.
  const invalidate = () => {
    void invalidateMethodQueries(
      queryClient,
      CampaignService.method.listMaps,
      CampaignService.method.getMapView,
    );
  };

  if (listQuery.isPending) return <div className="gx-skeleton" data-testid="maps-loading" />;
  if (listQuery.isError) {
    return (
      <p className="gx-campaign__error" role="alert">
        {t("campaign.mapsLoadError", { message: listQuery.error.message })}
      </p>
    );
  }

  return (
    <div className="gx-maps">
      <div className="gx-maps__bar">
        {maps.map((m) => (
          <button
            key={m.id}
            type="button"
            className="gx-kg-chip"
            aria-pressed={m.id === currentID}
            onClick={() => setOpenID(m.id)}
          >
            {m.name}
            {m.gmPrivate && <EyeOff size={11} aria-label={t("campaign.mapGmPrivateAria")} />}
          </button>
        ))}
        <NewMapButton
          onCreated={(id) => {
            invalidate();
            setOpenID(id);
          }}
          maps={maps}
          nodes={nodesQuery.data?.nodes ?? []}
        />
      </div>

      {currentID ? (
        // key={currentID}: a suggestion answers "does this belong on THIS map",
        // so its highlight must not survive navigating to another one.
        <MapView
          key={currentID}
          mapID={currentID}
          onNavigate={setOpenID}
          onChanged={invalidate}
          onOpenNode={onOpenNode}
        />
      ) : (
        <p className="gx-kg-empty">{t("campaign.mapsEmpty")}</p>
      )}
    </div>
  );
}

// MapView draws one Map: breadcrumb, image, pins, children, and the unpinned tray.
function MapView({
  mapID,
  onNavigate,
  onChanged,
  onOpenNode,
}: {
  mapID: string;
  onNavigate: (id: string) => void;
  onChanged: () => void;
  onOpenNode?: (nodeID: string) => void;
}) {
  const { t } = useI18n();
  const viewQuery = useQuery(CampaignService.method.getMapView, { id: mapID });
  const queryClient = useQueryClient();
  const surfaceRef = useRef<HTMLDivElement | null>(null);
  const [zoom, setZoom] = useState(1);
  // The pin being dragged and where the pointer has taken it. Kept in LOCAL state
  // rather than written into the query cache: mutating cached data in place does
  // not re-render, so the pin would only jump on release.
  // A drag carries where it STARTED, in client pixels, and whether the pointer has
  // travelled far enough to mean it. Without that, a plain click on a pin — press
  // and release at the same spot — reached the surface's mouse-up as a completed
  // drag and wrote the pin to wherever the pointer happened to be. Clicking a pin
  // to open its entry walked it across the map, one write RPC per click.
  const [drag, setDrag] = useState<
    { id: string; x: number; y: number; fromClientX: number; fromClientY: number; moved: boolean } | null
  >(null);
  const [placing, setPlacing] = useState<PbNode | null>(null);
  // Suggested pins (#541): a set of node ids the model thinks belong here. It is
  // a HIGHLIGHT over the existing tray, never a second list and never a position.
  const [suggested, setSuggested] = useState<ReadonlySet<string>>(() => new Set());
  const suggest = useMutation(CampaignService.method.suggestMapPins, {
    onSuccess: (res) => setSuggested(new Set(res.suggestions.map((s) => s.nodeId))),
  });
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const refresh = () => {
    void invalidateMethodQueries(queryClient, CampaignService.method.getMapView);
    onChanged();
  };
  const fail = (e: unknown) => setError(errorMessage(e));

  const createPin = useMutation(CampaignService.method.createPin, { onSuccess: refresh, onError: fail });
  const updatePin = useMutation(CampaignService.method.updatePin, { onSuccess: refresh, onError: fail });
  const deletePin = useMutation(CampaignService.method.deletePin, { onSuccess: refresh, onError: fail });
  const deleteMap = useMutation(CampaignService.method.deleteMap, { onSuccess: onChanged, onError: fail });

  if (viewQuery.isPending) return <div className="gx-skeleton" data-testid="map-loading" />;
  if (viewQuery.isError) {
    return (
      <p className="gx-campaign__error" role="alert">
        {t("campaign.mapOpenError", { message: viewQuery.error.message })}
      </p>
    );
  }
  const view = viewQuery.data;
  const map = view.map;
  if (!map) return null;

  // Pointer position → normalized 0..1, clamped. The clamp matters: a drag that
  // leaves the surface must land at the edge rather than be refused by the
  // server's range check after the GM already let go.
  const normalize = (clientX: number, clientY: number) => {
    const box = surfaceRef.current?.getBoundingClientRect();
    if (!box || box.width === 0 || box.height === 0) return null;
    const clamp = (v: number) => Math.min(1, Math.max(0, v));
    return { x: clamp((clientX - box.left) / box.width), y: clamp((clientY - box.top) / box.height) };
  };

  return (
    <div className="gx-maps__view">
      {/* Breadcrumb, farthest ancestor first — the chain reads outside-in. */}
      {view.breadcrumb.length > 0 && (
        <nav className="gx-maps__crumbs" aria-label={t("campaign.mapBreadcrumbAria")}>
          {[...view.breadcrumb].reverse().map((a) => (
            <span key={a.id}>
              <button type="button" className="gx-maps__crumb" onClick={() => onNavigate(a.id)}>
                {a.name}
              </button>
              <ChevronRight size={12} aria-hidden />
            </span>
          ))}
          <span className="gx-maps__crumb gx-maps__crumb--current">{map.name}</span>
        </nav>
      )}

      <div className="gx-maps__tools">
        <button type="button" className="gx-kg-chip" aria-label={t("campaign.zoomIn")} onClick={() => setZoom((z) => Math.min(z * 1.4, 8))}>
          +
        </button>
        <button type="button" className="gx-kg-chip" aria-label={t("campaign.zoomOut")} onClick={() => setZoom((z) => Math.max(z / 1.4, 1))}>
          −
        </button>
        {zoom !== 1 && (
          <Button variant="ghost" size="sm" onClick={() => setZoom(1)}>
            {t("campaign.zoomFit")}
          </Button>
        )}
        <Button
          variant="ghost"
          size="sm"
          iconStart={<Trash2 size={13} />}
          onClick={() => setConfirmDelete(true)}
        >
          {t("campaign.deleteMap")}
        </Button>
        {placing && <span className="gx-maps__hint">{t("campaign.placingHint", { name: placing.name })}</span>}
        {error && (
          <span className="gx-editor__status gx-editor__status--error" role="alert">
            {error}
          </span>
        )}
      </div>

      <div className="gx-maps__frame">
        <div
          ref={surfaceRef}
          className="gx-maps__surface"
          style={{ transform: `scale(${zoom})`, aspectRatio: `${map.widthPx} / ${map.heightPx}` }}
          role="application"
          aria-label={t("campaign.mapSurfaceAria", { name: map.name })}
          onMouseMove={(ev) => {
            if (!drag) return;
            const far =
              Math.hypot(ev.clientX - drag.fromClientX, ev.clientY - drag.fromClientY) > DRAG_SLOP_PX;
            // Below the slop the glyph does not move at all, so a click never nudges
            // the pin even visually.
            if (!far && !drag.moved) return;
            const at = normalize(ev.clientX, ev.clientY);
            if (at) setDrag({ ...drag, ...at, moved: true });
          }}
          onMouseUp={(ev) => {
            const at = normalize(ev.clientX, ev.clientY);
            if (!at) return;
            if (drag) {
              const pin = view.pins.find((p) => p.id === drag.id);
              const moved = drag.moved;
              setDrag(null);
              // A press-and-release that never travelled is a CLICK: the pin's own
              // onClick opens its entry, and no write is issued.
              if (pin && moved) {
                updatePin.mutate({
                  id: pin.id, x: at.x, y: at.y,
                  labelOverride: pin.labelOverride, gmPrivate: pin.gmPrivate,
                });
              }
              return;
            }
            if (placing) {
              createPin.mutate({ mapId: map.id, nodeId: placing.id, x: at.x, y: at.y });
              setPlacing(null);
            }
          }}
          // Abandoning a drag off the surface reverts to the stored position rather
          // than committing wherever the pointer happened to leave.
          onMouseLeave={() => setDrag(null)}
        >
          {/* A map restored from a bundle exported WITHOUT its images keeps its name,
              nesting, anchor and every pin, and has no picture. Requesting one
              anyway renders the browser's broken-image glyph, which reads as a bug;
              this says what actually happened and what fixes it. Pins still draw on
              top, at their normalized coordinates — they are the part that survived. */}
          {map.hasImage ? (
            <img className="gx-maps__image" src={mapImageURL(map)} alt={t("campaign.mapSurfaceAria", { name: map.name })} draggable={false} />
          ) : (
            <div className="gx-maps__image gx-maps__image--missing" role="img" aria-label={t("campaign.mapNoImageAria", { name: map.name })}>
              <span>{t("campaign.mapNoImage")}</span>
            </div>
          )}
          {view.pins.map((pin) => (
            <PinGlyph
              key={pin.id}
              pin={pin}
              at={drag?.id === pin.id ? drag : undefined}
              onGrab={(ev) =>
                setDrag({
                  id: pin.id,
                  x: pin.x,
                  y: pin.y,
                  fromClientX: ev.clientX,
                  fromClientY: ev.clientY,
                  moved: false,
                })
              }
              onOpen={() => onOpenNode?.(pin.nodeId)}
              onRemove={() => deletePin.mutate({ id: pin.id })}
            />
          ))}
        </div>
      </div>

      {view.children.length > 0 && (
        <div className="gx-maps__children">
          <span className="gx-field__label">{t("campaign.mapChildren")}</span>
          {view.children.map((child) => (
            <button key={child.id} type="button" className="gx-kg-chip" onClick={() => onNavigate(child.id)}>
              {child.name}
            </button>
          ))}
        </div>
      )}

      {/* The unpinned tray: placing the world is a drag, not a form.
          Suggestions (#541) MARK entries already in this tray rather than opening
          a second list — the model narrows what to look at, and the placing
          gesture is untouched, which is what keeps "suggest, never place" true. */}
      <div className="gx-maps__tray">
        <div className="gx-maps__tray-head">
          <span className="gx-field__label">{t("campaign.trayTitle")}</span>
          {view.unpinned.length > 0 && map.anchorNodeId !== "" && (
            <Button
              variant="ghost"
              size="sm"
              iconStart={<Sparkles size={12} />}
              disabled={suggest.isPending}
              onClick={() => suggest.mutate({ mapId: map.id })}
            >
              {suggest.isPending ? t("campaign.suggestPending") : t("campaign.suggestPlacements")}
            </Button>
          )}
        </div>
        {suggest.isError && (
          <span className="gx-editor__status gx-editor__status--error" role="alert">
            {suggest.error.message}
          </span>
        )}
        {suggested.size > 0 && (
          <span className="gx-field__hint">{t("campaign.suggestHint")}</span>
        )}
        {view.unpinned.length === 0 ? (
          <span className="gx-field__hint">{t("campaign.trayAllPlaced")}</span>
        ) : (
          <ul className="gx-maps__tray-list">
            {view.unpinned.map((n) => {
              const meta = metaOf(n.nodeType);
              return (
                <li key={n.id}>
                  <button
                    type="button"
                    className="gx-kg-chip"
                    aria-pressed={placing?.id === n.id}
                    data-suggested={suggested.has(n.id) || undefined}
                    aria-label={
                      suggested.has(n.id) ? t("campaign.suggestedAria", { name: n.name }) : undefined
                    }
                    style={{ color: meta.color, background: alphaBg(meta.color) }}
                    onClick={() => setPlacing((p) => (p?.id === n.id ? null : n))}
                  >
                    <Plus size={11} /> {n.name}
                  </button>
                </li>
              );
            })}
          </ul>
        )}
      </div>

      {confirmDelete && (
        <ConfirmDialog
          open
          onOpenChange={(open) => {
            if (!open) setConfirmDelete(false);
          }}
          title={t("campaign.deleteMapTitle", { name: map.name })}
          description={
            view.pins.length === 1
              ? t("campaign.deleteMapDescOne")
              : t("campaign.deleteMapDescMany", { n: view.pins.length })
          }
          confirmLabel={t("campaign.deleteMap")}
          onConfirm={() => {
            deleteMap.mutate({ id: map.id });
            setConfirmDelete(false);
          }}
        />
      )}
    </div>
  );
}

// PinGlyph is one pin: a positioned button at normalized coordinates. Click opens
// the entry, drag repositions, and the remove affordance unpins without touching
// the entry itself.
function PinGlyph({
  pin,
  at,
  onGrab,
  onOpen,
  onRemove,
}: {
  pin: Pin;
  /** The live drag position, when this pin is the one being dragged. */
  at?: { x: number; y: number };
  onGrab: (ev: ReactMouseEvent) => void;
  onOpen: () => void;
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
    >
      <button
        type="button"
        className="gx-maps__pin-dot"
        style={{ color: meta.color, background: alphaBg(meta.color), borderColor: meta.color }}
        aria-label={
          hidden
            ? t("campaign.pinAriaHidden", { name: label, type: t(meta.labelKey) })
            : t("campaign.pinAria", { name: label, type: t(meta.labelKey) })
        }
        onMouseDown={onGrab}
        onClick={onOpen}
      />
      <span className="gx-maps__pin-label">{label}</span>
      <button
        type="button"
        className="gx-maps__pin-remove"
        aria-label={t("campaign.unpinAria", { name: label })}
        onClick={onRemove}
      >
        ×
      </button>
    </div>
  );
}

// NewMapButton adds a Map, from an upload or from a generated draft (#538, #541).
//
// The image's real pixel dimensions are measured here, client-side, because only
// the browser has decoded it — the server stores what it is told and uses it for
// aspect ratio only.
//
// Generate mode is a DRAFT-REVIEW flow, deliberately identical in shape to the
// Knowledge generator: prompt → preview → save or discard. Nothing reaches
// campaign_map until the GM presses Add map, and discarding costs one closed
// dialog and zero RPCs. The bytes only ever existed here.
function NewMapButton({
  onCreated,
  maps,
  nodes,
}: {
  onCreated: (id: string) => void;
  maps: PbMap[];
  /** Location entries, for seeding a generated map from the wiki. */
  nodes: PbNode[];
}) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [parentID, setParentID] = useState("");
  const [gmPrivate, setGmPrivate] = useState(false);
  const [file, setFile] = useState<File | null>(null);
  const [error, setError] = useState<string | null>(null);
  const create = useMutation(CampaignService.method.createMap);

  // Generate mode (#541). `draft` is the unsaved image: bytes plus the object URL
  // the preview renders. It is the ONLY place those bytes exist.
  const [mode, setMode] = useState<"upload" | "generate">("upload");
  const [prompt, setPrompt] = useState("");
  const [anchorID, setAnchorID] = useState("");
  const [draft, setDraft] = useState<{ bytes: Uint8Array; contentType: string; url: string } | null>(
    null,
  );
  const generate = useMutation(CampaignService.method.generateMapImage);

  // Object URLs are a leak if they outlive their draft, and a draft is discarded
  // far more often than it is saved — that is the point of a review step.
  //
  // The live URL is mirrored in a REF because the unmount cleanup must revoke it
  // without touching state: React discards updates to an unmounted component, so
  // revoking inside a setState updater relies on that updater being run at all.
  const draftURL = useRef<string | null>(null);
  const dropDraft = () => {
    if (draftURL.current) {
      URL.revokeObjectURL(draftURL.current);
      draftURL.current = null;
    }
    setDraft(null);
  };
  useEffect(
    () => () => {
      if (draftURL.current) URL.revokeObjectURL(draftURL.current);
    },
    [],
  );

  const runGenerate = async () => {
    setError(null);
    if (prompt.trim() === "") return;
    try {
      const res = await generate.mutateAsync({ prompt: prompt.trim(), anchorNodeId: anchorID });
      dropDraft();
      const blob = new Blob([res.imageBytes as BlobPart], { type: res.contentType });
      const url = URL.createObjectURL(blob);
      draftURL.current = url;
      setDraft({ bytes: res.imageBytes, contentType: res.contentType, url });
    } catch (err) {
      setError(errorMessage(err));
    }
  };

  const reset = () => {
    dropDraft();
    setOpen(false);
    setName("");
    setFile(null);
    setParentID("");
    setGmPrivate(false);
    setPrompt("");
    setAnchorID("");
  };

  const submit = async () => {
    setError(null);
    if (name.trim() === "") return;
    // Both doors land in the SAME CreateMap call. A generated map is not a
    // different kind of map — it is a map whose bytes came from a model.
    //
    // The bytes and the anchor both key off MODE, and switching mode drops the
    // other source. Keying the bytes off "is there a draft" while keying the
    // anchor off the mode gave two conditions that could disagree: a GM who
    // generated, switched back to Upload and picked a file saved the GENERATED
    // bytes under the file's name.
    const source: { bytes: Uint8Array; contentType: string } | null =
      mode === "generate"
        ? draft
          ? { bytes: draft.bytes, contentType: draft.contentType }
          : null
        : file
          ? { bytes: new Uint8Array(await file.arrayBuffer()), contentType: file.type }
          : null;
    if (!source) return;
    try {
      const dims = await imageDimensions(
        new Blob([source.bytes as BlobPart], { type: source.contentType }),
        t("campaign.mapImageUnreadable"),
      );
      const res = await create.mutateAsync({
        name: name.trim(),
        imageBytes: source.bytes,
        contentType: source.contentType,
        widthPx: dims.width,
        heightPx: dims.height,
        parentMapId: parentID,
        // The anchor is carried through on save. Without it a generated map lands
        // with a NULL anchor and SuggestMapPins has no prose to read — the feature
        // would be unreachable for exactly the flow that seeded it.
        anchorNodeId: mode === "generate" ? anchorID : "",
        gmPrivate,
      });
      reset();
      if (res.map) onCreated(res.map.id);
    } catch (err) {
      setError(errorMessage(err));
    }
  };

  if (!open) {
    return (
      <Button variant="secondary" size="sm" iconStart={<Upload size={13} />} onClick={() => setOpen(true)}>
        {t("campaign.addMap")}
      </Button>
    );
  }

  const locations = nodes.filter((n) => n.nodeType === NodeType.LOCATION);

  return (
    <div className="gx-maps__new" role="dialog" aria-label={t("campaign.addMap")}>
      <div className="gx-maps__modes" role="group" aria-label={t("campaign.imageSourceAria")}>
        <button
          type="button"
          className="gx-kg-chip"
          aria-label={t("campaign.uploadAria")}
          aria-pressed={mode === "upload"}
          onClick={() => {
            setMode("upload");
            dropDraft();
          }}
        >
          <Upload size={12} /> {t("campaign.uploadChip")}
        </button>
        <button
          type="button"
          className="gx-kg-chip"
          aria-label={t("campaign.generateAria")}
          aria-pressed={mode === "generate"}
          onClick={() => {
            setMode("generate");
            setFile(null);
          }}
        >
          <Sparkles size={12} /> {t("campaign.generateChip")}
        </button>
      </div>

      <Input label={t("campaign.nameLabel")} value={name} onChange={(e) => setName(e.target.value)} placeholder={t("campaign.mapNamePlaceholder")} />

      {mode === "upload" ? (
        <div className="gx-field">
          <label className="gx-field__label" htmlFor="gx-map-file">
            {t("campaign.imageLabel")}
          </label>
          <input
            id="gx-map-file"
            type="file"
            accept="image/*"
            onChange={(e) => setFile(e.target.files?.[0] ?? null)}
          />
        </div>
      ) : (
        <>
          <div className="gx-field">
            <label className="gx-field__label" htmlFor="gx-map-prompt">
              {t("campaign.promptLabel")}
            </label>
            <span className="gx-field__hint">{t("campaign.promptHint")}</span>
            <textarea
              id="gx-map-prompt"
              className="gx-input gx-maps__prompt"
              rows={3}
              value={prompt}
              placeholder={t("campaign.promptPlaceholder")}
              onChange={(e) => setPrompt(e.target.value)}
            />
          </div>
          {locations.length > 0 && (
            <div className="gx-field">
              <label className="gx-field__label" htmlFor="gx-map-anchor">
                {t("campaign.anchorLabel")}
              </label>
              <span className="gx-field__hint">{t("campaign.anchorHint")}</span>
              <select
                id="gx-map-anchor"
                className="gx-input"
                value={anchorID}
                onChange={(e) => setAnchorID(e.target.value)}
              >
                <option value="">{t("campaign.anchorNone")}</option>
                {locations.map((n) => (
                  <option key={n.id} value={n.id}>
                    {n.name}
                  </option>
                ))}
              </select>
            </div>
          )}
          <div className="gx-kg-editor__actions">
            <Button
              variant="secondary"
              size="sm"
              iconStart={<Sparkles size={13} />}
              disabled={generate.isPending || prompt.trim() === ""}
              onClick={() => void runGenerate()}
            >
              {generate.isPending
                ? t("campaign.generatePending")
                : draft
                  ? t("campaign.regenerate")
                  : t("campaign.generateChip")}
            </Button>
            {draft && (
              <Button variant="ghost" size="sm" onClick={dropDraft} disabled={generate.isPending}>
                {t("campaign.discardDraft")}
              </Button>
            )}
          </div>
          {draft && (
            <div className="gx-maps__draft">
              <img className="gx-maps__draft-img" src={draft.url} alt={t("campaign.draftAlt")} />
            </div>
          )}
        </>
      )}
      {maps.length > 0 && (
        <div className="gx-field">
          <label className="gx-field__label" htmlFor="gx-map-parent">
            {t("campaign.parentLabel")}
          </label>
          <select
            id="gx-map-parent"
            className="gx-input"
            value={parentID}
            onChange={(e) => setParentID(e.target.value)}
          >
            <option value="">{t("campaign.parentNone")}</option>
            {maps.map((m) => (
              <option key={m.id} value={m.id}>
                {m.name}
              </option>
            ))}
          </select>
        </div>
      )}
      <Switch label={t("campaign.mapGmPrivateLabel")} checked={gmPrivate} onCheckedChange={setGmPrivate} />
      <div className="gx-kg-editor__actions">
        <Button
          variant="primary"
          disabled={
            create.isPending ||
            (mode === "generate" ? !draft : !file) ||
            name.trim() === ""
          }
          onClick={() => void submit()}
        >
          {create.isPending ? t("common.saving") : t("campaign.addMap")}
        </Button>
        <Button variant="ghost" onClick={reset} disabled={create.isPending}>
          {t("common.cancel")}
        </Button>
        {error && (
          <span className="gx-editor__status gx-editor__status--error" role="alert">
            {error}
          </span>
        )}
      </div>
    </div>
  );
}

/**
 * imageDimensions decodes just enough of a blob to read its pixel size.
 *
 * Takes a Blob rather than a File so it serves both doors: an uploaded File and
 * the generated bytes (#541), which never were a file.
 *
 * The failure message is passed in (pre-translated by the caller) because this
 * helper runs outside React and must not bake in a display-language string.
 */
function imageDimensions(file: Blob, unreadableMessage: string): Promise<{ width: number; height: number }> {
  return new Promise((resolve, reject) => {
    const url = URL.createObjectURL(file);
    const img = new Image();
    img.onload = () => {
      URL.revokeObjectURL(url);
      resolve({ width: img.naturalWidth, height: img.naturalHeight });
    };
    img.onerror = () => {
      URL.revokeObjectURL(url);
      reject(new Error(unreadableMessage));
    };
    img.src = url;
  });
}
