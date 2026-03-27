"use client";

import Link from "next/link";
import { useQuery } from "@tanstack/react-query";
import { Card, CardContent } from "@/components/ui/card";
import { SessionStatusBadge } from "@/components/session-status-badge";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { api } from "@/lib/api";
import { formatDuration, formatDate } from "@/lib/utils";

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
                <SessionStatusBadge status={session.status} />
              </TableCell>
              <TableCell className="text-muted-foreground">
                {(session.npc_names ?? []).join(", ")}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Card>
  );
}
