"use client";

import { useState } from "react";
import Link from "next/link";
import { X } from "lucide-react";
import { Button } from "@/components/ui/button";

const COOKIE_NOTICE_KEY = "glyphoxa_cookie_notice_dismissed";

export function CookieNotice() {
  const [dismissed, setDismissed] = useState(() => {
    if (typeof window === "undefined") return true;
    return !!localStorage.getItem(COOKIE_NOTICE_KEY);
  });

  function dismiss() {
    localStorage.setItem(COOKIE_NOTICE_KEY, "1");
    setDismissed(true);
  }

  if (dismissed) return null;

  return (
    <div
      role="status"
      aria-label="Cookie notice"
      className="fixed bottom-0 left-0 right-0 z-50 border-t border-border/50 bg-card/95 backdrop-blur-sm"
    >
      <div className="mx-auto flex max-w-4xl items-center gap-4 px-4 py-3 sm:px-6">
        <p className="flex-1 text-sm text-muted-foreground">
          This site uses only functional cookies and localStorage to keep you signed in. No tracking
          or analytics cookies are used.{" "}
          <Link href="/privacy#cookies" className="text-primary hover:underline">
            Learn more
          </Link>
        </p>
        <Button
          variant="outline"
          size="sm"
          onClick={dismiss}
          className="shrink-0"
        >
          Got it
        </Button>
        <button
          onClick={dismiss}
          className="shrink-0 rounded-md p-1 text-muted-foreground hover:text-foreground"
          aria-label="Dismiss cookie notice"
        >
          <X className="h-4 w-4" />
        </button>
      </div>
    </div>
  );
}
