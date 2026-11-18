import react from "@vitejs/plugin-react";
import { readFileSync } from "node:fs";
import type { Plugin } from "vite";
import { defineConfig } from "vitest/config";

// DEV ONLY, for `npm run dev` against a running stack: what the compose
// stack's web container does with nginx. /api goes to the item service,
// and the dev-token file is served for the "act as" picker.
function devStack(): Plugin {
  return {
    name: "pli-dev-stack",
    configureServer(server) {
      server.middlewares.use("/dev-tokens.yaml", (_req, res) => {
        res.setHeader("Content-Type", "text/plain; charset=utf-8");
        res.end(readFileSync(new URL("../dev-tokens.example.yaml", import.meta.url)));
      });
    },
  };
}

// The unit tests (vitest) run in jsdom; the browser test (e2e/, Playwright)
// has its own runner and config.
export default defineConfig({
  plugins: [react(), devStack()],
  server: {
    host: "127.0.0.1",
    proxy: {
      "/api": { target: "http://127.0.0.1:8080", rewrite: (path) => path.replace(/^\/api/, "") },
    },
  },
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.{ts,tsx}"],
    setupFiles: ["src/test/setup.ts"],
  },
});
