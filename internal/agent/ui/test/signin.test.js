import test from "node:test";
import assert from "node:assert/strict";
import {
  showFirstRun,
  signinBarMode,
  signinPollStep,
  signinErrorText,
  signinStartErrorText,
  signedInText,
  safeAuthorizeURL,
  SIGNIN_TEXT,
  SIGNIN_ERROR_TEXT,
  SIGNIN_GIVE_UP_MS,
  SIGNIN_MAX_POLL_FAILURES,
  SIGNIN_DONE_LINGER_MS,
} from "../app.js";

// GET /v1/auth/state (contract C5): {paired, principal, org, first_run, pending, last_error}.
const auth = (over = {}) => ({
  paired: false,
  principal: null,
  org: null,
  first_run: false,
  pending: false,
  last_error: null,
  ...over,
});

// --- showFirstRun: the spec's "does the first-open choice show?" table (AC-12). ---

test("row 1: unpaired, send_to_atlas never set -> the choice shows", () => {
  assert.equal(showFirstRun(auth({ first_run: true })), true);
});

test("rows 2, 3 and 5: the daemon says first_run false (a choice exists, or KELD_ATLAS chose) -> never shown", () => {
  assert.equal(showFirstRun(auth({ first_run: false })), false);
});

test("row 4: a paired machine never sees the screen, even if first_run were somehow true", () => {
  assert.equal(showFirstRun(auth({ paired: true, first_run: true })), false);
});

test("no auth state (an older daemon without the route, or it has not answered) -> never shown", () => {
  assert.equal(showFirstRun(null), false);
  assert.equal(showFirstRun(undefined), false);
  // A malformed answer is not a yes.
  assert.equal(showFirstRun({ first_run: "true" }), false);
});

// --- signinBarMode: what the bar above every pane says. ---

const atlasOn = { send_to_atlas: true };
const atlasOff = { send_to_atlas: false };
const idle = { status: "idle" };

test("unpaired with Send to Atlas on and no choice screen -> the not-signed-in prompt (table row 3)", () => {
  assert.equal(signinBarMode(auth(), atlasOn, idle, 0), "prompt");
});

test("unpaired and local only -> no bar: that was a choice, Settings keeps Sign in (table row 2)", () => {
  assert.equal(signinBarMode(auth(), atlasOff, idle, 0), null);
});

test("while the first-open choice is on screen the bar stays out of its way, even mid-sign-in (the screen shows it)", () => {
  assert.equal(signinBarMode(auth({ first_run: true }), atlasOn, idle, 0), null);
  assert.equal(signinBarMode(auth({ first_run: true }), atlasOn, { status: "waiting" }, 0), null);
});

test("paired -> no bar", () => {
  assert.equal(signinBarMode(auth({ paired: true }), atlasOn, idle, 0), null);
});

test("no auth state -> no bar: the page cannot tell, so it does not nag", () => {
  assert.equal(signinBarMode(null, atlasOn, idle, 0), null);
});

test("a sign-in in flight shows in the bar wherever it was started, even from local-only Settings", () => {
  for (const status of ["starting", "waiting", "failed"]) {
    assert.equal(signinBarMode(auth(), atlasOff, { status }, 0), "flow", status);
  }
});

test("a finished sign-in lingers as a confirmation, then the bar goes away", () => {
  const done = { status: "done", doneAt: 1000 };
  assert.equal(signinBarMode(auth({ paired: true }), atlasOn, done, 1000 + 100), "done");
  assert.equal(signinBarMode(auth({ paired: true }), atlasOn, done, 1000 + SIGNIN_DONE_LINGER_MS), null);
});

// --- signinPollStep: when polling /v1/auth/state stops, and why. ---

const t0 = 1_000_000;

test("paired -> stop, done", () => {
  assert.deepEqual(signinPollStep(auth({ paired: true }), { startedAt: t0, now: t0 + 3000 }), {
    stop: true,
    status: "done",
    error: null,
  });
});

test("still pending -> keep polling", () => {
  const s = signinPollStep(auth({ pending: true }), { startedAt: t0, now: t0 + 3000 });
  assert.equal(s.stop, false);
  assert.equal(s.status, "waiting");
});

test("the attempt is no longer pending and the daemon names why -> stop with that reason", () => {
  for (const code of ["not_started_here", "expired", "atlas_mismatch", "atlas_off", "atlas_error"]) {
    assert.deepEqual(signinPollStep(auth({ last_error: code }), { startedAt: t0, now: t0 + 3000 }), {
      stop: true,
      status: "failed",
      error: code,
    });
  }
});

test("a last_error while the attempt is STILL pending does not end it (a forged or stale return must not cancel a real sign-in)", () => {
  const s = signinPollStep(auth({ pending: true, last_error: "not_started_here" }), { startedAt: t0, now: t0 + 3000 });
  assert.equal(s.stop, false);
});

test("not pending, not paired and no reason -> stop rather than wait forever on nothing", () => {
  const s = signinPollStep(auth(), { startedAt: t0, now: t0 + 3000 });
  assert.equal(s.stop, true);
  assert.equal(s.status, "failed");
  assert.equal(s.error, "abandoned");
});

test("a missed poll (null) keeps going, but consecutive misses stop it as unreachable", () => {
  assert.equal(signinPollStep(null, { startedAt: t0, now: t0 + 3000, failures: 1 }).stop, false);
  assert.deepEqual(
    signinPollStep(null, { startedAt: t0, now: t0 + 3000, failures: SIGNIN_MAX_POLL_FAILURES }),
    { stop: true, status: "failed", error: "unreachable" }
  );
});

test("gives up at the daemon's own pending lifetime, and paired still wins at the last moment", () => {
  const late = { startedAt: t0, now: t0 + SIGNIN_GIVE_UP_MS };
  assert.deepEqual(signinPollStep(auth({ pending: true }), late), { stop: true, status: "failed", error: "expired" });
  assert.equal(signinPollStep(auth({ paired: true }), late).status, "done");
  assert.equal(SIGNIN_GIVE_UP_MS, 10 * 60 * 1000);
});

// --- error-code -> message ---

test("every last_error in contract C5 has its own sentence, never the raw code", () => {
  for (const code of ["not_started_here", "expired", "atlas_mismatch", "atlas_off", "atlas_error"]) {
    const text = signinErrorText(code);
    assert.ok(text && text !== code, code);
    assert.equal(text, SIGNIN_ERROR_TEXT[code]);
  }
});

test("the page's own stop reasons read as sentences too", () => {
  for (const code of ["unreachable", "abandoned", "send_to_atlas_is_off"]) {
    assert.ok(signinErrorText(code).length > 10, code);
  }
});

test("an unknown code surfaces verbatim rather than as a shrug; none at all gets a plain sentence", () => {
  assert.equal(signinErrorText("some_new_reason"), "some_new_reason");
  assert.ok(signinErrorText(null).length > 0);
});

test("POST /v1/auth/start's 409 send_to_atlas_is_off says what to do", () => {
  assert.match(signinStartErrorText(409, { error: "send_to_atlas_is_off" }), /Send to Atlas/);
});

test("start: a transport failure, a daemon without the route and a 5xx each get a plain sentence", () => {
  assert.match(signinStartErrorText(0, null), /reach Signal/i);
  assert.match(signinStartErrorText(404, null), /setup code/i);
  assert.ok(signinStartErrorText(500, {}).length > 0);
});

// --- signed-in wording ---

test("signed in names the person and the org when the daemon knows them", () => {
  assert.equal(signedInText(auth({ paired: true, principal: "ana@acme.test", org: "Acme" })), "Signed in as ana@acme.test · Acme");
  assert.equal(signedInText(auth({ paired: true, principal: "ana@acme.test" })), "Signed in as ana@acme.test");
  // Paired by a setup code on an older build: no principal recorded. Still true.
  assert.equal(signedInText(auth({ paired: true })), "Signed in to Atlas");
});

// --- the link fallback ---

test("the fallback link is rendered only for an http(s) URL", () => {
  assert.equal(safeAuthorizeURL("http://127.0.0.1:5000/cli/signal/authorize?state=x"), "http://127.0.0.1:5000/cli/signal/authorize?state=x");
  assert.equal(safeAuthorizeURL("https://atlas.keld.co/cli/signal/authorize?x=1"), "https://atlas.keld.co/cli/signal/authorize?x=1");
  assert.equal(safeAuthorizeURL("javascript:alert(1)"), "");
  assert.equal(safeAuthorizeURL(""), "");
  assert.equal(safeAuthorizeURL(null), "");
});

// --- copy lives in one place, and says what the wireframe says ---

test("the first-open copy is the wireframe's", () => {
  assert.equal(SIGNIN_TEXT.signIn, "Sign in with Atlas");
  assert.equal(SIGNIN_TEXT.localOnly, "Use locally only");
  assert.equal(SIGNIN_TEXT.later, "You can sign in later from Settings.");
  assert.equal(SIGNIN_TEXT.localNote, "Local only: nothing leaves this computer.");
  assert.equal(SIGNIN_TEXT.waiting, "Finish signing in in your browser");
});
