// Glyphoxa Design System — mock data shapes (ui_kits/glyphoxa-web)
// These are the mock shapes the prototype renders. The SPA replaces the campaign
// header data with the live GetActiveCampaign RPC (ADR-0039); the provider keys,
// voice list, and health badge stay as styled "coming soon" placeholders until
// later stages wire their RPCs.

export const mockCampaign = {
  id: "11111111-1111-1111-1111-111111111111",
  name: "The Sunless Citadel",
  system: "D&D 5e",
  language: "en",
};

export const mockProviderKeys = [
  { component: "llm", provider: "Groq", last4: "env", healthy: false },
  { component: "stt", provider: "ElevenLabs", last4: "env", healthy: false },
  { component: "tts", provider: "ElevenLabs", last4: "env", healthy: false },
];

export const mockVoices = [
  { id: "rachel", name: "Rachel" },
  { id: "antoni", name: "Antoni" },
  { id: "bella", name: "Bella" },
];

export const mockConnection = {
  bot: "Glyphoxa#4823",
  connected: false,
};

export const navItems = [
  { id: "configuration", label: "Configuration", to: "configuration" },
  { id: "campaign", label: "Campaign", to: "campaign" },
  { id: "session", label: "Session", to: "session" },
];
