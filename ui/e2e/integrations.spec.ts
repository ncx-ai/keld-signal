import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { test, expect } from "./support/fixtures";
import type { E2EState } from "./global-setup";

/**
 * THE LIVE JOURNEYS — AC-2 (configure a tool from the pane), AC-3 (a tool
 * installed after Signal is detected and configured), AC-9 (Codex hook trust)
 * and AC-6's client half (Report a problem).
 *
 * ⚠️ **THESE RAN AS `test.skip(true, …)` AT DESCRIBE LEVEL AND NOW RUN FOR
 * REAL.** The header they carried said WS-C1 "is none of it merged yet"; it
 * merged in `d428ba4`, the report route was mounted on 2026-09-15, and the
 * phase-2 review recorded the stale claim as a finding of its own — a doc
 * describing shipped code as unbuilt is the thing that keeps a gap invisible,
 * which is why the correction is written down rather than quietly swapped.
 *
 * What these add over `integrations-states.spec.ts` is the half a fixture
 * cannot reach. That suite proves the pane RENDERS every state in the
 * vocabulary, at 1280 and at 400, against responses it wrote itself. These
 * prove the states are REAL: the daemon's own `Compute` reading wiring facts
 * back off disk, its own adapters writing a real config with a real backup,
 * its own detector poll, and its own report bundle landing in a file.
 *
 * ---------------------------------------------------------------------------
 * THE TWO PREREQUISITES THE PREVIOUS HEADER FLAGGED, AND HOW EACH IS SETTLED
 * ---------------------------------------------------------------------------
 *
 * 1. **HOME ISOLATION IS LOAD-BEARING, NOT INCIDENTAL — this file CREATES
 *    `<HOME>/.codex/config.toml` and lets the daemon rewrite it.** The
 *    catalogue, all three tool adapters and `watch.DiscoverRoots()` resolve
 *    `~/.codex` and friends through `os.UserHomeDir()` — i.e. `$HOME` — at CALL
 *    time, so `e2e-up.sh`'s existing export does cover them; that was verified
 *    by reading the same values twice in one process with HOME moved between
 *    the reads. It is now also ASSERTED by the harness before the suite starts:
 *    `e2e-up.sh` fails the bring-up if any row reports `installed`, because a
 *    fresh isolated HOME has no tool config directory in it and a developer
 *    machine has several. Every path in this file is additionally built from
 *    `state.home` through `fixtureHome()`, never from `process.env.HOME`, and
 *    that helper refuses to return a directory that is not inside the work dir.
 *
 * 2. **AC-2 NEEDS AUTO-SETUP OFF AND AC-3 NEEDS IT ON — one daemon, not two.**
 *    `Settings.AutoSetupEnabled` resolves env > `agent-config.json` > ON, and
 *    the detector re-reads it LIVE on every tick. So `e2e-up.sh` writes
 *    `auto_setup_integrations: false` into the FILE (an env var would freeze it
 *    for the daemon's whole life) and the AC-3 journey flips that file to
 *    `true`, exactly as the pane's own toggle would. A second fixture daemon
 *    would cost another three-minute bring-up to prove nothing extra.
 *
 * ⚠️ **`KELD_INTEGRATIONS_POLL` IS DELIBERATELY NOT SHORTENED.** The detector
 * runs at its shipped 60 s default and AC-3 waits 90 s for it, which is the
 * budget the conformance chain uses for the same reason: AC-3's criterion is
 * "listed within 60 s", and a test that moves the poll to two seconds stops
 * measuring the thing the criterion names. It costs this suite one minute. The
 * other journeys do not pay it — `GET /v1/integrations` recomputes from disk on
 * every request, so a state that does not depend on auto-setup lands on the
 * page's own 10 s poll.
 *
 * ⚠️ **THE JOURNEYS ARE SERIAL AND EACH LEAVES THE MACHINE WHERE THE NEXT ONE
 * NEEDS IT.** Codex is unconfigured exactly once, so AC-2 spends that moment
 * and AC-9 and the report journey build on the tool AC-2 configured; AC-3 uses
 * Gemini rather than Codex for the same reason. That is why this file has a
 * Playwright project to itself (`integrations-live`, chromium, last) — see the
 * note on it in `playwright.config.ts`.
 *
 * ⚠️ **THEY NEED A FRESH BRING-UP, SO `KELD_E2E_REUSE=1` CANNOT RE-RUN THEM.**
 * The tool directories these journeys create live in the fixture HOME and
 * survive the browser, so a reused daemon starts with Codex already configured
 * and the first assertion below fails — with a message saying so rather than
 * with a puzzle. That is the same constraint `devgen` and `map-project` have,
 * one notch stronger, and it is the price of driving the real thing.
 */

// The config keld is asked to merge into, and the bytes the backup must hold
// afterwards. Deliberately innocuous: the adapters refuse a config whose own
// sections would collide, and a conflict is a different journey.
const CODEX_CONFIG_BEFORE = 'model = "gpt-5-codex"\napproval_policy = "on-request"\n';
const GEMINI_SETTINGS_BEFORE = '{\n  "theme": "Default"\n}\n';

// A Codex session transcript. One `session_meta` record carries both facts the
// daemon reads off a transcript: the top-level `timestamp` that answers "when
// did the newest session start" and `payload.cli_version` that answers AC-7's
// version question. It carries no prompt, so planting it cannot enqueue
// enrichment work for a corpus the other specs are asserting on.
function codexSessionMeta(at: Date): string {
  return (
    JSON.stringify({
      type: "session_meta",
      timestamp: at.toISOString(),
      payload: { id: "e2e-codex-session", cli_version: "0.153.4" },
    }) + "\n"
  );
}

function geminiChat(at: Date): string {
  return JSON.stringify({ sessionId: "e2e-gemini", timestamp: at.toISOString(), messages: [] }) + "\n";
}

/**
 * A `[hooks.state]` section that does NOT approve keld's hooks — what Codex
 * writes into its own config.toml when a human approves somebody else's.
 *
 * ⚠️ Its PRESENCE is the point. `tools.CodexHooksTrusted` answers
 * `(trusted=false, known=false)` — "we cannot tell" — when the file carries no
 * `hooks.state` section at all, and `Compute` refuses to send such a machine to
 * a `/hooks` screen its Codex may not have. An untrusted machine is one where
 * the section EXISTS and keld's own positional keys are missing from it.
 */
const HOOKS_STATE_UNTRUSTED =
  '\n[hooks.state."/some/other/tool/hooks.json:user_prompt_submit:0:0"]\n' +
  "enabled = true\n" +
  'trusted_hash = "sha256:0000000000000000000000000000000000000000000000000000000000000000"\n';

/**
 * The same section with keld's two required events approved. The key is
 * `<source>:<snake_event>:<i>:<j>`, where the source is matched by BASENAME
 * (`config.toml` = the inline hooks keld writes) and i/j are the hook's index
 * in `[[hooks.<Event>]]` and inside that entry's `hooks` list. Both are 0
 * because keld writes exactly one entry per event with exactly one command —
 * which the journey ASSERTS against the written file rather than assuming, so a
 * release that ever writes two fails loudly instead of approving the wrong
 * index and testing nothing.
 *
 * `SessionStart` is deliberately absent: `codexTrustEvents` is
 * {UserPromptSubmit, Stop}, because those are the two that name a prompt.
 */
const HOOKS_STATE_TRUSTED =
  '\n[hooks.state."/home/e2e/.codex/config.toml:user_prompt_submit:0:0"]\n' +
  "enabled = true\n" +
  'trusted_hash = "sha256:1111111111111111111111111111111111111111111111111111111111111111"\n' +
  '\n[hooks.state."/home/e2e/.codex/config.toml:stop:0:0"]\n' +
  "enabled = true\n" +
  'trusted_hash = "sha256:2222222222222222222222222222222222222222222222222222222222222222"\n';

// The three instruction sentences, quoted here the way AC-9 quotes the middle
// one — verbatim, from integrations/vocabulary.go.
const INSTRUCTION_RESTART =
  "Restart this tool to finish — a session that started before its config was written keeps using the settings it launched with.";
const INSTRUCTION_APPROVAL =
  "Open Codex, run /hooks, approve the two keld hooks. Signal confirms here within a minute.";

/**
 * The isolated HOME, and the refusal that makes writing into it safe.
 *
 * ⚠️ NEVER `process.env.HOME`. This process is the developer's shell; the
 * daemon's home is the one `e2e-up.sh` made. Two guards rather than one,
 * because the failure is not a red test — it is this suite rewriting the
 * machine's real Codex config and pointing its real editors at a throwaway
 * loopback port.
 */
function fixtureHome(state: E2EState): string {
  const home = path.resolve(state.home || "");
  const work = path.resolve(state.work || "");
  if (!home || home === path.resolve(os.homedir())) {
    throw new Error(`refusing to run: state.home (${home}) is this user's real home directory`);
  }
  if (!work || !home.startsWith(work + path.sep)) {
    throw new Error(`refusing to run: state.home (${home}) is not inside the e2e work dir (${work})`);
  }
  return home;
}

function writeFile(file: string, body: string, mtime?: Date): void {
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(file, body);
  if (mtime) fs.utimesSync(file, mtime, mtime);
}

function minutesAgo(n: number): Date {
  return new Date(Date.now() - n * 60_000);
}

const CODEX_CONFIG = (home: string) => path.join(home, ".codex", "config.toml");
const CODEX_SESSION = (home: string) =>
  path.join(home, ".codex", "sessions", "2026", "09", "16", "rollout-e2e.jsonl");

test.describe("Integrations · live daemon", () => {
  // ⚠️ **THE SUITE'S 45 s PER-TEST TIMEOUT IS BELOW THE DETECTOR'S OWN 60 s
  // POLL, so a journey that waits for a tick cannot fit inside it.** AC-3
  // passed at 30.2 s on the first run and was killed at 45.1 s on the second —
  // same code, same daemon, a tick that happened to land later. A 90 s `expect`
  // budget is not a budget at all while the test around it dies at 45.
  //
  // The fix is the TEST budget, never the poll: shortening
  // KELD_INTEGRATIONS_POLL to make the default timeout fit would stop AC-3
  // measuring the 60 s its own criterion names. Three polls plus the page's own
  // 10 s refresh is the wait that has to be affordable, so 180 s it is.
  test.setTimeout(180_000);

  test.beforeEach(({ state }) => {
    test.skip(
      !state.integrationsMounted,
      "GET /v1/integrations did not answer 200 on this build (see e2e-up.sh's bring-up line); " +
        "the live journeys have no route to drive"
    );
  });

  test("the pane lists this machine's tools with the states the daemon computed", async ({ signal, page, state }) => {
    fixtureHome(state); // the same refusal, asserted before anything reads a row
    await signal.open("integrations");

    const rows = page.locator(".intg-row");
    await expect(rows.first()).toBeVisible();
    for (const id of ["claude_code", "codex", "gemini_cli", "cowork", "pi", "antigravity", "cursor"]) {
      await expect(page.locator(`.intg-row[data-integration="${id}"]`)).toHaveCount(1);
    }
    await expect(rows).toHaveCount(7);

    // ⚠️ **THE STATES ARE THIS MACHINE'S, AND THIS ASSERTION IS WHAT PROVES
    // IT.** The fixture HOME has no Codex, Gemini or Cowork directory in it; the
    // laptop this runs on has all three. So a row here reading anything but
    // `not_installed` means the daemon is reading a home nobody isolated — and
    // it says so before the journeys below write a single file.
    for (const id of ["codex", "gemini_cli", "cowork"]) {
      await expect(
        page.locator(`.intg-row[data-integration="${id}"] .intg-state`),
        `${id} should be not_installed in a FRESH fixture HOME. If this run reused a daemon ` +
          `(KELD_E2E_REUSE=1), a previous run's journeys already created that directory — these ` +
          `journeys need a fresh bring-up. If it did not, HOME is not isolated: stop and check.`
      ).toHaveText("not_installed");
    }
    for (const id of ["pi", "antigravity", "cursor"]) {
      await expect(page.locator(`.intg-row[data-integration="${id}"] .intg-state`)).toHaveText("unsupported");
    }

    // ⚠️ **`claude_code` IS DELIBERATELY NOT IN THAT LIST, AND THE REASON IS THE
    // SUITE'S OWN DOING.** `devgen.spec.ts` presses the developer "Generate
    // block" control, and `daemon/devgen.go` writes a real synthetic transcript
    // to `<HOME>/.claude/projects/…` — the real Claude Code location, which is
    // what makes the generated block indistinguishable from a captured one. That
    // project is a DEPENDENCY of this one, so by the time these journeys run the
    // directory exists and the row correctly reads `not_configured`. Asserting
    // `not_installed` there would be asserting the absence of the suite's own
    // work, and it failed exactly that way the first time this ran.
    await expect(page.locator('.intg-row[data-integration="claude_code"] .intg-state')).not.toHaveText(
      "not_installed"
    );
    // Every pill came out of the published vocabulary: the pane maps nothing,
    // so a state it does not recognise renders as "unknown state: <value>".
    await expect(page.getByText(/^unknown state: /)).toHaveCount(0);

    // ⚠️ AC-7, for free and from a real read: the corpus this daemon watches is
    // Claude Code's, so its row carries the version off the newest transcript —
    // while `installed` is false, because the config DIRECTORY is a different
    // question from whether we can read the tool's work.
    await expect(page.locator('.intg-row[data-integration="claude_code"] .intg-version')).not.toHaveText("—");

    // The narrow end, on the live pane rather than a fixture. AC-2 names both
    // widths; `integrations-states.spec.ts` covers every STATE at both, and
    // this covers the states this machine is really in at the narrow one.
    await page.setViewportSize({ width: 400, height: 900 });
    await expect(page.locator('.intg-row[data-integration="codex"] .intg-state')).toHaveText("not_installed");
    await expect(page.locator('.intg-row[data-integration="cursor"] .intg-state')).toBeVisible();
  });

  test("AC-2: a tool that appears after Signal is offered Set up, and the click writes a backup", async ({
    signal,
    page,
    state,
  }) => {
    const home = fixtureHome(state);

    // The tool appears AFTER the daemon started, with a session already two
    // hours old — which is the machine AC-3's second half describes and the one
    // that makes `restart_required` the honest answer after the write.
    writeFile(CODEX_CONFIG(home), CODEX_CONFIG_BEFORE, minutesAgo(120));
    writeFile(CODEX_SESSION(home), codexSessionMeta(minutesAgo(120)), minutesAgo(120));

    await signal.open("integrations");
    const row = page.locator('.intg-row[data-integration="codex"]');

    // ⚠️ This is also AC-3's OTHER half — "with `auto_setup_integrations` off,
    // the row waits in `not_configured`" — asserted where it is actually true.
    // The fixture daemon has the toggle off, so the row is still waiting when
    // the person arrives, and the button below is the only thing that writes.
    await expect(row.locator(".intg-state")).toHaveText("not_configured", { timeout: 90_000 });
    await expect(row.locator(".intg-version")).toHaveText("0.153.4"); // AC-7, read not guessed

    const setUp = row.getByRole("button", { name: "Set up" });
    await expect(setUp).toBeVisible();
    await setUp.click();

    const result = row.locator(".intg-result");
    await expect(result).toContainText("Previous config saved to");
    await expect(result).toContainText("Restart the tool to finish.");

    // ⚠️ THE BACKUP IS A FILE, NOT A SENTENCE. The path the page printed is
    // opened and compared byte for byte against what was there before — the
    // half of "applies the tool's adapter with a backup" that a rendered string
    // cannot establish.
    const printed = (await result.textContent()) || "";
    const backup = /Previous config saved to (\S+)/.exec(printed)?.[1];
    expect(backup, `no backup path in the printed result: ${printed}`).toBeTruthy();
    expect(fs.readFileSync(backup as string, "utf8")).toBe(CODEX_CONFIG_BEFORE);

    // And the live config is the person's own settings PLUS keld's block —
    // merged by the daemon's own adapter, not replaced.
    const after = fs.readFileSync(CODEX_CONFIG(home), "utf8");
    expect(after).toContain('model = "gpt-5-codex"');
    expect(after).toContain("[[hooks.UserPromptSubmit]]");
    expect(after).toContain("[otel]");

    // ⚠️ The new state comes from the NEXT POLL, out of `Compute`, never from
    // the button: the session on this machine started before the config was
    // written, so it is still using what it launched with.
    await expect(row.locator(".intg-state")).toHaveText("restart_required", { timeout: 30_000 });
    await expect(row.locator(".intg-instruction", { hasText: INSTRUCTION_RESTART })).toHaveCount(1);
    await expect(setUp).toHaveCount(0);
  });

  test("AC-3: with auto-setup on, the detector configures it and the pane says so without a click", async ({
    signal,
    page,
    state,
  }) => {
    const home = fixtureHome(state);

    // Turn the toggle on the way the pane's own switch would: the detector
    // re-reads agent-config.json on every tick, so no restart is involved.
    const configPath = path.join(home, "agent-config.json");
    const config = JSON.parse(fs.readFileSync(configPath, "utf8"));
    expect(config.auto_setup_integrations, "the fixture daemon should start with auto-setup OFF").toBe(false);
    config.auto_setup_integrations = true;
    fs.writeFileSync(configPath, JSON.stringify(config));

    // ⚠️ GEMINI, NOT CODEX. A tool is unconfigured exactly once, and AC-2 spent
    // Codex's one such moment on the button. Using it again here would mean
    // asserting that a detector configured something already in the manifest,
    // which it correctly refuses to do.
    writeFile(path.join(home, ".gemini", "settings.json"), GEMINI_SETTINGS_BEFORE, minutesAgo(120));
    writeFile(path.join(home, ".gemini", "tmp", "e2e", "chats", "session.jsonl"), geminiChat(minutesAgo(120)), minutesAgo(120));

    await signal.open("integrations");
    const row = page.locator('.intg-row[data-integration="gemini_cli"]');

    // The pane says so, off the server's own `auto_setup` boolean.
    await expect(page.getByText("new tools are configured automatically")).toBeVisible({ timeout: 30_000 });

    // NO CLICK ANYWHERE IN THIS TEST. The detector's own poll is the only thing
    // that can move this row, and the budget is 90 s against its shipped 60 s.
    await expect(row.getByRole("button", { name: "Set up" })).toHaveCount(0, { timeout: 90_000 });
    await expect(row.locator(".intg-state")).toHaveText("restart_required", { timeout: 90_000 });
    await expect(row.locator(".intg-instruction", { hasText: INSTRUCTION_RESTART })).toHaveCount(1);

    // "configured with a backup" — read out of keld's own manifest, which is
    // what `configured` is computed from, and opened to prove it is the file.
    const manifest = JSON.parse(fs.readFileSync(path.join(home, "manifest.json"), "utf8"));
    expect(Object.keys(manifest.tools || {})).toContain("gemini"); // the adapter name, not the source id
    const backup = manifest.tools.gemini.backup_path;
    expect(backup).toBeTruthy();
    expect(fs.readFileSync(backup as string, "utf8")).toBe(GEMINI_SETTINGS_BEFORE);
  });

  test("AC-9: an untrusted Codex hook reads approval_required, and clears when hooks.state says trusted", async ({
    signal,
    page,
    state,
  }) => {
    const home = fixtureHome(state);
    const written = fs.readFileSync(CODEX_CONFIG(home), "utf8");

    // Codex's trust keys are POSITIONAL, so the indices below are only right
    // while keld writes one entry per event with one command in it. Asserted,
    // not assumed: a release that writes two fails here instead of silently
    // approving an index nothing is at.
    expect((written.match(/\[\[hooks\.UserPromptSubmit\]\]/g) || []).length).toBe(1);
    expect((written.match(/\[\[hooks\.Stop\]\]/g) || []).length).toBe(1);

    // The machine AC-9 describes: configured, restarted (the session below is
    // NEWER than the config, so `restart_required` is not the answer), and
    // Codex is holding the hooks back. Written in that order, with the config's
    // mtime pinned behind the session's instant, so the ordering is a fact
    // rather than a race between two writes a millisecond apart.
    const sessionAt = new Date();
    writeFile(CODEX_SESSION(home), codexSessionMeta(sessionAt), sessionAt);
    writeFile(CODEX_CONFIG(home), written + HOOKS_STATE_UNTRUSTED, new Date(sessionAt.getTime() - 60_000));

    await signal.open("integrations");
    const row = page.locator('.intg-row[data-integration="codex"]');

    await expect(row.locator(".intg-state")).toHaveText("approval_required", { timeout: 90_000 });
    await expect(row.locator(".intg-instruction", { hasText: INSTRUCTION_APPROVAL })).toHaveCount(1);
    // Never `broken` — the tool is not faulty, it is waiting on a human.
    await expect(row.locator(".intg-state")).not.toHaveText("broken");
    // And the hook lane never reads working before approval.
    const hookLane = row.locator('.intg-check[data-kind="hook"]');
    await expect(hookLane).toHaveAttribute("data-ok", "false");
    await expect(hookLane).toContainText("not trusted yet");

    // Approval clears it, and the answer is READ BACK off the file rather than
    // remembered: nothing about this daemon changed between the two halves
    // except the bytes in config.toml.
    const secondSessionAt = new Date();
    writeFile(CODEX_SESSION(home), codexSessionMeta(secondSessionAt), secondSessionAt);
    writeFile(CODEX_CONFIG(home), written + HOOKS_STATE_TRUSTED, new Date(secondSessionAt.getTime() - 60_000));

    // `idle`, not `working`: this fixture has no lane traffic at all, and a
    // quiet machine is idle. The point is that it is no longer held on a human.
    await expect(row.locator(".intg-state")).toHaveText("idle", { timeout: 90_000 });
    await expect(row.locator(".intg-instruction", { hasText: INSTRUCTION_APPROVAL })).toHaveCount(0);
    await expect(hookLane).toHaveAttribute("data-ok", "true");
  });

  test("Report a problem writes a bundle and names where it went", async ({ signal, page, state }) => {
    const home = fixtureHome(state);
    await signal.open("integrations");

    // ⚠️ CODEX, WHERE THIS USED TO SAY CLAUDE CODE. `Report a problem` is
    // offered on a CONFIGURED row, and Codex is the one tool this suite
    // configured deliberately, through the button, in AC-2 — so it is the only
    // row whose affordance does not depend on which other projects ran first.
    // Claude Code's directory exists here only because `devgen` wrote a
    // transcript into it, and whether it is CONFIGURED depends on whether AC-3
    // has already turned auto-setup on. Asserting against that would be
    // asserting an ordering, not a journey.
    const row = page.locator('.intg-row[data-integration="codex"]');
    const report = row.getByRole("button", { name: "Report a problem" });
    await expect(report).toBeVisible();
    await report.click();

    const result = row.locator(".intg-result");
    await expect(result).toContainText("Report written to");

    // The bundle is a FILE under ~/.keld/reports, opened here. WS-C2 owns what
    // is inside it; this asserts the route answered with a path that exists,
    // which is the failure the button exists to end.
    const printed = (await result.textContent()) || "";
    const bundle = /Report written to (\S+)/.exec(printed)?.[1];
    expect(bundle).toBeTruthy();
    expect(fs.existsSync(bundle as string)).toBe(true);
    expect(path.dirname(bundle as string)).toBe(path.join(home, "reports"));
    expect(JSON.parse(fs.readFileSync(bundle as string, "utf8")).source).toBe("codex");
  });
});
