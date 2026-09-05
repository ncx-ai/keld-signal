import { defineConfig, devices } from "@playwright/test";
import path from "node:path";

// Where scripts/e2e-up.sh puts the isolated HOME, the corpus, the daemon log
// and state.json. Inside this directory (gitignored) unless overridden, so a
// clean checkout needs no other location to exist.
export const WORK_DIR = process.env.KELD_E2E_WORK || path.join(__dirname, ".e2e-work");
export const STATE_FILE = path.join(WORK_DIR, "state.json");

const viewport = { width: 1280, height: 900 };

export default defineConfig({
  testDir: ".",
  testMatch: /.*\.spec\.ts$/,
  globalSetup: require.resolve("./global-setup"),
  globalTeardown: require.resolve("./global-teardown"),
  // One daemon, one worker: the Projects journey EDITS this machine's
  // projects.json (the edits are local by contract) and the Settings journey
  // writes show_breaks, so tests run in a fixed order rather than racing.
  workers: 1,
  fullyParallel: false,
  retries: 0,
  forbidOnly: !!process.env.CI,
  timeout: 45_000,
  expect: {
    timeout: 10_000,
    toHaveScreenshot: { maxDiffPixelRatio: 0.02, animations: "disabled" },
  },
  reporter: [["list"], ["html", { open: "never", outputFolder: "playwright-report" }]],
  outputDir: "test-results",
  use: {
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"], viewport },
      testIgnore: /not-running\.spec\.ts$/,
    },
    {
      name: "webkit",
      use: { ...devices["Desktop Safari"], viewport },
      testIgnore: /not-running\.spec\.ts$/,
    },
    {
      // The page with NO daemon behind it: a tiny static server serves the
      // page's own assets and forwards every /v1/* call to a dead loopback
      // port. Chromium only — this is one journey, not a browser matrix.
      name: "not-running",
      use: { ...devices["Desktop Chrome"], viewport },
      testMatch: /not-running\.spec\.ts$/,
    },
  ],
});
