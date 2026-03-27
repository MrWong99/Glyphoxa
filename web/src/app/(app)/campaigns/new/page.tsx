"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";
import Link from "next/link";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Breadcrumbs } from "@/components/breadcrumbs";
import { useCreateCampaign } from "@/lib/hooks";
import type { GameSystem } from "@/lib/types";

const gameSystems: GameSystem[] = [
  "D&D 5e",
  "D&D 5e (2024)",
  "Pathfinder 2e",
  "Das Schwarze Auge",
  "Call of Cthulhu",
  "Shadowrun",
  "Fate Core",
  "Savage Worlds",
  "Other",
];

export default function NewCampaignPage() {
  const router = useRouter();
  const createCampaign = useCreateCampaign();
  const [name, setName] = useState("");
  const [gameSystem, setGameSystem] = useState<string>("");
  const [description, setDescription] = useState("");
  const [language, setLanguage] = useState("en");
  const [touched, setTouched] = useState<Record<string, boolean>>({});

  const errors: Record<string, string> = {};
  if (!name.trim()) errors.name = "Campaign name is required";
  if (!gameSystem) errors.gameSystem = "Please select a game system";

  function touch(field: string) {
    setTouched((prev) => ({ ...prev, [field]: true }));
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setTouched({ name: true, gameSystem: true });
    if (Object.keys(errors).length > 0) return;
    try {
      await createCampaign.mutateAsync({
        name: name.trim(),
        game_system: gameSystem,
        description: description.trim(),
        language,
      });
      router.push("/campaigns");
    } catch {
      // Error is handled by the mutation's onError callback.
    }
  }

  return (
    <div className="mx-auto max-w-2xl space-y-6">
      <Breadcrumbs
        items={[
          { label: "Campaigns", href: "/campaigns" },
          { label: "New Campaign" },
        ]}
      />

      <h1 className="text-2xl font-bold tracking-tight">New Campaign</h1>

      <Card>
        <CardHeader>
          <CardTitle>Campaign Details</CardTitle>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSubmit} className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="name">Campaign Name</Label>
              <Input
                id="name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                onBlur={() => touch("name")}
                placeholder="Die Chroniken von Rabenheim"
                aria-invalid={touched.name && !!errors.name}
                required
              />
              {touched.name && errors.name && (
                <p className="text-xs text-destructive">{errors.name}</p>
              )}
            </div>

            <div className="space-y-2">
              <Label htmlFor="system">Game System</Label>
              <Select value={gameSystem} onValueChange={(v) => { if (v) setGameSystem(v); touch("gameSystem"); }}>
                <SelectTrigger aria-invalid={touched.gameSystem && !!errors.gameSystem}>
                  <SelectValue placeholder="Select a game system" />
                </SelectTrigger>
                <SelectContent>
                  {gameSystems.map((system) => (
                    <SelectItem key={system} value={system}>
                      {system}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {touched.gameSystem && errors.gameSystem && (
                <p className="text-xs text-destructive">{errors.gameSystem}</p>
              )}
            </div>

            <div className="space-y-2">
              <Label htmlFor="language">Language</Label>
              <Select value={language} onValueChange={(v) => v && setLanguage(v)}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="en">English</SelectItem>
                  <SelectItem value="de">Deutsch</SelectItem>
                  <SelectItem value="fr">Fran&ccedil;ais</SelectItem>
                  <SelectItem value="es">Espa&ntilde;ol</SelectItem>
                </SelectContent>
              </Select>
            </div>

            <div className="space-y-2">
              <Label htmlFor="description">Description</Label>
              <Textarea
                id="description"
                value={description}
                onChange={(e) => setDescription(e.target.value)}
                placeholder="Describe your campaign setting, story hooks, and key themes..."
                rows={6}
              />
            </div>

            <div className="flex justify-end gap-2 pt-2">
              <Button variant="outline" render={<Link href="/campaigns" />}>
                Cancel
              </Button>
              <Button
                type="submit"
                disabled={createCampaign.isPending}
              >
                {createCampaign.isPending ? "Creating..." : "Create Campaign"}
              </Button>
            </div>
          </form>
        </CardContent>
      </Card>
    </div>
  );
}
