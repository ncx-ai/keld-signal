import { test, expect } from "./support/fixtures";

/**
 * The developer block generator, driven the way a person drives it: turn the
 * toggle on in Settings, press the button in the top bar, and then look for the
 * block on Today and its repository under Projects.
 *
 * ⚠️ **THIS SPEC EXISTS BECAUSE THE FEATURE WAS SHIPPED WITHOUT IT.** The route
 * was exercised over HTTP and the block was confirmed in the ledger database, so
 * "it works" rested on evidence that skipped every part a person touches: the
 * toggle, the button, and whether anything renders at the far end. A ledger row
 * nobody can see is not the feature. Writing this immediately found two defects
 * the HTTP path could not: the repository box rendered empty on every reload
 * (`value` is not a textarea's content), and the switch cannot be driven by the
 * Today pane's helper.
 *
 * The wait is generous and explicit on purpose. Pressing the button writes a
 * transcript; the block appears only after the watcher's poll, the sidecar's
 * ingest and the emitter's sweep, so asserting immediately would be asserting
 * something the design never promised.
 */
test.describe("Generate block (developer)", () => {
  test.beforeEach(({ state }) => {
    test.skip(!state.settingsMounted, "GET /v1/settings is not mounted on this daemon build");
  });

  /** The Settings pane's switches live beside their label rather than inside
   *  it, so they are driven through `settingSwitch` (the Today pane's
   *  `setSwitch` looks for a label INSIDE the text node and finds nothing). */
  async function setSettingSwitch(signal: any, name: RegExp, on: boolean) {
    const { control, input } = signal.settingSwitch(name);
    if ((await input.isChecked()) !== on) await control.click();
    await expect(input).toBeChecked({ checked: on });
  }

  test("the button is hidden until the developer toggle is on", async ({ signal, page }) => {
    await signal.open("settings");
    // ⚠️ ESTABLISH the precondition rather than assume it. This asserted the
    // button was hidden on arrival, passed when the file ran alone, and failed
    // in the full suite: the journey below leaves the toggle ON, the setting is
    // stored on the machine, and the second browser to reach this file started
    // from the state the first one left. A spec that only passes when it runs
    // first is not a spec.
    await setSettingSwitch(signal, /^Generate block button/, false);
    await expect(page.getByRole("button", { name: "Generate block" })).toBeHidden();

    await setSettingSwitch(signal, /^Generate block button/, true);
    await expect(page.getByRole("button", { name: "Generate block" })).toBeVisible();

    // The repository box appears with it, and names the fourth, unmatchable
    // repository as part of the mix rather than leaving it a surprise.
    await expect(page.getByText("Repositories it draws from")).toBeVisible();
    await expect(page.getByText(/randomly named repository is always in the mix/)).toBeVisible();

    await setSettingSwitch(signal, /^Generate block button/, false);
    await expect(page.getByRole("button", { name: "Generate block" })).toBeHidden();
  });

  test("the repository list survives a reload", async ({ signal, page }) => {
    // ⚠️ It did not, and nothing HTTP-side could tell: the setting was stored
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

  test("pressing it produces a focus block on Today and a repository under Projects",
    async ({ signal, page }) => {
      test.slow(); // the block travels watcher -> ingest -> cutter -> emitter

      await signal.open("settings");
      await setSettingSwitch(signal, /^Generate block button/, true);

      // Configure one repository nobody has declared, so whatever this click
      // produces is distinguishable from the corpus's own four.
      //
      // ⚠️ **PINNING THE LIST DOES NOT PIN THE REPOSITORY, AND ASSUMING IT DID
      // MADE THIS TEST A COIN FLIP.** `devRepoChoices` always appends one
      // randomly named repository that no rule can match — that is the whole
      // reason the control exists, so an unattributed block can be seen — so a
      // one-entry list still gives the button a 50% chance of using the random
      // one. The first run of this spec passed on luck and the second failed on
      // a label that was never guaranteed. So the repository is READ OFF the
      // button rather than predicted, which is both deterministic and a
      // stronger assertion: it checks the thing the button actually did.
      const configured = `github.com/e2e-org/generated-${Date.now()}`;
      const box = page.getByLabel("Repositories the block generator draws from");
      await box.fill(configured);
      await box.blur();

      await signal.open("today");
      const before = await signal.blockRows().count();

      // ⚠️ **LOCATED BY ID, NOT BY ACCESSIBLE NAME.** The button's label IS the
      // thing under test — it changes to "<repo> ✓" — and a `getByRole("button",
      // {name: "Generate block"})` locator stops matching the moment it does.
      // The assertion then waited 30s on a locator that could only ever resolve
      // to the label it started with, and reported "unexpected value: Generate
      // block" about a button that had done exactly the right thing.
      const button = page.locator("#genBlockBtn");
      await expect(button).toBeVisible();
      await button.click();

      // The button reports the repository it used rather than "done": what it
      // creates is a transcript, and saying "done" while the page is unchanged
      // for the next minute reads as broken.
      await expect(button).toHaveText(/ ✓$/, { timeout: 30_000 });
      const label = (await button.textContent()) || "";
      const repo = label.replace(/ ✓$/, "").trim();
      expect(repo.length).toBeGreaterThan(0);

      // The part that actually matters: the block has to appear.
      await expect(async () => {
        await page.reload();
        await expect(page.getByText("Loading…")).toBeHidden();
        expect(await signal.blockRows().count()).toBeGreaterThan(before);
      }).toPass({ timeout: 240_000, intervals: [5_000] });

      // And it has to be attributed to the repository it was generated for —
      // either the configured one or the deliberately unmatchable one, both of
      // which must show up as work with a repository against it.
      await signal.open("projects");
      await expect(page.getByText(repo, { exact: false }))
        .toBeVisible({ timeout: 30_000 });

      // Leave the toggle off. The generated blocks cannot be un-cut — which is
      // why this whole project runs last — but the SETTING is restorable, and
      // leaving it on changes what the next run of this file starts from.
      await signal.open("settings");
      await setSettingSwitch(signal, /^Generate block button/, false);
    });
});
