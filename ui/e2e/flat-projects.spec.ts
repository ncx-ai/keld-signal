import http from "node:http";
import net from "node:net";
import fs from "node:fs";
import path from "node:path";
import { test as base, expect } from "@playwright/test";

// Revision 4 — only projects, no groups (R4-AC-1).
//
// The Projects page is ONE flat list. A block lands in every project that
// matches it, and each project's total counts it in full; the Attributed tile
// counts it once. Served from a fixture in the shape the Revision 4 daemon
// produces — GET /v1/projects has no `groups` key and no project has a
// `group` — so this spec does not depend on the daemon under test having that
// backend yet. The DOM under test is the real page; only the data is fixed.
//
// The machine: Atlas Platform, Signal Client, Billing, and one hidden project.
// Block X ($5) is in Atlas Platform AND Signal Client; block Y ($5) is in Atlas
// Platform only, and is an OLD row whose entry still names a group.

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
  const at = new Date(start * 1000).toISOString();
  return {
    key: { session, start },
    end: start + 1200,
    start_reason: "session_start",
    end_reason: "budget",
    source: "claude_code",
    cells: {
      cut: { status: "ok", at },
      measured: {
        status: "ok", at,
        tokens: { input: 60, output: 40, cache_read: 0, cache_creation: 0, request: 0 },
        requests: 3, model: "claude-opus-5", estimate_usd: 5,
      },
      attributed: { status: "ok", at, projects },
    },
  };
}

const LEDGER = {
  generated_at: new Date().toISOString(),
  blocks: [
    // An old row: written before Revision 4, it still names a group. Ignored.
    block("sess-y", Y, [{ project_id: "p_atlas", group: "products", method: "repo" }]),
    block("sess-x", X, [
      { project_id: "p_atlas", method: "repo" },
      { project_id: "p_signal", method: "repo" },
    ]),
  ],
  health: [],
};

const project = (id: string, title: string, repo: string, extra: object = {}) => ({
  id, title, repos: [repo], rules: [repo], origin: "user", hidden: false, ...extra,
});

const SUGGESTION = {
  id: "repo:github.com/ncx-ai/keld-website", kind: "repo", value: "github.com/ncx-ai/keld-website",
  blocks: 3, minutes: 45, tokens: 12000,
};

const CATALOG = {
  projects: [
    project("p_atlas", "Atlas Platform", "github.com/ncx-ai/keld-atlas"),
    project("p_signal", "Signal Client", "github.com/ncx-ai/keld-atlas"),
    project("p_billing", "Billing", "github.com/ncx-ai/keld-billing", { ticket_key: "BILL" }),
    project("p_old", "Old experiment", "github.com/ncx-ai/keld-old", { hidden: true }),
  ],
  suggestions: [SUGGESTION],
  coverage: { attributed: 2, total: 2, since: new Date().toISOString() },
  totals: {
    projects: [
      { id: "p_atlas", blocks: 2, minutes: 40, tokens: 200, usd: 10 },
      { id: "p_signal", blocks: 1, minutes: 20, tokens: 100, usd: 5 },
    ],
  },
};

const VISIBLE = CATALOG.projects.filter((p) => !p.hidden);

const bodyTitle = (raw: string) => (raw ? JSON.parse(raw).title : "");

type Shell = { url: string; posts: { path: string; body: any }[]; close: () => Promise<void> };

async function startShell(): Promise<Shell> {
  const posts: { path: string; body: any }[] = [];
  // Per shell, so a "New project" in one test does not leak into the next.
  const catalog: any = structuredClone(CATALOG);
  const json = (res: http.ServerResponse, body: unknown, status = 200) => {
    res.writeHead(status, { "Content-Type": "application/json" });
    res.end(JSON.stringify(body));
  };
  const server = http.createServer((req, res) => {
    const p = new URL(req.url || "/", "http://127.0.0.1").pathname;
    if (req.method === "POST" && p.startsWith("/v1/projects/")) {
      let raw = "";
      req.on("data", (c) => (raw += c));
      req.on("end", () => {
        posts.push({ path: p, body: raw ? JSON.parse(raw) : null });
        const body: any = { local_only: true, atlas_editor_url: "" };
        if (p === "/v1/projects/bundle") {
          // Applied the way the daemon applies it: the suggestion becomes a
          // project, so the next GET lists it and the note has a row to sit under.
          const made = project("p_new", bodyTitle(raw), SUGGESTION.value);
          catalog.projects.push(made);
          catalog.suggestions = [];
          body.project = made;
        }
        json(res, body);
      });
      return;
    }
    // The route Revision 4 removed. The page must never call it.
    if (p.startsWith("/v1/groups/")) {
      posts.push({ path: p, body: null });
      return json(res, {}, 404);
    }
    if (p === "/v1/ledger") return json(res, LEDGER);
    if (p === "/v1/settings") return json(res, { send_to_atlas: false });
    if (p === "/v1/projects") return json(res, catalog);
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
  return { url: `http://127.0.0.1:${port}`, posts, close: () => new Promise((r) => server.close(() => r())) };
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

/** A project row on the Projects pane, matched on its TITLE: every row
 *  carries a Map-to picker whose <option>s name the other projects, so a
 *  row-wide text match would find "Atlas Platform" in Signal Client's row. */
function projectRow(page: any, title: string) {
  return page.locator(".project-row", { has: page.locator(".row-title", { hasText: title }) });
}

async function open(page: any, shell: Shell, pane: string) {
  await page.goto(`${shell.url}/?secret=irrelevant#/${pane}`);
  await expect(page.getByText("Loading…")).toBeHidden();
}

test.describe("Only projects: one flat list", () => {
  test("NEGATIVE: the Projects pane has no group heading, switcher, tile or toggle", async ({ page, shell }) => {
    await open(page, shell, "projects");
    await expect(page.locator(".projects-card").first()).toBeVisible();
    const body = page.locator("body");
    await expect(body).not.toContainText("Groups on");
    await expect(body).not.toContainText("counts for my work");
    await expect(body).not.toContainText(/\bgroups?\b/i);
    await expect(page.locator(".group-card, .group-head, .group-switch, .shared-note")).toHaveCount(0);
    await expect(page.getByRole("tab")).toHaveCount(0);
    await expect(page.getByRole("checkbox")).toHaveCount(0);
  });

  test("every visible project is listed once, in one list, each with its own total", async ({ page, shell }) => {
    await open(page, shell, "projects");
    // One card, every visible project in it once, in the catalog's order.
    await expect(page.locator(".projects-card")).toHaveCount(2); // the list + the hidden list
    const titles = page.locator(".projects-card:not(.hidden-projects) .project-row .row-title");
    await expect(titles).toHaveCount(VISIBLE.length);
    for (const [i, p] of VISIBLE.entries()) await expect(titles.nth(i)).toContainText(p.title);

    // The shared block X counts IN FULL in both projects...
    await expect(projectRow(page, "Atlas Platform").locator("small")).toHaveText(/2 blocks · .+ · \$10\.00 est\.$/);
    await expect(projectRow(page, "Signal Client").locator("small")).toHaveText(/1 block · .+ · \$5\.00 est\.$/);
    // ...a project with no block yet shows its rules and no total...
    await expect(projectRow(page, "Billing").locator("small")).toHaveText("repo github.com/ncx-ai/keld-billing · tickets BILL-xxx");
    // ...and ONCE in coverage.
    await expect(page.getByText("2 of 2 focus blocks", { exact: true })).toBeVisible();
  });

  test("Today: a block in two projects lists both; an old row's group is ignored", async ({ page, shell }) => {
    await open(page, shell, "today");
    await expect(page.getByRole("tab")).toHaveCount(0);
    await expect(page.getByRole("tablist")).toHaveCount(0);

    const x = rowFor(page, X);
    await expect(x.locator(".ws-pill")).toHaveCount(2);
    await expect(x.locator(".ws-pill .pill")).toHaveText(["Atlas Platform", "Signal Client"]);

    const y = rowFor(page, Y);
    await expect(y.locator(".ws-pill .pill")).toHaveText(["Atlas Platform"]);
    await expect(page.locator("body")).not.toContainText(/\bgroups?\b/i);
  });

  test('"New project" asks for a name, not a group, and sends none', async ({ page, shell }) => {
    await open(page, shell, "projects");
    await page.getByRole("button", { name: "New project" }).click();
    await expect(page.getByLabel("New project name")).toHaveValue(SUGGESTION.value);
    await page.getByLabel("New project name").fill("Website");
    await page.getByRole("button", { name: "Create" }).click();
    await expect(projectRow(page, "Website")).toBeVisible();
    await expect(page.locator(".local-note")).toHaveText("Applied on this machine.");
    const bundle = shell.posts.filter((p) => p.path === "/v1/projects/bundle");
    expect(bundle).toEqual([{ path: "/v1/projects/bundle", body: { title: "Website", suggestions: [SUGGESTION.id] } }]);
    expect(shell.posts.some((p) => p.path.startsWith("/v1/groups/"))).toBe(false);
  });

  test("a hidden project is listed apart and can be shown again", async ({ page, shell }) => {
    await open(page, shell, "projects");
    await expect(page.getByText("Hidden · 1", { exact: true })).toBeVisible();
    const hidden = page.locator(".hidden-projects .project-row");
    await expect(hidden).toHaveCount(1);
    await expect(hidden).toContainText("Old experiment");
    // It is not in the main list, nor offered by any picker.
    await expect(page.locator(".projects-card:not(.hidden-projects)")).not.toContainText("Old experiment");

    await page.getByRole("button", { name: "Show Old experiment again" }).click();
    await expect(page.locator(".hidden-projects .local-note")).toHaveText("Applied on this machine.");
    expect(shell.posts).toContainEqual({ path: "/v1/projects/p_old/hide", body: { hidden: false } });
  });

  test("renders at desktop and phone width with no sideways scroll (screenshots for the review)", async ({ page, shell }, info) => {
    const shotDir = process.env.KELD_E2E_SHOT_DIR;
    if (shotDir) fs.mkdirSync(shotDir, { recursive: true });
    for (const [w, h] of [[1280, 900], [400, 900]]) {
      await page.setViewportSize({ width: w, height: h });
      for (const pane of ["today", "projects"]) {
        await open(page, shell, pane);
        await expect(page.locator(pane === "today" ? ".blocks-table" : ".projects-card").first()).toBeVisible();
        const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        expect(overflow, `${pane} at ${w}px scrolls sideways`).toBeLessThanOrEqual(0);
        const name = `flat-projects-${pane}-${info.project.name}-${w}.png`;
        await page.screenshot({ path: info.outputPath(name), fullPage: true });
        if (shotDir) await page.screenshot({ path: path.join(shotDir, name), fullPage: true });
      }
    }
  });
});
