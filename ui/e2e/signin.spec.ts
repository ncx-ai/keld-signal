import fs from "node:fs";
import path from "node:path";
import type { Page } from "@playwright/test";
import { test, expect } from "./support/signin-fixtures";
import type { SigninHarness } from "./support/signin-harness";

// The web sign-in round trip (AC-5 page half, AC-9 Signal half), driven the
// way a person does it: a real daemon, the conformance mock Atlas standing in
// for the authorize page, and a real browser following the link the page
// shows. KELD_AUTH_NO_BROWSER=1, so the daemon opens nothing and answers
// `opened:false` — which is exactly the case where that link is the only way
// through, and the case a Linux service with no display hits for real.

const SIGNED_IN = /^Signed in (as|to Atlas)/;

/** Follow the page's own link in a new tab of the same browser context, the
 *  way a click on it does, and return that tab once the daemon's callback
 *  page has loaded. */
async function finishInBrowser(page: Page): Promise<Page> {
  const link = page.getByRole("link", { name: "Open the Atlas sign-in page" });
  await expect(link).toBeVisible();
  const [tab] = await Promise.all([page.context().waitForEvent("page"), link.click()]);
  await tab.waitForLoadState("load");
  return tab;
}

function assertPaired(h: SigninHarness): void {
  const hook = h.readJSON("hook.json");
  expect(hook, "hook.json written by the callback").toBeTruthy();
  expect(String(hook.ingest_token || "")).not.toBe("");
  expect(h.exists("auth.json"), "auth.json written by the callback").toBe(true);
}

test.describe("Web sign-in", () => {
  test("first open → Sign in with Atlas → the browser comes back → the page says signed in within 5 s", async ({ page, harness, browserName }) => {
    await page.goto(harness.pageURL("today"));
    await expect(page.getByRole("region", { name: "Welcome to Signal" })).toBeVisible();
    await page.getByRole("button", { name: "Sign in with Atlas" }).click();

    // AC-1: the page always shows the link, and with no browser opened it says so.
    await expect(page.getByText("Finish signing in in your browser")).toBeVisible();
    await expect(page.getByText("Your browser didn't open. Open this link to finish:")).toBeVisible();
    const href = await page.getByRole("link", { name: "Open the Atlas sign-in page" }).getAttribute("href");
    const url = new URL(href || "");
    expect(`${url.origin}${url.pathname}`).toBe(`${harness.atlasURL}/cli/signal/authorize`);
    expect(url.searchParams.get("redirect_uri")).toBe(`${harness.daemon!.baseURL}/auth/callback`);
    expect(url.searchParams.get("code_challenge_method")).toBe("S256");
    expect(url.searchParams.get("state")).toMatch(/^[A-Za-z0-9_-]{43,}$/);

    // "Copy link" beside it, for wherever a click on the link opens nothing.
    const copy = page.getByRole("button", { name: "Copy link" });
    await expect(copy).toBeVisible();
    if (browserName === "chromium") {
      await page.context().grantPermissions(["clipboard-read", "clipboard-write"]);
      await copy.click();
      await expect(page.getByRole("button", { name: "Copied" })).toBeVisible();
      expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(href);
    }

    const tab = await finishInBrowser(page);
    await expect(tab.getByText("Signed in. Close this tab.")).toBeVisible();

    // AC-5: within 5 s of the callback, with no reload.
    await expect(page.getByText(SIGNED_IN)).toBeVisible({ timeout: 5_000 });
    await expect(page.getByRole("region", { name: "Welcome to Signal" })).toHaveCount(0);
    assertPaired(harness);
    // And the health strip under it agrees while the confirmation is still up,
    // rather than saying "Atlas not paired" until the page's 30 s refresh.
    await expect(page.locator(".health-strip")).toBeVisible();
    await expect(page.locator(".health-strip")).not.toContainText("Atlas not paired", { timeout: 3_000 });
    await expect(page.getByText(SIGNED_IN)).toBeVisible();

    // A paired machine never sees the choice again, and Settings says who it is.
    await page.goto(harness.pageURL("settings"));
    await expect(page.getByText("Loading…")).toBeHidden();
    await expect(page.getByRole("region", { name: "Welcome to Signal" })).toHaveCount(0);
    await expect(page.locator(".tile", { hasText: "Atlas account" }).getByText(SIGNED_IN)).toBeVisible();
    // The setup-code box is still there (AC-8).
    await expect(page.locator("#codeInput")).toBeVisible();
  });

  test("from local only, Settings' Sign in turns Send to Atlas back on and signs in", async ({ page, harness }) => {
    await page.goto(harness.pageURL("today"));
    await page.getByRole("button", { name: "Use without an account" }).click();
    await expect(page.getByRole("region", { name: "Welcome to Signal" })).toHaveCount(0);
    await expect.poll(() => harness.readJSON("agent-config.json")?.send_to_atlas).toBe(false);

    await page.goto(harness.pageURL("settings"));
    const tile = page.locator(".tile", { hasText: "Atlas account" });
    await expect(tile.getByText("Signing in turns Send to Atlas on.")).toBeVisible();
    await tile.getByRole("button", { name: "Sign in with Atlas" }).click();

    await expect(page.getByText("Finish signing in in your browser")).toBeVisible();
    await expect.poll(() => harness.readJSON("agent-config.json")?.send_to_atlas).toBe(true);
    // The restart choosing local only asked for is still owed, but pressing it
    // now would move the daemon to a new port and strand the browser's return.
    const restartBar = page.locator(".restart-bar");
    await expect(restartBar.getByText("Restart after signing in finishes.")).toBeVisible();
    await expect(restartBar.getByRole("button", { name: "Restart" })).toBeDisabled();
    await finishInBrowser(page);
    await expect(tile.getByText(SIGNED_IN)).toBeVisible({ timeout: 5_000 });
    assertPaired(harness);
    await expect(restartBar.getByRole("button", { name: "Restart" })).toBeEnabled();
  });

  test("KELD_ATLAS=0 pins Send to Atlas off: Settings' Sign in is disabled and says why", async ({ page, harness }) => {
    await harness.stopDaemon();
    await harness.startDaemon({ KELD_ATLAS: "0" });
    await page.goto(harness.pageURL("settings"));
    const tile = page.locator(".tile", { hasText: "Atlas account" });
    await expect(tile.getByRole("button", { name: "Sign in with Atlas" })).toBeDisabled();
    await expect(tile.getByText("Set by KELD_ATLAS on this machine.")).toBeVisible();
    // The env var already chose, so the first-open choice never shows either.
    await expect(page.getByRole("region", { name: "Welcome to Signal" })).toHaveCount(0);
  });

  test("signed in, Settings offers Unpair and asks first; Cancel leaves the pairing alone", async ({ page, harness }) => {
    // Never confirms: the confirm path restarts the INSTALLED service, which in a
    // test would be the developer's own Signal. That path is the Go unit tests'.
    await page.goto(harness.pageURL("today"));
    await page.getByRole("button", { name: "Sign in with Atlas" }).click();
    await finishInBrowser(page);
    await expect(page.getByText(SIGNED_IN).first()).toBeVisible({ timeout: 5_000 });
    await page.goto(harness.pageURL("settings"));
    const tile = page.locator(".tile", { hasText: "Atlas account" });
    await tile.getByRole("button", { name: "Unpair" }).click();
    await expect(tile.getByText(/keeps collecting here and stops sending to Atlas/)).toBeVisible();
    await expect(tile.getByRole("button", { name: "Unpair" })).toHaveCount(1); // the confirm button only
    await tile.getByRole("button", { name: "Cancel" }).click();
    await expect(tile.getByRole("button", { name: "Unpair" })).toBeVisible();
    assertPaired(harness);
  });

  test("Send to Atlas on but never signed in: no bar nags, and Settings signs in", async ({ page, harness }) => {
    // Decision-table row 3: send_to_atlas set to true by hand, not paired.
    await harness.stopDaemon();
    const cfgPath = path.join(harness.home, "agent-config.json");
    fs.writeFileSync(cfgPath, JSON.stringify({ ...JSON.parse(fs.readFileSync(cfgPath, "utf8")), send_to_atlas: true }));
    await harness.startDaemon();

    await page.goto(harness.pageURL("today"));
    await expect(page.getByRole("region", { name: "Welcome to Signal" })).toHaveCount(0);
    const bar = page.locator("#signinBanner");
    await expect(bar).toBeHidden();
    await page.goto(harness.pageURL("settings"));
    await expect(bar).toBeHidden();
    await page.locator(".tile", { hasText: "Atlas account" }).getByRole("button", { name: "Sign in with Atlas" }).click();
    await expect(bar.getByText("Finish signing in in your browser")).toBeVisible();
    await finishInBrowser(page);
    await expect(bar.getByText(SIGNED_IN)).toBeVisible({ timeout: 5_000 });
    assertPaired(harness);
  });

  test("a return with a state Signal never issued is refused: nothing written, no call to Atlas, the page still signed out", async ({ page, harness }) => {
    await page.goto(harness.pageURL("today"));
    await expect(page.getByRole("region", { name: "Welcome to Signal" })).toBeVisible();

    const forged = await page.context().newPage();
    const bogus = `${harness.daemon!.baseURL}/auth/callback?pairing_code=${encodeURIComponent(
      `127.0.0.1:1/FORGED-CODE`
    )}&state=bogus%3Cscript%3Ealert(1)%3C%2Fscript%3E`;
    const res = await forged.goto(bogus);
    expect(res, "the callback answered").toBeTruthy();
    expect(res!.headers()["content-type"] || "").toMatch(/^text\/html/);
    expect(res!.headers()["cache-control"] || "").toMatch(/no-store/);
    const html = await res!.text();
    // A fixed page: nothing from the URL comes back.
    expect(html).not.toContain("bogus");
    expect(html).not.toContain("<script>alert(1)");
    expect(html).not.toContain("FORGED-CODE");
    await expect(forged.getByText("This sign-in was not started here", { exact: false })).toBeVisible();
    await expect(forged.getByText("Signed in. Close this tab.")).toHaveCount(0);

    expect(harness.exists("hook.json"), "no hook.json after a refused return").toBe(false);
    expect(harness.exists("auth.json"), "no auth.json after a refused return").toBe(false);
    const counts = await harness.atlasCounts();
    expect(counts["/v1/cli/enroll"] || 0, "the refused return reached Atlas").toBe(0);

    await page.reload();
    await expect(page.getByRole("region", { name: "Welcome to Signal" })).toBeVisible();
    await expect(page.getByText(SIGNED_IN)).toHaveCount(0);
  });

  test("a forged return while a real sign-in is waiting does not cancel it", async ({ page, harness }) => {
    await page.goto(harness.pageURL("today"));
    await page.getByRole("button", { name: "Sign in with Atlas" }).click();
    await expect(page.getByText("Finish signing in in your browser")).toBeVisible();

    const forged = await page.context().newPage();
    await forged.goto(`${harness.daemon!.baseURL}/auth/callback?pairing_code=x&state=bogus`);
    await forged.close();

    // Several polls later the page is still waiting on the real one…
    await page.waitForTimeout(2_500);
    await expect(page.getByText("Finish signing in in your browser")).toBeVisible();
    await expect(page.getByRole("button", { name: "Try again" })).toHaveCount(0);

    // …which then completes.
    await finishInBrowser(page);
    await expect(page.getByText(SIGNED_IN)).toBeVisible({ timeout: 5_000 });
    assertPaired(harness);
  });
});
