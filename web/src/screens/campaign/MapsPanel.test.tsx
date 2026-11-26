import { useState } from "react";
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { act, render, screen, fireEvent, waitFor, within } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { toast, Toaster } from "sonner";

import {
  CampaignService,
  CreateMapResponseSchema,
  CreatePinResponseSchema,
  DeleteMapResponseSchema,
  DeletePinResponseSchema,
  GenerateMapImageResponseSchema,
  GetMapViewResponseSchema,
  ListMapsResponseSchema,
  ListNodesResponseSchema,
  NodeType,
  ReplaceMapImageResponseSchema,
  SuggestMapPinsResponseSchema,
  UpdateMapResponseSchema,
  UpdatePinResponseSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { MemoryScreenNav, type ScreenLocation } from "@/app/screenNav";
import { makeQueryClient } from "@/lib/queryClient";
import { CampaignScreenProvider } from "./campaignContext";
import { MapsPanel } from "./MapsPanel";

// The Maps view (#538, ADR-0060). What is worth pinning here is the pointer
// grammar: the same dot OPENS a pin's card, DRAGS the pin and takes the arrow
// keys, so the distinction between the gestures is the whole interaction, and
// getting it wrong is silent — the pin simply drifts.
//
// Everything runs against a small in-memory world behind a router transport, so
// a write → invalidate → refetch is the real round trip and a test can assert
// what the screen shows AFTER it, not just that an RPC fired.

type MapRow = {
  id: string;
  name: string;
  widthPx: number;
  heightPx: number;
  gmPrivate: boolean;
  parentMapId: string;
  anchorNodeId: string;
  hasImage: boolean;
};
type PinRow = {
  id: string;
  mapId: string;
  nodeId: string;
  x: number;
  y: number;
  labelOverride: string;
  gmPrivate: boolean;
};
type NodeRow = { id: string; name: string; nodeType: NodeType; body: string; gmPrivate: boolean };

const map = (over: Partial<MapRow> & { id: string; name: string }): MapRow => ({
  widthPx: 1200,
  heightPx: 800,
  gmPrivate: false,
  parentMapId: "",
  anchorNodeId: "",
  hasImage: true,
  ...over,
});
const node = (over: Partial<NodeRow> & { id: string; name: string }): NodeRow => ({
  nodeType: NodeType.LOCATION,
  body: "",
  gmPrivate: false,
  ...over,
});

const SALTMARSH = map({ id: "m1", name: "Saltmarsh" });
const LINE = node({
  id: "n1",
  name: "The Snapping Line",
  body: "A **fishing boat** with a patched sail, moored at the east pier.",
});
const PIN: PinRow = { id: "p1", mapId: "m1", nodeId: "n1", x: 0.25, y: 0.25, labelOverride: "", gmPrivate: false };

type Deferred = { promise: Promise<void>; resolve: () => void };
function deferred(): Deferred {
  let resolve = () => {};
  const promise = new Promise<void>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

function world(init: { maps?: MapRow[]; pins?: PinRow[]; nodes?: NodeRow[] } = {}) {
  // Copies: the handlers below mutate these rows, and the fixtures are shared.
  const maps = (init.maps ?? [SALTMARSH]).map((m) => ({ ...m }));
  const pins = (init.pins ?? [PIN]).map((p) => ({ ...p }));
  const nodes = (init.nodes ?? [LINE]).map((n) => ({ ...n }));
  let nextID = 100;

  // What the tests steer.
  const control = {
    /** CreatePin fails this many more times before it works. */
    failCreatePin: 0,
    /** When set, CreatePin waits on it — the write is "in flight". */
    holdCreatePin: null as Deferred | null,
    /** UpdatePin fails this many more times before it works. */
    failUpdatePin: 0,
    /** When set, UpdatePin waits on it (each call, until cleared). */
    holdUpdatePin: null as Deferred | null,
    generateErr: null as Error | null,
    suggestions: [] as { nodeId: string; name: string }[],
  };

  const pinOut = (p: PinRow) => {
    const n = nodes.find((x) => x.id === p.nodeId);
    return { ...p, nodeName: n?.name ?? "", nodeType: n?.nodeType ?? NodeType.NOTE, nodeGmPrivate: n?.gmPrivate ?? false };
  };

  const calls = {
    updatePin: vi.fn(),
    createPin: vi.fn(),
    deletePin: vi.fn(),
    updateMap: vi.fn(),
    replaceMapImage: vi.fn(),
    createMap: vi.fn(),
    deleteMap: vi.fn(),
    generateMapImage: vi.fn(),
    suggestMapPins: vi.fn(),
  };

  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      listMaps: () => create(ListMapsResponseSchema, { maps }),
      listNodes: () => create(ListNodesResponseSchema, { nodes }),
      getMapView: (req) => {
        const m = maps.find((x) => x.id === req.id);
        if (!m) throw new ConnectError("map not found", Code.NotFound);
        const here = pins.filter((p) => p.mapId === m.id);
        return create(GetMapViewResponseSchema, {
          map: m,
          pins: here.map(pinOut),
          breadcrumb: [],
          children: maps.filter((x) => x.parentMapId === m.id),
          unpinned: nodes.filter((n) => !here.some((p) => p.nodeId === n.id)),
        });
      },
      updatePin: async (req) => {
        calls.updatePin({ id: req.id, x: req.x, y: req.y, labelOverride: req.labelOverride, gmPrivate: req.gmPrivate });
        if (control.holdUpdatePin) await control.holdUpdatePin.promise;
        if (control.failUpdatePin > 0) {
          control.failUpdatePin--;
          throw new ConnectError("the pin is locked", Code.FailedPrecondition);
        }
        const p = pins.find((x) => x.id === req.id);
        if (!p) throw new ConnectError("pin not found", Code.NotFound);
        Object.assign(p, { x: req.x, y: req.y, labelOverride: req.labelOverride, gmPrivate: req.gmPrivate });
        return create(UpdatePinResponseSchema, { pin: pinOut(p) });
      },
      createPin: async (req) => {
        calls.createPin({
          mapId: req.mapId,
          nodeId: req.nodeId,
          x: req.x,
          y: req.y,
          labelOverride: req.labelOverride,
          gmPrivate: req.gmPrivate,
        });
        if (control.holdCreatePin) await control.holdCreatePin.promise;
        if (control.failCreatePin > 0) {
          control.failCreatePin--;
          throw new ConnectError("the server is having a moment", Code.Unavailable);
        }
        const p: PinRow = {
          id: `p${nextID++}`,
          mapId: req.mapId,
          nodeId: req.nodeId,
          x: req.x,
          y: req.y,
          labelOverride: req.labelOverride,
          gmPrivate: req.gmPrivate,
        };
        pins.push(p);
        return create(CreatePinResponseSchema, { pin: pinOut(p) });
      },
      deletePin: (req) => {
        calls.deletePin({ id: req.id });
        const i = pins.findIndex((x) => x.id === req.id);
        if (i >= 0) pins.splice(i, 1);
        return create(DeletePinResponseSchema, {});
      },
      updateMap: (req) => {
        calls.updateMap({
          id: req.id,
          name: req.name,
          parentMapId: req.parentMapId,
          anchorNodeId: req.anchorNodeId,
          gmPrivate: req.gmPrivate,
        });
        const m = maps.find((x) => x.id === req.id);
        if (!m) throw new ConnectError("map not found", Code.NotFound);
        Object.assign(m, {
          name: req.name,
          parentMapId: req.parentMapId,
          anchorNodeId: req.anchorNodeId,
          gmPrivate: req.gmPrivate,
        });
        return create(UpdateMapResponseSchema, { map: m });
      },
      replaceMapImage: (req) => {
        calls.replaceMapImage({
          id: req.id,
          imageBytes: Array.from(req.imageBytes),
          contentType: req.contentType,
          widthPx: req.widthPx,
          heightPx: req.heightPx,
        });
        const m = maps.find((x) => x.id === req.id);
        if (!m) throw new ConnectError("map not found", Code.NotFound);
        Object.assign(m, { hasImage: true, widthPx: req.widthPx, heightPx: req.heightPx });
        return create(ReplaceMapImageResponseSchema, { map: m });
      },
      createMap: (req) => {
        calls.createMap({
          name: req.name,
          imageBytes: Array.from(req.imageBytes),
          contentType: req.contentType,
          parentMapId: req.parentMapId,
          anchorNodeId: req.anchorNodeId,
          gmPrivate: req.gmPrivate,
        });
        const m = map({
          id: `m${nextID++}`,
          name: req.name,
          parentMapId: req.parentMapId,
          anchorNodeId: req.anchorNodeId,
          gmPrivate: req.gmPrivate,
        });
        maps.push(m);
        return create(CreateMapResponseSchema, { map: m });
      },
      deleteMap: (req) => {
        calls.deleteMap({ id: req.id });
        const i = maps.findIndex((x) => x.id === req.id);
        if (i >= 0) maps.splice(i, 1);
        return create(DeleteMapResponseSchema, {});
      },
      generateMapImage: (req) => {
        calls.generateMapImage({ prompt: req.prompt, anchorNodeId: req.anchorNodeId });
        if (control.generateErr) throw control.generateErr;
        return create(GenerateMapImageResponseSchema, {
          imageBytes: DRAFT_BYTES,
          contentType: "image/png",
          model: "gemini-2.5-flash-image",
          prompt: `composed: ${req.prompt}`,
        });
      },
      suggestMapPins: (req) => {
        calls.suggestMapPins({ mapId: req.mapId });
        return create(SuggestMapPinsResponseSchema, { suggestions: control.suggestions });
      },
    });
  });

  return { transport, calls, control, maps, pins, nodes };
}

type World = ReturnType<typeof world>;

function mount(w: World = world(), opts: { onOpenNode?: (id: string) => void } = {}) {
  render(
    <Providers transport={w.transport} queryClient={makeQueryClient()}>
      <MapsPanel onOpenNode={opts.onOpenNode} />
      <Toaster />
    </Providers>,
  );
  return w;
}

const BOX = { left: 0, top: 0, width: 1000, height: 600, right: 1000, bottom: 600, x: 0, y: 0 } as DOMRect;

async function theSurface(name = "Saltmarsh") {
  const surface = await screen.findByRole("application", { name: `${name} map` });
  // jsdom reports a zero-size rect for everything, and the panel's normalize()
  // returns null on a zero-width surface — so without a real box EVERY gesture
  // becomes a no-op and every assertion below passes for the wrong reason.
  surface.getBoundingClientRect = () => BOX;
  return surface;
}

const theDot = (name = "The Snapping Line") =>
  screen.getByRole("button", { name: new RegExp(`^${name} \\(`) });

/** The pin's on-map position, as the percentages the layer is drawn with. */
const placeOf = (dot: HTMLElement) => {
  const pin = dot.closest(".gx-maps__pin") as HTMLElement;
  return { left: pin.style.left, top: pin.style.top };
};

// Radix opens a dropdown menu from the keyboard on Enter.
function openMapMenu() {
  const trigger = screen.getByRole("button", { name: "More map actions" });
  act(() => trigger.focus());
  fireEvent.keyDown(trigger, { key: "Enter" });
}

const DRAFT_BYTES = new Uint8Array([0x89, 0x50, 0x4e, 0x47]);

function pngFile(name: string, bytes = [1, 2, 3, 4], type = "image/png") {
  const file = new File([new Uint8Array(bytes)], name, { type });
  // jsdom's File has no arrayBuffer(); the upload path needs one to read bytes.
  Object.defineProperty(file, "arrayBuffer", { value: async () => new Uint8Array(bytes).buffer });
  return file;
}

// jsdom has no image decoder, so `new Image()` never fires onload and every
// path that measures pixel dimensions client-side would hang forever. Stub a
// decoder that reports a fixed size: what is under test is the flow, not the
// browser's PNG parser.
beforeEach(() => {
  class StubImage {
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    naturalWidth = 1600;
    naturalHeight = 900;
    set src(_v: string) {
      queueMicrotask(() => this.onload?.());
    }
  }
  vi.stubGlobal("Image", StubImage);
  // Only the two methods: replacing the whole URL global would break every other
  // use of it (it is a constructor, not a bag of functions).
  vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:draft");
  vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
});
afterEach(() => {
  act(() => {
    toast.dismiss();
  });
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

// ── Pointer gestures ─────────────────────────────────────────────────────────

describe("MapsPanel pin gestures (pointer events)", () => {
  it("a drag past the slop threshold moves the pin, and it stays where it was dropped", async () => {
    const { calls } = mount();
    const surface = await theSurface();
    const dot = theDot();

    fireEvent.pointerDown(dot, { pointerId: 1, clientX: 100, clientY: 100 });
    fireEvent.pointerMove(surface, { pointerId: 1, clientX: 300, clientY: 240 });
    // Mid-drag the glyph follows the pointer.
    expect(placeOf(dot)).toEqual({ left: "30%", top: "40%" });
    fireEvent.pointerUp(surface, { pointerId: 1, clientX: 300, clientY: 240 });

    await waitFor(() => expect(calls.updatePin).toHaveBeenCalledTimes(1));
    // 300/1000 and 240/600 in the stubbed box — the write carries where the
    // pointer ended, normalized, and keeps the pin's label and privacy.
    const req = calls.updatePin.mock.calls[0][0];
    expect(req.x).toBeCloseTo(0.3);
    expect(req.y).toBeCloseTo(0.4);
    expect(req).toMatchObject({ id: "p1", labelOverride: "", gmPrivate: false });
    // It does not snap back to the stored position while the refetch is out.
    expect(placeOf(theDot())).toEqual({ left: "30%", top: "40%" });
    // …and the click the browser synthesises after a drag does not open the card.
    fireEvent.click(dot);
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("works for a finger: a touch drag takes pointer capture and moves the pin", async () => {
    const { calls } = mount();
    const surface = await theSurface();
    const dot = theDot();
    const capture = vi.spyOn(dot, "setPointerCapture");

    fireEvent.pointerDown(dot, { pointerId: 7, pointerType: "touch", clientX: 250, clientY: 150 });
    expect(capture).toHaveBeenCalledWith(7);
    // A second finger elsewhere on the map is not this drag.
    fireEvent.pointerMove(surface, { pointerId: 8, pointerType: "touch", clientX: 900, clientY: 500 });
    expect(placeOf(dot)).toEqual({ left: "25%", top: "25%" });
    fireEvent.pointerMove(surface, { pointerId: 7, pointerType: "touch", clientX: 600, clientY: 300 });
    fireEvent.pointerUp(surface, { pointerId: 7, pointerType: "touch", clientX: 600, clientY: 300 });

    await waitFor(() => expect(calls.updatePin).toHaveBeenCalledTimes(1));
    expect(calls.updatePin.mock.calls[0][0].x).toBeCloseTo(0.6);
    expect(calls.updatePin.mock.calls[0][0].y).toBeCloseTo(0.5);
  });

  it("still drags when pointer capture is refused", async () => {
    const { calls } = mount();
    const surface = await theSurface();
    const dot = theDot();
    // What a browser does for a pointer id that is already gone.
    vi.spyOn(dot, "setPointerCapture").mockImplementation(() => {
      throw new DOMException("no such pointer", "NotFoundError");
    });

    fireEvent.pointerDown(dot, { pointerId: 1, clientX: 100, clientY: 100 });
    fireEvent.pointerMove(surface, { pointerId: 1, clientX: 400, clientY: 300 });
    fireEvent.pointerUp(surface, { pointerId: 1, clientX: 400, clientY: 300 });

    await waitFor(() => expect(calls.updatePin).toHaveBeenCalledTimes(1));
  });

  it("a cancelled gesture puts the pin back and writes nothing", async () => {
    const { calls } = mount();
    const surface = await theSurface();
    const dot = theDot();

    fireEvent.pointerDown(dot, { pointerId: 1, pointerType: "touch", clientX: 250, clientY: 150 });
    fireEvent.pointerMove(surface, { pointerId: 1, pointerType: "touch", clientX: 600, clientY: 300 });
    expect(placeOf(dot)).toEqual({ left: "60%", top: "50%" });
    // The browser took the gesture over (a scroll, a system swipe).
    fireEvent.pointerCancel(surface, { pointerId: 1, pointerType: "touch" });

    expect(placeOf(dot)).toEqual({ left: "25%", top: "25%" });
    expect(calls.updatePin).not.toHaveBeenCalled();
  });

  it("a plain click on a pin opens its card and never moves it", async () => {
    const onOpenNode = vi.fn();
    const { calls } = mount(world(), { onOpenNode });
    const surface = await theSurface();
    const dot = theDot();

    // Press and release at the SAME point — an ordinary click.
    fireEvent.pointerDown(dot, { pointerId: 1, clientX: 100, clientY: 100 });
    fireEvent.pointerUp(surface, { pointerId: 1, clientX: 100, clientY: 100 });
    fireEvent.click(dot);

    // The card says what the pin IS, without leaving the map: name, type and
    // the start of the entry's text (markdown flattened).
    const card = await screen.findByRole("dialog", { name: "Pin: The Snapping Line" });
    expect(within(card).getByText("Location")).toBeInTheDocument();
    expect(within(card).getByText(/A fishing boat with a patched sail/)).toBeInTheDocument();
    expect(onOpenNode).not.toHaveBeenCalled();
    // The bug this pins: the press armed a drag, so the release wrote the pin to
    // wherever the pointer happened to be. Clicking a pin walked it across the
    // map, one write RPC per click.
    expect(calls.updatePin).not.toHaveBeenCalled();

    // Leaving for the entry is one explicit button.
    fireEvent.click(within(card).getByRole("button", { name: "Open entry" }));
    expect(onOpenNode).toHaveBeenCalledWith("n1");
  });

  it("hand tremor below the threshold does not become a drag", async () => {
    const { calls } = mount();
    const surface = await theSurface();
    const dot = theDot();

    // Two pixels of tremor, then release: not a drag.
    fireEvent.pointerDown(dot, { pointerId: 1, clientX: 100, clientY: 100 });
    fireEvent.pointerMove(surface, { pointerId: 1, clientX: 101, clientY: 102 });
    fireEvent.pointerUp(surface, { pointerId: 1, clientX: 101, clientY: 102 });

    // Then a REAL drag, so the assertion has a positive event to wait for rather
    // than proving nothing happened by not waiting long enough.
    fireEvent.pointerDown(dot, { pointerId: 1, clientX: 100, clientY: 100 });
    fireEvent.pointerMove(surface, { pointerId: 1, clientX: 500, clientY: 300 });
    fireEvent.pointerUp(surface, { pointerId: 1, clientX: 500, clientY: 300 });

    await waitFor(() => expect(calls.updatePin).toHaveBeenCalled());
    expect(calls.updatePin).toHaveBeenCalledTimes(1);
    expect(calls.updatePin.mock.calls[0][0].x).toBeCloseTo(0.5);
  });

  it("there is no always-visible remove button on the map any more", async () => {
    mount();
    await theSurface();
    expect(screen.queryByRole("button", { name: /Remove .* from this map/ })).toBeNull();
  });
});

// ── Keyboard ─────────────────────────────────────────────────────────────────

describe("MapsPanel pins from the keyboard", () => {
  it("arrow keys move a focused pin, Shift takes bigger steps, Enter commits one write", async () => {
    const { calls } = mount();
    await theSurface();
    const dot = theDot();
    act(() => dot.focus());

    fireEvent.keyDown(dot, { key: "ArrowRight" });
    fireEvent.keyDown(dot, { key: "ArrowRight" });
    fireEvent.keyDown(dot, { key: "ArrowDown", shiftKey: true });
    // Shown at the new place, and said out loud — but nothing written yet.
    expect(placeOf(dot).left).toBe("27%");
    expect(parseFloat(placeOf(dot).top)).toBeCloseTo(30);
    expect(screen.getByText(/Moving “The Snapping Line” with the arrow keys/)).toBeInTheDocument();
    expect(calls.updatePin).not.toHaveBeenCalled();

    fireEvent.keyDown(dot, { key: "Enter" });
    await waitFor(() => expect(calls.updatePin).toHaveBeenCalledTimes(1));
    const req = calls.updatePin.mock.calls[0][0];
    expect(req.x).toBeCloseTo(0.27);
    expect(req.y).toBeCloseTo(0.3);
    // Enter committed the move; it did not also open the card.
    expect(screen.queryByRole("dialog")).toBeNull();
    await waitFor(() => expect(screen.queryByText(/with the arrow keys/)).toBeNull());
  });

  it("Escape reverts a keyboard move and writes nothing", async () => {
    const { calls } = mount();
    await theSurface();
    const dot = theDot();
    act(() => dot.focus());

    fireEvent.keyDown(dot, { key: "ArrowLeft", shiftKey: true });
    expect(placeOf(dot).left).toBe("20%");
    fireEvent.keyDown(dot, { key: "Escape" });

    expect(placeOf(dot)).toEqual({ left: "25%", top: "25%" });
    expect(calls.updatePin).not.toHaveBeenCalled();
  });

  it("clamps a keyboard move to the edge of the map", async () => {
    const w = world({ pins: [{ ...PIN, x: 0.02, y: 0.5 }] });
    mount(w);
    await theSurface();
    const dot = theDot();
    fireEvent.keyDown(dot, { key: "ArrowLeft", shiftKey: true });
    expect(placeOf(dot).left).toBe("0%");
  });

  it("Enter on a pin that has not been moved opens its card", async () => {
    mount();
    await theSurface();
    const dot = theDot();
    // Enter and Space on a button are a click.
    fireEvent.keyDown(dot, { key: "Enter" });
    fireEvent.click(dot);
    expect(await screen.findByRole("dialog", { name: "Pin: The Snapping Line" })).toBeInTheDocument();
  });
});

// ── The pin card ─────────────────────────────────────────────────────────────

describe("MapsPanel pin card", () => {
  async function openCard() {
    await theSurface();
    fireEvent.click(theDot());
    return screen.findByRole("dialog", { name: /^Pin: / });
  }

  it("relabels the pin on this map through UpdatePin, keeping its position", async () => {
    const { calls } = mount();
    const card = await openCard();

    const save = within(card).getByRole("button", { name: "Save label" });
    // Nothing to save yet — but still focusable (aria-disabled, not disabled).
    expect(save).toHaveAttribute("aria-disabled", "true");
    expect(save).not.toBeDisabled();
    fireEvent.change(within(card).getByLabelText("Label on this map"), { target: { value: "  East pier  " } });
    fireEvent.click(save);

    await waitFor(() => expect(calls.updatePin).toHaveBeenCalledTimes(1));
    expect(calls.updatePin.mock.calls[0][0]).toEqual({
      id: "p1",
      x: 0.25,
      y: 0.25,
      labelOverride: "East pier",
      gmPrivate: false,
    });
    // The map now names the pin by its label; the card still names the entry.
    expect(await screen.findByRole("button", { name: /^East pier \(/ })).toBeInTheDocument();
  });

  it("hides the pin from players through UpdatePin, keeping its position and label", async () => {
    const w = world({ pins: [{ ...PIN, labelOverride: "East pier" }] });
    mount(w);
    await theSurface();
    fireEvent.click(theDot("East pier"));
    const card = await screen.findByRole("dialog", { name: "Pin: East pier" });

    fireEvent.click(within(card).getByRole("switch", { name: "Hide from players" }));

    await waitFor(() => expect(w.calls.updatePin).toHaveBeenCalledTimes(1));
    expect(w.calls.updatePin.mock.calls[0][0]).toEqual({
      id: "p1",
      x: 0.25,
      y: 0.25,
      labelOverride: "East pier",
      gmPrivate: true,
    });
    expect(await screen.findByRole("button", { name: /hidden from players/ })).toBeInTheDocument();
  });

  it("says why a pin of a GM-only entry cannot be shown", async () => {
    const w = world({ nodes: [{ ...LINE, gmPrivate: true }] });
    mount(w);
    await theSurface();
    fireEvent.click(theDot());
    const card = await screen.findByRole("dialog", { name: /^Pin: / });
    const toggle = within(card).getByRole("switch", { name: "Hide from players" });
    expect(toggle).toBeChecked();
    expect(toggle).toBeDisabled();
    expect(within(card).getByText(/The entry itself is GM-only/)).toBeInTheDocument();
  });

  it("removing a pin offers Undo, which puts it back where it was — label and privacy included", async () => {
    const w = world({ pins: [{ ...PIN, x: 0.4, y: 0.7, labelOverride: "East pier", gmPrivate: true }] });
    mount(w);
    await theSurface();
    fireEvent.click(theDot("East pier"));
    const card = await screen.findByRole("dialog", { name: "Pin: East pier" });

    fireEvent.click(within(card).getByRole("button", { name: "Remove from map" }));
    await waitFor(() => expect(w.calls.deletePin).toHaveBeenCalledWith({ id: "p1" }));
    // Gone from the map, back in the tray.
    await waitFor(() => expect(screen.queryByRole("button", { name: /^East pier \(/ })).toBeNull());
    expect(await screen.findByRole("button", { name: "The Snapping Line", pressed: false })).toBeInTheDocument();

    expect(await screen.findByText("Removed “East pier” from the map")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Undo" }));

    await waitFor(() => expect(w.calls.createPin).toHaveBeenCalledTimes(1));
    expect(w.calls.createPin.mock.calls[0][0]).toEqual({
      mapId: "m1",
      nodeId: "n1",
      x: 0.4,
      y: 0.7,
      labelOverride: "East pier",
      gmPrivate: true,
    });
    const back = await screen.findByRole("button", { name: /^East pier \(/ });
    expect(placeOf(back)).toEqual({ left: "40%", top: "70%" });
  });

  it("keeps keyboard focus inside the card after a label is saved, and after a refusal", async () => {
    const w = mount();
    const card = await openCard();
    const field = within(card).getByLabelText("Label on this map");
    const save = within(card).getByRole("button", { name: "Save label" });

    fireEvent.change(field, { target: { value: "East pier" } });
    act(() => save.focus());
    fireEvent.click(save);
    await waitFor(() => expect(w.calls.updatePin).toHaveBeenCalledTimes(1));
    expect(await screen.findByRole("button", { name: /^East pier \(/ })).toBeInTheDocument();
    // The read-back used to remount the whole card (a `key` on the label),
    // taking the focused button with it while the card stayed open.
    const after = screen.getByRole("dialog", { name: /^Pin: / });
    expect(after).toContainElement(document.activeElement as HTMLElement);
    expect(within(after).getByLabelText("Label on this map")).toHaveValue("East pier");

    // A refused save: the card stays, the draft stays, focus stays.
    w.control.failUpdatePin = 1;
    const field2 = within(after).getByLabelText("Label on this map");
    fireEvent.change(field2, { target: { value: "West pier" } });
    act(() => field2.focus());
    fireEvent.submit(field2.closest("form") as HTMLFormElement);
    expect(await screen.findByRole("alert")).toHaveTextContent("the pin is locked");
    expect(screen.getByRole("dialog", { name: /^Pin: / })).toContainElement(document.activeElement as HTMLElement);
    expect(field2).toHaveValue("West pier");
  });

  it("a privacy change made while a move is still out keeps the move", async () => {
    const w = world();
    w.control.holdUpdatePin = deferred();
    mount(w);
    const surface = await theSurface();
    const dot = theDot();

    fireEvent.pointerDown(dot, { pointerId: 1, clientX: 250, clientY: 150 });
    fireEvent.pointerMove(surface, { pointerId: 1, clientX: 600, clientY: 300 });
    fireEvent.pointerUp(surface, { pointerId: 1, clientX: 600, clientY: 300 });
    await waitFor(() => expect(w.calls.updatePin).toHaveBeenCalledTimes(1));

    // The move is still out when the GM taps the pin open and hides it.
    fireEvent.pointerDown(theDot(), { pointerId: 2, clientX: 600, clientY: 300 });
    fireEvent.pointerUp(surface, { pointerId: 2, clientX: 600, clientY: 300 });
    fireEvent.click(theDot());
    const card = await screen.findByRole("dialog", { name: /^Pin: / });
    fireEvent.click(within(card).getByRole("switch", { name: "Hide from players" }));
    // Queued behind the move, not racing it.
    expect(w.calls.updatePin).toHaveBeenCalledTimes(1);

    const hold = w.control.holdUpdatePin;
    w.control.holdUpdatePin = null;
    await act(async () => hold.resolve());
    await waitFor(() => expect(w.calls.updatePin).toHaveBeenCalledTimes(2));
    // UpdatePin saves every field: the toggle carries the NEW position, so it
    // does not put the pin back where it was.
    const second = w.calls.updatePin.mock.calls[1][0];
    expect(second).toMatchObject({ id: "p1", gmPrivate: true });
    expect(second.x).toBeCloseTo(0.6);
    expect(second.y).toBeCloseTo(0.5);
    await waitFor(() => expect(w.pins[0]).toMatchObject({ gmPrivate: true }));
    expect(w.pins[0].x).toBeCloseTo(0.6);
  });

  it("a move made while a label save is still out keeps the label; a refused write is not replayed", async () => {
    const w = world();
    w.control.holdUpdatePin = deferred();
    w.control.failUpdatePin = 1;
    mount(w);
    const surface = await theSurface();
    fireEvent.click(theDot());
    const card = await screen.findByRole("dialog", { name: /^Pin: / });
    fireEvent.change(within(card).getByLabelText("Label on this map"), { target: { value: "East pier" } });
    fireEvent.click(within(card).getByRole("button", { name: "Save label" }));
    await waitFor(() => expect(w.calls.updatePin).toHaveBeenCalledTimes(1));

    // While the label save is out (and about to be refused), drag the pin.
    fireEvent.pointerDown(theDot(), { pointerId: 1, clientX: 250, clientY: 150 });
    fireEvent.pointerMove(surface, { pointerId: 1, clientX: 600, clientY: 300 });
    fireEvent.pointerUp(surface, { pointerId: 1, clientX: 600, clientY: 300 });
    expect(w.calls.updatePin).toHaveBeenCalledTimes(1);

    const hold = w.control.holdUpdatePin;
    w.control.holdUpdatePin = null;
    await act(async () => hold.resolve());
    await waitFor(() => expect(w.calls.updatePin).toHaveBeenCalledTimes(2));
    // The refused label is NOT carried by the move queued behind it.
    const second = w.calls.updatePin.mock.calls[1][0];
    expect(second).toMatchObject({ id: "p1", labelOverride: "", gmPrivate: false });
    expect(second.x).toBeCloseTo(0.6);

    // And an ACCEPTED label is carried by a move queued behind it.
    w.control.holdUpdatePin = deferred();
    // The drag closed the card; a plain tap (press, release, click) reopens it.
    fireEvent.pointerDown(theDot(), { pointerId: 3, clientX: 600, clientY: 300 });
    fireEvent.pointerUp(surface, { pointerId: 3, clientX: 600, clientY: 300 });
    fireEvent.click(theDot());
    const card2 = await screen.findByRole("dialog", { name: /^Pin: / });
    fireEvent.change(within(card2).getByLabelText("Label on this map"), { target: { value: "East pier" } });
    fireEvent.click(within(card2).getByRole("button", { name: "Save label" }));
    await waitFor(() => expect(w.calls.updatePin).toHaveBeenCalledTimes(3));
    fireEvent.pointerDown(theDot(), { pointerId: 2, clientX: 600, clientY: 300 });
    fireEvent.pointerMove(surface, { pointerId: 2, clientX: 800, clientY: 450 });
    fireEvent.pointerUp(surface, { pointerId: 2, clientX: 800, clientY: 450 });
    const hold2 = w.control.holdUpdatePin;
    w.control.holdUpdatePin = null;
    await act(async () => hold2.resolve());
    await waitFor(() => expect(w.calls.updatePin).toHaveBeenCalledTimes(4));
    const fourth = w.calls.updatePin.mock.calls[3][0];
    expect(fourth).toMatchObject({ labelOverride: "East pier" });
    expect(fourth.x).toBeCloseTo(0.8);
  });
});

// ── Placing ──────────────────────────────────────────────────────────────────

const INN = node({ id: "n-inn", name: "The Rusty Anchor" });
const GUILD = node({ id: "n-guild", name: "Fishers' Guild", nodeType: NodeType.FACTION });
const OSKAR = node({ id: "n-oskar", name: "Oskar", nodeType: NodeType.NPC });

describe("MapsPanel placing from the tray", () => {
  it("arming a tray entry marks the surface as placing (crosshair cursor) until disarmed", async () => {
    mount(world({ pins: [], nodes: [INN] }));
    const surface = await theSurface();
    expect(surface).not.toHaveAttribute("data-placing");

    fireEvent.click(await screen.findByRole("button", { name: "The Rusty Anchor", pressed: false }));
    expect(surface).toHaveAttribute("data-placing");
    fireEvent.click(screen.getByRole("button", { name: "The Rusty Anchor", pressed: true }));
    expect(surface).not.toHaveAttribute("data-placing");
  });

  it("a click on the map places the armed entry there, with a provisional pin while the write is out", async () => {
    const w = world({ pins: [], nodes: [INN] });
    w.control.holdCreatePin = deferred();
    mount(w);
    const surface = await theSurface();
    fireEvent.click(await screen.findByRole("button", { name: "The Rusty Anchor", pressed: false }));

    fireEvent.click(surface, { clientX: 700, clientY: 150 });
    await waitFor(() => expect(w.calls.createPin).toHaveBeenCalledTimes(1));
    expect(w.calls.createPin.mock.calls[0][0]).toMatchObject({ mapId: "m1", nodeId: "n-inn" });
    expect(w.calls.createPin.mock.calls[0][0].x).toBeCloseTo(0.7);
    expect(w.calls.createPin.mock.calls[0][0].y).toBeCloseTo(0.25);

    // In flight: the click visibly landed, and the entry is still armed.
    const ghost = document.querySelector<HTMLElement>("[data-provisional]");
    expect(ghost).not.toBeNull();
    expect(ghost).toHaveTextContent("The Rusty Anchor");
    expect(ghost?.style.left).toBe("70%");
    expect(surface).toHaveAttribute("data-placing");
    // A second click while the first is out is not a second pin.
    fireEvent.click(surface, { clientX: 100, clientY: 100 });
    expect(w.calls.createPin).toHaveBeenCalledTimes(1);

    await act(async () => w.control.holdCreatePin?.resolve());
    // Landed: a real pin, no ghost, and the entry is disarmed.
    expect(await screen.findByRole("button", { name: /^The Rusty Anchor \(/ })).toBeInTheDocument();
    await waitFor(() => expect(document.querySelector("[data-provisional]")).toBeNull());
    expect(surface).not.toHaveAttribute("data-placing");
  });

  it("a failed CreatePin keeps the entry armed, and the error clears on the next success", async () => {
    const w = world({ pins: [], nodes: [INN] });
    w.control.failCreatePin = 1;
    mount(w);
    const surface = await theSurface();
    fireEvent.click(await screen.findByRole("button", { name: "The Rusty Anchor", pressed: false }));

    fireEvent.click(surface, { clientX: 500, clientY: 300 });
    expect(await screen.findByRole("alert")).toHaveTextContent("the server is having a moment");
    // Still armed: the GM does not have to find the entry in the tray again.
    expect(surface).toHaveAttribute("data-placing");
    expect(screen.getByRole("button", { name: "The Rusty Anchor", pressed: true })).toBeInTheDocument();
    expect(document.querySelector("[data-provisional]")).toBeNull();

    fireEvent.click(surface, { clientX: 500, clientY: 300 });
    expect(await screen.findByRole("button", { name: /^The Rusty Anchor \(/ })).toBeInTheDocument();
    expect(w.calls.createPin).toHaveBeenCalledTimes(2);
    await waitFor(() => expect(screen.queryByRole("alert")).toBeNull());
    expect(surface).not.toHaveAttribute("data-placing");
  });

  it("places without a mouse: 'Place at centre', then the new pin has focus for the arrow keys", async () => {
    const w = world({ pins: [], nodes: [INN] });
    mount(w);
    const surface = await theSurface();
    // "Centre" is the centre of what is VISIBLE: a frame 500×300 looking at the
    // top-left quarter of the (zoomed) 1000×600 surface.
    const frame = surface.parentElement as HTMLElement;
    frame.getBoundingClientRect = () => ({ ...BOX, width: 500, height: 300, right: 500, bottom: 300 }) as DOMRect;
    Object.defineProperty(frame, "clientWidth", { value: 500 });
    Object.defineProperty(frame, "clientHeight", { value: 300 });
    fireEvent.click(await screen.findByRole("button", { name: "The Rusty Anchor", pressed: false }));

    fireEvent.click(screen.getByRole("button", { name: "Place at centre" }));

    await waitFor(() => expect(w.calls.createPin).toHaveBeenCalledTimes(1));
    expect(w.calls.createPin.mock.calls[0][0]).toMatchObject({ nodeId: "n-inn", x: 0.25, y: 0.25 });
    const dot = await screen.findByRole("button", { name: /^The Rusty Anchor \(/ });
    await waitFor(() => expect(dot).toHaveFocus());
  });

  it("'Place at centre' keeps keyboard focus while the write is out, and after a refusal", async () => {
    const w = world({ pins: [], nodes: [INN] });
    w.control.holdCreatePin = deferred();
    w.control.failCreatePin = 1;
    mount(w);
    await theSurface();
    fireEvent.click(await screen.findByRole("button", { name: "The Rusty Anchor", pressed: false }));

    const button = screen.getByRole("button", { name: "Place at centre" });
    button.focus();
    fireEvent.click(button);
    await waitFor(() => expect(w.calls.createPin).toHaveBeenCalledTimes(1));
    // Busy, but NOT natively disabled: a browser moves focus off a disabled
    // button to <body>, and a keyboard GM would lose their place mid-retry.
    expect(button).not.toBeDisabled();
    expect(button).toHaveAttribute("aria-busy", "true");
    expect(button).toHaveAttribute("aria-disabled", "true");
    // A second press while the first is out asks for nothing.
    fireEvent.click(button);
    expect(w.calls.createPin).toHaveBeenCalledTimes(1);

    await act(async () => w.control.holdCreatePin?.resolve());
    expect(await screen.findByRole("alert")).toHaveTextContent("the server is having a moment");
    // Refused: still armed, the same button is there and still focused, ready to retry.
    expect(button).toBeInTheDocument();
    expect(button).toHaveFocus();
    expect(button).not.toHaveAttribute("aria-busy");
  });

  it("a tap places after a touch drag, and the click that follows a drag never places", async () => {
    const w = world({ nodes: [LINE, INN] });
    mount(w);
    const surface = await theSurface();
    fireEvent.click(await screen.findByRole("button", { name: "The Rusty Anchor", pressed: false }));

    // Drag the existing pin while an entry is armed. Some browsers send the
    // click that follows to the surface rather than the pin: not a placement.
    fireEvent.pointerDown(theDot(), { pointerId: 1, clientX: 250, clientY: 150 });
    fireEvent.pointerMove(surface, { pointerId: 1, clientX: 600, clientY: 300 });
    fireEvent.pointerUp(surface, { pointerId: 1, clientX: 600, clientY: 300 });
    fireEvent.click(surface, { clientX: 600, clientY: 300 });
    await waitFor(() => expect(w.calls.updatePin).toHaveBeenCalledTimes(1));
    expect(w.calls.createPin).not.toHaveBeenCalled();

    // A finger drag is followed by no click at all; the next tap must still place.
    fireEvent.pointerDown(theDot(), { pointerId: 2, pointerType: "touch", clientX: 600, clientY: 300 });
    fireEvent.pointerMove(surface, { pointerId: 2, pointerType: "touch", clientX: 100, clientY: 100 });
    fireEvent.pointerUp(surface, { pointerId: 2, pointerType: "touch", clientX: 100, clientY: 100 });
    await waitFor(() => expect(w.calls.updatePin).toHaveBeenCalledTimes(2));

    fireEvent.pointerDown(surface, { pointerId: 3, pointerType: "touch", clientX: 800, clientY: 450 });
    fireEvent.pointerUp(surface, { pointerId: 3, pointerType: "touch", clientX: 800, clientY: 450 });
    fireEvent.click(surface, { clientX: 800, clientY: 450 });
    await waitFor(() => expect(w.calls.createPin).toHaveBeenCalledTimes(1));
    expect(w.calls.createPin.mock.calls[0][0]).toMatchObject({ nodeId: "n-inn", x: 0.8, y: 0.75 });
  });

  it("Escape disarms the entry", async () => {
    mount(world({ pins: [], nodes: [INN] }));
    const surface = await theSurface();
    fireEvent.click(await screen.findByRole("button", { name: "The Rusty Anchor", pressed: false }));
    expect(surface).toHaveAttribute("data-placing");

    fireEvent.keyDown(document.body, { key: "Escape" });
    expect(surface).not.toHaveAttribute("data-placing");
  });

  it("clicks inside an open pin card never place the armed entry", async () => {
    const w = world({ nodes: [LINE, INN] });
    mount(w);
    await theSurface();
    fireEvent.click(await screen.findByRole("button", { name: "The Rusty Anchor", pressed: false }), { detail: 1 });
    fireEvent.click(theDot(), { clientX: 250, clientY: 150 });
    const card = await screen.findByRole("dialog", { name: /^Pin: / });

    // The card is portalled to <body>, but React bubbles its clicks through the
    // map surface. A pointer click lands wherever the card sits over the map…
    fireEvent.click(within(card).getByRole("switch", { name: "Hide from players" }), { clientX: 400, clientY: 200 });
    await waitFor(() => expect(w.calls.updatePin).toHaveBeenCalledTimes(1));
    // …and a keyboard press has no coordinates at all (the map's top-left corner).
    fireEvent.change(within(card).getByLabelText("Label on this map"), { target: { value: "East pier" } });
    fireEvent.click(within(card).getByRole("button", { name: "Save label" }), { clientX: 0, clientY: 0 });
    await waitFor(() => expect(w.calls.updatePin).toHaveBeenCalledTimes(2));

    expect(w.calls.createPin).not.toHaveBeenCalled();
    // Still armed: the card's controls did not use up the placement either.
    expect(screen.getByRole("button", { name: "The Rusty Anchor", pressed: true })).toBeInTheDocument();
  });

  it("arming an entry from the keyboard moves focus to 'Place at centre'", async () => {
    mount(world({ pins: [], nodes: [INN] }));
    await theSurface();
    const item = await screen.findByRole("button", { name: "The Rusty Anchor", pressed: false });
    act(() => item.focus());
    // Enter/Space on a button: a click with no pointer press (detail 0).
    fireEvent.click(item, { detail: 0 });
    await waitFor(() => expect(screen.getByRole("button", { name: "Place at centre" })).toHaveFocus());
  });

  it("arming an entry with the pointer leaves focus where it was", async () => {
    mount(world({ pins: [], nodes: [INN] }));
    await theSurface();
    const item = await screen.findByRole("button", { name: "The Rusty Anchor", pressed: false });
    act(() => item.focus());
    fireEvent.click(item, { detail: 1 });
    expect(await screen.findByRole("button", { name: "Place at centre" })).not.toHaveFocus();
    expect(item).toHaveFocus();
  });

  it("a click on an existing pin while placing is that pin's, not a placement under it", async () => {
    const w = world({ nodes: [LINE, INN] });
    mount(w);
    await theSurface();
    fireEvent.click(await screen.findByRole("button", { name: "The Rusty Anchor", pressed: false }));
    fireEvent.click(theDot(), { clientX: 250, clientY: 150 });
    expect(await screen.findByRole("dialog", { name: /^Pin: / })).toBeInTheDocument();
    expect(w.calls.createPin).not.toHaveBeenCalled();
  });
});

// ── Tray ─────────────────────────────────────────────────────────────────────

describe("MapsPanel tray", () => {
  const names = () =>
    within(screen.getByRole("complementary", { name: "Not on this map yet" }))
      .getAllByRole("listitem")
      .map((li) => li.textContent);

  it("filters by search and by type", async () => {
    mount(world({ pins: [], nodes: [INN, GUILD, OSKAR] }));
    await theSurface();
    await waitFor(() => expect(names()).toEqual(["Fishers' Guild", "Oskar", "The Rusty Anchor"]));

    fireEvent.change(screen.getByRole("searchbox", { name: "Search entries not on this map" }), {
      target: { value: "rusty" },
    });
    expect(names()).toEqual(["The Rusty Anchor"]);

    fireEvent.change(screen.getByRole("searchbox", { name: "Search entries not on this map" }), {
      target: { value: "" },
    });
    const types = screen.getByRole("group", { name: "Filter by type" });
    // Only the types that occur are offered.
    expect(within(types).getAllByRole("button").map((b) => b.textContent)).toEqual(["All", "NPC", "Location", "Faction"]);
    fireEvent.click(within(types).getByRole("button", { name: "Faction" }));
    expect(names()).toEqual(["Fishers' Guild"]);

    // Nothing left: say so, and offer the way out.
    fireEvent.change(screen.getByRole("searchbox", { name: "Search entries not on this map" }), {
      target: { value: "zzz" },
    });
    expect(screen.getByText("No entries match.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
    expect(names()).toHaveLength(3);
  });

  it("suggested pins mark the tray, float to the top and place nothing", async () => {
    const w = world({
      maps: [{ ...SALTMARSH, anchorNodeId: "n-town" }],
      pins: [],
      nodes: [GUILD, OSKAR, INN],
    });
    w.control.suggestions = [{ nodeId: "n-inn", name: "The Rusty Anchor" }];
    mount(w);
    await theSurface();

    fireEvent.click(await screen.findByRole("button", { name: "Suggest placements" }));
    await waitFor(() => expect(w.calls.suggestMapPins).toHaveBeenCalledWith({ mapId: "m1" }));

    // The suggestion is a HIGHLIGHT on the entry that was already in the tray…
    const chip = await screen.findByRole("button", { name: /suggested for this map/ });
    expect(chip).toHaveAttribute("data-suggested");
    // …moved to where the GM will see it…
    expect(names()[0]).toBe("The Rusty Anchor");
    // …and it arrives UNPLACED. Nothing is on the map until the GM puts it there.
    expect(screen.queryByRole("button", { name: /^The Rusty Anchor \(/ })).toBeNull();
    expect(w.calls.createPin).not.toHaveBeenCalled();
  });

  it("explains what enables 'Suggest placements' on a map that depicts no entry, and leads to the fix", async () => {
    mount(world({ pins: [], nodes: [INN] }));
    await theSurface();
    expect(screen.queryByRole("button", { name: "Suggest placements" })).toBeNull();
    expect(await screen.findByText(/Link the Location entry this map depicts/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Open Map settings" }));
    expect(await screen.findByRole("dialog", { name: "Map settings" })).toBeInTheDocument();
  });
});

// ── Zoom ─────────────────────────────────────────────────────────────────────

describe("MapsPanel zoom", () => {
  it("sizes the surface by width (never a transform) and shows a percentage", async () => {
    mount();
    const surface = await theSurface();
    expect(surface.style.width).toBe("100%");
    expect(surface.style.transform).toBe("");
    expect(screen.getByRole("status", { name: "Zoom 100%" })).toHaveTextContent("100%");

    fireEvent.click(screen.getByRole("button", { name: "Zoom in" }));
    expect(surface.style.width).toBe("140%");
    expect(surface.style.transform).toBe("");
    expect(screen.getByRole("status", { name: "Zoom 140%" })).toHaveTextContent("140%");
    // The pin is still drawn at the same normalized point, at its own size.
    expect(placeOf(theDot())).toEqual({ left: "25%", top: "25%" });

    fireEvent.click(screen.getByRole("button", { name: "Fit" }));
    expect(surface.style.width).toBe("100%");
    // Zooming out past "fit" is not a thing.
    fireEvent.click(screen.getByRole("button", { name: "Zoom out" }));
    expect(surface.style.width).toBe("100%");
  });

  it("Ctrl+wheel zooms; a plain wheel is left to scroll", async () => {
    mount();
    const surface = await theSurface();
    const frame = surface.parentElement as HTMLElement;

    const plain = fireEvent.wheel(frame, { deltaY: -100 });
    expect(plain).toBe(true); // not prevented: the browser scrolls
    expect(surface.style.width).toBe("100%");

    const zoomed = fireEvent.wheel(frame, { deltaY: -100, ctrlKey: true, clientX: 500, clientY: 300 });
    expect(zoomed).toBe(false); // prevented: the page itself must not zoom
    expect(screen.getByRole("status", { name: "Zoom 122%" })).toBeInTheDocument();

    fireEvent.wheel(frame, { deltaY: 5000, metaKey: true });
    expect(surface.style.width).toBe("100%");
  });
});

// ── Which map is open ────────────────────────────────────────────────────────

const HARBOUR = map({ id: "m2", name: "Harbour" });

describe("MapsPanel open map", () => {
  function mountWithNav(w: World, initial: Partial<ScreenLocation>) {
    const onNavigate = vi.fn();
    render(
      <Providers transport={w.transport} queryClient={makeQueryClient()}>
        <MemoryScreenNav initial={{ screen: "campaign", view: "maps", ...initial }} onNavigate={onNavigate}>
          <MapsPanel />
        </MemoryScreenNav>
      </Providers>,
    );
    return onNavigate;
  }

  it("opens the map the URL names", async () => {
    const onNavigate = mountWithNav(world({ maps: [SALTMARSH, HARBOUR], pins: [] }), { search: { map: "m2" } });
    expect(await screen.findByRole("application", { name: "Harbour map" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Harbour", pressed: true })).toBeInTheDocument();
    // Already right: nothing to correct.
    expect(onNavigate).not.toHaveBeenCalled();
  });

  it("writes the open map into the URL: a correction replaces, a choice pushes", async () => {
    const onNavigate = mountWithNav(world({ maps: [SALTMARSH, HARBOUR], pins: [] }), {});
    await screen.findByRole("application", { name: "Saltmarsh map" });
    // Arrived without ?map: the first map is shown and the URL is corrected in
    // place, so there is no extra Back step.
    await waitFor(() => expect(onNavigate).toHaveBeenCalledTimes(1));
    expect(onNavigate.mock.calls[0][0].search).toEqual({ map: "m1" });
    expect(onNavigate.mock.calls[0][1]).toEqual({ replace: true });

    fireEvent.click(screen.getByRole("button", { name: "Harbour" }));
    expect(await screen.findByRole("application", { name: "Harbour map" })).toBeInTheDocument();
    // The GM chose this one: Back returns to the map before it.
    expect(onNavigate).toHaveBeenLastCalledWith(
      expect.objectContaining({ search: { map: "m2" } }),
      { replace: false },
    );
  });

  it("falls back, in place, from a map id that does not exist", async () => {
    const onNavigate = mountWithNav(world({ maps: [SALTMARSH, HARBOUR], pins: [] }), { search: { map: "gone" } });
    expect(await screen.findByRole("application", { name: "Saltmarsh map" })).toBeInTheDocument();
    await waitFor(() =>
      expect(onNavigate).toHaveBeenLastCalledWith(expect.objectContaining({ search: { map: "m1" } }), {
        replace: true,
      }),
    );
  });

  it("without the URL, the campaign remembers the open map across a trip to the wiki and back", async () => {
    const w = world({ maps: [SALTMARSH, HARBOUR], pins: [{ ...PIN, mapId: "m2" }] });
    // The host as it is without a router: the panel unmounts when the GM opens
    // an entry (the view changes) and mounts again on the way back.
    function Host() {
      const [view, setView] = useState<"maps" | "knowledge">("maps");
      return (
        <CampaignScreenProvider
          value={{ campaign: undefined, roster: [], openNode: () => {}, openAgent: () => {}, openView: () => {} }}
        >
          {view === "maps" ? (
            <MapsPanel onOpenNode={() => setView("knowledge")} />
          ) : (
            <button type="button" onClick={() => setView("maps")}>
              Back to maps
            </button>
          )}
        </CampaignScreenProvider>
      );
    }
    render(
      <Providers transport={w.transport} queryClient={makeQueryClient()}>
        <Host />
      </Providers>,
    );

    await screen.findByRole("application", { name: "Saltmarsh map" });
    fireEvent.click(screen.getByRole("button", { name: "Harbour" }));
    await screen.findByRole("application", { name: "Harbour map" });

    // Pin → entry …
    fireEvent.click(theDot());
    fireEvent.click(await screen.findByRole("button", { name: "Open entry" }));
    expect(await screen.findByRole("button", { name: "Back to maps" })).toBeInTheDocument();
    // … and back: the same map, not the first one.
    fireEvent.click(screen.getByRole("button", { name: "Back to maps" }));
    expect(await screen.findByRole("application", { name: "Harbour map" })).toBeInTheDocument();
  });

  it("deleting the open map moves on to another one instead of an error panel", async () => {
    const w = world({ maps: [SALTMARSH, HARBOUR], pins: [] });
    mount(w);
    await theSurface();

    openMapMenu();
    fireEvent.click(await screen.findByRole("menuitem", { name: "Delete map" }));
    const confirm = await screen.findByRole("alertdialog");
    fireEvent.click(within(confirm).getByRole("button", { name: "Delete map" }));

    await waitFor(() => expect(w.calls.deleteMap).toHaveBeenCalledWith({ id: "m1" }));
    expect(await screen.findByRole("application", { name: "Harbour map" })).toBeInTheDocument();
    expect(screen.queryByText("Couldn't open this map")).toBeNull();
  });
});

// ── Map settings and image ───────────────────────────────────────────────────

const TOWN = node({ id: "n-town", name: "Saltmarsh (town)" });

describe("MapsPanel map settings", () => {
  it("renames, re-nests, anchors and hides a map from one dialog, in ONE UpdateMap call", async () => {
    const REGION = map({ id: "m-region", name: "The Dragon Coast" });
    const DOCKS = map({ id: "m-docks", name: "Docks", parentMapId: "m1" });
    const w = world({ maps: [SALTMARSH, REGION, DOCKS], pins: [], nodes: [TOWN, OSKAR] });
    mount(w);
    await theSurface();

    fireEvent.click(screen.getByRole("button", { name: "Map settings" }));
    const dialog = await screen.findByRole("dialog", { name: "Map settings" });

    const inside = within(dialog).getByLabelText("Inside") as HTMLSelectElement;
    // A map cannot go inside itself or inside one of its own children.
    expect(Array.from(inside.options).map((o) => o.textContent)).toEqual(["— Top level —", "The Dragon Coast"]);
    const depicts = within(dialog).getByLabelText("Depicts") as HTMLSelectElement;
    // Only Location entries can be depicted.
    expect(Array.from(depicts.options).map((o) => o.textContent)).toEqual(["— No entry —", "Saltmarsh (town)"]);

    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: " Saltmarsh town " } });
    fireEvent.change(inside, { target: { value: "m-region" } });
    fireEvent.change(depicts, { target: { value: "n-town" } });
    fireEvent.click(within(dialog).getByRole("switch", { name: /GM-only/ }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Save changes" }));

    await waitFor(() => expect(w.calls.updateMap).toHaveBeenCalledTimes(1));
    expect(w.calls.updateMap.mock.calls[0][0]).toEqual({
      id: "m1",
      name: "Saltmarsh town",
      parentMapId: "m-region",
      anchorNodeId: "n-town",
      gmPrivate: true,
    });
    // The dialog closes and the screen shows the saved map.
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "Map settings" })).toBeNull());
    expect(await screen.findByRole("application", { name: "Saltmarsh town map" })).toBeInTheDocument();
    expect(await screen.findByText("Map settings saved")).toBeInTheDocument();
    // Anchored now, so the tray can suggest.
    expect(await screen.findByRole("button", { name: "Suggest placements" })).toBeInTheDocument();
  });

  it("sends the fields that were NOT touched too — UpdateMap is a full-field save", async () => {
    const REGION = map({ id: "m-region", name: "The Dragon Coast" });
    const w = world({
      maps: [{ ...SALTMARSH, parentMapId: "m-region", anchorNodeId: "n-town", gmPrivate: true }, REGION],
      pins: [],
      nodes: [TOWN],
    });
    mount(w);
    await theSurface();

    fireEvent.click(screen.getByRole("button", { name: "Map settings" }));
    const dialog = await screen.findByRole("dialog", { name: "Map settings" });
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Old Saltmarsh" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Save changes" }));

    await waitFor(() => expect(w.calls.updateMap).toHaveBeenCalledTimes(1));
    expect(w.calls.updateMap.mock.calls[0][0]).toEqual({
      id: "m1",
      name: "Old Saltmarsh",
      parentMapId: "m-region",
      anchorNodeId: "n-town",
      gmPrivate: true,
    });
  });

  it("keeps the dialog open and says why when the save is refused", async () => {
    const w = world({ pins: [] });
    mount(w);
    await theSurface();
    fireEvent.click(screen.getByRole("button", { name: "Map settings" }));
    const dialog = await screen.findByRole("dialog", { name: "Map settings" });

    // An empty name cannot be saved at all.
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "   " } });
    expect(within(dialog).getByRole("button", { name: "Save changes" })).toBeDisabled();

    // The map vanished under the dialog (deleted in another tab).
    w.maps.splice(0, 1);
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "Saltmarsh" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Save changes" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent("Couldn't save: map not found");
  });

  it("replaces a map's image through ReplaceMapImage, measured client-side", async () => {
    const w = world({ pins: [] });
    mount(w);
    await theSurface();

    openMapMenu();
    expect(await screen.findByRole("menuitem", { name: "Replace image…" })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("New image file"), {
      target: { files: [pngFile("saltmarsh-v2.png", [9, 8, 7])] },
    });

    await waitFor(() => expect(w.calls.replaceMapImage).toHaveBeenCalledTimes(1));
    expect(w.calls.replaceMapImage.mock.calls[0][0]).toEqual({
      id: "m1",
      imageBytes: [9, 8, 7],
      contentType: "image/png",
      widthPx: 1600,
      heightPx: 900,
    });
    expect(await screen.findByText("Image replaced. Every pin kept its place.")).toBeInTheDocument();
  });

  it("refuses an unsupported or oversize replacement before sending anything", async () => {
    const w = world({ pins: [] });
    mount(w);
    await theSurface();

    fireEvent.change(screen.getByLabelText("New image file"), {
      target: { files: [pngFile("map.svg", [1], "image/svg+xml")] },
    });
    expect(await screen.findByRole("alert")).toHaveTextContent(/file type isn't supported/);

    const huge = pngFile("huge.png");
    Object.defineProperty(huge, "size", { value: 33 * 1024 * 1024 });
    fireEvent.change(screen.getByLabelText("New image file"), { target: { files: [huge] } });
    expect(await screen.findByRole("alert")).toHaveTextContent("That image is larger than 32 MB");
    expect(w.calls.replaceMapImage).not.toHaveBeenCalled();
  });

  it("an imported map without an image offers the upload as its primary action (#695)", async () => {
    const w = world({ maps: [{ ...SALTMARSH, hasImage: false }] });
    mount(w);
    const surface = await theSurface();
    // No <img> pointing at bytes that are not there.
    expect(surface.querySelector("img")).toBeNull();
    expect(screen.getByText(/restored from a backup that didn't include one/)).toBeInTheDocument();
    // The pins that survived the import are still drawn.
    expect(theDot()).toBeInTheDocument();

    expect(screen.getByRole("button", { name: "Upload image" })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("New image file"), { target: { files: [pngFile("saltmarsh.png")] } });

    await waitFor(() => expect(w.calls.replaceMapImage).toHaveBeenCalledTimes(1));
    // The map has its picture now.
    await waitFor(() => expect(surface.querySelector("img")).not.toBeNull());
    expect(screen.queryByRole("button", { name: "Upload image" })).toBeNull();
  });
});

// ── Empty and add states ─────────────────────────────────────────────────────

describe("MapsPanel empty and add states", () => {
  it("an empty campaign gets two ways to start, and each opens the dialog in its mode", async () => {
    mount(world({ maps: [], pins: [] }));
    expect(await screen.findByRole("heading", { name: "No maps yet" })).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Upload a map" }));
    let dialog = await screen.findByRole("dialog", { name: "Add a map" });
    expect(within(dialog).getByRole("button", { name: "Upload an image", pressed: true })).toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

    fireEvent.click(screen.getByRole("button", { name: "Generate one" }));
    dialog = await screen.findByRole("dialog", { name: "Add a map" });
    expect(within(dialog).getByRole("button", { name: "Generate an image", pressed: true })).toBeInTheDocument();
    expect(within(dialog).getByLabelText(/What should the map show/)).toBeInTheDocument();
  });

  it("states the size limit before submit and prefills the name from the file", async () => {
    const w = world({ pins: [] });
    mount(w);
    await theSurface();
    fireEvent.click(screen.getByRole("button", { name: "Add map" }));
    const dialog = await screen.findByRole("dialog", { name: "Add a map" });

    expect(within(dialog).getByText("PNG, JPEG, WebP or GIF, up to 32 MB.")).toBeInTheDocument();
    const add = within(dialog).getByRole("button", { name: "Add map" });
    expect(add).toBeDisabled();

    fireEvent.change(within(dialog).getByLabelText("Image"), {
      target: { files: [pngFile("old_harbour-district.png")] },
    });
    expect(within(dialog).getByLabelText("Name")).toHaveValue("old harbour district");
    expect(add).toBeEnabled();

    // A name the GM typed is theirs: another file does not overwrite it.
    fireEvent.change(within(dialog).getByLabelText("Name"), { target: { value: "The Docks" } });
    fireEvent.change(within(dialog).getByLabelText("Image"), { target: { files: [pngFile("scan-0042.png")] } });
    expect(within(dialog).getByLabelText("Name")).toHaveValue("The Docks");
  });

  it("blocks an oversize upload in the dialog, without a round trip", async () => {
    const w = world({ pins: [] });
    mount(w);
    await theSurface();
    fireEvent.click(screen.getByRole("button", { name: "Add map" }));
    const dialog = await screen.findByRole("dialog", { name: "Add a map" });

    const huge = pngFile("continent.png");
    Object.defineProperty(huge, "size", { value: 40 * 1024 * 1024 });
    fireEvent.change(within(dialog).getByLabelText("Image"), { target: { files: [huge] } });

    expect(within(dialog).getByText("That image is larger than 32 MB. Export it smaller and try again.")).toBeInTheDocument();
    expect(within(dialog).getByLabelText("Image")).toHaveAttribute("aria-invalid", "true");
    expect(within(dialog).getByRole("button", { name: "Add map" })).toBeDisabled();
    expect(w.calls.createMap).not.toHaveBeenCalled();
  });

  it("an uploaded map can say what it depicts — and then offers 'Suggest placements'", async () => {
    const w = world({ pins: [], nodes: [TOWN, INN] });
    mount(w);
    await theSurface();
    fireEvent.click(screen.getByRole("button", { name: "Add map" }));
    const dialog = await screen.findByRole("dialog", { name: "Add a map" });

    // "Depicts" used to exist only in generate mode, so SuggestMapPins was
    // unreachable for every uploaded map.
    fireEvent.change(within(dialog).getByLabelText("Image"), { target: { files: [pngFile("town.png")] } });
    fireEvent.change(within(dialog).getByLabelText("Depicts"), { target: { value: "n-town" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add map" }));

    await waitFor(() => expect(w.calls.createMap).toHaveBeenCalledTimes(1));
    expect(w.calls.createMap.mock.calls[0][0]).toMatchObject({
      name: "town",
      imageBytes: [1, 2, 3, 4],
      contentType: "image/png",
      anchorNodeId: "n-town",
    });
    // The new map opens, and its tray can suggest.
    expect(await screen.findByRole("application", { name: "town map" })).toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "Suggest placements" })).toBeInTheDocument();
  });
});

// ── Generated maps (#541) ─────────────────────────────────────────────────────

describe("generated maps", () => {
  function renderGenerate(opts: { generateErr?: Error } = {}) {
    const w = world({ pins: [], nodes: [TOWN, INN] });
    w.control.generateErr = opts.generateErr ?? null;
    mount(w);
    return w;
  }

  async function openGenerateMode() {
    await theSurface();
    fireEvent.click(screen.getByRole("button", { name: "Add map" }));
    fireEvent.click(await screen.findByRole("button", { name: "Generate an image" }));
  }

  it("generate → preview → save writes nothing until the GM saves", async () => {
    const { calls } = renderGenerate();
    await openGenerateMode();

    fireEvent.change(screen.getByLabelText(/What should the map show/), {
      target: { value: "a damp fishing town" },
    });
    fireEvent.click(screen.getByRole("button", { name: /^Generate$/ }));

    // The preview appears and NOTHING has been created.
    const preview = await screen.findByAltText("Generated map draft");
    expect(preview).toBeInTheDocument();
    await waitFor(() => expect(calls.generateMapImage).toHaveBeenCalled());
    expect(calls.createMap).not.toHaveBeenCalled();

    // Saving is the ordinary CreateMap call — a generated map is not a different
    // kind of map, it is a map whose bytes came from a model.
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Saltmarsh" } });
    fireEvent.click(screen.getByRole("button", { name: "Add map" }));
    await waitFor(() => expect(calls.createMap).toHaveBeenCalled());
    const saved = calls.createMap.mock.calls[0][0];
    expect(saved.name).toBe("Saltmarsh");
    // The BYTES, not just the name: a save that plumbed the wrong buffer through
    // would otherwise pass this test while storing the wrong picture.
    expect(saved.imageBytes).toEqual(Array.from(DRAFT_BYTES));
    expect(saved.contentType).toBe("image/png");
  });

  it("switching back to Upload drops the draft, so a file never saves generated bytes", async () => {
    const { calls } = renderGenerate();
    await openGenerateMode();
    fireEvent.change(screen.getByLabelText(/What should the map show/), {
      target: { value: "a town" },
    });
    fireEvent.click(screen.getByRole("button", { name: /^Generate$/ }));
    await screen.findByAltText("Generated map draft");

    // Two sources with two conditions is how the wrong bytes get saved: keying the
    // bytes off "is there a draft" while keying the rest off the mode meant a GM
    // who generated, switched to Upload and picked a file saved the GENERATED
    // bytes under the file's name.
    fireEvent.click(screen.getByRole("button", { name: "Upload an image" }));
    await waitFor(() => expect(screen.queryByAltText("Generated map draft")).toBeNull());

    fireEvent.change(screen.getByLabelText("Image"), { target: { files: [pngFile("town.png")] } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Saltmarsh" } });
    fireEvent.click(screen.getByRole("button", { name: "Add map" }));

    await waitFor(() => expect(calls.createMap).toHaveBeenCalled());
    const saved = calls.createMap.mock.calls[0][0];
    expect(saved.imageBytes).toEqual([1, 2, 3, 4]);
    // Nothing was picked under "Depicts", so nothing is anchored.
    expect(saved.anchorNodeId).toBe("");
  });

  it("discarding a draft costs one closed dialog and zero RPCs", async () => {
    const { calls } = renderGenerate();
    await openGenerateMode();
    fireEvent.change(screen.getByLabelText(/What should the map show/), {
      target: { value: "a town" },
    });
    fireEvent.click(screen.getByRole("button", { name: /^Generate$/ }));
    await screen.findByAltText("Generated map draft");

    fireEvent.click(screen.getByRole("button", { name: "Discard" }));

    await waitFor(() => expect(screen.queryByAltText("Generated map draft")).toBeNull());
    // The bytes only ever existed in the browser, so there is nothing to clean up
    // server-side and nothing was written.
    expect(calls.createMap).not.toHaveBeenCalled();
  });

  it("draws from the depicted entry and carries it through to the saved map", async () => {
    const { calls } = renderGenerate();
    await openGenerateMode();
    fireEvent.change(screen.getByLabelText(/What should the map show/), {
      target: { value: "the harbour" },
    });
    fireEvent.change(screen.getByLabelText("Depicts"), { target: { value: TOWN.id } });
    fireEvent.click(screen.getByRole("button", { name: /^Generate$/ }));
    await screen.findByAltText("Generated map draft");
    // The generation is seeded from that entry's prose…
    expect(calls.generateMapImage).toHaveBeenCalledWith({ prompt: "the harbour", anchorNodeId: TOWN.id });

    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Saltmarsh" } });
    fireEvent.click(screen.getByRole("button", { name: "Add map" }));

    await waitFor(() => expect(calls.createMap).toHaveBeenCalled());
    // …and without this the saved map has a NULL anchor and SuggestMapPins has no
    // prose to read — the feature would be unreachable for the very flow that
    // seeded it.
    expect(calls.createMap.mock.calls[0][0].anchorNodeId).toBe(TOWN.id);
  });

  it("an oversize generation says so and offers no draft to save", async () => {
    const { calls } = renderGenerate({
      // The server maps ErrImageTooLarge onto InvalidArgument — never Unavailable,
      // because Unavailable invites a retry and retrying re-bills the identical
      // oversize generation.
      generateErr: new ConnectError(
        "the generated image came back too large to store — try a simpler prompt",
        Code.InvalidArgument,
      ),
    });
    await openGenerateMode();
    fireEvent.change(screen.getByLabelText(/What should the map show/), {
      target: { value: "an entire continent" },
    });
    fireEvent.click(screen.getByRole("button", { name: /^Generate$/ }));

    expect(await screen.findByRole("alert")).toHaveTextContent(/too large/);
    expect(screen.queryByAltText("Generated map draft")).toBeNull();
    expect(calls.createMap).not.toHaveBeenCalled();
  });

  it("a spent plan allowance shows the app's own sentence, not the server's", async () => {
    const { calls } = renderGenerate({
      // The refusal exactly as the server sends it (allowance_error.go):
      // ResourceExhausted, an English message, and the reason the web keys on.
      // The reason has to survive the wire for the translated sentence to show.
      generateErr: new ConnectError("SERVER ENGLISH — must not be shown", Code.ResourceExhausted, {
        "Glyphoxa-Error-Reason": "allowance_exhausted",
      }),
    });
    await openGenerateMode();
    fireEvent.change(screen.getByLabelText(/What should the map show/), {
      target: { value: "the harbour" },
    });
    fireEvent.click(screen.getByRole("button", { name: /^Generate$/ }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/This month's included usage is used up/);
    expect(alert).not.toHaveTextContent(/SERVER ENGLISH|resource_exhausted/);
    expect(screen.queryByAltText("Generated map draft")).toBeNull();
    expect(calls.createMap).not.toHaveBeenCalled();
  });
});

// ── Load states ──────────────────────────────────────────────────────────────

describe("MapsPanel load states", () => {
  it("a failed list load offers a retry that works", async () => {
    let fail = true;
    const transport = createRouterTransport(({ service }) => {
      service(CampaignService, {
        listMaps: () => {
          if (fail) throw new ConnectError("db is down", Code.Unavailable);
          return create(ListMapsResponseSchema, { maps: [] });
        },
        listNodes: () => create(ListNodesResponseSchema, { nodes: [] }),
      });
    });
    const queryClient = makeQueryClient();
    queryClient.setDefaultOptions({ queries: { retry: false } });
    render(
      <Providers transport={transport} queryClient={queryClient}>
        <MapsPanel />
      </Providers>,
    );

    expect(await screen.findByText("Couldn't load your maps")).toBeInTheDocument();
    fail = false;
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("heading", { name: "No maps yet" })).toBeInTheDocument();
  });
});
