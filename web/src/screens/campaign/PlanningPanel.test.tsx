import { afterEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, screen, waitFor, within } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";

import { message, mockChat, renderPanel, thread, type Script } from "./planningTestKit";
import { resetPlanningStreams } from "./planningStream";

const HOUR = 60 * 60 * 1000;
const recent = () => thread("t-recent", "Harbour heist prep", new Date(Date.now() - HOUR));
const older = () => thread("t-older", "Act two loose ends", new Date(Date.now() - 3 * 24 * HOUR));
const ancient = () => thread("t-ancient", "Session zero", new Date("2026-08-01T18:00:00Z"));

const recentMessages = () => [
  message("m-a", "user", "What did the players learn at the lighthouse?"),
  message("m-b", "assistant", "They learned the keeper is a smuggler."),
];

function standard(scripts: Script[] = []) {
  return mockChat({
    threads: [recent(), older(), ancient()],
    messages: { "t-recent": recentMessages() },
    scripts,
  });
}

const composer = () => screen.getByRole("textbox", { name: /message to the butler/i }) as HTMLTextAreaElement;

function type(text: string) {
  fireEvent.change(composer(), { target: { value: text } });
}

function pressEnter() {
  fireEvent.keyDown(composer(), { key: "Enter" });
}

// Bubbles of the GM's own turns with this text, in the message list.
function userBubbles(text: string) {
  const list = screen.getByRole("list", { name: "Messages" });
  return [...list.querySelectorAll('[data-role="user"] .gx-planning__bubble')].filter((b) => b.textContent === text);
}

afterEach(() => {
  resetPlanningStreams();
  sessionStorage.clear();
});

describe("PlanningPanel threads", () => {
  it("groups conversations by last activity and opens the most recent one", async () => {
    renderPanel(standard());

    const rail = screen.getByRole("complementary", { name: "Planning conversations" });
    expect(await within(rail).findByText("Harbour heist prep")).toBeInTheDocument();
    const groups = within(rail).getAllByRole("heading", { level: 2 }).map((h) => h.textContent);
    expect(groups).toEqual(["Today", "Last 7 days", "Earlier"]);
    // Groups, not landmarks: the rail is already one.
    expect(within(rail).getByRole("group", { name: "Earlier" })).toHaveTextContent("Session zero");
    expect(within(rail).queryByRole("region")).not.toBeInTheDocument();

    expect(await screen.findByText(/the keeper is a smuggler/)).toBeInTheDocument();
    expect(within(rail).getByRole("button", { name: /^Harbour heist prep/ })).toHaveAttribute("aria-current", "true");
  });

  it("filters the rail by title", async () => {
    renderPanel(standard());
    const rail = screen.getByRole("complementary", { name: "Planning conversations" });
    await within(rail).findByText("Harbour heist prep");

    fireEvent.change(within(rail).getByRole("searchbox", { name: "Search conversations" }), { target: { value: "loose" } });
    expect(within(rail).getByText("Act two loose ends")).toBeInTheDocument();
    expect(within(rail).queryByText("Harbour heist prep")).not.toBeInTheDocument();

    fireEvent.change(within(rail).getByRole("searchbox"), { target: { value: "dragons" } });
    expect(within(rail).getByText("No conversation title matches “dragons”.")).toBeInTheDocument();
  });

  it("keeps the open conversation in the URL and follows it", async () => {
    const onNavigate = vi.fn();
    renderPanel(standard(), { nav: { search: { thread: "t-older" } }, onNavigate });

    // The URL's thread wins over "most recent".
    const rail = screen.getByRole("complementary", { name: "Planning conversations" });
    await waitFor(() => expect(within(rail).getByRole("button", { name: /^Act two loose ends/ })).toHaveAttribute("aria-current", "true"));
    expect(onNavigate).not.toHaveBeenCalled();

    fireEvent.click(within(rail).getByRole("button", { name: /^Harbour heist prep/ }));
    expect(onNavigate).toHaveBeenLastCalledWith(expect.objectContaining({ search: { thread: "t-recent" } }), { replace: false });
    expect(await screen.findByText(/the keeper is a smuggler/)).toBeInTheDocument();
  });

  it("drops a conversation from the URL that no longer exists", async () => {
    const onNavigate = vi.fn();
    renderPanel(standard(), { nav: { search: { thread: "t-gone" } }, onNavigate });
    // It falls back to the most recent conversation, replacing the dead entry.
    await waitFor(() =>
      expect(onNavigate).toHaveBeenLastCalledWith(expect.objectContaining({ search: { thread: "t-recent" } }), { replace: true }),
    );
    expect(await screen.findByText(/the keeper is a smuggler/)).toBeInTheDocument();
  });

  it("renames and deletes through the row's menu", async () => {
    const chat = standard();
    renderPanel(chat);
    const rail = screen.getByRole("complementary", { name: "Planning conversations" });
    await within(rail).findByText("Act two loose ends");

    fireEvent.keyDown(within(rail).getByRole("button", { name: "Actions for “Act two loose ends”" }), { key: "Enter" });
    fireEvent.click(await screen.findByRole("menuitem", { name: "Rename" }));
    const field = await within(rail).findByRole("textbox", { name: "Conversation name" });
    // Keyboard users land in the field, not on <body>.
    await waitFor(() => expect(field).toHaveFocus());
    fireEvent.change(field, { target: { value: "Act two threads" } });
    fireEvent.keyDown(field, { key: "Enter" });
    expect(await within(rail).findByText("Act two threads")).toBeInTheDocument();
    expect(chat.calls.rename).toBe(1);
    // …and back on the row when it ends.
    await waitFor(() => expect(within(rail).getByRole("button", { name: /^Act two threads/ })).toHaveFocus());

    fireEvent.keyDown(within(rail).getByRole("button", { name: "Actions for “Act two threads”" }), { key: "Enter" });
    fireEvent.click(await screen.findByRole("menuitem", { name: "Delete…" }));
    // Nothing is deleted until the GM confirms.
    expect(chat.calls.delete).toBe(0);
    fireEvent.click(await screen.findByRole("button", { name: "Delete conversation" }));
    await waitFor(() => expect(within(rail).queryByText("Act two threads")).not.toBeInTheDocument());
    expect(chat.calls.delete).toBe(1);
    // The row and its menu are gone: focus goes on to the composer, not <body>.
    await waitFor(() => expect(composer()).toHaveFocus());
  });

  it("opens the conversations as a sheet from the phone bar", async () => {
    renderPanel(standard());
    await screen.findByText(/the keeper is a smuggler/);
    fireEvent.click(screen.getByRole("button", { name: "Conversations (3)" }));
    const sheet = await screen.findByRole("dialog", { name: "Conversations" });
    fireEvent.click(within(sheet).getByRole("button", { name: /^Act two loose ends/ }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    const rail = screen.getByRole("complementary", { name: "Planning conversations" });
    expect(within(rail).getByRole("button", { name: /^Act two loose ends/ })).toHaveAttribute("aria-current", "true");
  });

  it("lands in the composer after 'New conversation' in the sheet", async () => {
    renderPanel(standard());
    await screen.findByText(/the keeper is a smuggler/);
    fireEvent.click(screen.getByRole("button", { name: "Conversations (3)" }));
    const sheet = await screen.findByRole("dialog", { name: "Conversations" });
    fireEvent.click(within(sheet).getByRole("button", { name: "New conversation" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await waitFor(() => expect(composer()).toHaveFocus());
    // …and stays there once the sheet's own focus handling has run.
    await act(async () => {
      await new Promise((r) => setTimeout(r, 20));
    });
    expect(composer()).toHaveFocus();
  });

  it("speaks German", async () => {
    localStorage.setItem("gx-lang", "de");
    try {
      renderPanel(mockChat({ threads: [] }));
      expect(await screen.findByText("Was kann der Butler?")).toBeInTheDocument();
      expect(screen.getByRole("button", { name: /Fass die letzte Session zusammen/ })).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Unterhaltungen (0)" })).toBeInTheDocument();
    } finally {
      localStorage.removeItem("gx-lang");
    }
  });
});

describe("PlanningPanel new conversations", () => {
  it("shows starters and creates nothing until the first message", async () => {
    const chat = standard([{ deltas: ["Last time, the party met Bart."] }]);
    renderPanel(chat);
    await screen.findByText(/the keeper is a smuggler/);

    fireEvent.click(screen.getAllByRole("button", { name: "New conversation" })[0]);
    const starters = await screen.findByRole("list", { name: "What can the Butler do?" });
    expect(within(starters).getAllByRole("button")).toHaveLength(6);
    expect(chat.calls.create).toBe(0);

    // A prompt ending in a blank fills the composer; nothing is sent.
    fireEvent.click(within(starters).getByRole("button", { name: /What do the players know about/ }));
    expect(composer().value).toBe("What do the players know about ");
    expect(chat.calls.create).toBe(0);
    expect(chat.exchanges).toHaveLength(0);

    // A complete prompt sends: the thread is created by that first message.
    fireEvent.click(within(starters).getByRole("button", { name: /Recap the last session/ }));
    await waitFor(() => expect(chat.exchanges).toHaveLength(1));
    expect(chat.calls.create).toBe(1);
    expect(chat.exchanges[0]).toMatchObject({ threadId: "t-new-1", message: "Recap the last session" });
    // The starters went with the exchange; focus is in the composer, not on <body>.
    expect(composer()).toHaveFocus();
    expect(await screen.findByText("Last time, the party met Bart.")).toBeInTheDocument();
    // The prefilled draft belonged to the not-yet-started conversation.
    expect(composer().value).toBe("");
  });

  it("starts with the starters when the campaign has no conversations", async () => {
    const chat = mockChat({ threads: [] });
    renderPanel(chat);
    expect(await screen.findByText("What can the Butler do?")).toBeInTheDocument();
    expect(screen.getByText(/No conversations yet/)).toBeInTheDocument();

    type("Who runs the docks?");
    pressEnter();
    await waitFor(() => expect(chat.exchanges).toHaveLength(1));
    expect(chat.calls.create).toBe(1);
  });
});

describe("PlanningPanel exchange", () => {
  it("keeps the composer editable while streaming, turns Send into Stop, and lands the reply once", async () => {
    const chat = standard([{ deltas: ["Start with "], hold: true }]);
    renderPanel(chat);
    await screen.findByText(/the keeper is a smuggler/);

    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
    type("Where should the session open?");
    pressEnter();

    expect(await screen.findByText("Start with")).toBeInTheDocument();
    expect(userBubbles("Where should the session open?")).toHaveLength(1);
    expect(composer()).toBeEnabled();
    expect(composer().value).toBe("");
    expect(screen.getByRole("button", { name: "Stop the Butler's reply" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Send" })).not.toBeInTheDocument();

    // Typing the next question while the reply streams is fine; sending waits.
    type("And then?");
    pressEnter();
    expect(chat.exchanges).toHaveLength(1);

    act(() => chat.release());
    expect(await screen.findByRole("button", { name: "Send" })).toBeEnabled();
    expect(composer().value).toBe("And then?");
    // Said once to a screen reader; the deltas themselves are not read out.
    expect(screen.getByText("The Butler has replied.")).toBeInTheDocument();
    await waitFor(() => expect(userBubbles("Where should the session open?")).toHaveLength(1));
    expect(chat.userTurns("t-recent", "Where should the session open?")).toBe(1);
  });

  it("moves focus off Stop to the composer when the reply ends", async () => {
    const chat = standard([{ deltas: ["Start with "], hold: true }]);
    renderPanel(chat);
    await screen.findByText(/the keeper is a smuggler/);
    type("Where should the session open?");
    pressEnter();
    const stop = await screen.findByRole("button", { name: "Stop the Butler's reply" });
    stop.focus();
    act(() => chat.release());
    await screen.findByRole("button", { name: "Send" });
    expect(composer()).toHaveFocus();
  });

  it("holds a follow-up until the finished reply is read back, then Stop marks only the new partial", async () => {
    const chat = standard([{ deltas: ["Reply one."] }, { deltas: ["Partial two"], hold: true }]);
    renderPanel(chat);
    await screen.findByText(/the keeper is a smuggler/);

    type("Question one");
    const openReads = chat.holdReads();
    pressEnter();
    await waitFor(() => expect(chat.messages.get("t-recent")).toHaveLength(4));
    expect(await screen.findByText("Reply one.")).toBeInTheDocument();

    // The reply is done but its re-read is still out: the follow-up typed in
    // the meantime waits instead of being lined up against stale rows.
    type("Question two");
    pressEnter();
    expect(chat.exchanges).toHaveLength(1);
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();

    act(() => openReads());
    await waitFor(() => expect(screen.getByRole("button", { name: "Send" })).toBeEnabled());
    pressEnter();
    expect(await screen.findByText("Partial two")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Stop the Butler's reply" }));
    await waitFor(() => expect(chat.messages.get("t-recent")?.at(-1)?.content).toBe("Partial two"));
    await waitFor(() => expect(document.querySelectorAll('[data-pending="true"]')).toHaveLength(0), { timeout: 3000 });

    const marked = [...document.querySelectorAll(".gx-planning__partial")].map((p) => p.closest("li")?.textContent ?? "");
    expect(marked).toHaveLength(1);
    expect(marked[0]).toContain("Partial two");
    expect(userBubbles("Question one")).toHaveLength(1);
    expect(userBubbles("Question two")).toHaveLength(1);
    expect(chat.messages.get("t-recent")!.map((m) => m.content).slice(2)).toEqual([
      "Question one",
      "Reply one.",
      "Question two",
      "Partial two",
    ]);
  });

  it("after Stop, holds a follow-up until the kept partial is read back, so each turn shows once", async () => {
    const chat = standard([{ deltas: ["The watch "], hold: true, latePartial: true }, { deltas: ["At dawn."] }]);
    renderPanel(chat);
    await screen.findByText(/the keeper is a smuggler/);
    type("When does the watch change?");
    pressEnter();
    await screen.findByText("The watch");

    type("And at dawn?");
    fireEvent.click(screen.getByRole("button", { name: "Stop the Butler's reply" }));
    await waitFor(() => expect(chat.aborted).toEqual([true]));
    // The server has not saved the partial yet, and the first re-read comes
    // back without it: sending now would race it.
    await waitFor(() => expect(chat.calls.get).toBeGreaterThanOrEqual(2));
    pressEnter();
    expect(chat.exchanges).toHaveLength(1);

    act(() => chat.releasePartial());
    await waitFor(() => expect(screen.getByRole("button", { name: "Send" })).toBeEnabled(), { timeout: 3000 });
    pressEnter();
    await waitFor(() => expect(screen.getByText("At dawn.")).toBeInTheDocument());
    await waitFor(() => expect(document.querySelectorAll('[data-pending="true"]')).toHaveLength(0));

    expect(userBubbles("When does the watch change?")).toHaveLength(1);
    expect(userBubbles("And at dawn?")).toHaveLength(1);
    expect(chat.messages.get("t-recent")!.map((m) => m.content).slice(2)).toEqual([
      "When does the watch change?",
      "The watch",
      "And at dawn?",
      "At dawn.",
    ]);
    const marked = [...document.querySelectorAll(".gx-planning__partial")].map((p) => p.closest("li")?.textContent ?? "");
    expect(marked).toHaveLength(1);
    expect(marked[0]).toContain("The watch");
  });

  it("waits for the open conversation's messages before sending into it", async () => {
    const chat = standard();
    const openReads = chat.holdReads();
    renderPanel(chat);
    const rail = screen.getByRole("complementary", { name: "Planning conversations" });
    await waitFor(() => expect(within(rail).getByRole("button", { name: /^Harbour heist prep/ })).toHaveAttribute("aria-current", "true"));

    type("Who guards the docks?");
    pressEnter();
    expect(chat.exchanges).toHaveLength(0);
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();

    act(() => openReads());
    await screen.findByText(/the keeper is a smuggler/);
    pressEnter();
    await waitFor(() => expect(screen.getByText("Start with the docks.")).toBeInTheDocument());
    await waitFor(() => expect(userBubbles("Who guards the docks?")).toHaveLength(1));
    expect(chat.exchanges).toHaveLength(1);
  });

  it("a Stop before the server saved the message gives the words back to the composer", async () => {
    const chat = standard([{ stallBeforeSave: true }]);
    renderPanel(chat);
    await screen.findByText(/the keeper is a smuggler/);
    type("Who guards the docks?");
    pressEnter();
    await screen.findAllByText("The Butler is writing…");
    expect(composer().value).toBe("");

    fireEvent.click(screen.getByRole("button", { name: "Stop the Butler's reply" }));
    await waitFor(() => expect(composer().value).toBe("Who guards the docks?"), { timeout: 4000 });
    expect(userBubbles("Who guards the docks?")).toHaveLength(0);
    expect(chat.userTurns("t-recent", "Who guards the docks?")).toBe(0);
    expect(screen.getByRole("button", { name: "Send" })).toBeEnabled();
  });

  it("renders Butler replies as markdown but keeps the GM's own words verbatim", async () => {
    const chat = mockChat({
      threads: [recent()],
      messages: { "t-recent": [message("u", "user", "Is **this** rendered?"), message("a", "assistant", "The keeper is **definitely** a smuggler.")] },
    });
    renderPanel(chat);
    await screen.findByText(/a smuggler/);
    expect(document.querySelector('[data-role="assistant"] .gx-planning__reply strong')?.textContent).toBe("definitely");
    expect(screen.getByText("Is **this** rendered?")).toBeInTheDocument();
    expect(document.querySelector('[data-role="user"] strong')).toBeNull();
  });

  it("copies a reply as markdown", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    renderPanel(standard());
    await screen.findByText(/the keeper is a smuggler/);
    fireEvent.click(screen.getByRole("button", { name: "Copy this reply as Markdown" }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith("They learned the keeper is a smuggler."));
    expect(await screen.findByRole("button", { name: "Copied" })).toBeInTheDocument();
  });

  it("keeps the reply streaming across a visit to another view, without a duplicate turn", async () => {
    const chat = standard([{ deltas: ["The docks "], hold: true }]);
    const view = renderPanel(chat);
    await screen.findByText(/the keeper is a smuggler/);
    type("Who guards the docks?");
    pressEnter();
    expect(await screen.findByText("The docks")).toBeInTheDocument();

    view.leave();
    expect(screen.getByTestId("other-view")).toBeInTheDocument();
    expect(chat.aborted).toEqual([false]);

    view.remount();
    expect(await screen.findByText("The docks")).toBeInTheDocument();
    expect(userBubbles("Who guards the docks?")).toHaveLength(1);

    act(() => chat.release());
    await waitFor(() => expect(screen.getByRole("button", { name: "Send" })).toBeInTheDocument());
    await waitFor(() => expect(userBubbles("Who guards the docks?")).toHaveLength(1));
    expect(chat.exchanges).toHaveLength(1);
  });

  it("finishes the reply while the panel is away and shows it on return", async () => {
    const chat = standard([{ deltas: ["Night watch, mostly."], hold: true }]);
    const view = renderPanel(chat);
    await screen.findByText(/the keeper is a smuggler/);
    type("Who guards the docks?");
    pressEnter();
    await screen.findByText("Night watch, mostly.");

    view.leave();
    act(() => chat.release());
    await waitFor(() => expect(chat.messages.get("t-recent")).toHaveLength(4));
    view.remount();
    expect(await screen.findByText("Night watch, mostly.")).toBeInTheDocument();
    expect(userBubbles("Who guards the docks?")).toHaveLength(1);
  });

  it("Stop ends the stream and the kept partial reply replaces the local one", async () => {
    const chat = standard([{ deltas: ["The watch ", "changes at midnight"], hold: true }]);
    renderPanel(chat);
    await screen.findByText(/the keeper is a smuggler/);
    type("When does the watch change?");
    pressEnter();
    await screen.findByText("The watch changes at midnight");

    fireEvent.click(screen.getByRole("button", { name: "Stop the Butler's reply" }));
    // Stop turns into a disabled Send: focus goes to the composer, not <body>.
    expect(composer()).toHaveFocus();
    await waitFor(() => expect(chat.aborted).toEqual([true]));
    expect(document.querySelector(".gx-planning__partial")).toHaveTextContent("Stopped — this reply is incomplete.");
    expect(await screen.findByRole("button", { name: "Send" })).toBeInTheDocument();

    // The re-read brings the server's copy; the reply shows once, still marked.
    await waitFor(() => expect(chat.messages.get("t-recent")?.at(-1)?.content).toBe("The watch changes at midnight"));
    await waitFor(() => expect(document.querySelectorAll('[data-role="assistant"][data-pending="true"]')).toHaveLength(0), {
      timeout: 3000,
    });
    expect(screen.getAllByText("The watch changes at midnight")).toHaveLength(1);
    expect(document.querySelector(".gx-planning__partial")).toHaveTextContent("Stopped — this reply is incomplete.");
  });

  it("Try again after a failure re-sends the same text and the thread holds one user turn", async () => {
    const chat = standard([
      { deltas: ["The docks are"], fail: new ConnectError("the LLM provider is having trouble right now — try again shortly", Code.Unavailable) },
      { deltas: ["The docks are quiet."] },
    ]);
    renderPanel(chat);
    await screen.findByText(/the keeper is a smuggler/);
    type("Who guards the docks?");
    pressEnter();

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("The AI provider couldn't answer");
    expect(alert).toHaveTextContent("the LLM provider is having trouble right now");
    // What streamed before the failure stays, marked as incomplete.
    expect(screen.getByText("The docks are")).toBeInTheDocument();
    expect(screen.getByText("Incomplete — the reply broke off here.")).toBeInTheDocument();

    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(chat.exchanges).toHaveLength(2));
    expect(chat.exchanges[1].message).toBe("Who guards the docks?");
    expect(await screen.findByText("The docks are quiet.")).toBeInTheDocument();
    expect(chat.userTurns("t-recent", "Who guards the docks?")).toBe(1);
    await waitFor(() => expect(userBubbles("Who guards the docks?")).toHaveLength(1));
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("a rejected key shows the server's sentence with Open Setup, and closing it keeps the streamed text", async () => {
    const chat = standard([
      { deltas: ["Let me check"], fail: new ConnectError("the planning chat failed — check the LLM provider configuration and try again", Code.Unavailable) },
    ]);
    const onNavigate = vi.fn();
    renderPanel(chat, { onNavigate });
    await screen.findByText(/the keeper is a smuggler/);
    type("Recap please");
    pressEnter();

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("check the LLM provider configuration");
    fireEvent.click(within(alert).getByRole("button", { name: "Open Setup" }));
    expect(onNavigate).toHaveBeenCalledWith(expect.objectContaining({ screen: "configuration" }), { replace: false });

    // (The memory nav stays on this panel, which re-renders the notice.)
    fireEvent.click(within(await screen.findByRole("alert")).getByRole("button", { name: "Close" }));
    await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
    expect(screen.getByText("Let me check")).toBeInTheDocument();
  });

  it("a spent plan allowance says so and gives the message back", async () => {
    const chat = standard([
      { refuse: new ConnectError("SERVER ENGLISH", Code.ResourceExhausted, { "Glyphoxa-Error-Reason": "allowance_exhausted" }) },
    ]);
    renderPanel(chat);
    await screen.findByText(/the keeper is a smuggler/);
    type("Where should the session open?");
    pressEnter();

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Included usage used up");
    expect(alert).toHaveTextContent(/This month's included usage is used up/);
    // The gate is per plan: no retry, no trip to Setup.
    expect(within(alert).queryByRole("button", { name: "Try again" })).not.toBeInTheDocument();
    expect(within(alert).queryByRole("button", { name: "Open Setup" })).not.toBeInTheDocument();
    await waitFor(() => expect(composer().value).toBe("Where should the session open?"));
    expect(userBubbles("Where should the session open?")).toHaveLength(0);
  });

  it("a provider's own rate limit is not reported as a spent allowance", async () => {
    const chat = standard([
      {
        fail: new ConnectError(
          "your LLM provider is out of quota or rate-limited — check your plan, or try again shortly",
          Code.ResourceExhausted,
        ),
      },
    ]);
    renderPanel(chat);
    await screen.findByText(/the keeper is a smuggler/);
    type("Where should the session open?");
    pressEnter();
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("The AI provider couldn't answer");
    expect(alert).not.toHaveTextContent(/included usage/);
    // Persisted before the provider call: no duplicate in the composer.
    expect(composer().value).toBe("");
  });

  it("shows the tools the Butler used on the finished reply, and a way to a filed suggestion", async () => {
    const chat = standard([{ tools: [{ name: "recap" }, { name: "propose_knowledge" }], deltas: ["Noted for the wiki."] }]);
    const onNavigate = vi.fn();
    renderPanel(chat, { onNavigate });
    await screen.findByText(/the keeper is a smuggler/);
    type("Note this down for the wiki: Maren smuggles");
    pressEnter();

    await screen.findByText("Noted for the wiki.");
    await waitFor(() => expect(screen.getByRole("button", { name: "Send" })).toBeInTheDocument());
    // The chips stay on the persisted reply after the stream is gone.
    await waitFor(() => expect(document.querySelectorAll('[data-role="assistant"][data-pending="true"]')).toHaveLength(0));
    const chips = screen.getByRole("list", { name: "What the Butler looked up" });
    expect(within(chips).getAllByRole("listitem").map((li) => li.textContent)).toEqual(["Used session recap", "Used wiki suggestion"]);
    expect(screen.getByText("Suggestion filed")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Review the suggestion in Suggestions" }));
    expect(onNavigate).toHaveBeenCalledWith(expect.objectContaining({ view: "proposals" }), { replace: false });
  });

  it("points at a reply streaming in another conversation", async () => {
    const chat = standard([{ deltas: ["Thinking"], hold: true }]);
    renderPanel(chat);
    await screen.findByText(/the keeper is a smuggler/);
    type("Plan the heist");
    pressEnter();
    await screen.findByText("Thinking");

    const rail = screen.getByRole("complementary", { name: "Planning conversations" });
    fireEvent.click(within(rail).getByRole("button", { name: /^Act two loose ends/ }));
    const chip = await screen.findByText("The Butler is replying in “Harbour heist prep”.");
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
    fireEvent.click(within(chip.parentElement!).getByRole("button", { name: "Open it" }));
    expect(await screen.findByText("Thinking")).toBeInTheDocument();
  });
});

describe("PlanningPanel composer", () => {
  it("counts in code points, shows the counter near the limit and blocks Send past it", async () => {
    const chat = standard();
    renderPanel(chat);
    await screen.findByText(/the keeper is a smuggler/);

    type("x".repeat(3400));
    expect(screen.queryByText(/\/ 4,000/)).not.toBeInTheDocument();

    // Emoji are two UTF-16 units but one code point each, as the server counts.
    type("🐉".repeat(4000));
    expect(screen.getByText("4,000 / 4,000")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Send" })).toBeEnabled();

    type("y".repeat(5000));
    const counter = screen.getByText(/5,000 \/ 4,000/);
    expect(counter).toHaveAttribute("data-over", "true");
    expect(counter).toHaveTextContent("1,000 characters too long");
    expect(composer()).toHaveAttribute("aria-invalid", "true");
    expect(composer().value).toHaveLength(5000); // nothing silently cut off
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
    pressEnter();
    expect(chat.exchanges).toHaveLength(0);
  });

  it("writes the counter's numbers the German way", async () => {
    localStorage.setItem("gx-lang", "de");
    try {
      renderPanel(standard());
      await screen.findByText(/the keeper is a smuggler/);
      fireEvent.change(document.querySelector("textarea")!, { target: { value: "x".repeat(4000) } });
      expect(screen.getByText("4.000 / 4.000")).toBeInTheDocument();
    } finally {
      localStorage.removeItem("gx-lang");
    }
  });

  it("keeps a draft per conversation", async () => {
    renderPanel(standard());
    await screen.findByText(/the keeper is a smuggler/);
    type("Half a thought about the keeper");

    const rail = screen.getByRole("complementary", { name: "Planning conversations" });
    fireEvent.click(within(rail).getByRole("button", { name: /^Act two loose ends/ }));
    await waitFor(() => expect(composer().value).toBe(""));
    type("Something for act two");

    fireEvent.click(within(rail).getByRole("button", { name: /^Harbour heist prep/ }));
    await waitFor(() => expect(composer().value).toBe("Half a thought about the keeper"));
    fireEvent.click(within(rail).getByRole("button", { name: /^Act two loose ends/ }));
    await waitFor(() => expect(composer().value).toBe("Something for act two"));
  });

  it("keeps the draft through a reload of the panel", async () => {
    const chat = standard();
    const view = renderPanel(chat);
    await screen.findByText(/the keeper is a smuggler/);
    type("Unfinished");
    view.remount();
    await screen.findByText(/the keeper is a smuggler/);
    expect(composer().value).toBe("Unfinished");
  });
});
