import { test as base, expect, type Page, type Locator } from "@playwright/test";
import fs from "node:fs";
import { STATE_FILE } from "../playwright.config";
import type { E2EState } from "../global-setup";

export { expect };

export type Pane = "today" | "projects" | "integrations" | "settings";

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

  /** Open a pane that lives behind the Developer box (DEV_ONLY_PANES in
   *  app.js — Integrations, while its state machine settles).
   *
   *  ⚠️ SEEDED, NOT TAPPED. enterDeveloperMode() clicks the version seven
   *  times, which needs a pane already rendered and TOGGLES — so it cannot be
   *  used to reach a pane that is hidden until it runs. This writes the same
   *  per-browser preference the taps write, before the first script runs, so
   *  the very first navigation already sees developer mode on. Deliberately a
   *  DIFFERENT call from open(): if the pane ever stops being dev-only, these
   *  specs keep passing and the visibility test is what says so, rather than
   *  open() quietly papering over the change for everyone.
   */
  async openDev(pane: Pane): Promise<void> {
    await this.page.addInitScript(() => {
      try {
        const key = "keld_signal_local_prefs";
        const prev = JSON.parse(localStorage.getItem(key) || "{}");
        localStorage.setItem(key, JSON.stringify({ ...prev, devMode: true }));
      } catch {
        // A context with storage blocked still renders; the spec that needs
        // the pane will fail on the pane, not here.
      }
    });
    await this.open(pane);
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

  /** Turn developer mode on the way a person does: seven taps on the version
   *  in the sidebar, inside the tap window. Developer mode is a per-browser
   *  preference (localStorage), so it survives every `open()` in the same
   *  test and never leaks into the next one, which starts a fresh context.
   *
   *  ⚠️ The taps TOGGLE. Call this once per test, on a fresh context; a second
   *  call turns developer mode back off. The Developer heading is the proof
   *  the taps landed — without it the developer rows are simply absent and a
   *  spec waiting for one times out saying nothing useful. */
  async enterDeveloperMode(): Promise<void> {
    const version = this.page.locator("#navVersion");
    await expect(version).toBeVisible();
    for (let i = 0; i < 7; i++) await version.click();
    await expect(this.page.getByText("Developer", { exact: true })).toBeVisible();
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
