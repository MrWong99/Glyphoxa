import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { KeyboardEvent as ReactKeyboardEvent, PointerEvent as ReactPointerEvent } from "react";
import { createClient } from "@connectrpc/connect";
import { useMutation, useQuery, useTransport } from "@connectrpc/connect-query";
import { toast } from "sonner";
import {
  ChevronRight,
  Crosshair,
  ImageUp,
  Minus,
  MoreVertical,
  Plus,
  Settings2,
  Trash2,
  Upload,
} from "lucide-react";

import { CampaignService } from "@gen/glyphoxa/management/v1/management_pb";
import type { Map as PbMap, Node as PbNode, Pin } from "@gen/glyphoxa/management/v1/management_pb";
import { Alert } from "@/components/ui/Alert";
import { Button } from "@/components/ui/Button";
import { ConfirmDialog } from "@/components/ui/ConfirmDialog";
import { ErrorState } from "@/components/ui/ErrorState";
import { IconButton } from "@/components/ui/IconButton";
import { Menu, MenuItem, MenuSeparator } from "@/components/ui/Menu";
import { Skeleton } from "@/components/ui/Skeleton";
import { toastUndo } from "@/components/ui/toast";
import { apiErrorMessage, errorMessage } from "@/lib/connectError";
import { useI18n } from "@/i18n";
import { MapSettingsDialog } from "./MapDialogs";
import { PinGlyph, type PinChange } from "./MapPin";
import { MapTray } from "./MapTray";
import { MAP_IMAGE_ACCEPT, MAP_IMAGE_MAX_MB, imageFileProblem, mapImageURL, readImageFile } from "./mapImage";

// MapView draws one Map: breadcrumb, toolbar, the image with its pins, the maps
// nested inside it, and the tray of entries not placed yet.
//
// Zoom sizes the surface by WIDTH inside a scrolling frame — no transform, no
// mapping library, no tiles. Pins are absolutely positioned at NORMALIZED
// coordinates, so the layer is resolution independent, a re-uploaded image
// keeps every pin, and a pin stays the same size on screen however far in the
// GM zooms (a transform would have scaled the dots and labels with the image).
//
// THE GESTURE GRAMMAR. A pin's dot is one button with three jobs, and telling
// them apart is the whole interaction:
//
//   press and release in place      opens the pin's card
//   press, travel past the slop     drags the pin; release writes it
//   arrow keys, then Enter          moves it without a pointer (Escape reverts)
//
// Pointer Events, not mouse events: the same code path serves a mouse, a finger
// and a pen (the pins were mouse-only, #695), and pointer capture keeps a drag
// alive when the pointer leaves the surface.

/**
 * How far the pointer must travel before a press on a pin counts as a drag rather
 * than a click, in client pixels. Small enough that a deliberate nudge still
 * registers; large enough to absorb the hand tremor in an ordinary click.
 */
const DRAG_SLOP_PX = 4;

const ZOOM_MIN = 1;
const ZOOM_MAX = 8;
const ZOOM_STEP = 1.4;

/** Arrow-key travel, as a share of the map: fine by default, coarse with Shift. */
const NUDGE_FINE = 0.01;
const NUDGE_COARSE = 0.05;

type Point = { x: number; y: number };

const clamp01 = (v: number) => Math.min(1, Math.max(0, v));

export function MapView({
  mapID,
  maps,
  nodes,
  onNavigate,
  onChanged,
  onDeleted,
  onOpenNode,
}: {
  mapID: string;
  /** Every map of the campaign, for the settings dialog's "Inside" choice. */
  maps: PbMap[];
  /** The campaign's entries (already loaded by the panel): pin excerpts, "Depicts". */
  nodes: PbNode[];
  onNavigate: (id: string) => void;
  /** Refetches both reads of this data (the map list and the open view). */
  onChanged: () => Promise<void>;
  // Deleting THIS map must also drop the panel's selection — otherwise the
  // view re-reads the deleted id and strands on an error panel.
  onDeleted: (id: string) => void;
  onOpenNode?: (nodeID: string) => void;
}) {
  const { t } = useI18n();
  const viewQuery = useQuery(CampaignService.method.getMapView, { id: mapID });
  const transport = useTransport();
  const hintID = useId();

  const surfaceRef = useRef<HTMLDivElement | null>(null);
  // State, not a ref: the wheel listener below is attached natively, and the
  // frame only mounts once the view has loaded.
  const [frame, setFrame] = useState<HTMLDivElement | null>(null);
  const fileRef = useRef<HTMLInputElement | null>(null);

  const [zoom, setZoom] = useState(1);
  // The pin being dragged and where the pointer has taken it. Kept in LOCAL state
  // rather than written into the query cache: mutating cached data in place does
  // not re-render, so the pin would only jump on release.
  // A drag carries where it STARTED, in client pixels, and whether the pointer has
  // travelled far enough to mean it. Without that, a plain click on a pin — press
  // and release at the same spot — reached the surface's pointer-up as a completed
  // drag and wrote the pin to wherever the pointer happened to be.
  const [drag, setDrag] = useState<
    (Point & { id: string; fromClientX: number; fromClientY: number; moved: boolean; pointerId: number; captured: boolean }) | null
  >(null);
  // A pin being moved with the arrow keys: shown at the new place, written on Enter.
  const [nudge, setNudge] = useState<(Point & { id: string }) | null>(null);
  // Positions that have been written but not read back yet. Without them a
  // released pin snaps to its OLD place until the refetch lands.
  const [pendingMoves, setPendingMoves] = useState<Record<string, Point>>({});
  const [placing, setPlacing] = useState<PbNode | null>(null);
  // The pin a CreatePin in flight will become: drawn at once, dashed, so the
  // click visibly landed even on a slow connection.
  const [provisional, setProvisional] = useState<(Point & { node: PbNode }) | null>(null);
  const [openPin, setOpenPin] = useState<string | null>(null);
  // Suggested pins (#541): a set of node ids the model thinks belong here. It is
  // a HIGHLIGHT over the existing tray, never a second list and never a position.
  const [suggested, setSuggested] = useState<ReadonlySet<string>>(() => new Set());
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [replacing, setReplacing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // What a screen reader hears about a move it cannot see.
  const [announcement, setAnnouncement] = useState("");

  // A drag that just ended is followed by a click on the same dot (the browser
  // synthesises it from the press and the release). It must not open the card.
  const suppressClick = useRef(false);
  // The entry whose new pin should take focus once it renders (keyboard placing).
  const focusNode = useRef<string | null>(null);

  // Every success clears the error line: an error from three actions ago,
  // still on screen after things work again, reads as a fault that is not there.
  const fail = (e: unknown) => setError(errorMessage(e));
  const settled = () => {
    setError(null);
    return onChanged();
  };

  const suggest = useMutation(CampaignService.method.suggestMapPins, {
    onSuccess: (res) => setSuggested(new Set(res.suggestions.map((s) => s.nodeId))),
  });
  const createPin = useMutation(CampaignService.method.createPin);
  const updatePin = useMutation(CampaignService.method.updatePin);
  const deletePin = useMutation(CampaignService.method.deletePin);
  const replaceImage = useMutation(CampaignService.method.replaceMapImage);
  const deleteMap = useMutation(CampaignService.method.deleteMap, {
    onSuccess: () => {
      onDeleted(mapID);
      void onChanged();
    },
    onError: fail,
  });

  const view = viewQuery.data;
  const pins = view?.pins;
  const nodesByID = useMemo(() => new Map(nodes.map((n) => [n.id, n])), [nodes]);

  // The provisional pin gives way to the real one as soon as the view has it.
  useEffect(() => {
    if (provisional && pins?.some((p) => p.nodeId === provisional.node.id)) setProvisional(null);
  }, [pins, provisional]);

  // Keyboard placing ends on a button that then disappears; hand focus to the
  // new pin, where the arrow keys can fine-tune it.
  useEffect(() => {
    const nodeID = focusNode.current;
    if (!nodeID) return;
    const dots = surfaceRef.current?.querySelectorAll<HTMLElement>("[data-node-id]") ?? [];
    const dot = Array.from(dots).find((el) => el.dataset.nodeId === nodeID);
    if (dot) {
      focusNode.current = null;
      dot.focus();
    }
  }, [pins]);

  // Escape disarms the tray entry from anywhere — but not while a dialog or a
  // pin card is open, where Escape already means "close this".
  useEffect(() => {
    if (!placing) return;
    const onKey = (ev: KeyboardEvent) => {
      if (ev.key !== "Escape" || ev.defaultPrevented) return;
      if (document.querySelector('[role="dialog"], [role="alertdialog"], [role="menu"]')) return;
      setPlacing(null);
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [placing]);

  // ── Zoom ──────────────────────────────────────────────────────────────────
  // The point that should stay under the cursor (or at the frame's centre)
  // across a zoom step: where it is on the surface, and where it is in the frame.
  const zoomAnchor = useRef<{ fx: number; fy: number; px: number; py: number } | null>(null);
  const zoomRef = useRef(zoom);
  zoomRef.current = zoom;

  const zoomTo = (next: number, client?: { x: number; y: number }) => {
    const target = Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, next));
    if (target === zoomRef.current) return;
    const surface = surfaceRef.current;
    if (frame && surface) {
      const fb = frame.getBoundingClientRect();
      const sb = surface.getBoundingClientRect();
      const cx = client?.x ?? fb.left + frame.clientWidth / 2;
      const cy = client?.y ?? fb.top + frame.clientHeight / 2;
      if (sb.width > 0 && sb.height > 0) {
        zoomAnchor.current = {
          fx: (cx - sb.left) / sb.width,
          fy: (cy - sb.top) / sb.height,
          px: cx - fb.left,
          py: cy - fb.top,
        };
      }
    }
    setZoom(target);
  };
  const zoomToRef = useRef(zoomTo);
  zoomToRef.current = zoomTo;

  // After the surface has its new width, scroll so the anchor has not moved.
  useLayoutEffect(() => {
    const anchor = zoomAnchor.current;
    zoomAnchor.current = null;
    const surface = surfaceRef.current;
    if (!anchor || !frame || !surface) return;
    frame.scrollLeft = anchor.fx * surface.offsetWidth - anchor.px;
    frame.scrollTop = anchor.fy * surface.offsetHeight - anchor.py;
  }, [zoom, frame]);

  // Ctrl/Cmd + wheel zooms at the cursor (a trackpad pinch arrives as exactly
  // that). A plain wheel still scrolls the frame. Attached natively because
  // React registers wheel listeners as passive, and a passive listener cannot
  // stop the browser from zooming the whole page instead.
  useEffect(() => {
    if (!frame) return;
    const onWheel = (ev: WheelEvent) => {
      if (!ev.ctrlKey && !ev.metaKey) return;
      ev.preventDefault();
      // Lines (Firefox with a mouse wheel) → roughly pixels.
      const dy = ev.deltaMode === 1 ? ev.deltaY * 33 : ev.deltaY;
      zoomToRef.current(zoomRef.current * Math.exp(-dy * 0.002), { x: ev.clientX, y: ev.clientY });
    };
    frame.addEventListener("wheel", onWheel, { passive: false });
    return () => frame.removeEventListener("wheel", onWheel);
  }, [frame]);

  // Only when there is nothing to show: once a map is on screen, a failed
  // background refetch must not tear it down to an error panel.
  if (!view?.map) {
    if (viewQuery.isPending) return <Skeleton.Block ratio="16 / 9" label={t("maps.loadingMap")} />;
    return (
      <ErrorState
        title={t("maps.openErrorTitle")}
        message={viewQuery.isError ? errorMessage(viewQuery.error) : undefined}
        onRetry={() => void viewQuery.refetch()}
        retrying={viewQuery.isFetching}
      />
    );
  }
  const map = view.map;

  // Pointer position → normalized 0..1, clamped. The clamp matters: a drag that
  // leaves the surface must land at the edge rather than be refused by the
  // server's range check after the GM already let go.
  const normalize = (clientX: number, clientY: number): Point | null => {
    const box = surfaceRef.current?.getBoundingClientRect();
    if (!box || box.width === 0 || box.height === 0) return null;
    return { x: clamp01((clientX - box.left) / box.width), y: clamp01((clientY - box.top) / box.height) };
  };

  /** The middle of what is visible — where a keyboard placement lands. */
  const visibleCentre = (): Point => {
    if (frame) {
      const fb = frame.getBoundingClientRect();
      const at = normalize(fb.left + frame.clientWidth / 2, fb.top + frame.clientHeight / 2);
      if (at) return at;
    }
    return { x: 0.5, y: 0.5 };
  };

  const labelOf = (pin: Pin) => pin.labelOverride || pin.nodeName;

  // ── Writes ────────────────────────────────────────────────────────────────

  const movePin = (pin: Pin, at: Point) => {
    setPendingMoves((m) => ({ ...m, [pin.id]: at }));
    const drop = () =>
      setPendingMoves((m) => {
        // Only this write's entry: a second move of the same pin owns its own.
        if (m[pin.id] !== at) return m;
        const rest = { ...m };
        delete rest[pin.id];
        return rest;
      });
    updatePin.mutate(
      { id: pin.id, x: at.x, y: at.y, labelOverride: pin.labelOverride, gmPrivate: pin.gmPrivate },
      {
        onSuccess: () => {
          setAnnouncement(t("maps.pinMoved", { name: labelOf(pin) }));
          void settled().finally(drop);
        },
        onError: (e) => {
          drop();
          fail(e);
        },
      },
    );
  };

  const changePin = (pin: Pin, change: PinChange) => {
    // Position rides along unchanged: UpdatePin is a full-field save.
    updatePin.mutate(
      { id: pin.id, x: pin.x, y: pin.y, labelOverride: change.labelOverride, gmPrivate: change.gmPrivate },
      { onSuccess: () => void settled(), onError: fail },
    );
  };

  const place = (node: PbNode, at: Point, opts: { focus?: boolean } = {}) => {
    // One placement at a time: a second click while the first is in flight
    // would ask for the same entry twice and be refused as already pinned.
    if (createPin.isPending) return;
    setProvisional({ node, ...at });
    createPin.mutate(
      { mapId: map.id, nodeId: node.id, x: at.x, y: at.y },
      {
        onSuccess: () => {
          // Only now is the entry disarmed. On a failure it stays armed, so
          // the next click simply tries again.
          setPlacing((current) => (current?.id === node.id ? null : current));
          if (opts.focus) focusNode.current = node.id;
          setAnnouncement(t("maps.pinPlaced", { name: node.name }));
          void settled();
        },
        onError: (e) => {
          setProvisional(null);
          fail(e);
        },
      },
    );
  };

  const removePin = (pin: Pin) => {
    setOpenPin(null);
    deletePin.mutate(
      { id: pin.id },
      {
        onSuccess: () => {
          // The pin (and the card the click came from) is gone; keep keyboard
          // focus on the map rather than letting it fall to <body>.
          frame?.focus();
          void settled();
          // Undo re-creates the pin where it was, label and privacy included.
          // Through the transport, not a hook: the toast outlives this view if
          // the GM has already moved on to another map.
          toastUndo(t("maps.pinRemoved", { name: labelOf(pin) }), () => {
            createClient(CampaignService, transport)
              .createPin({
                mapId: pin.mapId,
                nodeId: pin.nodeId,
                x: pin.x,
                y: pin.y,
                labelOverride: pin.labelOverride,
                gmPrivate: pin.gmPrivate,
              })
              .then(() => onChanged())
              .catch((e: unknown) => toast.error(t("maps.pinRestoreError", { message: errorMessage(e) })));
          });
        },
        onError: fail,
      },
    );
  };

  const replaceWith = async (file: File) => {
    const problem = imageFileProblem(file);
    if (problem) {
      setError(problem === "type" ? t("maps.imageWrongType") : t("maps.imageTooLarge", { mb: MAP_IMAGE_MAX_MB }));
      return;
    }
    setReplacing(true);
    try {
      const image = await readImageFile(file, t("campaign.mapImageUnreadable"));
      await replaceImage.mutateAsync({
        id: map.id,
        imageBytes: image.bytes,
        contentType: image.contentType,
        widthPx: image.width,
        heightPx: image.height,
      });
      await settled();
      toast.success(t("maps.imageReplaced"));
    } catch (e) {
      fail(e);
    } finally {
      setReplacing(false);
    }
  };

  // ── The pin's gesture handlers ────────────────────────────────────────────

  const grab = (pin: Pin, ev: ReactPointerEvent<HTMLButtonElement>) => {
    suppressClick.current = false;
    if (ev.pointerType === "mouse" && ev.button !== 0) return;
    let captured = false;
    // Capture keeps the move and the release coming to this pin when the
    // pointer leaves the surface (and is what makes a touch drag work at all).
    // Guarded: an already-released pointer id throws.
    try {
      ev.currentTarget.setPointerCapture(ev.pointerId);
      captured = true;
    } catch {
      captured = false;
    }
    setNudge(null);
    setDrag({
      id: pin.id,
      x: pin.x,
      y: pin.y,
      fromClientX: ev.clientX,
      fromClientY: ev.clientY,
      moved: false,
      pointerId: ev.pointerId,
      captured,
    });
  };

  const onPinKeyDown = (pin: Pin, ev: ReactKeyboardEvent<HTMLButtonElement>) => {
    suppressClick.current = false;
    const mine = nudge?.id === pin.id ? nudge : null;
    const step = ev.shiftKey ? NUDGE_COARSE : NUDGE_FINE;
    const delta: Record<string, Point> = {
      ArrowLeft: { x: -step, y: 0 },
      ArrowRight: { x: step, y: 0 },
      ArrowUp: { x: 0, y: -step },
      ArrowDown: { x: 0, y: step },
    };
    const d = delta[ev.key];
    if (d) {
      // Not a scroll of the frame, and not a move of the page.
      ev.preventDefault();
      const from = mine ?? pendingMoves[pin.id] ?? pin;
      const next = { id: pin.id, x: clamp01(from.x + d.x), y: clamp01(from.y + d.y) };
      setOpenPin(null);
      setNudge(next);
      setAnnouncement(
        t("maps.pinMoving", {
          name: labelOf(pin),
          x: Math.round(next.x * 100),
          y: Math.round(next.y * 100),
        }),
      );
      return;
    }
    if (!mine) return;
    if (ev.key === "Enter") {
      // Enter on a button is a click; with a move pending it commits instead.
      ev.preventDefault();
      setNudge(null);
      movePin(pin, { x: mine.x, y: mine.y });
    } else if (ev.key === "Escape") {
      ev.preventDefault();
      ev.stopPropagation();
      setNudge(null);
      setAnnouncement(t("maps.pinMoveCancelled", { name: labelOf(pin) }));
    }
  };

  const onPinClick = (pin: Pin, ev: { preventDefault: () => void }) => {
    // preventDefault is what stops the Popover trigger from toggling the card.
    if (suppressClick.current) {
      suppressClick.current = false;
      ev.preventDefault();
      return;
    }
    // Space arrives as a click: with a keyboard move pending it commits, like Enter.
    if (nudge?.id === pin.id) {
      ev.preventDefault();
      setNudge(null);
      movePin(pin, { x: nudge.x, y: nudge.y });
    }
  };

  const nudging = nudge ? view.pins.find((p) => p.id === nudge.id) : undefined;
  const zoomPct = Math.round(zoom * 100);

  return (
    <div className="gx-maps__view">
      {/* Breadcrumb, farthest ancestor first — the chain reads outside-in. */}
      {view.breadcrumb.length > 0 && (
        <nav className="gx-maps__crumbs" aria-label={t("campaign.mapBreadcrumbAria")}>
          {[...view.breadcrumb].reverse().map((a) => (
            <span key={a.id} className="gx-maps__crumb-step">
              <button type="button" className="gx-maps__link" onClick={() => onNavigate(a.id)}>
                {a.name}
              </button>
              <ChevronRight size={12} aria-hidden />
            </span>
          ))}
          <span className="gx-maps__crumb-current" aria-current="page">
            {map.name}
          </span>
        </nav>
      )}

      <div className="gx-maps__tools">
        <div className="gx-maps__zoom" role="group" aria-label={t("maps.zoomGroupAria")}>
          <IconButton
            label={t("campaign.zoomOut")}
            size="sm"
            // aria-disabled, not disabled: a button that disables itself under
            // the finger that pressed it drops keyboard focus to <body>.
            aria-disabled={zoom <= ZOOM_MIN || undefined}
            onClick={() => zoomTo(zoom / ZOOM_STEP)}
          >
            <Minus size={14} />
          </IconButton>
          <output className="gx-maps__zoom-readout" aria-label={t("maps.zoomReadoutAria", { pct: zoomPct })}>
            {zoomPct}%
          </output>
          <IconButton
            label={t("campaign.zoomIn")}
            size="sm"
            aria-disabled={zoom >= ZOOM_MAX || undefined}
            onClick={() => zoomTo(zoom * ZOOM_STEP)}
          >
            <Plus size={14} />
          </IconButton>
          <Button variant="ghost" size="sm" aria-disabled={zoom === 1 || undefined} onClick={() => zoomTo(1)}>
            {t("campaign.zoomFit")}
          </Button>
        </div>

        <div className="gx-maps__actions">
          <Button
            variant="secondary"
            size="sm"
            iconStart={<Settings2 size={13} />}
            onClick={() => setSettingsOpen(true)}
          >
            {t("maps.settingsButton")}
          </Button>
          <Menu
            trigger={
              <IconButton label={t("maps.moreActions")} size="sm" loading={replacing}>
                <MoreVertical size={15} />
              </IconButton>
            }
          >
            <MenuItem icon={<ImageUp size={14} />} onSelect={() => fileRef.current?.click()}>
              {t("maps.replaceImage")}
            </MenuItem>
            <MenuSeparator />
            <MenuItem tone="danger" icon={<Trash2 size={14} />} onSelect={() => setConfirmDelete(true)}>
              {t("campaign.deleteMap")}
            </MenuItem>
          </Menu>
          {/* One hidden picker behind both "Replace image" and the no-image
              placeholder's upload button. */}
          <input
            ref={fileRef}
            type="file"
            hidden
            accept={MAP_IMAGE_ACCEPT}
            aria-label={t("maps.replaceImageFileAria")}
            onChange={(e) => {
              const file = e.target.files?.[0];
              // Cleared so picking the SAME file again (after an error) fires a change.
              e.target.value = "";
              if (file) void replaceWith(file);
            }}
          />
        </div>
      </div>

      {error && (
        <Alert tone="danger" onDismiss={() => setError(null)}>
          {error}
        </Alert>
      )}

      {/* What the map is waiting for, said where the GM is looking. */}
      {nudging ? (
        <p className="gx-maps__hint" role="status">
          {t("maps.nudgeHint", { name: labelOf(nudging) })}
        </p>
      ) : (
        placing && (
          <div className="gx-maps__hint" role="status">
            <span>{t("campaign.placingHint", { name: placing.name })}</span>
            <Button
              variant="secondary"
              size="sm"
              iconStart={<Crosshair size={13} />}
              loading={createPin.isPending}
              onClick={() => place(placing, visibleCentre(), { focus: true })}
            >
              {t("maps.placeAtCentre")}
            </Button>
            <Button variant="ghost" size="sm" onClick={() => setPlacing(null)}>
              {t("common.cancel")}
            </Button>
          </div>
        )
      )}

      <div className="gx-maps__stage">
        <div className="gx-maps__main">
          <div className="gx-maps__frame" ref={setFrame} tabIndex={-1}>
            <div
              ref={surfaceRef}
              className="gx-maps__surface"
              data-placing={placing ? "" : undefined}
              data-noimage={map.hasImage ? undefined : ""}
              style={{ width: `${zoom * 100}%`, aspectRatio: `${map.widthPx} / ${map.heightPx}` }}
              role="application"
              aria-label={t("campaign.mapSurfaceAria", { name: map.name })}
              onPointerMove={(ev) => {
                if (!drag || ev.pointerId !== drag.pointerId) return;
                const far =
                  Math.hypot(ev.clientX - drag.fromClientX, ev.clientY - drag.fromClientY) > DRAG_SLOP_PX;
                // Below the slop the glyph does not move at all, so a click never
                // nudges the pin even visually.
                if (!far && !drag.moved) return;
                const at = normalize(ev.clientX, ev.clientY);
                if (!at) return;
                if (!drag.moved) setOpenPin(null);
                setDrag({ ...drag, ...at, moved: true });
              }}
              onPointerUp={(ev) => {
                if (!drag || ev.pointerId !== drag.pointerId) return;
                const pin = view.pins.find((p) => p.id === drag.id);
                const moved = drag.moved;
                setDrag(null);
                // A press-and-release that never travelled is a CLICK: the dot's
                // own click opens the card, and no write is issued.
                if (!pin || !moved) return;
                suppressClick.current = true;
                movePin(pin, normalize(ev.clientX, ev.clientY) ?? { x: drag.x, y: drag.y });
              }}
              // The browser took the gesture over (a scroll, a system swipe):
              // the pin goes back where it was.
              onPointerCancel={() => setDrag(null)}
              // Without capture a drag that leaves the surface is abandoned and
              // reverts, rather than committing wherever the pointer left.
              onPointerLeave={() => {
                if (drag && !drag.captured) setDrag(null);
              }}
              onClick={(ev) => {
                if (!placing) return;
                // A click on a pin is that pin's, not a placement under it.
                if (ev.target instanceof Element && ev.target.closest(".gx-maps__pin, .gx-maps__noimage-action")) return;
                const at = normalize(ev.clientX, ev.clientY);
                if (at) place(placing, at);
              }}
            >
              {/* A map restored from a bundle exported WITHOUT its images keeps its
                  name, nesting, anchor and every pin, and has no picture.
                  Requesting one anyway renders the browser's broken-image glyph,
                  which reads as a bug; this says what actually happened and puts
                  the fix — upload the picture — right there. Pins still draw on
                  top, at their normalized coordinates: they are the part that
                  survived. */}
              {map.hasImage ? (
                <img
                  className="gx-maps__image"
                  src={mapImageURL(map)}
                  alt={t("campaign.mapSurfaceAria", { name: map.name })}
                  draggable={false}
                />
              ) : (
                <div className="gx-maps__noimage">
                  <p className="gx-maps__noimage-text">{t("campaign.mapNoImage")}</p>
                  <div className="gx-maps__noimage-action">
                    <Button
                      variant="primary"
                      size="sm"
                      iconStart={<Upload size={13} />}
                      loading={replacing}
                      onClick={() => fileRef.current?.click()}
                    >
                      {t("maps.uploadImage")}
                    </Button>
                    <span className="gx-maps__noimage-hint">{t("maps.imageHint", { mb: MAP_IMAGE_MAX_MB })}</span>
                  </div>
                </div>
              )}

              {view.pins.map((pin) => {
                const dragging = drag?.id === pin.id && drag.moved;
                const nudged = nudge?.id === pin.id;
                return (
                  <PinGlyph
                    key={pin.id}
                    pin={pin}
                    at={dragging && drag ? drag : nudged && nudge ? nudge : pendingMoves[pin.id]}
                    moving={Boolean(dragging) || nudged}
                    node={nodesByID.get(pin.nodeId)}
                    open={openPin === pin.id}
                    onOpenChange={(open) => setOpenPin(open ? pin.id : null)}
                    describedBy={hintID}
                    updating={updatePin.isPending}
                    onChange={(change) => changePin(pin, change)}
                    onOpenEntry={onOpenNode ? () => onOpenNode(pin.nodeId) : undefined}
                    onRemove={() => removePin(pin)}
                    dotProps={{
                      onPointerDown: (ev) => grab(pin, ev),
                      onKeyDown: (ev) => onPinKeyDown(pin, ev),
                      onClick: (ev) => onPinClick(pin, ev),
                      // Tabbing away from a half-moved pin puts it back: a move
                      // is only ever written by Enter.
                      onBlur: () => {
                        if (nudge?.id === pin.id) setNudge(null);
                      },
                    }}
                  />
                );
              })}

              {provisional && (
                <div
                  className="gx-maps__pin gx-maps__pin--provisional"
                  style={{ left: `${provisional.x * 100}%`, top: `${provisional.y * 100}%` }}
                  data-provisional=""
                  aria-hidden="true"
                >
                  <span className="gx-maps__pin-dot" />
                  <span className="gx-maps__pin-label">{provisional.node.name}</span>
                </div>
              )}
            </div>
          </div>

          <p id={hintID} className="gx-maps__gesture-hint">
            {t("maps.pinGestureHint")}
          </p>

          {view.children.length > 0 && (
            <div className="gx-maps__children">
              <span className="gx-field__label">{t("campaign.mapChildren")}</span>
              {view.children.map((child) => (
                <button key={child.id} type="button" className="gx-maps__chip" onClick={() => onNavigate(child.id)}>
                  {child.name}
                </button>
              ))}
            </div>
          )}
        </div>

        <MapTray
          unpinned={view.unpinned}
          suggested={suggested}
          placingID={placing?.id ?? null}
          onToggle={(n) => setPlacing((p) => (p?.id === n.id ? null : n))}
          canSuggest={map.anchorNodeId !== ""}
          suggesting={suggest.isPending}
          suggestError={suggest.isError ? apiErrorMessage(suggest.error, t) : null}
          onSuggest={() => suggest.mutate({ mapId: map.id })}
          onLinkEntry={() => setSettingsOpen(true)}
        />
      </div>

      <p className="gx-visually-hidden" role="status" aria-live="polite">
        {announcement}
      </p>

      {settingsOpen && (
        <MapSettingsDialog
          map={map}
          maps={maps}
          nodes={nodes}
          onClose={() => setSettingsOpen(false)}
          onSaved={() => void settled()}
        />
      )}

      {confirmDelete && (
        <ConfirmDialog
          open
          onOpenChange={(open) => {
            if (!open) setConfirmDelete(false);
          }}
          title={t("campaign.deleteMapTitle", { name: map.name })}
          description={
            view.pins.length === 0
              ? t("campaign.deleteMapDescNone")
              : view.pins.length === 1
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
