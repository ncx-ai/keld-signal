import fs from "node:fs";
import path from "node:path";
import { test, expect } from "./support/fixtures";

/** Every request in the generated corpus, read straight off its transcripts:
 *  one per (session, request id), from its first line, as Claude Code writes
 *  a request's usage on every line of it. Independent of the daemon's reader. */
function corpusRequests(dir: string): { count: number; tokens: number; unnamed: number } {
  const seen = new Set<string>();
  let tokens = 0;
  let unnamed = 0;
  const walk = (d: string): string[] =>
    fs.readdirSync(d, { withFileTypes: true }).flatMap((e) => (e.isDirectory() ? walk(path.join(d, e.name)) : e.name.endsWith(".jsonl") ? [path.join(d, e.name)] : []));
  for (const file of walk(dir)) {
    for (const line of fs.readFileSync(file, "utf8").split("\n")) {
      if (!line.includes('"usage"')) continue;
      let r: any;
      try {
        r = JSON.parse(line);
      } catch {
        continue;
      }
      const u = r.type === "assistant" && r.message && r.message.usage;
      const id = r.requestId || (r.message && r.message.id);
      if (!u || !id || seen.has(`${r.sessionId}|${id}`)) continue;
      seen.add(`${r.sessionId}|${id}`);
      tokens += (u.input_tokens || 0) + (u.output_tokens || 0) + (u.cache_read_input_tokens || 0) + (u.cache_creation_input_tokens || 0);
      if (!r.message.model) unnamed++;
    }
  }
  return { count: seen.size, tokens, unnamed };
}

/** app.js's formatVolume, for comparing a tile against a count. */
function formatVolume(v: number): string {
  if (v >= 1e9) return `${(v / 1e9).toFixed(1)}B`;
  if (v >= 1e6) return `${(v / 1e6).toFixed(1)}M`;
  if (v >= 1e3) return `${(v / 1e3).toFixed(1)}K`;
  return `${v}`;
}

// The Overview, as a person sees it: Signal opens on it, it draws the range
// they last picked, and every figure on it comes from the same ledger the
// Focus blocks list does. Spec:
// docs/superpowers/specs/2026-09-28-signal-2c-overview-discovery.html.

test.describe("Overview", () => {
  test("Signal opens on the Overview, and Focus blocks is the old Today", async ({ signal, page }) => {
    await page.goto(`${signal.state.baseURL}/?secret=${encodeURIComponent(signal.state.secret)}`);
    await expect(page.locator("#topbarTitle")).toHaveText("Overview");
    const nav = page.getByRole("navigation", { name: "Panes" }).getByRole("link");
    await expect(nav.nth(0)).toHaveText("Overview");
    await expect(nav.nth(1)).toHaveText("Focus blocks");

    // A bookmark made before the rename still lands on the same pane.
    await signal.open("today");
    await expect(page.locator("#topbarTitle")).toHaveText("Focus blocks");
    await expect(signal.blockRows().first()).toBeVisible();
    await signal.open("focus");
    await expect(page.locator("#topbarTitle")).toHaveText("Focus blocks");
  });

  test("a first visit draws the last 7 days: four tiles, the chart and the histogram", async ({ signal, page }) => {
    await signal.open("overview");
    await expect(page.locator(".ov-title")).toHaveText("Last 7 days");
    const tiles = page.locator(".ov-tile");
    await expect(tiles).toHaveCount(4);
    await expect(tiles.nth(0).locator(".l")).toHaveText("Tokens");
    await expect(tiles.nth(0).locator(".v")).toContainText(/^\d+(\.\d)?[KMB]?/);
    await expect(tiles.nth(1).locator(".v")).toContainText(/^\$\d+\.\d{2}/);
    await expect(tiles.nth(3).locator(".v")).toContainText(/^\d+/);
    // The chart's summary carries "est." on its dollar figure, like every
    // dollar figure on the page.
    await expect(page.locator(".ov-summary")).toContainText(/\$\d+\.\d{2} est\./);
    await expect(page.locator(".ov-bars .ov-col")).toHaveCount(7);
    await expect(page.locator(".ov-hrow")).toHaveCount(7);
    await expect(page.locator(".ov-hrow").first().locator(".ov-cell")).toHaveCount(72);
  });

  test("hovering a tile shows what it is made of", async ({ signal, page }) => {
    await signal.open("overview");
    const tile = page.locator(".ov-tile").nth(2);
    await expect(tile.locator(".ov-breakdown")).toBeHidden();
    await tile.hover();
    await expect(tile.locator(".ov-breakdown")).toBeVisible();
    expect(await tile.locator(".ov-brow").count()).toBeGreaterThanOrEqual(1);
  });

  test("the range picked is the range Signal reopens on", async ({ signal, page }) => {
    await signal.open("overview");
    await page.getByRole("toolbar", { name: "Range" }).getByRole("button", { name: "1m" }).click();
    await expect(page.locator(".ov-title")).toHaveText("Last 30 days");
    await expect(page.locator(".ov-bars .ov-col")).toHaveCount(30);
    await page.reload();
    await expect(page.locator(".ov-title")).toHaveText("Last 30 days");
  });

  test("By repo splits the chart and the histogram by repository, with one legend", async ({ signal, page }) => {
    await signal.open("overview");
    const splits = page.getByRole("toolbar", { name: "Split" });
    await splits.getByRole("button", { name: "By repo" }).click();
    await expect(splits.getByRole("button", { name: "By repo" })).toHaveAttribute("aria-pressed", "true");
    const chartLegend = page.locator(".ov-chart .ov-legend span");
    // The spend line, plus at least one repository or "no repository".
    expect(await chartLegend.count()).toBeGreaterThanOrEqual(2);
    const chartKeys = (await chartLegend.allInnerTexts()).slice(1).map((t) => t.split(" · ")[0]);
    const histKeys = await page.locator(".ov-hist .ov-legend span").allInnerTexts();
    expect(histKeys).toEqual(chartKeys);
  });

  test("a range with nothing in it says so and draws no axes", async ({ signal, page }) => {
    await signal.open("overview");
    await page.getByRole("toolbar", { name: "Range" }).getByRole("button", { name: "Custom" }).click();
    await page.locator("#ovFrom").fill("2020-01-01");
    await page.locator("#ovTo").fill("2020-01-02");
    await expect(page.getByText("Nothing captured in this range.")).toBeVisible();
    await expect(page.locator(".ov-axis")).toHaveCount(0);
  });

  test("a range cut by the row cap says so, and shades what was not loaded", async ({ signal, page }) => {
    // The corpus holds ~21 blocks, so the cap is reached by answering the
    // Overview's own request (the only one carrying limit=2000) with 2000
    // blocks from the last three days. Focus blocks' request is untouched.
    const now = Math.floor(Date.now() / 1000);
    const blocks = Array.from({ length: 2000 }, (_, i) => ({
      key: { session: `s${i % 40}`, start: now - 3600 - i * 120 },
      end: now - 3600 - i * 120 + 60,
      source: "claude_code",
      start_reason: "idle",
      end_reason: "budget",
      cells: { measured: { status: "ok", tokens: { input: 1, output: 1, cache_read: 0, cache_creation: 0, request: 0 }, requests: 1, model: "m", estimate_usd: 0.01 } },
    }));
    await page.route(/\/v1\/ledger\?since=\d+&limit=2000$/, (route) =>
      route.fulfill({ json: { generated_at: new Date().toISOString(), health: [], blocks, pending: [] } })
    );
    // Usage that starts well before the range, so a capped column is one with
    // data whose blocks were not loaded — not one with no usage at all.
    await page.route(/\/v1\/usage\?since=\d+&until=\d+$/, (route) =>
      route.fulfill({ json: { bucket_seconds: 300, backfill_done: true, sources: { claude_code: { first_at: now - 30 * 86400 } },
        buckets: [{ at: now - 1800, source: "claude_code", transcript: "s0", model: "m", requests: 1, tokens: { input: 1, output: 1, cache_read: 0, cache_creation: 0 }, estimate_usd: 0.01 }] } })
    );
    await signal.open("overview");
    await expect(page.getByText(/oldest were not loaded/)).toBeVisible();
    // Tokens come from requests, which the cap does not touch; the splits read
    // off blocks are the ones it cuts.
    await page.getByRole("toolbar", { name: "Split" }).getByRole("button", { name: "By project" }).click();
    expect(await page.locator('.ov-col.not-loaded[title$="· not loaded"]').count()).toBeGreaterThanOrEqual(1);
  });

  test("a past range the row cap never reached says it was not loaded, not that it was empty", async ({ signal, page }) => {
    const now = Math.floor(Date.now() / 1000);
    const blocks = Array.from({ length: 2000 }, (_, i) => ({
      key: { session: "s", start: now - 3600 - i * 60 },
      end: now - 3600 - i * 60 + 30,
      source: "claude_code", start_reason: "idle", end_reason: "budget", cells: {},
    }));
    await page.route(/\/v1\/ledger\?since=\d+&limit=2000$/, (route) =>
      route.fulfill({ json: { generated_at: new Date().toISOString(), health: [], blocks, pending: [] } })
    );
    await signal.open("overview");
    await page.getByRole("toolbar", { name: "Range" }).getByRole("button", { name: "Custom" }).click();
    await page.locator("#ovFrom").fill("2026-01-01");
    await page.locator("#ovTo").fill("2026-01-07");
    await expect(page.getByText(/This range could not be loaded/)).toBeVisible();
    await expect(page.getByText("Nothing captured in this range.")).toHaveCount(0);
  });

  test("when Signal stops answering, the Overview shows the range as last loaded and says so", async ({ signal, page }) => {
    await signal.open("overview");
    await expect(page.locator(".ov-tile").first()).toBeVisible();
    // Only the Overview's own request fails, so this is the cached-range
    // path, not the whole-page "not running" one.
    await page.route(/\/v1\/ledger\?since=\d+&limit=2000$/, (route) => route.abort());
    await page.reload();
    await expect(page.getByText(/showing this range as it was last loaded/)).toBeVisible();
    await expect(page.locator(".ov-tile")).toHaveCount(4);
  });

  // T17: tokens are counted per request. The Overview's Tokens tile is the sum
  // of the corpus's own requests, the daemon's /v1/usage agrees with the
  // transcripts, and no request that names a model shows as "no model".
  test("T17: the Tokens tile is the corpus's requests, and none is 'no model'", async ({ signal, page, state }) => {
    const corpus = corpusRequests(path.join(state.work, "corpus"));
    expect(corpus.count).toBeGreaterThan(0);
    expect(corpus.unnamed).toBe(0);
    const now = Math.floor(Date.now() / 1000);
    const res = await page.request.get(`${state.baseURL}/v1/usage?since=0&until=${now + 86400}`, {
      headers: { "x-keld-agent-secret": state.secret },
    });
    expect(res.ok()).toBe(true);
    const usage = await res.json();
    const served = usage.buckets.reduce(
      (acc: { n: number; t: number }, b: any) => ({ n: acc.n + b.requests, t: acc.t + b.tokens.input + b.tokens.output + b.tokens.cache_read + b.tokens.cache_creation }),
      { n: 0, t: 0 }
    );
    expect(served).toEqual({ n: corpus.count, t: corpus.tokens });
    expect(usage.buckets.filter((b: any) => !b.model)).toHaveLength(0);

    await signal.open("overview");
    const tokensTile = page.locator(".ov-tile").nth(0);
    await expect(tokensTile.locator(".v")).toContainText(formatVolume(corpus.tokens));
    await tokensTile.hover();
    await expect(tokensTile.locator(".ov-breakdown")).toBeVisible();
    await expect(tokensTile.locator(".ov-blabel", { hasText: "no model" })).toHaveCount(0);
  });

  // "not attributed yet" is hidden for now (2026-09-30), not removed: the
  // category is still computed, and the page does not draw it. Showing it again
  // is the next PR, with the block-cutting fix.
  test("'not attributed yet' is not drawn in either legend", async ({ signal, page }) => {
    const now = Math.floor(Date.now() / 1000);
    const start = now - (now % 300) - 3600;
    const tokens = { input: 1, output: 1, cache_read: 0, cache_creation: 0, request: 0 };
    const blocks = [
      { key: { session: "a", start }, end: start + 1200, source: "claude_code", start_reason: "idle", end_reason: "budget",
        cells: { measured: { status: "ok", tokens, requests: 1, model: "m", estimate_usd: 0.01 } } }, // attribution never ran
      { key: { session: "b", start }, end: start + 1200, source: "claude_code", start_reason: "idle", end_reason: "budget",
        cells: { measured: { status: "ok", tokens, requests: 1, model: "m", estimate_usd: 0.01 }, attributed: { status: "failed", reason: "no_rule_matched" } } },
    ];
    await page.route(/\/v1\/ledger\?since=\d+&limit=2000$/, (route) =>
      route.fulfill({ json: { generated_at: new Date().toISOString(), health: [], blocks, pending: [] } })
    );
    await signal.open("overview");
    await page.getByRole("toolbar", { name: "Split" }).getByRole("button", { name: "By project" }).click();
    const legends = page.locator(".ov-legend");
    await expect(legends.first()).toContainText("no project");
    await expect(page.locator(".ov-legend", { hasText: "not attributed yet" })).toHaveCount(0);
  });

  // The Overview fills the window: the chart takes what the histogram leaves,
  // and the histogram sits at the bottom — one row for a day, taller for a week
  // and a month, its rows shortening rather than the page running past the
  // window. Decided with Gabriel, 2026-09-30.
  test("at 1440x900 every range fits the window, with the histogram at the bottom", async ({ signal, page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await signal.open("overview");
    const measure = () =>
      page.evaluate(() => {
        const pane = document.getElementById("paneRoot")!;
        const r = (s: string) => document.querySelector(s)!.getBoundingClientRect();
        return { scroll: pane.scrollHeight - pane.clientHeight, histBottom: r(".ov-hist").bottom, plot: r(".ov-plot").height, hist: r(".ov-hist").height };
      });
    const range = page.getByRole("toolbar", { name: "Range" });
    const seen: Record<string, { plot: number; hist: number }> = {};
    for (const [button, rows] of [["Today", 1], ["7d", 7], ["1m", 30]] as const) {
      await range.getByRole("button", { name: button }).click();
      await expect(page.locator(".ov-hrow")).toHaveCount(rows);
      const m = await measure();
      expect(m.scroll, `${button}: the pane scrolls`).toBeLessThanOrEqual(0);
      expect(900 - m.histBottom, `${button}: the histogram does not reach the bottom`).toBeLessThanOrEqual(24);
      seen[button] = m;
    }
    // The chart gets what the histogram does not need, and the histogram grows
    // with the range.
    expect(seen.Today.plot).toBeGreaterThan(seen["7d"].plot);
    expect(seen["7d"].plot).toBeGreaterThan(seen["1m"].plot);
    expect(seen.Today.hist).toBeLessThan(seen["7d"].hist);
    expect(seen["7d"].hist).toBeLessThan(seen["1m"].hist);
  });

  for (const width of [1280, 400]) {
    test(`no sideways scroll at ${width}px`, async ({ signal, page }) => {
      await page.setViewportSize({ width, height: 900 });
      await signal.open("overview");
      await expect(page.locator(".ov-tile").first()).toBeVisible();
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
      expect(overflow).toBeLessThanOrEqual(0);
    });
  }
});
