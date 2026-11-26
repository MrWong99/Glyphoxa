// The share tags that need an ABSOLUTE url — og:url and og:image (and its
// twitter twin). A link preview is fetched by Discord's, Slack's or a search
// engine's crawler, which reads the HTML as served and resolves nothing
// relative; and the SPA is one bundle shipped to every deployment, so the
// document cannot know its own public address.
//
// So these tags are emitted only when the BUILD is told the origin
// (VITE_PUBLIC_ORIGIN, read by the plugin in vite.config.ts). A build without
// it — a stock self-host image, a dev build — carries the text tags from
// index.html and no image, which every crawler renders as a plain text card.
// A wrong absolute URL would be worse than none: a broken image in every
// preview.
//
// Kept free of Vite imports so it is unit-tested like any other module.

/** The 1200×630 crop of the real live-session capture, in web/public/. */
export const SHARE_IMAGE_PATH = "/og-image.jpg";
export const SHARE_IMAGE_WIDTH = 1200;
export const SHARE_IMAGE_HEIGHT = 630;

export type MetaTag = { tag: "meta"; attrs: Record<string, string>; injectTo: "head" };

/**
 * Normalizes what a deployment put in VITE_PUBLIC_ORIGIN to a bare origin
 * ("https://glyphoxa.com"): surrounding space and a trailing slash or path are
 * dropped. Empty or unset answers null. Anything that is not an http(s) URL
 * throws — a typo there should fail the build, not ship a broken preview.
 */
export function publicOrigin(raw: string | undefined): string | null {
  const value = (raw ?? "").trim();
  if (value === "") return null;
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    throw new Error(`VITE_PUBLIC_ORIGIN must be an absolute URL like https://example.com, got "${value}"`);
  }
  if (url.protocol !== "https:" && url.protocol !== "http:") {
    throw new Error(`VITE_PUBLIC_ORIGIN must be an http(s) URL, got "${value}"`);
  }
  return url.origin;
}

/** The absolute-url share tags for an origin; none when the origin is unknown. */
export function shareTags(raw: string | undefined): MetaTag[] {
  const origin = publicOrigin(raw);
  if (!origin) return [];
  const image = origin + SHARE_IMAGE_PATH;
  const meta = (attrs: Record<string, string>): MetaTag => ({ tag: "meta", attrs, injectTo: "head" });
  return [
    meta({ property: "og:url", content: `${origin}/` }),
    meta({ property: "og:image", content: image }),
    meta({ property: "og:image:width", content: String(SHARE_IMAGE_WIDTH) }),
    meta({ property: "og:image:height", content: String(SHARE_IMAGE_HEIGHT) }),
    meta({ name: "twitter:image", content: image }),
  ];
}

/**
 * The card type follows the image: a large-image card with no image renders
 * as an empty frame on some platforms, so index.html ships "summary" and the
 * build upgrades it only when it also emits the image.
 */
export function withLargeCard(html: string): string {
  return html.replace(
    /(<meta\s+name="twitter:card"\s+content=")summary(")/,
    "$1summary_large_image$2",
  );
}
