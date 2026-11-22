import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { toast, Toaster } from "sonner";
import { afterEach, describe, expect, it, vi } from "vitest";

import { I18nProvider } from "@/i18n";
import { toastUndo } from "./toast";

// The real sonner host, mounted the way AppShell mounts it.
function Host() {
  return <Toaster theme="dark" position="bottom-right" richColors />;
}

afterEach(() => {
  act(() => {
    toast.dismiss();
  });
  localStorage.removeItem("gx-lang");
});

describe("toastUndo", () => {
  it("shows the message with an Undo action", async () => {
    render(<Host />);
    act(() => {
      toastUndo("Removed Flo from the cast", () => {}, { description: "Their lines stay in the transcript." });
    });
    expect(await screen.findByText("Removed Flo from the cast")).toBeInTheDocument();
    expect(screen.getByText("Their lines stay in the transcript.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Undo" })).toBeInTheDocument();
  });

  it("runs onUndo once when Undo is pressed, and closes the toast", async () => {
    const onUndo = vi.fn();
    render(<Host />);
    act(() => {
      toastUndo("Removed Flo from the cast", onUndo);
    });
    const undo = await screen.findByRole("button", { name: "Undo" });
    fireEvent.click(undo);
    fireEvent.click(undo);
    expect(onUndo).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(screen.queryByText("Removed Flo from the cast")).toBeNull());
  });

  it("stays up for eight seconds by default and for the given duration otherwise", () => {
    render(<Host />);
    const ids: Array<string | number> = [];
    act(() => {
      ids.push(toastUndo("default", () => {}));
      ids.push(toastUndo("short", () => {}, { duration: 3000 }));
    });
    // sonner keeps what each toast was created with; read the duration there.
    const durations = ids.map((id) => {
      const shown = toast.getToasts().find((entry) => entry.id === id);
      return shown && "duration" in shown ? shown.duration : undefined;
    });
    expect(durations).toEqual([8000, 3000]);
  });

  it("returns the toast id, so the caller can close it early", async () => {
    render(<Host />);
    let id: string | number = "";
    act(() => {
      id = toastUndo("Removed Flo from the cast", () => {});
    });
    expect(await screen.findByText("Removed Flo from the cast")).toBeInTheDocument();
    act(() => {
      toast.dismiss(id);
    });
    await waitFor(() => expect(screen.queryByText("Removed Flo from the cast")).toBeNull());
  });

  it("labels the action in the display language without being handed a translator", async () => {
    localStorage.setItem("gx-lang", "de");
    render(
      <I18nProvider>
        <Host />
      </I18nProvider>,
    );
    act(() => {
      toastUndo("Flo wurde entfernt", () => {});
    });
    expect(await screen.findByRole("button", { name: "Rückgängig" })).toBeInTheDocument();
  });
});
