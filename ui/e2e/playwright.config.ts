import { defineConfig, devices } from "@playwright/test";
import os from "node:os";
import path from "node:path";

// Where scripts/e2e-up.sh puts the isolated HOME, the corpus, the daemon log
// and state.json.
//
// WARNING: OUTSIDE the repository, deliberately, and found the hard way. This
// was `ui/e2e/.e2e-work`, and the suite failed there while passing under the
// system temp dir: the sidecar rewrites any path containing
// `/.claude/worktrees/<name>` back to its main checkout (analysis/paths.py's
// WORKTREE regex, so a file edited in a worktree attributes to the repository
// instead of splitting its own share). A synthetic checkout generated under
// that path resolved its git root to THE REAL REPO, `vcs_of` answered
// "git (reported, unverifiable)", and no block ever got a `repo` dimension --
// the Projects pane grouped everything by bare directory name and the projects
// spec failed on an assertion that was correct.
//
// Nothing is wrong with the sidecar: that rule exists for real worktrees. What
// was wrong was generating a fake repository inside one. Test artifacts do not
// belong in the source tree anyway, and keeping them in the system temp dir
// means a failed run cannot leave a nested `.git` behind for a status check to
// trip over.
export const WORK_DIR =
  process.env.KELD_E2E_WORK || path.join(os.tmpdir(), "keld-signal-e2e");
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
      testIgnore: /(not-running|devgen)\.spec\.ts$/,
    },
    {
      name: "webkit",
      use: { ...devices["Desktop Safari"], viewport },
      testIgnore: /(not-running|devgen)\.spec\.ts$/,
    },
    {
      // The page with NO daemon behind it: a tiny static server serves the
      // page's own assets and forwards every /v1/* call to a dead loopback
      // port. Chromium only — this is one journey, not a browser matrix.
      name: "not-running",
      use: { ...devices["Desktop Chrome"], viewport },
      testMatch: /not-running\.spec\.ts$/,
    },
    {
      // ⚠️ **THE BLOCK GENERATOR RUNS LAST, IN A PROJECT OF ITS OWN, BECAUSE IT
      // MUTATES THE CORPUS EVERY OTHER SPEC READS.** Pressing the button adds a
      // real block to the shared ledger — that is the point of it — and it
      // failed the visual baseline on BOTH browsers the first time this suite
      // ran with it included.
      //
      // Renaming the file to sort last would not have been enough: Playwright
      // runs project by project, so chromium's generated blocks would still be
      // on the page when webkit reached its own screenshot. `dependencies` is
      // the only ordering primitive that binds ACROSS projects.
      //
      // Chromium only: this is one journey through a developer control, not a
      // browser matrix, and a second run would add a second block for no extra
      // coverage.
      name: "devgen",
      use: { ...devices["Desktop Chrome"], viewport },
      testMatch: /devgen\.spec\.ts$/,
      dependencies: ["chromium", "webkit", "not-running"],
    },
  ],
});
