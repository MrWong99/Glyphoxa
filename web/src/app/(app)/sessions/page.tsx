"use client";

import Link from "next/link";
import { ScrollText, AlertTriangle } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Breadcrumbs } from "@/components/breadcrumbs";
import { SessionStatusBadge } from "@/components/session-status-badge";
import { useSessions } from "@/lib/hooks";
import { formatDuration, formatDate } from "@/lib/utils";

export default function SessionsPage() {
  const { data: sessions, isLoading, isError, error } = useSessions();

  return (
    <div className="space-y-6">
      <Breadcrumbs items={[{ label: "Sessions" }]} />

      <div>
        <h1 className="text-2xl font-bold tracking-tight">Sessions</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          View past and active voice sessions across all campaigns.
        </p>
      </div>

      {isLoading ? (
        <Card>
          <CardContent className="space-y-3 p-6">
            {[1, 2, 3, 4].map((i) => (
              <div key={i} className="flex gap-4">
                <div className="h-5 w-28 rounded bg-muted skeleton-shimmer" />
                <div className="h-5 w-32 rounded bg-muted skeleton-shimmer" />
                <div className="h-5 w-16 rounded bg-muted skeleton-shimmer" />
                <div className="h-5 w-16 rounded bg-muted skeleton-shimmer" />
                <div className="h-5 flex-1 rounded bg-muted skeleton-shimmer" />
              </div>
            ))}
          </CardContent>
        </Card>
      ) : isError ? (
        <Card className="border-destructive/50">
          <CardContent className="py-8 text-center">
            <AlertTriangle className="mx-auto h-12 w-12 text-destructive" />
            <h3 className="mt-4 text-lg font-medium">Failed to load sessions</h3>
            <p className="mt-1 text-sm text-muted-foreground">
              {error?.message || "An unexpected error occurred."}
            </p>
          </CardContent>
        </Card>
      ) : sessions && sessions.length > 0 ? (
        <Card>
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Date</TableHead>
                  <TableHead>Campaign</TableHead>
                  <TableHead>Duration</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>NPCs</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {sessions.map((session) => (
                  <TableRow key={session.id} className="transition-colors">
                    <TableCell>
                      <Link
                        href={`/sessions/${session.id}`}
                        className="text-primary hover:underline"
                      >
                        {formatDate(session.started_at)}
                      </Link>
                    </TableCell>
                    <TableCell>
                      <Link
                        href={`/campaigns/${session.campaign_id}`}
                        className="hover:underline"
                      >
                        {session.campaign_name}
                      </Link>
                    </TableCell>
                    <TableCell className="font-mono text-sm">
                      {formatDuration(session.duration_seconds)}
                    </TableCell>
                    <TableCell>
                      <SessionStatusBadge status={session.status} />
                    </TableCell>
                    <TableCell className="max-w-48 truncate text-muted-foreground">
                      {(session.npc_names ?? []).join(", ")}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        </Card>
      ) : (
        <Card className="border-dashed">
          <CardContent className="py-16 text-center">
            <div className="mx-auto mb-4 flex h-16 w-16 items-center justify-center rounded-2xl bg-primary/10">
              <ScrollText className="h-8 w-8 text-primary/60" />
            </div>
            <h3 className="text-lg font-medium">No sessions yet</h3>
            <p className="mx-auto mt-2 max-w-sm text-sm text-muted-foreground">
              Sessions will appear here once you start a voice session with your NPCs in Discord.
            </p>
          </CardContent>
        </Card>
      )}
    </div>
  );
}
