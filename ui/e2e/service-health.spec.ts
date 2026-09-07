import { test, expect, type Page } from "./support/fixtures";

/**
 * The analysis service's own health, as a person meets it.
 *
 * ⚠️ **WHAT THIS SPEC DOES AND DOES NOT PROVE.** The daemon this suite brings
 * up has a healthy analysis service, and a broken one cannot be arranged: the
 * failure this feature exists for is a sidecar that has stopped answering, and
 * `scripts/e2e-up.sh` has no way to produce that without leaving the machine's
 * own state wrecked for every spec after it. So the `service` block is
 * SYNTHESIZED here — the real `GET /v1/ledger` is fetched from the real
 * daemon and only that one key is written onto it before the page sees it,
 * and `POST /v1/service/restart` is answered here rather than by the daemon.
 *
 * That makes this a test of THE PAGE against the pinned wire shape: the
 * banner appearing on every pane, the button disabling, the 202 not being
 * allowed to read as success, a refusal being reported. It is NOT a test of
 * the daemon's contract — nothing here would notice if the daemon stopped
 * sending `service` at all, and `service-health.test.js` is where the shape
 * itself is pinned.
 *
 * It is read-only against the daemon: it fetches, never mutates, so it shares
 * the default projects with every other journey rather than needing one of
 * its own the way `devgen` and `map-project` do.
 */

type ServiceBlock = { state: string; reason: string; failures: number };

/** Serve the real ledger with one key written onto it. */
async function withService(page: Page, service: ServiceBlock | null): Promise<void> {
  await page.route("**/v1/ledger", async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    if (service === null) delete body.service;
    else body.service = service;
    await route.fulfill({ response, json: body });
  });
}

/** Answer the restart route ourselves, with whatever status the case is about. */
async function restartAnswers(page: Page, status: number): Promise<{ calls: () => number }> {
  let calls = 0;
  await page.route("**/v1/service/restart", async (route) => {
    calls++;
    await route.fulfill({ status, contentType: "application/json", body: JSON.stringify({ accepted: status < 400 }) });
  });
  return { calls: () => calls };
}

const STUCK: ServiceBlock = {
  state: "stuck",
  reason: "Signal restarted the analysis service twice and it still isn't answering. 290 blocks are waiting.",
  failures: 2,
};

const banner = (page: Page) => page.locator("#serviceBanner");
const restartBtn = (page: Page) => page.locator("#serviceRestartBtn");

test.describe("Analysis service health", () => {
  test("a healthy service shows no alarm and no button anywhere on the page", async ({ signal, page }) => {
    await withService(page, { state: "ok", reason: "", failures: 0 });
    await signal.open("today");
    await expect(banner(page)).toBeHidden();
    await expect(restartBtn(page)).toBeHidden();
    await expect(page.locator("#navHealthText")).toHaveText("all good");
  });

  test("⚠️ a daemon too old to send `service` at all renders nothing — silence, never a reassurance", async ({ signal, page }) => {
    await withService(page, null);
    await signal.open("today");
    await expect(banner(page)).toBeHidden();
    // and the sidebar is not turned bad by a fact nobody sent
    await expect(page.locator("#navHealthText")).not.toHaveText(/service/);
  });

  test("a machine with no analysis service installed is not reported as broken", async ({ signal, page }) => {
    await withService(page, { state: "not_applicable", reason: "No analysis service on this machine.", failures: 0 });
    await signal.open("today");
    await expect(banner(page)).toBeHidden();
  });

  test("`stuck` names the problem, shows the reason VERBATIM, and offers Restart on every pane", async ({ signal, page }) => {
    await withService(page, STUCK);
    for (const pane of ["today", "projects", "settings"] as const) {
      await signal.open(pane);
      await expect(banner(page)).toBeVisible();
      // The whole point of `stuck`'s reason is that it says restarting was
      // already tried. It must survive to the screen unaltered.
      await expect(page.locator("#serviceReason")).toHaveText(STUCK.reason);
      await expect(restartBtn(page)).toBeVisible();
      await expect(restartBtn(page)).toBeEnabled();
    }
    // The sidebar agrees with the banner rather than reading "all good" beside it.
    await expect(page.locator("#navHealthText")).toHaveText(/service/);
  });

  test("`degraded` shows its reason and a Restart too, and reads differently from `stuck`", async ({ signal, page }) => {
    const reason = "The analysis service stopped answering 12 minutes ago.";
    await withService(page, { state: "degraded", reason, failures: 0 });
    await signal.open("today");
    await expect(banner(page)).toBeVisible();
    await expect(page.locator("#serviceReason")).toHaveText(reason);
    await expect(restartBtn(page)).toBeEnabled();
    await expect(banner(page)).toHaveClass(/degraded/);
    await expect(banner(page)).not.toHaveClass(/stuck/);
  });

  test("the server saying `restarting` is reflected: the button is disabled without this page pretending", async ({ signal, page }) => {
    await withService(page, { state: "restarting", reason: "Restart in progress.", failures: 1 });
    await signal.open("today");
    await expect(banner(page)).toBeVisible();
    await expect(restartBtn(page)).toBeDisabled();
    await expect(restartBtn(page)).toHaveText(/restarting/i);
  });

  test("⚠️ a 202 disables the button and does NOT flip the page to 'all good' — the alarm stands until the ledger says otherwise", async ({ signal, page }) => {
    const restart = await restartAnswers(page, 202);
    await withService(page, STUCK); // the ledger keeps saying stuck, forever
    await signal.open("today");

    await restartBtn(page).click();
    await expect(restartBtn(page)).toBeDisabled();
    expect(restart.calls()).toBe(1);

    // The banner is still there, still saying the same thing, and nothing on
    // screen claims the service came back.
    await expect(banner(page)).toBeVisible();
    await expect(page.locator("#serviceReason")).toHaveText(STUCK.reason);
    await expect(page.locator("#serviceProgress")).toContainText(/requested/i);
    await expect(page.locator("#serviceProgress")).not.toContainText(/fixed|all good|back up/i);
    await expect(page.locator("#navHealthText")).not.toHaveText("all good");
  });

  test("a REFUSED restart is reported as a failure and the press is handed back", async ({ signal, page }) => {
    await restartAnswers(page, 500);
    await withService(page, STUCK);
    await signal.open("today");

    await restartBtn(page).click();
    await expect(page.locator("#serviceProgress")).toContainText(/couldn't/i);
    await expect(restartBtn(page)).toBeEnabled();
    await expect(restartBtn(page)).toHaveText(/try again/i);
    await expect(banner(page)).toBeVisible();
  });

  test("the alarm clears only when the LEDGER stops reporting one", async ({ signal, page }) => {
    let service: ServiceBlock = STUCK;
    await page.route("**/v1/ledger", async (route) => {
      const response = await route.fetch();
      const body = await response.json();
      body.service = service;
      await route.fulfill({ response, json: body });
    });
    await restartAnswers(page, 202);
    await signal.open("today");
    await expect(banner(page)).toBeVisible();

    await restartBtn(page).click();
    await expect(restartBtn(page)).toBeDisabled();

    // The daemon actually fixed it. The page finds out by polling, not by
    // having assumed it at the moment of the 202.
    service = { state: "ok", reason: "", failures: 0 };
    await expect(banner(page)).toBeHidden({ timeout: 40_000 });
    await expect(page.locator("#navHealthText")).toHaveText("all good");
  });
});
