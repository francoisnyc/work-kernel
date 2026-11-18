import { defineConfig, devices } from "@playwright/test";

// The browser smoke test. It runs against the compose stack
// (docker compose up -d --build --wait), which serves the UI on 8090; it
// starts nothing itself.
export default defineConfig({
  testDir: "e2e",
  // One stack, shared data (ana's one open leave balance): one at a time.
  workers: 1,
  fullyParallel: false,
  retries: 0,
  timeout: 60_000,
  reporter: [["list"]],
  use: {
    baseURL: process.env.PLI_WEB_URL ?? "http://localhost:8090",
    trace: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
