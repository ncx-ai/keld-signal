import test from "node:test";
import assert from "node:assert/strict";
import {
  readonlyNote,
  settingsErrorText,
  configErrorText,
  localOnlyConfirmationText,
  startAtLoginProps,
  projectRulesSummary,
  restartBarText,
  nextRestartStatus,
  RESTART_IDLE,
  RESTART_NEEDED,
  RESTART_RESTARTING,
  RESTART_WAITING,
  RESTART_READY,
} from "../app.js";

// --- readonlyNote: "set by KELD_… on this machine" (the D2 brief), for every
// key GET /v1/settings' `readonly` can name. ---

test("readonlyNote names the real env var for each of the three v3 keys that support one", () => {
  assert.equal(readonlyNote("send_to_atlas"), "Set by KELD_ATLAS on this machine.");
  assert.equal(readonlyNote("dev_blocks"), "Set by KELD_DEV_BLOCKS on this machine.");
  assert.equal(readonlyNote("attribution"), "Set by KELD_ATTRIBUTION on this machine.");
});

test("readonlyNote still says something sensible for a key it doesn't know about", () => {
  const note = readonlyNote("some_future_key");
  assert.match(note, /^Set by KELD_/);
  assert.match(note, /on this machine\.$/);
});

// --- settingsErrorText: PUT /v1/settings' one documented refusal, shown
// verbatim, and never swallowed for an unmapped or transport-level failure. ---

test("the documented dev_blocks refusal renders as a real sentence, not the raw code", () => {
  const text = settingsErrorText(409, { error: "turn_off_send_to_atlas_first" });
  assert.match(text, /Send to Atlas/i);
  assert.notEqual(text, "turn_off_send_to_atlas_first");
});

test("an error code this page doesn't map still surfaces verbatim rather than a generic shrug", () => {
  assert.equal(settingsErrorText(409, { error: "some_new_reason" }), "some_new_reason");
});

test("a transport failure (status 0, no body) gets a plain retry sentence, not a crash or silence", () => {
  const text = settingsErrorText(0, null);
  assert.ok(text.length > 0);
});

test("a 500 with no error field gets a plain retry sentence", () => {
  const text = settingsErrorText(500, {});
  assert.ok(text.length > 0);
  assert.notEqual(text, "");
});

// --- configErrorText: POST /v1/config's 400 and 409. ---

test("400 reads as a setup-code problem", () => {
  assert.equal(configErrorText(400, {}), "That does not look like a setup code");
});

test("409 (Atlas off) tells the user what to do about it, naming Send to Atlas", () => {
  const text = configErrorText(409, {});
  assert.match(text, /Send to Atlas/i);
});

test("an unmapped status with an error body still shows something, never silence", () => {
  assert.equal(configErrorText(422, { error: "weird" }), "weird");
});

test("a network failure (status 0) still shows something actionable", () => {
  const text = configErrorText(0, null);
  assert.ok(text.length > 0);
});

// --- localOnlyConfirmationText: the one sentence every /v1/projects mutation's
// local_only:true renders as. Must never imply the org learned anything. ---

test("localOnlyConfirmationText says the change is local and points at Atlas for the org-wide edit", () => {
  const text = localOnlyConfirmationText();
  assert.match(text, /Applied on this machine/);
  assert.match(text, /Atlas/);
  assert.doesNotMatch(text, /synced|published|sent to Atlas|the org (now )?knows/i);
});

// --- startAtLoginProps: page convention 4 — not a working toggle until D9. ---

test("Start at login always renders unchecked, disabled, and says where it really lives", () => {
  const props = startAtLoginProps();
  assert.equal(props.checked, false);
  assert.equal(props.disabled, true);
  assert.equal(props.note, "in the desktop app");
});

// --- projectRulesSummary: the response's `rules`, never raw repos/keywords. ---

test("projectRulesSummary reads `rules`, not `repos`, when the two disagree", () => {
  // A project whose declared repos include something NOT in the matched
  // `rules` the daemon actually computed (e.g. a keyword that hasn't matched
  // any observed block yet) must summarise from `rules`, or the card would
  // show a candidate as if it were a real rule.
  const p = { repos: ["github.com/org/real-repo", "github.com/org/unmatched-candidate"], rules: ["github.com/org/real-repo"], ticket_key: "" };
  const summary = projectRulesSummary(p);
  assert.match(summary, /real-repo/);
  assert.doesNotMatch(summary, /unmatched-candidate/);
});

test("projectRulesSummary appends the ticket key rule alongside repo rules", () => {
  const p = { repos: [], rules: ["github.com/org/a"], ticket_key: "KELD" };
  const summary = projectRulesSummary(p);
  assert.match(summary, /repo github\.com\/org\/a/);
  assert.match(summary, /tickets KELD-xxx/);
});

test("projectRulesSummary says so plainly when a project has no rules at all", () => {
  assert.equal(projectRulesSummary({ repos: [], rules: [], ticket_key: "" }), "no rules yet");
});

test("projectRulesSummary falls back to `repos` only for a payload that predates `rules`", () => {
  const p = { repos: ["github.com/org/a", "github.com/org/b"], ticket_key: "" };
  assert.match(projectRulesSummary(p), /repo github\.com\/org\/a \+1/);
});

// --- restart bar: restartBarText + nextRestartStatus, the full sequence. ---

test("restartBarText has no text at all while idle", () => {
  assert.equal(restartBarText(RESTART_IDLE), "");
});

test("restartBarText names Signal restarting once a restart is needed", () => {
  assert.match(restartBarText(RESTART_NEEDED), /restarts/i);
});

test("restartBarText reads the same 'restarting' word through both the request and the wait", () => {
  assert.equal(restartBarText(RESTART_RESTARTING), restartBarText(RESTART_WAITING));
  assert.match(restartBarText(RESTART_WAITING), /restarting/i);
});

test("restartBarText says Signal is back once the daemon answers again", () => {
  assert.match(restartBarText(RESTART_READY), /back/i);
});

test("nextRestartStatus walks the full sequence: idle -> needed -> restarting -> waiting -> ready", () => {
  let s = RESTART_IDLE;
  s = nextRestartStatus(s, "restart_required");
  assert.equal(s, RESTART_NEEDED);
  s = nextRestartStatus(s, "clicked");
  assert.equal(s, RESTART_RESTARTING);
  s = nextRestartStatus(s, "sent");
  assert.equal(s, RESTART_WAITING);
  // a 503/network failure while polling is a NO-OP, never a regression to an
  // earlier state — the bar keeps saying "restarting" until the daemon is
  // actually back, however many failed polls that takes.
  s = nextRestartStatus(s, "ledger_down");
  assert.equal(s, RESTART_WAITING);
  s = nextRestartStatus(s, "ledger_ok");
  assert.equal(s, RESTART_READY);
});

test("nextRestartStatus ignores an event that doesn't apply to the current state", () => {
  assert.equal(nextRestartStatus(RESTART_IDLE, "clicked"), RESTART_IDLE);
  assert.equal(nextRestartStatus(RESTART_NEEDED, "ledger_ok"), RESTART_NEEDED);
  assert.equal(nextRestartStatus(RESTART_READY, "restart_required"), RESTART_READY);
});
