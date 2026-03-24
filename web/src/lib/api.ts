import type {
  Campaign,
  NPC,
  Session,
  TranscriptEntry,
  DashboardStats,
  ActivityItem,
  User,
  Invite,
  UserRole,
  UserPreferences,
  PaginatedResponse,
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

const TOKEN_KEY = "glyphoxa_token";

export function getStoredToken(): string | null {
  if (typeof window === "undefined") return null;
  return localStorage.getItem(TOKEN_KEY);
}

export function setStoredToken(token: string): void {
  if (typeof window !== "undefined") {
    localStorage.setItem(TOKEN_KEY, token);
  }
}

export function clearStoredToken(): void {
  if (typeof window !== "undefined") {
    localStorage.removeItem(TOKEN_KEY);
  }
}

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const token = getStoredToken();
  const authHeaders: Record<string, string> = {};
  if (token) {
    authHeaders["Authorization"] = `Bearer ${token}`;
  }

  const res = await fetch(`${API_BASE}${path}`, {
    ...options,
    credentials: "include",
    headers: {
      "Content-Type": "application/json",
      ...authHeaders,
      ...options?.headers,
    },
  });

  if (res.status === 401) {
    clearStoredToken();
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

interface AuthResponse {
  data: {
    access_token: string;
    token_type: string;
    expires_in: number;
    user: User;
  };
}

// Auth
export const auth = {
  me: () => request<User>("/auth/me"),
  logout: () => {
    clearStoredToken();
    return request<void>("/auth/logout", { method: "POST" }).catch(() => {
      // Logout endpoint may not exist; clearing token is sufficient.
    });
  },
  discordUrl: () => `${API_BASE}/auth/discord`,
  loginApiKey: async (apiKey: string): Promise<AuthResponse> => {
    const res = await request<AuthResponse>("/auth/apikey", {
      method: "POST",
      body: JSON.stringify({ api_key: apiKey }),
    });
    setStoredToken(res.data.access_token);
    return res;
  },
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

// Users
export const users = {
  list: (params?: { role?: UserRole; limit?: number; offset?: number }) => {
    const q = new URLSearchParams();
    if (params?.role) q.set("role", params.role);
    if (params?.limit) q.set("limit", String(params.limit));
    if (params?.offset) q.set("offset", String(params.offset));
    const qs = q.toString();
    return request<PaginatedResponse<User>>(`/users${qs ? `?${qs}` : ""}`);
  },
  get: (id: string) => request<User>(`/users/${id}`),
  update: (id: string, data: { display_name?: string; role?: UserRole }) =>
    request<User>(`/users/${id}`, {
      method: "PUT",
      body: JSON.stringify(data),
    }),
  delete: (id: string) =>
    request<void>(`/users/${id}`, { method: "DELETE" }),
  invite: (role: UserRole = "viewer") =>
    request<Invite>("/users/invite", {
      method: "POST",
      body: JSON.stringify({ role }),
    }),
  updateMe: (data: { display_name: string }) =>
    request<User>("/auth/me", {
      method: "PUT",
      body: JSON.stringify(data),
    }),
  updatePreferences: (prefs: Partial<UserPreferences>) =>
    request<User>("/auth/me/preferences", {
      method: "PATCH",
      body: JSON.stringify(prefs),
    }),
};

export const api = { auth, dashboard, campaigns, npcs, sessions, users };
