import { test } from "node:test";
import assert from "node:assert/strict";
import { engineNotice, REASON_TEXT } from "../app.js";

// A healthy machine gets NOTHING. The whole direction of travel here — wizard
// pane, then card with a button, then this — has been removing decisions
// nobody had the information to make.
test("a current engine says nothing at all", () => {
  assert.equal(engineNotice({ needed: true, installed: true, outdated: false, status: "idle" }), null);
});

// ml_backend "off" is a choice. Nothing here may undo it.
test("a machine that needs no engine says nothing", () => {
  assert.equal(engineNotice({ needed: false, installed: false, outdated: true, status: "idle" }), null);
});

// ⚠️ NO BUTTON ON THE ORDINARY PATH. The daemon starts its own fetch
// (engineManager.autoStart) because a mismatched engine is version SKEW, not a
// preference — the failure this project paid three silent weeks for. Offering
// "Update" would be asking permission for work already running.
test("an outdated engine reports, it does not ask", () => {
  const n = engineNotice({ needed: true, installed: true, outdated: true, version: "v3.0.4", status: "idle" });
  assert.equal(n.action, "", "an outdated engine must not render a button");
  assert.match(n.title, /Updating/);
});

test("a missing engine reports installing, also with no button", () => {
  const n = engineNotice({ needed: true, installed: false, outdated: false, status: "idle" });
  assert.equal(n.action, "");
  assert.match(n.title, /Installing/);
});

// "Updating" and "Installing" are different facts to the reader: one machine is
// catching up with itself, the other is getting the thing for the first time.
test("the verb distinguishes a first install from an update", () => {
  const install = engineNotice({ needed: true, installed: false, status: "running", received: 1, total: 10 });
  const update = engineNotice({ needed: true, installed: true, outdated: true, status: "running", received: 1, total: 10 });
  assert.match(install.title, /Installing/);
  assert.match(update.title, /Updating/);
});

test("a download in flight shows its percentage", () => {
  const n = engineNotice({ needed: true, installed: true, outdated: true, status: "running", received: 50, total: 100 });
  assert.equal(n.kind, "running");
  assert.equal(n.percent, 50);
  assert.equal(n.detail, "50%");
  assert.equal(n.action, "");
});

// ⚠️ NEVER AN INVENTED NUMBER. A bar sitting at 0% while bytes arrive is the
// progress indicator lying — which is how the installer pane read while its
// download had already finished.
test("an indeterminate download shows no percentage", () => {
  const n = engineNotice({ needed: true, installed: false, status: "running", received: 0, total: 0 });
  assert.equal(n.percent, null);
  assert.equal(n.detail, "Starting…");
});

// The version is the whole point of having watched it happen; a bar that just
// vanishes leaves somebody wondering whether it worked.
test("a finished update names the version it landed on", () => {
  const n = engineNotice({ needed: true, installed: true, outdated: false, status: "done", version: "v3.0.5-rc.5" });
  assert.equal(n.kind, "done");
  assert.match(n.detail, /v3\.0\.5-rc\.5/);
  assert.equal(n.action, "");
});

// A failure is the ONE place a human choice exists again: retry now, or leave
// it to the next daemon start. Both are said.
test("a failure keeps the reason and says Signal retries anyway", () => {
  const n = engineNotice({
    needed: true, installed: true, outdated: true, status: "failed",
    error: "retry: gave up after 5 attempt(s): http status 504",
  });
  assert.equal(n.kind, "failed");
  assert.equal(n.action, "Try again");
  assert.match(n.detail, /504/);
  assert.match(n.detail, /try again when it next starts/);
});

test("a failure with no reason still says so honestly", () => {
  const n = engineNotice({ needed: true, installed: false, status: "failed", error: "" });
  assert.match(n.detail, /No reason was reported/);
});

test("no answer yet is not an absent engine", () => {
  assert.equal(engineNotice(null), null);
  assert.equal(engineNotice(undefined), null);
});

// ⚠️ THE PILL AND THE BAR MUST NOT CONTRADICT EACH OTHER. The strip's own copy
// told people to "reinstall to update it" — an instruction for work the daemon
// is already doing, printed directly above a bar showing it happen.
test("the health pill no longer tells anyone to reinstall by hand", () => {
  assert.doesNotMatch(REASON_TEXT.sidecar_outdated, /reinstall/i);
  assert.match(REASON_TEXT.sidecar_outdated, /updating itself/i);
});
