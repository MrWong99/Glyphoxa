import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

import { downloadBlob, fetchCampaignExport, importCampaignBundle } from "./download";

// jsdom has no real download pipeline: URL.createObjectURL is unimplemented and
// anchor.click() is a no-op. We stub the object-URL lifecycle (mirrors audio.ts)
// and observe the transient <a download> node the helper builds.
const createObjectURL = vi.fn(() => "blob:export");
const revokeObjectURL = vi.fn();

beforeEach(() => {
  createObjectURL.mockClear();
  revokeObjectURL.mockClear();
  (URL as unknown as Record<string, unknown>).createObjectURL = createObjectURL;
  (URL as unknown as Record<string, unknown>).revokeObjectURL = revokeObjectURL;
});

afterEach(() => {
  delete (URL as unknown as Record<string, unknown>).createObjectURL;
  delete (URL as unknown as Record<string, unknown>).revokeObjectURL;
  document.body.innerHTML = "";
  vi.restoreAllMocks();
});

describe("downloadBlob", () => {
  it("creates and clicks a hidden <a download=filename>, then revokes the object URL", () => {
    // Observe the anchor at click time: the helper removes it synchronously after.
    let clicked: { download: string; href: string } | null = null;
    const origCreate = document.createElement.bind(document);
    vi.spyOn(document, "createElement").mockImplementation((tag: string) => {
      const el = origCreate(tag) as HTMLElement;
      if (tag === "a") {
        el.addEventListener("click", () => {
          const a = el as HTMLAnchorElement;
          clicked = { download: a.download, href: a.href };
        });
      }
      return el;
    });

    vi.useFakeTimers();
    downloadBlob(new Blob(["x"]), "camp.glyphoxa.json.gz");

    expect(createObjectURL).toHaveBeenCalledOnce();
    expect(clicked).not.toBeNull();
    expect(clicked!.download).toBe("camp.glyphoxa.json.gz");
    expect(clicked!.href).toContain("blob:export");
    // The transient anchor is gone immediately.
    expect(document.querySelector("a")).toBeNull();

    // The object URL is revoked on a DEFERRED timer (Safari aborts large-blob
    // downloads on an immediate revoke), so it's still live right after the click…
    expect(revokeObjectURL).not.toHaveBeenCalled();
    // …and released once the timer fires — no leak.
    vi.advanceTimersByTime(10_000);
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:export");
    vi.useRealTimers();
  });
});

describe("fetchCampaignExport", () => {
  it("returns the blob and the filename from Content-Disposition on 200", async () => {
    const blob = new Blob(["bundle-bytes"], { type: "application/gzip" });
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(blob, {
        status: 200,
        headers: {
          "Content-Type": "application/gzip",
          "Content-Disposition": 'attachment; filename="The Prancing Pony.glyphoxa.json.gz"',
        },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const out = await fetchCampaignExport("camp-1");

    expect(fetchMock).toHaveBeenCalledWith("/api/v1/campaigns/camp-1/export");
    expect(out.filename).toBe("The Prancing Pony.glyphoxa.json.gz");
    expect(out.blob).toBeInstanceOf(Blob);
    vi.unstubAllGlobals();
  });

  it("falls back to a default filename when Content-Disposition is absent", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(new Blob(["x"]), { status: 200 })));
    const out = await fetchCampaignExport("camp-1");
    expect(out.filename).toBe("campaign.glyphoxa.json.gz");
    vi.unstubAllGlobals();
  });

  it("throws the server's error text on a non-OK response", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response("campaign not found", { status: 404 })),
    );
    await expect(fetchCampaignExport("missing")).rejects.toThrow(/campaign not found/);
    vi.unstubAllGlobals();
  });
});

describe("importCampaignBundle", () => {
  const bundleFile = () =>
    new File(["{}"], "bundle.glyphoxa.json.gz", { type: "application/gzip" });

  afterEach(() => {
    document.cookie = "glyphoxa_csrf=; expires=Thu, 01 Jan 1970 00:00:00 GMT";
  });

  it("POSTs the file as multipart field 'bundle' with the CSRF header and returns the summary", async () => {
    document.cookie = "glyphoxa_csrf=tok-123";
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          campaign_id: "camp-9",
          name: "The Prancing Pony",
          agents: 2,
          nodes: 4,
          edges: 2,
          characters: 1,
          sessions: 0,
          lines: 0,
          chunks: 0,
          dropped_participant_refs: 0,
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    const summary = await importCampaignBundle(bundleFile());

    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe("/api/v1/campaigns/import");
    expect(init.method).toBe("POST");
    expect(init.body).toBeInstanceOf(FormData);
    expect((init.body as FormData).get("bundle")).toBeInstanceOf(File);
    expect((init.headers as Record<string, string>)["X-CSRF-Token"]).toBe("tok-123");
    // Same-origin credentialed by default — never opt out, or the operator gate rejects.
    expect(init.credentials).toBeUndefined();

    expect(summary).toEqual({
      campaignId: "camp-9",
      name: "The Prancing Pony",
      agents: 2,
      nodes: 4,
      edges: 2,
      characters: 1,
      sessions: 0,
      lines: 0,
      chunks: 0,
      droppedParticipantRefs: 0,
    });
    vi.unstubAllGlobals();
  });

  it("maps dropped_participant_refs into droppedParticipantRefs on the summary", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          campaign_id: "camp-9",
          name: "The Prancing Pony",
          agents: 2,
          nodes: 4,
          edges: 2,
          characters: 1,
          sessions: 3,
          lines: 10,
          chunks: 5,
          dropped_participant_refs: 2,
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
    vi.stubGlobal("fetch", fetchMock);

    const summary = await importCampaignBundle(bundleFile());

    expect(summary.droppedParticipantRefs).toBe(2);
    vi.unstubAllGlobals();
  });

  it("throws the server {error} message on a 400 bad-bundle response", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ error: "unsupported bundle format v2 (this build reads v1)" }), {
          status: 400,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );
    await expect(importCampaignBundle(bundleFile())).rejects.toThrow(/unsupported bundle format v2/);
    vi.unstubAllGlobals();
  });

  it("throws the server's PLAIN-TEXT message on a 413 oversized response", async () => {
    // ServeImport's MaxBytesReader 413 is http.Error plain text, NOT JSON — the
    // client must surface it verbatim, not a generic "Import failed (413)".
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response("bundle exceeds maximum upload size\n", {
          status: 413,
          headers: { "Content-Type": "text/plain; charset=utf-8" },
        }),
      ),
    );
    await expect(importCampaignBundle(bundleFile())).rejects.toThrow(
      /bundle exceeds maximum upload size/,
    );
    vi.unstubAllGlobals();
  });
});
