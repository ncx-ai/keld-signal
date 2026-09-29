import { defineConfig, devices } from "@playwright/test";

/**
 * The web sign-in against a REAL, locally running Atlas (not the mock).
 *
 *   KELD_E2E_ATLAS_WEB=http://localhost:3000 \
 *     npx playwright test --config=local-atlas/local-atlas.config.ts
 *
 * Opt-in: with KELD_E2E_ATLAS_WEB unset, global setup builds nothing and every
 * test skips with a message saying so, so CI (which has no Atlas) is untouched.
 * The main config and signin.config.ts both ignore this directory.
 *
 * Every test starts its own throwaway `keld-agent run` on a fresh KELD_HOME
 * (support/signin-harness.ts, the same harness the mock suite uses) with
 * KELD_API_URL / KELD_ATLAS_WEB_URL pointed at that Atlas. Signing in against a
 * real Atlas mints real grants and CLI tokens there, through its own routes;
 * nothing else is written to it.
 *
 * One worker: one daemon at a time on telemetry port 14421.
 */
export default defineConfig({
  testDir: ".",
  testMatch: /\.spec\.ts$/,
  globalSetup: require.resolve("./local-atlas-setup"),
  workers: 1,
  fullyParallel: false,
  retries: 0,
  forbidOnly: !!process.env.CI,
  timeout: 90_000,
  expect: { timeout: 10_000 },
  reporter: [["list"], ["html", { open: "never", outputFolder: "../playwright-report-local-atlas" }]],
  outputDir: "../test-results/local-atlas",
  use: { trace: "retain-on-failure", screenshot: "only-on-failure" },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"], viewport: { width: 1280, height: 900 } } }],
});
