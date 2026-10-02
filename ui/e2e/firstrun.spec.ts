import { test, expect } from "./support/signin-fixtures";

// The first-open choice (AC-12), against a real, fresh, unpaired daemon whose
// agent-config.json has never held send_to_atlas and whose environment carries
// no KELD_ATLAS — decision-table row 1, the only row that shows it.

test.describe("First open", () => {
  test("a fresh machine offers Sign in with Atlas or Use locally only, on every pane", async ({ page, harness }) => {
    for (const pane of ["today", "projects", "settings"]) {
      await page.goto(harness.pageURL(pane));
      await expect(page.getByRole("region", { name: "Welcome to Signal" })).toBeVisible();
      await expect(page.getByText("Signal is already collecting on this computer.")).toBeVisible();
      await expect(page.getByRole("button", { name: "Sign in with Atlas" })).toBeVisible();
      await expect(page.getByRole("button", { name: "Use locally only" })).toBeVisible();
      await expect(page.getByText("You can sign in later from Settings.")).toBeVisible();
      await expect(page.getByText("Local only: nothing leaves this computer.")).toBeVisible();
      // The bar stays out of the choice's way.
      await expect(page.locator("#signinBanner")).toBeHidden();
      // Nor does the top bar state a choice nobody has made yet.
      await expect(page.locator("#envPill")).toBeHidden();
    }
  });

  test("Use locally only is kept across a reload and a daemon restart, and Settings still offers Sign in", async ({ page, harness }) => {
    await page.goto(harness.pageURL("today"));
    await page.getByRole("button", { name: "Use locally only" }).click();

    await expect(page.getByRole("region", { name: "Welcome to Signal" })).toHaveCount(0);
    await expect(page.locator("#topbarTitle")).toHaveText("Focus blocks");
    await expect.poll(() => harness.readJSON("agent-config.json")?.send_to_atlas).toBe(false);
    // Local only is a choice, not a fault: no not-signed-in bar.
    await expect(page.locator("#signinBanner")).toBeHidden();
    // Once chosen, the top bar says what was chosen.
    await expect(page.locator("#envPill")).toHaveText("Local only");

    await page.reload();
    await expect(page.getByText("Loading…")).toBeHidden();
    await expect(page.locator("#topbarTitle")).toHaveText("Focus blocks");
    await expect(page.getByRole("region", { name: "Welcome to Signal" })).toHaveCount(0);

    // A restart moves the page to a new port with a new secret; the choice
    // lives in agent-config.json, so it survives.
    await harness.stopDaemon();
    await harness.startDaemon();
    await page.goto(harness.pageURL("today"));
    await expect(page.getByText("Loading…")).toBeHidden();
    await expect(page.locator("#topbarTitle")).toHaveText("Focus blocks");
    await expect(page.getByRole("region", { name: "Welcome to Signal" })).toHaveCount(0);
    await expect(page.locator("#signinBanner")).toBeHidden();

    await page.goto(harness.pageURL("settings"));
    const tile = page.locator(".tile", { hasText: "Atlas account" });
    await expect(tile.getByText("Not signed in.")).toBeVisible();
    await expect(tile.getByRole("button", { name: "Sign in with Atlas" })).toBeEnabled();
    const sendToAtlas = page.getByText(/^Send to Atlas/).locator("..").locator("input[type=checkbox]").first();
    await expect(sendToAtlas).not.toBeChecked();

    // Nothing was asked of Atlas along the way.
    const counts = await harness.atlasCounts();
    expect(Object.keys(counts).filter((k) => k.startsWith("/v1/cli/")), "local only made a sign-in call").toEqual([]);
  });
});
