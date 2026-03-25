"use client";

import { useState } from "react";
import { Card, CardContent, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectTrigger,
  SelectValue,
  SelectContent,
  SelectItem,
} from "@/components/ui/select";
import { Badge } from "@/components/ui/badge";
import { useTestProvider, useHasRole } from "@/lib/hooks";
import type { ProviderTestResult } from "@/lib/types";
import {
  Zap,
  Mic,
  Volume2,
  CheckCircle,
  XCircle,
  Loader2,
  ShieldAlert,
} from "lucide-react";

const PROVIDER_TYPES = [
  {
    type: "llm",
    label: "LLM (Language Model)",
    icon: Zap,
    providers: ["openai", "anthropic", "google"],
  },
  {
    type: "stt",
    label: "STT (Speech-to-Text)",
    icon: Mic,
    providers: ["deepgram", "whisper"],
  },
  {
    type: "tts",
    label: "TTS (Text-to-Speech)",
    icon: Volume2,
    providers: ["elevenlabs", "openai"],
  },
];

function ProviderTestCard({
  type,
  label,
  icon: Icon,
  providers,
}: {
  type: string;
  label: string;
  icon: typeof Zap;
  providers: string[];
}) {
  const [provider, setProvider] = useState(providers[0]);
  const [apiKey, setApiKey] = useState("");
  const [baseUrl, setBaseUrl] = useState("");
  const [result, setResult] = useState<ProviderTestResult | null>(null);
  const testMutation = useTestProvider();

  async function handleTest() {
    setResult(null);
    const res = await testMutation.mutateAsync({
      type,
      provider,
      api_key: apiKey,
      base_url: baseUrl || undefined,
    });
    setResult(res);
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-lg">
          <Icon className="h-5 w-5 text-primary" />
          {label}
        </CardTitle>
        <CardDescription>
          Test your {label.toLowerCase()} provider connection
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        <div className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-2">
            <Label>Provider</Label>
            <Select value={provider} onValueChange={(v) => setProvider(v ?? providers[0])}>
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {providers.map((p) => (
                  <SelectItem key={p} value={p}>
                    {p.charAt(0).toUpperCase() + p.slice(1)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-2">
            <Label>API Key</Label>
            <Input
              type="password"
              placeholder="Enter API key"
              value={apiKey}
              onChange={(e) => setApiKey(e.target.value)}
            />
          </div>
        </div>

        <div className="space-y-2">
          <Label>
            Base URL{" "}
            <span className="text-muted-foreground">(optional)</span>
          </Label>
          <Input
            placeholder="Custom endpoint URL"
            value={baseUrl}
            onChange={(e) => setBaseUrl(e.target.value)}
          />
        </div>

        <div className="flex items-center gap-3">
          <Button
            onClick={handleTest}
            disabled={!apiKey || testMutation.isPending}
          >
            {testMutation.isPending ? (
              <>
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                Testing...
              </>
            ) : (
              "Test Connection"
            )}
          </Button>

          {result && (
            <div className="flex items-center gap-2">
              {result.status === "ok" ? (
                <>
                  <CheckCircle className="h-4 w-4 text-green-500" />
                  <Badge variant="outline" className="border-green-500/30 text-green-500">
                    Connected ({result.latency_ms}ms)
                  </Badge>
                </>
              ) : (
                <>
                  <XCircle className="h-4 w-4 text-destructive" />
                  <Badge variant="destructive">
                    {result.error || "Connection failed"}
                  </Badge>
                </>
              )}
            </div>
          )}
        </div>
      </CardContent>
    </Card>
  );
}

export default function ProvidersPage() {
  const isAdmin = useHasRole("tenant_admin");

  if (!isAdmin) {
    return (
      <div className="flex flex-col items-center justify-center gap-4 py-20">
        <ShieldAlert className="h-12 w-12 text-muted-foreground/40" />
        <h2 className="text-xl font-semibold">Access Denied</h2>
        <p className="text-muted-foreground">
          Provider configuration requires admin access.
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">
          Provider Configuration
        </h1>
        <p className="text-muted-foreground">
          Test your AI provider connections. API keys are not stored — they are
          only used for the test request.
        </p>
      </div>

      <div className="space-y-4">
        {PROVIDER_TYPES.map((pt) => (
          <ProviderTestCard key={pt.type} {...pt} />
        ))}
      </div>
    </div>
  );
}
