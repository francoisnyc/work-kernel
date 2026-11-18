import react from "@vitejs/plugin-react";
import { defineConfig } from "vitest/config";

// The unit tests (vitest) run in jsdom; the browser test (e2e/, Playwright)
// has its own runner and config.
export default defineConfig({
  plugins: [react()],
  test: {
    environment: "jsdom",
    include: ["src/**/*.test.{ts,tsx}"],
    setupFiles: ["src/test/setup.ts"],
  },
});
