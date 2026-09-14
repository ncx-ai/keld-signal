import { spawn } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { STATE_FILE, WORK_DIR } from "./playwright.config";

const REPO_ROOT = path.resolve(__dirname, "..", "..");
const UP_SCRIPT = path.join(REPO_ROOT, "scripts", "e2e-up.sh");

export type E2EState = {
  baseURL: string;
  secret: string;
  port: number;
  daemonPid: number;
  pgid: number;
  sidecarPidFile: string;
  home: string;
  work: string;
  log: string;
  settingsMounted: boolean;
  blocks: number;
  intended: number;
};

function alive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

/**
 * Bring the isolated daemon up (scripts/e2e-up.sh) and leave state.json for
 * the fixtures. The script is started in its OWN process group (detached), so
 * the daemon it leaves running is reapable as a group by global-teardown even
 * if the daemon's pid alone has gone.
 *
 * KELD_E2E_REUSE=1 skips the bring-up when a previous run's daemon is still
 * alive — a developer convenience for iterating on specs, never the default.
 */
export default async function globalSetup(): Promise<void> {
  if (process.env.KELD_E2E_REUSE === "1" && fs.existsSync(STATE_FILE)) {
    const prev = JSON.parse(fs.readFileSync(STATE_FILE, "utf8")) as E2EState;
    if (alive(prev.daemonPid)) {
      console.log(`e2e: reusing running daemon at ${prev.baseURL} (KELD_E2E_REUSE=1)`);
      return;
    }
  }

  fs.mkdirSync(WORK_DIR, { recursive: true });
  fs.rmSync(STATE_FILE, { force: true });

  const started = Date.now();
  const code = await new Promise<number>((resolve, reject) => {
    const child = spawn("bash", [UP_SCRIPT, WORK_DIR], {
      cwd: REPO_ROOT,
      detached: true,
      stdio: ["ignore", "inherit", "inherit"],
      env: { ...process.env },
    });
    child.on("error", reject);
    child.on("exit", (c) => resolve(c ?? 1));
  });
  if (code !== 0) {
    throw new Error(`scripts/e2e-up.sh exited ${code}; the daemon is not reachable, so nothing can pass`);
  }
  if (!fs.existsSync(STATE_FILE)) {
    throw new Error(`scripts/e2e-up.sh exited 0 but wrote no ${STATE_FILE}`);
  }
  const state = JSON.parse(fs.readFileSync(STATE_FILE, "utf8")) as E2EState;
  if (!alive(state.daemonPid)) {
    throw new Error(`daemon pid ${state.daemonPid} is not alive after bring-up (see ${state.log})`);
  }
  console.log(
    `e2e: daemon up at ${state.baseURL} in ${Math.round((Date.now() - started) / 1000)}s — ` +
      `${state.blocks}/${state.intended} blocks, /v1/settings ${state.settingsMounted ? "mounted" : "NOT mounted"}`
  );
}
