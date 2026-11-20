import { describe, it, expect } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
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

// W15: the exchange stream carried no AbortSignal, so leaving the panel left a
// paid LLM reply streaming to nobody until the server finished it.
describe("PlanningPanel exchange cancellation", () => {
  it("aborts the in-flight exchange when the panel unmounts", async () => {
    let aborted = false;
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
              aborted = true;
              resolve();
            });
          });
        },
      });
    });
    const { unmount } = render(
      <Providers transport={transport} queryClient={makeQueryClient()}>
        <PlanningPanel campaignId="c1" />
      </Providers>,
    );

    const composer = await screen.findByRole("textbox");
    await screen.findAllByText("Prep");
    fireEvent.change(composer, { target: { value: "What next?" } });
    fireEvent.keyDown(composer, { key: "Enter" });
    expect(await screen.findByText("Thinking")).toBeInTheDocument();
    expect(aborted).toBe(false);

    unmount();
    await waitFor(() => expect(aborted).toBe(true));
  });
});
