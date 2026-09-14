import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import path from "node:path";
import {
  serviceAlert,
  serviceHeadline,
  serviceReasonText,
  serviceQueueNote,
  serviceButtonProps,
  serviceProgressText,
  nextServiceRestart,
  reconcileServiceRestart,
  navHealthState,
  healthLabel,
  SERVICE_RESTART_IDLE,
  SERVICE_RESTART_SENDING,
  SERVICE_RESTART_ACCEPTED,
  SERVICE_RESTART_FAILED,
} from "../app.js";

// GET /v1/ledger's `service` block, per the pinned contract:
//   { state: ok|degraded|restarting|stuck|not_applicable,
//     reason: "human sentence, empty when ok",
//     failures: 0 }
const ledgerWith = (service) => ({ blocks: [], health: [], service });

// The sentence a real `stuck` carries — it is the one that says restarting was
// already tried, which is exactly what a lookup table would destroy.
const STUCK_REASON =
  "Signal restarted the analysis service twice and it still isn't answering. 290 blocks are waiting.";

// --- serviceAlert: what is worth saying, and the four different silences. ---

test("`ok` produces no alarm at all — a healthy machine shows nothing and offers nothing", () => {
  assert.equal(serviceAlert(ledgerWith({ state: "ok", reason: "", failures: 0 })), null);
});

test("`not_applicable` produces no alarm — a machine with no analysis service installed is not broken", () => {
  assert.equal(
    serviceAlert(ledgerWith({ state: "not_applicable", reason: "No analysis service on this machine.", failures: 0 })),
    null
  );
});

test("⚠️ an ABSENT `service` key renders nothing — an older daemon cannot say, and 'cannot say' is never 'fine'", () => {
  // The whole ledger a pre-`service` daemon sends: blocks and health, no
  // `service` key. Rendering "all good" here would be a confident negative
  // from a check that did not run.
  assert.equal(serviceAlert({ blocks: [], health: [{ key: "daemon", status: "ok" }] }), null);
  assert.equal(serviceAlert({ blocks: [], health: [], service: null }), null);
  assert.equal(serviceAlert({}), null);
  assert.equal(serviceAlert(null), null);
  assert.equal(serviceAlert(undefined), null);
});

test("a `service` value that is not an object is dropped rather than coerced into an alarm", () => {
  assert.equal(serviceAlert(ledgerWith("degraded")), null);
  assert.equal(serviceAlert(ledgerWith(["degraded"])) , null);
});

test("a state string this page does not recognise is DROPPED, never rendered as an alarm or as health", () => {
  // A newer daemon inventing a sixth value must not make this page assert
  // anything about it — the same refusal the Go side makes for an unknown
  // dynamics status.
  assert.equal(serviceAlert(ledgerWith({ state: "evicted", reason: "?", failures: 1 })), null);
  assert.equal(serviceAlert(ledgerWith({ state: "", reason: "", failures: 0 })), null);
  assert.equal(serviceAlert(ledgerWith({ reason: "no state field at all" })), null);
});

test("`degraded` and `stuck` both produce an alarm carrying the server's own reason", () => {
  const degraded = serviceAlert(ledgerWith({ state: "degraded", reason: "The analysis service stopped answering.", failures: 0 }));
  assert.equal(degraded.state, "degraded");
  assert.equal(degraded.reason, "The analysis service stopped answering.");

  const stuck = serviceAlert(ledgerWith({ state: "stuck", reason: STUCK_REASON, failures: 2 }));
  assert.equal(stuck.state, "stuck");
  assert.equal(stuck.failures, 2);
});

test("`restarting` produces an alarm too — the page reflects the server's restart, it does not wait to be told by a button", () => {
  const alert = serviceAlert(ledgerWith({ state: "restarting", reason: "Restart in progress.", failures: 1 }));
  assert.equal(alert.state, "restarting");
});

test("while the daemon is unreachable the service banner stands down — the cached block is a stale fact and Restart could reach nothing", () => {
  const stale = ledgerWith({ state: "stuck", reason: STUCK_REASON, failures: 3 });
  assert.notEqual(serviceAlert(stale), null);
  assert.equal(serviceAlert(stale, { offline: true }), null);
});

test("a malformed reason/failures does not crash the alarm — the state is still worth showing", () => {
  const alert = serviceAlert(ledgerWith({ state: "stuck", reason: 42, failures: "many" }));
  assert.equal(alert.reason, "");
  assert.equal(alert.failures, 0);
});

// --- Copy. ---

test("the reason is shown VERBATIM for `stuck` — it is the sentence that says restarting was already tried", () => {
  const alert = serviceAlert(ledgerWith({ state: "stuck", reason: STUCK_REASON, failures: 2 }));
  assert.equal(serviceReasonText(alert), STUCK_REASON);
});

test("an unmapped reason sentence is never routed through a lookup table that would generalise it away", () => {
  const odd = "The analysis service is answering /health but refusing every job.";
  const alert = serviceAlert(ledgerWith({ state: "degraded", reason: odd, failures: 0 }));
  assert.equal(serviceReasonText(alert), odd);
});

test("an EMPTY reason says we were not told, rather than inventing a cause", () => {
  const alert = serviceAlert(ledgerWith({ state: "degraded", reason: "", failures: 0 }));
  const text = serviceReasonText(alert);
  assert.ok(text.length > 0);
  assert.match(text, /didn't say why/i);
});

test("no service copy leaks the internal word 'sidecar' — the same rule healthLabel already follows", () => {
  const copy = [
    serviceHeadline("degraded"),
    serviceHeadline("stuck"),
    serviceHeadline("restarting"),
    serviceQueueNote("degraded"),
    serviceQueueNote("stuck"),
    serviceProgressText(SERVICE_RESTART_SENDING),
    serviceProgressText(SERVICE_RESTART_ACCEPTED),
    serviceProgressText(SERVICE_RESTART_FAILED),
    serviceButtonProps(SERVICE_RESTART_IDLE, "stuck").label,
  ].join(" | ");
  assert.doesNotMatch(copy, /sidecar|launchctl|kickstart|spool/i, copy);
  // and it uses the name the health strip already established
  assert.match(serviceHeadline("stuck"), new RegExp(healthLabel("sidecar"), "i"));
});

test("the headlines tell `degraded` and `stuck` apart, and say nothing at all for a silent state", () => {
  assert.notEqual(serviceHeadline("degraded"), serviceHeadline("stuck"));
  assert.equal(serviceHeadline("ok"), "");
  assert.equal(serviceHeadline("not_applicable"), "");
  assert.equal(serviceHeadline(undefined), "");
});

test("the queue note appears only while work is piling up, never while a restart is already under way", () => {
  assert.match(serviceQueueNote("degraded"), /still being recorded/i);
  assert.match(serviceQueueNote("stuck"), /still being recorded/i);
  assert.equal(serviceQueueNote("restarting"), "");
  assert.equal(serviceQueueNote("ok"), "");
  assert.equal(serviceQueueNote("not_applicable"), "");
});

// --- The button. ---

test("`degraded` and `stuck` both offer a pressable Restart", () => {
  for (const state of ["degraded", "stuck"]) {
    const props = serviceButtonProps(SERVICE_RESTART_IDLE, state);
    assert.equal(props.disabled, false, state);
    assert.match(props.label, /restart/i);
  }
});

test("the button DISABLES itself while the request is in flight", () => {
  const props = serviceButtonProps(SERVICE_RESTART_SENDING, "stuck");
  assert.equal(props.disabled, true);
  assert.match(props.label, /restarting/i);
});

test("⚠️ a 202 leaves the button DISABLED and the alarm standing — it never reports success", () => {
  const props = serviceButtonProps(SERVICE_RESTART_ACCEPTED, "stuck");
  assert.equal(props.disabled, true);
  const progress = serviceProgressText(SERVICE_RESTART_ACCEPTED);
  // "requested", not "restarted"/"fixed"/"back"/"working"
  assert.match(progress, /requested/i);
  assert.doesNotMatch(progress, /\b(fixed|working again|all good|back up|restarted)\b/i);
  assert.doesNotMatch(props.label, /\b(done|fixed|restarted)\b/i);
  // and the alarm itself is still the ledger's to retire, not the button's
  assert.notEqual(serviceAlert(ledgerWith({ state: "stuck", reason: STUCK_REASON, failures: 2 })), null);
});

test("a FAILED POST is reported as a failure and offers the press again — never swallowed", () => {
  const props = serviceButtonProps(SERVICE_RESTART_FAILED, "stuck");
  assert.equal(props.disabled, false, "a failure must leave the person able to try again");
  const progress = serviceProgressText(SERVICE_RESTART_FAILED);
  assert.ok(progress.length > 0, "a failed restart request must say so");
  assert.match(progress, /couldn't/i);
});

test("the server saying `restarting` disables the button whatever this page's own status is — reflected, not pretended", () => {
  for (const status of [SERVICE_RESTART_IDLE, SERVICE_RESTART_FAILED, SERVICE_RESTART_ACCEPTED]) {
    const props = serviceButtonProps(status, "restarting");
    assert.equal(props.disabled, true, status);
    assert.match(props.label, /restarting/i);
  }
});

test("nothing is in progress while idle, so the line beside the button is empty", () => {
  assert.equal(serviceProgressText(SERVICE_RESTART_IDLE), "");
});

// --- nextServiceRestart: the reducer. ---

test("the reducer walks click -> accepted and stops there; there is no 'succeeded' state to reach", () => {
  let s = SERVICE_RESTART_IDLE;
  s = nextServiceRestart(s, "clicked");
  assert.equal(s, SERVICE_RESTART_SENDING);
  s = nextServiceRestart(s, "accepted");
  assert.equal(s, SERVICE_RESTART_ACCEPTED);
  // Nothing the page can do to itself moves it on from here.
  assert.equal(nextServiceRestart(s, "accepted"), SERVICE_RESTART_ACCEPTED);
  assert.equal(nextServiceRestart(s, "clicked"), SERVICE_RESTART_ACCEPTED);
});

test("⚠️ a 202 the ledger never answers hands the press BACK — a permanently disabled button is its own lie", () => {
  // Found by driving the real page: 202, then a ledger that keeps reporting
  // the same state forever, left the control reading "Restart requested" and
  // disabled with no way to try a second time.
  assert.equal(nextServiceRestart(SERVICE_RESTART_ACCEPTED, "gave_up"), SERVICE_RESTART_IDLE);
  assert.equal(serviceButtonProps(SERVICE_RESTART_IDLE, "stuck").disabled, false);
  // and it only applies to a waiting request — nothing else answers to it
  assert.equal(nextServiceRestart(SERVICE_RESTART_SENDING, "gave_up"), SERVICE_RESTART_SENDING);
  assert.equal(nextServiceRestart(SERVICE_RESTART_IDLE, "gave_up"), SERVICE_RESTART_IDLE);
});

test("a refusal moves SENDING to FAILED, and FAILED can be clicked again", () => {
  assert.equal(nextServiceRestart(SERVICE_RESTART_SENDING, "refused"), SERVICE_RESTART_FAILED);
  assert.equal(nextServiceRestart(SERVICE_RESTART_FAILED, "clicked"), SERVICE_RESTART_SENDING);
});

test("a second press while a request is in flight cannot queue a second restart", () => {
  assert.equal(nextServiceRestart(SERVICE_RESTART_SENDING, "clicked"), SERVICE_RESTART_SENDING);
});

test("an event that doesn't apply to the current state changes nothing", () => {
  assert.equal(nextServiceRestart(SERVICE_RESTART_IDLE, "accepted"), SERVICE_RESTART_IDLE);
  assert.equal(nextServiceRestart(SERVICE_RESTART_IDLE, "refused"), SERVICE_RESTART_IDLE);
});

// --- reconcileServiceRestart: only the ledger retires the alarm. ---

test("a pending 'Restart requested' survives a poll that still reports the SAME state", () => {
  const restart = { status: SERVICE_RESTART_ACCEPTED, forState: "stuck" };
  const alert = serviceAlert(ledgerWith({ state: "stuck", reason: STUCK_REASON, failures: 2 }));
  assert.deepEqual(reconcileServiceRestart(restart, alert), restart);
});

test("a poll reporting a DIFFERENT state drops the local claim — the server has answered it", () => {
  const restart = { status: SERVICE_RESTART_ACCEPTED, forState: "stuck" };
  const alert = serviceAlert(ledgerWith({ state: "restarting", reason: "Restart in progress.", failures: 2 }));
  assert.deepEqual(reconcileServiceRestart(restart, alert), { status: SERVICE_RESTART_IDLE, forState: "" });
});

test("a poll with NO alarm clears the button state, which is the only way 'Restart requested' ever goes away", () => {
  const restart = { status: SERVICE_RESTART_ACCEPTED, forState: "stuck" };
  assert.deepEqual(reconcileServiceRestart(restart, null), { status: SERVICE_RESTART_IDLE, forState: "" });
});

test("reconcile survives a missing restart record", () => {
  assert.deepEqual(reconcileServiceRestart(undefined, null), { status: SERVICE_RESTART_IDLE, forState: "" });
});

// --- navHealthState: the nav dot, with the service alarm beside the pipeline
// health array rather than merged into it. ---

const OK_HEALTH = [
  { key: "daemon", status: "ok", detail: "2.5.0", at: "t" },
  { key: "store", status: "ok", detail: "", at: "t" },
];

test("a healthy machine with no service alarm reads 'all good' — no alarm anywhere", () => {
  assert.deepEqual(navHealthState({ health: OK_HEALTH, settings: {} }), { tone: "ok", text: "all good" });
});

test("an older daemon (no `service` key) leaves the dot exactly where it was — it does not go bad on a fact nobody sent", () => {
  const alert = serviceAlert({ blocks: [], health: OK_HEALTH });
  assert.deepEqual(navHealthState({ alert, health: OK_HEALTH, settings: {} }), { tone: "ok", text: "all good" });
});

test("a service alarm turns the dot bad and NAMES the service, so the sidebar cannot read 'all good' beside a banner that doesn't", () => {
  const stuck = serviceAlert(ledgerWith({ state: "stuck", reason: STUCK_REASON, failures: 2 }));
  const s = navHealthState({ alert: stuck, health: OK_HEALTH, settings: {} });
  assert.equal(s.tone, "bad");
  assert.match(s.text, /service/i);
  assert.notEqual(s.text, "all good");
});

test("`restarting` is a warn, not a failure — something is being done about it", () => {
  const alert = serviceAlert(ledgerWith({ state: "restarting", reason: "Restart in progress.", failures: 1 }));
  assert.equal(navHealthState({ alert, health: OK_HEALTH, settings: {} }).tone, "warn");
});

test("a block pending delivery does NOT read as a dead service, and a dead service does not read as a pending block", () => {
  const pendingBlocks = [{ key: "atlas", status: "pending", detail: "", at: "t" }];
  // pipeline pending, service silent -> the pipeline's own wording
  assert.deepEqual(navHealthState({ health: pendingBlocks, settings: {} }), { tone: "warn", text: "catching up" });
  // service stuck, pipeline fine -> the service's wording, not the pipeline's
  const stuck = serviceAlert(ledgerWith({ state: "stuck", reason: STUCK_REASON, failures: 2 }));
  assert.notEqual(navHealthState({ alert: stuck, health: OK_HEALTH, settings: {} }).text, "catching up");
});

test("'not running' still outranks everything — an unreachable daemon is the largest true fact", () => {
  const stuck = serviceAlert(ledgerWith({ state: "stuck", reason: STUCK_REASON, failures: 2 }));
  assert.deepEqual(navHealthState({ offline: true, alert: stuck, health: OK_HEALTH, settings: {} }), {
    tone: "bad",
    text: "not running",
  });
});

test("the existing pipeline-health behaviour is unchanged when no service alarm is present", () => {
  const failed = [{ key: "atlas", status: "failed", detail: "atlas_rejected", at: "t" }];
  assert.deepEqual(navHealthState({ health: failed, settings: { send_to_atlas: true } }), {
    tone: "bad",
    text: "needs attention",
  });
  // and Send to Atlas off still hides the Atlas cell, so it cannot drive the dot
  assert.deepEqual(navHealthState({ health: failed, settings: { send_to_atlas: false } }), {
    tone: "ok",
    text: "all good",
  });
});

// --- Wiring pins. `node --test` has no DOM and no CSS engine, so the few
// facts that live in the markup/stylesheet rather than in a function are
// pinned textually — the same thing test/css-visibility.test.js does for the
// [hidden] rule this banner depends on. ---

const uiDir = path.dirname(fileURLToPath(import.meta.url));
const html = readFileSync(path.join(uiDir, "..", "index.html"), "utf8");
const js = readFileSync(path.join(uiDir, "..", "app.js"), "utf8");
const cssText = readFileSync(path.join(uiDir, "..", "app.css"), "utf8");

test("the banner ships HIDDEN in the markup, so a healthy machine shows no alarm even before the first render", () => {
  const tag = html.match(/<div id="serviceBanner"[^>]*>/);
  assert.ok(tag, "index.html must carry the service banner");
  assert.match(tag[0], /\bhidden\b/, "the banner must start hidden — the first paint of a healthy machine must be silent");
});

test("the banner sits above the panes, beside the offline banner — not inside the Today pane's health strip", () => {
  assert.ok(html.indexOf('id="serviceBanner"') < html.indexOf('id="paneRoot"'), "must render above the pane content");
  assert.ok(html.indexOf('id="serviceBanner"') > html.indexOf("<main>"), "must live in main, so it shows on every pane");
});

test("app.css styles .service-banner with its own display rule, which is exactly why the [hidden] !important rule is load-bearing", () => {
  assert.match(cssText, /\.service-banner\s*\{[^}]*display:\s*flex/);
});

test("the Restart control posts to the pinned route, and to nothing else", () => {
  assert.match(js, /sendJSON\(\s*"\/v1\/service\/restart",\s*"POST"/);
});

test("⚠️ the service block is never folded into the health array — visibleHealth still filters on `atlas` alone", () => {
  const fn = js.match(/export function visibleHealth[\s\S]*?\n\}/);
  assert.ok(fn);
  assert.doesNotMatch(fn[0], /service/i, "visibleHealth must stay about the pipeline health array only");
});

test("clickServiceRestart never declares success from the response: no success wording is written there", () => {
  const fn = js.match(/async function clickServiceRestart\([\s\S]*?\n  \}/);
  assert.ok(fn, "clickServiceRestart must exist");
  assert.doesNotMatch(fn[0], /\b(fixed|all good|healthy|Restarted)\b/, fn[0]);
});
