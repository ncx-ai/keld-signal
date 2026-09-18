import http from "node:http";
import fs from "node:fs";
import path from "node:path";
import { test as base, expect, type Page } from "@playwright/test";

/**
 * THE PAGE'S DURABILITY SENTENCE, AND THE DOCUMENT IT COMES FROM.
 *
 * `docs/durability.md` is the full statement — one section per lane, every
 * claim citing the file and symbol that makes it true. The page carries the
 * one-sentence version beside the health strip, where a person reading a red
 * badge is already asking the question it answers.
 *
 * ⚠️ **THE SENTENCE IS READ OFF THE DOCUMENT, NEVER TYPED HERE.** Two copies of
 * one claim drift, and this suite exists to make that impossible rather than
 * unlikely: the expected text is extracted from the `page-copy:durability`
 * markers in the markdown, so editing the page without editing the document
 * (or the reverse) fails here. `internal/agent/ui/test/durability.test.js`
 * pins the same pair from the Node side; this one proves it reaches the screen.
 *
 * ⚠️ **THE PAGE IS SERVED BY A LOCAL SHELL, NOT BY THE DAEMON** — the idiom
 * `not-running.spec.ts` and `integrations-states.spec.ts` already establish.
 * What is under test is what the pane does with a `GET /v1/ledger` response,
 * so the response is a fixture written inline.
 */

const REPO_ROOT = path.resolve(__dirname, "..", "..");
const UI_DIR = path.join(REPO_ROOT, "internal", "agent", "ui");
const DOC = path.join(REPO_ROOT, "docs", "durability.md");

const TYPES: Record<string, string> = {
  ".html": "text/html; charset=utf-8",
  ".css": "text/css",
  ".js": "text/javascript",
};

// Both widths the rest of the suite runs at: 400 is below the page's own
// 720/780px breakpoints, which is where a full-width line inside a
// space-between flex strip would be squeezed if it were a third flex item.
const WIDTHS = [
  { name: "1280x800", width: 1280, height: 800 },
  { name: "400 wide", width: 400, height: 900 },
];

function docSentence(): string {
  const src = fs.readFileSync(DOC, "utf8");
  const m = src.match(/<!-- page-copy:durability -->([\s\S]*?)<!-- \/page-copy:durability -->/);
  if (!m) {
    throw new Error(
      `docs/durability.md carries no page-copy:durability block; the page's sentence has no document to match`
    );
  }
  return m[1]
    .replace(/^\s*>\s?/gm, "")
    .replace(/\s+/g, " ")
    .trim();
}

type Health = Array<{ key: string; status: string; detail?: string }>;

type Shell = {
  url: string;
  /** What GET /v1/ledger answers with. Mutable so one shell serves both cases. */
  serveHealth: (health: Health) => void;
  settings: Record<string, unknown>;
  close: () => Promise<void>;
};

async function startShell(): Promise<Shell> {
  let health: Health = [];
  const settings: Record<string, unknown> = { readonly: [], send_to_atlas: true };

  const json = (res: http.ServerResponse, body: unknown, status = 200) => {
    res.writeHead(status, { "Content-Type": "application/json" });
    res.end(JSON.stringify(body));
  };

  const server = http.createServer((req, res) => {
    const url = new URL(req.url || "/", "http://127.0.0.1");
    const p = url.pathname;
    // A day with no blocks: the Today pane still renders its cards, its empty
    // line and the health strip, which is all this suite reads.
    if (p === "/v1/ledger")
      return json(res, { generated_at: "2026-09-19T09:13:00Z", blocks: [], health });
    if (p === "/v1/settings") return json(res, settings);
    if (p === "/v1/projects") return json(res, { projects: [] });
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
  const port = (server.address() as { port: number }).port;
  return {
    url: `http://127.0.0.1:${port}`,
    serveHealth: (h) => {
      health = h;
    },
    settings,
    close: () => new Promise((resolve) => server.close(() => resolve())),
  };
}

const test = base.extend<{ shell: Shell }>({
  shell: async ({}, use) => {
    const shell = await startShell();
    await use(shell);
    await shell.close();
  },
});

let nonce = 0;

async function openToday(page: Page, shell: Shell, size: { width: number; height: number }): Promise<void> {
  await page.setViewportSize({ width: size.width, height: size.height });
  await page.route(/^https?:\/\/(?!127\.0\.0\.1[:/]|localhost[:/])/, (route) => route.abort());
  // The nonce is load-bearing for the same reason it is in
  // integrations-states.spec.ts: a goto differing only in its hash is a
  // same-document navigation and would render the previous fixture.
  await page.goto(`${shell.url}/?secret=irrelevant&n=${nonce++}#/today`);
  await expect(page.getByText("Loading…")).toBeHidden();
  await expect(page.locator(".health-strip")).toBeVisible();
}

const OK_HEALTH: Health = [
  { key: "daemon", status: "ok", detail: "3.0.0" },
  { key: "atlas", status: "ok", detail: "" },
];

const BROKEN_HEALTH: Health = [
  { key: "daemon", status: "ok", detail: "3.0.0" },
  { key: "atlas", status: "failed", detail: "atlas_unavailable" },
];

for (const size of WIDTHS) {
  test.describe(`Durability sentence · ${size.name}`, () => {
    test("a failed health cell puts the sentence on screen, word for word as docs/durability.md has it", async ({
      page,
      shell,
    }) => {
      shell.serveHealth(BROKEN_HEALTH);
      await openToday(page, shell, size);

      const note = page.locator(".health-strip .durability-note");
      await expect(note).toBeVisible();
      await expect(note).toHaveText(docSentence());
      // It sits with the badge it explains, not somewhere else on the page.
      await expect(page.locator(".health-strip")).toContainText("Atlas unreachable");
    });

    test("the hedge survives — the page never promises nothing is lost", async ({ page, shell }) => {
      shell.serveHealth(BROKEN_HEALTH);
      await openToday(page, shell, size);
      // Three lanes genuinely lose things (docs/durability.md names each), so
      // "usually" is the word that keeps this sentence true.
      await expect(page.locator(".health-strip .durability-note")).toContainText("usually");
    });

    test("a healthy machine is told nothing it did not ask about", async ({ page, shell }) => {
      shell.serveHealth(OK_HEALTH);
      await openToday(page, shell, size);
      await expect(page.locator(".health-strip .durability-note")).toHaveCount(0);
    });
  });
}
