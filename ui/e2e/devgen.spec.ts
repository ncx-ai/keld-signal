import { test, expect } from "./support/fixtures";

/**
 * USER STORY
 *
 *   As a developer, when I press "Generate block", the block is in Focus
 *   blocks when the page comes back, and Projects reflects it — the block's
 *   repository is listed there.
 *
 * Both halves are load-bearing: a block that appears on Today but changes
 * nothing under Projects means attribution did not run, which is the half the
 * repository list exists for.
 *
 * ⚠️ **"WHEN THE PAGE COMES BACK" IS THE PART THAT WAS BROKEN.** The route used
 * to write a transcript and answer immediately, leaving the rest to timers —
 * the watcher's poll, then the block emitter's sweep, which is FIVE MINUTES by
 * default. The button showed a tick and the page showed nothing, sometimes for
 * minutes. The first version of this spec papered over that with a 240-second
 * poll-and-reload loop, which made a broken control look like a slow one and is
 * exactly the wrong thing for a test to do. It does not wait any more: the
 * route drives ingest, advance and one sweep before it answers, and reports how
 * many blocks the ledger actually holds. If that regresses, this fails in
 * seconds instead of hiding it.
 *
 * Negative cases: the button is absent while the toggle is off, and the
 * repository list must survive a reload (it did not — `value` is not a
 * textarea's content).
 */
test.describe("Generate block (developer)", () => {
  test.beforeEach(({ state }) => {
    test.skip(!state.settingsMounted, "GET /v1/settings is not mounted on this daemon build");
  });

  /** The Settings pane's switches sit beside their label rather than inside it,
   *  so they are driven through `settingSwitch` (the Today pane's `setSwitch`
   *  looks for a label INSIDE the text node and finds nothing). */
  async function setSettingSwitch(signal: any, name: RegExp, on: boolean) {
    const { control, input } = signal.settingSwitch(name);
    if ((await input.isChecked()) !== on) await control.click();
    await expect(input).toBeChecked({ checked: on });
  }

  test("NEGATIVE: the button is absent while the toggle is off", async ({ signal, page }) => {
    await signal.open("settings");
    // ESTABLISH the precondition rather than assume it: the journey below turns
    // the toggle on, the setting is stored on the machine, and a spec that only
    // passes when it runs first is not a spec.
    await setSettingSwitch(signal, /^Generate block button/, false);
    await expect(page.locator("#genBlockBtn")).toBeHidden();

    await setSettingSwitch(signal, /^Generate block button/, true);
    await expect(page.locator("#genBlockBtn")).toBeVisible();
    await expect(page.getByText("Repositories it draws from")).toBeVisible();
    await expect(page.getByText(/randomly named repository is always in the mix/)).toBeVisible();

    await setSettingSwitch(signal, /^Generate block button/, false);
    await expect(page.locator("#genBlockBtn")).toBeHidden();
  });

  test("NEGATIVE: the repository list survives a reload", async ({ signal, page }) => {
    // It did not, and nothing HTTP-side could tell: the setting was stored
    // correctly and the box came back empty, which reads as "my repositories
    // were not saved".
    await signal.open("settings");
    await setSettingSwitch(signal, /^Generate block button/, true);
    const box = page.getByLabel("Repositories the block generator draws from");
    await box.fill("github.com/e2e-org/persisted");
    await box.blur();

    await page.reload();
    await expect(page.getByText("Loading…")).toBeHidden();
    await expect(page.getByLabel("Repositories the block generator draws from"))
      .toHaveValue("github.com/e2e-org/persisted");
  });

  test("THE STORY: press it, and the block is in Focus blocks and reflected in Projects",
    async ({ signal, page }) => {
      await signal.open("settings");
      await setSettingSwitch(signal, /^Generate block button/, true);

      // Configure one repository nobody has declared, so what this click
      // produces is distinguishable from the corpus's own four.
      //
      // ⚠️ Pinning the list does NOT pin the repository: devRepoChoices always
      // appends one randomly named repository no rule can match — the whole
      // reason the control exists — so a one-entry list still leaves the button
      // a 50% chance of using the random one. The repository is therefore READ
      // OFF the button rather than predicted.
      const configured = `github.com/e2e-org/generated-${Date.now()}`;
      const box = page.getByLabel("Repositories the block generator draws from");
      await box.fill(configured);
      await box.blur();

      await signal.open("today");
      const blocksBefore = await signal.blockRows().count();

      // Located by id: the label IS the thing under test, so a name-based
      // locator stops matching the moment the button does its job.
      const button = page.locator("#genBlockBtn");
      await expect(button).toBeVisible();
      await button.click();

      // The tick means the block EXISTS, not that a request was accepted. The
      // route drives the pipeline and reports the ledger's own count, so
      // "— not cut" is a real outcome this assertion must not accept.
      await expect(button).toHaveText(/ ✓$/, { timeout: 60_000 });
      const repo = ((await button.textContent()) || "").replace(/ ✓$/, "").trim();
      expect(repo.length).toBeGreaterThan(0);

      // FOCUS BLOCKS, with no polling loop. The page reloaded itself when the
      // route answered; one reload here proves the block survives a fresh load
      // rather than living in memory.
      await page.reload();
      await expect(page.getByText("Loading…")).toBeHidden();
      expect(await signal.blockRows().count()).toBeGreaterThan(blocksBefore);

      // PROJECTS reflects it: the block's repository is listed there.
      await signal.open("projects");
      await expect(page.getByText(repo, { exact: false }).first())
        .toBeVisible({ timeout: 30_000 });

      // Leave the toggle off. The generated blocks cannot be un-cut — which is
      // why this whole project runs last — but the SETTING is restorable.
      await signal.open("settings");
      await setSettingSwitch(signal, /^Generate block button/, false);
    });
});
