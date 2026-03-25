"use client";

import { use, useMemo, useState, useEffect, useCallback } from "react";
import Link from "next/link";
import { Clock, Radio, Users, Server, MessageSquare } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Separator } from "@/components/ui/separator";
import { Breadcrumbs } from "@/components/breadcrumbs";
import { useSession, useTranscript, useStopSession } from "@/lib/hooks";
import { cn, formatDuration } from "@/lib/utils";

function formatTime(dateStr: string): string {
  return new Date(dateStr).toLocaleTimeString("en-US", {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

function formatLongDate(dateStr: string): string {
  return new Date(dateStr).toLocaleDateString("en-US", {
    weekday: "long",
    month: "long",
    day: "numeric",
    year: "numeric",
  });
}

function LiveSessionTimer({ startedAt }: { startedAt: string }) {
  const computeElapsed = useCallback(() => {
    const start = new Date(startedAt).getTime();
    const diff = Math.max(0, Math.floor((Date.now() - start) / 1000));
    const h = Math.floor(diff / 3600);
    const m = Math.floor((diff % 3600) / 60);
    const s = diff % 60;
    return `${h}:${m.toString().padStart(2, "0")}:${s.toString().padStart(2, "0")}`;
  }, [startedAt]);

  const [elapsed, setElapsed] = useState(computeElapsed);

  useEffect(() => {
    const interval = setInterval(() => setElapsed(computeElapsed()), 1000);
    return () => clearInterval(interval);
  }, [computeElapsed]);

  return <span className="font-mono tabular-nums">{elapsed}</span>;
}

const colorPool = [
  "text-blue-400",
  "text-green-400",
  "text-purple-400",
  "text-orange-400",
  "text-pink-400",
  "text-cyan-400",
  "text-yellow-400",
  "text-red-400",
];

function buildSpeakerColorMap(speakers: string[]): Record<string, string> {
  const unique = [...new Set(speakers)];
  const map: Record<string, string> = {};
  unique.forEach((speaker, i) => {
    map[speaker] = colorPool[i % colorPool.length];
  });
  return map;
}

export default function SessionDetailPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  const { data: session, isLoading: sessionLoading } = useSession(id);
  const isLive = session?.status === "active";
  const { data: transcript, isLoading: transcriptLoading } = useTranscript(id, isLive);
  const stopSession = useStopSession();

  const speakerColors = useMemo(() => {
    if (!transcript) return {};
    const npcSpeakers = transcript
      .filter((e) => e.speaker_type === "npc")
      .map((e) => e.speaker);
    return buildSpeakerColorMap(npcSpeakers);
  }, [transcript]);

  if (sessionLoading) {
    return (
      <div className="mx-auto max-w-4xl space-y-4">
        <div className="h-4 w-48 rounded bg-muted skeleton-shimmer" />
        <div className="h-8 w-64 rounded bg-muted skeleton-shimmer" />
        <div className="grid gap-4 sm:grid-cols-4">
          {[1, 2, 3, 4].map((i) => (
            <Card key={i}><CardContent className="h-16 p-4 skeleton-shimmer" /></Card>
          ))}
        </div>
        <div className="h-96 rounded bg-muted skeleton-shimmer" />
      </div>
    );
  }

  if (!session) {
    return (
      <div className="flex flex-col items-center gap-3 py-16 text-center">
        <p className="text-muted-foreground">Session not found.</p>
        <Button variant="outline" render={<Link href="/sessions" />}>
          Back to Sessions
        </Button>
      </div>
    );
  }

  const isActive = isLive;

  return (
    <div className="mx-auto max-w-4xl space-y-6">
      <Breadcrumbs
        items={[
          { label: "Sessions", href: "/sessions" },
          { label: `${session.campaign_name} — ${formatLongDate(session.started_at)}` },
        ]}
      />

      {/* Header */}
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">{session.campaign_name}</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            {formatLongDate(session.started_at)}
          </p>
        </div>
        {isActive && (
          <Button
            variant="destructive"
            onClick={() => stopSession.mutate(id)}
            disabled={stopSession.isPending}
          >
            Stop Session
          </Button>
        )}
      </div>

      {/* Session info */}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Card className={isActive ? "border-green-500/30" : undefined}>
          <CardContent className="flex items-center gap-3 p-4">
            <div className={cn("flex h-8 w-8 shrink-0 items-center justify-center rounded-lg", isActive ? "bg-green-500/10" : "bg-muted")}>
              <Radio className={cn("h-4 w-4", isActive ? "text-green-500" : "text-muted-foreground")} />
            </div>
            <div>
              <p className="text-xs text-muted-foreground">Status</p>
              <Badge
                variant={
                  isActive
                    ? "default"
                    : session.status === "ended"
                      ? "secondary"
                      : "destructive"
                }
                className={isActive ? "bg-green-600 hover:bg-green-600" : undefined}
              >
                {isActive && <span className="mr-1.5 h-1.5 w-1.5 rounded-full bg-white animate-pulse" />}
                {session.status}
              </Badge>
            </div>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="flex items-center gap-3 p-4">
            <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-primary/10">
              <Clock className="h-4 w-4 text-primary" />
            </div>
            <div>
              <p className="text-xs text-muted-foreground">Duration</p>
              <p className="font-medium">
                {isActive ? (
                  <LiveSessionTimer startedAt={session.started_at} />
                ) : (
                  formatDuration(session.duration_seconds)
                )}
              </p>
            </div>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="flex items-center gap-3 p-4">
            <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-muted">
              <Users className="h-4 w-4 text-muted-foreground" />
            </div>
            <div>
              <p className="text-xs text-muted-foreground">NPCs</p>
              <p className="font-medium">{session.npc_names.length}</p>
            </div>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="flex items-center gap-3 p-4">
            <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-muted">
              <Server className="h-4 w-4 text-muted-foreground" />
            </div>
            <div>
              <p className="text-xs text-muted-foreground">Guild</p>
              <p className="font-medium truncate max-w-[120px]">{session.guild_name}</p>
            </div>
          </CardContent>
        </Card>
      </div>

      {/* Transcript */}
      <Card>
        <CardHeader>
          <div className="flex items-center justify-between">
            <CardTitle className="flex items-center gap-2">
              <MessageSquare className="h-4 w-4" />
              Transcript
            </CardTitle>
            {isActive && (
              <Badge variant="secondary" className="text-xs">
                <span className="mr-1.5 h-1.5 w-1.5 rounded-full bg-green-500 animate-pulse" />
                Live
              </Badge>
            )}
          </div>
        </CardHeader>
        <CardContent>
          {transcriptLoading ? (
            <div className="space-y-3">
              {[1, 2, 3, 4, 5].map((i) => (
                <div key={i} className="flex gap-3">
                  <div className="h-4 w-20 rounded bg-muted skeleton-shimmer" />
                  <div className="h-4 flex-1 rounded bg-muted skeleton-shimmer" />
                </div>
              ))}
            </div>
          ) : transcript && transcript.length > 0 ? (
            <div className="space-y-1 max-h-[600px] overflow-y-auto overscroll-contain">
              {transcript.map((entry, i) => {
                const prevEntry = i > 0 ? transcript[i - 1] : null;
                const showTimestamp =
                  !prevEntry ||
                  new Date(entry.timestamp).getTime() -
                    new Date(prevEntry.timestamp).getTime() >
                    60_000;

                return (
                  <div key={entry.id}>
                    {showTimestamp && (
                      <div className="my-3 flex items-center gap-2">
                        <Separator className="flex-1" />
                        <span className="text-xs text-muted-foreground font-mono">
                          {formatTime(entry.timestamp)}
                        </span>
                        <Separator className="flex-1" />
                      </div>
                    )}
                    <div
                      className={cn(
                        "rounded-lg px-3 py-2 transition-colors",
                        entry.speaker_type === "system"
                          ? "bg-muted/50 text-center text-xs text-muted-foreground italic"
                          : "hover:bg-muted/30",
                      )}
                    >
                      {entry.speaker_type !== "system" && (
                        <span
                          className={cn(
                            "mr-2 text-sm font-semibold",
                            entry.speaker_type === "npc"
                              ? speakerColors[entry.speaker] ?? "text-foreground"
                              : "text-foreground",
                          )}
                        >
                          {entry.speaker}:
                        </span>
                      )}
                      <span className="text-sm leading-relaxed">{entry.content}</span>
                    </div>
                  </div>
                );
              })}
            </div>
          ) : (
            <div className="flex flex-col items-center gap-2 py-12 text-center">
              <MessageSquare className="h-10 w-10 text-muted-foreground/40" />
              <p className="text-sm text-muted-foreground">No transcript entries for this session.</p>
              {isActive && (
                <p className="text-xs text-muted-foreground/70">
                  Transcript entries will appear here as players and NPCs speak.
                </p>
              )}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
