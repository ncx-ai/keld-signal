import { test, expect } from "./support/fixtures";

// Breaks are derived by the page (docs/v3/contracts.md: the gap between two
// consecutive blocks of one session when it is >= 15 minutes). The corpus
// scripts/e2e-up.sh generates has sessions with such gaps by construction.
test.describe("Show breaks", () => {
  test("toggling it reveals a break row between two blocks with a gap of at least 15 minutes, and hides it again", async ({ signal, page }) => {
    await signal.open("today");
    await expect(signal.blockRows().first()).toBeVisible();

    // Start from off, whatever a previous run left in the settings file.
    await signal.setSwitch(/^Show breaks/, false);
    await expect(signal.breakRows()).toHaveCount(0);

    await signal.setSwitch(/^Show breaks/, true);
    const breaks = signal.breakRows();
    await expect(breaks.first()).toBeVisible();

    // "☕ break · 50 min · 10:20 → 11:10 · no tokens in or out"
    const text = await breaks.first().innerText();
    const m = /break · (?:(\d+)h)?\s?(?:(\d+) ?m(?:in)?)?/.exec(text);
    expect(m, `break row text: ${text}`).not.toBeNull();
    const minutes = Number(m![1] || 0) * 60 + Number(m![2] || 0);
    expect(minutes).toBeGreaterThanOrEqual(15);
    expect(text).toContain("no tokens in or out");

    // It sits BETWEEN two focus blocks in the timeline, never at an edge.
    const all = await page.getByRole("row").allInnerTexts();
    const i = all.findIndex((t) => t.includes(text.trim()));
    expect(i).toBeGreaterThan(1); // 0 is the header row
    expect(all[i - 1]).toMatch(/\d{2}:\d{2} → \d{2}:\d{2}/);
    expect(all[i + 1]).toMatch(/\d{2}:\d{2} → \d{2}:\d{2}/);
    expect(all[i - 1]).not.toContain("break ·");
    expect(all[i + 1]).not.toContain("break ·");

    await signal.setSwitch(/^Show breaks/, false);
    await expect(signal.breakRows()).toHaveCount(0);
  });
});
