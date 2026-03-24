"use client";

import { use } from "react";
import { useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { ArrowLeft, Save, Plus, X } from "lucide-react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Badge } from "@/components/ui/badge";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useCampaign, useCreateNPC } from "@/lib/hooks";

export default function NewNPCPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id: campaignId } = use(params);
  const router = useRouter();
  const { data: campaign } = useCampaign(campaignId);
  const createNPC = useCreateNPC(campaignId);

  const [name, setName] = useState("");
  const [personality, setPersonality] = useState("");
  const [voiceProvider, setVoiceProvider] = useState("elevenlabs");
  const [voiceId, setVoiceId] = useState("");
  const [engine, setEngine] = useState<"cascaded" | "s2s" | "sentence">("cascaded");
  const [budgetTier, setBudgetTier] = useState<"fast" | "standard" | "deep">("standard");
  const [knowledgeScope, setKnowledgeScope] = useState<string[]>([]);
  const [behaviorRules, setBehaviorRules] = useState<string[]>([]);
  const [addressOnly, setAddressOnly] = useState(false);
  const [newKnowledge, setNewKnowledge] = useState("");
  const [newRule, setNewRule] = useState("");
  const [touched, setTouched] = useState<Record<string, boolean>>({});

  const errors: Record<string, string> = {};
  if (!name.trim()) errors.name = "NPC name is required";
  if (!voiceId.trim()) errors.voiceId = "Voice ID is required";
  if (!personality.trim()) errors.personality = "Personality description is required";
  if (personality.length > 2000) errors.personality = "Personality must be under 2000 characters";

  function touch(field: string) {
    setTouched((prev) => ({ ...prev, [field]: true }));
  }

  async function handleSave() {
    setTouched({ name: true, voiceId: true, personality: true });
    if (Object.keys(errors).length > 0) return;
    await createNPC.mutateAsync({
      name: name.trim(),
      personality: personality.trim(),
      voice_provider: voiceProvider,
      voice_id: voiceId.trim(),
      engine,
      budget_tier: budgetTier,
      knowledge_scope: knowledgeScope,
      behavior_rules: behaviorRules,
      address_only: addressOnly,
    });
    router.push(`/campaigns/${campaignId}`);
  }

  function addKnowledge() {
    if (newKnowledge.trim()) {
      setKnowledgeScope([...knowledgeScope, newKnowledge.trim()]);
      setNewKnowledge("");
    }
  }

  function addRule() {
    if (newRule.trim()) {
      setBehaviorRules([...behaviorRules, newRule.trim()]);
      setNewRule("");
    }
  }

  return (
    <div className="mx-auto max-w-3xl space-y-6">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <Button variant="ghost" size="icon" aria-label="Back to campaign" render={<Link href={`/campaigns/${campaignId}`} />}>
              <ArrowLeft className="h-4 w-4" />
          </Button>
          <div>
            <p className="text-sm text-muted-foreground">
              {campaign?.name ?? "Campaign"} / NPCs
            </p>
            <h1 className="text-2xl font-bold">New NPC</h1>
          </div>
        </div>
        <Button onClick={handleSave} disabled={createNPC.isPending}>
          <Save className="mr-1 h-4 w-4" />
          {createNPC.isPending ? "Creating..." : "Create NPC"}
        </Button>
      </div>

      {/* Identity */}
      <Card>
        <CardHeader><CardTitle>Identity</CardTitle></CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="npc-name">Name</Label>
            <Input
              id="npc-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              onBlur={() => touch("name")}
              placeholder="Heinrich der Wächter"
              aria-invalid={touched.name && !!errors.name}
            />
            {touched.name && errors.name && (
              <p className="text-xs text-destructive">{errors.name}</p>
            )}
          </div>
        </CardContent>
      </Card>

      {/* Personality */}
      <Card>
        <CardHeader><CardTitle>Personality</CardTitle></CardHeader>
        <CardContent className="space-y-2">
          <Textarea
            value={personality}
            onChange={(e) => setPersonality(e.target.value)}
            onBlur={() => touch("personality")}
            placeholder="A stern but fair city guard who has watched the gates of Rabenheim for over 20 years..."
            rows={6}
            aria-invalid={touched.personality && !!errors.personality}
          />
          <div className="flex items-center justify-between">
            {touched.personality && errors.personality ? (
              <p className="text-xs text-destructive">{errors.personality}</p>
            ) : (
              <span />
            )}
            <p className="text-xs text-muted-foreground">{personality.length} / 2000 characters</p>
          </div>
        </CardContent>
      </Card>

      {/* Voice */}
      <Card>
        <CardHeader><CardTitle>Voice</CardTitle></CardHeader>
        <CardContent className="space-y-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <Label>Provider</Label>
              <Select value={voiceProvider} onValueChange={(v) => v && setVoiceProvider(v)}>
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem value="elevenlabs">ElevenLabs</SelectItem>
                  <SelectItem value="azure">Azure</SelectItem>
                  <SelectItem value="google">Google</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-2">
              <Label htmlFor="voice-id">Voice ID</Label>
              <Input
                id="voice-id"
                value={voiceId}
                onChange={(e) => setVoiceId(e.target.value)}
                onBlur={() => touch("voiceId")}
                placeholder="Helmut"
                aria-invalid={touched.voiceId && !!errors.voiceId}
              />
              {touched.voiceId && errors.voiceId && (
                <p className="text-xs text-destructive">{errors.voiceId}</p>
              )}
            </div>
          </div>
        </CardContent>
      </Card>

      {/* Engine & Tier */}
      <Card>
        <CardHeader><CardTitle>Engine &amp; Budget Tier</CardTitle></CardHeader>
        <CardContent className="space-y-6">
          <fieldset className="space-y-2">
            <legend className="text-sm font-medium">Engine</legend>
            <div className="grid gap-3 sm:grid-cols-3" role="radiogroup" aria-label="Engine selection">
              {(["cascaded", "s2s", "sentence"] as const).map((eng) => (
                <button
                  key={eng}
                  type="button"
                  role="radio"
                  aria-checked={engine === eng}
                  onClick={() => setEngine(eng)}
                  className={`rounded-lg border p-3 text-left transition-colors ${engine === eng ? "border-primary bg-primary/10" : "border-border hover:border-primary/50"}`}
                >
                  <p className="font-medium capitalize">{eng === "s2s" ? "Speech-to-Speech" : eng}</p>
                  <p className="mt-1 text-xs text-muted-foreground">
                    {eng === "cascaded" ? "STT → LLM → TTS — Best quality" : eng === "s2s" ? "Direct speech pipeline — Lowest latency" : "Sentence cascade — Good balance"}
                  </p>
                </button>
              ))}
            </div>
          </fieldset>
          <fieldset className="space-y-2">
            <legend className="text-sm font-medium">Budget Tier</legend>
            <div className="grid gap-3 sm:grid-cols-3" role="radiogroup" aria-label="Budget tier selection">
              {(["fast", "standard", "deep"] as const).map((tier) => (
                <button
                  key={tier}
                  type="button"
                  role="radio"
                  aria-checked={budgetTier === tier}
                  onClick={() => setBudgetTier(tier)}
                  className={`rounded-lg border p-3 text-left transition-colors ${budgetTier === tier ? "border-primary bg-primary/10" : "border-border hover:border-primary/50"}`}
                >
                  <p className="font-medium capitalize">{tier}</p>
                  <p className="mt-1 text-xs text-muted-foreground">
                    {tier === "fast" ? "Quick responses, basic reasoning" : tier === "standard" ? "Balanced quality and speed" : "Thorough reasoning + tool use"}
                  </p>
                </button>
              ))}
            </div>
          </fieldset>
        </CardContent>
      </Card>

      {/* Knowledge */}
      <Card>
        <CardHeader><CardTitle>Knowledge Scope</CardTitle></CardHeader>
        <CardContent className="space-y-3">
          <div className="flex flex-wrap gap-2">
            {knowledgeScope.map((item, i) => (
              <Badge key={i} variant="secondary" className="gap-1 pr-1">
                {item}
                <button type="button" aria-label={`Remove ${item}`} onClick={() => setKnowledgeScope(knowledgeScope.filter((_, j) => j !== i))} className="ml-1 rounded-full p-0.5 hover:bg-muted">
                  <X className="h-3 w-3" />
                </button>
              </Badge>
            ))}
          </div>
          <div className="flex gap-2">
            <Input value={newKnowledge} onChange={(e) => setNewKnowledge(e.target.value)} placeholder="Add knowledge topic..." onKeyDown={(e) => e.key === "Enter" && (e.preventDefault(), addKnowledge())} />
            <Button type="button" variant="outline" size="icon" aria-label="Add knowledge topic" onClick={addKnowledge}><Plus className="h-4 w-4" /></Button>
          </div>
        </CardContent>
      </Card>

      {/* Behavior Rules */}
      <Card>
        <CardHeader><CardTitle>Behavior Rules</CardTitle></CardHeader>
        <CardContent className="space-y-3">
          {behaviorRules.length > 0 && (
            <ul className="space-y-2">
              {behaviorRules.map((rule, i) => (
                <li key={i} className="flex items-center justify-between rounded-md border border-border px-3 py-2 text-sm">
                  <span>{rule}</span>
                  <button type="button" aria-label={`Remove rule: ${rule}`} onClick={() => setBehaviorRules(behaviorRules.filter((_, j) => j !== i))} className="ml-2 text-muted-foreground hover:text-foreground">
                    <X className="h-4 w-4" />
                  </button>
                </li>
              ))}
            </ul>
          )}
          <div className="flex gap-2">
            <Input value={newRule} onChange={(e) => setNewRule(e.target.value)} placeholder="Add behavior rule..." onKeyDown={(e) => e.key === "Enter" && (e.preventDefault(), addRule())} />
            <Button type="button" variant="outline" size="icon" aria-label="Add behavior rule" onClick={addRule}><Plus className="h-4 w-4" /></Button>
          </div>
        </CardContent>
      </Card>

      {/* Advanced */}
      <Card>
        <CardHeader><CardTitle>Advanced</CardTitle></CardHeader>
        <CardContent>
          <label className="flex items-center gap-3">
            <input type="checkbox" checked={addressOnly} onChange={(e) => setAddressOnly(e.target.checked)} className="h-4 w-4 rounded border-border" />
            <div>
              <p className="text-sm font-medium">Address Only</p>
              <p className="text-xs text-muted-foreground">NPC only responds when directly addressed by name</p>
            </div>
          </label>
        </CardContent>
      </Card>
    </div>
  );
}
