import { defineConfig, devices } from "@playwright/test";

/**
 * The web sign-in suite: `signin.spec.ts` and `firstrun.spec.ts`.
 *
 *   npx playwright test --config=signin.config.ts
 *
 * A config of its own because the daemon it needs is the opposite of the one
 * `playwright.config.ts` brings up. That one is paired, runs KELD_ATLAS=0 and
 * needs the sidecar venv and a generated corpus; these need an UNPAIRED daemon
 * with Send to Atlas never set, a mock Atlas to sign in against, and nothing
 * else — so they run on a stock CI runner (ci.yml, job `ui-e2e-signin`) where
 * the main suite cannot. Each test starts its own daemon and mock
 * (support/signin-harness.ts), because signing in changes the machine.
 *
 * Chromium only: CI installs one browser, and what is under test is the
 * daemon's round trip and the page's reaction to it, not a rendering matrix.
 * `--project=webkit` is there for a local pass.
 */
export default defineConfig({
  testDir: ".",
  testMatch: /(signin|firstrun)\.spec\.ts$/,
  globalSetup: require.resolve("./signin-setup"),
  workers: 1,
  fullyParallel: false,
  retries: 0,
  forbidOnly: !!process.env.CI,
  timeout: 90_000,
  expect: { timeout: 10_000 },
  reporter: [["list"], ["html", { open: "never", outputFolder: "playwright-report-signin" }]],
  outputDir: "test-results",
  use: { trace: "retain-on-failure", screenshot: "only-on-failure" },
  projects: [
    { name: "chromium", use: { ...devices["Desktop Chrome"], viewport: { width: 1280, height: 900 } } },
    { name: "webkit", use: { ...devices["Desktop Safari"], viewport: { width: 1280, height: 900 } } },
  ],
});
