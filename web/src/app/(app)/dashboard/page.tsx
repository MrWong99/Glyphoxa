"use client";

import { useState, useEffect, useCallback } from "react";
import Link from "next/link";
import {
  Swords,
  Radio,
  Clock,
  Plus,
  Eye,
  Square,
  ScrollText,
  AlertTriangle,
  TrendingUp,
  Sparkles,
} from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import {
  useUser,
  useDashboardStats,
  useActiveSessions,
  useActivity,
  useStopSession,
  useHasRole,
} from "@/lib/hooks";
import { formatRelativeTime } from "@/lib/utils";

function greeting(): string {
  const hour = new Date().getHours();
  if (hour < 12) return "Good morning";
  if (hour < 18) return "Good afternoon";
  return "Good evening";
}

function LiveTimer({ startedAt }: { startedAt: string }) {
  const [elapsed, setElapsed] = useState("");

  const computeElapsed = useCallback(() => {
    const start = new Date(startedAt).getTime();
    const diff = Math.max(0, Math.floor((Date.now() - start) / 1000));
    const h = Math.floor(diff / 3600);
    const m = Math.floor((diff % 3600) / 60);
    const s = diff % 60;
    return `${h}:${m.toString().padStart(2, "0")}:${s.toString().padStart(2, "0")}`;
  }, [startedAt]);

  useEffect(() => {
    setElapsed(computeElapsed());
    const interval = setInterval(() => setElapsed(computeElapsed()), 1000);
    return () => clearInterval(interval);
  }, [computeElapsed]);

  return (
    <span className="font-mono tabular-nums text-sm">{elapsed}</span>
  );
}

function StatCardSkeleton() {
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between pb-2">
        <div className="h-4 w-20 rounded bg-muted skeleton-shimmer" />
        <div className="h-4 w-4 rounded bg-muted skeleton-shimmer" />
      </CardHeader>
      <CardContent>
        <div className="h-8 w-16 rounded bg-muted skeleton-shimmer" />
      </CardContent>
    </Card>
  );
}

function ActivitySkeleton() {
  return (
    <div className="flex items-start gap-3">
      <div className="h-8 w-8 rounded-full bg-muted skeleton-shimmer" />
      <div className="flex-1 space-y-1.5">
        <div className="h-4 w-3/4 rounded bg-muted skeleton-shimmer" />
        <div className="h-3 w-16 rounded bg-muted skeleton-shimmer" />
      </div>
    </div>
  );
}

export default function DashboardPage() {
  const { data: user } = useUser();
  const { data: stats, isLoading: statsLoading, isError: statsError } = useDashboardStats();
  const { data: activeSessions } = useActiveSessions();
  const { data: activity, isLoading: activityLoading, isError: activityError } = useActivity();
  const stopSession = useStopSession();
  const canManage = useHasRole("dm");

  const usagePercent = Math.min(100, ((stats?.hours_used ?? 0) / (stats?.hours_limit ?? 100)) * 100);

  return (
    <div className="space-y-8">
      {/* Header */}
      <div>
        <h1 className="text-3xl font-bold tracking-tight">
          {greeting()},{" "}
          <span className="bg-gradient-to-r from-primary to-[oklch(0.6_0.18_240)] bg-clip-text text-transparent">
            {user?.display_name ?? "Dungeon Master"}
          </span>
        </h1>
        <p className="mt-1 text-muted-foreground">
          Here&apos;s what&apos;s happening with your campaigns.
        </p>
      </div>

      {/* Stats cards */}
      {statsLoading ? (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          <StatCardSkeleton />
          <StatCardSkeleton />
          <StatCardSkeleton />
        </div>
      ) : statsError ? (
        <Card className="border-destructive/50">
          <CardContent className="flex items-center gap-3 p-4">
            <AlertTriangle className="h-5 w-5 text-destructive" />
            <p className="text-sm text-muted-foreground">
              Failed to load dashboard stats.
            </p>
          </CardContent>
        </Card>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          <Link href="/campaigns">
            <Card className="group transition-all duration-200 hover:border-primary/30 hover:shadow-lg hover:shadow-primary/5">
              <CardHeader className="flex flex-row items-center justify-between pb-2">
                <CardTitle className="text-sm font-medium text-muted-foreground">
                  Campaigns
                </CardTitle>
                <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-primary/10 transition-colors group-hover:bg-primary/20">
                  <Swords className="h-4 w-4 text-primary" />
                </div>
              </CardHeader>
              <CardContent>
                <div className="text-3xl font-bold">
                  {stats?.campaign_count ?? 0}
                </div>
                <p className="mt-1 flex items-center gap-1 text-xs text-muted-foreground">
                  <TrendingUp className="h-3 w-3 text-green-500" />
                  Active campaigns
                </p>
              </CardContent>
            </Card>
          </Link>

          <Card className="transition-all duration-200 hover:border-primary/30">
            <CardHeader className="flex flex-row items-center justify-between pb-2">
              <CardTitle className="text-sm font-medium text-muted-foreground">
                Active Sessions
              </CardTitle>
              <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-green-500/10">
                <Radio className="h-4 w-4 text-green-500" />
              </div>
            </CardHeader>
            <CardContent>
              <div className="text-3xl font-bold">
                {stats?.active_session_count ?? 0}
              </div>
              <p className="mt-1 text-xs text-muted-foreground">
                {(stats?.active_session_count ?? 0) > 0
                  ? "Live right now"
                  : "No active sessions"}
              </p>
            </CardContent>
          </Card>

          <Card className="transition-all duration-200 hover:border-primary/30">
            <CardHeader className="flex flex-row items-center justify-between pb-2">
              <CardTitle className="text-sm font-medium text-muted-foreground">
                This Month
              </CardTitle>
              <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-primary/10">
                <Clock className="h-4 w-4 text-primary" />
              </div>
            </CardHeader>
            <CardContent>
              <div className="text-3xl font-bold">
                {stats?.hours_used ?? 0}h
                <span className="text-base font-normal text-muted-foreground">
                  {" "}/ {stats?.hours_limit ?? 100}h
                </span>
              </div>
              <div className="mt-3 h-2 w-full overflow-hidden rounded-full bg-secondary">
                <div
                  className="h-full rounded-full bg-gradient-to-r from-primary to-[oklch(0.6_0.18_240)] transition-all duration-500"
                  style={{ width: `${usagePercent}%` }}
                />
              </div>
              <p className="mt-1.5 text-xs text-muted-foreground">
                {Math.round(usagePercent)}% of monthly quota used
              </p>
            </CardContent>
          </Card>
        </div>
      )}

      {/* Active sessions */}
      {activeSessions && activeSessions.length > 0 && (
        <div className="space-y-4">
          <div className="flex items-center gap-2">
            <span className="relative flex h-2.5 w-2.5">
              <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-green-400 opacity-75" />
              <span className="relative inline-flex h-2.5 w-2.5 rounded-full bg-green-500" />
            </span>
            <h2 className="text-lg font-semibold">Active Sessions</h2>
          </div>
          <div className="grid gap-3 sm:grid-cols-2">
            {activeSessions.map((session) => (
              <Card key={session.id} className="transition-all duration-200 hover:border-green-500/30">
                <CardContent className="p-4">
                  <div className="flex items-start justify-between">
                    <div className="space-y-1.5">
                      <div className="flex items-center gap-2">
                        <span className="h-2 w-2 rounded-full bg-green-500" />
                        <span className="font-semibold">
                          {session.campaign_name}
                        </span>
                      </div>
                      <p className="text-sm text-muted-foreground">
                        {session.guild_name}
                      </p>
                      <div className="flex items-center gap-1.5 text-sm text-muted-foreground">
                        <Clock className="h-3.5 w-3.5" />
                        <LiveTimer startedAt={session.started_at} />
                      </div>
                      <p className="text-xs text-muted-foreground">
                        NPCs: {session.npc_names.join(", ")}
                      </p>
                    </div>
                    <div className="flex flex-col gap-2">
                      <Button variant="outline" size="sm" render={<Link href={`/sessions/${session.id}`} />}>
                          <Eye className="mr-1 h-3 w-3" />
                          View
                      </Button>
                      {canManage && (
                        <Button
                          variant="destructive"
                          size="sm"
                          onClick={() => stopSession.mutate(session.id)}
                          disabled={stopSession.isPending}
                        >
                          <Square className="mr-1 h-3 w-3" />
                          Stop
                        </Button>
                      )}
                    </div>
                  </div>
                </CardContent>
              </Card>
            ))}
          </div>
        </div>
      )}

      <div className="grid gap-6 lg:grid-cols-2">
        {/* Recent activity */}
        <div className="space-y-4">
          <h2 className="text-lg font-semibold">Recent Activity</h2>
          <Card>
            <CardContent className="divide-y divide-border p-0">
              {activityLoading ? (
                <div className="space-y-4 p-4">
                  {[1, 2, 3].map((i) => (
                    <ActivitySkeleton key={i} />
                  ))}
                </div>
              ) : activityError ? (
                <div className="flex items-center gap-2 p-6">
                  <AlertTriangle className="h-4 w-4 text-destructive" />
                  <p className="text-sm text-muted-foreground">
                    Failed to load activity.
                  </p>
                </div>
              ) : activity && activity.length > 0 ? (
                activity.slice(0, 8).map((item) => (
                  <div key={item.id} className="flex items-start gap-3 p-4 transition-colors hover:bg-muted/30">
                    <div className="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-muted">
                      {item.type.startsWith("session") ? (
                        <Radio className="h-3.5 w-3.5 text-muted-foreground" />
                      ) : item.type.startsWith("npc") ? (
                        <Sparkles className="h-3.5 w-3.5 text-muted-foreground" />
                      ) : (
                        <Swords className="h-3.5 w-3.5 text-muted-foreground" />
                      )}
                    </div>
                    <div className="flex-1 min-w-0">
                      <p className="text-sm">{item.description}</p>
                      <p className="text-xs text-muted-foreground">
                        {formatRelativeTime(item.timestamp)}
                      </p>
                    </div>
                  </div>
                ))
              ) : (
                <div className="flex flex-col items-center gap-2 p-8 text-center">
                  <ScrollText className="h-10 w-10 text-muted-foreground/40" />
                  <p className="text-sm text-muted-foreground">No recent activity</p>
                  <p className="text-xs text-muted-foreground/70">
                    Activity will appear here as you use Glyphoxa.
                  </p>
                </div>
              )}
            </CardContent>
          </Card>
        </div>

        {/* Quick actions */}
        <div className="space-y-4">
          <h2 className="text-lg font-semibold">Quick Actions</h2>
          <div className="grid gap-3">
            {canManage && (
              <Link href="/campaigns/new">
                <Card className="group cursor-pointer transition-all duration-200 hover:border-primary/30 hover:shadow-lg hover:shadow-primary/5">
                  <CardContent className="flex items-center gap-4 p-4">
                    <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-primary/10 transition-colors group-hover:bg-primary/20">
                      <Plus className="h-5 w-5 text-primary" />
                    </div>
                    <div>
                      <p className="font-medium">New Campaign</p>
                      <p className="text-sm text-muted-foreground">
                        Create a new campaign with AI voice NPCs
                      </p>
                    </div>
                  </CardContent>
                </Card>
              </Link>
            )}
            <Link href="/sessions">
              <Card className="group cursor-pointer transition-all duration-200 hover:border-primary/30">
                <CardContent className="flex items-center gap-4 p-4">
                  <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-muted transition-colors group-hover:bg-primary/10">
                    <ScrollText className="h-5 w-5 text-muted-foreground group-hover:text-primary" />
                  </div>
                  <div>
                    <p className="font-medium">View Transcripts</p>
                    <p className="text-sm text-muted-foreground">
                      Browse session transcripts and recordings
                    </p>
                  </div>
                </CardContent>
              </Card>
            </Link>
            <Link href="/campaigns">
              <Card className="group cursor-pointer transition-all duration-200 hover:border-primary/30">
                <CardContent className="flex items-center gap-4 p-4">
                  <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-xl bg-muted transition-colors group-hover:bg-primary/10">
                    <Swords className="h-5 w-5 text-muted-foreground group-hover:text-primary" />
                  </div>
                  <div>
                    <p className="font-medium">Manage Campaigns</p>
                    <p className="text-sm text-muted-foreground">
                      Edit campaigns, NPCs, and settings
                    </p>
                  </div>
                </CardContent>
              </Card>
            </Link>
          </div>
        </div>
      </div>
    </div>
  );
}
