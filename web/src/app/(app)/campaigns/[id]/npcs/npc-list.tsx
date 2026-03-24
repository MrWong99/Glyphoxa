"use client";

import Link from "next/link";
import { Plus, Mic, Brain, Zap } from "lucide-react";
import { Card, CardContent } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { useNPCs } from "@/lib/hooks";

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

  if (isLoading) {
    return (
      <div className="grid gap-4 sm:grid-cols-2">
        {[1, 2].map((i) => (
          <Card key={i} className="animate-pulse">
            <CardContent className="h-32 p-6" />
          </Card>
        ))}
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h2 className="text-lg font-semibold">NPCs</h2>
        <Button render={<Link href={`/campaigns/${campaignId}/npcs/new`} />}>
            <Plus className="mr-1 h-4 w-4" />
            New NPC
        </Button>
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
                <Card className="h-full transition-colors hover:bg-accent/50">
                  <CardContent className="space-y-2 p-4">
                    <div className="flex items-start justify-between">
                      <h3 className="font-semibold">{npc.name}</h3>
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
        <Card>
          <CardContent className="py-8 text-center">
            <p className="text-sm text-muted-foreground">
              No NPCs yet. Create your first NPC to bring your campaign to life.
            </p>
            <Button className="mt-3" size="sm" render={<Link href={`/campaigns/${campaignId}/npcs/new`} />}>
                <Plus className="mr-1 h-4 w-4" />
                Create NPC
            </Button>
          </CardContent>
        </Card>
      )}
    </div>
  );
}
