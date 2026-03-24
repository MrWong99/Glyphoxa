"use client";

import { useState } from "react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useUser, useUpdateMe, useUpdatePreferences } from "@/lib/hooks";
import { Avatar, AvatarFallback, AvatarImage } from "@/components/ui/avatar";
import { Badge } from "@/components/ui/badge";
import { Breadcrumbs } from "@/components/breadcrumbs";

export default function SettingsPage() {
  const { data: user } = useUser();
  const updateMe = useUpdateMe();
  const updatePreferences = useUpdatePreferences();

  const [displayNameDraft, setDisplayNameDraft] = useState<string | null>(null);
  const [themeDraft, setThemeDraft] = useState<"light" | "dark" | "system" | null>(null);
  const [localeDraft, setLocaleDraft] = useState<string | null>(null);

  const displayName = displayNameDraft ?? user?.display_name ?? "";
  const theme = themeDraft ?? user?.preferences?.theme ?? "system";
  const locale = localeDraft ?? user?.preferences?.locale ?? "en";

  const handleSaveProfile = () => {
    if (displayName.trim()) {
      updateMe.mutate({ display_name: displayName.trim() });
    }
  };

  const handleSavePreferences = () => {
    updatePreferences.mutate({ theme, locale });
  };

  return (
    <div className="mx-auto max-w-2xl space-y-6">
      <Breadcrumbs items={[{ label: "Settings" }]} />

      <div>
        <h1 className="text-2xl font-bold tracking-tight">Settings</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Manage your account and preferences.
        </p>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Account</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="flex items-center gap-4">
            <Avatar className="h-16 w-16 border-2 border-primary/20">
              <AvatarImage
                src={user?.avatar_url ?? undefined}
                alt={user?.display_name ?? "User"}
              />
              <AvatarFallback className="bg-primary/10 text-lg text-primary">
                {user?.display_name?.[0]?.toUpperCase() ?? "?"}
              </AvatarFallback>
            </Avatar>
            <div>
              <p className="text-lg font-medium">{user?.display_name}</p>
              <p className="text-sm text-muted-foreground">{user?.email}</p>
              <Badge variant="secondary" className="mt-1.5">
                {user?.role ?? "dm"}
              </Badge>
            </div>
          </div>

          <div className="space-y-2">
            <Label htmlFor="displayName">Display Name</Label>
            <div className="flex gap-2">
              <Input
                id="displayName"
                value={displayName}
                onChange={(e) => setDisplayNameDraft(e.target.value)}
                placeholder="Your display name"
              />
              <Button
                onClick={handleSaveProfile}
                disabled={
                  updateMe.isPending ||
                  !displayName.trim() ||
                  displayName.trim() === user?.display_name
                }
              >
                {updateMe.isPending ? "Saving..." : "Save"}
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Preferences</CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="theme">Theme</Label>
            <Select value={theme} onValueChange={(v) => setThemeDraft(v as "light" | "dark" | "system")}>
              <SelectTrigger id="theme">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="system">System</SelectItem>
                <SelectItem value="light">Light</SelectItem>
                <SelectItem value="dark">Dark</SelectItem>
              </SelectContent>
            </Select>
          </div>

          <div className="space-y-2">
            <Label htmlFor="locale">Language</Label>
            <Select value={locale} onValueChange={setLocaleDraft}>
              <SelectTrigger id="locale">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="en">English</SelectItem>
                <SelectItem value="de">Deutsch</SelectItem>
              </SelectContent>
            </Select>
          </div>

          <Button
            onClick={handleSavePreferences}
            disabled={updatePreferences.isPending}
          >
            {updatePreferences.isPending ? "Saving..." : "Save Preferences"}
          </Button>
        </CardContent>
      </Card>

      <Card className="border-destructive/50">
        <CardHeader>
          <CardTitle className="text-destructive">Danger Zone</CardTitle>
        </CardHeader>
        <CardContent>
          <Button variant="destructive" disabled>
            Delete Account
          </Button>
          <p className="mt-2 text-xs text-muted-foreground">
            Account deletion is not yet available. Contact support if you need to
            delete your account.
          </p>
        </CardContent>
      </Card>
    </div>
  );
}
