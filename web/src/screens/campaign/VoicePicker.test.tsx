import { useState } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { Code, ConnectError, createRouterTransport } from "@connectrpc/connect";
import { useQuery } from "@connectrpc/connect-query";
import { create } from "@bufbuild/protobuf";

import {
  ListVoicesResponseSchema,
  PreviewVoiceResponseSchema,
  VoiceSchema,
  VoiceService,
} from "@gen/glyphoxa/management/v1/management_pb";
import { Providers } from "@/app/Providers";
import { MemoryScreenNav, type ScreenLocation } from "@/app/screenNav";
import { makeQueryClient } from "@/lib/queryClient";
import { VoicePicker } from "./VoicePicker";

const VOICES = [
  create(VoiceSchema, { provider: "elevenlabs", voiceId: "rachel", name: "Rachel", language: "en", label: "ElevenLabs · Rachel" }),
  create(VoiceSchema, { provider: "elevenlabs", voiceId: "marcus", name: "Marcus", language: "en-GB", label: "ElevenLabs · Marcus" }),
  create(VoiceSchema, { provider: "elevenlabs", voiceId: "greta", name: "Greta", language: "de", label: "ElevenLabs · Greta" }),
];

// A real <audio> node with a controllable play()/pause(), as in lib/audio.test:
// jsdom has no media pipeline, and "the sample stopped" is only observable on
// the element (paused, released from the document).
const players: { el: HTMLAudioElement; pause: ReturnType<typeof vi.fn> }[] = [];
let playImpl: (el: HTMLAudioElement) => Promise<void> = () => Promise.resolve();
function FakeAudio(src: string): HTMLAudioElement {
  const el = document.createElement("audio");
  el.src = src;
  const pause = vi.fn();
  Object.defineProperty(el, "play", { value: () => playImpl(el), configurable: true });
  Object.defineProperty(el, "pause", { value: pause, configurable: true });
  players.push({ el, pause });
  return el;
}

beforeEach(() => {
  players.length = 0;
  playImpl = () => Promise.resolve();
  vi.stubGlobal("Audio", FakeAudio);
  (URL as unknown as Record<string, unknown>).createObjectURL = () => "blob:preview";
  (URL as unknown as Record<string, unknown>).revokeObjectURL = () => {};
});

afterEach(() => {
  vi.unstubAllGlobals();
  delete (URL as unknown as Record<string, unknown>).createObjectURL;
  delete (URL as unknown as Record<string, unknown>).revokeObjectURL;
});

type Preview = { voiceId: string; text: string };

function renderPicker(
  opts: {
    value?: string;
    campaignLanguage?: string;
    voices?: typeof VOICES;
    /** Fail the catalog read this many times before answering. */
    failCatalog?: number;
    previewError?: boolean;
    /** Hold every preview until released (the sample is still being synthesized). */
    holdPreviews?: boolean;
  } = {},
) {
  const previews: Preview[] = [];
  const onSelect = vi.fn();
  const moves: ScreenLocation[] = [];
  const held: (() => void)[] = [];
  let catalogFailures = opts.failCatalog ?? 0;
  const transport = createRouterTransport(({ service }) => {
    service(VoiceService, {
      listVoices: () => {
        if (catalogFailures > 0) {
          catalogFailures -= 1;
          throw new ConnectError("voice catalog is unavailable", Code.Unavailable);
        }
        return create(ListVoicesResponseSchema, { voices: opts.voices ?? VOICES });
      },
      previewVoice: async (req) => {
        previews.push({ voiceId: req.voiceId, text: req.text });
        if (opts.previewError) throw new ConnectError("voice preview is unavailable", Code.Unavailable);
        if (opts.holdPreviews) await new Promise<void>((resolve) => held.push(resolve));
        return create(PreviewVoiceResponseSchema, { audio: new Uint8Array([1, 2, 3]), mimeType: "audio/wav" });
      },
    });
  });

  function Harness() {
    const voicesQuery = useQuery(VoiceService.method.listVoices, {});
    const [open, setOpen] = useState(true);
    const [value, setValue] = useState(opts.value ?? "rachel");
    return (
      <>
        <button type="button" onClick={() => setOpen(true)}>
          reopen
        </button>
        <output data-testid="value">{value}</output>
        <VoicePicker
          open={open}
          onOpenChange={setOpen}
          voicesQuery={voicesQuery}
          value={value}
          onSelect={(id) => {
            onSelect(id);
            setValue(id);
          }}
          campaignLanguage={opts.campaignLanguage}
        />
      </>
    );
  }

  const queryClient = makeQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  render(
    <Providers transport={transport} queryClient={queryClient}>
      <MemoryScreenNav initial={{ screen: "campaign", view: "cast" }} onNavigate={(next) => moves.push(next)}>
        <Harness />
      </MemoryScreenNav>
    </Providers>,
  );
  return { previews, onSelect, moves, release: () => held.splice(0).forEach((resolve) => resolve()) };
}

const dialog = () => screen.getByRole("dialog", { name: "Choose a voice" });
const rows = () => within(screen.getByRole("list", { name: "Voices" })).getAllByRole("listitem");
const rowNames = () => rows().map((li) => li.querySelector(".gx-cast-voices__name")?.textContent);
// The row's own button ("Marcus British English"), not its "Play Marcus" one.
const pick = (name: string) => screen.getByRole("button", { name: new RegExp(`^${name}\\b`) });

describe("VoicePicker", () => {
  it("lists every voice with its name and language, the current one first and marked", async () => {
    renderPicker({ value: "marcus" });
    await screen.findByRole("list", { name: "Voices" });
    // The form's voice leads; the rest keep the catalog's order.
    expect(rowNames()).toEqual(["Marcus", "Rachel", "Greta"]);
    expect(rows()[0]).toHaveTextContent("British English");
    expect(rows()[0]).toHaveTextContent("Current");
    expect(rows()[2]).toHaveTextContent("German");
    // The current voice is the one chosen to start with.
    expect(pick("Marcus")).toHaveAttribute("aria-pressed", "true");
    expect(pick("Rachel")).toHaveAttribute("aria-pressed", "false");
  });

  // A preview is paid text-to-speech: nothing may start without a button press.
  it("never plays by itself: not on opening, not on choosing a row", async () => {
    const { previews } = renderPicker();
    await screen.findByRole("list", { name: "Voices" });
    fireEvent.click(pick("Marcus"));
    fireEvent.click(pick("Greta"));
    await act(() => new Promise((resolve) => setTimeout(resolve, 20)));
    expect(previews).toEqual([]);
  });

  it("plays a row's own voice with the typed sample line, without changing the selection", async () => {
    const { previews, onSelect } = renderPicker({ value: "rachel" });
    await screen.findByRole("list", { name: "Voices" });
    fireEvent.change(screen.getByLabelText("Sample line"), { target: { value: "  Welcome to the Tankard.  " } });

    fireEvent.click(screen.getByRole("button", { name: "Play Marcus" }));
    await waitFor(() => expect(previews).toEqual([{ voiceId: "marcus", text: "Welcome to the Tankard." }]));

    // Listening is not choosing: Rachel is still the chosen row, the form was
    // not touched, and there is nothing to confirm yet.
    expect(pick("Rachel")).toHaveAttribute("aria-pressed", "true");
    expect(pick("Marcus")).toHaveAttribute("aria-pressed", "false");
    expect(onSelect).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Use this voice" })).toBeDisabled();
  });

  it("sends an empty sample line as empty, so the server speaks its standard line", async () => {
    const { previews } = renderPicker();
    await screen.findByRole("list", { name: "Voices" });
    fireEvent.click(screen.getByRole("button", { name: "Play Greta" }));
    await waitFor(() => expect(previews).toEqual([{ voiceId: "greta", text: "" }]));
  });

  it("turns the play button into Stop while its sample plays, and stops on press", async () => {
    renderPicker();
    await screen.findByRole("list", { name: "Voices" });
    fireEvent.click(screen.getByRole("button", { name: "Play Marcus" }));

    const stop = await screen.findByRole("button", { name: "Stop Marcus" });
    await waitFor(() => expect(players).toHaveLength(1));
    expect(players[0].el.isConnected).toBe(true);

    fireEvent.click(stop);
    expect(players[0].pause).toHaveBeenCalled();
    expect(players[0].el.isConnected).toBe(false);
    expect(screen.getByRole("button", { name: "Play Marcus" })).toBeInTheDocument();
  });

  it("plays one sample at a time: starting another stops the first", async () => {
    renderPicker();
    await screen.findByRole("list", { name: "Voices" });
    fireEvent.click(screen.getByRole("button", { name: "Play Marcus" }));
    await waitFor(() => expect(players).toHaveLength(1));

    fireEvent.click(screen.getByRole("button", { name: "Play Greta" }));
    await waitFor(() => expect(players).toHaveLength(2));
    expect(players[0].pause).toHaveBeenCalled();
    expect(players[0].el.isConnected).toBe(false);
    expect(players[1].el.isConnected).toBe(true);
    // Only the second one offers Stop.
    expect(screen.getByRole("button", { name: "Stop Greta" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Play Marcus" })).toBeInTheDocument();
  });

  it("stops the sample when the dialog closes", async () => {
    renderPicker();
    await screen.findByRole("list", { name: "Voices" });
    fireEvent.click(screen.getByRole("button", { name: "Play Marcus" }));
    await waitFor(() => expect(players).toHaveLength(1));

    fireEvent.click(within(dialog()).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(players[0].pause).toHaveBeenCalled();
    expect(players[0].el.isConnected).toBe(false);
  });

  // A sample still being synthesized when the dialog closes must not start
  // talking afterwards.
  it("drops a sample that was still loading when the dialog closed", async () => {
    const { previews, release } = renderPicker({ holdPreviews: true });
    await screen.findByRole("list", { name: "Voices" });
    fireEvent.click(screen.getByRole("button", { name: "Play Marcus" }));
    await waitFor(() => expect(previews).toHaveLength(1));
    // Still pressable while loading: the same button cancels.
    expect(screen.getByRole("button", { name: "Stop Marcus" })).toBeEnabled();

    fireEvent.keyDown(dialog(), { key: "Escape" });
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    release();
    await act(() => new Promise((resolve) => setTimeout(resolve, 20)));
    expect(players).toHaveLength(0);
  });

  it("says so when a sample cannot be played", async () => {
    renderPicker({ previewError: true });
    await screen.findByRole("list", { name: "Voices" });
    fireEvent.click(screen.getByRole("button", { name: "Play Marcus" }));
    expect(await within(dialog()).findByRole("alert")).toHaveTextContent(/couldn't preview.*unavailable/i);
    // And the row is ready for another try.
    expect(screen.getByRole("button", { name: "Play Marcus" })).toBeInTheDocument();
  });

  // A clip the browser cannot play: the element fires "error" first, then
  // play() rejects (the media failure steps). The GM must still be told (#154).
  it("says so when the returned clip cannot be played", async () => {
    playImpl = (el) => {
      el.dispatchEvent(new Event("error"));
      return Promise.reject(new DOMException("The element has no supported sources.", "NotSupportedError"));
    };
    renderPicker();
    await screen.findByRole("list", { name: "Voices" });
    fireEvent.click(screen.getByRole("button", { name: "Play Marcus" }));
    expect(await within(dialog()).findByRole("alert")).toHaveTextContent(/couldn't preview.*no supported sources/i);
    expect(screen.getByRole("button", { name: "Play Marcus" })).toBeInTheDocument();
    expect(players[0].el.isConnected).toBe(false);
  });

  it("hands the chosen voice to the form only on confirm, and closes", async () => {
    const { onSelect } = renderPicker({ value: "rachel" });
    await screen.findByRole("list", { name: "Voices" });
    fireEvent.click(pick("Greta"));
    expect(onSelect).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Use this voice" }));
    expect(onSelect).toHaveBeenCalledWith("greta");
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.getByTestId("value")).toHaveTextContent("greta");
  });

  it("cancelling keeps the form's voice, and the next opening starts from it", async () => {
    const { onSelect } = renderPicker({ value: "rachel" });
    await screen.findByRole("list", { name: "Voices" });
    fireEvent.click(pick("Greta"));
    fireEvent.change(screen.getByLabelText("Search voices"), { target: { value: "gre" } });
    fireEvent.click(within(dialog()).getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(onSelect).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "reopen" }));
    await screen.findByRole("list", { name: "Voices" });
    expect(pick("Rachel")).toHaveAttribute("aria-pressed", "true");
    expect(rowNames()).toEqual(["Rachel", "Marcus", "Greta"]);
  });

  it("filters by name and by language as the GM types", async () => {
    renderPicker();
    await screen.findByRole("list", { name: "Voices" });
    const search = screen.getByLabelText("Search voices");

    fireEvent.change(search, { target: { value: "mar" } });
    expect(rowNames()).toEqual(["Marcus"]);
    fireEvent.change(search, { target: { value: "german" } });
    expect(rowNames()).toEqual(["Greta"]);

    fireEvent.change(search, { target: { value: "zzz" } });
    expect(screen.getByText("No voice matches your search.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Show all voices" }));
    expect(rowNames()).toEqual(["Rachel", "Marcus", "Greta"]);
  });

  it("offers a campaign-language chip that narrows the list to that language", async () => {
    renderPicker({ campaignLanguage: "en" });
    await screen.findByRole("list", { name: "Voices" });
    const chip = screen.getByRole("button", { name: "Campaign language: English" });
    // Off to begin with: no voice is hidden unasked.
    expect(chip).toHaveAttribute("aria-pressed", "false");
    expect(rowNames()).toEqual(["Rachel", "Marcus", "Greta"]);

    fireEvent.click(chip);
    expect(chip).toHaveAttribute("aria-pressed", "true");
    // "en-GB" is English too.
    expect(rowNames()).toEqual(["Rachel", "Marcus"]);
  });

  it("offers no language chip when the catalog names no languages", async () => {
    renderPicker({
      campaignLanguage: "en",
      voices: [create(VoiceSchema, { voiceId: "x", name: "Xan" })],
    });
    await screen.findByRole("list", { name: "Voices" });
    expect(screen.queryByRole("button", { name: /Campaign language/ })).not.toBeInTheDocument();
  });

  it("says why when the catalog cannot be loaded, and recovers on Try again", async () => {
    renderPicker({ failCatalog: 1 });
    const alert = await within(dialog()).findByRole("alert");
    expect(alert).toHaveTextContent("Couldn't load the voice catalog");
    fireEvent.click(within(alert).getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("list", { name: "Voices" })).toBeInTheDocument();
  });

  it("points to Setup when the catalog is empty", async () => {
    const { moves } = renderPicker({ voices: [] });
    expect(await screen.findByText("No voices available")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Open Setup" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(moves.at(-1)?.screen).toBe("configuration");
  });
});
