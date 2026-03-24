"use client";

import Link from "next/link";
import { Plus, Swords, AlertTriangle } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { useCampaigns } from "@/lib/hooks";
import { formatRelativeTime } from "@/lib/utils";

export default function CampaignsPage() {
  const { data: campaigns, isLoading, isError, error } = useCampaigns();

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <h1 className="text-2xl font-bold">Campaigns</h1>
        <Button render={<Link href="/campaigns/new" />}>
            <Plus className="mr-1 h-4 w-4" />
            New Campaign
        </Button>
      </div>

      {isLoading ? (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {[1, 2, 3].map((i) => (
            <Card key={i} className="animate-pulse">
              <CardContent className="h-40 p-6" />
            </Card>
          ))}
        </div>
      ) : isError ? (
        <Card className="border-destructive/50">
          <CardContent className="py-8 text-center">
            <AlertTriangle className="mx-auto h-12 w-12 text-destructive" />
            <h3 className="mt-4 text-lg font-medium">Failed to load campaigns</h3>
            <p className="mt-1 text-sm text-muted-foreground">
              {error?.message || "An unexpected error occurred."}
            </p>
          </CardContent>
        </Card>
      ) : campaigns && campaigns.length > 0 ? (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {campaigns.map((campaign) => (
            <Link key={campaign.id} href={`/campaigns/${campaign.id}`}>
              <Card className="h-full transition-colors hover:bg-accent/50">
                <CardContent className="space-y-3 p-6">
                  <div className="flex items-start justify-between">
                    <div>
                      <h3 className="font-semibold leading-tight">
                        {campaign.name}
                      </h3>
                      <p className="mt-1 text-sm text-muted-foreground">
                        {campaign.game_system}
                      </p>
                    </div>
                    <Swords className="h-5 w-5 text-muted-foreground" />
                  </div>
                  <div className="flex items-center gap-2 text-sm text-muted-foreground">
                    <span>
                      {campaign.npc_count}{" "}
                      {campaign.npc_count === 1 ? "NPC" : "NPCs"}
                    </span>
                    <span>&middot;</span>
                    <span>
                      Last session: {formatRelativeTime(campaign.last_session_at)}
                    </span>
                  </div>
                  {campaign.has_active_session && (
                    <Badge
                      variant="default"
                      className="bg-green-600 hover:bg-green-600"
                    >
                      Active session
                    </Badge>
                  )}
                </CardContent>
              </Card>
            </Link>
          ))}

          {/* Create new card */}
          <Link href="/campaigns/new">
            <Card className="flex h-full items-center justify-center border-dashed transition-colors hover:bg-accent/50">
              <CardContent className="py-12 text-center">
                <Plus className="mx-auto h-8 w-8 text-muted-foreground" />
                <p className="mt-2 text-sm font-medium text-muted-foreground">
                  Create New Campaign
                </p>
              </CardContent>
            </Card>
          </Link>
        </div>
      ) : (
        <Card>
          <CardContent className="py-12 text-center">
            <Swords className="mx-auto h-12 w-12 text-muted-foreground" />
            <h3 className="mt-4 text-lg font-medium">No campaigns yet</h3>
            <p className="mt-1 text-sm text-muted-foreground">
              Create your first campaign to get started with AI voice NPCs.
            </p>
            <Button className="mt-4" render={<Link href="/campaigns/new" />}>
                <Plus className="mr-1 h-4 w-4" />
                Create Campaign
            </Button>
          </CardContent>
        </Card>
      )}
    </div>
  );
}
