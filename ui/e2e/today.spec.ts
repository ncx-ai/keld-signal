import { test, expect } from "./support/fixtures";

// The Today pane, as a person sees it: the cards Signal cut from the corpus,
// the date it generated them for, and the health strip saying the machine's
// own pieces are up. Nothing here reads /v1/*.
test.describe("Today", () => {
  test("shows at least one focus block card with a time range, project, tokens and an est. price", async ({ signal, page }) => {
    await signal.open("today");

    const rows = signal.blockRows();
    await expect(rows.first()).toBeVisible();
    expect(await rows.count()).toBeGreaterThanOrEqual(1);

    const card = rows.first();
    const cells = card.getByRole("cell");
    // Focus block: "HH:MM → HH:MM" plus its length and how it ended.
    await expect(cells.nth(0)).toContainText(/\d{2}:\d{2} → \d{2}:\d{2}/);
    await expect(cells.nth(0)).toContainText(/\d+ min|\d+h/);
    // Project: a project name, or the honest "no project" — never the "—"
    // that means attribution has not run (it runs right after the cut).
    await expect
      .poll(async () => (await cells.nth(1).innerText()).trim(), { timeout: 20_000 })
      .not.toMatch(/^(—|)$/);
    // Tokens: a real figure, formatted (e.g. "1.5M", "40.2K").
    await expect(cells.nth(2)).toContainText(/^\d+(\.\d)?[KM]?$/);
    // Every dollar figure carries "est." (docs/v3/contracts.md).
    await expect(cells.nth(3)).toContainText(/\$\d+\.\d{2} est\./);
  });

  test("states the date line and where the data comes from", async ({ signal, page }) => {
    await signal.open("today");
    const dateLine = page.getByText("from your device, on your device");
    await expect(dateLine).toBeVisible();
    // "Friday 5 September · from your device, on your device"
    await expect(dateLine).toHaveText(/^[A-Z][a-z]+day \d{1,2} [A-Z][a-z]+ · from your device, on your device$/);
  });

  test("the health strip names Signal and the Analysis service, both up", async ({ signal, page }) => {
    await signal.open("today");
    const signalPill = page.getByText(/^Signal( \S+)?$/);
    const sidecarPill = page.getByText(/^Analysis service( \S+)?$/);
    await expect(signalPill).toBeVisible();
    await expect(sidecarPill).toBeVisible();
    // Green pills — the class is how the page says "ok"; the sidebar's own
    // one-word summary says the same thing in words.
    await expect(signalPill).toHaveClass(/\bok\b/);
    await expect(sidecarPill).toHaveClass(/\bok\b/);
    await expect(page.getByText(/^(all good|catching up)$/)).toBeVisible();
    // With Send to Atlas off there is no Atlas pill to misread.
    await expect(page.getByText(/^Atlas( \S+)?$/)).toHaveCount(0);
  });

  test("does not show the not-running banner while the daemon is up", async ({ signal }) => {
    await signal.open("today");
    await expect(signal.notRunningBanner()).toBeHidden();
    await expect(signal.page.getByText("not running", { exact: true })).toHaveCount(0);
  });
});
