import { useMemo, useRef, useState } from "react";
import type { MouseEvent as ReactMouseEvent } from "react";
import { useMutation, useQuery, createConnectQueryKey } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { ChevronRight, EyeOff, Plus, Trash2, Upload } from "lucide-react";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import type { Map as PbMap, Node as PbNode, Pin } from "@gen/glyphoxa/management/v1/management_pb";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Input";
import { Switch } from "@/components/ui/Switch";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
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
  const queryClient = useQueryClient();
  const listQuery = useQuery(CampaignService.method.listMaps, {});
  const [openID, setOpenID] = useState<string | null>(null);

  const maps = useMemo(() => listQuery.data?.maps ?? [], [listQuery.data]);
  const currentID = openID ?? maps[0]?.id ?? null;

  // Both reads of the same data: the map list and any open map view.
  const invalidate = () => {
    void queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: CampaignService.method.listMaps,
        cardinality: "finite",
      }),
    });
    void queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: CampaignService.method.getMapView,
        cardinality: "finite",
      }),
    });
  };

  if (listQuery.isPending) return <div className="gx-skeleton" data-testid="maps-loading" />;
  if (listQuery.isError) {
    return (
      <p className="gx-campaign__error" role="alert">
        Could not load maps: {listQuery.error.message}
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
            {m.gmPrivate && <EyeOff size={11} aria-label="GM private" />}
          </button>
        ))}
        <NewMapButton onCreated={(id) => { invalidate(); setOpenID(id); }} maps={maps} />
      </div>

      {currentID ? (
        <MapView
          mapID={currentID}
          onNavigate={setOpenID}
          onChanged={invalidate}
          onOpenNode={onOpenNode}
        />
      ) : (
        <p className="gx-kg-empty">
          No maps yet. Upload one and your world gets a place for everything in it.
        </p>
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
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const refresh = () => {
    void queryClient.invalidateQueries({
      queryKey: createConnectQueryKey({
        schema: CampaignService.method.getMapView,
        cardinality: "finite",
      }),
    });
    onChanged();
  };
  const fail = (e: unknown) => setError(e instanceof Error ? e.message : String(e));

  const createPin = useMutation(CampaignService.method.createPin, { onSuccess: refresh, onError: fail });
  const updatePin = useMutation(CampaignService.method.updatePin, { onSuccess: refresh, onError: fail });
  const deletePin = useMutation(CampaignService.method.deletePin, { onSuccess: refresh, onError: fail });
  const deleteMap = useMutation(CampaignService.method.deleteMap, { onSuccess: onChanged, onError: fail });

  if (viewQuery.isPending) return <div className="gx-skeleton" data-testid="map-loading" />;
  if (viewQuery.isError) {
    return (
      <p className="gx-campaign__error" role="alert">
        Could not open the map: {viewQuery.error.message}
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
        <nav className="gx-maps__crumbs" aria-label="Map breadcrumb">
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
        <button type="button" className="gx-kg-chip" aria-label="Zoom in" onClick={() => setZoom((z) => Math.min(z * 1.4, 8))}>
          +
        </button>
        <button type="button" className="gx-kg-chip" aria-label="Zoom out" onClick={() => setZoom((z) => Math.max(z / 1.4, 1))}>
          −
        </button>
        {zoom !== 1 && (
          <Button variant="ghost" size="sm" onClick={() => setZoom(1)}>
            Fit
          </Button>
        )}
        <Button
          variant="ghost"
          size="sm"
          iconStart={<Trash2 size={13} />}
          onClick={() => setConfirmDelete(true)}
        >
          Delete map
        </Button>
        {placing && <span className="gx-maps__hint">Click the map to place “{placing.name}”.</span>}
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
          aria-label={`${map.name} map`}
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
          <img className="gx-maps__image" src={mapImageURL(map)} alt={`${map.name} map`} draggable={false} />
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
          <span className="gx-field__label">Inside this map</span>
          {view.children.map((child) => (
            <button key={child.id} type="button" className="gx-kg-chip" onClick={() => onNavigate(child.id)}>
              {child.name}
            </button>
          ))}
        </div>
      )}

      {/* The unpinned tray: placing the world is a drag, not a form. */}
      <div className="gx-maps__tray">
        <span className="gx-field__label">Not on this map yet</span>
        {view.unpinned.length === 0 ? (
          <span className="gx-field__hint">Everything placeable is already pinned here.</span>
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
          title={`Delete “${map.name}”?`}
          description={
            <>
              This permanently deletes the map and its {view.pins.length} pin
              {view.pins.length === 1 ? "" : "s"}, along with the image. The entries themselves are
              untouched — only their positions here are lost. This can't be undone.
            </>
          }
          confirmLabel="Delete map"
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
        aria-label={`${label} (${meta.label})${hidden ? ", hidden from players" : ""}`}
        onMouseDown={onGrab}
        onClick={onOpen}
      />
      <span className="gx-maps__pin-label">{label}</span>
      <button
        type="button"
        className="gx-maps__pin-remove"
        aria-label={`Unpin ${label}`}
        onClick={onRemove}
      >
        ×
      </button>
    </div>
  );
}

// NewMapButton uploads an image and creates the Map around it. The image's real
// pixel dimensions are measured here, client-side, because only the browser has
// decoded it — the server stores what it is told and uses it for aspect ratio only.
function NewMapButton({ onCreated, maps }: { onCreated: (id: string) => void; maps: PbMap[] }) {
  const [open, setOpen] = useState(false);
  const [name, setName] = useState("");
  const [parentID, setParentID] = useState("");
  const [gmPrivate, setGmPrivate] = useState(false);
  const [file, setFile] = useState<File | null>(null);
  const [error, setError] = useState<string | null>(null);
  const create = useMutation(CampaignService.method.createMap);

  const submit = async () => {
    setError(null);
    if (!file || name.trim() === "") return;
    try {
      const bytes = new Uint8Array(await file.arrayBuffer());
      const dims = await imageDimensions(file);
      const res = await create.mutateAsync({
        name: name.trim(),
        imageBytes: bytes,
        contentType: file.type,
        widthPx: dims.width,
        heightPx: dims.height,
        parentMapId: parentID,
        gmPrivate,
      });
      setOpen(false);
      setName("");
      setFile(null);
      setParentID("");
      setGmPrivate(false);
      if (res.map) onCreated(res.map.id);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  if (!open) {
    return (
      <Button variant="secondary" size="sm" iconStart={<Upload size={13} />} onClick={() => setOpen(true)}>
        Add map
      </Button>
    );
  }

  return (
    <div className="gx-maps__new" role="dialog" aria-label="Add map">
      <Input label="Name" value={name} onChange={(e) => setName(e.target.value)} placeholder="Saltmarsh" />
      <div className="gx-field">
        <label className="gx-field__label" htmlFor="gx-map-file">
          Image
        </label>
        <input
          id="gx-map-file"
          type="file"
          accept="image/*"
          onChange={(e) => setFile(e.target.files?.[0] ?? null)}
        />
      </div>
      {maps.length > 0 && (
        <div className="gx-field">
          <label className="gx-field__label" htmlFor="gx-map-parent">
            Inside
          </label>
          <select
            id="gx-map-parent"
            className="gx-input"
            value={parentID}
            onChange={(e) => setParentID(e.target.value)}
          >
            <option value="">— Top level —</option>
            {maps.map((m) => (
              <option key={m.id} value={m.id}>
                {m.name}
              </option>
            ))}
          </select>
        </div>
      )}
      <Switch label="GM private — never shown to players" checked={gmPrivate} onCheckedChange={setGmPrivate} />
      <div className="gx-kg-editor__actions">
        <Button variant="primary" disabled={create.isPending || !file || name.trim() === ""} onClick={() => void submit()}>
          {create.isPending ? "Uploading…" : "Add map"}
        </Button>
        <Button variant="ghost" onClick={() => setOpen(false)} disabled={create.isPending}>
          Cancel
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

/** imageDimensions decodes just enough of the file to read its pixel size. */
function imageDimensions(file: File): Promise<{ width: number; height: number }> {
  return new Promise((resolve, reject) => {
    const url = URL.createObjectURL(file);
    const img = new Image();
    img.onload = () => {
      URL.revokeObjectURL(url);
      resolve({ width: img.naturalWidth, height: img.naturalHeight });
    };
    img.onerror = () => {
      URL.revokeObjectURL(url);
      reject(new Error("that file could not be read as an image"));
    };
    img.src = url;
  });
}
