import { readCookie } from "./transport";

// Campaign Bundle export/import over the plain-net/http endpoints that sit beside
// the Connect handler (ADR-0015/0053): GET /api/v1/campaigns/{id}/export streams a
// gzip bundle; POST /api/v1/campaigns/import takes a multipart upload. Both are
// same-origin, so the session cookie rides along by default — we must NOT set
// credentials:'omit', or the operator gate (ADR-0041) rejects the request. The
// import is a state change, so it carries the CSRF double-submit header (ADR-0016).

// ImportSummary is the JSON counts the importer returns (ADR-0053 d7: import does
// NOT auto-activate, so the summary drives an explicit "switch to it?" prompt).
export type ImportSummary = {
  campaignId: string;
  name: string;
  agents: number;
  nodes: number;
  edges: number;
  characters: number;
  sessions: number;
  lines: number;
  chunks: number;
};

// The filename used when the server sends no Content-Disposition (it always does,
// but a proxy could strip it — a sensible name beats a bare "download").
const FALLBACK_FILENAME = "campaign.glyphoxa.json.gz";

// downloadBlob saves a blob to disk by clicking a transient <a download>. The
// anchor is appended (some browsers ignore a click on a detached node), clicked,
// then removed and its object URL revoked so nothing leaks. Defensively no-op
// outside a real browser (jsdom, where URL.createObjectURL is unimplemented) so
// callers can fire-and-forget — mirrors audio.ts's jsdom guard.
export function downloadBlob(blob: Blob, filename: string): void {
  if (typeof document === "undefined" || typeof URL?.createObjectURL !== "function") return;
  const url = URL.createObjectURL(blob);
  try {
    const a = document.createElement("a");
    a.href = url;
    a.download = filename;
    a.style.display = "none";
    document.body.appendChild(a);
    a.click();
    a.remove();
  } finally {
    URL.revokeObjectURL(url);
  }
}

// filenameFromDisposition pulls the filename out of a Content-Disposition header,
// falling back to a stable default when it's absent or unparseable.
function filenameFromDisposition(header: string | null): string {
  if (!header) return FALLBACK_FILENAME;
  const match = header.match(/filename\*?=(?:UTF-8'')?"?([^";]+)"?/i);
  return match ? decodeURIComponent(match[1]) : FALLBACK_FILENAME;
}

// fetchCampaignExport downloads a campaign's bundle. Returns the blob plus the
// server-chosen filename (from Content-Disposition). A non-OK response throws the
// server's plain-text error so the caller can surface it in a toast.
export async function fetchCampaignExport(
  campaignId: string,
): Promise<{ blob: Blob; filename: string }> {
  const res = await fetch(`/api/v1/campaigns/${campaignId}/export`);
  if (!res.ok) {
    const text = (await res.text()).trim();
    throw new Error(text || `Export failed (${res.status})`);
  }
  const blob = await res.blob();
  return { blob, filename: filenameFromDisposition(res.headers.get("Content-Disposition")) };
}

// importCampaignBundle uploads a bundle file to the importer. The file rides a
// multipart field named "bundle"; the CSRF token mirrors the glyphoxa_csrf cookie
// into X-CSRF-Token (ADR-0016 double-submit). On success it returns the counts;
// on any error (400 bad bundle, 413 too large, 500) it throws the server's
// {"error": …} message so the caller shows exactly why it failed.
export async function importCampaignBundle(file: File): Promise<ImportSummary> {
  const form = new FormData();
  form.append("bundle", file);
  const token = readCookie("glyphoxa_csrf");
  const res = await fetch("/api/v1/campaigns/import", {
    method: "POST",
    body: form,
    headers: token ? { "X-CSRF-Token": token } : {},
  });
  if (!res.ok) {
    let message = `Import failed (${res.status})`;
    try {
      const body = (await res.json()) as { error?: string };
      if (body?.error) message = body.error;
    } catch {
      // Non-JSON body (e.g. a proxy's 413 page) — keep the status-based message.
    }
    throw new Error(message);
  }
  const body = (await res.json()) as {
    campaign_id: string;
    name: string;
    agents: number;
    nodes: number;
    edges: number;
    characters: number;
    sessions: number;
    lines: number;
    chunks: number;
  };
  return {
    campaignId: body.campaign_id,
    name: body.name,
    agents: body.agents,
    nodes: body.nodes,
    edges: body.edges,
    characters: body.characters,
    sessions: body.sessions,
    lines: body.lines,
    chunks: body.chunks,
  };
}
