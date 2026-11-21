import { defineConfig, devices } from "@playwright/test";

// The three journeys (lea on the board, sam working an incident, kim
// opening a request). They run against the compose stack
// (docker compose up -d --build --wait, or after make scenario), which
// serves the apps on 8091; they start nothing themselves and seed their
// own data through the public API.
export default defineConfig({
  testDir: "e2e",
  workers: 1,
  fullyParallel: false,
  retries: 0,
  timeout: 90_000,
  reporter: [["list"]],
  use: {
    baseURL: process.env.PLI_POC_URL ?? "http://localhost:8091",
    trace: "retain-on-failure",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
