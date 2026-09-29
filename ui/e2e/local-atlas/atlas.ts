import fs from "node:fs";
import path from "node:path";
import { test as base, expect, type BrowserContext, type Page } from "@playwright/test";
import { SigninHarness } from "../support/signin-harness";
import { sealContext } from "../support/signin-fixtures";

export { expect };

/** The real Atlas under test. Unset means the whole suite skips. */
export const ATLAS_WEB = (process.env.KELD_E2E_ATLAS_WEB || "").replace(/\/+$/, "");
/** Its API — also its `otlp_public_url`, which is the host inside every
 *  pairing code. The daemon compares that host EXACTLY against KELD_API_URL,
 *  so this must be the same spelling Atlas uses (localhost, not 127.0.0.1). */
export const ATLAS_API = (process.env.KELD_E2E_ATLAS_API || "http://localhost:8000").replace(/\/+$/, "");
const TELEMETRY_PORT = process.env.KELD_E2E_TELEMETRY_PORT || "14421";

export const ADMIN = { email: "admin@acme.test", password: "acme2026", org: "Acme" };
export const VIEWER = { email: "sarah@acme.test", password: "acme2026", org: "Acme" };

export const SIGNED_IN = /^Signed in (as|to Atlas)/;
export const SIGNED_IN_TAB = "Signed in. Close this tab.";

/**
 * A fresh, unpaired daemon for every test, pointed at the real Atlas. The
 * browser context is sealed to loopback (localhost:3000/8000 and 127.0.0.1
 * are all loopback), so a redirect anywhere else is aborted, not followed.
 */
export const test = base.extend<{ harness: SigninHarness }>({
  harness: async ({ context }, use, testInfo) => {
    test.skip(!ATLAS_WEB, "KELD_E2E_ATLAS_WEB is not set: this suite needs a real local Atlas (see ui/e2e/README.md)");
    await sealContext(context);
    const h = new SigninHarness(`la-${testInfo.title}`, { telemetryPort: TELEMETRY_PORT });
    try {
      h.useAtlas(ATLAS_API, ATLAS_WEB);
      await h.startDaemon();
      await use(h);
    } finally {
      await h.close();
    }
  },
});

// ---- the daemon ----

/** A page-secret call to the daemon, the way the page makes it. */
export async function daemonCall(h: SigninHarness, method: string, route: string, body?: unknown): Promise<{ status: number; body: any }> {
  const res = await fetch(`${h.daemon!.baseURL}${route}`, {
    method,
    headers: { "x-keld-agent-secret": h.daemon!.secret, ...(body === undefined ? {} : { "content-type": "application/json" }) },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  let parsed: any = text;
  try {
    parsed = JSON.parse(text);
  } catch {
    // not JSON
  }
  return { status: res.status, body: parsed };
}

export async function authState(h: SigninHarness): Promise<any> {
  const r = await daemonCall(h, "GET", "/v1/auth/state");
  expect(r.status, `GET /v1/auth/state: ${JSON.stringify(r.body)}`).toBe(200);
  return r.body;
}

/** The two files a pairing writes, verbatim, for "nothing changed" checks. */
export function pairingFiles(h: SigninHarness): { hook: string | null; auth: string | null } {
  const read = (n: string) => {
    try {
      return fs.readFileSync(path.join(h.home, n), "utf8");
    } catch {
      return null;
    }
  };
  return { hook: read("hook.json"), auth: read("auth.json") };
}

export function expectNotPaired(h: SigninHarness, why: string): void {
  const f = pairingFiles(h);
  expect(f.hook, `${why}: hook.json must not exist`).toBeNull();
  expect(f.auth, `${why}: auth.json must not exist`).toBeNull();
}

/** Paired as `who`: both files, the right principal/org/host, and an ingest
 *  token the REAL Atlas accepts — the proof that what was written is live. */
export async function expectPairedAs(h: SigninHarness, who: { email: string; org: string }): Promise<{ ingestToken: string }> {
  const hook = h.readJSON("hook.json");
  const auth = h.readJSON("auth.json");
  expect(hook, "hook.json written by the pairing").toBeTruthy();
  expect(auth, "auth.json written by the pairing").toBeTruthy();
  expect(String(hook.ingest_token || ""), "hook.json carries an ingest token").not.toBe("");
  expect(hook.endpoint, "hook.json endpoint is Atlas's otlp_public_url").toBe(ATLAS_API);
  expect(auth.principal, "auth.json principal").toBe(who.email);
  expect(auth.org, "auth.json org").toBe(who.org);
  expect(auth.api_url, "auth.json api_url").toBe(ATLAS_API);
  expect(String(auth.access_token || ""), "auth.json carries a CLI token").not.toBe("");

  const res = await fetch(`${ATLAS_API}/v1/enrichment-settings`, { headers: { "x-keld-ingest-token": hook.ingest_token } });
  expect(res.status, `Atlas refused the ingest token the pairing wrote (GET /v1/enrichment-settings → ${res.status})`).toBe(200);
  return { ingestToken: hook.ingest_token };
}

// ---- the page ----

/** Click Sign in with Atlas wherever `scope` shows it, and return the
 *  authorize URL the page then shows as its link. */
export async function startSignin(page: Page, scope = page.locator("body")): Promise<string> {
  await scope.getByRole("button", { name: "Sign in with Atlas" }).click();
  await expect(page.getByText("Finish signing in in your browser")).toBeVisible();
  const href = await page.getByRole("link", { name: "Open the Atlas sign-in page" }).getAttribute("href");
  expect(href, "the page shows the authorize link").toBeTruthy();
  const u = new URL(href!);
  expect(`${u.origin}${u.pathname}`, "the link goes to the real Atlas's authorize page").toBe(`${ATLAS_WEB}/cli/signal/authorize`);
  return href!;
}

/** Follow the page's own link in a new tab of the same context. */
export async function openAuthorizeTab(page: Page): Promise<Page> {
  const link = page.getByRole("link", { name: "Open the Atlas sign-in page" });
  const [tab] = await Promise.all([page.context().waitForEvent("page"), link.click()]);
  await tab.waitForLoadState("domcontentloaded");
  return tab;
}

// ---- Atlas ----

/** Atlas's own email login form, as a person fills it. */
export async function fillAtlasLogin(tab: Page, who: { email: string; password: string }): Promise<void> {
  await expect(tab.locator("#email"), `expected Atlas's login form, got ${tab.url()}`).toBeVisible();
  await tab.locator("#email").fill(who.email);
  await tab.locator("#password").fill(who.password);
  await tab.getByRole("button", { name: "Sign in", exact: true }).click();
}

/** Sign the whole browser context in to Atlas without the form: the session
 *  cookie lands in the context's jar, exactly as the form would leave it. */
export async function atlasSession(context: BrowserContext, who: { email: string; password: string }): Promise<void> {
  const res = await context.request.post(`${ATLAS_WEB}/api/auth/login`, { data: { email: who.email, password: who.password } });
  expect(res.status(), `Atlas login for ${who.email}: ${await res.text()}`).toBe(200);
}

/** On the authorize page: the person, the org, one Continue — then click it. */
export async function continueAs(tab: Page, who: { email: string; org: string }): Promise<void> {
  await expect(tab.getByText(`Continue as ${who.email}`), `authorize page at ${tab.url()}`).toBeVisible({ timeout: 15_000 });
  await expect(tab.getByText(who.org, { exact: true })).toBeVisible();
  await expect(tab.getByRole("button", { name: "Continue" })).toHaveCount(1);
  await tab.getByRole("button", { name: "Continue" }).click();
}

/**
 * Hold the browser's return to the daemon: the redirect to
 * http://127.0.0.1:<port>/auth/callback is answered by the test (a stub page),
 * so the daemon never sees it, and the URL Atlas built is handed back.
 */
export async function holdCallback(context: BrowserContext, h: SigninHarness): Promise<{ url: Promise<string>; release: () => Promise<void> }> {
  const pattern = `http://127.0.0.1:${h.daemon!.port}/auth/callback**`;
  let resolve!: (u: string) => void;
  const url = new Promise<string>((r) => (resolve = r));
  await context.route(pattern, (route) => {
    resolve(route.request().url());
    return route.fulfill({ status: 200, contentType: "text/html", body: "<p>held by the test</p>" });
  });
  return { url, release: () => context.unroute(pattern) };
}

/** The bare code inside a pairing code (`http://localhost:8000/ABCD-EFGH`). */
export function codeOf(callbackURL: string): { pairingCode: string; code: string; state: string } {
  const u = new URL(callbackURL);
  const pairingCode = u.searchParams.get("pairing_code") || "";
  return { pairingCode, code: pairingCode.slice(pairingCode.lastIndexOf("/") + 1), state: u.searchParams.get("state") || "" };
}

/** Every main-frame URL a tab visits, and every request it made that did not
 *  go to Atlas's own web origin or the daemon — for "never left Atlas". */
export function watchTab(tab: Page, allowedOrigins: string[]): { navigations: string[]; offsite: string[] } {
  const out = { navigations: [tab.url()].filter((u) => u && u !== "about:blank"), offsite: [] as string[] };
  tab.on("framenavigated", (f) => {
    if (f === tab.mainFrame()) out.navigations.push(f.url());
  });
  tab.on("request", (r) => {
    try {
      if (!allowedOrigins.includes(new URL(r.url()).origin)) out.offsite.push(r.url());
    } catch {
      out.offsite.push(r.url());
    }
  });
  return out;
}
