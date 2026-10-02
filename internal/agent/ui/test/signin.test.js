import test from "node:test";
import assert from "node:assert/strict";
import {
  copyText,
  envPill,
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
  signinLinkSentence,
  unpairView,
  unpairErrorText,
  UNPAIR_TEXT,
  atlasEnvLine,
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

test("unpaired with Send to Atlas on -> no bar: not being signed in is never a bar of its own", () => {
  assert.equal(signinBarMode(auth(), atlasOn, idle, 0), null);
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
  for (const code of ["not_started_here", "expired", "atlas_mismatch", "atlas_off", "atlas_error", "save_failed"]) {
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
  for (const code of ["not_started_here", "expired", "atlas_mismatch", "atlas_off", "atlas_error", "save_failed"]) {
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
  assert.equal(SIGNIN_TEXT.localOnly, "Use without an account");
  assert.equal(SIGNIN_TEXT.waiting, "Finish signing in in your browser");
});

// --- the "Send to Atlas" pill in the top bar ---

test("envPill: no label while the first-open choice is up — nobody has chosen yet", () => {
  assert.equal(envPill({ send_to_atlas: true }, auth({ first_run: true })), null);
  assert.equal(envPill({ send_to_atlas: false }, auth({ first_run: true })), null);
});

test("envPill: once a choice exists the label says where the data goes", () => {
  const atlas = { icon: "cloud", text: "Atlas" };
  assert.deepEqual(envPill({ send_to_atlas: true }, auth({ first_run: false })), atlas);
  assert.deepEqual(envPill({ send_to_atlas: false }, auth({ first_run: false })), { icon: "local", text: "Local" });
  // A paired machine never shows the choice, so its label stays.
  assert.deepEqual(envPill({ send_to_atlas: true }, auth({ paired: true, first_run: true })), atlas);
  // An older daemon with no /v1/auth/state is not a first run.
  assert.deepEqual(envPill({ send_to_atlas: true }, null), atlas);
});

test("envPill: a non-production Atlas is named; production and local-only are not", () => {
  const on = (name) => ({ send_to_atlas: true, atlas_env: { name } });
  assert.deepEqual(envPill(on("prod"), auth()), { icon: "cloud", text: "Atlas" });
  assert.deepEqual(envPill(on("dev"), auth()), { icon: "cloud", text: "Atlas dev" });
  assert.deepEqual(envPill(on("local"), auth()), { icon: "cloud", text: "Atlas local" });
  assert.deepEqual(envPill(on("custom"), auth()), { icon: "cloud", text: "Atlas custom" });
  // Nothing is sent, so which Atlas is configured does not matter here.
  assert.deepEqual(envPill({ send_to_atlas: false, atlas_env: { name: "dev" } }, auth()), { icon: "local", text: "Local" });
});

test("envPill: no settings yet, no label", () => {
  assert.equal(envPill(null, auth()), null);
});

test("a sign-in that could not be saved here is this computer's failure, not Atlas's", () => {
  assert.equal(signinErrorText("save_failed"), "Signal couldn't save the sign-in on this computer.");
  assert.notEqual(signinErrorText("save_failed"), signinErrorText("atlas_error"));
});

// --- "Copy link": the fallback when a click on the link cannot open anything ---
// (the desktop app's web view, a locked-down browser). It must report what
// happened, never claim a copy that did not happen.

test("copyText uses the async clipboard when there is one", async () => {
  let got = null;
  const ok = await copyText("https://atlas.test/a?b=1", { clipboard: { writeText: async (t) => { got = t; } } });
  assert.equal(ok, true);
  assert.equal(got, "https://atlas.test/a?b=1");
});

test("copyText falls back to the selection copy when the clipboard refuses", async () => {
  let fallbackGot = null;
  const ok = await copyText("u", {
    clipboard: { writeText: async () => { throw new Error("NotAllowedError"); } },
    fallback: (t) => { fallbackGot = t; return true; },
  });
  assert.equal(ok, true);
  assert.equal(fallbackGot, "u");
});

test("copyText says false when nothing could copy", async () => {
  assert.equal(await copyText("u", { clipboard: null, fallback: () => false }), false);
  assert.equal(await copyText("", { clipboard: { writeText: async () => {} } }), false);
});

test("the link's sentence says whether the browser opened; the link shows either way", () => {
  assert.equal(signinLinkSentence(true), "Browser didn't open? Use this link:");
  assert.equal(signinLinkSentence(false), "Your browser didn't open. Open this link to finish:");
  assert.equal(signinLinkSentence(undefined), "Your browser didn't open. Open this link to finish:");
});

test("Unpair asks before it acts: idle shows the button, confirm shows the question and no button", () => {
  assert.deepEqual(unpairView("idle", null), { button: true, confirm: false, note: null, busy: false });
  const c = unpairView("confirm", null);
  assert.equal(c.button, false);
  assert.equal(c.confirm, true);
  assert.match(c.note, /keeps collecting here and stops sending to Atlas until you sign in again/);
});

test("Unpair while sending or restarting offers nothing to click", () => {
  for (const st of ["sending", "done"]) {
    const v = unpairView(st, null);
    assert.equal(v.button, false, st);
    assert.equal(v.confirm, false, st);
    assert.equal(v.busy, true, st);
  }
  assert.equal(unpairView("done", null).note, UNPAIR_TEXT.done);
});

test("a refused Unpair says why and offers the button again", () => {
  const v = unpairView("failed", "signin_in_progress");
  assert.equal(v.button, true);
  assert.equal(v.note, "Finish or cancel the sign-in first.");
  assert.match(unpairErrorText("pairing_set_by_env"), /KELD_CTX_TOKEN/);
  assert.equal(unpairErrorText(null), "Signal couldn't unpair. Try again.");
});

test("the Atlas environment line: silent on production unless developer mode, always shown off it", () => {
  const prod = { name: "prod", api: "https://atlas.keld.co", web: "https://atlas.keld.co" };
  assert.equal(atlasEnvLine(prod, false), null);
  assert.deepEqual(atlasEnvLine(prod, true), { name: "prod", where: "https://atlas.keld.co", offProduction: false });
  const local = { name: "local", api: "http://localhost:8000", web: "http://localhost:3000" };
  assert.deepEqual(atlasEnvLine(local, false), { name: "local", where: "API http://localhost:8000 · web http://localhost:3000", offProduction: true });
  assert.equal(atlasEnvLine(undefined, true), null);
});
