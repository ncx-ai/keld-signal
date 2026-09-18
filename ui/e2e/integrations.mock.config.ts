import { defineConfig, devices } from "@playwright/test";

/**
 * The DAEMON-FREE runner for the integrations state suite.
 *
 * `playwright.config.ts` collects `integrations-states.spec.ts` too and is the
 * config the whole suite runs under. This one exists because that config's
 * `globalSetup` brings a real daemon up through `scripts/e2e-up.sh`, which
 * needs the ~5 GB sidecar venv and a generated corpus — and the state suite
 * needs neither: it serves the page's own three files itself and answers
 * `/v1/integrations` from a fixture, because what it tests is what the pane
 * does with a response.
 *
 *   npx playwright test --config=integrations.mock.config.ts
 *
 * Use it while iterating on the pane, and on a machine that cannot bring the
 * daemon up. It is not a substitute for the journey specs in
 * `integrations.spec.ts`, which need real wiring facts and WS-C1's route.
 */
export default defineConfig({
  testDir: ".",
  // The daemon-free specs. `durability.spec.ts` joins the state suite here
  // rather than under `playwright.config.ts` for the same reason: it serves the
  // page itself and answers /v1/ledger from an inline fixture, so it needs
  // neither the sidecar venv nor a generated corpus.
  testMatch: /(integrations-states|durability)\.spec\.ts$/,
  workers: 1,
  fullyParallel: false,
  retries: 0,
  timeout: 45_000,
  expect: { timeout: 10_000 },
  reporter: [["list"]],
  outputDir: "test-results",
  use: { trace: "retain-on-failure", screenshot: "only-on-failure" },
  // Both engines, because app.css is written to render in the system web view
  // a Tauri shell will host as well as in Chrome — the header of app.css says
  // so, and a Chromium-only run would not check it.
  projects: [
    { name: "chromium", use: { ...devices["Desktop Chrome"] } },
    { name: "webkit", use: { ...devices["Desktop Safari"] } },
  ],
});
