"use client";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { useAdminStats, useAdminUsers, useHasRole } from "@/lib/hooks";
import { formatRelativeTime } from "@/lib/utils";
import {
  Building2,
  Users,
  Swords,
  Radio,
  Clock,
  ScrollText,
  ShieldAlert,
} from "lucide-react";
import Link from "next/link";

export default function AdminDashboardPage() {
  const isAdmin = useHasRole("super_admin");
  const { data: stats, isLoading: statsLoading } = useAdminStats();
  const { data: usersResp } = useAdminUsers({ limit: 10 });

  if (!isAdmin) {
    return (
      <div className="flex flex-col items-center justify-center gap-4 py-20">
        <ShieldAlert className="h-12 w-12 text-muted-foreground/40" />
        <h2 className="text-xl font-semibold">Access Denied</h2>
        <p className="text-muted-foreground">
          This page is restricted to super administrators.
        </p>
      </div>
    );
  }

  const statCards = [
    {
      label: "Total Tenants",
      value: stats?.total_tenants ?? 0,
      icon: Building2,
    },
    {
      label: "Total Users",
      value: stats?.total_users ?? 0,
      icon: Users,
    },
    {
      label: "Total Campaigns",
      value: stats?.total_campaigns ?? 0,
      icon: Swords,
    },
    {
      label: "Active Sessions",
      value: stats?.active_sessions ?? 0,
      icon: Radio,
    },
    {
      label: "Session Hours (Month)",
      value: stats?.total_session_hours?.toFixed(1) ?? "0",
      icon: Clock,
    },
    {
      label: "Audit Log Entries",
      value: stats?.audit_log_count ?? 0,
      icon: ScrollText,
    },
  ];

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">
          Admin Dashboard
        </h1>
        <p className="text-muted-foreground">
          System-wide overview for super administrators
        </p>
      </div>

      {/* Stats Grid */}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {statCards.map((card) => (
          <Card key={card.label}>
            <CardContent className="flex items-center gap-4 p-6">
              <div className="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-primary/10">
                <card.icon className="h-5 w-5 text-primary" />
              </div>
              <div>
                <p className="text-sm text-muted-foreground">{card.label}</p>
                <p className="text-2xl font-bold">
                  {statsLoading ? (
                    <span className="inline-block h-7 w-12 animate-pulse rounded bg-muted" />
                  ) : (
                    card.value
                  )}
                </p>
              </div>
            </CardContent>
          </Card>
        ))}
      </div>

      {/* Recent Users */}
      <Card>
        <CardHeader className="flex flex-row items-center justify-between">
          <CardTitle>Recent Users</CardTitle>
          <Link
            href="/admin/audit-log"
            className="text-sm text-primary hover:underline"
          >
            View Audit Log
          </Link>
        </CardHeader>
        <CardContent>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Tenant</TableHead>
                <TableHead>Role</TableHead>
                <TableHead>Joined</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {usersResp?.data?.map((user) => (
                <TableRow key={user.id}>
                  <TableCell className="font-medium">
                    {user.display_name}
                  </TableCell>
                  <TableCell>
                    <code className="rounded bg-muted px-1.5 py-0.5 text-xs">
                      {user.tenant_id || "—"}
                    </code>
                  </TableCell>
                  <TableCell>
                    <Badge variant={user.role === "super_admin" ? "default" : "secondary"}>
                      {user.role}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-muted-foreground">
                    {formatRelativeTime(user.created_at)}
                  </TableCell>
                </TableRow>
              ))}
              {(!usersResp?.data || usersResp.data.length === 0) && (
                <TableRow>
                  <TableCell
                    colSpan={4}
                    className="text-center text-muted-foreground"
                  >
                    No users found
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </CardContent>
      </Card>
    </div>
  );
}
