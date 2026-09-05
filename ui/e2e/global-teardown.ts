import fs from "node:fs";
import path from "node:path";
import { STATE_FILE, WORK_DIR } from "./playwright.config";
import type { E2EState } from "./global-setup";

function alive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

function signal(pid: number, sig: NodeJS.Signals): void {
  try {
    process.kill(pid, sig);
  } catch {
    // already gone
  }
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/**
 * Stop the isolated daemon and everything it spawned, then remove the temp
 * home. SIGTERM to the daemon first — that is the path that lets it stop its
 * sidecar's process group cleanly (AGENTS.md: a supervisor kill reaps the
 * GROUP) — then SIGKILL the bring-up script's group and the sidecar's own
 * group unconditionally, because a tidy exit can still leave a straggler.
 */
export default async function globalTeardown(): Promise<void> {
  if (!fs.existsSync(STATE_FILE)) return;
  const state = JSON.parse(fs.readFileSync(STATE_FILE, "utf8")) as E2EState;

  if (process.env.KELD_E2E_REUSE === "1") {
    console.log("e2e: KELD_E2E_REUSE=1 — leaving the daemon running");
    return;
  }

  signal(state.daemonPid, "SIGTERM");
  for (let i = 0; i < 60 && alive(state.daemonPid); i++) await sleep(250);

  // Group kills. Never our own group: the script ran detached, so its pgid is
  // its own pid, but guard anyway — a wrong negative pid here would take the
  // test runner down with it.
  if (state.pgid && state.pgid !== process.pid) signal(-state.pgid, "SIGKILL");
  try {
    const sidecarPid = Number(fs.readFileSync(state.sidecarPidFile, "utf8").trim());
    if (sidecarPid > 1) {
      signal(-sidecarPid, "SIGKILL");
      signal(sidecarPid, "SIGKILL");
    }
  } catch {
    // no sidecar pid file: it never started, or the daemon already reaped it
  }
  signal(state.daemonPid, "SIGKILL");

  // Keep the daemon log next to the Playwright artifacts for a failed run's
  // post-mortem; the temp home itself (corpus, checkouts, state) goes.
  try {
    fs.mkdirSync(path.join(__dirname, "test-results"), { recursive: true });
    fs.copyFileSync(state.log, path.join(__dirname, "test-results", "daemon.log"));
  } catch {
    // best effort
  }
  if (process.env.KELD_E2E_KEEP === "1") {
    console.log(`e2e: KELD_E2E_KEEP=1 — leaving ${WORK_DIR} in place`);
    return;
  }
  fs.rmSync(WORK_DIR, { recursive: true, force: true });
}
