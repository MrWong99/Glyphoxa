import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createClient } from "@connectrpc/connect";
import { useTransport } from "@connectrpc/connect-query";

import { VoiceService } from "@gen/glyphoxa/management/v1/management_pb";
import { useI18n } from "@/i18n";
import { playAudioBlob } from "@/lib/audio";
import { apiErrorMessage } from "@/lib/connectError";

// useVoicePreview plays ONE voice sample at a time (VoiceService.PreviewVoice,
// then the WAV it returns) and can stop it: the Preview button in the agent
// editor and every row of the voice picker go through it.
//
// A preview is paid text-to-speech (the server writes its characters to the
// Usage Ledger), so nothing here ever starts by itself: `play` is only called
// from a button press. Starting a second sample stops the first, `stop` ends
// whatever is loading or playing, and unmounting stops it too — a sample must
// not keep talking after the dialog that started it has closed.
//
// The request carries an AbortSignal, so a sample that is stopped while it is
// still being synthesized is cancelled rather than fetched and thrown away.

/** The longest sample line the server accepts (internal/rpc/voice.go maxPreviewRunes). */
export const MAX_PREVIEW_CHARS = 300;

export type VoicePreview = {
  /** The voice whose sample is loading or playing, if any. */
  activeId: string | null;
  phase: "idle" | "loading" | "playing";
  /** The last failure, already worded for the GM; cleared by the next play. */
  error: string | null;
  /** Starts a sample of `voiceId` speaking `text` (empty: the server's standard line). */
  play: (voiceId: string, text?: string) => void;
  /** Stops whatever is loading or playing. */
  stop: () => void;
  /** `stop`, and forgets the last failure (a dialog that closes starts clean next time). */
  reset: () => void;
  /** `stop` when `voiceId` is the active sample, `play` otherwise. */
  toggle: (voiceId: string, text?: string) => void;
};

export function useVoicePreview(): VoicePreview {
  const { t } = useI18n();
  const transport = useTransport();
  const client = useMemo(() => createClient(VoiceService, transport), [transport]);

  const [state, setState] = useState<{ activeId: string | null; phase: VoicePreview["phase"] }>({
    activeId: null,
    phase: "idle",
  });
  const [error, setError] = useState<string | null>(null);

  // The sample in flight: aborting it cancels the request AND the playback.
  const current = useRef<AbortController | null>(null);
  const activeId = useRef<string | null>(null);

  const stop = useCallback(() => {
    current.current?.abort();
    current.current = null;
    activeId.current = null;
    setState({ activeId: null, phase: "idle" });
  }, []);

  const play = useCallback(
    (voiceId: string, text = "") => {
      if (!voiceId) return;
      current.current?.abort();
      const run = new AbortController();
      current.current = run;
      activeId.current = voiceId;
      setError(null);
      setState({ activeId: voiceId, phase: "loading" });

      // Everything after an await checks that this run is still the current
      // one: a stopped or superseded sample must not touch the state.
      const live = () => current.current === run;
      const finish = () => {
        if (!live()) return;
        current.current = null;
        activeId.current = null;
        setState({ activeId: null, phase: "idle" });
      };

      void (async () => {
        try {
          const res = await client.previewVoice({ voiceId, text }, { signal: run.signal });
          if (!live()) return;
          setState({ activeId: voiceId, phase: "playing" });
          await playAudioBlob(res.audio, res.mimeType, { signal: run.signal, onEnded: finish });
        } catch (err) {
          if (!live()) return;
          finish();
          setError(apiErrorMessage(err, t));
        }
      })();
    },
    [client, t],
  );

  const reset = useCallback(() => {
    stop();
    setError(null);
  }, [stop]);

  const toggle = useCallback(
    (voiceId: string, text = "") => {
      if (activeId.current === voiceId) stop();
      else play(voiceId, text);
    },
    [play, stop],
  );

  // Unmount: silence, and no state update on the way out.
  useEffect(
    () => () => {
      current.current?.abort();
      current.current = null;
    },
    [],
  );

  return { activeId: state.activeId, phase: state.phase, error, play, stop, reset, toggle };
}
