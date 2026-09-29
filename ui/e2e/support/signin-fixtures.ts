import { test as base, expect, type BrowserContext } from "@playwright/test";
import { SigninHarness } from "./signin-harness";

export { expect };

/**
 * Seal the whole CONTEXT, not one page: the sign-in opens the authorize URL in
 * a second tab, and a page-level route would leave that tab free to reach the
 * network. Loopback is the daemon and the mock Atlas; nothing else may load
 * (the page's one external request is Google Fonts).
 */
export async function sealContext(context: BrowserContext): Promise<void> {
  await context.route(/^https?:\/\/(?!127\.0\.0\.1[:/]|localhost[:/])/, (route) => route.abort());
}

/** A fresh mock Atlas and a fresh, unpaired daemon for every test. */
export const test = base.extend<{ harness: SigninHarness }>({
  harness: async ({ context }, use, testInfo) => {
    await sealContext(context);
    const h = new SigninHarness(testInfo.title);
    try {
      await h.startAtlas();
      await h.startDaemon();
      await use(h);
    } finally {
      await h.close();
    }
  },
});
