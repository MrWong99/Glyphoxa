"use client";

import { Suspense, useEffect, useState } from "react";
import { useSearchParams } from "next/navigation";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Loader2, Sparkles, AlertCircle } from "lucide-react";
import { api } from "@/lib/api";

function InviteHandler() {
  const searchParams = useSearchParams();
  const token = searchParams.get("token");
  const [status, setStatus] = useState<"loading" | "valid" | "invalid">(!token ? "invalid" : "loading");
  const [inviteInfo, setInviteInfo] = useState<{ role: string; tenant_id: string } | null>(null);

  useEffect(() => {
    if (!token) return;
    api.invites
      .validate(token)
      .then((data) => {
        setInviteInfo(data);
        setStatus("valid");
      })
      .catch(() => setStatus("invalid"));
  }, [token]);

  if (status === "loading") {
    return (
      <div className="flex flex-col items-center gap-3 text-muted-foreground">
        <Loader2 className="h-8 w-8 animate-spin" />
        <p>Validating invite...</p>
      </div>
    );
  }

  if (status === "invalid" || !token) {
    return (
      <Card className="w-full max-w-md border-border/50 shadow-xl">
        <CardContent className="flex flex-col items-center gap-4 py-8">
          <AlertCircle className="h-12 w-12 text-destructive" />
          <p className="text-center text-muted-foreground">
            This invite link is invalid, expired, or has already been used.
          </p>
          <Button variant="outline" onClick={() => (window.location.href = "/login")}>
            Go to Login
          </Button>
        </CardContent>
      </Card>
    );
  }

  return (
    <Card className="w-full max-w-md border-border/50 shadow-xl shadow-primary/5">
      <CardHeader>
        <div className="mx-auto mb-2 flex h-12 w-12 items-center justify-center rounded-2xl bg-primary/10">
          <Sparkles className="h-6 w-6 text-primary" />
        </div>
        <CardTitle className="text-center text-xl">You&apos;re Invited!</CardTitle>
      </CardHeader>
      <CardContent className="space-y-4 text-center">
        <p className="text-muted-foreground">
          You&apos;ve been invited to join <strong>{inviteInfo?.tenant_id}</strong> as a{" "}
          <strong>{inviteInfo?.role}</strong>.
        </p>
        <Button
          className="w-full"
          size="lg"
          onClick={() => {
            window.location.href = api.auth.discordUrl(token);
          }}
        >
          Accept Invite with Discord
        </Button>
      </CardContent>
    </Card>
  );
}

export default function InvitePage() {
  return (
    <div className="flex min-h-screen items-center justify-center bg-background p-4">
      <Suspense
        fallback={
          <div className="flex items-center gap-2 text-muted-foreground">
            <Loader2 className="h-6 w-6 animate-spin" />
            Loading...
          </div>
        }
      >
        <InviteHandler />
      </Suspense>
    </div>
  );
}
