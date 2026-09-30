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
 *
 * Watching it run (for a person, not CI):
 *   --ui                     Playwright's UI: press play per test, scrub the timeline after
 *   --headed                 a visible browser window
 *   KELD_E2E_SLOWMO=600      milliseconds between browser actions, so a headed run can be followed
 *   KELD_E2E_RECORD=1        keep a video and a trace of every test, passed or not
 *                            (open with `npx playwright show-report ../playwright-report-local-atlas`)
 */
const slowMo = Number(process.env.KELD_E2E_SLOWMO || 0);
const record = process.env.KELD_E2E_RECORD === "1";
export default defineConfig({
  testDir: ".",
  testMatch: /\.spec\.ts$/,
  globalSetup: require.resolve("./local-atlas-setup"),
  workers: 1,
  fullyParallel: false,
  retries: 0,
  forbidOnly: !!process.env.CI,
  // A slowed-down run takes longer than any real one; don't let the budget cut it off.
  timeout: slowMo > 0 ? 300_000 : 90_000,
  expect: { timeout: 10_000 },
  reporter: [["list"], ["html", { open: "never", outputFolder: "../playwright-report-local-atlas" }]],
  outputDir: "../test-results/local-atlas",
  use: {
    trace: record ? "on" : "retain-on-failure",
    video: record ? "on" : "off",
    screenshot: record ? "on" : "only-on-failure",
    launchOptions: { slowMo },
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"], viewport: { width: 1280, height: 900 } } }],
});
