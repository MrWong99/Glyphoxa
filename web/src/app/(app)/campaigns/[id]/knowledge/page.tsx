"use client";

import { useParams } from "next/navigation";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { useCampaign, useKnowledgeGraph } from "@/lib/hooks";
import { Loader2, Network, AlertTriangle } from "lucide-react";
import { KnowledgeGraphVisualization } from "./knowledge-graph";

export default function KnowledgeGraphPage() {
  const { id } = useParams<{ id: string }>();
  const { data: campaign, isLoading: campaignLoading } = useCampaign(id);
  const { data: graphData, isLoading: graphLoading } = useKnowledgeGraph(id);

  if (campaignLoading || graphLoading) {
    return (
      <div className="flex items-center justify-center py-20">
        <Loader2 className="h-8 w-8 animate-spin text-primary" />
      </div>
    );
  }

  if (!campaign) {
    return (
      <div className="flex flex-col items-center justify-center gap-4 py-20">
        <AlertTriangle className="h-12 w-12 text-muted-foreground/40" />
        <h2 className="text-xl font-semibold">Campaign not found</h2>
      </div>
    );
  }

  const entities = graphData?.entities ?? [];
  const relationships = graphData?.relationships ?? [];

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">
          Knowledge Graph
        </h1>
        <p className="text-muted-foreground">
          {campaign.name} — {entities.length} entities, {relationships.length}{" "}
          relationships
        </p>
      </div>

      {/* Entity Type Legend */}
      <div className="flex flex-wrap gap-2">
        {Array.from(new Set(entities.map((e) => e.type))).map((type) => (
          <Badge key={type} variant="outline">
            {type} ({entities.filter((e) => e.type === type).length})
          </Badge>
        ))}
      </div>

      {/* Graph Visualization */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <Network className="h-5 w-5" />
            Entity Relationships
          </CardTitle>
        </CardHeader>
        <CardContent>
          {entities.length === 0 ? (
            <div className="flex flex-col items-center justify-center gap-4 py-16">
              <Network className="h-12 w-12 text-muted-foreground/30" />
              <p className="text-muted-foreground">
                No knowledge entities yet. Run a session to start building the
                knowledge graph.
              </p>
            </div>
          ) : (
            <KnowledgeGraphVisualization
              entities={entities}
              relationships={relationships}
            />
          )}
        </CardContent>
      </Card>

      {/* Entity List */}
      <Card>
        <CardHeader>
          <CardTitle>All Entities</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {entities.map((entity) => (
              <div
                key={entity.id}
                className="rounded-lg border border-border/50 p-3 space-y-1"
              >
                <div className="flex items-center justify-between">
                  <span className="font-medium text-sm">{entity.name}</span>
                  <Badge variant="secondary" className="text-xs">
                    {entity.type}
                  </Badge>
                </div>
                {entity.attributes &&
                  Object.keys(entity.attributes).length > 0 && (
                    <div className="text-xs text-muted-foreground">
                      {Object.entries(entity.attributes)
                        .filter(([k]) => k !== "relationships")
                        .slice(0, 3)
                        .map(([k, v]) => (
                          <span key={k} className="mr-2">
                            {k}: {String(v)}
                          </span>
                        ))}
                    </div>
                  )}
              </div>
            ))}
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
