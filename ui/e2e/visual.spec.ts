import { test, expect } from "./support/fixtures";

// One screenshot of the Today pane per browser, compared against the committed
// baseline in visual.spec.ts-snapshots/ with the tolerance the config sets.
//
// What is masked is what legitimately moves between runs: the date line
// (today's date), the time-range column (the corpus is anchored to "now",
// so clock times shift while its structure — counts, tokens, spend, run/break
// shape — does not), the Project column and the Rhythm squares (the Projects
// journey attributes blocks locally earlier in the same run, and attribution
// finishes asynchronously). Everything else — tiles, headers, health strip,
// typography, layout — is compared.
//
// Update the baseline deliberately: `npm run update-baselines`.
test("the Today pane looks as it did", async ({ signal, page }) => {
  await signal.open("today");
  await expect(signal.blockRows().first()).toBeVisible();
  await expect(page.getByText(/^Signal( \S+)?$/)).toBeVisible();
  await signal.setSwitch(/^Show breaks/, false);
  // Let attribution settle so the masked column does not resize mid-shot.
  await page.waitForTimeout(500);

  await expect(page).toHaveScreenshot("today.png", {
    fullPage: true,
    mask: [
      page.getByText("from your device, on your device"),
      page.getByRole("row").locator("td:nth-child(1)"),
      page.getByRole("row").locator("td:nth-child(2)"),
      page.locator(".rhythm"),
    ],
    maskColor: "#d9dde3",
  });
});
