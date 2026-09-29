import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { AGENT_BIN, BIN_DIR, CONFORM_BIN } from "./support/signin-harness";

const REPO_ROOT = path.resolve(__dirname, "..", "..");

/**
 * Build the two binaries the sign-in suite runs — this checkout's `keld-agent`
 * and `keld-conform` (which serves the mock Atlas) — once per run. Each test
 * then starts its own daemon and mock from them (support/signin-harness.ts).
 *
 * KELD_E2E_SIGNIN_NOBUILD=1 reuses what a previous run built, for iterating on
 * specs. A build failure throws: nothing in this suite can pass without them.
 */
export default async function signinSetup(): Promise<void> {
  if (process.env.KELD_E2E_SIGNIN_NOBUILD === "1" && fs.existsSync(AGENT_BIN) && fs.existsSync(CONFORM_BIN)) {
    console.log(`e2e-signin: reusing binaries in ${BIN_DIR} (KELD_E2E_SIGNIN_NOBUILD=1)`);
    return;
  }
  fs.mkdirSync(BIN_DIR, { recursive: true });
  const started = Date.now();
  for (const [out, pkg] of [
    [AGENT_BIN, "./cmd/keld-agent"],
    [CONFORM_BIN, "./cmd/keld-conform"],
  ]) {
    execFileSync("go", ["build", "-o", out, pkg], { cwd: REPO_ROOT, stdio: "inherit" });
  }
  console.log(`e2e-signin: built keld-agent + keld-conform in ${Math.round((Date.now() - started) / 1000)}s -> ${BIN_DIR}`);
}
