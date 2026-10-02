import fs from "node:fs";
import path from "node:path";
import type { Page, Request } from "@playwright/test";
import {
  ADMIN,
  ATLAS_API,
  ATLAS_WEB,
  SIGNED_IN,
  SIGNED_IN_TAB,
  VIEWER,
  atlasSession,
  authState,
  codeOf,
  continueAs,
  daemonCall,
  expect,
  expectNotPaired,
  expectPairedAs,
  fillAtlasLogin,
  holdCallback,
  openAuthorizeTab,
  pairingFiles,
  signupCode,
  startSignin,
  test,
  verifyLinkFor,
  watchTab,
} from "./atlas";
import type { SigninHarness } from "../support/signin-harness";

// The web sign-in (docs/superpowers/specs/2026-09-29-signal-web-signin-discovery.html,
// contracts C1–C6 of docs/superpowers/plans/2026-09-29-signal-web-signin-plan.md)
// end to end against the REAL local Atlas: a real throwaway `keld-agent run`,
// a real browser, Atlas's real login form, authorize page and enroll route.
// Every case asserts what landed on disk in the daemon's KELD_HOME and what the
// page shows, not only status codes.

const WELCOME = { name: "Welcome to Signal" } as const;

/** Every navigation and every POST to Atlas's mint route, context-wide — the
 *  authorize tab is opened by a click, so a per-tab listener would miss its
 *  first request. */
function recordContext(page: Page): { navigations: string[]; mints: string[]; callbacks: string[] } {
  const out = { navigations: [] as string[], mints: [] as string[], callbacks: [] as string[] };
  page.context().on("request", (r: Request) => {
    const u = r.url();
    if (r.isNavigationRequest()) out.navigations.push(u);
    if (r.method() === "POST" && /\/api\/cli\/authorize$/.test(new URL(u).pathname)) out.mints.push(u);
    if (/^http:\/\/127\.0\.0\.1:\d+\/auth\/callback/.test(u)) out.callbacks.push(u);
  });
  return out;
}

function mtimes(h: SigninHarness): Record<string, number | null> {
  const out: Record<string, number | null> = {};
  for (const n of ["hook.json", "auth.json"]) {
    const p = path.join(h.home, n);
    out[n] = fs.existsSync(p) ? fs.statSync(p).mtimeMs : null;
  }
  return out;
}

async function restartWith(h: SigninHarness, patch: Record<string, unknown>): Promise<void> {
  await h.stopDaemon();
  const cfgPath = path.join(h.home, "agent-config.json");
  fs.writeFileSync(cfgPath, JSON.stringify({ ...JSON.parse(fs.readFileSync(cfgPath, "utf8")), ...patch }) + "\n");
  await h.startDaemon();
}

test.describe("Web sign-in against the real local Atlas", () => {
  test("01 happy path: first open → Sign in → Atlas email login → Continue → signed in within 5 s, ingest token live", async ({ page, harness }) => {
    const seen = recordContext(page);
    await page.goto(harness.pageURL("today"));
    await expect(page.getByRole("heading", WELCOME)).toBeVisible();

    const href = await startSignin(page);
    const u = new URL(href);
    expect(u.searchParams.get("redirect_uri")).toBe(`${harness.daemon!.baseURL}/auth/callback`);
    expect(u.searchParams.get("code_challenge_method")).toBe("S256");

    const tab = await openAuthorizeTab(page);
    await expect(tab, "a signed-out browser is sent to Atlas's login").toHaveURL(/\/login\?next=/);
    await fillAtlasLogin(tab, ADMIN);
    await continueAs(tab, ADMIN);
    await expect(tab.getByText(SIGNED_IN_TAB)).toBeVisible();
    expect(seen.mints, "exactly one grant minted, by the Continue click").toHaveLength(1);

    // AC-5: the page says so within 5 s, no reload, with principal + org.
    await expect(page.getByText(`Signed in as ${ADMIN.email} · ${ADMIN.org}`)).toBeVisible({ timeout: 5_000 });
    await expect(page.getByRole("heading", WELCOME)).toHaveCount(0);

    await expectPairedAs(harness, ADMIN);
    const st = await authState(harness);
    expect(st).toMatchObject({ paired: true, principal: ADMIN.email, org: ADMIN.org, first_run: false, last_error: null });

    await page.goto(harness.pageURL("settings"));
    await expect(page.locator(".tile", { hasText: "Atlas account" }).getByText(`Signed in as ${ADMIN.email} · ${ADMIN.org}`)).toBeVisible();
  });

  test("02 already signed in to Atlas: the authorize page shows Continue at once, no login form, and pairs", async ({ page, harness }) => {
    await atlasSession(page.context(), ADMIN);
    const seen = recordContext(page);
    await page.goto(harness.pageURL("today"));
    await startSignin(page);
    const tab = await openAuthorizeTab(page);
    await continueAs(tab, ADMIN);
    await expect(tab.getByText(SIGNED_IN_TAB)).toBeVisible();
    expect(
      seen.navigations.filter((n) => new URL(n).pathname === "/login"),
      "a signed-in browser must not be sent through /login"
    ).toEqual([]);
    await expect(page.getByText(SIGNED_IN)).toBeVisible({ timeout: 5_000 });
    await expectPairedAs(harness, ADMIN);
  });

  test("03 signed out: authorize → /login?next=… → after login, back on the SAME authorize URL (same state) → pairs", async ({ page, harness }) => {
    await page.goto(harness.pageURL("today"));
    const href = await startSignin(page);
    const want = new URL(href);

    const tab = await openAuthorizeTab(page);
    await expect(tab).toHaveURL(/\/login\?next=/);
    const next = new URL(tab.url()).searchParams.get("next");
    expect(next, "login's next is the authorize path + query, whole").toBe(want.pathname + want.search);

    await fillAtlasLogin(tab, ADMIN);
    await expect(tab.getByText(`Continue as ${ADMIN.email}`)).toBeVisible({ timeout: 15_000 });
    const back = new URL(tab.url());
    expect(back.origin + back.pathname).toBe(want.origin + want.pathname);
    for (const k of ["redirect_uri", "state", "code_challenge", "code_challenge_method"]) {
      expect(back.searchParams.get(k), `${k} survived the login round trip`).toBe(want.searchParams.get(k));
    }
    expect(tab.url(), "the very same authorize URL").toBe(href);

    await continueAs(tab, ADMIN);
    await expect(tab.getByText(SIGNED_IN_TAB)).toBeVisible();
    await expect(page.getByText(SIGNED_IN)).toBeVisible({ timeout: 5_000 });
    await expectPairedAs(harness, ADMIN);
  });

  test("04 Cancel on Continue: nothing minted, Signal stays signed out; trying again afterwards pairs", async ({ page, harness }) => {
    await atlasSession(page.context(), ADMIN);
    const seen = recordContext(page);
    await page.goto(harness.pageURL("today"));
    const first = await startSignin(page);
    const tab = await openAuthorizeTab(page);
    await expect(tab.getByText(`Continue as ${ADMIN.email}`)).toBeVisible({ timeout: 15_000 });
    await tab.getByRole("button", { name: "Cancel" }).click();
    await expect(tab.getByText("Signal will stay signed out. You can close this tab.")).toBeVisible();

    // A few polls later: no mint, no return, nothing written, still waiting.
    await page.waitForTimeout(2_500);
    expect(seen.mints, "Cancel must not call the mint route").toEqual([]);
    expect(seen.callbacks, "Cancel must not send the browser back to Signal").toEqual([]);
    expectNotPaired(harness, "after Cancel");
    expect(await authState(harness)).toMatchObject({ paired: false, first_run: true });
    await expect(page.getByText(SIGNED_IN)).toHaveCount(0);
    await tab.close();

    // Try again: a fresh start from the page, a fresh state, and it pairs.
    await page.reload();
    await expect(page.getByRole("heading", WELCOME)).toBeVisible();
    const second = await startSignin(page);
    expect(new URL(second).searchParams.get("state")).not.toBe(new URL(first).searchParams.get("state"));
    const tab2 = await openAuthorizeTab(page);
    await continueAs(tab2, ADMIN);
    await expect(tab2.getByText(SIGNED_IN_TAB)).toBeVisible();
    await expect(page.getByText(SIGNED_IN)).toBeVisible({ timeout: 5_000 });
    expect(seen.mints).toHaveLength(1);
    await expectPairedAs(harness, ADMIN);
  });

  for (const hostile of ["https://evil.example/auth/callback", "http://127.0.0.1.evil.example:5000/auth/callback"]) {
    test(`05 hostile redirect_uri ${hostile}: "This sign-in link is not valid.", no Continue, never leaves Atlas`, async ({ page, harness }) => {
      await atlasSession(page.context(), ADMIN);
      const seen = recordContext(page);
      const start = await daemonCall(harness, "POST", "/v1/auth/start", {});
      expect(start.status, JSON.stringify(start.body)).toBe(200);
      const u = new URL(start.body.authorize_url);
      u.searchParams.set("redirect_uri", hostile);

      const tab = await page.context().newPage();
      const watched = watchTab(tab, [ATLAS_WEB]);
      await tab.goto(u.toString());
      await expect(tab.getByText("This sign-in link is not valid.")).toBeVisible({ timeout: 15_000 });
      await expect(tab.getByRole("button", { name: "Continue" })).toHaveCount(0);
      await tab.waitForTimeout(1_000);

      expect(watched.navigations.every((n) => new URL(n).origin === ATLAS_WEB), `tab navigated to: ${watched.navigations.join(", ")}`).toBe(true);
      expect(new URL(tab.url()).origin).toBe(ATLAS_WEB);
      expect(watched.offsite.filter((r) => /evil/.test(r)), "no request went to the hostile host").toEqual([]);
      expect(seen.mints, "a hostile link must never reach the mint route").toEqual([]);
      expectNotPaired(harness, "after a hostile link");
    });
  }

  test("06 forged return with an unknown state: refusal page, nothing written; the real sign-in started just before still completes", async ({ page, harness }) => {
    await page.goto(harness.pageURL("today"));
    await startSignin(page);

    const forged = await page.context().newPage();
    const fakeState = "A".repeat(43);
    const res = await forged.goto(
      `${harness.daemon!.baseURL}/auth/callback?pairing_code=${encodeURIComponent(`${ATLAS_API}/FORG-EDXX`)}&state=${fakeState}`
    );
    expect(res!.status()).toBe(400);
    const html = await res!.text();
    expect(html, "nothing from the URL is echoed").not.toContain("FORG-EDXX");
    expect(html).not.toContain(fakeState);
    await expect(forged.getByText("This sign-in was not started here. Start again from Signal.")).toBeVisible();
    expectNotPaired(harness, "after a forged return");
    await forged.close();

    // The real one is still waiting, then completes against the real Atlas.
    await page.waitForTimeout(2_000);
    await expect(page.getByText("Finish signing in in your browser")).toBeVisible();
    await expect(page.getByRole("button", { name: "Try again" })).toHaveCount(0);
    const tab = await openAuthorizeTab(page);
    await fillAtlasLogin(tab, ADMIN);
    await continueAs(tab, ADMIN);
    await expect(tab.getByText(SIGNED_IN_TAB)).toBeVisible();
    await expect(page.getByText(SIGNED_IN)).toBeVisible({ timeout: 5_000 });
    await expectPairedAs(harness, ADMIN);
  });

  test("07 replay: the same real callback URL opened a second time is refused and changes no file", async ({ page, harness }) => {
    await atlasSession(page.context(), ADMIN);
    await page.goto(harness.pageURL("today"));
    await startSignin(page);
    const tab = await openAuthorizeTab(page);
    await continueAs(tab, ADMIN);
    await expect(tab.getByText(SIGNED_IN_TAB)).toBeVisible();
    const callback = tab.url();
    expect(callback.startsWith(`${harness.daemon!.baseURL}/auth/callback?`), `landed on ${callback}`).toBe(true);
    expect(codeOf(callback).pairingCode.startsWith(`${ATLAS_API}/`)).toBe(true);
    await expectPairedAs(harness, ADMIN);

    const before = { files: pairingFiles(harness), mtimes: mtimes(harness) };
    const replay = await page.context().newPage();
    const res = await replay.goto(callback);
    expect(res!.status()).toBe(400);
    await expect(replay.getByText("This sign-in was not started here. Start again from Signal.")).toBeVisible();
    await expect(replay.getByText(SIGNED_IN_TAB)).toHaveCount(0);
    expect(pairingFiles(harness), "replay rewrote hook.json/auth.json").toEqual(before.files);
    expect(mtimes(harness), "replay touched hook.json/auth.json").toEqual(before.mtimes);
    expect(await authState(harness)).toMatchObject({ paired: true, principal: ADMIN.email });
  });

  test("08 verifier: a real browser code redeemed without code_verifier is 410 and burned; the daemon then gets \"That code expired\"", async ({ page, harness }) => {
    await atlasSession(page.context(), ADMIN);
    await page.goto(harness.pageURL("today"));
    await startSignin(page);
    const held = await holdCallback(page.context(), harness);
    const tab = await openAuthorizeTab(page);
    await continueAs(tab, ADMIN);
    const callback = await held.url;
    await held.release();
    const { code, pairingCode } = codeOf(callback);
    expect(pairingCode.startsWith(`${ATLAS_API}/`), pairingCode).toBe(true);
    expectNotPaired(harness, "while the return is held");

    // The thief's move: redeem the lifted code with no verifier.
    const stolen = await fetch(`${ATLAS_API}/v1/cli/enroll`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ code }),
    });
    expect(stolen.status, `enroll without verifier answered ${stolen.status}: ${await stolen.clone().text()}`).toBe(410);

    // The real return now arrives: the code is gone, so nothing is written.
    const late = await page.context().newPage();
    const res = await late.goto(callback);
    expect(res!.status()).toBe(410);
    await expect(late.getByText("That code expired. Start again from Signal.")).toBeVisible();
    expectNotPaired(harness, "after the burned code came back");
    await expect(page.getByText("That sign-in expired before it finished.")).toBeVisible({ timeout: 5_000 });
    await expect(page.getByRole("button", { name: "Try again" })).toBeVisible();
    expect(await authState(harness)).toMatchObject({ paired: false, last_error: "expired" });
  });

  test("09 wrong Atlas: a daemon started against 127.0.0.1:8000 refuses the real code naming localhost:8000", async ({ page, harness }) => {
    const other = ATLAS_API.replace("//localhost", "//127.0.0.1");
    test.skip(other === ATLAS_API, `KELD_E2E_ATLAS_API (${ATLAS_API}) is not a localhost URL, so there is no second spelling to start against`);
    await harness.stopDaemon();
    harness.useAtlas(other, ATLAS_WEB);
    await harness.startDaemon();

    await atlasSession(page.context(), ADMIN);
    await page.goto(harness.pageURL("today"));
    await startSignin(page);
    const tab = await openAuthorizeTab(page);
    await continueAs(tab, ADMIN);
    await expect(tab.getByText("This sign-in came back from a different Atlas than the one Signal asked.", { exact: false })).toBeVisible();
    expect(codeOf(tab.url()).pairingCode.startsWith(`${ATLAS_API}/`), "Atlas named itself as localhost").toBe(true);
    expectNotPaired(harness, "after a return from a different Atlas");
    await expect(page.getByText("The browser came back from a different Atlas than the one this sign-in started with.")).toBeVisible({ timeout: 5_000 });
    expect(await authState(harness)).toMatchObject({ paired: false, last_error: "atlas_mismatch" });
  });

  test("10 Send to Atlas off: /v1/auth/start answers 409, and a return that arrives while it is off is refused", async ({ page, harness }) => {
    await atlasSession(page.context(), ADMIN);
    await page.goto(harness.pageURL("today"));
    // A sign-in begun while Atlas is on, its return held in flight…
    await startSignin(page);
    const held = await holdCallback(page.context(), harness);
    const tab = await openAuthorizeTab(page);
    await continueAs(tab, ADMIN);
    const callback = await held.url;
    await held.release();

    // …then the person chooses local only on the page.
    await page.getByRole("button", { name: "Use locally only" }).click();
    await expect(page.getByRole("heading", WELCOME)).toHaveCount(0);
    await expect.poll(() => harness.readJSON("agent-config.json")?.send_to_atlas).toBe(false);

    const start = await daemonCall(harness, "POST", "/v1/auth/start", {});
    expect(start.status).toBe(409);
    expect(start.body).toEqual({ error: "send_to_atlas_is_off" });

    const late = await page.context().newPage();
    const res = await late.goto(callback);
    expect(res!.status()).toBe(409);
    await expect(late.getByText("Send to Atlas is off, so Signal did not sign in.", { exact: false })).toBeVisible();
    expectNotPaired(harness, "after a return while Send to Atlas is off");
    expect(await authState(harness)).toMatchObject({ paired: false, last_error: "atlas_off" });
  });

  test("11 first-run local only survives a daemon restart; Settings' Sign in then completes against the real Atlas", async ({ page, harness }) => {
    await page.goto(harness.pageURL("today"));
    await page.getByRole("button", { name: "Use locally only" }).click();
    await expect(page.getByRole("heading", WELCOME)).toHaveCount(0);
    await expect.poll(() => harness.readJSON("agent-config.json")?.send_to_atlas).toBe(false);

    await harness.stopDaemon();
    await harness.startDaemon();
    expect(harness.readJSON("agent-config.json")?.send_to_atlas, "the choice is still on disk after the restart").toBe(false);
    expect(await authState(harness)).toMatchObject({ first_run: false, paired: false });
    await page.goto(harness.pageURL("today"));
    await expect(page.getByText("Loading…")).toBeHidden();
    await expect(page.getByRole("heading", WELCOME)).toHaveCount(0);

    await page.goto(harness.pageURL("settings"));
    const tile = page.locator(".tile", { hasText: "Atlas account" });
    await expect(tile.getByText("Signing in turns Send to Atlas on.")).toBeVisible();
    await startSignin(page, tile);
    await expect.poll(() => harness.readJSON("agent-config.json")?.send_to_atlas).toBe(true);
    const tab = await openAuthorizeTab(page);
    await fillAtlasLogin(tab, ADMIN);
    await continueAs(tab, ADMIN);
    await expect(tab.getByText(SIGNED_IN_TAB)).toBeVisible();
    await expect(tile.getByText(`Signed in as ${ADMIN.email} · ${ADMIN.org}`)).toBeVisible({ timeout: 5_000 });
    await expectPairedAs(harness, ADMIN);
  });

  // The raw query each case puts after `next=`, and what it decodes to.
  const hostileNext: [string, string][] = [
    ["/\\evil.com", "/\\evil.com"],
    ["%2F%5Cevil.com", "/\\evil.com"],
    ["/%2F%2Fevil.com", "///evil.com"],
    ["%2F%252F%252Fevil.com", "/%2F%2Fevil.com"],
  ];
  test("12 /login?next= open-redirect bypasses: after signing in the browser stays on Atlas", async ({ page }) => {
    test.skip(!ATLAS_WEB, "KELD_E2E_ATLAS_WEB is not set");
    const evil: string[] = [];
    page.context().on("request", (r) => {
      if (/evil/.test(new URL(r.url()).hostname)) evil.push(r.url());
    });
    await page.context().route(/^https?:\/\/(?!127\.0\.0\.1[:/]|localhost[:/])/, (route) => route.abort());
    for (const [raw, decoded] of hostileNext) {
      await page.context().clearCookies();
      await page.goto(`${ATLAS_WEB}/login?next=${raw}`);
      expect(new URL(page.url()).searchParams.get("next"), `next=${raw} as the page reads it`).toBe(decoded);
      await fillAtlasLogin(page, ADMIN);
      await expect
        .poll(() => (evil.length ? `EVIL ${evil.join(",")}` : new URL(page.url()).pathname), { message: `next=${raw}: where the browser went`, timeout: 15_000 })
        .not.toBe("/login");
      expect(evil, `next=${raw} sent the browser off Atlas`).toEqual([]);
      expect(new URL(page.url()).origin, `next=${raw}`).toBe(ATLAS_WEB);
    }
  });

  // 13 was the setup-code box in Settings (AC-8). The box and POST /v1/config
  // were removed: a setup code still pairs from a terminal (keld login --code,
  // keld-agent install --code), which this browser suite does not drive.

  test("14 a viewer (sarah@acme.test) signs in and gets her own principal", async ({ page, harness }) => {
    await page.goto(harness.pageURL("today"));
    await startSignin(page);
    const tab = await openAuthorizeTab(page);
    await fillAtlasLogin(tab, VIEWER);
    await continueAs(tab, VIEWER);
    await expect(tab.getByText(SIGNED_IN_TAB)).toBeVisible();
    await expect(page.getByText(`Signed in as ${VIEWER.email} · ${VIEWER.org}`)).toBeVisible({ timeout: 5_000 });
    await expectPairedAs(harness, VIEWER);
    expect(await authState(harness)).toMatchObject({ paired: true, principal: VIEWER.email });
  });

  test("15 a brand-new email signup from Signal's sign-in: verify link → back on Continue (not /onboarding) → signed in as that email", async ({ page, harness }) => {
    const code = signupCode();
    test.skip(!code, "no KELD_SIGNUP_CODE in the Atlas api container (or docker cannot reach it); set KELD_E2E_ATLAS_API_CONTAINER");
    // A real org and user are created in the local Atlas: unique per run, never reused.
    const stamp = `${Date.now()}`;
    const who = { email: `signal-e2e-${stamp}@signup.test`, password: "signalE2e2026", org: `Signal E2E ${stamp}` };
    const seen = recordContext(page);

    await page.goto(harness.pageURL("today"));
    const href = await startSignin(page);
    const tab = await openAuthorizeTab(page);
    await expect(tab).toHaveURL(/\/login\?next=/);

    // Login → Sign up keeps the way back, and so must the form it leads to.
    // Typed into before hydration, the form is rebuilt and submits as empty. The signup page's
    // own OAuth-buttons fetch runs in an effect, so its answer means hydrated. (Login makes the
    // same fetch, hence the referer.)
    const hydrated = tab.waitForResponse((r) =>
      new URL(r.url()).pathname === "/api/auth/oauth/providers" && /\/signup\?/.test(r.request().headers()["referer"] || ""));
    const signUp = tab.getByRole("link", { name: "Sign up" });
    // Server-rendered it is a bare /signup; login adds ?next= once hydrated.
    await expect(signUp, "login's Sign up link carries the way back").toHaveAttribute("href", /^\/signup\?next=/);
    await signUp.click();
    await expect(tab).toHaveURL(/\/signup\?next=/);
    await hydrated;
    const want = new URL(href);
    expect(new URL(tab.url()).searchParams.get("next"), "signup's next is the authorize path + query, whole").toBe(want.pathname + want.search);
    await tab.locator("#org").fill(who.org);
    await tab.locator("#name").fill("Signal E2E");
    await tab.locator("#email").fill(who.email);
    await tab.locator("#password").fill(who.password);
    await tab.locator("#code").fill(code!);
    await tab.getByRole("button", { name: "Sign up", exact: true }).click();
    await expect(tab.getByText("Check your email")).toBeVisible();
    expectNotPaired(harness, "before the email is verified");

    // The "email": Atlas's console sender prints the link instead of sending it.
    let link: string | null = null;
    await expect.poll(() => (link = verifyLinkFor(who.email)), { message: `no verify link printed for ${who.email}`, timeout: 15_000 }).not.toBeNull();
    const verify = new URL(link!);
    expect(`${verify.origin}${verify.pathname}`, "the emailed link is Atlas's verify page").toBe(`${ATLAS_WEB}/signup/verify`);
    expect([...verify.searchParams.keys()], "the emailed link carries the token and nothing else").toEqual(["token"]);

    await tab.goto(link!);
    await expect(tab, "verifying lands back on the very same authorize URL").toHaveURL(href, { timeout: 15_000 });
    await continueAs(tab, who);
    await expect(tab.getByText(SIGNED_IN_TAB)).toBeVisible();
    expect(
      seen.navigations.filter((n) => new URL(n).pathname === "/onboarding"),
      "verifying must return to Signal's sign-in, not start onboarding"
    ).toEqual([]);
    expect(seen.mints, "exactly one grant minted, by the Continue click").toHaveLength(1);

    await expect(page.getByText(`Signed in as ${who.email} · ${who.org}`)).toBeVisible({ timeout: 5_000 });
    await expectPairedAs(harness, who);
    expect(await authState(harness)).toMatchObject({ paired: true, principal: who.email, org: who.org });
  });
});
