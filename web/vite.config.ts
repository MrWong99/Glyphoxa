/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import { fileURLToPath, URL } from "node:url";

// The built bundle lands in internal/spa/dist so Go's `//go:embed all:dist`
// picks it up (ADR-0013/0039). emptyOutDir is FALSE: the committed placeholder
// index.html that keeps `go build` working on a fresh checkout must NOT be
// deleted by a dev's local `vite build` before it writes the real one.
export default defineConfig({
  plugins: [react()],
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
