import { test } from "node:test";
import assert from "node:assert/strict";
import { engineNotice } from "../app.js";

// A healthy machine gets NO card. The whole reason this moved out of the macOS
// installer is that it put a 315 MB download in front of somebody who had not
// asked for one; putting the same button on every page would be the same
// mistake one screen along.
test("a current engine says nothing at all", () => {
  assert.equal(engineNotice({ needed: true, installed: true, outdated: false, status: "idle" }), null);
});

// ml_backend "off" is a choice. The page must not offer to undo it.
test("a machine that needs no engine says nothing", () => {
  assert.equal(engineNotice({ needed: false, installed: false, status: "idle" }), null);
});

test("no engine at all earns a card with a download button", () => {
  const n = engineNotice({ needed: true, installed: false, status: "idle" });
  assert.equal(n.kind, "missing");
  assert.equal(n.action, "Download");
  assert.match(n.detail, /focus blocks/);
});

// ⚠️ BOTH VERSIONS, because "out of date" with no numbers is a claim the person
// cannot check — and the two halves ship as separate artifacts on separate
// cadences, which is exactly how a 2.3.0 daemon ran against an Aug-11 sidecar
// for three weeks while doctor reported no problems.
test("an outdated engine names what is installed and what is expected", () => {
  const n = engineNotice({
    needed: true, installed: true, outdated: true,
    version: "v3.0.4", expected: "3.0.5", status: "idle",
  });
  assert.equal(n.kind, "outdated");
  assert.equal(n.action, "Update");
  assert.match(n.detail, /v3\.0\.4/);
  assert.match(n.detail, /3\.0\.5/);
});

test("a download in flight shows a percentage and no button", () => {
  const n = engineNotice({ needed: true, installed: false, status: "running", received: 50, total: 100 });
  assert.equal(n.kind, "running");
  assert.equal(n.percent, 50);
  assert.equal(n.action, "");
});

// ⚠️ NEVER AN INVENTED NUMBER. A fetch whose total is unknown must not render
// "0%" — a bar that sits at zero while bytes are arriving is the progress
// indicator lying, which is what made the installer's pane unreadable.
test("an indeterminate download shows no percentage", () => {
  const n = engineNotice({ needed: true, installed: false, status: "running", received: 0, total: 0 });
  assert.equal(n.percent, null);
  assert.equal(n.detail, "Starting…");
});

// A failure has to carry its reason: "http status 504" means try later, "no
// space left on device" is a different action entirely.
test("a failed download keeps the server's reason verbatim", () => {
  const n = engineNotice({
    needed: true, installed: false, status: "failed",
    error: "retry: gave up after 5 attempt(s): http status 504",
  });
  assert.equal(n.kind, "failed");
  assert.equal(n.action, "Try again");
  assert.match(n.detail, /504/);
});

test("a failed download with no reason still says so honestly", () => {
  const n = engineNotice({ needed: true, installed: false, status: "failed", error: "" });
  assert.equal(n.detail, "No reason was reported.");
});

// The page loads before the first /v1/engine answer; that must render as
// nothing, not as "not installed".
test("no answer yet is not an absent engine", () => {
  assert.equal(engineNotice(null), null);
  assert.equal(engineNotice(undefined), null);
});
