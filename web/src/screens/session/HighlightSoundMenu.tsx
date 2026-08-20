import { useState } from "react";
import { useMutation } from "@connectrpc/connect-query";
import { useQueryClient } from "@tanstack/react-query";
import { Music, Trash2 } from "lucide-react";
import { toast } from "sonner";

import { SessionService } from "@gen/glyphoxa/management/v1/management_pb";
import type { Highlight } from "@gen/glyphoxa/management/v1/management_pb";
import { Button } from "@/components/ui/Button";
import { invalidateMethodQueries } from "@/lib/queryClient";
import { useI18n } from "@/i18n";

// HighlightSoundMenu is the GM's opt-in "Add sound" action for ONE promoted
// Session Highlight (#312, Epic 8): pick a Sting (a short sound effect layered
// under the clip) or a Music track (a composed instrumental theme), generated
// asynchronously by ElevenLabs and attached as a separate audio next to the
// clip. None is the default; re-running the action regenerates or changes the
// choice, and Remove clears it — all usable any time after promotion, from the
// live strip or the archive alike (the strip renders it wherever it renders).
//
// The dialog is the ShareHighlightDialog expand-in-place shape: collapsed to a
// single button, expanded to a compact labeled group. An unconfigured tenant
// (no ElevenLabs tts provider) gets the server's FailedPrecondition surfaced
// as a toast at click time, never a silent forever-pending state.
export function HighlightSoundMenu({ highlight }: { highlight: Highlight }) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const queryClient = useQueryClient();

  // A choice must refresh the list read (the single state tree, ADR-0018): the
  // sound triad clears server-side and the strip's pending state keys off it.
  const invalidate = () =>
    void invalidateMethodQueries(queryClient, SessionService.method.listHighlights);

  const setSound = useMutation(SessionService.method.setHighlightSound, {
    onSuccess: () => {
      invalidate();
      setOpen(false);
    },
    onError: (err: Error) => toast.error(t("session.couldntSetSound", { message: err.message })),
  });

  const choose = (kind: string) => {
    setSound.mutate(
      { id: highlight.id, kind },
      {
        onSuccess: () =>
          toast.success(kind === "" ? t("session.soundRemoved") : t("session.soundRequested")),
      },
    );
  };

  const hasChoice = highlight.soundKind !== "";
  if (!open) {
    return (
      <Button
        variant="secondary"
        size="sm"
        iconStart={<Music size={14} />}
        onClick={() => setOpen(true)}
      >
        {hasChoice
          ? t("session.soundChoice", {
              kind:
                highlight.soundKind === "music" ? t("session.soundMusic") : t("session.soundSting"),
            })
          : t("session.addSound")}
      </Button>
    );
  }

  return (
    <div className="gx-highlight-sound" role="group" aria-label={t("session.soundMenuLabel")}>
      <p className="gx-highlight-sound__hint">{t("session.soundHint")}</p>
      <div className="gx-highlight-sound__choices">
        <Button
          variant={highlight.soundKind === "sting" ? "primary" : "secondary"}
          size="sm"
          onClick={() => choose("sting")}
          disabled={setSound.isPending}
        >
          {t("session.soundSting")}
        </Button>
        <Button
          variant={highlight.soundKind === "music" ? "primary" : "secondary"}
          size="sm"
          onClick={() => choose("music")}
          disabled={setSound.isPending}
        >
          {t("session.soundMusic")}
        </Button>
        {hasChoice && (
          <Button
            variant="ghost"
            size="sm"
            iconStart={<Trash2 size={14} />}
            onClick={() => choose("")}
            disabled={setSound.isPending}
          >
            {t("session.removeSound")}
          </Button>
        )}
        <Button variant="ghost" size="sm" onClick={() => setOpen(false)}>
          {t("common.close")}
        </Button>
      </div>
    </div>
  );
}
