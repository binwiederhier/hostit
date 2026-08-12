import { defineConfig, devices } from "@playwright/test";

// Web end-to-end tests drive a real browser against a running hostit instance
// (stage by default). Auth uses the breakglass endpoint (admin-token gated), so no
// Google round-trip is needed -- see e2e/auth.setup.js.
//
// Run: HOSTIT_BASE_URL=https://stageapps.heckel.io npm run test:e2e
// The admin token is read from ansible secrets or the HOSTIT_ADMIN_TOKEN env var.
const baseURL = process.env.HOSTIT_BASE_URL || "https://stageapps.heckel.io";

export default defineConfig({
  testDir: "./e2e",
  // These specs create/delete real apps, so keep them serial and give the backend
  // room; one flaky retry absorbs transient network blips.
  fullyParallel: false,
  workers: 1,
  retries: 1,
  timeout: 60_000,
  expect: { timeout: 20_000 },
  reporter: [["list"]],
  use: {
    baseURL,
    trace: "retain-on-failure",
    ignoreHTTPSErrors: true,
  },
  projects: [
    { name: "setup", testMatch: /auth\.setup\.js/ },
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"], storageState: "e2e/.auth/state.json" },
      dependencies: ["setup"],
    },
  ],
});
