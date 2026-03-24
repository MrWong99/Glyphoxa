"use client";

import Link from "next/link";
import { Plus, Swords, AlertTriangle } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Breadcrumbs } from "@/components/breadcrumbs";
import { useCampaigns, useHasRole } from "@/lib/hooks";
import { formatRelativeTime } from "@/lib/utils";

function CampaignCardSkeleton() {
  return (
    <Card>
      <CardContent className="space-y-3 p-6">
        <div className="flex items-start justify-between">
          <div className="space-y-2">
            <div className="h-5 w-32 rounded bg-muted skeleton-shimmer" />
            <div className="h-4 w-20 rounded bg-muted skeleton-shimmer" />
          </div>
          <div className="h-5 w-5 rounded bg-muted skeleton-shimmer" />
        </div>
        <div className="h-4 w-48 rounded bg-muted skeleton-shimmer" />
      </CardContent>
    </Card>
  );
}

export default function CampaignsPage() {
  const { data: campaigns, isLoading, isError, error } = useCampaigns();
  const canCreate = useHasRole("dm");

  return (
    <div className="space-y-6">
      <Breadcrumbs items={[{ label: "Campaigns" }]} />

      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">Campaigns</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            Manage your tabletop RPG campaigns and their AI voice NPCs.
          </p>
        </div>
        {canCreate && (
          <Button render={<Link href="/campaigns/new" />}>
              <Plus className="mr-1 h-4 w-4" />
              New Campaign
          </Button>
        )}
      </div>

      {isLoading ? (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          <CampaignCardSkeleton />
          <CampaignCardSkeleton />
          <CampaignCardSkeleton />
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
              <Card className="group h-full transition-all duration-200 hover:border-primary/30 hover:shadow-lg hover:shadow-primary/5">
                <CardContent className="space-y-3 p-6">
                  <div className="flex items-start justify-between">
                    <div>
                      <h3 className="font-semibold leading-tight group-hover:text-primary transition-colors">
                        {campaign.name}
                      </h3>
                      <p className="mt-1 text-sm text-muted-foreground">
                        {campaign.game_system}
                      </p>
                    </div>
                    <Swords className="h-5 w-5 text-muted-foreground/50 transition-colors group-hover:text-primary/50" />
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
                      <span className="mr-1.5 h-1.5 w-1.5 rounded-full bg-white animate-pulse" />
                      Active session
                    </Badge>
                  )}
                </CardContent>
              </Card>
            </Link>
          ))}

          {/* Create new card */}
          {canCreate && (
            <Link href="/campaigns/new">
              <Card className="flex h-full items-center justify-center border-dashed transition-all duration-200 hover:border-primary/40 hover:bg-primary/5">
                <CardContent className="py-12 text-center">
                  <div className="mx-auto mb-3 flex h-12 w-12 items-center justify-center rounded-xl bg-primary/10">
                    <Plus className="h-6 w-6 text-primary" />
                  </div>
                  <p className="text-sm font-medium text-muted-foreground">
                    Create New Campaign
                  </p>
                </CardContent>
              </Card>
            </Link>
          )}
        </div>
      ) : (
        <Card className="border-dashed">
          <CardContent className="py-16 text-center">
            <div className="mx-auto mb-4 flex h-16 w-16 items-center justify-center rounded-2xl bg-primary/10">
              <Swords className="h-8 w-8 text-primary/60" />
            </div>
            <h3 className="text-lg font-medium">No campaigns yet</h3>
            <p className="mx-auto mt-2 max-w-sm text-sm text-muted-foreground">
              Create your first campaign to get started with AI voice NPCs for your tabletop sessions.
            </p>
            {canCreate && (
              <Button className="mt-6" render={<Link href="/campaigns/new" />}>
                  <Plus className="mr-1 h-4 w-4" />
                  Create Campaign
              </Button>
            )}
          </CardContent>
        </Card>
      )}
    </div>
  );
}
