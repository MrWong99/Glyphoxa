/// <reference types="vitest/config" />
import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";
import { readdirSync, rmSync } from "node:fs";
import { join, resolve } from "node:path";
import { fileURLToPath, URL } from "node:url";

import { shareTags, withLargeCard } from "./src/lib/shareMeta";

// pruneStaleAssets removes the hashed files of EARLIER builds from
// <outDir>/assets. With emptyOutDir off (see below) every `npm run build` only
// ever added files there, and `//go:embed all:dist` embeds whatever is on
// disk — a dev binary quietly carried every bundle ever built on that machine
// (tens of MB). It touches nothing outside assets/: the tracked placeholder
// index.html and the copied public files are left to Vite.
//
// It runs AFTER this build's files are written, keeping exactly the set Vite
// just emitted, rather than wiping the directory up front: a build that fails
// halfway must not leave an index.html whose assets were already deleted
// (internal/spa.TestEmbeddedIndexReferencesResolve would catch it, but only
// after the working tree is broken).
function pruneStaleAssets(): Plugin {
  let assetsPath = "";
  let assetsDir = "assets";
  return {
    name: "gx-prune-stale-assets",
    apply: "build",
    configResolved(config) {
      assetsDir = config.build.assetsDir;
      assetsPath = join(resolve(config.root, config.build.outDir), assetsDir);
    },
    writeBundle(_options, bundle) {
      const emitted = new Set(Object.keys(bundle));
      let present: string[];
      try {
        present = readdirSync(assetsPath);
      } catch {
        return; // nothing built into assets/ — nothing to prune
      }
      for (const name of present) {
        if (!emitted.has(`${assetsDir}/${name}`)) {
          rmSync(join(assetsPath, name), { recursive: true, force: true });
        }
      }
    },
  };
}

// shareOrigin adds the link-preview tags that need an absolute URL (og:url,
// og:image) to index.html — only when the build is given the deployment's
// public address, e.g. `VITE_PUBLIC_ORIGIN=https://glyphoxa.example npm run
// build`. Without it the document keeps its text-only tags. The rules, and
// why, are in src/lib/shareMeta.ts.
function shareOrigin(): Plugin {
  let origin: string | undefined;
  return {
    name: "gx-share-origin",
    configResolved(config) {
      origin = config.env.VITE_PUBLIC_ORIGIN as string | undefined;
    },
    transformIndexHtml(html) {
      const tags = shareTags(origin);
      return tags.length === 0 ? html : { html: withLargeCard(html), tags };
    },
  };
}

// The built bundle lands in internal/spa/dist so Go's `//go:embed all:dist`
// picks it up (ADR-0013/0039). emptyOutDir is FALSE: the committed placeholder
// index.html that keeps `go build` working on a fresh checkout must NOT be
// deleted by a dev's local `vite build` before it writes the real one. The
// stale hashed assets that policy would otherwise pile up are pruned by
// pruneStaleAssets instead.
export default defineConfig({
  plugins: [react(), pruneStaleAssets(), shareOrigin()],
  resolve: {
    alias: {
      "@gen": fileURLToPath(new URL("../gen/ts", import.meta.url)),
      "@": fileURLToPath(new URL("./src", import.meta.url)),
      // The generated *_pb.ts lives in ../gen/ts (outside web/), so Rollup's node
      // resolution from it can't reach web/node_modules. Pin the @bufbuild/protobuf
      // subpath exports the generated client imports to this project's copy. The
      // base package keeps default resolution (its own internal imports are
      // relative, so no alias is needed/wanted there).
      "@bufbuild/protobuf/codegenv2": fileURLToPath(
        new URL("./node_modules/@bufbuild/protobuf/dist/esm/codegenv2/index.js", import.meta.url),
      ),
      "@bufbuild/protobuf/wkt": fileURLToPath(
        new URL("./node_modules/@bufbuild/protobuf/dist/esm/wkt/index.js", import.meta.url),
      ),
    },
  },
  build: {
    outDir: "../internal/spa/dist",
    emptyOutDir: false,
  },
  server: {
    // Dev: the SPA dials Connect at /api; proxy it to the Go web tier.
    proxy: {
      "/api": {
        target: "http://localhost:8080",
        changeOrigin: true,
      },
    },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./vitest.setup.ts"],
    css: false,
  },
});
