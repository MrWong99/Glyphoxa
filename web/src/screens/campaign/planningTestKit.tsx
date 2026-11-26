import { render } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import type { ReactNode } from "react";

import {
  ChatService,
  PlanningThreadSchema,
  PlanningMessageSchema,
  ListPlanningThreadsResponseSchema,
  GetPlanningThreadResponseSchema,
  CreatePlanningThreadResponseSchema,
  DeletePlanningThreadResponseSchema,
  RenamePlanningThreadResponseSchema,
  PlanningExchangeResponseSchema,
  PlanningTextDeltaSchema,
  PlanningToolActivitySchema,
  PlanningExchangeDoneSchema,
  type PlanningMessage,
  type PlanningThread,
  type PlanningExchangeRequest,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { MemoryScreenNav, type ScreenLocation } from "@/app/screenNav";
import { makeQueryClient } from "@/lib/queryClient";
import { PlanningPanel } from "./PlanningPanel";

// An in-memory ChatService that behaves like the server where the panel can
// tell: it persists the user turn before "calling the model", reuses a
// trailing unanswered user turn when the same text comes again, keeps the
// streamed text as the reply when the client stops, and auto-titles.

export const CAMPAIGN_ID = "c-1";

export function thread(id: string, title: string, updated: Date): PlanningThread {
  return create(PlanningThreadSchema, { id, campaignId: CAMPAIGN_ID, title, updatedAt: timestampFromDate(updated) });
}

export function message(id: string, role: "user" | "assistant", content: string): PlanningMessage {
  return create(PlanningMessageSchema, { id, role, content, createdAt: timestampFromDate(new Date("2026-08-12T20:00:00Z")) });
}

/**
 * One scripted exchange:
 *   deltas    streamed text, in order
 *   tools     tool frames sent before the text
 *   hold      after the deltas, wait for release() — or for the client to stop
 *   refuse    fail BEFORE persisting anything (allowance, no key)
 *   fail      fail AFTER persisting the user turn and streaming the deltas
 */
export type Script = {
  deltas?: string[];
  tools?: { name: string; isError?: boolean }[];
  hold?: boolean;
  refuse?: ConnectError;
  fail?: ConnectError;
};

export function mockChat(opts: { threads?: PlanningThread[]; messages?: Record<string, PlanningMessage[]>; scripts?: Script[] } = {}) {
  const threads = [...(opts.threads ?? [])];
  const messages = new Map<string, PlanningMessage[]>(
    threads.map((th) => [th.id, [...(opts.messages?.[th.id] ?? [])]] as const),
  );
  const scripts = [...(opts.scripts ?? [])];
  const exchanges: PlanningExchangeRequest[] = [];
  const calls = { create: 0, list: 0, get: 0, delete: 0, rename: 0 };
  const aborted: boolean[] = [];
  let seq = 0;
  let release: () => void = () => {};

  const append = (threadId: string, role: "user" | "assistant", content: string) => {
    const m = message(`m-${++seq}`, role, content);
    messages.get(threadId)?.push(m);
    return m;
  };

  const transport = createRouterTransport(({ service }) => {
    service(ChatService, {
      listPlanningThreads: () => {
        calls.list++;
        return create(ListPlanningThreadsResponseSchema, { threads });
      },
      getPlanningThread: (req) => {
        calls.get++;
        const th = threads.find((x) => x.id === req.threadId);
        if (!th) throw new ConnectError("thread not found", Code.NotFound);
        return create(GetPlanningThreadResponseSchema, { thread: th, messages: [...(messages.get(req.threadId) ?? [])] });
      },
      createPlanningThread: () => {
        calls.create++;
        const th = thread(`t-new-${calls.create}`, "", new Date());
        threads.unshift(th);
        messages.set(th.id, []);
        return create(CreatePlanningThreadResponseSchema, { thread: th });
      },
      renamePlanningThread: (req) => {
        calls.rename++;
        const th = threads.find((x) => x.id === req.threadId)!;
        th.title = req.title;
        return create(RenamePlanningThreadResponseSchema, { thread: th });
      },
      deletePlanningThread: (req) => {
        calls.delete++;
        const i = threads.findIndex((x) => x.id === req.threadId);
        if (i >= 0) threads.splice(i, 1);
        messages.delete(req.threadId);
        return create(DeletePlanningThreadResponseSchema, {});
      },
      async *planningExchange(req, ctx) {
        exchanges.push(req);
        const index = aborted.length;
        aborted.push(false);
        const script = scripts.shift() ?? { deltas: ["Start with the docks."] };
        if (script.refuse) throw script.refuse;

        const history = messages.get(req.threadId) ?? [];
        const last = history[history.length - 1];
        if (!(last && last.role === "user" && last.content === req.message)) append(req.threadId, "user", req.message);
        const th = threads.find((x) => x.id === req.threadId);
        if (th && th.title === "") th.title = req.message.slice(0, 60);

        for (const tool of script.tools ?? []) {
          yield create(PlanningExchangeResponseSchema, {
            frame: { case: "tool", value: create(PlanningToolActivitySchema, { toolName: tool.name, isError: tool.isError ?? false }) },
          });
        }
        let streamed = "";
        for (const delta of script.deltas ?? []) {
          streamed += delta;
          yield create(PlanningExchangeResponseSchema, {
            frame: { case: "text", value: create(PlanningTextDeltaSchema, { delta }) },
          });
        }
        if (script.hold) {
          const stopped = await new Promise<boolean>((resolve) => {
            release = () => resolve(false);
            ctx.signal.addEventListener("abort", () => resolve(true));
          });
          if (stopped) {
            aborted[index] = true;
            // The engine keeps what had streamed (ADR-0062 amendment).
            if (streamed.trim() !== "") append(req.threadId, "assistant", streamed.trim());
            return;
          }
        }
        if (script.fail) throw script.fail;
        const reply = append(req.threadId, "assistant", streamed.trim());
        yield create(PlanningExchangeResponseSchema, {
          frame: { case: "done", value: create(PlanningExchangeDoneSchema, { assistantMessage: reply, thread: th }) },
        });
      },
    });
  });

  return {
    transport,
    exchanges,
    calls,
    aborted,
    messages,
    threads,
    release: () => release(),
    userTurns: (threadId: string, text: string) =>
      (messages.get(threadId) ?? []).filter((m) => m.role === "user" && m.content === text).length,
  };
}

export type MockChat = ReturnType<typeof mockChat>;

/** Renders the panel over a mock, optionally under an in-memory screen nav. */
export function renderPanel(
  chat: MockChat,
  opts: { nav?: Partial<ScreenLocation>; onNavigate?: (next: ScreenLocation) => void; queryClient?: ReturnType<typeof makeQueryClient> } = {},
) {
  const queryClient = opts.queryClient ?? makeQueryClient();
  const wrap = (node: ReactNode) => (
    <Providers transport={chat.transport} queryClient={queryClient}>
      {opts.nav || opts.onNavigate ? (
        <MemoryScreenNav initial={{ screen: "campaign", view: "planning", ...opts.nav }} onNavigate={opts.onNavigate}>
          {node}
        </MemoryScreenNav>
      ) : (
        node
      )}
    </Providers>
  );
  const result = render(wrap(<PlanningPanel campaignId={CAMPAIGN_ID} />));
  return {
    ...result,
    queryClient,
    /** Unmounts the panel (another view) and mounts it again (back to Planning). */
    remount: () => {
      result.rerender(wrap(<div data-testid="other-view" />));
      result.rerender(wrap(<PlanningPanel campaignId={CAMPAIGN_ID} />));
    },
    leave: () => result.rerender(wrap(<div data-testid="other-view" />)),
  };
}
