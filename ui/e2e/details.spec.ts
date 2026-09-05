import { test, expect } from "./support/fixtures";

// "Show details" is a per-viewer preference (docs/v3/contracts.md, page
// convention 3): off in every fresh browser context, and when on it adds the
// stage/status line under each card that the plain view keeps out of sight.
test.describe("Show details", () => {
  test("reveals reason/status text on a card that is hidden by default, and hides it again", async ({ signal, page }) => {
    await signal.open("today");
    await expect(signal.blockRows().first()).toBeVisible();

    const detailLine = page.getByText(/cut=\w+.*measured=\w+.*attributed=\w+/).first();
    await expect(detailLine).toHaveCount(0);
    await expect(page.getByText(/reason=\w+/)).toHaveCount(0);

    await signal.setSwitch(/^Show details/, true);
    await expect(detailLine).toBeVisible();
    // Every stage is stated (ok / failed / absent), so a reader can tell
    // "never happened" from "we checked and it failed".
    await expect(detailLine).toContainText(/cut=(ok|failed|pending|absent)/);
    await expect(detailLine).toContainText(/attributed=(ok|failed|pending|absent)/);
    await expect(detailLine).toContainText(/sent=\S+/);
    // Each card gets exactly one details line.
    expect(await page.getByText(/cut=\w+.*measured=\w+/).count()).toBe(await signal.blockRows().count());

    await signal.setSwitch(/^Show details/, false);
    await expect(page.getByText(/cut=\w+.*measured=\w+/)).toHaveCount(0);
  });
});
