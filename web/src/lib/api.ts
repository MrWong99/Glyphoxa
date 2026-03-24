import type {
  Campaign,
  NPC,
  Session,
  TranscriptEntry,
  DashboardStats,
  ActivityItem,
  User,
} from "./types";

const API_BASE = process.env.NEXT_PUBLIC_API_URL ?? "/api/v1";

class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await fetch(`${API_BASE}${path}`, {
    ...options,
    credentials: "include",
    headers: {
      "Content-Type": "application/json",
      ...options?.headers,
    },
  });

  if (res.status === 401) {
    if (typeof window !== "undefined") {
      window.location.href = "/login";
    }
    throw new ApiError(401, "Unauthorized");
  }

  if (!res.ok) {
    const body = await res.text();
    throw new ApiError(res.status, body || res.statusText);
  }

  if (res.status === 204) return undefined as T;
  return res.json();
}

// Auth
export const auth = {
  me: () => request<User>("/auth/me"),
  logout: () => request<void>("/auth/logout", { method: "POST" }),
  discordUrl: () => `${API_BASE}/auth/discord`,
};

// Dashboard
export const dashboard = {
  stats: () => request<DashboardStats>("/dashboard/stats"),
  activity: () => request<ActivityItem[]>("/dashboard/activity"),
  activeSessions: () => request<Session[]>("/dashboard/active-sessions"),
};

// Campaigns
export const campaigns = {
  list: () => request<Campaign[]>("/campaigns"),
  get: (id: string) => request<Campaign>(`/campaigns/${id}`),
  create: (data: Partial<Campaign>) =>
    request<Campaign>("/campaigns", {
      method: "POST",
      body: JSON.stringify(data),
    }),
  update: (id: string, data: Partial<Campaign>) =>
    request<Campaign>(`/campaigns/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    }),
  delete: (id: string) =>
    request<void>(`/campaigns/${id}`, { method: "DELETE" }),
};

// NPCs
export const npcs = {
  list: (campaignId: string) =>
    request<NPC[]>(`/campaigns/${campaignId}/npcs`),
  get: (campaignId: string, npcId: string) =>
    request<NPC>(`/campaigns/${campaignId}/npcs/${npcId}`),
  create: (campaignId: string, data: Partial<NPC>) =>
    request<NPC>(`/campaigns/${campaignId}/npcs`, {
      method: "POST",
      body: JSON.stringify(data),
    }),
  update: (campaignId: string, npcId: string, data: Partial<NPC>) =>
    request<NPC>(`/campaigns/${campaignId}/npcs/${npcId}`, {
      method: "PUT",
      body: JSON.stringify(data),
    }),
  delete: (campaignId: string, npcId: string) =>
    request<void>(`/campaigns/${campaignId}/npcs/${npcId}`, {
      method: "DELETE",
    }),
};

// Sessions
export const sessions = {
  list: () => request<Session[]>("/sessions"),
  listByCampaign: (campaignId: string) =>
    request<Session[]>(`/campaigns/${campaignId}/sessions`),
  get: (id: string) => request<Session>(`/sessions/${id}`),
  stop: (id: string) =>
    request<void>(`/sessions/${id}`, { method: "DELETE" }),
  transcript: (id: string) =>
    request<TranscriptEntry[]>(`/sessions/${id}/transcript`),
};

export const api = { auth, dashboard, campaigns, npcs, sessions };
