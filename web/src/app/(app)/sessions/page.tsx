"use client";

import Link from "next/link";
import { ScrollText } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useSessions } from "@/lib/hooks";

function formatDuration(seconds: number): string {
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  return `${h}h ${m}m`;
}

function formatDate(dateStr: string): string {
  return new Date(dateStr).toLocaleDateString("en-US", {
    month: "short",
    day: "numeric",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

export default function SessionsPage() {
  const { data: sessions, isLoading } = useSessions();

  return (
    <div className="space-y-6">
      <h1 className="text-2xl font-bold">Sessions</h1>

      {isLoading ? (
        <Card className="animate-pulse">
          <CardContent className="h-64" />
        </Card>
      ) : sessions && sessions.length > 0 ? (
        <Card>
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
                <TableRow key={session.id}>
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
                  <TableCell>
                    {formatDuration(session.duration_seconds)}
                  </TableCell>
                  <TableCell>
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
                  </TableCell>
                  <TableCell className="max-w-48 truncate text-muted-foreground">
                    {session.npc_names.join(", ")}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      ) : (
        <Card>
          <CardContent className="py-12 text-center">
            <ScrollText className="mx-auto h-12 w-12 text-muted-foreground" />
            <h3 className="mt-4 text-lg font-medium">No sessions yet</h3>
            <p className="mt-1 text-sm text-muted-foreground">
              Sessions will appear here once you start a voice session with your
              NPCs.
            </p>
          </CardContent>
        </Card>
      )}
    </div>
  );
}
