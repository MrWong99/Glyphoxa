import { useEffect, useRef, useState } from "react";
import { Check, Copy } from "lucide-react";
import { toast } from "sonner";

import { useI18n } from "@/i18n";
import { IconButton } from "./IconButton";

// CopyButton copies a piece of text (a Butler reply as Markdown, one code
// block) to the clipboard. The confirmation is the button itself: its icon and
// its name turn into "Copied" for two seconds, which a screen reader hears on
// the focused button. A refused clipboard (no permission, an insecure origin)
// says so in a toast instead of failing silently.

const COPIED_MS = 2000;

export function CopyButton({
  text,
  label,
  showLabel = false,
  ariaLabel,
  className = "",
}: {
  text: string;
  /** The button's name before copying ("Copy", "Copy code"). */
  label?: string;
  /** Show the label as text beside the icon, not only as the accessible name. */
  showLabel?: boolean;
  /** A fuller accessible name for the labelled button ("Copy this reply as Markdown"). */
  ariaLabel?: string;
  className?: string;
}) {
  const { t } = useI18n();
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current);
  }, []);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      toast.error(t("chat.copyFailed"));
      return;
    }
    setCopied(true);
    if (timer.current) clearTimeout(timer.current);
    timer.current = setTimeout(() => setCopied(false), COPIED_MS);
  };

  const name = copied ? t("chat.copied") : (label ?? t("chat.copy"));
  const icon = copied ? <Check size={14} aria-hidden="true" /> : <Copy size={14} aria-hidden="true" />;

  if (showLabel) {
    return (
      <button
        type="button"
        className={className ? `gx-copy ${className}` : "gx-copy"}
        data-copied={copied ? "true" : undefined}
        aria-label={copied ? name : ariaLabel}
        onClick={() => void copy()}
      >
        {icon}
        <span>{name}</span>
      </button>
    );
  }
  return (
    <IconButton
      label={name}
      size="sm"
      tooltip={false}
      className={className}
      data-copied={copied ? "true" : undefined}
      onClick={() => void copy()}
    >
      {icon}
    </IconButton>
  );
}
