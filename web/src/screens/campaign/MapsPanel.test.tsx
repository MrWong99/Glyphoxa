import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  CampaignService,
  GetMapViewResponseSchema,
  ListMapsResponseSchema,
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
