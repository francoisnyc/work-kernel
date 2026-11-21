import react from "@vitejs/plugin-react";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import type { Plugin } from "vite";
import { defineConfig } from "vitest/config";

// web/'s generated client and rank port, imported read-only. POC_SHARED is
// where they are when the build does not run inside the repository (the
// Docker build copies them next to the app).
const shared = process.env.POC_SHARED ?? fileURLToPath(new URL("../../web/src", import.meta.url));

// DEV ONLY, for `npm run dev` against a running stack: what the compose
// stack's poc container does with nginx. /api goes to the item service,
// and the dev-token file is served for the sign-in screen.
function devStack(): Plugin {
  return {
    name: "pli-poc-dev-stack",
    configureServer(server) {
      server.middlewares.use("/dev-tokens.yaml", (_req, res) => {
        res.setHeader("Content-Type", "text/plain; charset=utf-8");
        res.end(readFileSync(new URL("../../dev-tokens.example.yaml", import.meta.url)));
      });
    },
  };
}

// The unit tests (vitest): the logic (model/, theme/) in Node, where a
// test may read the repository's files; the components and hooks (.tsx)
// in jsdom. The journeys (e2e/, Playwright) have their own runner.
export default defineConfig({
  plugins: [react(), devStack()],
  resolve: {
    alias: { "@gen": `${shared}/gen`, "@rank": `${shared}/rank` },
    // The shared files' own imports resolve from this app, not from web/.
    dedupe: ["@bufbuild/protobuf"],
  },
  server: {
    host: "127.0.0.1",
    port: 5174,
    proxy: {
      "/api": { target: "http://127.0.0.1:8080", rewrite: (path) => path.replace(/^\/api/, "") },
    },
  },
  test: {
    projects: [
      { extends: true, test: { name: "logic", environment: "node", include: ["src/**/*.test.ts"] } },
      { extends: true, test: { name: "dom", environment: "jsdom", include: ["src/**/*.test.tsx"], setupFiles: ["src/test/setup.ts"] } },
    ],
  },
});
