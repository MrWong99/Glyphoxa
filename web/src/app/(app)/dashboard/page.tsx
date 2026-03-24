"use client";

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
} from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import {
  useUser,
  useDashboardStats,
  useActiveSessions,
  useActivity,
  useStopSession,
} from "@/lib/hooks";
import { formatRelativeTime } from "@/lib/utils";

function greeting(): string {
  const hour = new Date().getHours();
  if (hour < 12) return "Good morning";
  if (hour < 18) return "Good afternoon";
  return "Good evening";
}

function formatDashboardDuration(seconds: number): string {
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  return `${h}:${m.toString().padStart(2, "0")}`;
}

function StatCardSkeleton() {
  return (
    <Card className="animate-pulse">
      <CardHeader className="flex flex-row items-center justify-between pb-2">
        <div className="h-4 w-20 rounded bg-muted" />
        <div className="h-4 w-4 rounded bg-muted" />
      </CardHeader>
      <CardContent>
        <div className="h-8 w-16 rounded bg-muted" />
      </CardContent>
    </Card>
  );
}

export default function DashboardPage() {
  const { data: user } = useUser();
  const { data: stats, isLoading: statsLoading, isError: statsError } = useDashboardStats();
  const { data: activeSessions } = useActiveSessions();
  const { data: activity, isLoading: activityLoading, isError: activityError } = useActivity();
  const stopSession = useStopSession();

  return (
    <div className="space-y-6">
      <h1 className="text-2xl font-bold">
        {greeting()}, {user?.display_name ?? "Dungeon Master"}
      </h1>

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
            <Card className="transition-colors hover:bg-accent/50">
              <CardHeader className="flex flex-row items-center justify-between pb-2">
                <CardTitle className="text-sm font-medium text-muted-foreground">
                  Campaigns
                </CardTitle>
                <Swords className="h-4 w-4 text-muted-foreground" />
              </CardHeader>
              <CardContent>
                <div className="text-3xl font-bold">
                  {stats?.campaign_count ?? 0}
                </div>
              </CardContent>
            </Card>
          </Link>

          <Card>
            <CardHeader className="flex flex-row items-center justify-between pb-2">
              <CardTitle className="text-sm font-medium text-muted-foreground">
                Active Sessions
              </CardTitle>
              <Radio className="h-4 w-4 text-muted-foreground" />
            </CardHeader>
            <CardContent>
              <div className="text-3xl font-bold">
                {stats?.active_session_count ?? 0}
              </div>
            </CardContent>
          </Card>

          <Card>
            <CardHeader className="flex flex-row items-center justify-between pb-2">
              <CardTitle className="text-sm font-medium text-muted-foreground">
                This Month
              </CardTitle>
              <Clock className="h-4 w-4 text-muted-foreground" />
            </CardHeader>
            <CardContent>
              <div className="text-3xl font-bold">
                {stats?.hours_used ?? 0}h
                <span className="text-base font-normal text-muted-foreground">
                  {" "}
                  / {stats?.hours_limit ?? 100}h
                </span>
              </div>
              <div className="mt-2 h-2 w-full overflow-hidden rounded-full bg-secondary">
                <div
                  className="h-full rounded-full bg-primary transition-all"
                  style={{
                    width: `${Math.min(100, ((stats?.hours_used ?? 0) / (stats?.hours_limit ?? 100)) * 100)}%`,
                  }}
                />
              </div>
            </CardContent>
          </Card>
        </div>
      )}

      {/* Active sessions */}
      {activeSessions && activeSessions.length > 0 && (
        <div className="space-y-3">
          <h2 className="text-lg font-semibold">Active Sessions</h2>
          <div className="space-y-2">
            {activeSessions.map((session) => (
              <Card key={session.id}>
                <CardContent className="flex items-center justify-between p-4">
                  <div className="space-y-1">
                    <div className="flex items-center gap-2">
                      <span className="h-2 w-2 rounded-full bg-green-500" />
                      <span className="font-medium">
                        {session.campaign_name}
                      </span>
                    </div>
                    <p className="text-sm text-muted-foreground">
                      {session.guild_name} &middot; Duration:{" "}
                      {formatDashboardDuration(session.duration_seconds)}
                    </p>
                    <p className="text-sm text-muted-foreground">
                      NPCs: {session.npc_names.join(", ")}
                    </p>
                  </div>
                  <div className="flex gap-2">
                    <Button variant="outline" size="sm" render={<Link href={`/sessions/${session.id}`} />}>
                        <Eye className="mr-1 h-3 w-3" />
                        View
                    </Button>
                    <Button
                      variant="destructive"
                      size="sm"
                      onClick={() => stopSession.mutate(session.id)}
                      disabled={stopSession.isPending}
                    >
                      <Square className="mr-1 h-3 w-3" />
                      Stop
                    </Button>
                  </div>
                </CardContent>
              </Card>
            ))}
          </div>
        </div>
      )}

      {/* Recent activity */}
      <div className="space-y-3">
        <h2 className="text-lg font-semibold">Recent Activity</h2>
        <Card>
          <CardContent className="divide-y divide-border p-0">
            {activityLoading ? (
              <div className="space-y-3 p-4">
                {[1, 2, 3].map((i) => (
                  <div key={i} className="flex animate-pulse items-start gap-3">
                    <div className="h-4 w-4 rounded bg-muted" />
                    <div className="flex-1 space-y-1">
                      <div className="h-4 w-3/4 rounded bg-muted" />
                      <div className="h-3 w-16 rounded bg-muted" />
                    </div>
                  </div>
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
              activity.slice(0, 10).map((item) => (
                <div key={item.id} className="flex items-start gap-3 p-4">
                  <div className="mt-0.5 text-muted-foreground">
                    {item.type.startsWith("session") ? (
                      <Radio className="h-4 w-4" />
                    ) : item.type.startsWith("npc") ? (
                      <Swords className="h-4 w-4" />
                    ) : (
                      <ScrollText className="h-4 w-4" />
                    )}
                  </div>
                  <div className="flex-1">
                    <p className="text-sm">{item.description}</p>
                    <p className="text-xs text-muted-foreground">
                      {formatRelativeTime(item.timestamp)}
                    </p>
                  </div>
                </div>
              ))
            ) : (
              <div className="p-6 text-center text-sm text-muted-foreground">
                No recent activity
              </div>
            )}
          </CardContent>
        </Card>
      </div>

      {/* Quick actions */}
      <div className="flex flex-wrap gap-3">
        <Button render={<Link href="/campaigns/new" />}>
            <Plus className="mr-1 h-4 w-4" />
            New Campaign
        </Button>
        <Button variant="outline" render={<Link href="/sessions" />}>
            <ScrollText className="mr-1 h-4 w-4" />
            View Transcripts
        </Button>
      </div>
    </div>
  );
}
