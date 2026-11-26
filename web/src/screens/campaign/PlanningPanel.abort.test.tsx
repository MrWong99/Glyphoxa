import { afterEach, describe, it, expect } from "vitest";
import { act, render, screen, fireEvent, waitFor } from "@testing-library/react";
import { createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";

import {
  ChatService,
  GetPlanningThreadResponseSchema,
  ListPlanningThreadsResponseSchema,
  PlanningExchangeResponseSchema,
  PlanningTextDeltaSchema,
  PlanningThreadSchema,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { makeQueryClient } from "@/lib/queryClient";
import { PlanningPanel } from "./PlanningPanel";
import { resetPlanningStreams, stopExchangesOutside } from "./planningStream";

// The exchange's cancel posture (ADR-0062 amendment 2026-11-26). It used to be
// aborted whenever the panel unmounted (W15), which cost the GM the reply when
// they glanced at another view. Now only two things end it early: Stop, and a
// change of Active Campaign. Leaving the view does not.

function setup() {
  const signals = { aborted: false };
  const thread = create(PlanningThreadSchema, { id: "t1", campaignId: "c1", title: "Prep" });
  const transport = createRouterTransport(({ service }) => {
    service(ChatService, {
      listPlanningThreads: () => create(ListPlanningThreadsResponseSchema, { threads: [thread] }),
      getPlanningThread: () => create(GetPlanningThreadResponseSchema, { thread, messages: [] }),
      async *planningExchange(_req, ctx) {
        yield create(PlanningExchangeResponseSchema, {
          frame: { case: "text", value: create(PlanningTextDeltaSchema, { delta: "Thinking" }) },
        });
        // Held open until the caller goes away.
        await new Promise<void>((resolve) => {
          ctx.signal.addEventListener("abort", () => {
            signals.aborted = true;
            resolve();
          });
        });
      },
    });
  });
  const queryClient = makeQueryClient();
  const panel = (campaignId = "c1") => (
    <Providers transport={transport} queryClient={queryClient}>
      <PlanningPanel campaignId={campaignId} />
    </Providers>
  );
  const view = render(panel());
  return { signals, view, panel };
}

async function sendOne() {
  const composer = await screen.findByRole("textbox", { name: /message to the butler/i });
  await screen.findAllByText("Prep");
  fireEvent.change(composer, { target: { value: "What next?" } });
  fireEvent.keyDown(composer, { key: "Enter" });
  expect(await screen.findByText("Thinking")).toBeInTheDocument();
}

afterEach(() => {
  resetPlanningStreams();
  sessionStorage.clear();
});

describe("PlanningPanel exchange cancellation", () => {
  it("keeps the exchange running when the panel unmounts", async () => {
    const { signals, view } = setup();
    await sendOne();
    view.unmount();
    // Give an abort every chance to arrive: it must not.
    await act(async () => {
      await new Promise((r) => setTimeout(r, 50));
    });
    expect(signals.aborted).toBe(false);
  });

  it("aborts the exchange on Stop", async () => {
    const { signals } = setup();
    await sendOne();
    fireEvent.click(screen.getByRole("button", { name: "Stop the Butler's reply" }));
    await waitFor(() => expect(signals.aborted).toBe(true));
  });

  it("aborts the exchange when the Active Campaign changes", async () => {
    const { signals, view } = setup();
    await sendOne();
    view.unmount();
    // What Campaign.tsx does when the switch lands.
    act(() => stopExchangesOutside("c2"));
    await waitFor(() => expect(signals.aborted).toBe(true));
  });

  it("aborts the exchange when the panel mounts for another campaign", async () => {
    const { signals, view, panel } = setup();
    await sendOne();
    view.rerender(panel("c2"));
    await waitFor(() => expect(signals.aborted).toBe(true));
  });
});
