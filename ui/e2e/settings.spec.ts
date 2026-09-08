import { test, expect } from "./support/fixtures";

// The Settings pane against docs/v3/contracts.md's GET|PUT /v1/settings and
// its page conventions. The daemon under test runs with KELD_ATLAS=0, so
// Send to Atlas is off and pinned, and the developer granularity is available.
test.describe("Settings", () => {
  test.beforeEach(({ state }) => {
    test.skip(!state.settingsMounted, "GET /v1/settings is not mounted on this daemon build yet (lane B2 in flight)");
  });

  test('Send to Atlas shows "off" and the page says it is local only', async ({ signal, page }) => {
    await signal.open("settings");
    const { input } = signal.settingSwitch(/^Send to Atlas/);
    await expect(input).not.toBeChecked();
    // KELD_ATLAS=0 pins it, and the page says so next to the control.
    await expect(input).toBeDisabled();
    await expect(page.getByText("Set by KELD_ATLAS on this machine.")).toBeVisible();
    await expect(page.getByText("Local only", { exact: true })).toBeVisible();
    await expect(page.getByText("Send to Atlas: on")).toHaveCount(0);
  });

  // ⚠️ **THIS ASSERTED THE CONTROL WAS VISIBLE, AND IT IS NOW HIDDEN ON
  // PURPOSE.** Block granularity changes how a block is CUT — the 20-minute
  // budget becomes 5 minutes, one prompt or one minute — which is the shape of
  // every block a machine produces from then on, and the non-default modes have
  // never been exercised end to end. The control is hidden behind
  // SHOW_BLOCK_GRANULARITY in app.js until they are; the SETTING is untouched,
  // so KELD_DEV_BLOCKS, the dev_blocks key and the server's refusal while Send
  // to Atlas is on all still work and are still tested.
  //
  // Inverted rather than deleted: when the flag goes back on, this test is the
  // thing that says what "on" should look like.
  test("developer block granularity is hidden until its modes are tested", async ({ signal, page }) => {
    await signal.open("settings");
    await expect(page.getByText("Block granularity")).toHaveCount(0);
    await expect(page.getByRole("radio")).toHaveCount(0);
  });

  test('"Show breaks" persists across a reload', async ({ signal, page }) => {
    await signal.open("today");
    await signal.setSwitch(/^Show breaks/, true);
    await page.reload();
    await expect(page.getByText("Loading…")).toBeHidden();
    await expect(signal.switchNamed(/^Show breaks/).input).toBeChecked();
    await expect(signal.breakRows().first()).toBeVisible();

    await signal.setSwitch(/^Show breaks/, false);
    await page.reload();
    await expect(page.getByText("Loading…")).toBeHidden();
    await expect(signal.switchNamed(/^Show breaks/).input).not.toBeChecked();
    await expect(signal.breakRows()).toHaveCount(0);
  });

  test('"Start at login" is disabled, with "in the desktop app"', async ({ signal, page }) => {
    await signal.open("settings");
    const { input } = signal.settingSwitch(/^Start at login/);
    await expect(input).toBeDisabled();
    await expect(input).not.toBeChecked();
    await expect(page.getByText(/in the desktop app/)).toBeVisible();
  });
});
