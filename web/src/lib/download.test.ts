import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

import {
  BundleRequestError,
  downloadBlob,
  fetchCampaignExport,
  importCampaignBundle,
  isBundleBusy,
  isBundleTooLarge,
} from "./download";

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
    // A plain string body, not a jsdom Blob: jsdom's Blob and Node's fetch live
    // in different realms, and Node 22's undici stringifies a foreign Blob to
    // "[object Blob]" instead of streaming its bytes. The Content-Type header is
    // what gives the returned Blob its type either way.
    const fetchMock = vi.fn().mockResolvedValue(
      new Response("bundle-bytes", {
        status: 200,
        headers: {
          "Content-Type": "application/gzip",
          "Content-Disposition": 'attachment; filename="The Prancing Pony.glyphoxa.json.gz"',
        },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    const out = await fetchCampaignExport("camp-1");

    // A bundle is COMPLETE unless the caller says otherwise: history and map
    // images are both asked for.
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/campaigns/camp-1/export?include_history=true&include_images=true",
    );
    expect(out.filename).toBe("The Prancing Pony.glyphoxa.json.gz");
    // Cross-realm again: which Blob class `res.blob()` returns depends on the
    // Node version (Node 22 hands back Node's, Node 24 jsdom's — which has no
    // text()/arrayBuffer()), so neither `instanceof` nor the byte-reading
    // methods are portable. `type` and `size` are the contract the caller
    // relies on and both realms implement them.
    expect(out.blob.type).toBe("application/gzip");
    expect(out.blob.size).toBe("bundle-bytes".length);
    vi.unstubAllGlobals();
  });

  it("drops a parameter that is switched off instead of sending it as false", async () => {
    const ok = () => new Response("x", { status: 200 });
    const fetchMock = vi.fn().mockImplementation(() => Promise.resolve(ok()));
    vi.stubGlobal("fetch", fetchMock);

    await fetchCampaignExport("camp-1", { includeHistory: false });
    await fetchCampaignExport("camp-1", { includeImages: false });
    await fetchCampaignExport("camp-1", { includeHistory: false, includeImages: false });

    expect(fetchMock.mock.calls.map((c) => c[0])).toEqual([
      "/api/v1/campaigns/camp-1/export?include_images=true",
      "/api/v1/campaigns/camp-1/export?include_history=true",
      // Nothing asked for: no query string at all, not a dangling "?".
      "/api/v1/campaigns/camp-1/export",
    ]);
    vi.unstubAllGlobals();
  });

  it("marks a 503 as 'busy' so the caller can offer a retry instead of an error", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response("another campaign import or export is in progress; retry shortly\n", { status: 503 }),
      ),
    );
    const err = await fetchCampaignExport("camp-1").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(BundleRequestError);
    expect((err as BundleRequestError).status).toBe(503);
    expect((err as Error).message).toMatch(/import or export is in progress/);
    expect(isBundleBusy(err)).toBe(true);
    expect(isBundleTooLarge(err)).toBe(false);
    // A plain Error (a dropped connection) is neither.
    expect(isBundleBusy(new Error("network down"))).toBe(false);
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

  it("falls back to a language-NEUTRAL status on a bodyless failure", async () => {
    // A proxy 502 carries no message of its own. This module runs outside React
    // and holds no translation, and the caller already wraps whatever it throws
    // in a localized template ("Couldn't export campaign: {message}") — so the
    // fallback must be a bare status, never an English sentence that would ride
    // untranslated into a German toast.
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("", { status: 502 })));
    await expect(fetchCampaignExport("camp-1")).rejects.toThrow(/^HTTP 502$/);
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

  it("defaults droppedParticipantRefs to 0 when the field is absent (old server)", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            campaign_id: "camp-9",
            name: "The Prancing Pony",
            agents: 0,
            nodes: 0,
            edges: 0,
            characters: 0,
            sessions: 0,
            lines: 0,
            chunks: 0,
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      ),
    );

    const summary = await importCampaignBundle(bundleFile());

    expect(summary.droppedParticipantRefs).toBe(0);
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
    // client must surface it verbatim, not a generic "HTTP 413".
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response("bundle exceeds maximum upload size\n", {
          status: 413,
          headers: { "Content-Type": "text/plain; charset=utf-8" },
        }),
      ),
    );
    const err = await importCampaignBundle(bundleFile()).catch((e: unknown) => e);
    expect((err as Error).message).toMatch(/bundle exceeds maximum upload size/);
    // …and it is recognisable as "too large", which Duplicate answers by
    // retrying without the map images.
    expect(isBundleTooLarge(err)).toBe(true);
    vi.unstubAllGlobals();
  });

  it("marks the importer's JSON 503 as 'busy'", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ error: "another campaign import or export is in progress; retry shortly" }), {
          status: 503,
          headers: { "Content-Type": "application/json" },
        }),
      ),
    );
    const err = await importCampaignBundle(bundleFile()).catch((e: unknown) => e);
    expect(isBundleBusy(err)).toBe(true);
    expect((err as Error).message).toBe("another campaign import or export is in progress; retry shortly");
    vi.unstubAllGlobals();
  });

  it("uploads a bare Blob as a named file part (the bundle an export just returned)", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ campaign_id: "camp-10", name: "Copy" }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await importCampaignBundle(new Blob(["{}"], { type: "application/gzip" }), "The Prancing Pony.glyphoxa.json.gz");

    const part = (fetchMock.mock.calls[0][1].body as FormData).get("bundle");
    expect(part).toBeInstanceOf(File);
    expect((part as File).name).toBe("The Prancing Pony.glyphoxa.json.gz");
    vi.unstubAllGlobals();
  });

  it("falls back to a language-NEUTRAL status on a bodyless failure", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response("", { status: 504 })));
    await expect(importCampaignBundle(bundleFile())).rejects.toThrow(/^HTTP 504$/);
    vi.unstubAllGlobals();
  });
});
