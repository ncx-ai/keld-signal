import { test, expect } from "./support/fixtures";

// One screenshot of the Today pane per browser, compared against the committed
// baseline in visual.spec.ts-snapshots/ with the tolerance the config sets.
//
// What is masked is what legitimately moves between runs: the date line
// (today's date), the time-range column, the Project column and the Rhythm
// squares (the Projects journey attributes blocks locally earlier in the same
// run, and attribution finishes asynchronously). Everything else — tiles,
// headers, health strip, typography, layout — is compared.
//
// ⚠️ **THE SHOT IS CLIPPED, AND THIS COMMENT USED TO CLAIM IT DID NOT NEED TO
// BE.** It said the corpus's "structure — counts, tokens, spend, run/break
// shape — does not [shift]" because it is anchored to "now". The count does
// shift, and by the one dimension a fullPage screenshot is most sensitive to:
// blockgen lays its sessions across the seven days ending at the anchor, and
// how many of them land on TODAY depends on how far into the local day the
// suite runs. Measured: the same corpus rendered 15 rows when the baseline was
// captured and 17 hours later in the day, a 193px-taller page, and the test
// failed on roughly half of full-suite runs while passing every time it ran
// alone or with KELD_E2E_END pinned. It read as flakiness; it was a real
// dependency on the wall clock that nothing declared.
//
// Clipping to the top of the pane compares exactly what the paragraph above
// says this test is for — chrome, tiles, health strip, typography, and the
// first cards — and stops the arbitrary length of the tail from deciding
// whether it passes. The functional specs already assert on the rows
// themselves, which is where a count belongs.
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
    clip: { x: 0, y: 0, width: 1280, height: 900 },
    mask: [
      page.getByText("from your device, on your device"),
      page.getByRole("row").locator("td:nth-child(1)"),
      page.getByRole("row").locator("td:nth-child(2)"),
      page.locator(".rhythm"),
    ],
    maskColor: "#d9dde3",
  });
});
