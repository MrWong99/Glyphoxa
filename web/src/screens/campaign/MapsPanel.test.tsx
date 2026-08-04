import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  CampaignService,
  CreateMapResponseSchema,
  GenerateMapImageResponseSchema,
  GetMapViewResponseSchema,
  ListMapsResponseSchema,
  ListNodesResponseSchema,
  NodeType,
  SuggestMapPinsResponseSchema,
  UpdatePinResponseSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { MapsPanel } from "./MapsPanel";

// The Maps tab (#538, ADR-0060). What is worth pinning here is the pointer
// grammar: the same button both OPENS an entry and DRAGS its pin, so the
// distinction between the two gestures is the whole interaction, and getting it
// wrong is silent — the pin simply drifts.

const MAP = {
  id: "m1",
  name: "Saltmarsh",
  widthPx: 1200,
  heightPx: 800,
  gmPrivate: false,
};

const PIN = {
  id: "p1",
  mapId: "m1",
  nodeId: "n1",
  nodeName: "The Snapping Line",
  nodeType: 3,
  x: 0.25,
  y: 0.25,
  labelOverride: "",
  gmPrivate: false,
  nodeGmPrivate: false,
};

function renderMaps() {
  const updatePin = vi.fn((req: { x: number; y: number }) =>
    create(UpdatePinResponseSchema, { pin: { ...PIN, x: req.x, y: req.y } }),
  );
  const onOpenNode = vi.fn();
  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      listMaps: () => create(ListMapsResponseSchema, { maps: [MAP] }),
      getMapView: () =>
        create(GetMapViewResponseSchema, {
          map: MAP,
          pins: [PIN],
          breadcrumb: [],
          children: [],
          unpinned: [],
        }),
      updatePin,
    });
  });
  render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <MapsPanel onOpenNode={onOpenNode} />
    </Providers>,
  );
  return { updatePin, onOpenNode };
}

async function openTheMap() {
  const card = await screen.findByRole("button", { name: /Saltmarsh/ });
  fireEvent.click(card);
  const surface = await screen.findByRole("application", { name: /Saltmarsh map/ });
  // jsdom reports a zero-size rect for everything, and the panel's normalize()
  // returns null on a zero-width surface — so without a real box EVERY gesture
  // becomes a no-op and every assertion below passes for the wrong reason.
  surface.getBoundingClientRect = () =>
    ({ left: 0, top: 0, width: 1000, height: 600, right: 1000, bottom: 600, x: 0, y: 0 }) as DOMRect;
  return surface;
}

describe("MapsPanel pin gestures", () => {
  it("a drag past the slop threshold moves the pin", async () => {
    const { updatePin } = renderMaps();
    const surface = await openTheMap();
    const dot = screen.getByRole("button", { name: /^The Snapping Line \(/ });

    fireEvent.mouseDown(dot, { clientX: 100, clientY: 100 });
    fireEvent.mouseMove(surface, { clientX: 300, clientY: 240 });
    fireEvent.mouseUp(surface, { clientX: 300, clientY: 240 });

    await waitFor(() => expect(updatePin).toHaveBeenCalledTimes(1));
    // 300/1000 and 240/600 in the stubbed box — the write carries where the pointer
    // ended, normalized.
    const req = updatePin.mock.calls[0][0];
    expect(req.x).toBeCloseTo(0.3);
    expect(req.y).toBeCloseTo(0.4);
  });

  it("a plain click on a pin opens its entry and never moves it", async () => {
    const { updatePin, onOpenNode } = renderMaps();
    const surface = await openTheMap();
    const dot = screen.getByRole("button", { name: /^The Snapping Line \(/ });

    // Press and release at the SAME point — an ordinary click.
    fireEvent.mouseDown(dot, { clientX: 100, clientY: 100 });
    fireEvent.mouseUp(surface, { clientX: 100, clientY: 100 });
    fireEvent.click(dot);

    // onOpenNode is the synchronisation point: once the click has been handled, a
    // write that was going to fire would already have been issued.
    await waitFor(() => expect(onOpenNode).toHaveBeenCalledWith("n1"));
    // The bug this pins: the press armed a drag, so the release wrote the pin to
    // wherever the pointer happened to be. Clicking a pin to read its entry walked
    // it across the map, one write RPC per click.
    expect(updatePin).not.toHaveBeenCalled();
  });

  it("hand tremor below the threshold does not become a drag", async () => {
    const { updatePin } = renderMaps();
    const surface = await openTheMap();
    const dot = screen.getByRole("button", { name: /^The Snapping Line \(/ });

    // Two pixels of tremor, then release: not a drag.
    fireEvent.mouseDown(dot, { clientX: 100, clientY: 100 });
    fireEvent.mouseMove(surface, { clientX: 101, clientY: 102 });
    fireEvent.mouseUp(surface, { clientX: 101, clientY: 102 });

    // Then a REAL drag, so the assertion has a positive event to wait for rather
    // than proving nothing happened by not waiting long enough.
    fireEvent.mouseDown(dot, { clientX: 100, clientY: 100 });
    fireEvent.mouseMove(surface, { clientX: 500, clientY: 300 });
    fireEvent.mouseUp(surface, { clientX: 500, clientY: 300 });

    await waitFor(() => expect(updatePin).toHaveBeenCalled());
    expect(updatePin).toHaveBeenCalledTimes(1);
    const req = updatePin.mock.calls[0][0];
    expect(req.x).toBeCloseTo(0.5);
  });
});

// ── Generated maps (#541) ─────────────────────────────────────────────────────

const DRAFT_BYTES = new Uint8Array([0x89, 0x50, 0x4e, 0x47]);

// jsdom has no image decoder, so `new Image()` never fires onload and the save
// path — which measures pixel dimensions client-side — would hang forever. Stub
// a decoder that reports a fixed size: what is under test is the draft-review
// flow, not the browser's PNG parser.
beforeEach(() => {
  class StubImage {
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    naturalWidth = 1200;
    naturalHeight = 800;
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
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});
const ANCHOR = { id: "n-town", name: "Saltmarsh", nodeType: NodeType.LOCATION };

function renderGenerate(opts: { generateErr?: Error; suggestions?: { nodeId: string; name: string }[] } = {}) {
  const generate = vi.fn((req: { prompt: string; anchorNodeId: string }) => {
    if (opts.generateErr) throw opts.generateErr;
    return create(GenerateMapImageResponseSchema, {
      imageBytes: DRAFT_BYTES,
      contentType: "image/png",
      model: "gemini-2.5-flash-image",
      prompt: `composed: ${req.prompt}`,
    });
  });
  const createMap = vi.fn(
    (req: { name: string; anchorNodeId: string; imageBytes: Uint8Array; contentType: string }) =>
      create(CreateMapResponseSchema, { map: { ...MAP, id: "m2", name: req.name } }),
  );
  const suggestMapPins = vi.fn(() =>
    create(SuggestMapPinsResponseSchema, { suggestions: opts.suggestions ?? [] }),
  );
  const transport = createRouterTransport(({ service }) => {
    service(CampaignService, {
      listMaps: () => create(ListMapsResponseSchema, { maps: [MAP] }),
      listNodes: () => create(ListNodesResponseSchema, { nodes: [ANCHOR] }),
      getMapView: () =>
        create(GetMapViewResponseSchema, {
          map: { ...MAP, anchorNodeId: ANCHOR.id },
          pins: [],
          breadcrumb: [],
          children: [],
          unpinned: [{ id: "n-inn", name: "The Rusty Anchor", nodeType: NodeType.LOCATION }],
        }),
      generateMapImage: generate,
      createMap: createMap,
      suggestMapPins,
    });
  });
  render(
    <Providers transport={transport} queryClient={makeQueryClient()}>
      <MapsPanel />
    </Providers>,
  );
  return { generate, createMap, suggestMapPins };
}

async function openGenerateMode() {
  fireEvent.click(await screen.findByRole("button", { name: /Add map/ }));
  fireEvent.click(await screen.findByRole("button", { name: "Generate an image" }));
}

describe("generated maps", () => {
  it("generate → preview → save writes nothing until the GM saves", async () => {
    const { generate, createMap } = renderGenerate();
    await openGenerateMode();

    fireEvent.change(screen.getByLabelText(/What should the map show/), {
      target: { value: "a damp fishing town" },
    });
    fireEvent.click(screen.getByRole("button", { name: /^Generate$/ }));

    // The preview appears and NOTHING has been created.
    const preview = await screen.findByAltText("Generated map draft");
    expect(preview).toBeInTheDocument();
    await waitFor(() => expect(generate).toHaveBeenCalled());
    expect(createMap).not.toHaveBeenCalled();

    // Saving is the ordinary CreateMap call — a generated map is not a different
    // kind of map, it is a map whose bytes came from a model.
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Saltmarsh" } });
    fireEvent.click(screen.getByRole("button", { name: "Add map" }));
    await waitFor(() => expect(createMap).toHaveBeenCalled());
    const saved = createMap.mock.calls[0][0];
    expect(saved.name).toBe("Saltmarsh");
    // The BYTES, not just the name: a save that plumbed the wrong buffer through
    // would otherwise pass this test while storing the wrong picture.
    expect(Array.from(saved.imageBytes)).toEqual(Array.from(DRAFT_BYTES));
    expect(saved.contentType).toBe("image/png");
  });

  it("switching back to Upload drops the draft, so a file never saves generated bytes", async () => {
    const { createMap } = renderGenerate();
    await openGenerateMode();
    fireEvent.change(screen.getByLabelText(/What should the map show/), {
      target: { value: "a town" },
    });
    fireEvent.click(screen.getByRole("button", { name: /^Generate$/ }));
    await screen.findByAltText("Generated map draft");

    // Two sources with two conditions is how the wrong bytes get saved: keying the
    // bytes off "is there a draft" while keying the anchor off the mode meant a GM
    // who generated, switched to Upload and picked a file saved the GENERATED
    // bytes under the file's name.
    fireEvent.click(screen.getByRole("button", { name: "Upload an image" }));
    await waitFor(() => expect(screen.queryByAltText("Generated map draft")).toBeNull());

    const fileInput = screen.getByLabelText("Image");
    const file = new File([new Uint8Array([1, 2, 3, 4])], "town.png", { type: "image/png" });
    // jsdom's File has no arrayBuffer(); the upload path needs one to read bytes.
    Object.defineProperty(file, "arrayBuffer", {
      value: async () => new Uint8Array([1, 2, 3, 4]).buffer,
    });
    fireEvent.change(fileInput, { target: { files: [file] } });
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Saltmarsh" } });
    fireEvent.click(screen.getByRole("button", { name: "Add map" }));

    await waitFor(() => expect(createMap).toHaveBeenCalled());
    const saved = createMap.mock.calls[0][0];
    expect(Array.from(saved.imageBytes)).toEqual([1, 2, 3, 4]);
    // And no anchor: the anchor picker belongs to generation.
    expect(saved.anchorNodeId).toBe("");
  });

  it("discarding a draft costs one closed dialog and zero RPCs", async () => {
    const { createMap } = renderGenerate();
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
    expect(createMap).not.toHaveBeenCalled();
  });

  it("carries the anchor entry through to the saved map", async () => {
    const { createMap } = renderGenerate();
    await openGenerateMode();
    fireEvent.change(screen.getByLabelText(/What should the map show/), {
      target: { value: "the harbour" },
    });
    fireEvent.change(screen.getByLabelText("Base on entry"), { target: { value: ANCHOR.id } });
    fireEvent.click(screen.getByRole("button", { name: /^Generate$/ }));
    await screen.findByAltText("Generated map draft");

    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Saltmarsh" } });
    fireEvent.click(screen.getByRole("button", { name: "Add map" }));

    await waitFor(() => expect(createMap).toHaveBeenCalled());
    // Without this the saved map has a NULL anchor and SuggestMapPins has no prose
    // to read — the feature would be unreachable for the very flow that seeded it.
    expect(createMap.mock.calls[0][0].anchorNodeId).toBe(ANCHOR.id);
  });

  it("an oversize generation says so and offers no draft to save", async () => {
    const { createMap } = renderGenerate({
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
    expect(createMap).not.toHaveBeenCalled();
  });

  it("suggested pins mark the tray and place nothing", async () => {
    const { suggestMapPins } = renderGenerate({
      suggestions: [{ nodeId: "n-inn", name: "The Rusty Anchor" }],
    });
    fireEvent.click(await screen.findByRole("button", { name: /Saltmarsh/ }));
    await screen.findByRole("application", { name: /Saltmarsh map/ });

    fireEvent.click(screen.getByRole("button", { name: /Suggest pins/ }));
    await waitFor(() => expect(suggestMapPins).toHaveBeenCalled());

    // The suggestion is a HIGHLIGHT on the entry that was already in the tray.
    const chip = await screen.findByRole("button", { name: /suggested for this map/ });
    expect(chip).toHaveAttribute("data-suggested");
    // AC3: it arrives UNPLACED. Nothing is on the map until the GM drags it.
    expect(screen.queryByRole("button", { name: /^The Rusty Anchor \(/ })).toBeNull();
  });
});
