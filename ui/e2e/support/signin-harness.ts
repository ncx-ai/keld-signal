import { spawn, type ChildProcess } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

/**
 * The sign-in suite's own bring-up: a FRESH, unpaired `keld-agent` per test,
 * pointed at the conformance mock Atlas (`keld-conform mockatlas`) on loopback.
 *
 * It does not share `scripts/e2e-up.sh`'s daemon, and cannot: that one is
 * PAIRED (it writes a hook.json so the rest of the suite has something to
 * show) and runs `KELD_ATLAS=0`, and both of those facts are exactly what
 * decide whether the first-open choice appears (`first_run` = not paired AND
 * send_to_atlas never set AND KELD_ATLAS unset). A sign-in test also changes
 * the machine it runs on — it writes auth.json and hook.json — so every test
 * gets its own home and nothing leaks between them.
 *
 * What it needs is small on purpose, so CI can run it on a stock runner: the
 * two Go binaries `signin-setup.ts` builds, and nothing else. No sidecar venv,
 * no corpus, no model — `ml_backend: "off"` means the daemon needs no analysis
 * service and never fetches the ~300 MB engine (the engine manager fetches only
 * when one is NEEDED, and "off" needs none), so the daemon itself makes no
 * request off this machine.
 */

export const SIGNIN_WORK = process.env.KELD_E2E_SIGNIN_WORK || path.join(os.tmpdir(), "keld-signal-e2e-signin");
export const BIN_DIR = path.join(SIGNIN_WORK, "bin");
export const AGENT_BIN = path.join(BIN_DIR, process.platform === "win32" ? "keld-agent.exe" : "keld-agent");
export const CONFORM_BIN = path.join(BIN_DIR, process.platform === "win32" ? "keld-conform.exe" : "keld-conform");

// The plan's rule for any daemon an agent starts: a telemetry port in
// 14400-14499, never the real 14318. One daemon at a time (workers: 1), and a
// restart waits for the previous process to exit before binding it again.
const TELEMETRY_PORT = process.env.KELD_E2E_TELEMETRY_PORT || "14411";

export type HarnessOptions = {
  /** KELD_TELEMETRY_PORT for this harness's daemon (default 14411, or
   *  KELD_E2E_TELEMETRY_PORT). The local-Atlas suite uses its own. */
  telemetryPort?: string;
};

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

function waitForLine(child: ChildProcess, re: RegExp, timeoutMs: number, what: string): Promise<RegExpMatchArray> {
  return new Promise((resolve, reject) => {
    let buf = "";
    const timer = setTimeout(() => reject(new Error(`${what}: no line matching ${re} within ${timeoutMs}ms; got: ${buf.slice(-2000)}`)), timeoutMs);
    const onData = (d: Buffer) => {
      buf += d.toString();
      const m = buf.match(re);
      if (m) {
        clearTimeout(timer);
        child.stdout?.off("data", onData);
        resolve(m);
      }
    };
    child.stdout?.on("data", onData);
    child.on("exit", (code) => {
      clearTimeout(timer);
      reject(new Error(`${what} exited ${code} before printing ${re}; got: ${buf.slice(-2000)}`));
    });
  });
}

function exited(child: ChildProcess): Promise<void> {
  if (child.exitCode !== null || child.signalCode !== null) return Promise.resolve();
  return new Promise((resolve) => child.once("exit", () => resolve()));
}

async function stopChild(child: ChildProcess | null, graceMs = 8000): Promise<void> {
  if (!child || child.exitCode !== null || child.signalCode !== null) return;
  child.kill("SIGTERM");
  const done = await Promise.race([exited(child).then(() => true), sleep(graceMs).then(() => false)]);
  if (!done) {
    child.kill("SIGKILL");
    await exited(child);
  }
}

/** Every KELD_* the developer's own shell carries is dropped before ours are
 *  added: an inherited KELD_ATLAS=0 would silently turn first_run off, and an
 *  inherited KELD_HOME would point the daemon at a real home. */
function cleanEnv(): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = {};
  for (const [k, v] of Object.entries(process.env)) {
    if (!k.startsWith("KELD_")) env[k] = v;
  }
  return env;
}

export type Daemon = { baseURL: string; secret: string; port: number };

export class SigninHarness {
  readonly root: string;
  readonly home: string;
  readonly atlasState: string;
  readonly logPath: string;
  /** The mock Atlas's URL, when this harness started one (startAtlas). */
  atlasURL = "";
  /** What the daemon is handed as KELD_API_URL and KELD_ATLAS_WEB_URL: the
   *  mock for both, or a real Atlas's two halves via useAtlas(). */
  apiURL = "";
  webURL = "";
  daemon: Daemon | null = null;
  private readonly telemetryPort: string;
  private atlas: ChildProcess | null = null;
  private agent: ChildProcess | null = null;
  private log: fs.WriteStream;

  constructor(name: string, opts: HarnessOptions = {}) {
    this.telemetryPort = opts.telemetryPort || TELEMETRY_PORT;
    for (const bin of [AGENT_BIN, CONFORM_BIN]) {
      if (!fs.existsSync(bin)) throw new Error(`${bin} is missing: signin-setup.ts did not build it, so nothing here can pass`);
    }
    fs.mkdirSync(SIGNIN_WORK, { recursive: true });
    this.root = fs.mkdtempSync(path.join(SIGNIN_WORK, `${name.replace(/[^a-z0-9]+/gi, "-").slice(0, 40)}-`));
    this.home = path.join(this.root, "home");
    this.atlasState = path.join(this.root, "atlas");
    this.logPath = path.join(this.root, "daemon.log");
    fs.mkdirSync(path.join(this.home, "state"), { recursive: true });
    this.log = fs.createWriteStream(this.logPath, { flags: "a" });
    // A fresh install's daemon-side defaults, minus the two that would reach
    // off this machine or rewrite tool configs. Deliberately WITHOUT
    // send_to_atlas: its absence is what the first-open choice keys on.
    fs.writeFileSync(
      path.join(this.home, "agent-config.json"),
      JSON.stringify({ ml_backend: "off", auto_setup_integrations: false }) + "\n"
    );
  }

  async startAtlas(): Promise<void> {
    this.atlas = spawn(CONFORM_BIN, ["mockatlas", "--port", "0", "--state", this.atlasState], {
      stdio: ["ignore", "pipe", "pipe"],
      env: cleanEnv(),
    });
    this.atlas.stderr?.on("data", (d) => this.log.write(`[mockatlas] ${d}`));
    const m = await waitForLine(this.atlas, /mockatlas listening on (http:\/\/127\.0\.0\.1:\d+)/, 15_000, "mock Atlas");
    this.atlasURL = m[1];
    this.apiURL = this.webURL = this.atlasURL;
  }

  /** Point the daemon at an Atlas this harness did NOT start — the real one
   *  the local-Atlas suite signs in against. Takes effect on the next
   *  startDaemon(). */
  useAtlas(apiURL: string, webURL: string): void {
    this.apiURL = apiURL;
    this.webURL = webURL;
  }

  /** Start (or restart) the daemon on this harness's home. Resolves once the
   *  NEW daemon answers its page routes with its NEW secret — agent.json is
   *  rewritten with a fresh secret and a fresh random port on every start. */
  async startDaemon(): Promise<Daemon> {
    if (!this.apiURL) throw new Error("start the mock Atlas (or useAtlas) first");
    const infoPath = path.join(this.home, "agent.json");
    const prevSecret = this.readAgentJSON()?.secret || "";
    const env: NodeJS.ProcessEnv = {
      ...cleanEnv(),
      HOME: this.home,
      USERPROFILE: this.home,
      KELD_HOME: this.home,
      KELD_API_URL: this.apiURL,
      KELD_ATLAS_WEB_URL: this.webURL,
      KELD_AUTH_NO_BROWSER: "1",
      KELD_TELEMETRY_PORT: this.telemetryPort,
      KELD_AUTOUPDATE: "0",
      KELD_WATCH_POLL: "2s",
    };
    this.agent = spawn(AGENT_BIN, ["run"], { stdio: ["ignore", "pipe", "pipe"], env });
    this.agent.stdout?.on("data", (d) => this.log.write(d));
    this.agent.stderr?.on("data", (d) => this.log.write(d));

    const deadline = Date.now() + 60_000;
    while (Date.now() < deadline) {
      if (this.agent.exitCode !== null) throw new Error(`keld-agent exited ${this.agent.exitCode} during start-up; see ${this.logPath}`);
      const info = fs.existsSync(infoPath) ? this.readAgentJSON() : null;
      if (info && info.port && info.secret && info.secret !== prevSecret) {
        const baseURL = `http://127.0.0.1:${info.port}`;
        try {
          const res = await fetch(`${baseURL}/v1/auth/state`, { headers: { "x-keld-agent-secret": info.secret } });
          if (res.ok) {
            this.daemon = { baseURL, secret: info.secret, port: info.port };
            return this.daemon;
          }
          if (res.status === 404) {
            throw new Error(`GET /v1/auth/state is not mounted on this keld-agent build (404): the daemon half of web sign-in is missing`);
          }
        } catch (err) {
          if (err instanceof Error && err.message.includes("not mounted")) throw err;
          // not listening yet
        }
      }
      await sleep(250);
    }
    throw new Error(`keld-agent did not answer /v1/auth/state within 60s; see ${this.logPath}`);
  }

  async stopDaemon(): Promise<void> {
    await stopChild(this.agent);
    this.agent = null;
    this.daemon = null;
  }

  /** The page URL `keld signal open` would hand a person. */
  pageURL(pane = "today"): string {
    if (!this.daemon) throw new Error("daemon not started");
    return `${this.daemon.baseURL}/?secret=${encodeURIComponent(this.daemon.secret)}#/${pane}`;
  }

  readAgentJSON(): { port: number; secret: string } | null {
    try {
      return JSON.parse(fs.readFileSync(path.join(this.home, "agent.json"), "utf8"));
    } catch {
      return null;
    }
  }

  readJSON(name: string): any {
    const p = path.join(this.home, name);
    return fs.existsSync(p) ? JSON.parse(fs.readFileSync(p, "utf8")) : null;
  }

  exists(name: string): boolean {
    return fs.existsSync(path.join(this.home, name));
  }

  /** How many requests the mock Atlas has seen per route. */
  async atlasCounts(): Promise<Record<string, number>> {
    if (!this.atlasURL) throw new Error("atlasCounts() needs the mock Atlas; this harness did not start one");
    const res = await fetch(`${this.atlasURL}/_conform/counts`);
    if (!res.ok) return {};
    const body = (await res.json()) as { counts?: Record<string, number> };
    return body.counts || {};
  }

  async close(): Promise<void> {
    await this.stopDaemon();
    await stopChild(this.atlas);
    this.atlas = null;
    await new Promise<void>((r) => this.log.end(() => r()));
    if (process.env.KELD_E2E_KEEP === "1") return;
    // Keep the daemon's log beside Playwright's artifacts for a failed run.
    try {
      const out = path.join(__dirname, "..", "test-results", "signin-logs");
      fs.mkdirSync(out, { recursive: true });
      fs.copyFileSync(this.logPath, path.join(out, `${path.basename(this.root)}.log`));
    } catch {
      // best effort
    }
    fs.rmSync(this.root, { recursive: true, force: true });
  }
}
