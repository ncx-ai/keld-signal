import { test as base, expect, type Page, type Locator } from "@playwright/test";
import fs from "node:fs";
import { STATE_FILE } from "../playwright.config";
import type { E2EState } from "../global-setup";

export { expect };

export type Pane = "today" | "projects" | "settings";

/**
 * The page as a user drives it. Every spec goes through this: it opens a pane
 * by URL (the `?secret=` handoff `keld signal open` performs, per
 * docs/v3/contracts.md's page conventions) and then reads only what is on
 * screen. No spec fetches /v1/* itself.
 */
export class SignalApp {
  constructor(readonly page: Page, readonly state: E2EState) {}

  /** Navigate to a pane the way the CLI's link does, and wait until the pane
   *  has rendered something other than its "Loading…" placeholder. */
  async open(pane: Pane): Promise<void> {
    await this.page.goto(`${this.state.baseURL}/?secret=${encodeURIComponent(this.state.secret)}#/${pane}`);
    await expect(this.page.getByText("Loading…")).toBeHidden();
  }

  /** The switch labelled `label` (the page renders `<span>label <label
   *  class=switch><input type=checkbox>…</label></span>`). Returns the
   *  clickable label and the checkbox it drives; the input itself is visually
   *  hidden, so state is read off it and clicks go to the label. */
  switchNamed(label: string | RegExp): { control: Locator; input: Locator } {
    const holder = this.page.getByText(label);
    return { control: holder.locator("label").first(), input: holder.locator("input[type=checkbox]").first() };
  }

  async setSwitch(label: string | RegExp, on: boolean): Promise<void> {
    const { control, input } = this.switchNamed(label);
    if ((await input.isChecked()) !== on) await control.click();
    await expect(input).toBeChecked({ checked: on });
  }

  /** A setting row's switch: `<div class=settings-row><span>Name<div
   *  class=desc>…</div></span><label class=switch><input></label></div>`. */
  settingSwitch(name: RegExp): { control: Locator; input: Locator } {
    const row = this.page.getByText(name).locator("..");
    return { control: row.locator("label.switch").first(), input: row.locator("input[type=checkbox]").first() };
  }

  /** Table rows of the Today timeline that are focus-block cards (a time
   *  range "HH:MM → HH:MM" in their first cell). */
  blockRows(): Locator {
    return this.page.getByRole("row").filter({ hasText: /\d{2}:\d{2} → \d{2}:\d{2}/ }).filter({ hasNotText: "break ·" });
  }

  breakRows(): Locator {
    return this.page.getByRole("row").filter({ hasText: /break · \d+ ?(min|h)/ });
  }

  notRunningBanner(): Locator {
    return this.page.getByText("Signal is not running on this machine");
  }
}

function loadState(): E2EState {
  if (!fs.existsSync(STATE_FILE)) {
    throw new Error(`no ${STATE_FILE}: the daemon was not brought up, so this test cannot pass`);
  }
  return JSON.parse(fs.readFileSync(STATE_FILE, "utf8")) as E2EState;
}

/**
 * Block every request that is not to loopback. The page's one external
 * request is Google Fonts; nothing in this suite may touch the network, and
 * the fallback stack makes the screenshots deterministic on one machine.
 */
async function sealNetwork(page: Page): Promise<void> {
  await page.route(/^https?:\/\/(?!127\.0\.0\.1[:/]|localhost[:/])/, (route) => route.abort());
}

export const test = base.extend<{ signal: SignalApp; state: E2EState }>({
  state: async ({}, use) => {
    await use(loadState());
  },
  signal: async ({ page, state }, use) => {
    await sealNetwork(page);
    await use(new SignalApp(page, state));
  },
});
