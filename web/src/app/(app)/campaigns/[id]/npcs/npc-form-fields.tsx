"use client";

import { useState } from "react";
import { Plus, X, Volume2, ImagePlus, Wand2 } from "lucide-react";
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
import { toast } from "sonner";

export const PERSONALITY_TEMPLATES = [
  { label: "Gruff Guard", text: "A stern but fair guard who has watched over this area for decades. Suspicious of strangers, protective of the townsfolk, speaks in short, clipped sentences." },
  { label: "Cheerful Innkeeper", text: "A warm and welcoming innkeeper who loves nothing more than a good story and a full tavern. Knows all the local gossip and is always ready with advice." },
  { label: "Mysterious Merchant", text: "A traveling merchant who deals in rare and unusual goods. Speaks cryptically, knows more than they let on, and always has the right item for the right price." },
  { label: "Wise Elder", text: "An ancient scholar or village elder with deep knowledge of history and lore. Patient and thoughtful, speaks in measured tones, occasionally shares prophecies." },
];

export interface NPCFormState {
  name: string;
  personality: string;
  voiceProvider: string;
  voiceId: string;
  engine: "cascaded" | "s2s" | "sentence";
  budgetTier: "fast" | "standard" | "deep";
  knowledgeScope: string[];
  behaviorRules: string[];
  addressOnly: boolean;
}

interface NPCFormFieldsProps {
  state: NPCFormState;
  onChange: <K extends keyof NPCFormState>(key: K, value: NPCFormState[K]) => void;
  touched: Record<string, boolean>;
  errors: Record<string, string>;
  onTouch: (field: string) => void;
}

export function NPCFormFields({ state, onChange, touched, errors, onTouch }: NPCFormFieldsProps) {
  return (
    <>
      {/* Identity with Avatar Placeholder */}
      <Card>
        <CardHeader><CardTitle>Identity</CardTitle></CardHeader>
        <CardContent className="space-y-4">
          <div className="flex gap-4">
            <div className="flex h-20 w-20 shrink-0 items-center justify-center rounded-xl border-2 border-dashed border-border bg-muted/30 transition-colors hover:border-primary/30 hover:bg-primary/5 cursor-pointer" role="button" aria-label="Upload character portrait" tabIndex={0}>
              <ImagePlus className="h-6 w-6 text-muted-foreground/50" />
            </div>
            <div className="flex-1 space-y-2">
              <Label htmlFor="npc-name">Name</Label>
              <Input
                id="npc-name"
                value={state.name}
                onChange={(e) => onChange("name", e.target.value)}
                onBlur={() => onTouch("name")}
                placeholder="Heinrich der W&auml;chter"
                aria-invalid={touched.name && !!errors.name}
              />
              {touched.name && errors.name && (
                <p className="text-xs text-destructive">{errors.name}</p>
              )}
            </div>
          </div>
        </CardContent>
      </Card>

      {/* Personality with Templates */}
      <Card>
        <CardHeader>
          <div className="flex items-center justify-between">
            <CardTitle>Personality</CardTitle>
            <div className="flex flex-wrap gap-1.5">
              {PERSONALITY_TEMPLATES.map((t) => (
                <Button
                  key={t.label}
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="h-7 text-xs"
                  onClick={() => {
                    onChange("personality", t.text);
                    toast.info(`Template "${t.label}" applied`);
                  }}
                >
                  <Wand2 className="mr-1 h-3 w-3" />
                  {t.label}
                </Button>
              ))}
            </div>
          </div>
        </CardHeader>
        <CardContent className="space-y-2">
          <Textarea
            value={state.personality}
            onChange={(e) => onChange("personality", e.target.value)}
            onBlur={() => onTouch("personality")}
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
            <p className="text-xs text-muted-foreground">{state.personality.length} / 2000 characters</p>
          </div>
        </CardContent>
      </Card>

      {/* Voice with Preview */}
      <Card>
        <CardHeader><CardTitle>Voice</CardTitle></CardHeader>
        <CardContent className="space-y-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <Label>Provider</Label>
              <Select value={state.voiceProvider} onValueChange={(v) => { if (v) onChange("voiceProvider", v); }}>
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
              <div className="flex gap-2">
                <Input
                  id="voice-id"
                  value={state.voiceId}
                  onChange={(e) => onChange("voiceId", e.target.value)}
                  onBlur={() => onTouch("voiceId")}
                  placeholder="Helmut"
                  aria-invalid={touched.voiceId && !!errors.voiceId}
                  className="flex-1"
                />
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  disabled={!state.voiceId.trim()}
                  aria-label="Preview voice"
                  onClick={() => toast.info("Voice preview coming soon")}
                >
                  <Volume2 className="h-4 w-4" />
                </Button>
              </div>
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
                  aria-checked={state.engine === eng}
                  onClick={() => onChange("engine", eng)}
                  className={`rounded-lg border p-3 text-left transition-all duration-200 ${
                    state.engine === eng
                      ? "border-primary bg-primary/10 shadow-sm shadow-primary/10"
                      : "border-border hover:border-primary/50 hover:bg-primary/5"
                  }`}
                >
                  <p className="font-medium capitalize">{eng === "s2s" ? "Speech-to-Speech" : eng}</p>
                  <p className="mt-1 text-xs text-muted-foreground">
                    {eng === "cascaded"
                      ? "STT \u2192 LLM \u2192 TTS \u2014 Best quality"
                      : eng === "s2s"
                        ? "Direct speech pipeline \u2014 Lowest latency"
                        : "Sentence cascade \u2014 Good balance"}
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
                  aria-checked={state.budgetTier === tier}
                  onClick={() => onChange("budgetTier", tier)}
                  className={`rounded-lg border p-3 text-left transition-all duration-200 ${
                    state.budgetTier === tier
                      ? "border-primary bg-primary/10 shadow-sm shadow-primary/10"
                      : "border-border hover:border-primary/50 hover:bg-primary/5"
                  }`}
                >
                  <p className="font-medium capitalize">{tier}</p>
                  <p className="mt-1 text-xs text-muted-foreground">
                    {tier === "fast"
                      ? "Quick responses, basic reasoning"
                      : tier === "standard"
                        ? "Balanced quality and speed"
                        : "Thorough reasoning + tool use"}
                  </p>
                </button>
              ))}
            </div>
          </fieldset>
        </CardContent>
      </Card>

      {/* Knowledge */}
      <NPCListEditor
        title="Knowledge Scope"
        items={state.knowledgeScope}
        onChange={(items) => onChange("knowledgeScope", items)}
        placeholder="Add knowledge topic..."
        emptyMessage='Add topics this NPC has knowledge about (e.g., "local tavern history", "guard patrol routes").'
        variant="badge"
      />

      {/* Behavior Rules */}
      <NPCListEditor
        title="Behavior Rules"
        items={state.behaviorRules}
        onChange={(items) => onChange("behaviorRules", items)}
        placeholder="Add behavior rule..."
        emptyMessage='Define rules that govern this NPC&apos;s behavior (e.g., "Never reveal the secret passage").'
        variant="numbered"
      />

      {/* Advanced */}
      <Card>
        <CardHeader><CardTitle>Advanced</CardTitle></CardHeader>
        <CardContent>
          <label className="flex cursor-pointer items-center gap-3 rounded-lg p-2 transition-colors hover:bg-muted/30">
            <input
              type="checkbox"
              checked={state.addressOnly}
              onChange={(e) => onChange("addressOnly", e.target.checked)}
              className="h-4 w-4 rounded border-border accent-primary"
            />
            <div>
              <p className="text-sm font-medium">Address Only</p>
              <p className="text-xs text-muted-foreground">
                NPC only responds when directly addressed by name
              </p>
            </div>
          </label>
        </CardContent>
      </Card>
    </>
  );
}

/** Reusable list editor for knowledge scope and behavior rules. */
function NPCListEditor({
  title,
  items,
  onChange,
  placeholder,
  emptyMessage,
  variant,
}: {
  title: string;
  items: string[];
  onChange: (items: string[]) => void;
  placeholder: string;
  emptyMessage: string;
  variant: "badge" | "numbered";
}) {
  const [newItem, setNewItem] = useState("");

  function add() {
    if (newItem.trim()) {
      onChange([...items, newItem.trim()]);
      setNewItem("");
    }
  }

  return (
    <Card>
      <CardHeader><CardTitle>{title}</CardTitle></CardHeader>
      <CardContent className="space-y-3">
        {items.length === 0 && (
          <p className="text-sm text-muted-foreground">{emptyMessage}</p>
        )}
        {variant === "badge" ? (
          <div className="flex flex-wrap gap-2">
            {items.map((item, i) => (
              <Badge key={i} variant="secondary" className="gap-1 pr-1">
                {item}
                <button
                  type="button"
                  aria-label={`Remove ${item}`}
                  onClick={() => onChange(items.filter((_, j) => j !== i))}
                  className="ml-1 rounded-full p-0.5 hover:bg-muted"
                >
                  <X className="h-3 w-3" />
                </button>
              </Badge>
            ))}
          </div>
        ) : items.length > 0 ? (
          <ul className="space-y-2">
            {items.map((rule, i) => (
              <li
                key={i}
                className="flex items-center justify-between rounded-lg border border-border px-3 py-2.5 text-sm transition-colors hover:bg-muted/30"
              >
                <span className="flex items-center gap-2">
                  <span className="flex h-5 w-5 shrink-0 items-center justify-center rounded-full bg-primary/10 text-xs font-medium text-primary">{i + 1}</span>
                  {rule}
                </span>
                <button
                  type="button"
                  aria-label={`Remove rule: ${rule}`}
                  onClick={() => onChange(items.filter((_, j) => j !== i))}
                  className="ml-2 text-muted-foreground hover:text-foreground"
                >
                  <X className="h-4 w-4" />
                </button>
              </li>
            ))}
          </ul>
        ) : null}
        <div className="flex gap-2">
          <Input
            value={newItem}
            onChange={(e) => setNewItem(e.target.value)}
            placeholder={placeholder}
            onKeyDown={(e) => e.key === "Enter" && (e.preventDefault(), add())}
          />
          <Button type="button" variant="outline" size="icon" aria-label={`Add ${title.toLowerCase()}`} onClick={add}>
            <Plus className="h-4 w-4" />
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}
