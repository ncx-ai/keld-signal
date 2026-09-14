import http from "node:http";
import net from "node:net";
import fs from "node:fs";
import path from "node:path";
import { test as base, expect } from "@playwright/test";

// The page with NO daemon behind it. A tiny static server serves the page's
// own three files (the shell a desktop app or a cached tab would still have)
// and forwards every /v1/* call to a loopback port nothing listens on, so the
// page experiences exactly what it would with Signal stopped: assets load,
// every data call is refused. It must say so, never render blank.
const UI_DIR = path.resolve(__dirname, "..", "..", "internal", "agent", "ui");
const TYPES: Record<string, string> = { ".html": "text/html; charset=utf-8", ".css": "text/css", ".js": "text/javascript" };

async function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const s = net.createServer();
    s.listen(0, "127.0.0.1", () => {
      const port = (s.address() as net.AddressInfo).port;
      s.close(() => resolve(port));
    });
    s.on("error", reject);
  });
}

async function startShell(deadPort: number): Promise<{ url: string; close: () => Promise<void> }> {
  const server = http.createServer((req, res) => {
    const url = new URL(req.url || "/", "http://127.0.0.1");
    if (url.pathname.startsWith("/v1/")) {
      const up = http.request({ host: "127.0.0.1", port: deadPort, path: req.url, method: req.method, headers: req.headers }, (r) => {
        res.writeHead(r.statusCode || 502, r.headers);
        r.pipe(res);
      });
      up.on("error", () => {
        res.writeHead(502, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ error: "daemon_not_running" }));
      });
      req.pipe(up);
      return;
    }
    const file = url.pathname === "/" ? "index.html" : url.pathname.slice(1);
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
  return {
    url: `http://127.0.0.1:${port}`,
    close: () => new Promise((resolve) => server.close(() => resolve())),
  };
}

const test = base.extend<{ shell: { url: string } }>({
  shell: async ({}, use) => {
    const shell = await startShell(await freePort());
    await use(shell);
    await shell.close();
  },
});

test.describe("Signal not running", () => {
  test("the page says Signal is not running and how to start it — never a blank page", async ({ page, shell }) => {
    await page.route(/^https?:\/\/(?!127\.0\.0\.1[:/]|localhost[:/])/, (route) => route.abort());
    await page.goto(`${shell.url}/?secret=irrelevant#/today`);

    await expect(page.getByText("Signal is not running on this machine")).toBeVisible();
    await expect(page.getByText("keld-agent run")).toBeVisible();
    await expect(page.getByText("then reload this page")).toBeVisible();
    // The shell itself is intact: navigation and the sidebar's own verdict.
    await expect(page.getByText("Keld Signal", { exact: true })).toBeVisible();
    await expect(page.getByRole("link", { name: "Today" })).toBeVisible();
    await expect(page.getByText("not running", { exact: true })).toBeVisible();
    // No spinner left behind, and no block card pretending to be data.
    await expect(page.getByText("Loading…")).toBeHidden();
    await expect(page.getByRole("row").filter({ hasText: /\d{2}:\d{2} → \d{2}:\d{2}/ })).toHaveCount(0);
    expect((await page.locator("body").innerText()).trim().length).toBeGreaterThan(0);
  });

  test("the other panes say so too", async ({ page, shell }) => {
    await page.route(/^https?:\/\/(?!127\.0\.0\.1[:/]|localhost[:/])/, (route) => route.abort());
    await page.goto(`${shell.url}/?secret=irrelevant#/settings`);
    await expect(page.getByText("Signal is not running on this machine")).toBeVisible();
    await expect(page.getByText(/Signal may not be running/)).toBeVisible();
    await page.getByRole("link", { name: "Projects" }).click();
    await expect(page.getByText("Signal is not running on this machine")).toBeVisible();
  });
});
