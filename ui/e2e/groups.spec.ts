import http from "node:http";
import net from "node:net";
import fs from "node:fs";
import path from "node:path";
import { test as base, expect } from "@playwright/test";

// A block in several projects, across groups and inside one (2026-09-23).
//
// The e2e daemon runs with Atlas off and one local group, so a two-group org is
// served from a fixture here: the page's own files, and /v1/* answered with the
// exact shapes the daemon produces (the ledger's `projects` list, the
// catalog's `groups`/`projects`/`totals`). The DOM under test is the real
// page; only the data is fixed.
//
// The org: Products {Atlas Platform, Signal Client} and Features {Billing}.
// Block X ($5) is in Atlas Platform AND Signal Client AND Billing; block Y ($5)
// is in Atlas Platform only.

const REPO_ROOT = path.resolve(__dirname, "..", "..");
const UI_DIR = path.join(REPO_ROOT, "internal", "agent", "ui");
const TYPES: Record<string, string> = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".svg": "image/svg+xml",
  ".png": "image/png",
};

function todayAt(h: number, m: number): number {
  const d = new Date();
  d.setHours(h, m, 0, 0);
  return Math.floor(d.getTime() / 1000);
}

const X = todayAt(9, 0);
const Y = todayAt(10, 0);

function block(session: string, start: number, projects: object[]) {
  return {
    key: { session, start },
    end: start + 1200,
    start_reason: "session_start",
    end_reason: "budget",
    source: "claude_code",
    cells: {
      cut: { status: "ok", at: new Date(start * 1000).toISOString() },
      measured: {
        status: "ok", at: new Date(start * 1000).toISOString(),
        tokens: { input: 60, output: 40, cache_read: 0, cache_creation: 0, request: 0 },
        requests: 3, model: "claude-opus-5", estimate_usd: 5,
      },
      attributed: { status: "ok", at: new Date(start * 1000).toISOString(), projects },
    },
  };
}

const LEDGER = {
  generated_at: new Date().toISOString(),
  blocks: [
    block("sess-y", Y, [{ project_id: "products:atlas", group: "products", method: "repo" }]),
    block("sess-x", X, [
      { project_id: "products:atlas", group: "products", method: "repo" },
      { project_id: "products:signal", group: "products", method: "repo" },
      { project_id: "features:billing", group: "features", method: "ticket" },
    ]),
  ],
  health: [],
};

const ws = (id: string, title: string, group: string, repo: string) => ({
  id, title, group, repos: [repo], rules: [repo], origin: "user",
});

const CATALOG = {
  groups: [
    { key: "products", name: "Products", origin: "local" },
    { key: "features", name: "Features", origin: "local" },
  ],
  projects: [
    ws("products:atlas", "Atlas Platform", "products", "github.com/ncx-ai/keld-atlas"),
    ws("products:signal", "Signal Client", "products", "github.com/ncx-ai/keld-atlas"),
    { ...ws("features:billing", "Billing", "features", "github.com/ncx-ai/keld-billing"), ticket_key: "BILL" },
  ],
  suggestions: [],
  coverage: { attributed: 2, total: 2, since: new Date().toISOString() },
  totals: {
    groups: [
      { key: "features", blocks: 1, minutes: 20, tokens: 100, usd: 5, shared_blocks: 0 },
      { key: "products", blocks: 2, minutes: 40, tokens: 200, usd: 10, shared_blocks: 1 },
    ],
    projects: [
      { id: "features:billing", group: "features", blocks: 1, minutes: 20, tokens: 100, usd: 5 },
      { id: "products:atlas", group: "products", blocks: 2, minutes: 40, tokens: 200, usd: 10 },
      { id: "products:signal", group: "products", blocks: 1, minutes: 20, tokens: 100, usd: 5 },
    ],
  },
};

type Shell = { url: string; close: () => Promise<void> };

async function startShell(): Promise<Shell> {
  const json = (res: http.ServerResponse, body: unknown, status = 200) => {
    res.writeHead(status, { "Content-Type": "application/json" });
    res.end(JSON.stringify(body));
  };
  const server = http.createServer((req, res) => {
    const p = new URL(req.url || "/", "http://127.0.0.1").pathname;
    if (p === "/v1/ledger") return json(res, LEDGER);
    if (p === "/v1/settings") return json(res, { send_to_atlas: false });
    if (p === "/v1/projects") return json(res, CATALOG);
    if (p.startsWith("/v1/")) return json(res, {}, 404);
    const file = p === "/" ? "index.html" : p.slice(1);
    const full = path.join(UI_DIR, file);
    if (!full.startsWith(UI_DIR) || !fs.existsSync(full)) {
      res.writeHead(404);
      res.end();
      return;
    }
    res.writeHead(200, { "Content-Type": TYPES[path.extname(full)] || "application/octet-stream" });
    fs.createReadStream(full).pipe(res);
  });
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const port = (server.address() as net.AddressInfo).port;
  return { url: `http://127.0.0.1:${port}`, close: () => new Promise((r) => server.close(() => r())) };
}

const test = base.extend<{ shell: Shell }>({
  shell: async ({}, use) => {
    const s = await startShell();
    await use(s);
    await s.close();
  },
});

function rowFor(page: any, start: number) {
  // The row's first cell holds the block's own time range.
  const hhmm = (s: number) => {
    const d = new Date(s * 1000);
    return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
  };
  return page.locator(".blocks-table tbody tr", { hasText: hhmm(start) });
}

test.describe("Groups: a block in several projects", () => {
  test("Today shows every project a block holds, and the switcher filters to one group", async ({ page, shell }) => {
    await page.goto(`${shell.url}/?secret=irrelevant#/today`);

    const tabs = page.getByRole("tab");
    await expect(tabs).toHaveText(["All groups", "Products", "Features"]);
    await expect(page.getByRole("tab", { name: "All groups" })).toHaveAttribute("aria-selected", "true");

    const x = rowFor(page, X);
    await expect(x.locator(".ws-pill")).toHaveCount(3);
    await expect(x).toContainText("Atlas Platform");
    await expect(x).toContainText("Signal Client");
    await expect(x).toContainText("Billing");

    // Products: X holds TWO projects in this one group, and shows both.
    await page.getByRole("tab", { name: "Products" }).click();
    await expect(x.locator(".ws-pill")).toHaveCount(2);
    await expect(x).not.toContainText("Billing");

    // Features: Y is not in Features at all — an honest "no project".
    await page.getByRole("tab", { name: "Features" }).click();
    await expect(x.locator(".ws-pill")).toHaveCount(1);
    await expect(x).toContainText("Billing");
    await expect(rowFor(page, Y)).toContainText("no project");

    // The choice is remembered for this viewer.
    await page.reload();
    await expect(page.getByRole("tab", { name: "Features" })).toHaveAttribute("aria-selected", "true");
  });

  test("the Projects pane totals each group once and says why its projects add up to more", async ({ page, shell }) => {
    await page.goto(`${shell.url}/?secret=irrelevant#/projects`);

    const products = page.locator(".group-card", { hasText: "Products" });
    await expect(products.locator(".group-total")).toHaveText(/^2 blocks · .+ · \$10\.00 est\.$/);
    await expect(products.locator(".shared-note")).toHaveText(
      "1 block is in more than one project here, so the projects add up to more than the group."
    );
    // Matched on the row's TITLE: every row now carries a Map-to picker whose
    // <option>s name the other projects, so a row-wide text match would find
    // "Atlas Platform" in Signal Client's row too.
    const row = (title: string) =>
      products.locator(".project-row", { has: page.locator(".row-title", { hasText: title }) });
    await expect(row("Atlas Platform")).toContainText("$10.00 est.");
    await expect(row("Signal Client")).toContainText("$5.00 est.");

    const features = page.locator(".group-card", { hasText: "Features" });
    await expect(features.locator(".group-total")).toHaveText(/^1 block · .+ · \$5\.00 est\.$/);
    await expect(features.locator(".shared-note")).toHaveCount(0);
  });

  test("NEGATIVE: nothing on either pane asks a person to pick one", async ({ page, shell }) => {
    for (const pane of ["today", "projects"]) {
      await page.goto(`${shell.url}/?secret=irrelevant#/${pane}`);
      await expect(page.locator("main, body").first()).not.toContainText(/pick one/i);
      await expect(page.locator(".pill.no", { hasText: "conflict" })).toHaveCount(0);
    }
  });

  test("renders at desktop and phone width (screenshots for the review)", async ({ page, shell }, info) => {
    for (const [w, h] of [[1280, 900], [400, 900]]) {
      await page.setViewportSize({ width: w, height: h });
      for (const pane of ["today", "projects"]) {
        await page.goto(`${shell.url}/?secret=irrelevant#/${pane}`);
        await expect(page.getByRole("tab").first().or(page.locator(".group-card").first())).toBeVisible();
        // No horizontal page scroll at phone width.
        const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        expect(overflow, `${pane} at ${w}px scrolls sideways`).toBeLessThanOrEqual(1);
        await page.screenshot({ path: info.outputPath(`groups-${pane}-${w}.png`), fullPage: true });
      }
    }
  });
});
