import http from "node:http";
import net from "node:net";
import fs from "node:fs";
import path from "node:path";
import { test as base, expect, type Page } from "@playwright/test";
import { STATE_FILE } from "./playwright.config";

/**
 * EVERY STATE THE SERVER CAN SEND, RENDERED, AT TWO WIDTHS.
 *
 * The cases are DERIVED from the vocabulary, never typed here: a state the
 * daemon grows without a fixture beside it fails this suite rather than
 * rendering as nothing. That is the whole point of `vocabulary.states` riding
 * the response (docs/signal-integrations-wire.md §2).
 *
 * ⚠️ **THE VOCABULARY HAS TWO SOURCES AND THE FALLBACK IS NOT A WEAKENING.**
 * First choice is the live daemon's own `GET /v1/integrations`. That route is
 * WS-C1's and is not merged yet, and `scripts/e2e-up.sh` additionally needs the
 * ~5 GB sidecar venv to bring a daemon up at all, so the second source is the
 * contract package itself — `internal/agent/integrations/types.go`, the file
 * `vocabulary.go` is pinned against by `types_test.go`. Either way the list
 * comes from the server side of the seam and a new state arrives here on its
 * own. Which source was used is printed once, so a run can never quietly be
 * the weaker one.
 *
 * ⚠️ **THE PAGE IS SERVED BY A LOCAL SHELL, NOT BY THE DAEMON.** This suite
 * tests RENDERING — what the pane does with a response — so the response is a
 * fixture and the daemon is not in the picture. `not-running.spec.ts` already
 * establishes the idiom (serve the three UI files, answer /v1/* ourselves).
 * The journey specs that need a real daemon and real wiring facts live in
 * `integrations.spec.ts` and are skipped until WS-C1 lands.
 */

const REPO_ROOT = path.resolve(__dirname, "..", "..");
const UI_DIR = path.join(REPO_ROOT, "internal", "agent", "ui");
const CONTRACT_TYPES = path.join(REPO_ROOT, "internal", "agent", "integrations", "types.go");
const APP_JS = path.join(UI_DIR, "app.js");
const FIXTURES = path.join(__dirname, "fixtures", "integrations");

const TYPES: Record<string, string> = {
  ".html": "text/html; charset=utf-8",
  ".css": "text/css",
  ".js": "text/javascript",
};

// Both widths every assertion runs at. 400 is the narrow end the page's own
// breakpoints (720/780px) are below, so this is the layout after the nav has
// turned into a bar and the shell into one column.
const WIDTHS = [
  { name: "1280x800", width: 1280, height: 800 },
  { name: "400 wide", width: 400, height: 900 },
];

// ---------------------------------------------------------------------------
// Where the vocabulary comes from
// ---------------------------------------------------------------------------

type VocabSource = { states: string[]; from: string };

async function liveVocabulary(): Promise<VocabSource | null> {
  if (!fs.existsSync(STATE_FILE)) return null;
  let state: { baseURL?: string; secret?: string };
  try {
    state = JSON.parse(fs.readFileSync(STATE_FILE, "utf8"));
  } catch {
    return null;
  }
  if (!state.baseURL) return null;
  try {
    const res = await fetch(`${state.baseURL}/v1/integrations`, {
      headers: { "x-keld-agent-secret": state.secret || "" },
      signal: AbortSignal.timeout(3000),
    });
    if (!res.ok) return null; // 404 until WS-C1 mounts the route
    const body = (await res.json()) as { vocabulary?: { states?: string[] } };
    const states = body.vocabulary?.states;
    if (!Array.isArray(states) || states.length === 0) return null;
    return { states, from: `the live daemon at ${state.baseURL}` };
  } catch {
    return null;
  }
}

/**
 * The contract package's own `State` constants. Read off `types.go` rather
 * than `vocabulary.go` deliberately: `types_test.go` pins the slice against
 * these constants, so a constant that exists without being in the vocabulary
 * is already a Go test failure — reading the constants therefore cannot miss a
 * state the route would publish.
 */
function contractVocabulary(): VocabSource {
  const src = fs.readFileSync(CONTRACT_TYPES, "utf8");
  const states = [...src.matchAll(/^\s*\w+\s+State\s*=\s*"([a-z_]+)"/gm)].map((m) => m[1]);
  if (states.length === 0) {
    throw new Error(`no State constants found in ${CONTRACT_TYPES}; this suite cannot derive its cases`);
  }
  return { states, from: path.relative(REPO_ROOT, CONTRACT_TYPES) };
}

let vocabulary: VocabSource;

// ---------------------------------------------------------------------------
// The shell: the page's own files, and /v1/* answered from a fixture
// ---------------------------------------------------------------------------

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

type Shell = {
  url: string;
  serve: (body: unknown) => void;
  /** What GET /v1/settings answers with, and what a PUT merges into. The
   *  Developer box is rendered from this, so a spec that drives the switch
   *  reads its own writes exactly as the page does. */
  settings: Record<string, unknown>;
  close: () => Promise<void>;
};

async function startShell(): Promise<Shell> {
  // What the pane under test is handed. Mutable so one page can be re-polled
  // with a different answer (the poll is what a real state change arrives on).
  let integrations: unknown = { integrations: [], vocabulary: { states: [], waiting_on: [] }, auto_setup: true };
  // The page's Settings pane state. `readonly: []` because no env var pins
  // anything here; `tool_otlp` is absent, which is what a daemon that has never
  // been told otherwise answers — and the row must render OFF from that.
  const settings: Record<string, unknown> = { readonly: [] };

  const json = (res: http.ServerResponse, body: unknown, status = 200) => {
    res.writeHead(status, { "Content-Type": "application/json" });
    res.end(JSON.stringify(body));
  };

  const server = http.createServer((req, res) => {
    const url = new URL(req.url || "/", "http://127.0.0.1");
    const p = url.pathname;
    if (p === "/v1/integrations") return json(res, integrations);
    if (p.startsWith("/v1/integrations/") && p.endsWith("/setup")) {
      return json(res, { backup: "/tmp/keld-e2e/codex/config.toml.keld-backup", restart_required: true });
    }
    if (p.startsWith("/v1/integrations/") && p.endsWith("/report")) {
      return json(res, { report_path: "/tmp/keld-e2e/reports/2026-09-15T09-13-00Z-codex.json", event_queued: true });
    }
    // The rest of the page's boot. Minimal but valid: an empty ledger and empty
    // settings are what a machine that has done no work yet answers, and the
    // integrations pane must not depend on any of it.
    // A daemon health row with a detail is what draws the version in the
    // sidebar — which is the control developer mode is reached through, so
    // without it the seven taps have nothing to land on.
    if (p === "/v1/ledger")
      return json(res, {
        generated_at: "2026-09-15T09:13:00Z",
        blocks: [],
        health: [{ key: "daemon", status: "ok", detail: "3.0.0" }],
      });
    if (p === "/v1/settings") {
      if (req.method === "PUT") {
        let body = "";
        req.on("data", (c) => (body += c));
        req.on("end", () => {
          try {
            Object.assign(settings, JSON.parse(body || "{}"));
          } catch {
            /* a malformed body is the test's bug, and an empty merge shows it */
          }
          // No daemon restart for tool_otlp: the detector reads it live and the
          // restart that IS needed belongs to the tool. See ingress/settings.go.
          json(res, { restart_required: false });
        });
        return;
      }
      return json(res, settings);
    }
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
  const port = (server.address() as net.AddressInfo).port;
  return {
    url: `http://127.0.0.1:${port}`,
    serve: (body) => {
      integrations = body;
    },
    settings,
    close: () => new Promise((resolve) => server.close(() => resolve())),
  };
}

function fixture(name: string): unknown {
  const file = path.join(FIXTURES, `${name}.json`);
  if (!fs.existsSync(file)) {
    // The refusal this suite exists for: a state the server can send with
    // nothing here saying what it should look like.
    throw new Error(
      `no fixture ui/e2e/fixtures/integrations/${name}.json for state "${name}", which ${vocabulary.from} publishes. ` +
        `Add the fixture and its assertions — a state with no fixture renders untested.`
    );
  }
  return JSON.parse(fs.readFileSync(file, "utf8"));
}

const test = base.extend<{ shell: Shell }>({
  shell: async ({}, use) => {
    const shell = await startShell();
    await use(shell);
    await shell.close();
  },
});

test.beforeAll(async () => {
  vocabulary = (await liveVocabulary()) || contractVocabulary();
  console.log(`integrations: ${vocabulary.states.length} states derived from ${vocabulary.from}`);
});

async function openPane(page: Page, shell: Shell, size: { width: number; height: number }): Promise<void> {
  await page.setViewportSize({ width: size.width, height: size.height });
  await page.route(/^https?:\/\/(?!127\.0\.0\.1[:/]|localhost[:/])/, (route) => route.abort());
  // ⚠️ **THE NONCE IS LOAD-BEARING.** A `goto` to a URL that differs only in
  // its hash is a SAME-DOCUMENT navigation: the page is not re-fetched, so a
  // second fixture served to the same page rendered the first one's rows and
  // the failure read as "the pane ignores the server".
  await page.goto(`${shell.url}/?secret=irrelevant&n=${nonce++}#/integrations`);
  await expect(page.getByText("Loading…")).toBeHidden();
  await expect(page.locator(".intg-row").first()).toBeVisible();
}

let nonce = 0;

const row = (page: Page, id: string) => page.locator(`.intg-row[data-integration="${id}"]`);
const pill = (page: Page, id: string) => row(page, id).locator(".intg-state");
const checks = (page: Page, id: string) => row(page, id).locator(".intg-check");

// ---------------------------------------------------------------------------

for (const size of WIDTHS) {
  test.describe(`Integrations pane · ${size.name}`, () => {
    test("every state in the vocabulary has a fixture and renders its own name verbatim", async ({ page, shell }) => {
      for (const state of vocabulary.states) {
        const body = fixture(state) as { integrations: Array<{ id: string; state: string }> };
        expect(
          body.integrations.some((i) => i.state === state),
          `fixture ${state}.json must carry a row whose state is "${state}"`
        ).toBe(true);
        shell.serve(body);
        await openPane(page, shell, size);
        const id = body.integrations[0].id;
        // The pill prints what the server sent, character for character.
        await expect(pill(page, id)).toHaveText(state);
        await expect(pill(page, id)).toHaveAttribute("data-state", state);
        await expect(page.getByText(/^unknown state: /)).toHaveCount(0);
      }
    });

    test("a state outside the vocabulary renders literally, and is mapped to nothing", async ({ page, shell }) => {
      shell.serve(fixture("unknown-state"));
      await openPane(page, shell, size);
      await expect(pill(page, "claude_code")).toHaveText("unknown state: quantum_entangled");
      // Not silently rendered as one of the states it is not.
      for (const state of vocabulary.states) {
        await expect(pill(page, "claude_code")).not.toHaveText(state);
      }
      // The rest of the row still renders: an unknown state is not a broken row.
      await expect(row(page, "claude_code")).toContainText("Claude Code");
      await expect(row(page, "claude_code")).toContainText("2.1.267");
    });

    test("not_installed: nothing wired, no instruction, a dash for the version", async ({ page, shell }) => {
      shell.serve(fixture("not_installed"));
      await openPane(page, shell, size);
      await expect(pill(page, "codex")).toHaveText("not_installed");
      await expect(checks(page, "codex")).toHaveCount(2);
      await expect(checks(page, "codex").first()).toContainText("config not written");
      await expect(checks(page, "codex").first()).toHaveAttribute("data-ok", "false");
      await expect(row(page, "codex").locator(".intg-version")).toHaveText("—");
      await expect(row(page, "codex")).not.toContainText("unknown version");
      await expect(row(page, "codex").locator(".intg-instruction")).toHaveCount(0);
      await expect(row(page, "codex").getByRole("button", { name: "Set up" })).toHaveCount(0);
      await expect(row(page, "codex").getByRole("button", { name: "Report a problem" })).toHaveCount(0);
    });

    test("not_configured: Set up is offered, and posting it shows the backup and the restart notice", async ({ page, shell }) => {
      shell.serve(fixture("not_configured"));
      await openPane(page, shell, size);
      await expect(pill(page, "codex")).toHaveText("not_configured");
      await expect(row(page, "codex").locator(".intg-version")).toHaveText("0.153.4");
      const setup = row(page, "codex").getByRole("button", { name: "Set up" });
      await expect(setup).toBeVisible();
      // Not configured, so there is nothing yet to report a problem about.
      await expect(row(page, "codex").getByRole("button", { name: "Report a problem" })).toHaveCount(0);

      await setup.click();
      await expect(row(page, "codex").locator(".intg-result")).toContainText("config.toml.keld-backup");
      await expect(row(page, "codex").locator(".intg-result")).toContainText("Restart");
    });

    test("restart_required: the server's restart sentence, once, and the lane says what it is waiting for", async ({ page, shell }) => {
      shell.serve(fixture("restart_required"));
      await openPane(page, shell, size);
      await expect(pill(page, "claude_code")).toHaveText("restart_required");
      const sentence =
        "Restart this tool to finish — a session that started before its config was written keeps using the settings it launched with.";
      // Two lanes carry the same sentence; a person reads it once.
      await expect(row(page, "claude_code").locator(".intg-instruction")).toHaveCount(1);
      await expect(row(page, "claude_code").locator(".intg-instruction")).toHaveText(sentence);
      await expect(checks(page, "claude_code").nth(0)).toContainText("not restarted since");
      await expect(checks(page, "claude_code").nth(0)).toHaveAttribute("data-ok", "false");
      await expect(checks(page, "claude_code").nth(2)).toContainText("transcripts readable");
      await expect(checks(page, "claude_code").nth(2)).toHaveAttribute("data-ok", "true");
      // The window to restart, named beside the sentence telling you to.
      await expect(row(page, "claude_code").locator(".intg-stale")).toHaveText("session 8f21c0de");
    });

    /**
     * ⚠️ **"RESTART THIS TOOL" IS NOT AN INSTRUCTION WHEN TWO WINDOWS ARE
     * OPEN.** Measured on the maintainer's machine 2026-09-18: two live Claude
     * Code sessions, one restarted since the config and one carried over, and
     * the row could only say "restart". The daemon resolves the verdict over
     * every live session, so it already knows which one is stale; the row now
     * prints the first eight characters of that id — the same prefix
     * `keld signal doctor` prints for the same session, so a person reading one
     * recognises the other.
     *
     * The wire carries the VERDICT, not the session list, so what a fixture can
     * show is the id the daemon named. The second row is a healthy tool, which
     * is what makes "only the stale row says it" checkable in one render.
     */
    test("two live sessions: the row names the window to restart, and a healthy row names none", async ({ page, shell }) => {
      shell.serve(fixture("two-live-sessions"));
      await openPane(page, shell, size);
      const stale = row(page, "claude_code").locator(".intg-stale");
      await expect(stale).toHaveCount(1);
      await expect(stale).toHaveText("session 8f21c0de");
      // Never the whole id: it is an identifier to recognise, not to read out.
      await expect(row(page, "claude_code")).not.toContainText("8f21c0de-4b17");
      // And the tool that is fine says nothing about sessions at all.
      await expect(row(page, "codex").locator(".intg-stale")).toHaveCount(0);
    });

    test("a healthy machine names no session anywhere", async ({ page, shell }) => {
      for (const name of ["working", "idle", "not_configured"]) {
        shell.serve(fixture(name));
        await openPane(page, shell, size);
        await expect(page.locator(".intg-stale")).toHaveCount(0);
      }
    });

    test("approval_required: the AC-9 sentence verbatim, beside the reader sentence", async ({ page, shell }) => {
      shell.serve(fixture("approval_required"));
      await openPane(page, shell, size);
      await expect(pill(page, "codex")).toHaveText("approval_required");
      const instructions = row(page, "codex").locator(".intg-instruction");
      await expect(instructions).toHaveCount(2);
      await expect(instructions.nth(0)).toHaveText(
        "Open Codex, run /hooks, approve the two keld hooks. Signal confirms here within a minute."
      );
      await expect(instructions.nth(1)).toHaveText(
        "Signal captures this tool but cannot read its transcripts yet, so its prompts are not classified — nothing for you to do."
      );
      await expect(checks(page, "codex").nth(0)).toContainText("not trusted");
      // A lane that cannot feed at this support level says so rather than
      // looking like a lane that has gone quiet.
      await expect(checks(page, "codex").nth(3)).toContainText("not expected");
    });

    test("idle: every lane wired, nothing waiting, nothing said", async ({ page, shell }) => {
      shell.serve(fixture("idle"));
      await openPane(page, shell, size);
      await expect(pill(page, "claude_code")).toHaveText("idle");
      await expect(checks(page, "claude_code")).toHaveCount(4);
      for (let i = 0; i < 4; i++) {
        await expect(checks(page, "claude_code").nth(i)).toHaveAttribute("data-ok", "true");
      }
      await expect(row(page, "claude_code").locator(".intg-instruction")).toHaveCount(0);
    });

    test("working: the wire doc's example, all four lanes, and Report a problem", async ({ page, shell }) => {
      shell.serve(fixture("working"));
      await openPane(page, shell, size);
      await expect(pill(page, "claude_code")).toHaveText("working");
      await expect(checks(page, "claude_code").nth(0)).toContainText("config written");
      await expect(checks(page, "claude_code").nth(1)).toContainText("points at Signal");
      await expect(checks(page, "claude_code").nth(2)).toContainText("transcripts readable");
      await expect(checks(page, "claude_code").nth(3)).toContainText("transcripts parsed");

      const report = row(page, "claude_code").getByRole("button", { name: "Report a problem" });
      await expect(report).toBeVisible();
      await report.click();
      await expect(row(page, "claude_code").locator(".intg-result")).toContainText(
        "2026-09-15T09-13-00Z-codex.json"
      );
    });

    test("broken: the silent lane is named, and the row still says which", async ({ page, shell }) => {
      shell.serve(fixture("broken"));
      await openPane(page, shell, size);
      await expect(pill(page, "claude_code")).toHaveText("broken");
      await expect(checks(page, "claude_code").nth(0)).toContainText("nothing arrived");
      await expect(checks(page, "claude_code").nth(0)).toHaveAttribute("data-ok", "false");
      await expect(checks(page, "claude_code").nth(1)).toHaveAttribute("data-ok", "true");
    });

    test("unsupported: the storage class, and that Signal does not claim to capture it", async ({ page, shell }) => {
      shell.serve(fixture("unsupported"));
      await openPane(page, shell, size);
      await expect(pill(page, "cursor")).toHaveText("unsupported");
      await expect(row(page, "cursor")).toContainText("db-poll");
      await expect(row(page, "cursor")).toContainText("not yet supported");
      await expect(row(page, "cursor").getByRole("button")).toHaveCount(0);
      await expect(checks(page, "cursor")).toHaveCount(0);
    });

    test("the whole catalogue renders in the order the server sent it", async ({ page, shell }) => {
      shell.serve(fixture("catalogue"));
      await openPane(page, shell, size);
      await expect(page.locator(".intg-row")).toHaveCount(3);
      await expect(page.locator(".intg-row .intg-name")).toHaveText(["Claude Code", "Codex", "Cursor"]);
      // Nothing overflows the viewport at either width.
      const overflow = await page.evaluate(
        () => document.documentElement.scrollWidth - document.documentElement.clientWidth
      );
      expect(overflow).toBeLessThanOrEqual(1);
    });
  });
}

// ---------------------------------------------------------------------------
// The rule, enforced on the source rather than hoped for
// ---------------------------------------------------------------------------

/**
 * The two integrations blocks of app.js, and ONLY those: the pure functions
 * and the DOM section, which are far apart in the file with three unrelated
 * panes between them. Slicing from the first marker to the last would sweep
 * in the Settings restart bar, whose own vocabulary genuinely contains
 * `restart_required` — a guard that fails on somebody else's code is a guard
 * that gets deleted.
 */
function integrationsSource(): string {
  const src = fs.readFileSync(APP_JS, "utf8");
  const parts: string[] = [];
  for (const [open, close] of [
    ["// ---- Integrations: pure ----", "// ---- /Integrations: pure ----"],
    ["// ---- Integrations ----", "// ---- /Integrations ----"],
  ]) {
    const a = src.indexOf(open);
    const b = src.indexOf(close);
    expect(a, `app.js must carry the marker ${open} so this guard can find the pane`).toBeGreaterThan(-1);
    expect(b, `app.js must carry the marker ${close}`).toBeGreaterThan(a);
    parts.push(src.slice(a, b));
  }
  return parts.join("\n");
}

test.describe("no state logic in JavaScript", () => {
  /**
   * ⚠️ **THE PANE MAY NOT COMPARE AGAINST A STATE, AT ALL.** Not for copy, not
   * for an affordance, not for a colour — a second copy of `Compute`'s rule in
   * JavaScript is the defect AC-8 exists to prevent, and it would drift the
   * day the daemon's table changes without the page shipping.
   *
   * The pane therefore reaches for the server's own booleans (`configured`,
   * `supported`, `installed`) where it needs an affordance, prints `state`
   * verbatim, and hands the colour to CSS via `data-state`. The one thing it
   * decides is whether the value is IN the published vocabulary, which is a
   * membership test against a list the server sent, not a literal.
   */
  test("the Integrations section of app.js contains no state literal", () => {
    const states = contractVocabulary().states;
    const section = integrationsSource();
    for (const state of states) {
      for (const quoted of [`"${state}"`, `'${state}'`, `\`${state}\``]) {
        expect(
          section.includes(quoted),
          `app.js's Integrations section contains the state literal ${quoted}. The pane renders states, it never decides them.`
        ).toBe(false);
      }
    }
    // And the refusal for a value the vocabulary does not hold is produced in
    // exactly ONE place. Matched on the QUOTED literal, not the phrase: the
    // comments above it name the behaviour more than once, which is a good
    // thing, and a guard that punished that would be teaching the wrong
    // lesson.
    expect(section.match(/"unknown state: "/g) || []).toHaveLength(1);
  });

  test("the pane's only integrations render path goes through the server's vocabulary", () => {
    const section = integrationsSource();
    // No switch over states, and no map keyed by them.
    expect(/switch\s*\(\s*[\w.]*[Ss]tate/.test(section)).toBe(false);
    expect(section).toContain("vocabulary");
  });
});

// ---------------------------------------------------------------------------
// The Developer switch that turns the tool's own OTLP export off
// ---------------------------------------------------------------------------

/**
 * ⚠️ **THE LANE IS OFF BY DEFAULT AND SCHEDULED FOR REMOVAL.** Signal reads a
 * tool's usage from the tool's own transcript; the OTLP export adds nothing
 * Atlas prices, and it is the one lane that needs a credential inside a file the
 * tool reads once at startup. It stays reachable behind the Developer box so
 * someone can prove to themselves that nothing needed arrives only here.
 *
 * The copy is asserted VERBATIM rather than by substring: it carries a
 * deprecation notice and a restart instruction, and a reworded half is exactly
 * the kind of change that would go unnoticed.
 */
const TOOL_OTLP_TITLE = "Extended telemetry from the tool (OTLP)";
const TOOL_OTLP_BODY =
  "Off. Signal reads usage from the tool's own transcript; this lane is scheduled for removal " +
  "once we have confirmed nothing we need arrives only here. Turning it on writes into the " +
  "tool's configuration, and the tool must be restarted once to pick it up.";
const TOOL_OTLP_RESTART = "Restart the tool once to pick this up.";

/** The Settings pane, opened the way a person opens it. */
async function openSettings(page: Page, shell: Shell): Promise<void> {
  await page.route(/^https?:\/\/(?!127\.0\.0\.1[:/]|localhost[:/])/, (route) => route.abort());
  await page.goto(`${shell.url}/?secret=irrelevant&n=${nonce++}#/settings`);
  await expect(page.getByText("Loading…")).toBeHidden();
  // The Environment tile's own label: unique, and drawn from nothing the
  // Developer box depends on, so it settles the pane without vouching for it.
  await expect(page.getByText("Environment", { exact: true })).toBeVisible();
}

/** Developer mode, the way a person reaches it: seven taps on the version in the
 *  sidebar, inside the tap window. ⚠️ The taps TOGGLE — call once per test. */
async function enterDeveloperMode(page: Page): Promise<void> {
  const version = page.locator("#navVersion");
  await expect(version).toBeVisible();
  for (let i = 0; i < 7; i++) await version.click();
  await expect(page.getByText("Developer", { exact: true })).toBeVisible();
}

/** The row's switch. The input is visually hidden, so state is read off it and
 *  clicks go to the label — the idiom `support/fixtures.ts` already uses. */
function otlpSwitch(page: Page) {
  const row = page.locator(".settings-row").filter({ has: page.getByText(TOOL_OTLP_TITLE, { exact: true }) });
  return { control: row.locator("label.switch").first(), input: row.locator("input[type=checkbox]").first() };
}

test.describe("Developer · extended tool telemetry (OTLP)", () => {
  test("the row is not reachable until developer mode is on", async ({ page, shell }) => {
    await openSettings(page, shell);
    // The Atlas box is drawn; the developer rows under it are not.
    await expect(page.getByText("Atlas", { exact: true })).toBeVisible();
    await expect(page.getByText(TOOL_OTLP_TITLE, { exact: true })).toHaveCount(0);
    await expect(page.getByText("Developer", { exact: true })).toHaveCount(0);

    await enterDeveloperMode(page);
    await expect(page.getByText(TOOL_OTLP_TITLE, { exact: true })).toBeVisible();
  });

  test("its title and body are the deprecation notice, verbatim, and it reads off", async ({ page, shell }) => {
    await openSettings(page, shell);
    await enterDeveloperMode(page);

    await expect(page.getByText(TOOL_OTLP_TITLE, { exact: true })).toHaveCount(1);
    await expect(page.getByText(TOOL_OTLP_BODY, { exact: true })).toHaveCount(1);
    // A daemon that has never been told otherwise sends no `tool_otlp` at all,
    // and the row must read that as OFF rather than as "unknown".
    await expect(otlpSwitch(page).input).not.toBeChecked();
    // Nothing tells anyone to restart anything while the lane is off.
    await expect(page.getByText(TOOL_OTLP_RESTART, { exact: true })).toHaveCount(0);
  });

  test("turning it on shows the restart instruction, once", async ({ page, shell }) => {
    await openSettings(page, shell);
    await enterDeveloperMode(page);

    const { control, input } = otlpSwitch(page);
    await control.click();
    await expect(input).toBeChecked();

    // Once. Two copies of one instruction reads as two things to do.
    await expect(page.getByText(TOOL_OTLP_RESTART, { exact: true })).toHaveCount(1);
    // And it is what the daemon was told, not just what the page drew.
    await expect.poll(() => shell.settings.tool_otlp).toBe(true);
    // Signal's own restart bar stays down: the restart belongs to the tool.
    await expect(page.getByText("Signal restarts to apply this.")).toHaveCount(0);

    // Off again takes the instruction away.
    await control.click();
    await expect(input).not.toBeChecked();
    await expect(page.getByText(TOOL_OTLP_RESTART, { exact: true })).toHaveCount(0);
    await expect.poll(() => shell.settings.tool_otlp).toBe(false);
  });

  /**
   * ⚠️ **NO FIXTURE MAY RENDER `broken · otel` WHILE THE SWITCH IS OFF**, and
   * this is checked over EVERY fixture rather than over one: with the lane not
   * expected, `Compute` cannot produce that pair at all (pinned Go-side in
   * `integrations/toolotlp_test.go`), so a fixture that showed it would be
   * teaching the pane a state the server can no longer send.
   */
  test("no fixture renders a broken otel lane", async ({ page, shell }) => {
    const files = fs.readdirSync(FIXTURES).filter((f) => f.endsWith(".json"));
    expect(files.length).toBeGreaterThan(0);
    for (const file of files) {
      const body = JSON.parse(fs.readFileSync(path.join(FIXTURES, file), "utf8")) as {
        integrations?: Array<{ id: string; state: string; broken_lane?: string }>;
      };
      for (const row of body.integrations || []) {
        expect(
          row.state === "broken" && row.broken_lane === "otel",
          `${file}: ${row.id} is broken · otel, which the server cannot produce with tool_otlp off`
        ).toBe(false);
      }
      shell.serve(body);
      await openPane(page, shell, WIDTHS[0]);
      // Rendered, too: the pane names the silent lane in the checklist, so a
      // fixture sneaking it in another way would still show here.
      await expect(page.locator('.intg-check[data-kind="otel"]').filter({ hasText: "nothing arrived" })).toHaveCount(0);
    }
  });
});
