import http from "node:http";
import net from "node:net";
import fs from "node:fs";
import path from "node:path";
import { test as base, expect } from "@playwright/test";

// Revision 2 — Signal labels on its own (R2-AC-3).
//
// Every project on the Projects pane is the person's own Signal
// project. The daemon's catalog no longer says `origin: "atlas"` (an old
// "Same as" overlay reads "user"), but the page must be right even when
// handed the old shape, so this fixture serves it on purpose: an overlay
// stored with `origin: "atlas"` and an Atlas id, beside ordinary Signal
// projects and a suggestion. The DOM under test is the real page; only the
// data is fixed — the same mocked-catalog shell flat-projects.spec.ts uses.
// Since Revision 4 the catalog carries no groups, so neither does this one.

const REPO_ROOT = path.resolve(__dirname, "..", "..");
const UI_DIR = path.join(REPO_ROOT, "internal", "agent", "ui");
const TYPES: Record<string, string> = {
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".svg": "image/svg+xml",
  ".png": "image/png",
};

const OVERLAY = {
  id: "keld_projects:signal",
  title: "Signal Platform",
  repos: ["github.com/ncx-ai/keld-signal"],
  rules: ["github.com/ncx-ai/keld-signal"],
  origin: "atlas",
  atlas_value_id: "keld_projects:signal",
};

const CATALOG = {
  projects: [
    OVERLAY,
    { id: "development:billing", title: "Billing", repos: ["github.com/ncx-ai/keld-billing"], rules: ["github.com/ncx-ai/keld-billing"], origin: "user" },
    { id: "development:docs", title: "Docs site", repos: ["github.com/ncx-ai/keld-docs"], rules: ["github.com/ncx-ai/keld-docs"], origin: "local" },
    { id: "development:old", title: "Old experiment", repos: [], rules: [], origin: "user", hidden: true },
  ],
  suggestions: [
    { id: "repo:github.com/ncx-ai/keld-website", kind: "repo", value: "github.com/ncx-ai/keld-website", blocks: 3, minutes: 45, tokens: 12000 },
  ],
  coverage: { attributed: 4, total: 7, since: new Date().toISOString() },
  totals: { projects: [] },
};

// The catalog's visible projects, in its order: what every picker must offer.
const VISIBLE = CATALOG.projects.filter((w: any) => !w.hidden);

type Shell = { url: string; posts: { path: string; body: any }[]; close: () => Promise<void> };

async function startShell(): Promise<Shell> {
  const posts: { path: string; body: any }[] = [];
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
        // The daemon still sends atlas_editor_url; the page must not render it.
        json(res, { local_only: true, atlas_editor_url: "https://atlas.invalid/projects" });
      });
      return;
    }
    if (p === "/v1/ledger") return json(res, { generated_at: new Date().toISOString(), blocks: [], health: [] });
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
  return { url: `http://127.0.0.1:${port}`, posts, close: () => new Promise((r) => server.close(() => r())) };
}

const test = base.extend<{ shell: Shell }>({
  shell: async ({}, use) => {
    const s = await startShell();
    await use(s);
    await s.close();
  },
});

async function openProjects(page: any, shell: Shell) {
  await page.goto(`${shell.url}/?secret=irrelevant#/projects`);
  await expect(page.locator(".projects-card").first()).toBeVisible();
}

test.describe("Signal labels on its own: the Projects pane", () => {
  test('NEGATIVE: no "in Atlas" anywhere on the pane, even for an overlay stored with origin "atlas"', async ({ page, shell }) => {
    await openProjects(page, shell);
    const pane = page.locator("#paneRoot");
    // textContent, so the pickers' <option> labels are included.
    await expect(pane).toContainText("Signal Platform");
    await expect(pane).not.toContainText(/in Atlas/i);
    await expect(pane).not.toContainText(/from Atlas/i);
    // Every row carries the same pill; none is marked as the org's.
    const pills = pane.locator(".projects-card:not(.hidden-projects) .project-row .pill");
    await expect(pills).toHaveCount(VISIBLE.length);
    await expect(pills).toHaveText(VISIBLE.map(() => "local"));
  });

  test("the Same-as picker lists exactly the catalog's projects, by title", async ({ page, shell }) => {
    await openProjects(page, shell);
    const picker = page.getByRole("combobox", { name: /^Same as an existing project/ });
    await expect(picker).toHaveCount(1);
    const options = picker.locator("option:not([value=''])");
    await expect(options).toHaveText(VISIBLE.map((w) => w.title));
    expect(await options.evaluateAll((os) => os.map((o) => (o as HTMLOptionElement).value)))
      .toEqual(VISIBLE.map((w) => w.id));
  });

  test("placing a suggestion onto the overlay confirms locally and sends nobody to Atlas", async ({ page, shell }) => {
    await openProjects(page, shell);
    await page.getByRole("combobox", { name: /^Same as an existing project/ }).selectOption(OVERLAY.id);
    const note = page.locator(".local-note");
    await expect(note).toHaveText("Applied on this machine.");
    await expect(note.locator("a")).toHaveCount(0);
    expect(shell.posts).toContainEqual({
      path: "/v1/projects/place",
      body: { suggestion: CATALOG.suggestions[0].id, same_as: OVERLAY.id },
    });
  });

  test("Map-to is offered on the overlay row, and on every other project", async ({ page, shell }) => {
    await openProjects(page, shell);
    for (const w of VISIBLE) {
      const mapTo = page.getByLabel(`Map ${w.title} onto another project`);
      await expect(mapTo, `Map-to on ${w.title}`).toBeVisible();
      // Offers every OTHER project, never itself.
      await expect(mapTo.locator("option:not([value=''])"))
        .toHaveText(VISIBLE.filter((o) => o.id !== w.id).map((o) => o.title));
    }
    await page.getByLabel(`Map ${OVERLAY.title} onto another project`).selectOption("development:billing");
    await expect(page.locator(".local-note")).toHaveText("Applied on this machine.");
    expect(shell.posts).toContainEqual({
      path: `/v1/projects/${encodeURIComponent(OVERLAY.id)}/same-as`,
      body: { same_as: "development:billing" },
    });
  });

  test("renders at desktop and phone width with no sideways scroll (screenshots for the review)", async ({ page, shell }, info) => {
    const shotDir = process.env.KELD_E2E_SHOT_DIR;
    for (const [w, h] of [[1280, 900], [400, 900]]) {
      await page.setViewportSize({ width: w, height: h });
      await openProjects(page, shell);
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);      expect(overflow, `projects at ${w}px scrolls sideways`).toBeLessThanOrEqual(0);
      const name = `signal-only-projects-${info.project.name}-${w}.png`;
      await page.screenshot({ path: info.outputPath(name), fullPage: true });
      if (shotDir) await page.screenshot({ path: path.join(shotDir, name), fullPage: true });
    }
  });
});
