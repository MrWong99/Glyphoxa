"use client";

import { useState } from "react";
import { Users as UsersIcon, AlertTriangle, Plus, Trash2, ShieldAlert } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Breadcrumbs } from "@/components/breadcrumbs";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import {
  useUsers,
  useDeleteUser,
  useCreateInvite,
  useUser,
  useHasRole,
} from "@/lib/hooks";
import { toast } from "sonner";
import type { UserRole } from "@/lib/types";

const roleBadgeVariant: Record<
  UserRole,
  "default" | "secondary" | "destructive" | "outline"
> = {
  super_admin: "destructive",
  tenant_admin: "default",
  dm: "secondary",
  viewer: "outline",
};

export default function UsersPage() {
  const { data: currentUser } = useUser();
  const isAdmin = useHasRole("tenant_admin");
  const { data, isLoading, isError, error } = useUsers();
  const deleteUser = useDeleteUser();
  const createInvite = useCreateInvite();
  const [inviteRole, setInviteRole] = useState<UserRole>("viewer");

  const users = data?.data ?? [];

  if (currentUser && !isAdmin) {
    return (
      <div className="flex flex-col items-center gap-3 py-16 text-center">
        <ShieldAlert className="h-12 w-12 text-destructive" />
        <h2 className="text-lg font-semibold">Access Denied</h2>
        <p className="text-sm text-muted-foreground">
          You need tenant admin permissions to manage users.
        </p>
      </div>
    );
  }

  const handleInvite = async () => {
    try {
      const result = await createInvite.mutateAsync(inviteRole);
      // Copy token to clipboard.
      if (result?.token) {
        await navigator.clipboard.writeText(result.token);
        toast.success("Invite link copied to clipboard");
      }
    } catch {
      // Error is handled by the mutation's onError callback.
    }
  };

  return (
    <div className="space-y-6">
      <Breadcrumbs items={[{ label: "Users" }]} />

      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">Users</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            Manage team members and invite new users.
          </p>
        </div>

        <div className="flex items-center gap-2">
          <Select
            value={inviteRole}
            onValueChange={(v) => setInviteRole(v as UserRole)}
          >
            <SelectTrigger className="w-32">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="viewer">Viewer</SelectItem>
              <SelectItem value="dm">DM</SelectItem>
              <SelectItem value="tenant_admin">Admin</SelectItem>
            </SelectContent>
          </Select>
          <Button onClick={handleInvite} disabled={createInvite.isPending}>
            <Plus className="mr-2 h-4 w-4" />
            Invite
          </Button>
        </div>
      </div>

      {isLoading ? (
        <Card>
          <CardContent className="space-y-3 p-6">
            {[1, 2, 3].map((i) => (
              <div key={i} className="flex gap-4">
                <div className="h-8 w-8 rounded-full bg-muted skeleton-shimmer" />
                <div className="h-5 w-32 rounded bg-muted skeleton-shimmer" />
                <div className="h-5 w-24 rounded bg-muted skeleton-shimmer" />
                <div className="h-5 w-16 rounded bg-muted skeleton-shimmer" />
              </div>
            ))}
          </CardContent>
        </Card>
      ) : isError ? (
        <Card className="border-destructive/50">
          <CardContent className="py-8 text-center">
            <AlertTriangle className="mx-auto h-12 w-12 text-destructive" />
            <h3 className="mt-4 text-lg font-medium">Failed to load users</h3>
            <p className="mt-1 text-sm text-muted-foreground">
              {error?.message || "An unexpected error occurred."}
            </p>
          </CardContent>
        </Card>
      ) : users.length > 0 ? (
        <Card>
          <CardHeader>
            <CardTitle>
              Team Members ({data?.total ?? users.length})
            </CardTitle>
          </CardHeader>
          <div className="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>User</TableHead>
                  <TableHead>Email</TableHead>
                  <TableHead>Role</TableHead>
                  <TableHead className="w-12" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {users.map((u) => (
                  <TableRow key={u.id}>
                    <TableCell>
                      <div className="flex items-center gap-3">
                        <Avatar className="h-8 w-8">
                          <AvatarImage
                            src={u.avatar_url ?? undefined}
                            alt={u.display_name}
                          />
                          <AvatarFallback className="text-xs">
                            {u.display_name?.[0]?.toUpperCase() ?? "?"}
                          </AvatarFallback>
                        </Avatar>
                        <span className="font-medium">
                          {u.display_name}
                          {u.id === currentUser?.id && (
                            <span className="ml-1.5 text-xs text-muted-foreground">
                              (you)
                            </span>
                          )}
                        </span>
                      </div>
                    </TableCell>
                    <TableCell className="text-muted-foreground">
                      {u.email || "\u2014"}
                    </TableCell>
                    <TableCell>
                      <Badge variant={roleBadgeVariant[u.role]}>
                        {u.role.replace("_", " ")}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      {u.id !== currentUser?.id && (
                        <Button
                          variant="ghost"
                          size="icon"
                          className="text-destructive hover:text-destructive"
                          onClick={() => {
                            if (confirm(`Remove ${u.display_name}?`)) {
                              deleteUser.mutate(u.id);
                            }
                          }}
                        >
                          <Trash2 className="h-4 w-4" />
                        </Button>
                      )}
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
              <UsersIcon className="h-8 w-8 text-primary/60" />
            </div>
            <h3 className="text-lg font-medium">No team members yet</h3>
            <p className="mx-auto mt-2 max-w-sm text-sm text-muted-foreground">
              Invite your first team member using the button above.
            </p>
          </CardContent>
        </Card>
      )}
    </div>
  );
}
