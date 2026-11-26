import { Copy, RotateCw } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Markdown } from "@/components/ui/Markdown";
import { Spinner } from "@/components/ui/Spinner";
import { useI18n } from "@/i18n";
import { formatStamp, formatTime } from "./sessionFormat";
import type { Recap } from "./useRecap";

/** Copies text to the clipboard and says so; a browser that refuses is reported, not ignored. */
export async function copyText(text: string, okMessage: string, failMessage: string): Promise<void> {
  try {
    await navigator.clipboard.writeText(text);
    toast.success(okMessage);
  } catch {
    toast.error(failMessage);
  }
}

// RecapCard shows a session's recap (#274): the pending row while GenerateRecap
// is in flight — it can take two minutes, so the row says so — and the result,
// labelled with the session it covers and when it was written, with Copy and
// Regenerate. Nothing renders before the first recap of a session.
//
// A recap is kept only for as long as this tab is open (useRecap.ts); the note
// under it says that, so "Copy" reads as the way to keep it.
export function RecapCard({
  recap,
  pending,
  sessionStartedMs,
  onRegenerate,
}: {
  recap: Recap | undefined;
  pending: boolean;
  /** When the covered session started, for the label; null when unknown. */
  sessionStartedMs: number | null;
  onRegenerate: () => void;
}) {
  const { t, lang } = useI18n();

  if (!recap && !pending) return null;

  const stamp = sessionStartedMs != null ? formatStamp(new Date(sessionStartedMs), lang) : "—";

  return (
    <Card className="gx-session__recap" data-testid={recap ? "recap-result" : undefined}>
      <div className="gx-session__recap-head">
        <div className="gx-session__recap-label">{t("session.recapLabel", { stamp })}</div>
        {recap && (
          <div className="gx-session__recap-actions">
            <Button
              variant="ghost"
              size="sm"
              iconStart={<Copy size={14} />}
              onClick={() => void copyText(recap.text, t("session.recapCopied"), t("session.copyFailed"))}
            >
              {t("session.copy")}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              iconStart={<RotateCw size={14} />}
              onClick={onRegenerate}
              loading={pending}
              data-testid="recap-regenerate"
            >
              {t("session.recapRegenerate")}
            </Button>
          </div>
        )}
      </div>
      {pending && (
        <div className="gx-session__recap-pending" role="status" data-testid="recap-pending">
          <Spinner />
          <span>{t("session.generatingRecap")}</span>
          <span className="gx-session__recap-hint">{t("session.recapHint")}</span>
        </div>
      )}
      {recap && (
        <>
          {/* Recap prose is Butler output — rendered as markdown like the planning
              chat's bubbles. */}
          <Markdown text={recap.text} className="gx-session__recap-text" />
          <p className="gx-session__recap-note">
            {t("session.recapKeptNote", { time: formatTime(new Date(recap.generatedAt), lang) })}
          </p>
        </>
      )}
    </Card>
  );
}
