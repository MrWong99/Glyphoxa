// Product names of the LLM providers the wire names by id. Product names are
// never translated. An id this build does not know (a newer server) is shown
// as-is rather than hidden: a truthful odd name beats a wrong familiar one.
const LLM_PROVIDER_LABELS: Record<string, string> = {
  groq: "Groq",
  anthropic: "Anthropic",
  gemini: "Gemini",
};

export function llmProviderLabel(provider: string): string {
  return LLM_PROVIDER_LABELS[provider] ?? provider;
}
