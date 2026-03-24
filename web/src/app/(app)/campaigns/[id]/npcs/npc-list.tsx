"use client";

import Link from "next/link";
import { Plus, Mic, Brain, Zap, Sparkles } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { useNPCs, useHasRole } from "@/lib/hooks";

const engineLabels: Record<string, string> = {
  cascaded: "Cascaded",
  s2s: "Speech-to-Speech",
  sentence: "Sentence",
};

const tierIcons: Record<string, typeof Zap> = {
  fast: Zap,
  standard: Brain,
  deep: Brain,
};

interface NPCListProps {
  campaignId: string;
}

export function NPCList({ campaignId }: NPCListProps) {
  const { data: npcs, isLoading } = useNPCs(campaignId);
  const canCreate = useHasRole("dm");

  if (isLoading) {
    return (
      <div className="grid gap-4 sm:grid-cols-2">
        {[1, 2].map((i) => (
          <Card key={i}>
            <CardContent className="space-y-3 p-6">
              <div className="flex items-start justify-between">
                <div className="h-5 w-24 rounded bg-muted skeleton-shimmer" />
                <div className="h-5 w-16 rounded bg-muted skeleton-shimmer" />
              </div>
              <div className="h-4 w-full rounded bg-muted skeleton-shimmer" />
              <div className="h-4 w-2/3 rounded bg-muted skeleton-shimmer" />
            </CardContent>
          </Card>
        ))}
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h2 className="text-lg font-semibold">NPCs</h2>
        {canCreate && (
          <Button render={<Link href={`/campaigns/${campaignId}/npcs/new`} />}>
              <Plus className="mr-1 h-4 w-4" />
              New NPC
          </Button>
        )}
      </div>

      {npcs && npcs.length > 0 ? (
        <div className="grid gap-4 sm:grid-cols-2">
          {npcs.map((npc) => {
            const TierIcon = tierIcons[npc.budget_tier] ?? Brain;
            return (
              <Link
                key={npc.id}
                href={`/campaigns/${campaignId}/npcs/${npc.id}`}
              >
                <Card className="group h-full transition-all duration-200 hover:border-primary/30 hover:shadow-lg hover:shadow-primary/5">
                  <CardContent className="space-y-2 p-4">
                    <div className="flex items-start justify-between">
                      <h3 className="font-semibold group-hover:text-primary transition-colors">{npc.name}</h3>
                      <Badge variant="secondary">
                        {engineLabels[npc.engine] ?? npc.engine}
                      </Badge>
                    </div>
                    <p className="line-clamp-2 text-sm text-muted-foreground">
                      {npc.personality}
                    </p>
                    <div className="flex items-center gap-3 text-xs text-muted-foreground">
                      <span className="flex items-center gap-1">
                        <Mic className="h-3 w-3" />
                        {npc.voice_provider}/{npc.voice_id}
                      </span>
                      <span className="flex items-center gap-1">
                        <TierIcon className="h-3 w-3" />
                        {npc.budget_tier}
                      </span>
                    </div>
                  </CardContent>
                </Card>
              </Link>
            );
          })}
        </div>
      ) : (
        <Card className="border-dashed">
          <CardContent className="py-12 text-center">
            <div className="mx-auto mb-3 flex h-12 w-12 items-center justify-center rounded-xl bg-primary/10">
              <Sparkles className="h-6 w-6 text-primary/60" />
            </div>
            <p className="font-medium">No NPCs yet</p>
            <p className="mt-1 text-sm text-muted-foreground">
              Create your first NPC to bring your campaign to life.
            </p>
            {canCreate && (
              <Button className="mt-4" size="sm" render={<Link href={`/campaigns/${campaignId}/npcs/new`} />}>
                  <Plus className="mr-1 h-4 w-4" />
                  Create NPC
              </Button>
            )}
          </CardContent>
        </Card>
      )}
    </div>
  );
}
