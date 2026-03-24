"use client";

import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { api } from "@/lib/api";

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

interface CampaignSessionsProps {
  campaignId: string;
}

export function CampaignSessions({ campaignId }: CampaignSessionsProps) {
  const { data: sessions, isLoading } = useQuery({
    queryKey: ["campaigns", campaignId, "sessions"],
    queryFn: () => api.sessions.listByCampaign(campaignId),
    enabled: !!campaignId,
  });

  if (isLoading) {
    return <Card className="animate-pulse"><CardContent className="h-32" /></Card>;
  }

  if (!sessions || sessions.length === 0) {
    return (
      <Card>
        <CardContent className="py-8 text-center">
          <p className="text-sm text-muted-foreground">
            No sessions recorded for this campaign yet.
          </p>
        </CardContent>
      </Card>
    );
  }

  return (
    <Card>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Date</TableHead>
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
              <TableCell>{formatDuration(session.duration_seconds)}</TableCell>
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
              <TableCell className="text-muted-foreground">
                {session.npc_names.join(", ")}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Card>
  );
}
