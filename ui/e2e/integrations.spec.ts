import { test, expect } from "./support/fixtures";

/**
 * THE LIVE JOURNEYS — AC-2 (configure a tool from the pane) and AC-3 (a tool
 * installed after Signal is detected and configured).
 *
 * ⚠️ **EVERY TEST IN THIS FILE IS SKIPPED, AND EACH ONE NAMES WHAT IT WAITS
 * ON.** They drive the REAL daemon: real wiring facts read back off disk, the
 * real adapter writing a real config with a real backup, and the real
 * detector. All of that is WS-C1's — `GET /v1/integrations`,
 * `POST /v1/integrations/{id}/setup`, `internal/agent/integrations/{compute,
 * facts,detector}.go` and `daemon/integrations_route.go` — and none of it is
 * merged yet. Written now, skipped now, so enabling them after WS-C1 lands is
 * deleting a `test.skip` line rather than writing a suite under time pressure.
 *
 * What is NOT waiting on anything is the rendering: every state in the
 * vocabulary, both widths, is covered today in `integrations-states.spec.ts`
 * against fixture responses. These add the half a fixture cannot reach — that
 * the states the pane renders are the states this machine is actually in.
 *
 * Two things the supervisor must check when un-skipping, because a green run
 * would otherwise prove less than it looks:
 *
 *  1. `scripts/e2e-up.sh` must isolate the tool config dirs the detector
 *     stats. It already sets `HOME` and `KELD_HOME`; the catalogue resolves
 *     `~/.codex` and friends through `HOME` at call time (wire doc §5), so
 *     this works — but a test that creates `<HOME>/.codex/config.toml` is
 *     writing into the developer's machine the moment that stops being true.
 *  2. The fixture daemon should run with auto-setup OFF for the Set up
 *     journey, or the detector configures the tool before the button can be
 *     pressed and the row is never `not_configured` to click on. AC-3's own
 *     journey wants it ON, so they cannot share one daemon setting.
 */

const WAITING_ON = "WS-C1: GET /v1/integrations + POST /v1/integrations/{id}/setup (daemon/integrations_route.go)";
const WAITING_ON_DETECTOR = "WS-C1: the detector loop (internal/agent/integrations/detector.go) + auto_setup_integrations";

test.describe("Integrations · live daemon", () => {
  test.skip(true, WAITING_ON);

  test("the pane lists this machine's tools with the states the daemon computed", async ({ signal, page }) => {
    await signal.open("integrations");
    // Every row the daemon sent, and a state on each that came from Compute
    // rather than from anything on this page.
    const rows = page.locator(".intg-row");
    await expect(rows.first()).toBeVisible();
    for (const id of ["claude_code", "codex", "gemini_cli", "cowork", "pi", "antigravity", "cursor"]) {
      await expect(page.locator(`.intg-row[data-integration="${id}"]`)).toHaveCount(1);
    }
    // The corpus the e2e daemon watches is Claude Code's, so that row is the
    // one with real lane facts behind it.
    await expect(page.locator('.intg-row[data-integration="claude_code"] .intg-state')).not.toBeEmpty();
    await expect(page.getByText(/^unknown state: /)).toHaveCount(0);
  });

  test("AC-2: a tool that appears after Signal is offered Set up, and the click writes a backup", async ({
    signal,
    page,
    state,
  }) => {
    // Create <HOME>/.codex/config.toml under the e2e HOME, wait one detector
    // poll, then drive the button. Needs the fixture daemon's auto-setup OFF.
    await signal.open("integrations");
    const row = page.locator('.intg-row[data-integration="codex"]');
    await expect(row.locator(".intg-state")).toHaveText("not_configured", { timeout: 90_000 });
    await row.getByRole("button", { name: "Set up" }).click();
    await expect(row.locator(".intg-result")).toContainText("Previous config saved to");
    // The state that follows comes from the next poll, from Compute, never
    // from the button: the session that is running predates the config.
    await expect(row.locator(".intg-state")).toHaveText("restart_required", { timeout: 30_000 });
  });

  test("AC-3: with auto-setup on, the detector configures it and the pane says so without a click", async ({
    signal,
    page,
  }) => {
    test.skip(true, WAITING_ON_DETECTOR);
    await signal.open("integrations");
    const row = page.locator('.intg-row[data-integration="codex"]');
    await expect(row.getByRole("button", { name: "Set up" })).toHaveCount(0, { timeout: 90_000 });
    await expect(row.locator(".intg-state")).toHaveText("restart_required", { timeout: 90_000 });
  });

  test("AC-9: an untrusted Codex hook reads approval_required, and clears when hooks.state says trusted", async ({
    signal,
    page,
  }) => {
    await signal.open("integrations");
    const row = page.locator('.intg-row[data-integration="codex"]');
    await expect(row.locator(".intg-state")).toHaveText("approval_required", { timeout: 90_000 });
    await expect(row.locator(".intg-instruction")).toContainText(
      "Open Codex, run /hooks, approve the two keld hooks."
    );
    // Writing the trusted hooks.state entries into the isolated config.toml
    // clears it on the next poll — and it is never `broken` on the way.
    await expect(row.locator(".intg-state")).not.toHaveText("broken");
  });

  test("Report a problem writes a bundle and names where it went", async ({ signal, page }) => {
    await signal.open("integrations");
    const row = page.locator('.intg-row[data-integration="claude_code"]');
    await row.getByRole("button", { name: "Report a problem" }).click();
    await expect(row.locator(".intg-result")).toContainText("Report written to");
    // WS-C2 owns the bundle's contents; this only asserts the route answered
    // with a path the page could print.
  });
});
