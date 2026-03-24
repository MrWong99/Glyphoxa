"use client";

import { use } from "react";
import Link from "next/link";
import { ArrowLeft, Clock, Radio, Users } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Separator } from "@/components/ui/separator";
import { useSession, useTranscript, useStopSession } from "@/lib/hooks";
import { cn } from "@/lib/utils";

function formatDuration(seconds: number): string {
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = seconds % 60;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m ${s}s`;
}

function formatTime(dateStr: string): string {
  return new Date(dateStr).toLocaleTimeString("en-US", {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

function formatDate(dateStr: string): string {
  return new Date(dateStr).toLocaleDateString("en-US", {
    weekday: "long",
    month: "long",
    day: "numeric",
    year: "numeric",
  });
}

const speakerColors: Record<string, string> = {};
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

function getSpeakerColor(speaker: string): string {
  if (!(speaker in speakerColors)) {
    speakerColors[speaker] =
      colorPool[Object.keys(speakerColors).length % colorPool.length];
  }
  return speakerColors[speaker];
}

export default function SessionDetailPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  const { data: session, isLoading: sessionLoading } = useSession(id);
  const { data: transcript, isLoading: transcriptLoading } = useTranscript(id);
  const stopSession = useStopSession();

  if (sessionLoading) {
    return (
      <div className="mx-auto max-w-4xl animate-pulse space-y-4">
        <div className="h-8 w-48 rounded bg-muted" />
        <div className="h-96 rounded bg-muted" />
      </div>
    );
  }

  if (!session) {
    return (
      <div className="text-center">
        <p className="text-muted-foreground">Session not found.</p>
      </div>
    );
  }

  return (
    <div className="mx-auto max-w-4xl space-y-6">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <Button variant="ghost" size="icon" render={<Link href="/sessions" />}>
              <ArrowLeft className="h-4 w-4" />
          </Button>
          <div>
            <h1 className="text-2xl font-bold">{session.campaign_name}</h1>
            <p className="text-sm text-muted-foreground">
              {formatDate(session.started_at)}
            </p>
          </div>
        </div>
        {session.status === "active" && (
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
      <div className="grid gap-4 sm:grid-cols-4">
        <Card>
          <CardContent className="flex items-center gap-3 p-4">
            <Radio className="h-4 w-4 text-muted-foreground" />
            <div>
              <p className="text-xs text-muted-foreground">Status</p>
              <Badge
                variant={
                  session.status === "active"
                    ? "default"
                    : session.status === "ended"
                      ? "secondary"
                      : "destructive"
                }
              >
                {session.status}
              </Badge>
            </div>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="flex items-center gap-3 p-4">
            <Clock className="h-4 w-4 text-muted-foreground" />
            <div>
              <p className="text-xs text-muted-foreground">Duration</p>
              <p className="font-medium">
                {formatDuration(session.duration_seconds)}
              </p>
            </div>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="flex items-center gap-3 p-4">
            <Users className="h-4 w-4 text-muted-foreground" />
            <div>
              <p className="text-xs text-muted-foreground">NPCs</p>
              <p className="font-medium">{session.npc_names.length}</p>
            </div>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="flex items-center gap-3 p-4">
            <div className="h-4 w-4" />
            <div>
              <p className="text-xs text-muted-foreground">Guild</p>
              <p className="font-medium">{session.guild_name}</p>
            </div>
          </CardContent>
        </Card>
      </div>

      {/* Transcript */}
      <Card>
        <CardHeader>
          <CardTitle>Transcript</CardTitle>
        </CardHeader>
        <CardContent>
          {transcriptLoading ? (
            <div className="space-y-3">
              {[1, 2, 3, 4, 5].map((i) => (
                <div key={i} className="animate-pulse">
                  <div className="h-4 w-24 rounded bg-muted" />
                  <div className="mt-1 h-4 w-3/4 rounded bg-muted" />
                </div>
              ))}
            </div>
          ) : transcript && transcript.length > 0 ? (
            <div className="space-y-1">
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
                        <span className="text-xs text-muted-foreground">
                          {formatTime(entry.timestamp)}
                        </span>
                        <Separator className="flex-1" />
                      </div>
                    )}
                    <div
                      className={cn(
                        "rounded-md px-3 py-1.5",
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
                              ? getSpeakerColor(entry.speaker)
                              : "text-foreground",
                          )}
                        >
                          {entry.speaker}:
                        </span>
                      )}
                      <span className="text-sm">{entry.content}</span>
                    </div>
                  </div>
                );
              })}
            </div>
          ) : (
            <p className="py-8 text-center text-sm text-muted-foreground">
              No transcript entries for this session.
            </p>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
