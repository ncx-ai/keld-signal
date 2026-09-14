// Keld Signal — the page. Framework-free: this file is loaded directly as an
// ES module by index.html, and imported directly (no build step, no
// transpile) by `node --test` for the pure functions in the first section.
// Everything that touches `document`/`window`/`fetch`/`localStorage` is
// guarded behind `typeof document !== "undefined"` at the bottom, so this
// file has zero side effects when imported under Node.
//
// Renders exactly the shapes docs/v3/contracts.md defines for GET /v1/ledger,
// GET /v1/settings and GET /v1/projects. Nothing here invents a field that
// contract doesn't define; where the mocks show something the contract is
// silent on (see the three "NOT IN CONTRACTS.MD" notes below), it is kept
// entirely client-side rather than sent to the daemon as if it were real.

// =====================================================================
// Pure functions — unit tested directly (see test/*.test.js).
// =====================================================================

/** A gap between two consecutive blocks of one session is a break once it
 *  reaches this many minutes (docs/v3/contracts.md: "Breaks are NOT stored:
 *  the page derives them as the gap between consecutive blocks of one session
 *  when the gap >= 15 minutes"). */
export const BREAK_GAP_MINUTES = 15;

const CELL_STAGES = ["cut", "measured", "attributed", "sent", "received"];

function blockId(b) {
  return `${b.key.session}@${b.key.start}`;
}

/**
 * Derive the breaks between one session's consecutive focus blocks. A block
 * the 20-minute cap cut abuts the next one with gap 0 — that is never a
 * break, only silence >= BREAK_GAP_MINUTES is. Blocks from different sessions
 * never produce a break between them (a session boundary is not silence).
 *
 * Returns objects {earlier, later, startAt, endAt, minutes} — startAt/endAt
 * are the break's own span (earlier.end .. later.key.start), so it renders
 * with no tokens of its own by construction, never by trusting a flag.
 */
export function deriveBreaks(blocks) {
  const bySession = new Map();
  for (const b of blocks) {
    const list = bySession.get(b.key.session) ?? [];
    list.push(b);
    bySession.set(b.key.session, list);
  }
  const breaks = [];
  for (const list of bySession.values()) {
    const sorted = [...list].sort((a, b) => a.key.start - b.key.start);
    for (let i = 1; i < sorted.length; i++) {
      const earlier = sorted[i - 1];
      const later = sorted[i];
      const gapMinutes = (later.key.start - earlier.end) / 60;
      if (gapMinutes >= BREAK_GAP_MINUTES) {
        breaks.push({
          earlier,
          later,
          startAt: earlier.end,
          endAt: later.key.start,
          minutes: gapMinutes,
        });
      }
    }
  }
  return breaks;
}

/**
 * The Today timeline: blocks newest-first, with a break row spliced in
 * immediately after the (chronologically later) block that follows each gap,
 * when showBreaks is on. Pure — no DOM.
 */
export function buildTimeline(blocks, { showBreaks = false } = {}) {
  const sorted = [...blocks].sort((a, b) => b.key.start - a.key.start);
  const items = sorted.map((block) => ({ type: "block", block }));
  if (!showBreaks) return items;
  const breaksByLater = new Map(
    deriveBreaks(blocks).map((brk) => [blockId(brk.later), brk])
  );
  const withBreaks = [];
  for (const item of items) {
    withBreaks.push(item);
    const brk = breaksByLater.get(blockId(item.block));
    if (brk) withBreaks.push({ type: "break", break: brk });
  }
  return withBreaks;
}

/**
 * Read one stage's cell off a block. A cell ABSENT from `cells` (the key is
 * simply not there) means UNKNOWN — the stage never happened yet, or never
 * will for a structural reason distinct from failure — and must never be
 * rendered as "we checked and it failed". This is the one seam every renderer
 * must go through rather than reading `block.cells[stage]` directly, per the
 * hard rule in the D6 brief.
 */
export function readCell(block, stage) {
  const cell = block && block.cells ? block.cells[stage] : undefined;
  if (cell === undefined || cell === null) {
    return { present: false, status: "unknown" };
  }
  return { present: true, ...cell };
}

/** Every dollar figure on this page carries "est." — the client never knows
 *  the org's negotiated rates (docs/v3/contracts.md). */
export function formatEstUSD(usd) {
  const n = Number(usd) || 0;
  const sign = n < 0 ? "-" : "";
  return `${sign}$${Math.abs(n).toFixed(2)} est.`;
}

/** A dollar figure with NO "est." suffix and full cents ("$6.42") — for the
 *  one place the word "est." already lives in the surrounding label ("EST.
 *  SPEND") rather than the value, so the two never say it twice. Never use
 *  this next to a bare number with no "est." anywhere nearby — the per-row
 *  Est. column keeps formatEstUSD precisely because its own label is plain
 *  "Est." with no qualifier. */
export function formatUSD(usd) {
  const n = Number(usd) || 0;
  const sign = n < 0 ? "-" : "";
  return `${sign}$${Math.abs(n).toFixed(2)}`;
}

export function formatMinutes(minutes) {
  const m = Math.round(minutes);
  if (m < 60) return `${m} min`;
  const h = Math.floor(m / 60);
  const rem = m % 60;
  return rem === 0 ? `${h}h` : `${h}h ${rem}m`;
}

/** "17:01" in the given IANA zone, or the viewer's local zone when omitted —
 *  UTC is used by tests for a deterministic result. */
export function formatClock(unixSeconds, timeZone) {
  const d = new Date(unixSeconds * 1000);
  const opts = { hour: "2-digit", minute: "2-digit", hour12: false };
  if (timeZone) opts.timeZone = timeZone;
  return new Intl.DateTimeFormat("en-GB", opts).format(d);
}

export function formatRange(startUnix, endUnix, timeZone) {
  return `${formatClock(startUnix, timeZone)} → ${formatClock(endUnix, timeZone)}`;
}

/** "Thursday 4 September" — the Today pane's date subline, derived from the
 *  ledger's own `generated_at` rather than the viewer's clock, so the page
 *  reports the day Signal actually generated the answer for. Assembled from
 *  formatToParts rather than trusting a locale's default punctuation/order,
 *  because "en-GB" inserts a comma ("Thursday, 4 September") that the design
 *  doesn't want. timeZone is UTC in tests for a deterministic result. */
export function formatDateHeading(isoOrUnix, timeZone) {
  const d = typeof isoOrUnix === "number" ? new Date(isoOrUnix * 1000) : new Date(isoOrUnix);
  const opts = { weekday: "long", day: "numeric", month: "long" };
  if (timeZone) opts.timeZone = timeZone;
  const parts = new Intl.DateTimeFormat("en-GB", opts).formatToParts(d);
  const get = (type) => parts.find((p) => p.type === type)?.value || "";
  return `${get("weekday")} ${get("day")} ${get("month")}`;
}

/** input+output+cache_read+cache_creation — the "raw" total shown on a card;
 *  `request` (price-weighted) is what pricing uses, not the display sum. Not
 *  specified by docs/v3/contracts.md which class(es) a card's headline token
 *  count should sum — this is the choice this lane made; see the report. */
export function totalTokens(t) {
  if (!t) return 0;
  return (
    (t.input || 0) +
    (t.output || 0) +
    (t.cache_read || 0) +
    (t.cache_creation || 0)
  );
}

export function formatTokens(n) {
  const v = Number(n) || 0;
  if (v >= 1_000_000) return `${(v / 1_000_000).toFixed(1)}M`;
  if (v >= 1_000) return `${(v / 1_000).toFixed(1)}K`;
  return `${v}`;
}

/** Send to Atlas, read the same way the daemon's Settings.AtlasEnabled()
 *  does: absent means on. */
export function atlasEnabled(settings) {
  return !!(settings && settings.send_to_atlas !== false);
}

/** The Today table's column headers: with Atlas off there is no Atlas
 *  column at all (never an empty or greyed one) — one function both the
 *  renderer and the tests read, so the two cannot drift apart. */
export function tableColumns(settings) {
  const cols = ["Focus block", "Project", "Tokens", "Est.", "Model"];
  if (atlasEnabled(settings)) cols.push("Atlas");
  return cols;
}

/** The Atlas column's cell for one block, collapsing sent+received into one
 *  status a pill can render: "unknown" (sent never happened — absent, not a
 *  failure), "n/a" (Atlas was off when this block was measured), "pending"
 *  (sent, not yet acknowledged), "ok" or "failed" (with its reason/HTTP
 *  status, straight from the received cell). */
export function atlasCellStatus(block) {
  const sent = readCell(block, "sent");
  if (!sent.present) return { status: "unknown" };
  const received = readCell(block, "received");
  if (!received.present) return { status: "pending" };
  return { status: received.status, reason: received.reason, httpStatus: received.http_status };
}

/** Every measured cell (tokens/model/estimate) off a block, or null if the
 *  "measured" stage never happened (absent, not a zeroed-out object). */
export function measuredOf(block) {
  const cell = readCell(block, "measured");
  return cell.present ? cell : null;
}

/** The block's project name/method, or null if attribution hasn't run yet. A
 *  present-but-empty project_id (no rule matched) is distinct from absent —
 *  callers should check `.project_id` themselves once this returns non-null. */
export function attributionOf(block) {
  const cell = readCell(block, "attributed");
  return cell.present ? cell : null;
}

/** A project's title by id, or null if the ledger names an id GET
 *  /v1/projects never returned (a real, if unlikely, disagreement between
 *  the two routes — not the common "no rule matched" case, which is a block
 *  whose project_id is empty, not one that names an id nobody knows). */
export function projectTitle(projectId, projects) {
  if (!projectId) return null;
  const list = (projects && projects.projects) || [];
  const match = list.find((p) => p.id === projectId);
  return match ? match.title : null;
}

/**
 * What the Today table's Project cell should show for one block, as data —
 * never an internal id. Four outcomes, and the renderer must not collapse
 * any two of them into the same look:
 *   - "unknown"    — attribution hasn't run yet (the cell itself is absent)
 *   - "none"       — it ran and no rule matched this block
 *   - "attributed" — a project matched; `text` is its TITLE, never its id
 *   - "unresolved" — the ledger names a project id GET /v1/projects doesn't
 *                    know about; `text` falls back to the raw id, but the
 *                    caller must style this like "unknown", not like a real
 *                    attributed project — showing an id at all here is
 *                    already the degraded case.
 */
export function projectCellInfo(block, projects) {
  const attr = attributionOf(block);
  if (!attr) return { kind: "unknown" };
  if (!attr.project_id) return { kind: "none" };
  const title = projectTitle(attr.project_id, projects);
  if (title) return { kind: "attributed", text: title, method: attr.method || "" };
  return { kind: "unresolved", text: attr.project_id };
}

/** The rhythm strip's fill for one block: a stable-ish hash of the resolved
 *  project id into a small palette, so the same project keeps the same
 *  colour across a render — and a single, clearly-visible neutral for
 *  "unattributed" or "unresolved", never the near-background tone that reads
 *  as a missing/blank square. */
export const RHYTHM_PALETTE = ["var(--green)", "var(--sage)", "var(--indigo)", "var(--amber)"];
export const RHYTHM_UNATTRIBUTED_COLOR = "var(--rule-strong)";

export function rhythmColorFor(block, projects) {
  const attr = attributionOf(block);
  if (!attr || !attr.project_id) return RHYTHM_UNATTRIBUTED_COLOR;
  const info = projectCellInfo(block, projects);
  if (info.kind !== "attributed") return RHYTHM_UNATTRIBUTED_COLOR;
  if (attr.method === "embedding") return "var(--indigo)";
  let h = 0;
  for (const c of attr.project_id) h = (h * 31 + c.charCodeAt(0)) >>> 0;
  return RHYTHM_PALETTE[h % RHYTHM_PALETTE.length];
}

/** The rhythm strip's hover text for one block: its time range and whatever
 *  the Project column itself would show, so a square never explains itself
 *  in words the table doesn't also stand behind. */
export function rhythmTitleFor(block, projects) {
  const info = projectCellInfo(block, projects);
  const label = info.kind === "attributed" || info.kind === "unresolved" ? info.text : "no project";
  return `${formatRange(block.key.start, block.end)} · ${label}`;
}

/** Summed stats for the Today header tiles. Every field is computed from
 *  `blocks` alone (whatever GET /v1/ledger returned) — no field here reaches
 *  further back than the ledger's own answer. */
export function focusStats(blocks) {
  let totalMinutes = 0;
  let longestMinutes = 0;
  let tokens = 0;
  let usd = 0;
  const days = new Set();
  for (const b of blocks) {
    const minutes = (b.end - b.key.start) / 60;
    totalMinutes += minutes;
    if (minutes > longestMinutes) longestMinutes = minutes;
    const m = measuredOf(b);
    if (m) {
      tokens += totalTokens(m.tokens);
      usd += m.estimate_usd || 0;
    }
    days.add(new Date(b.key.start * 1000).toISOString().slice(0, 10));
  }
  const breaks = deriveBreaks(blocks);
  const breakMinutes = breaks.reduce((s, br) => s + br.minutes, 0);
  return {
    count: blocks.length,
    totalMinutes,
    longestMinutes,
    tokens,
    usd,
    breakCount: breaks.length,
    breakMinutes,
    dayCount: days.size, // a same-window proxy for "streak"; see the report
  };
}

/** Two projects that both claim the same repo (or the same ticket key) among
 *  their rules conflict — computable from the projects list alone, with no
 *  extra field the contract doesn't already define. Hidden projects and
 *  projects in a switched-off workstream never conflict with anything. */
export function findConflicts(projects, offWorkstreams) {
  const off = new Set(offWorkstreams || []);
  const live = projects.filter((p) => !p.hidden && !off.has(p.workstream));
  const byRepo = new Map();
  const byTicket = new Map();
  for (const p of live) {
    for (const r of p.repos || []) {
      const key = r.toLowerCase();
      const list = byRepo.get(key) ?? [];
      list.push(p);
      byRepo.set(key, list);
    }
    if (p.ticket_key) {
      const key = p.ticket_key.toUpperCase();
      const list = byTicket.get(key) ?? [];
      list.push(p);
      byTicket.set(key, list);
    }
  }
  const conflicts = new Map(); // project id -> Set of other project ids
  const addMutual = (list) => {
    if (list.length < 2) return;
    for (const p of list) {
      const others = conflicts.get(p.id) ?? new Set();
      for (const q of list) if (q.id !== p.id) others.add(q.id);
      conflicts.set(p.id, others);
    }
  };
  for (const list of byRepo.values()) addMutual(list);
  for (const list of byTicket.values()) addMutual(list);
  const out = {};
  for (const [id, set] of conflicts) out[id] = [...set];
  return out;
}

/** What a project card shows as "the rules": GET /v1/projects' own `rules`
 *  field (internal/agent/ingress/projects.go's projectView — declared repos
 *  always, a repo-shaped keyword only once it has actually matched an
 *  observed block), never the raw `repos ∪ keywords` the store holds. Reading
 *  raw keywords would show an unmatched candidate like "design/ux" as if it
 *  were a real rule — precisely the internal-vocabulary failure the daemon's
 *  own split exists to prevent; the page must not re-introduce it by reading
 *  the wrong field. Falls back to `repos` only for a payload that predates the
 *  `rules` field (a fixture not yet updated), never as the normal path. */
export function projectRulesSummary(p) {
  const rules = Array.isArray(p.rules) ? p.rules : p.repos || [];
  const bits = [];
  if (rules.length) bits.push(`repo ${rules[0]}${rules.length > 1 ? ` +${rules.length - 1}` : ""}`);
  if (p.ticket_key) bits.push(`tickets ${p.ticket_key}-xxx`);
  return bits.join(" · ") || "no rules yet";
}

/** Every mutating /v1/projects (or /v1/workstreams) route answers
 *  `{local_only: true, atlas_editor_url: "..."}` (docs/v3/contracts.md's
 *  verified note: a machine cannot write to Atlas's vocabulary today). This
 *  is the ONE sentence the page ever shows for that fact — one function so
 *  two call sites cannot drift into saying it differently, and so neither
 *  can accidentally imply the org learned anything. */
export function localOnlyConfirmationText() {
  return "Applied on this machine. To change it for everyone, edit the workstream in Atlas.";
}

/** Every project a suggestion's "Same as" picker may offer — every project
 *  GET /v1/projects returns, INCLUDING the org's own (origin `atlas`):
 *  internal/agent/ingress/projects.go's handleGetProjects already merges the
 *  local document with the org's pooled workstream values into one list, so
 *  "same as" is never limited to local projects. A hidden project is left
 *  out — placing a suggestion on one a person chose to hide would silently
 *  un-hide nothing and just confuse the coverage count. */
export function sameAsOptions(projects) {
  return (projects || [])
    .filter((p) => !p.hidden)
    .map((p) => ({ id: p.id, label: p.origin === "atlas" ? `${p.title} · in Atlas` : p.title }));
}

/** The confirmation sentence after "same as" specifically — placing a
 *  suggestion onto an Atlas-origin project is a LOCAL OVERLAY (this
 *  machine's rule is added locally; the org's project itself is never
 *  written), so it needs its own sentence rather than
 *  localOnlyConfirmationText(): that one's "edit the workstream in Atlas"
 *  reads as an invitation to go change the org's copy, which is backwards
 *  for a project this machine did not create. Placing onto a LOCAL project
 *  (or "New project", which only ever creates one) keeps the general
 *  sentence — there IS no org copy to leave alone in that case, so the
 *  "edit it in Atlas" advice is the real next step. */
export function sameAsConfirmationText(targetOrigin) {
  if (targetOrigin === "atlas") {
    return "Applied on this machine. The org's project is unchanged.";
  }
  return localOnlyConfirmationText();
}

/** "Start at login" (docs/v3/contracts.md, page convention 4): NOT a working
 *  toggle until the desktop shell (Tauri autostart, D9) owns it. Always
 *  unchecked and disabled, with a note saying where it actually lives — a
 *  toggle that silently does nothing is the defect this whole page exists to
 *  remove, so this is never rendered as live state from settings/localStorage. */
export function startAtLoginProps() {
  return { checked: false, disabled: true, note: "in the desktop app" };
}

/** The env var GET /v1/settings' `readonly` names a key by, and the note the
 *  page shows next to a control that key disables — "keys named in readonly
 *  render disabled with 'set by KELD_… on this machine'" (the D2 brief).
 *  internal/agent/settings/v3.go + attrib.go name the three that currently
 *  support an env override; an unknown future key still gets an honest guess
 *  rather than silently showing nothing. */
export const SETTINGS_ENV = {
  send_to_atlas: "KELD_ATLAS",
  dev_blocks: "KELD_DEV_BLOCKS",
  attribution: "KELD_ATTRIBUTION",
};

/** validProjectTitle is the one rule for naming a project from a suggestion:
 *  trimmed, and empty means "no".
 *
 *  ⚠️ It is a named function rather than an inline `if (!title)` because that
 *  inline check is exactly what swallowed the `prompt()` bug — a null from a
 *  dialog that never opened was indistinguishable from a person choosing to
 *  cancel, so the page could not tell "you said no" from "I never asked". The
 *  caller now decides those separately: Cancel closes the field, an empty name
 *  says so. Returns the trimmed name, or "" for a name that is not one.
 */
/** projectGroups is what "Your projects" iterates: the org's workstreams, plus
 *  one group for any project whose workstream is in none of them.
 *
 *  ⚠️ **WITHOUT THE SECOND HALF, A PROJECT CAN BE INVISIBLE.** The pane renders
 *  projects by looping over workstreams and drawing each one's members, so a
 *  project filed under a key that is in no list is never drawn at all. Measured
 *  on a real machine: two projects on disk, `"workstreams": null` from the API,
 *  and a pane reading "YOUR PROJECTS" followed by nothing. The person who made
 *  them saw their suggestion disappear and nothing appear, which is
 *  indistinguishable from the suggestion having been thrown away.
 *
 *  That is the state of EVERY machine with Send to Atlas off, because the
 *  workstream list is pushed down by Atlas and nothing local seeded it.
 *
 *  The daemon now seeds it too (projects.ensureWorkstream), so this is the
 *  second of two guards rather than the only one — deliberately, because the
 *  rule worth keeping is "the page never silently drops a project", not "that
 *  one data bug was fixed". A synthetic group carries `synthetic: true` so the
 *  caller can decline to offer an org-level control on a bucket the org never
 *  declared.
 */
export function projectGroups(workstreams, projects) {
  const groups = (workstreams || []).map((w) => ({ ...w, synthetic: false }));
  const known = new Set(groups.map((w) => w.key));
  const extra = new Map();
  for (const p of projects || []) {
    if (p.hidden) continue;
    const key = p.workstream || "development";
    if (known.has(key) || extra.has(key)) continue;
    extra.set(key, { key, name: workstreamDisplayName(key), off: false, synthetic: true });
  }
  return groups.concat([...extra.values()]);
}

/** workstreamDisplayName turns a key into something a person reads. Mirrors the
 *  Go side's function of the same name so a locally-seeded workstream is
 *  labelled identically whether the page or the daemon named it. */
export function workstreamDisplayName(key) {
  const out = String(key || "").replace(/[_-]+/g, " ").trim();
  if (!out) return String(key || "");
  return out[0].toUpperCase() + out.slice(1);
}

export function validProjectTitle(title) {
  return String(title == null ? "" : title).trim();
}

export function readonlyNote(key) {
  const env = SETTINGS_ENV[key] || `KELD_${String(key || "").toUpperCase()}`;
  return `Set by ${env} on this machine.`;
}

/** `PUT /v1/settings`'s one documented refusal (docs/v3/contracts.md):
 *  `dev_blocks` while `send_to_atlas` is on → 409
 *  `{"error":"turn_off_send_to_atlas_first"}`. The sentence is shown VERBATIM
 *  for a code this page knows, and for any code it doesn't — never swallowed
 *  into a generic "something went wrong", which would hide a real, actionable
 *  refusal behind a shrug. */
export const SETTINGS_ERROR_TEXT = {
  turn_off_send_to_atlas_first: "Turn off Send to Atlas first — dev blocks are refused while it's on.",
};

export function settingsErrorText(status, body) {
  const code = body && body.error;
  if (code) return SETTINGS_ERROR_TEXT[code] || code;
  if (!status || status >= 500) return "Signal couldn't save that just now — try again.";
  return "That change was refused.";
}

/** `POST /v1/config`'s two documented refusals (docs/v3/contracts.md): a
 *  malformed code is 400, and the route is refused with 409 while
 *  `send_to_atlas` is false (pointing at a different Atlas is meaningless
 *  while nothing is being sent to one). */
export function configErrorText(status, body) {
  if (status === 400) return "That does not look like a setup code";
  if (status === 409) return "Turn on Send to Atlas first";
  if (body && body.error) return body.error;
  return "Couldn't reach Signal to set that — try again.";
}

/** The restart-bar state machine. `PUT /v1/settings` answers
 *  `restart_required` for `send_to_atlas`/`dev_blocks`; `POST /v1/config`
 *  answers it on every success (a new host always needs one). Either landing
 *  moves NEEDED → the bar shows and offers Restart; the daemon's only
 *  restart trigger is `PUT /v1/settings?restart=1` (docs/v3/contracts.md), so
 *  clicking it re-PUTs (RESTARTING), then the page polls `/v1/ledger`
 *  (WAITING) until the new process answers (READY), and reloads. A pure
 *  reducer so the sequence is one thing to test, not something to reconstruct
 *  from reading the click handler. */
export const RESTART_IDLE = "idle";
export const RESTART_NEEDED = "needed";
export const RESTART_RESTARTING = "restarting";
export const RESTART_WAITING = "waiting";
export const RESTART_READY = "ready";

export function restartBarText(status) {
  switch (status) {
    case RESTART_NEEDED:
      return "Signal restarts to apply this.";
    case RESTART_RESTARTING:
    case RESTART_WAITING:
      return "Restarting…";
    case RESTART_READY:
      return "Signal is back — reloading…";
    default:
      return "";
  }
}

export function nextRestartStatus(status, event) {
  switch (status) {
    case RESTART_NEEDED:
      return event === "clicked" ? RESTART_RESTARTING : RESTART_NEEDED;
    case RESTART_RESTARTING:
      return event === "sent" ? RESTART_WAITING : RESTART_RESTARTING;
    case RESTART_WAITING:
      return event === "ledger_ok" ? RESTART_READY : RESTART_WAITING;
    case RESTART_READY:
      return RESTART_READY;
    default: // RESTART_IDLE, or any state this reducer doesn't recognise
      return event === "restart_required" ? RESTART_NEEDED : RESTART_IDLE;
  }
}

/** One plain sentence per closed reason code
 *  (internal/agent/ledger/recorder.go). Copy is from the user's side of the
 *  screen — no "spool", "cursor" or "corr_id" here; those stay inside the
 *  raw-code line the details toggle shows beside this sentence. */
export const REASON_TEXT = {
  atlas_rejected: "Atlas rejected the token — re-pair in Settings.",
  atlas_refused: "Atlas refused this batch — retrying it won't help.",
  atlas_unavailable: "Couldn't reach Atlas — Signal will retry.",
  captive_portal: "Got a login page back instead of Atlas — check the network.",
  atlas_off: "Send to Atlas is off.",
  sidecar_outdated: "The local analysis service is out of date — reinstall to update it.",
  sidecar_down: "The local analysis service isn't responding.",
  sidecar_behind: "The local analysis service is still catching up.",
  attribute_failed: "Couldn't work out a project for this block after several tries.",
  no_rule_matched: "No project rule matched this yet.",
  conflict: "Two projects claim this — pick one in Projects.",
  no_tokens: "No usage was recorded in this block.",
  spooled: "Saved on this machine — will send once Atlas is reachable.",
  weights_unavailable: "Vector attribution needs a one-time download that hasn't finished.",
  "": "",
};

/** startOfLocalDay is midnight this morning, in the viewer's own timezone, as
 *  unix seconds — the `since` the Today pane asks the ledger for.
 *
 *  ⚠️ **LOCAL, NOT UTC, AND THE DIFFERENCE IS VISIBLE TO A PERSON.** "Today" is
 *  the day someone is having, not the day in Greenwich: west of UTC a UTC-midnight
 *  bound would drop this morning's work, and east of it the pane would still be
 *  showing yesterday evening's. The block's own instant is a UTC point either
 *  way; only the boundary is local.
 *
 *  Compared on BLOCK START, which is what the ledger's `since` already filters
 *  on and what each row already displays. A block that runs across midnight
 *  therefore belongs to the day it began — the alternative, splitting it or
 *  counting it twice, would make the headline numbers stop summing to the list.
 */
export function startOfLocalDay(now) {
  const d = new Date(now);
  d.setHours(0, 0, 0, 0);
  return Math.floor(d.getTime() / 1000);
}

/** todayLedgerURL is the ledger request the Today pane makes. */
export function todayLedgerURL(now) {
  return `/v1/ledger?since=${startOfLocalDay(now)}`;
}

/** How many taps on the version turns developer mode on or off. Seven, because
 *  that is the number people already know from Android's build-number gesture —
 *  a hidden control is only useful if someone can be TOLD how to reach it, and
 *  the familiar number is the instruction. */
export const DEV_TAPS = 7;

/** How long a tap streak survives without another tap.
 *
 *  ⚠️ Without a window the count is immortal: seven stray clicks spread over a
 *  week would flip developer mode on for someone who never meant to, and the
 *  first they would know of it is a Developer box appearing in Settings. Three
 *  seconds is long enough for a deliberate run and short enough that idle
 *  clicking never accumulates. */
export const DEV_TAP_WINDOW_MS = 3000;

/** devTapNext advances the tap streak. Pure: it takes the previous streak and
 *  returns the next one, so the sequence is testable without a DOM or a clock.
 *
 *  `taps` counts up to DEV_TAPS and then resets; `toggled` is true only on the
 *  tap that flipped it. `remaining` is what the hint shows and is deliberately
 *  0 until the streak is past halfway — announcing "6 more" on the first stray
 *  click would advertise a control that is meant to be told, not found.
 */
export function devTapNext(prev, on, now) {
  const p = prev || { taps: 0, last: 0 };
  const continuing = now - p.last <= DEV_TAP_WINDOW_MS;
  const taps = (continuing ? p.taps : 0) + 1;
  if (taps >= DEV_TAPS) {
    return { taps: 0, last: now, toggled: true, on: !on, remaining: 0 };
  }
  const left = DEV_TAPS - taps;
  return { taps, last: now, toggled: false, on, remaining: left <= 3 ? left : 0 };
}

/** versionFromHealth reads the daemon's own version off the health rows.
 *
 *  ⚠️ Returns "" when there is no daemon row or it carries no detail, and the
 *  caller draws NOTHING rather than a blank line or the word "undefined". An
 *  unknown version is not a version; see localagent.SidecarVersionState for the
 *  same refusal one layer down.
 */
export function versionFromHealth(health) {
  for (const h of health || []) {
    if (h && h.key === "daemon" && typeof h.detail === "string" && h.detail !== "") {
      return h.detail;
    }
  }
  return "";
}

/** versionLabel is what the nav footer shows. */
export function versionLabel(version) {
  return version ? `v${version}` : "";
}

export function reasonText(code) {
  return REASON_TEXT[code] || (code ? code : "");
}

/** How long a wait has to last before it stops being ordinary catch-up.
 *
 * Fifteen minutes, and the number is derived rather than picked: the block
 * emitter sweeps every 5 minutes, and a first whole-file ingest is measured at
 * 5.1s on a 90 MB transcript — so ordinary catch-up clears inside one or two
 * sweeps. Three sweeps is the point past which "still behind" can no longer be
 * explained by one slow sweep, and it is the same span the block cutter itself
 * already treats as silence (3 empty 5-minute bins).
 */
export const STUCK_AFTER_MS = 15 * 60 * 1000;

/** pendingWaitMs is how long this session has been waiting, or null when that
 *  cannot be known.
 *
 *  ⚠️ **IT READS `since`, NEVER `at`.** `at` is refreshed on every sweep the
 *  session still cannot be cut, so `now - at` is bounded by the sweep interval
 *  whether the wait is twenty seconds or a day old. Thresholding on it would
 *  measure the sweep timer and never fire. `since` is written once, when the
 *  streak began.
 *
 *  null — not 0 — when `since` is absent or unparseable: a row written by an
 *  older daemon has no age, and an unknown age must not be rendered as either
 *  a problem or a reassurance.
 */
export function pendingWaitMs(entry, now) {
  if (!entry || typeof entry.since !== "string" || entry.since === "") return null;
  const started = Date.parse(entry.since);
  if (!Number.isFinite(started)) return null;
  const ms = now - started;
  return ms >= 0 ? ms : null; // a future timestamp is a broken clock, not a wait
}

/** pendingText is the sentence shown beside a waiting row.
 *
 *  ⚠️ **THE QUIET MESSAGE IS THE DEFAULT AND THE UNKNOWN CASE.** A page that
 *  cries about ordinary catch-up trains people to ignore it, which is worse
 *  than saying nothing — so escalation happens only on a wait we can actually
 *  measure and that has actually lasted. No age, an unparseable age, or a
 *  short one all render the ordinary sentence.
 *
 *  The escalated sentence deliberately says what is observable and stops
 *  there. It does NOT name a cause: nothing in this payload carries memory
 *  pressure, load, or anything else that would explain the delay, and naming
 *  one would be inventing a finding from a check nobody ran.
 */
export function pendingText(entry, now) {
  const ordinary = reasonText(entry && entry.reason) ||
    "Signal hasn't been able to ask for this session's focus blocks yet.";
  const ms = pendingWaitMs(entry, now);
  if (ms === null || ms < STUCK_AFTER_MS) return ordinary;
  return `${ordinary} It has been waiting ${humanWait(ms)} — longer than catching up normally takes.`;
}

/** How long after its last refresh a pending row stops being about NOW.
 *
 * ⚠️ **THIS ASKS A DIFFERENT QUESTION FROM `since`, AND `at` IS THE RIGHT FIELD
 * FOR IT.** `since` answers "how long has this been waiting"; `at` answers "is
 * anyone still waiting". A live wait is re-reported on every 5-minute sweep, so
 * its `at` is never more than one interval old. A row whose `at` has not moved
 * in an hour is not a slow wait — it is a note about a question nobody is
 * asking any more, because the session went quiet and left the swept set.
 *
 * An hour is twelve sweep intervals. Deliberately generous: a live wait
 * refreshes every five minutes so it can never drift near this, while a real
 * abandonment is PERMANENT, so waiting an extra hour to say so costs nothing
 * and mislabelling a live wait costs trust. The margin is the whole point.
 */
export const PENDING_ABANDONED_AFTER_MS = 60 * 60 * 1000;

/** pendingIsAbandoned reports whether nothing is still asking about this row.
 *
 *  ⚠️ Returns FALSE when `at` is missing or unreadable. An unknown age must
 *  never be declared abandoned — "Signal stopped waiting on this" is a definite
 *  claim, and making it from a check that could not run is the confident
 *  negative this codebase refuses everywhere. Unknown falls back to the
 *  existing quiet "waiting" reading, which is what shipped before this.
 */
export function pendingIsAbandoned(entry, now) {
  if (!entry || typeof entry.at !== "string" || entry.at === "") return false;
  const last = Date.parse(entry.at);
  if (!Number.isFinite(last)) return false;
  return now - last > PENDING_ABANDONED_AFTER_MS;
}

/** splitPending separates rows something is still waiting on from rows nothing
 *  is asking about any more. Order within each group is preserved. */
export function splitPending(list, now) {
  const waiting = [];
  const abandoned = [];
  for (const p of list || []) {
    (pendingIsAbandoned(p, now) ? abandoned : waiting).push(p);
  }
  return { waiting, abandoned };
}

/** humanWait renders a duration the way a person would say it. Whole units
 *  only: "1 hour" reads as a fact, "1.03 hours" reads as a machine talking. */
export function humanWait(ms) {
  const mins = Math.floor(ms / 60000);
  if (mins < 60) return `${mins} minutes`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return hours === 1 ? "over an hour" : `over ${hours} hours`;
  const days = Math.floor(hours / 24);
  return days === 1 ? "over a day" : `over ${days} days`;
}

/** Short phrases for the health strip's compact pills — the SAME closed
 *  reason vocabulary as REASON_TEXT, worded to fit next to a version number
 *  instead of standing alone as a sentence. `health[].detail` is typed as
 *  "a version string or a Reason" (internal/agent/ledger/recorder.go), so a
 *  pill that prints `detail` unmapped risks showing the raw code
 *  ("sidecar_outdated") verbatim — exactly the internal-vocabulary leak the
 *  D6 brief forbids. Anything not in this table is treated as a version and
 *  shown as-is. */
const HEALTH_DETAIL_SHORT = {
  atlas_rejected: "token rejected",
  atlas_refused: "batch refused",
  atlas_unavailable: "unreachable",
  captive_portal: "captive network",
  atlas_off: "off",
  sidecar_outdated: "out of date",
  sidecar_down: "not responding",
  sidecar_behind: "catching up",
};

export function healthDetailText(detail) {
  if (!detail) return "";
  return HEALTH_DETAIL_SHORT[detail] || detail;
}

// Copy is from the user's side of the screen (per the D6 brief): what a
// person needs to know about `store` is whether Signal can save its own
// records, not that there is a filesystem/database word for it.
const HEALTH_LABEL = {
  daemon: "Signal",
  sidecar: "Analysis service",
  telemetry: "Telemetry",
  atlas: "Atlas",
  store: "Records",
};

export function healthLabel(key) {
  return HEALTH_LABEL[key] || key;
}

/** Health rows to show, given Send to Atlas's current state: the "atlas" row
 *  never appears while it's off, so there is no Atlas cell to misread — the
 *  same rule that hides the Atlas column on every block card. */
export function visibleHealth(health, settings) {
  const list = health || [];
  if (atlasEnabled(settings)) return list;
  return list.filter((h) => h.key !== "atlas");
}

/** ---- The analysis service's own health, which is NOT the `health` array ----
 *
 *  `GET /v1/ledger` carries a top-level `service` block beside `health`:
 *
 *      "service": { "state": "ok"|"degraded"|"restarting"|"stuck"
 *                            |"not_applicable",
 *                   "reason": "human sentence, empty when ok",
 *                   "failures": 0 }
 *
 *  ⚠️ **It is deliberately not folded into `health`.** That array is PIPELINE
 *  health — per-stage cells about blocks (cut / measured / attributed / sent /
 *  received) — and answers a different question. A block pending delivery must
 *  never render as a dead service, and a dead service must never hide inside a
 *  row about blocks, so this is read separately and rendered in its own place.
 *
 *  ⚠️ **AN ABSENT `service` KEY MEANS "THIS DAEMON IS TOO OLD TO SAY", NEVER
 *  "FINE".** The page renders nothing in that case. This is the same refusal
 *  `visibleHealth` makes for the Atlas cell, `version.Skew` makes with
 *  `known=false`, and `dynamics` makes by DROPPING a status string it does not
 *  recognise: a check that did not run must never publish a confident
 *  negative. An unrecognised `state` is dropped for exactly that reason too —
 *  a newer daemon inventing a sixth value must not make this page assert
 *  something about it.
 */
export const SERVICE_OK = "ok";
export const SERVICE_DEGRADED = "degraded";
export const SERVICE_RESTARTING = "restarting";
export const SERVICE_STUCK = "stuck";
export const SERVICE_NOT_APPLICABLE = "not_applicable";

/** The three states that put something on screen. `ok` and `not_applicable`
 *  are silent BY DESIGN: a healthy machine shows no alarm and no button, and a
 *  machine that legitimately has no analysis service installed is not broken
 *  (the daemon's own `noAnalysisService` path — see AGENTS.md's Model
 *  backends) and must never be reported as if it were. */
const SERVICE_ALARM_STATES = [SERVICE_DEGRADED, SERVICE_RESTARTING, SERVICE_STUCK];

/**
 * The service alarm to render, or null for "say nothing".
 *
 * Null covers four different silences, and they are all silences on purpose:
 * no `service` key at all (an older daemon), `ok`, `not_applicable`, and a
 * `state` string this page does not recognise.
 *
 * `offline` is the fifth: with the daemon unreachable the only `service`
 * block we have is whatever the ledger cache last saw, which is a stale fact
 * about a process that is not answering — and the Restart button could not
 * reach anything anyway. The offline banner already says the true thing, so
 * this one stands down rather than double-reporting it.
 */
export function serviceAlert(ledger, { offline = false } = {}) {
  if (offline) return null;
  const s = ledger && ledger.service;
  if (!s || typeof s !== "object") return null;
  if (!SERVICE_ALARM_STATES.includes(s.state)) return null;
  return {
    state: s.state,
    // The contract types `reason` as a human sentence, so it is shown
    // VERBATIM — never mapped through REASON_TEXT/HEALTH_DETAIL_SHORT. For
    // `stuck` that sentence is the whole point: it is what says restarting
    // was already tried, and a lookup table would replace it with a generic
    // line that does not.
    reason: typeof s.reason === "string" ? s.reason : "",
    // Carried but deliberately NOT rendered: the contract says `failures` is
    // a count and does not say what it counts, and this page does not invent
    // a meaning for a number in order to have something to print.
    failures: typeof s.failures === "number" ? s.failures : 0,
  };
}

/** The headline, per state. Names the thing the way the health strip already
 *  does ("Analysis service", healthLabel("sidecar")) — the word "sidecar"
 *  never reaches the screen. */
export function serviceHeadline(state) {
  switch (state) {
    case SERVICE_DEGRADED:
      return "The analysis service isn't healthy.";
    case SERVICE_STUCK:
      return "The analysis service isn't recovering on its own.";
    case SERVICE_RESTARTING:
      return "Restarting the analysis service…";
    default:
      return "";
  }
}

/** The server's sentence, verbatim. Only an EMPTY reason gets a fallback —
 *  and the fallback says we were not told why, rather than inventing a cause. */
export function serviceReasonText(alert) {
  if (!alert) return "";
  if (alert.reason) return alert.reason;
  return "Signal didn't say why.";
}

/** What happens to the work meanwhile. True for as long as the daemon queues
 *  and spools jobs against an unready analysis service rather than dropping
 *  them (AGENTS.md, Delivery reliability). Not shown while it is already
 *  coming back — the headline says that. */
export function serviceQueueNote(state) {
  if (state === SERVICE_DEGRADED || state === SERVICE_STUCK) {
    return "Your work is still being recorded — Signal holds it until the service is back.";
  }
  return "";
}

/** The Restart button's own state machine, a pure reducer for the same reason
 *  nextRestartStatus is one: the sequence is one thing to test rather than
 *  something to reconstruct from reading a click handler.
 *
 *  ⚠️ **THERE IS NO "SUCCEEDED" STATE, AND THAT IS THE POINT.**
 *  `POST /v1/service/restart` answers 202 — "accepted", not "fixed" — so the
 *  furthest this machine can travel on its own is ACCEPTED. Only the next
 *  `GET /v1/ledger` can retire the alarm, by no longer sending one. */
export const SERVICE_RESTART_IDLE = "idle";
export const SERVICE_RESTART_SENDING = "sending";
export const SERVICE_RESTART_ACCEPTED = "accepted";
export const SERVICE_RESTART_FAILED = "failed";

export function nextServiceRestart(status, event) {
  switch (status) {
    case SERVICE_RESTART_SENDING:
      if (event === "accepted") return SERVICE_RESTART_ACCEPTED;
      if (event === "refused") return SERVICE_RESTART_FAILED;
      return SERVICE_RESTART_SENDING;
    case SERVICE_RESTART_ACCEPTED:
      // ⚠️ **"gave_up" EXISTS BECAUSE A DISABLED BUTTON IS ALSO A LIE ONCE IT
      // IS PERMANENT.** Found by driving the real page: the daemon answers
      // 202, the ledger goes on reporting the same state, and nothing ever
      // reconciles the local claim away — so the control read "Restart
      // requested", disabled, forever, on the one machine where a second
      // attempt is the only thing left to try. After a bounded wait the page
      // stops claiming a request is outstanding and hands the press back.
      return event === "gave_up" ? SERVICE_RESTART_IDLE : SERVICE_RESTART_ACCEPTED;
    default: // IDLE, FAILED, or anything this reducer doesn't recognise
      return event === "clicked" ? SERVICE_RESTART_SENDING : status;
  }
}

/**
 * Reconcile a click's local status against what the ledger now says.
 *
 * The local status is a claim about ONE observed state ("I asked for a
 * restart while it was stuck"). The moment the server reports something else
 * — including no alarm at all — that claim has been answered and is dropped,
 * which is how "Restart requested" stops being shown without the page ever
 * deciding for itself that the restart worked.
 */
export function reconcileServiceRestart(restart, alert) {
  const r = restart || { status: SERVICE_RESTART_IDLE, forState: "" };
  if (!alert) return { status: SERVICE_RESTART_IDLE, forState: "" };
  if (r.forState && r.forState !== alert.state) return { status: SERVICE_RESTART_IDLE, forState: "" };
  return r;
}

/** The button: label, and whether it can be pressed. Disabled while a request
 *  is in flight, disabled once accepted (pressing again buys nothing until the
 *  ledger answers), and disabled whenever the SERVER says it is already
 *  restarting — the button reflects that state rather than pretending to it.
 *  Pressing repeatedly therefore cannot queue a second restart behind the
 *  first. */
export function serviceButtonProps(status, state) {
  if (state === SERVICE_RESTARTING) return { label: "Restarting…", disabled: true };
  switch (status) {
    case SERVICE_RESTART_SENDING:
      return { label: "Restarting…", disabled: true };
    case SERVICE_RESTART_ACCEPTED:
      return { label: "Restart requested", disabled: true };
    case SERVICE_RESTART_FAILED:
      return { label: "Try again", disabled: false };
    default:
      return { label: "Restart service", disabled: false };
  }
}

/** The line beside the button. ⚠️ ACCEPTED must never read as success: a 202
 *  says the daemon took the request, and the only thing that can say the
 *  service is back is the next poll finding no alarm to render. */
export function serviceProgressText(status) {
  switch (status) {
    case SERVICE_RESTART_SENDING:
      return "Asking Signal to restart it…";
    case SERVICE_RESTART_ACCEPTED:
      return "Restart requested. This page will clear once the service answers again.";
    case SERVICE_RESTART_FAILED:
      return "Couldn't ask Signal to restart it.";
    default:
      return "";
  }
}

/**
 * The nav dot, as one pure function over everything that can move it.
 *
 * ⚠️ The service alarm is NOT merged into the `health` array — it is a
 * separate input with its own precedence, so a dead service can never hide
 * inside a row about blocks and a block pending delivery can never render as
 * a dead service. What it does share is the dot, because a sidebar reading
 * "all good" beside a banner saying the analysis service is down is the page
 * contradicting itself.
 *
 * Precedence: not running > service alarm > pipeline health. Each answers a
 * strictly larger question than the next, and the largest true one is the one
 * a person needs.
 */
export function navHealthState({ offline = false, alert = null, health = [], settings = null } = {}) {
  if (offline) return { tone: "bad", text: "not running" };
  if (alert) {
    if (alert.state === SERVICE_RESTARTING) return { tone: "warn", text: "service restarting" };
    if (alert.state === SERVICE_STUCK) return { tone: "bad", text: "service not recovering" };
    return { tone: "bad", text: "service unhealthy" };
  }
  const visible = visibleHealth(health, settings);
  if (visible.some((h) => h.status === "failed")) return { tone: "bad", text: "needs attention" };
  if (visible.some((h) => h.status === "pending")) return { tone: "warn", text: "catching up" };
  return { tone: "ok", text: "all good" };
}

export { CELL_STAGES };

// =====================================================================
// DOM wiring — everything below only runs in a browser.
// =====================================================================

if (typeof document !== "undefined") {
  const LEDGER_CACHE_KEY = "keld_signal_cached_ledger";
  const LOCAL_PREFS_KEY = "keld_signal_local_prefs";

  function readJSONStorage(key, fallback) {
    try {
      const raw = localStorage.getItem(key);
      return raw ? JSON.parse(raw) : fallback;
    } catch {
      return fallback;
    }
  }
  function writeJSONStorage(key, value) {
    try {
      localStorage.setItem(key, JSON.stringify(value));
    } catch {
      // best-effort only: a private window or a full quota must not break
      // the page, it just loses the convenience the cache buys.
    }
  }

  // "Show details on cards" is a page-only preference (docs/v3/contracts.md's
  // page convention 3): it changes nothing the daemon does, so it lives
  // entirely client-side rather than in agent-config.json. "Start at login"
  // used to live here too, as a toggle that looked real and did nothing —
  // page convention 4 is explicit that this is the defect to remove, so it is
  // no longer a stored preference at all; startAtLoginProps() always renders
  // it disabled.
  function loadLocalPrefs() {
    return readJSONStorage(LOCAL_PREFS_KEY, {
      showDetails: false,
      // Per-viewer and local by design: developer mode changes what THIS person
      // sees, never what the daemon does. Nothing about it is published, and a
      // second machine signed into the same org is unaffected.
      devMode: false,
    });
  }
  function saveLocalPrefs(p) {
    writeJSONStorage(LOCAL_PREFS_KEY, p);
  }

  const state = {
    pane: "today",
    ledger: null,
    settings: null,
    projects: null,
    offline: false,
    local: loadLocalPrefs(),
    // restart: the bar's own state machine (see nextRestartStatus/
    // restartBarText). `patch` is whatever PUT /v1/settings body last needs
    // resending with ?restart=1 — empty for a restart /v1/config asked for,
    // since that route already wrote everything itself.
    restart: { status: RESTART_IDLE, patch: {} },
    // serviceRestart: the analysis-service Restart button's own state, and
    // the service state it was pressed against. `forState` is what lets
    // reconcileServiceRestart drop a stale "Restart requested" the moment the
    // ledger says something different — the page never decides on its own
    // that a restart worked.
    serviceRestart: { status: SERVICE_RESTART_IDLE, forState: "" },
    // settingsError: the last PUT /v1/settings refusal, scoped to the key(s)
    // it was about, so it renders next to the control that caused it rather
    // than as an unscoped banner nobody can connect to an action.
    settingsError: null,
    configError: "",
    configHost: "",
    // naming: {id, title, error} while a suggestion is being turned into a
    // project. Null the rest of the time. It lives in state rather than in the
    // DOM because `route()` re-renders the whole pane, so a value held only in
    // an input would be lost the moment anything else refreshed.
    naming: null,
    // confirmations: rowKey -> {url}. Set after any /v1/projects (or
    // /v1/workstreams) mutation whose response carries local_only — read by
    // renderProjects to show localOnlyConfirmationText() under the row the
    // mutation affected. Never cleared by loadAll(): a fixture/dev PUT that
    // doesn't persist must not make the confirmation flicker away on the next
    // poll.
    confirmations: new Map(),
  };

  async function fetchJSON(path, opts) {
    const res = await fetch(path, { credentials: "same-origin", ...opts });
    if (!res.ok) throw new Error(`${path}: ${res.status}`);
    return res.json();
  }

  // sendJSON never throws on a non-2xx — settingsErrorText/configErrorText
  // need the STATUS and the BODY of a refusal (400/409), which fetchJSON's
  // throw-on-!ok would discard. Network failure (daemon down, dev server with
  // no route) is reported as status 0 with a null body, which both error-text
  // functions already treat as "couldn't reach it" rather than crashing.
  async function sendJSON(path, method, payload) {
    try {
      const res = await fetch(path, {
        method,
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload || {}),
      });
      let body = null;
      try {
        body = await res.json();
      } catch {
        // a 204/empty body is not an error in itself
      }
      return { ok: res.ok, status: res.status, body };
    } catch {
      return { ok: false, status: 0, body: null };
    }
  }

  async function loadAll() {
    try {
      const [ledger, settings, projects] = await Promise.all([
        // ⚠️ **BOUNDED TO TODAY, AND IT USED TO BE UNBOUNDED.** This asked for
        // the whole ledger and the pane drew all of it: measured on a real
        // machine, 108 blocks across FOUR days under a heading reading
        // "Tuesday 8 September", with the four headline cards counting all of
        // them. The route has always taken `since`; nothing passed one.
        fetchJSON(todayLedgerURL(Date.now())),
        fetchJSON("/v1/settings"),
        fetchJSON("/v1/projects"),
      ]);
      state.ledger = ledger;
      state.settings = settings;
      state.projects = projects;
      state.offline = false;
      writeJSONStorage(LEDGER_CACHE_KEY, ledger);
    } catch (err) {
      state.offline = true;
      // T14: yesterday's cards still show from the ledger, never a blank
      // window or a spinner. If we have never once loaded successfully there
      // is nothing to show, and the empty state below says so honestly.
      state.ledger = state.ledger || readJSONStorage(LEDGER_CACHE_KEY, null);
    }
  }

  function paneFromHash() {
    const h = (location.hash || "#/today").replace(/^#\//, "");
    return ["today", "projects", "settings"].includes(h) ? h : "today";
  }

  function setActiveNav(pane) {
    document.querySelectorAll("nav[aria-label='Panes'] a").forEach((a) => {
      a.classList.toggle("on", a.dataset.pane === pane);
    });
    document.getElementById("topbarTitle").textContent =
      pane === "today" ? "Today" : pane === "projects" ? "Projects" : "Settings";
  }

  function el(tag, attrs, ...children) {
    const node = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs || {})) {
      if (k === "class") node.className = v;
      else if (k === "html") node.innerHTML = v;
      else if (k.startsWith("on") && typeof v === "function") node.addEventListener(k.slice(2), v);
      else if (v !== false && v != null) node.setAttribute(k, v === true ? "" : v);
    }
    for (const c of children.flat()) {
      if (c == null || c === false) continue;
      node.appendChild(typeof c === "string" ? document.createTextNode(c) : c);
    }
    return node;
  }

  function switchEl({ checked, disabled, onChange, label }) {
    const input = el("input", { type: "checkbox", checked: !!checked, disabled: !!disabled });
    if (onChange) input.addEventListener("change", () => onChange(input.checked));
    return el(
      "label",
      { class: "switch" + (disabled ? " disabled" : "") },
      input,
      el("span", { class: "track" }),
      label ? el("span", {}, label) : null
    );
  }

  function pillFor(status) {
    const cls = status === "ok" ? "ok" : status === "failed" ? "no" : status === "pending" ? "wait" : "unknown";
    const glyph = status === "ok" ? "✓" : status === "failed" ? "✗" : status === "pending" ? "…" : "—";
    return el("span", { class: `pill ${cls}` }, el("span", { class: "dot" }), glyph);
  }

  // ---- Today ----

  function renderToday(root) {
    const { ledger, settings, projects } = state;
    root.innerHTML = "";
    if (!ledger) {
      root.appendChild(el("p", { class: "loading" }, "No focus blocks yet. Once Signal has watched some work, they'll show up here."));
      return;
    }
    const blocks = ledger.blocks || [];
    const stats = focusStats(blocks);
    const showAtlas = atlasEnabled(settings);
    const showBreaks = !!(settings && settings.show_breaks);

    if (ledger.generated_at) {
      root.appendChild(
        el("p", { class: "pane-sub" }, `${formatDateHeading(ledger.generated_at)} · from your device, on your device`)
      );
    }

    root.appendChild(
      el(
        "div",
        { class: "tiles" },
        el("div", { class: "tile" }, el("div", { class: "l" }, "Focus blocks"), el("div", { class: "v" }, `${stats.count}`)),
        el(
          "div",
          { class: "tile" },
          el("div", { class: "l" }, "Focus time"),
          el("div", { class: "v" }, formatMinutes(stats.totalMinutes), el("small", {}, `longest ${formatMinutes(stats.longestMinutes)}`))
        ),
        el("div", { class: "tile" }, el("div", { class: "l" }, "Tokens"), el("div", { class: "v" }, formatTokens(stats.tokens))),
        el(
          "div",
          { class: "tile" },
          el("div", { class: "l" }, "Est. spend"),
          el("div", { class: "v" }, formatUSD(stats.usd))
        )
      )
    );

    if (blocks.length) {
      const squares = [...blocks]
        .sort((a, b) => a.key.start - b.key.start)
        .map((b) =>
          el("span", {
            class: "sq",
            style: `background:${rhythmColorFor(b, projects)}`,
            title: rhythmTitleFor(b, projects),
          })
        );
      root.appendChild(
        el("div", { class: "rhythm" }, el("span", { class: "label" }, "Rhythm"), ...squares)
      );
    }

    if (!blocks.length) {
      root.appendChild(
        el(
          "p",
          { style: "color:var(--muted)" },
          "No focus blocks yet today. Once Signal watches 20 minutes of work, or 15 minutes of quiet closes what you've done, the first card lands here."
        )
      );
    }

    const columns = tableColumns(settings);
    const table = el("table", { class: "blocks-table" }, el("thead", {}, el("tr", {}, ...columns.map((c) => el("th", {}, c)))));
    const tbody = el("tbody", {});
    const timeline = blocks.length ? buildTimeline(blocks, { showBreaks }) : [];
    for (const item of timeline) {
      if (item.type === "break") {
        const cols = columns.length;
        tbody.appendChild(
          el(
            "tr",
            { class: "break-row" },
            el("td", { colspan: `${cols}` }, `☕ break · ${formatMinutes(item.break.minutes)} · ${formatRange(item.break.startAt, item.break.endAt)} · no tokens in or out`)
          )
        );
        continue;
      }
      const b = item.block;
      const measured = measuredOf(b);
      const info = projectCellInfo(b, projects);

      let projectCell;
      if (info.kind === "attributed") {
        projectCell = el("span", {}, el("span", { class: "pill ok" }, info.text), " ", el("small", { style: "color:var(--muted)" }, info.method));
      } else if (info.kind === "none") {
        projectCell = el("span", { class: "pill wait" }, "no project");
      } else if (info.kind === "unresolved") {
        // The ledger names a project id GET /v1/projects doesn't know about —
        // show the id (there is nothing better to show) but never dress it
        // up as a normal attributed project.
        projectCell = el("span", { class: "pill unknown" }, info.text);
      } else {
        projectCell = el("span", { class: "pill unknown" }, "—");
      }

      const row = [
        el(
          "td",
          {},
          el("span", { class: "mono" }, formatRange(b.key.start, b.end)),
          el("small", {}, `${formatMinutes((b.end - b.key.start) / 60)} · ended: ${b.end_reason || "—"}`)
        ),
        el("td", {}, projectCell),
        el("td", { class: "mono" }, measured ? formatTokens(totalTokens(measured.tokens)) : "—"),
        el("td", { class: "mono" }, measured ? formatEstUSD(measured.estimate_usd) : "est. pending"),
        el("td", { class: "mono" }, measured ? measured.model || "—" : "—"),
      ];
      if (showAtlas) {
        const atlas = atlasCellStatus(b);
        let atlasCell;
        if (atlas.status === "failed") {
          atlasCell = el(
            "span",
            {},
            pillFor("failed"),
            el("span", { class: "reason" }, reasonText(atlas.reason), " ", el("span", { class: "link", onclick: () => retryBlock(b) }, "Retry"))
          );
        } else if (atlas.status === "n/a") {
          atlasCell = el("span", { class: "pill unknown" }, "n/a");
        } else {
          atlasCell = pillFor(atlas.status);
        }
        row.push(el("td", {}, atlasCell));
      }
      const tr = el("tr", {}, ...row);
      tbody.appendChild(tr);

      if (state.local.showDetails) {
        const detailBits = CELL_STAGES.map((stage) => {
          const c = readCell(b, stage);
          const bits = [`${stage}=${c.present ? c.status : "absent"}`];
          if (c.reason) bits.push(`reason=${c.reason}`);
          if (c.http_status) bits.push(`http=${c.http_status}`);
          return bits.join(" ");
        }).join("  ·  ");
        tbody.appendChild(
          el(
            "tr",
            {},
            el(
              "td",
              { colspan: `${columns.length}` },
              el("div", { class: "details-line" }, el("span", { class: "mono" }, `${b.key.session}@${b.key.start} — ${detailBits}`))
            )
          )
        );
      }
    }
    table.appendChild(tbody);
    // ⚠️ **THE LIST IS THE ONLY THING THAT SCROLLS.** Everything above it —
    // the date, the four cards, the rhythm — and the health strip below it are
    // fixed, so resizing the window vertically changes the height of this
    // container and nothing else. Before this the whole page scrolled in the
    // window and the health strip sat below the fold, which is why it was
    // invisible until you scrolled to the bottom.
    const scroller = el("div", { class: "today-scroll" });
    if (blocks.length) scroller.appendChild(el("div", { class: "wrap" }, table));

    // ⚠️ **"Waiting to be cut" COUNTS ONLY WHAT IS STILL BEING WAITED ON.**
    // It used to count every pending row, including ones frozen for a day —
    // measured on a real machine: three rows last touched 21 hours earlier,
    // for sessions nothing had asked about since, under a heading claiming the
    // analysis service was still catching up. A count that includes tombstones
    // is a number a person cannot act on.
    const { waiting, abandoned } = splitPending(ledger.pending, Date.now());

    const pendingRow = (p, pill, text) =>
      el(
        "div",
        { class: "suggestion-row" },
        el("span", { class: "pill wait" }, pill),
        el("span", { style: "margin-left:10px;color:var(--ink-2)" }, text)
      );

    if (waiting.length) {
      const list = el("div", { class: "wrap", style: "margin-top:8px" });
      for (const p of waiting) list.appendChild(pendingRow(p, "waiting", pendingText(p, Date.now())));
      scroller.appendChild(el("div", { class: "section-label" }, `Waiting to be cut · ${waiting.length}`));
      scroller.appendChild(list);
    }
    // ⚠️ **`abandoned` IS COMPUTED AND DELIBERATELY NOT DRAWN.** It was, for
    // one iteration, as a "Never characterised" section — and that was wrong
    // twice over. The page said "all good" in the same frame, which is a
    // contradiction no reader should have to reconcile; and a person cannot
    // act on it at all, because the sessions are named by id, the work is
    // historical, and no button changes it. It is an OPERATOR fact and it
    // moved to `keld signal doctor`, where an operator is already looking:
    // localagent.AbandonedSessions.
    //
    // What is kept is the half that was actually a lie — these rows are
    // EXCLUDED from the count above, so "Waiting to be cut" means what it
    // says. Deleting the split and going back to `ledger.pending.length` puts
    // the tombstones back in the number.
    void abandoned;

    root.appendChild(scroller);
    root.appendChild(renderHealthStrip());
  }

  async function retryBlock(block) {
    // Best-effort: there is no dedicated retry route in docs/v3/contracts.md
    // yet (D8 territory); re-fetching the ledger is the honest thing this
    // lane can do today rather than pretend a retry button that does nothing.
    await loadAll();
    route();
  }

  function renderHealthStrip() {
    const { ledger, settings } = state;
    const health = visibleHealth(ledger ? ledger.health : [], settings);
    const cells = health.map((h) => {
      const detail = healthDetailText(h.detail);
      return el(
        "span",
        { class: `pill ${h.status === "ok" ? "ok" : h.status === "failed" ? "no" : "wait"}` },
        el("span", { class: "dot" }),
        `${healthLabel(h.key)}${detail ? " " + detail : ""}`
      );
    });
    return el(
      "div",
      { class: "health-strip" },
      el("div", { class: "cells" }, ...cells),
      el(
        "div",
        { class: "toggles" },
        el("span", {}, "Show breaks ", switchEl({
          checked: !!(settings && settings.show_breaks),
          onChange: (v) => updateSettings({ show_breaks: v }),
        })),
        el("span", {}, "Show details ", switchEl({
          checked: !!state.local.showDetails,
          onChange: (v) => {
            state.local.showDetails = v;
            saveLocalPrefs(state.local);
            route();
          },
        }))
      )
    );
  }

  // updateSettings PUTs one key at a time (every toggle its own PUT, per the
  // D2 brief) and reads BOTH halves of the response: a refusal (409, or
  // anything else non-2xx) is shown next to the control that caused it and
  // the toggle is left unchanged — a swallowed error would otherwise make a
  // refused dev_blocks radio look like it took effect. A success is applied
  // optimistically (see the note that used to live here: DevServer's PUT
  // echoes and never persists, so an immediate reload would overwrite this
  // with the unchanged fixture) and, when it carries `restart_required`,
  // arms the restart bar with this patch so Restart can resend it.
  async function updateSettings(patch) {
    const res = await sendJSON("/v1/settings", "PUT", patch);
    if (!res.ok) {
      state.settingsError = { keys: Object.keys(patch), text: settingsErrorText(res.status, res.body) };
      route();
      return;
    }
    state.settingsError = null;
    state.settings = { ...state.settings, ...patch };
    if (res.body && res.body.restart_required) {
      state.restart.patch = { ...state.restart.patch, ...patch };
      state.restart.status = nextRestartStatus(state.restart.status, "restart_required");
    }
    route();
  }

  function settingsErrorFor(key) {
    return state.settingsError && state.settingsError.keys.includes(key) ? state.settingsError.text : null;
  }

  // The daemon's only restart trigger is PUT /v1/settings?restart=1
  // (docs/v3/contracts.md); POST /v1/config's own restart_required rides the
  // same mechanism with whatever settings patch is pending (empty when a
  // config change is what asked for it — that route already wrote
  // hook.json/auth.json itself). Not specified by contracts.md which route a
  // config-triggered restart should use — this lane's choice; see the report.
  async function clickRestart() {
    state.restart.status = nextRestartStatus(state.restart.status, "clicked");
    route();
    await sendJSON("/v1/settings?restart=1", "PUT", state.restart.patch);
    state.restart.status = nextRestartStatus(state.restart.status, "sent");
    route();
    pollUntilBack();
  }

  // Bounded: RESTART_POLL_MAX_ATTEMPTS * RESTART_POLL_INTERVAL_MS is a
  // generous ceiling for a daemon restart (Swap/confirm elsewhere in this
  // repo budgets minutes, not seconds) without polling forever against a
  // daemon that never comes back.
  const RESTART_POLL_INTERVAL_MS = 750;
  const RESTART_POLL_MAX_ATTEMPTS = 60;

  async function pollUntilBack() {
    for (let attempt = 0; attempt < RESTART_POLL_MAX_ATTEMPTS; attempt++) {
      await new Promise((resolve) => setTimeout(resolve, RESTART_POLL_INTERVAL_MS));
      try {
        // Same bound as loadAll's, so the liveness probe and the pane cannot
        // disagree about which request "the ledger is answering" means.
        const res = await fetch(todayLedgerURL(Date.now()), { credentials: "same-origin" });
        if (res.ok) {
          state.restart.status = nextRestartStatus(state.restart.status, "ledger_ok");
          route();
          location.reload();
          return;
        }
      } catch {
        // not back yet — keep polling
      }
    }
    // Gave up waiting: reload anyway so the page re-reads whatever is there
    // (a slower restart, or one that needs a human to look) rather than
    // sitting on "Restarting…" forever.
    location.reload();
  }

  // ---- Projects ----

  function renderProjects(root) {
    const { projects, settings } = state;
    root.innerHTML = "";
    if (!projects) {
      root.appendChild(el("p", { class: "loading" }, "No project data yet."));
      return;
    }
    const workstreams = projects.workstreams || [];
    const allProjects = projects.projects || [];
    const suggestions = projects.suggestions || [];
    const coverage = projects.coverage || { attributed: 0, total: 0 };
    const offKeys = workstreams.filter((w) => w.off).map((w) => w.key);
    const conflicts = findConflicts(allProjects, offKeys);

    const leftOverBlocks = Math.max(0, (coverage.total || 0) - (coverage.attributed || 0));
    const pct = coverage.total ? Math.round((100 * coverage.attributed) / coverage.total) : 0;

    root.appendChild(
      el(
        "div",
        { class: "tiles three" },
        el("div", { class: "tile" }, el("div", { class: "l" }, "Attributed"), el("div", { class: "v" }, `${pct}%`, el("small", {}, `${coverage.attributed || 0} of ${coverage.total || 0} focus blocks`))),
        el("div", { class: "tile" }, el("div", { class: "l" }, "Left over"), el("div", { class: "v", style: "color:var(--amber-strong)" }, `${leftOverBlocks} blocks`)),
        el("div", { class: "tile" }, el("div", { class: "l" }, "Workstreams on"), el("div", { class: "v" }, `${workstreams.length - offKeys.length}`, el("small", {}, `of ${workstreams.length}`)))
      )
    );

    root.appendChild(el("div", { class: "section-label suggested" }, `Suggested by your activity · ${suggestions.length}`));
    if (!suggestions.length) {
      root.appendChild(el("p", { style: "color:var(--muted);font-size:13px" }, "Nothing left over — every focus block landed in a project."));
    } else {
      for (const s of suggestions) {
        root.appendChild(
          el(
            "div",
            { class: "suggestion-row" },
            el("div", { class: "row-title" }, s.value, el("small", {}, `${kindLabel(s.kind)} · ${s.blocks} blocks · ${formatMinutes(s.minutes)} · ${formatTokens(s.tokens)} tokens`)),
            namingThis(s)
              ? el(
                  "div",
                  { class: "row-actions" },
                  el("input", {
                    type: "text",
                    id: "projectNameInput",
                    class: "project-name",
                    "aria-label": "New project name",
                    value: state.naming.title,
                    oninput: (e) => { state.naming.title = e.target.value; },
                    onkeydown: (e) => {
                      if (e.key === "Enter") bundleSuggestion(s, workstreams, state.naming.title);
                      if (e.key === "Escape") cancelNamingProject();
                    },
                  }),
                  el("button", { class: "btn", onclick: () => bundleSuggestion(s, workstreams, state.naming.title) }, "Create"),
                  el("button", { class: "btn btn-quiet", onclick: cancelNamingProject }, "Cancel")
                )
              : el(
                  "div",
                  { class: "row-actions" },
                  sameAsSelect(s),
                  el("button", { class: "btn", onclick: () => startNamingProject(s) }, "New project")
                ),
            namingThis(s) && state.naming.error
              ? el("div", { class: "settings-note error-note" }, state.naming.error)
              : null
          )
        );
      }
    }

    root.appendChild(el("div", { class: "section-label" }, "Your projects"));
    for (const w of projectGroups(workstreams, allProjects)) {
      const inThis = allProjects.filter((p) => p.workstream === w.key && !p.hidden);
      const card = el(
        "div",
        { class: "workstream-card" + (w.off ? " off" : "") },
        el(
          "div",
          { class: "workstream-head" },
          el("span", { class: "name" }, `${w.name}`),
          // A synthetic group is this machine's own bucket, not one the org
          // declared, so it offers no "counts for my work" switch: that flag is
          // stored per workstream key and would appear to reset on reload,
          // which is a control that lies about what it did.
          w.synthetic
            ? null
            : el("label", {}, "counts for my work ", switchEl({
                checked: !w.off,
                onChange: (v) => setWorkstreamOff(w.key, !v),
              }))
        )
      );
      appendConfirmation(card, `workstream:${w.key}`);
      if (w.off) {
        card.appendChild(el("div", { class: "workstream-off-note" }, "Your work never lands here. Turn on if you work in this area."));
      } else if (!inThis.length) {
        card.appendChild(el("div", { class: "workstream-off-note" }, "No projects yet."));
      } else {
        for (const p of inThis) {
          const conflictIds = conflicts[p.id] || [];
          const row = el(
            "div",
            { class: "project-row" },
            el(
              "div",
              { class: "row-title" },
              p.title,
              el("small", {}, projectRulesSummary(p))
            ),
            // ⚠️ **THE PICKER IS ON LOCAL PROJECTS ONLY.** An org project is not
            // ours to fold away: its identity lives in Atlas, and removing the
            // local overlay entry would drop the rules this machine added while
            // leaving the org's value untouched — a deletion that looks like a
            // move. The daemon refuses it too (projects.MapProjectTo); this is
            // the half that keeps a person from being offered it.
            // ⚠️ An EMPTY CELL, never `null`, for an org project. The row is a
            // three-column grid; skipping the child entirely lets the pill fall
            // into the picker's column, and the org cards' pills then sat 16px
            // left of the local ones (measured 1871 against 1887) — a whole
            // column of misalignment from an absent element.
            p.origin === "atlas"
              ? el("span", { class: "row-spacer", "aria-hidden": "true" })
              : mapProjectSelect(p),
            conflictIds.length
              ? el("span", { class: "pill no" }, "conflict · pick one")
              : el("span", { class: "pill ok" }, p.origin === "atlas" ? "✓ in Atlas" : "local")
          );
          card.appendChild(row);
          if (conflictIds.length) {
            card.appendChild(el("div", { class: "conflict-note" }, `Also claimed by: ${conflictIds.join(", ")}`));
          }
          appendConfirmation(card, `project:${p.id}`);
        }
      }
      root.appendChild(card);
    }
  }

  function kindLabel(kind) {
    return kind === "repo" ? "matched by repository" : kind === "ticket" ? "ticket key from branch names" : "matched by workspace";
  }

  // noteLocalConfirmation records the local-only confirmation for one row,
  // read straight off a mutating route's response (docs/v3/contracts.md:
  // every one of them carries `{local_only, atlas_editor_url}`). rowKey is
  // this lane's own choice of "the affected row" — the daemon's response
  // names no row, only the fact — so the caller (which knows what it just
  // mutated) supplies it.
  function noteLocalConfirmation(rowKey, resp, targetOrigin) {
    if (resp && resp.local_only) {
      state.confirmations.set(rowKey, { url: resp.atlas_editor_url || "", origin: targetOrigin || "" });
    }
  }

  // appendConfirmation renders noteLocalConfirmation's result under the row
  // it belongs to, once — a quiet line, never a claim the org learned
  // anything (localOnlyConfirmationText's own doc comment).
  function appendConfirmation(parent, rowKey) {
    const c = state.confirmations.get(rowKey);
    if (!c) return;
    // ⚠️ The sentence depends on whose project was the target. Placing onto an
    // ATLAS project is a local overlay, so it says the org's project is
    // unchanged; "edit the workstream in Atlas" would read as an invitation to
    // go change the org's copy, which is backwards. A local project has no org
    // copy to leave alone, so the general advice is the real next step.
    const atlasTarget = c.origin === "atlas";
    parent.appendChild(
      el(
        "div",
        { class: "local-note" },
        sameAsConfirmationText(c.origin),
        !atlasTarget && c.url ? el("a", { href: c.url, target: "_blank", rel: "noopener" }, " Open the workstream in Atlas") : null
      )
    );
  }

  /** The "Same as" picker for one suggestion.
   *
   *  A <select> rather than the prompt() this replaced: a person cannot be
   *  expected to type a project id, and the ids the org's values carry
   *  ("keld_projects:signal") are not something anyone would guess. The
   *  options come from sameAsOptions, which includes the org's own projects —
   *  placing onto one is a LOCAL OVERLAY, so the confirmation says the org's
   *  project is unchanged.
   *
   *  It reads as a button until used ("Same as…") because it is an action,
   *  not a setting: the first option is a non-selectable label, and choosing a
   *  real one fires immediately and resets, so the control never shows a
   *  stale "current value" for something that is not a value. */
  function sameAsSelect(suggestion) {
    const opts = sameAsOptions(state.projects.projects || []);
    const sel = el(
      "select",
      { class: "btn", "aria-label": `Same as an existing project, for ${suggestion.value}` },
      el("option", { value: "" }, opts.length ? "Same as…" : "Same as… (no projects yet)")
    );
    for (const o of opts) sel.appendChild(el("option", { value: o.id }, o.label));
    sel.disabled = opts.length === 0;
    sel.onchange = async () => {
      const target = sel.value;
      sel.selectedIndex = 0;
      if (target) await placeSuggestion(suggestion, target);
    };
    return sel;
  }

  /** mapProjectSelect folds a LOCAL project into another one — normally one of
   *  the org's. The rules move with it and the local entry goes; see
   *  projects.MapProjectTo for why keeping it beside its target would make
   *  every one of its blocks a conflict.
   *
   *  It offers every project except this one, so a person cannot map a project
   *  onto itself — which the daemon also refuses, since doing it would delete
   *  the entry and then put its rules back on the one just removed. */
  function mapProjectSelect(project) {
    const opts = sameAsOptions(state.projects.projects || []).filter((o) => o.id !== project.id);
    const sel = el(
      "select",
      // ⚠️ A DISTINCT ACCESSIBLE NAME from the suggestion picker's "Same as an
      // existing project, for X". They are two different controls — one places
      // a suggestion, this one folds a whole project away — and sharing a name
      // pattern made a spec counting suggestion pickers find these too. A name
      // is an identity; two controls with one identity is a bug for a screen
      // reader before it is a bug for a test.
      { class: "btn", "aria-label": `Map ${project.title} onto another project` },
      el("option", { value: "" }, opts.length ? "Same as…" : "Same as… (nothing to map to)")
    );
    for (const o of opts) sel.appendChild(el("option", { value: o.id }, o.label));
    sel.disabled = opts.length === 0;
    sel.onchange = async () => {
      const target = sel.value;
      sel.selectedIndex = 0;
      if (target) await mapProject(project, target);
    };
    return sel;
  }

  async function mapProject(project, target) {
    if (!target) return;
    const chosen = (state.projects.projects || []).find((p) => p.id === target);
    const res = await sendJSON(`/v1/projects/${encodeURIComponent(project.id)}/same-as`, "POST", { same_as: target });
    // The confirmation is placed on the TARGET row, because the source row is
    // about to stop existing — a note under a row that disappears is a note
    // nobody reads.
    if (res.ok) noteLocalConfirmation(`project:${target}`, res.body, chosen && chosen.origin);
    await loadAll();
    route();
  }

  async function placeSuggestion(suggestion, target) {
    if (!target) return;
    const chosen = (state.projects.projects || []).find((p) => p.id === target);
    const res = await sendJSON("/v1/projects/place", "POST", { suggestion: suggestion.id, same_as: target });
    // The sentence depends on WHOSE project it was: an Atlas-origin target gets
    // "the org's project is unchanged", a local one gets the general advice.
    if (res.ok) noteLocalConfirmation(`project:${target}`, res.body, chosen && chosen.origin);
    await loadAll();
    route();
  }

  // ⚠️ **THIS USED TO CALL `prompt()`, AND IN THE DESKTOP APP THAT DID NOTHING
  // AT ALL.** WKWebView — what the Tauri shell runs on — does not implement
  // `window.prompt` unless the host app provides a text-input panel, and Tauri
  // does not. So it returned null instantly, the `if (!title) return` swallowed
  // it, and pressing "New project" was silent: no dialog, no project, no error.
  //
  // It was invisible to the suite because Playwright AUTO-HANDLES native
  // dialogs, so the browser spec passed while the shipped app had no dialog to
  // handle. That is why the page now owns its own field, and why
  // `TestPageUsesNoNativeDialogs` exists: the rule is not "this one call was
  // replaced", it is that a native dialog anywhere in this page is a control
  // that silently does nothing for the people who use the app.
  //
  // The prefill is the suggestion's own value — the repository name — because
  // that is what a person would type, and the field is selected on open so the
  // first keystroke replaces it.
  function startNamingProject(suggestion) {
    state.naming = { id: suggestion.id, title: suggestion.value, error: "" };
    route();
    // After the render, not before: the input does not exist yet.
    requestAnimationFrame(() => {
      const input = document.getElementById("projectNameInput");
      if (input) {
        input.focus();
        input.select();
      }
    });
  }

  /** namingThis reports whether this suggestion is the one being named. */
  function namingThis(suggestion) {
    return !!(state.naming && state.naming.id === suggestion.id);
  }

  function cancelNamingProject() {
    state.naming = null;
    route();
  }

  async function bundleSuggestion(suggestion, workstreams, title) {
    // NEGATIVE 1: an empty name creates nothing and leaves the suggestion where
    // it was. Said out loud rather than silently ignored — silence here is the
    // exact defect this replaced.
    const name = validProjectTitle(title);
    if (!name) {
      state.naming = { id: suggestion.id, title: title || "", error: "Give the project a name first." };
      route();
      return;
    }
    const workstream = (workstreams[0] && workstreams[0].key) || "development";
    const res = await sendJSON("/v1/projects/bundle", "POST",
      { title: name, workstream, suggestions: [suggestion.id] });
    if (res.ok && res.body && res.body.project && res.body.project.id) {
      noteLocalConfirmation(`project:${res.body.project.id}`, res.body);
      state.naming = null;
    } else {
      // NEGATIVE 2: a refusal is REPORTED. The page going quiet on a failed
      // create is indistinguishable from the bug this fixes.
      state.naming = { id: suggestion.id, title: name, error: "That could not be created. Nothing was changed." };
    }
    await loadAll();
    route();
  }

  async function setWorkstreamOff(key, off) {
    const res = await sendJSON(`/v1/workstreams/${encodeURIComponent(key)}/off`, "PUT", { off });
    if (res.ok) noteLocalConfirmation(`workstream:${key}`, res.body);
    await loadAll();
    route();
  }

  // ---- Settings ----

  // fieldNote renders a control's readonly note (env-pinned, takes priority
  // since it explains why the control can't be touched at all) or its
  // settings-error (a refusal from the last PUT about this key), or nothing.
  function fieldNote(key, readonly) {
    if (readonly.has(key)) return el("div", { class: "settings-note readonly-note" }, readonlyNote(key));
    const err = settingsErrorFor(key);
    return err ? el("div", { class: "settings-note error-note" }, err) : null;
  }

  function renderSettings(root) {
    const { settings } = state;
    root.innerHTML = "";
    if (!settings) {
      root.appendChild(el("p", { class: "loading" }, "Settings unavailable — Signal may not be running."));
      return;
    }
    const readonly = new Set(settings.readonly || []);
    const atlasOn = atlasEnabled(settings);
    const startAtLogin = startAtLoginProps();

    const bar = renderRestartBar();
    if (bar) root.appendChild(bar);

    root.appendChild(
      el(
        "div",
        { class: "tiles", style: "grid-template-columns:1fr 1fr" },
        el(
          "div",
          { class: "tile" },
          el("div", { class: "l" }, "Environment"),
          el("div", { class: "codebox" },
            el("input", { type: "text", id: "codeInput", placeholder: "atlas-dev.keld.co/ABCD-EFGH" }),
            el("button", { class: "btn", onclick: submitCode }, "Switch")
          ),
          state.configError
            ? el("div", { class: "settings-note error-note" }, state.configError)
            : state.configHost
            ? el("div", { class: "settings-note", style: "color:var(--green-strong)" }, `Now pointing at ${state.configHost}.`)
            : el("div", { class: "settings-note", style: "color:var(--muted)" }, "Paste a setup code from any Atlas. Signal restarts and points there.")
        ),
        el(
          "div",
          { class: "tile" },
          el("div", { class: "l" }, "Attribution"),
          el(
            "div",
            { class: "settings-row" },
            el("span", {}, "Suggest projects from repositories and ticket keys", el("div", { class: "desc" }, "Always on — deterministic, no model, costs nothing.")),
            switchEl({ checked: true, disabled: true })
          ),
          // Vector attribution used to be the second row here. It is a
          // developer control while the feature is still being built — see
          // renderDevAttribution — so a person who never turned developer mode
          // on cannot switch on a 1.2 GB download and a message-reading model
          // from this tile.
          el(
            "div",
            { class: "settings-row" },
            el("span", {}, "Start at login", el("div", { class: "desc" }, `Not yet a working toggle here — ${startAtLogin.note}.`)),
            switchEl({ checked: startAtLogin.checked, disabled: startAtLogin.disabled })
          ),
          el(
            "div",
            { class: "settings-row" },
            el("span", {}, "Show details on cards"),
            switchEl({ checked: !!state.local.showDetails, onChange: (v) => { state.local.showDetails = v; saveLocalPrefs(state.local); route(); } })
          )
        ),
        renderDevBlocksTile(settings, atlasOn, readonly)
      )
    );
  }

  /** Whether the developer block-granularity control is drawn.
   *
   *  ⚠️ **OFF BECAUSE IT HAS NOT BEEN TESTED, NOT BECAUSE IT IS GONE.** It
   *  changes how a block is CUT — the 20-minute budget becomes 5 minutes, one
   *  prompt, or one minute — which is the shape of every block a machine
   *  produces from then on, and nothing has exercised the non-default modes end
   *  to end. A control that silently changes what the whole product measures is
   *  not something to leave in reach while that is true.
   *
   *  The setting itself is untouched: `KELD_DEV_BLOCKS` and the `dev_blocks`
   *  key still work, the server still refuses them while Send to Atlas is on,
   *  and every test for that refusal still runs. This hides one control, and
   *  flipping it back is this one line.
   */
  const SHOW_BLOCK_GRANULARITY = false;

  function renderDevBlocksTile(settings, atlasOn, readonly) {
    const modes = [
      ["", "20 minutes", "default"],
      ["bin", "5 minutes", "one bin"],
      ["prompt", "per prompt", ""],
      ["minute", "1 minute", "dev store"],
    ];
    const current = settings.dev_blocks || "";
    const isReadonly = readonly.has("dev_blocks");
    const disabled = atlasOn || isReadonly;
    const grid = el(
      "div",
      { class: "radio-grid" },
      ...modes.map(([value, label, note]) =>
        el(
          "label",
          {},
          el("input", {
            type: "radio",
            name: "devblocks",
            checked: current === value,
            disabled,
            onchange: () => updateSettings({ dev_blocks: value }),
          }),
          label,
          note ? el("small", {}, ` ${note}`) : null
        )
      )
    );
    // The readonly note takes priority — it's env-pinned regardless of
    // Send to Atlas, and the "available while it's off" sentence would be
    // actively wrong if send_to_atlas happens to already be off. A 409's
    // error note is shown too (it can only ever be this same refusal, but
    // settingsErrorFor is the one seam every field note goes through).
    let note = null;
    if (isReadonly) note = el("div", { class: "settings-note readonly-note" }, readonlyNote("dev_blocks"));
    else if (atlasOn) note = el("div", { class: "settings-note" }, "Available while Send to Atlas is off. Dev blocks never leave the machine.");
    const err = settingsErrorFor("dev_blocks");
    // ⚠️ **SEND TO ATLAS IS NOT A DEVELOPER CONTROL, AND HIDING IT WITH THE
    // DEVELOPER ROWS WOULD BE A BUG, NOT A FEATURE.** It decides whether this
    // machine publishes at all — the single most consequential switch on the
    // page — and it lives in this box only because the developer rows below it
    // are refused or reinterpreted depending on it (see the comment on the row
    // itself). So the box is always drawn and always carries that row; what
    // developer mode gates is the rows underneath, and the heading, which is
    // the only part that is actually about developing.
    const dev = devModeOn();
    return el(
      "div",
      { class: "tile", style: "background:var(--nested)" },
      el("div", { class: "l" }, dev ? "Developer" : "Atlas"),
      // ⚠️ Send to Atlas lives HERE rather than in a tile of its own. The two
      // controls are read together and never separately: every other switch in
      // this box is refused or reinterpreted depending on it, so putting them
      // side by side is what makes those refusals legible instead of arriving
      // as an error from a control three tiles away.
      el(
        "div",
        { class: "settings-row" },
        el("span", {}, "Send to Atlas", el("div", { class: "desc" }, "Publish focus blocks, sync projects, take the org's workstreams. Off: nothing leaves this machine.")),
        switchEl({ checked: atlasOn, disabled: readonly.has("send_to_atlas"), onChange: (v) => updateSettings({ send_to_atlas: v }) })
      ),
      fieldNote("send_to_atlas", readonly),
      dev && SHOW_BLOCK_GRANULARITY ? el("div", { class: "settings-sep" }) : null,
      dev && SHOW_BLOCK_GRANULARITY ? el("div", { style: "margin-top:6px;color:var(--muted)" }, "Block granularity") : null,
      dev && SHOW_BLOCK_GRANULARITY ? grid : null,
      dev && SHOW_BLOCK_GRANULARITY ? note : null,
      dev && SHOW_BLOCK_GRANULARITY && err ? el("div", { class: "settings-note error-note" }, err) : null,
      dev ? el("div", { class: "settings-sep" }) : null,
      dev ? renderDevGenerate(settings) : null,
      dev ? el("div", { class: "settings-sep" }) : null,
      dev ? renderDevAttribution(settings, readonly) : null
    );
  }

  // renderDevAttribution is the vector-attribution switch, a DEVELOPER control
  // for as long as the feature is still being built (moved here from the
  // Attribution tile on 2026-09-09). Two reasons it is gated rather than merely
  // labelled: switching it on starts a 1.2 GB download and a model that reads
  // messages on this device, and the installer now writes it OFF on every
  // install (settings.WriteInstallDefaults), so the only way it turns on is a
  // person in developer mode choosing it. The env pin (KELD_ATTRIBUTION) still
  // wins and still renders read-only here, exactly as it did in its old tile.
  function renderDevAttribution(settings, readonly) {
    return el(
      "div",
      {},
      el(
        "div",
        { class: "settings-row" },
        el("span", {}, "Vector attribution", el("div", { class: "desc" }, "In development. Downloads a 1.2 GB text model and reads your messages on this device to name projects for non-coding work. Off on every install.")),
        switchEl({ checked: !!settings.attribution, disabled: readonly.has("attribution"), onChange: (v) => updateSettings({ attribution: v }) })
      ),
      fieldNote("attribution", readonly)
    );
  }

  // renderDevGenerate is the "Generate block" switch and the repository list it
  // draws from.
  //
  // ⚠️ **IT IS NOT DISABLED WHILE SEND TO ATLAS IS ON, UNLIKE THE GRANULARITY
  // ABOVE IT, AND THAT IS DELIBERATE RATHER THAN AN OVERSIGHT.** A dev
  // granularity MISLABELS work someone really did; the generator ADDS work
  // nobody did, and travelling all the way to Atlas is the entire thing being
  // tested. What makes it acceptable is that every generated session is named
  // `devgen-…`, so the rows stay filterable and deletable wherever they land.
  function renderDevGenerate(settings) {
    const on = !!settings.dev_generate;
    const repos = Array.isArray(settings.dev_repos) ? settings.dev_repos : [];
    return el(
      "div",
      {},
      el(
        "div",
        { class: "settings-row" },
        el("span", {}, "Generate block button", el("div", { class: "desc" }, "Puts a button in the top bar that writes one synthetic session. It becomes a real block: watched, ingested, cut, priced and published like any other.")),
        switchEl({ checked: on, onChange: (v) => updateSettings({ dev_generate: v }) })
      ),
      on
        ? el(
            "div",
            {},
            el("div", { style: "margin-top:10px;color:var(--muted)" }, "Repositories it draws from"),
            // ⚠️ The current value is a CHILD, not a `value` attribute. `el`
            // sets attributes, and `setAttribute("value", …)` does nothing at
            // all on a textarea — its content is its child text node — so the
            // box rendered empty on every reload while the setting was stored
            // correctly, which reads as "my repositories were not saved".
            el("textarea", {
              id: "devReposInput",
              "aria-label": "Repositories the block generator draws from",
              class: "devrepos",
              rows: 4,
              placeholder: defaultDevRepos().join("\n"),
              onchange: (e) => updateSettings({ dev_repos: splitRepos(e.target.value) }),
            }, repos.join("\n")),
            // The fourth entry is the point of the control, not a footnote:
            // every declared repository attributes cleanly, so without one that
            // matches nothing the Projects pane's own job is never exercised.
            el("div", { class: "settings-note", style: "color:var(--muted)" },
               "One per line; blank uses the three defaults. A fourth, randomly named repository is always in the mix, so you can see an unattributed block.")
          )
        : null
    );
  }

  /** The three stable repositories the daemon falls back to. Shown as the
   *  textarea's placeholder so the box is never a blank prompt with no clue
   *  what belongs in it. */
  function defaultDevRepos() {
    return [
      "github.com/ncx-ai/keld-signal",
      "github.com/ncx-ai/keld-atlas",
      "github.com/ncx-ai/sdk-testbench",
    ];
  }

  /** splitRepos turns the textarea into the array the route takes. Empty lines
   *  are dropped rather than sent as empty repositories, and an all-blank box
   *  sends an empty array, which the daemon reads as "use the defaults". */
  function splitRepos(text) {
    return String(text || "")
      .split("\n")
      .map((s) => s.trim())
      .filter((s) => s.length > 0);
  }

  // renderRestartBar renders nothing at all while idle (the common case) —
  // see restartBarText/nextRestartStatus in app.js's pure section for the
  // sequence it reflects. Returns null rather than a hidden node, so the
  // caller can skip appendChild entirely.
  function renderRestartBar() {
    const status = state.restart.status;
    if (status === RESTART_IDLE) return null;
    const text = restartBarText(status);
    const canClick = status === RESTART_NEEDED;
    return el(
      "div",
      { class: "restart-bar" },
      el("span", {}, text),
      canClick ? el("button", { class: "btn", onclick: clickRestart }, "Restart") : null
    );
  }

  async function submitCode() {
    const input = document.getElementById("codeInput");
    const code = input && input.value.trim();
    if (!code) return;
    state.configError = "";
    state.configHost = "";
    const res = await sendJSON("/v1/config", "POST", { code });
    if (!res.ok) {
      state.configError = configErrorText(res.status, res.body);
      route();
      return;
    }
    state.configHost = (res.body && res.body.host) || "";
    if (res.body && res.body.restart_required) {
      state.restart.patch = {};
      state.restart.status = nextRestartStatus(state.restart.status, "restart_required");
    }
    await loadAll();
    route();
  }

  // ---- Router / boot ----

  // renderGenerateButton shows the developer's "Generate block" button when the
  // setting is on, and hides it otherwise.
  //
  // ⚠️ **THE BUTTON REPORTS WHAT WAS WRITTEN, NOT "done".** What it creates is a
  // transcript; the block appears a poll or two later, once the watcher has seen
  // the file, the sidecar has ingested it and the cutter has closed it. A button
  // that said "done" and left the page unchanged for ten seconds would read as
  // broken, so it names the repository and says the block is on its way.
  function renderGenerateButton() {
    const btn = document.getElementById("genBlockBtn");
    if (!btn) return;
    // ⚠️ Gated on developer mode AS WELL as the setting. Without this, someone
    // who turned the generator on and then left developer mode would keep a
    // developer button in their top bar with no way to reach the switch that
    // removes it — the setting lives in the box that just disappeared.
    const on = !!(state.settings && state.settings.dev_generate) && devModeOn();
    btn.hidden = !on;
    if (!on) return;
    btn.onclick = clickGenerateBlock;
  }

  // ⚠️ **THE BUTTON IS NEVER DISABLED.** It used to grey itself out for the
  // duration of the request plus the label's own hold, which made a control
  // whose entire purpose is "give me another block" refuse the second press for
  // six seconds. Queueing several is a normal thing to want, and the daemon
  // serialises the pipeline drive (daemon/devgen.go) so overlapping presses are
  // safe — the page has no business enforcing that with a disabled attribute.
  //
  // `inFlight` counts presses rather than blocking them, so the label can say
  // how many are still running instead of hiding the fact.
  let inFlight = 0;

  async function clickGenerateBlock() {
    const btn = document.getElementById("genBlockBtn");
    if (!btn) return;
    inFlight++;
    const previous = "Generate block";
    btn.textContent = inFlight > 1 ? `Generating… (${inFlight})` : "Generating…";
    try {
      const res = await sendJSON("/v1/dev/generate", "POST", {});
      const body = (res && res.body) || {};
      if (res && res.ok && body.repo) {
        // ⚠️ **THE LABEL REPORTS THE BLOCK, NOT THE REQUEST.** The route now
        // drives the pipeline and answers with how many blocks the ledger
        // actually holds for this session, so a tick here means the block is
        // on the page below — it used to mean only that a file had been
        // written, while the block appeared up to five minutes later or not at
        // all. A zero says so rather than showing a tick over nothing.
        const n = Number(body.blocks || 0);
        btn.textContent = n > 0
          ? shortRepo(body.repo) + " ✓"
          : shortRepo(body.repo) + " — not cut";
      } else {
        btn.textContent = "Failed";
      }
    } catch (e) {
      btn.textContent = "Failed";
    }
    inFlight--;
    // Re-read once: by the time the route answered, the block is already in
    // the ledger, so this is a refresh rather than a hopeful poll.
    await loadAll();
    route();
    // Six seconds, not two and a half. The label names the repository the block
    // was generated for, which is the one fact worth reading — and the reload
    // above eats part of the window, so the shorter delay left barely a second
    // to see it.
    //
    // A press that is still running wins the label back: restoring "Generate
    // block" underneath a live request would say nothing is happening while
    // something is.
    setTimeout(() => {
      const b = document.getElementById("genBlockBtn");
      if (!b) return;
      if (inFlight > 0) {
        b.textContent = inFlight > 1 ? `Generating… (${inFlight})` : "Generating…";
        return;
      }
      b.textContent = previous;
    }, 6000);
  }

  /** shortRepo trims a remote to its last path segment for a button label.
   *  Never truncated mid-word: an identifier cut short is a false identifier,
   *  so the whole segment is kept however long it is. */
  function shortRepo(remote) {
    const parts = String(remote || "").split("/");
    return parts[parts.length - 1] || String(remote || "");
  }

  function renderEnvPill() {
    const pill = document.getElementById("envPill");
    const settings = state.settings;
    if (!settings) {
      pill.hidden = true;
      renderGenerateButton();
      return;
    }
    pill.hidden = false;
    pill.textContent = atlasEnabled(settings) ? "Send to Atlas: on" : "Local only";
    renderGenerateButton();
  }

  // devTap holds the streak between clicks. Module state rather than a stored
  // preference: an unfinished streak is not something to remember across a
  // reload — see DEV_TAP_WINDOW_MS.
  let devTap = { taps: 0, last: 0 };
  let devHintTimer = null;

  function devModeOn() {
    return !!loadLocalPrefs().devMode;
  }

  /** renderNavVersion draws the version under the status line, and wires the
   *  hidden gesture onto it.
   *
   *  ⚠️ **DRAWN ONLY WHEN THERE IS A VERSION TO DRAW.** An absent daemon row
   *  means the page has not heard from the daemon, not that it is version-less,
   *  and rendering an empty line (or "vundefined") would put a blank control in
   *  the corner that still counts taps. */
  function renderNavVersion() {
    const el0 = document.getElementById("navVersion");
    if (!el0) return;
    const label = versionLabel(versionFromHealth(state.ledger ? state.ledger.health : []));
    el0.hidden = label === "";
    if (label === "") return;
    if (el0.dataset.label !== label) {
      el0.dataset.label = label;
      el0.textContent = label;
    }
    el0.onclick = () => {
      const next = devTapNext(devTap, devModeOn(), Date.now());
      devTap = { taps: next.taps, last: next.last };
      if (next.toggled) {
        const prefs = loadLocalPrefs();
        prefs.devMode = next.on;
        saveLocalPrefs(prefs);
        showNavVersionHint(el0, label, next.on ? "Developer mode on" : "Developer mode off");
        route();
        return;
      }
      if (next.remaining > 0) {
        showNavVersionHint(el0, label, `${next.remaining} more…`);
      }
    };
  }

  // The hint is the only feedback this gesture gives, and it appears only once
  // a streak is clearly deliberate. It replaces the label for a moment and then
  // puts it back — the element must never be left showing anything but the
  // version, or the corner of the page quietly becomes a status area.
  function showNavVersionHint(node, label, text) {
    node.textContent = text;
    if (devHintTimer) clearTimeout(devHintTimer);
    devHintTimer = setTimeout(() => {
      node.textContent = label;
      devHintTimer = null;
    }, 1400);
  }

  function renderNavHealth(alert) {
    const dot = document.getElementById("navHealthDot");
    const text = document.getElementById("navHealthText");
    dot.classList.remove("ok", "bad", "warn");
    const s = navHealthState({
      offline: state.offline,
      alert,
      health: state.ledger ? state.ledger.health : [],
      settings: state.settings,
    });
    dot.classList.add(s.tone);
    text.textContent = s.text;
  }

  /**
   * The service banner — the machine-level "something Signal depends on has
   * stopped working" strip, sitting beside the offline banner above every
   * pane rather than inside the Today pane's health strip. Two reasons, both
   * about being unmissable: it must be there whichever pane a person lands
   * on, and it is the one place on this page that already carries a fact of
   * this size ("Signal is not running on this machine").
   *
   * Everything it shows comes from the ledger. The one thing held locally is
   * whether a Restart request is in flight, and reconcileServiceRestart hands
   * even that back to the server's answer as soon as one arrives.
   */
  function renderServiceBanner(alert) {
    const banner = document.getElementById("serviceBanner");
    if (!banner) return;
    // Reconcile FIRST, including against a null alert: a ledger that no
    // longer reports an alarm is what retires a pending "Restart requested",
    // and doing this after an early return would leave that status latched
    // forever behind a hidden banner.
    state.serviceRestart = reconcileServiceRestart(state.serviceRestart, alert);
    const status = state.serviceRestart.status;

    // Absent `service` key, `ok`, `not_applicable`, an unrecognised state, or
    // the daemon being unreachable: nothing at all. Never a reassuring
    // "service: fine" derived from a check that did not run.
    if (!alert) {
      banner.hidden = true;
      banner.classList.remove(SERVICE_DEGRADED, SERVICE_STUCK, SERVICE_RESTARTING);
      return;
    }

    banner.hidden = false;
    banner.classList.remove(SERVICE_DEGRADED, SERVICE_STUCK, SERVICE_RESTARTING);
    banner.classList.add(alert.state);

    document.getElementById("serviceHeadline").textContent = serviceHeadline(alert.state);
    document.getElementById("serviceReason").textContent = serviceReasonText(alert);

    const note = document.getElementById("serviceNote");
    const noteText = serviceQueueNote(alert.state);
    note.textContent = noteText;
    note.hidden = !noteText;

    const progress = document.getElementById("serviceProgress");
    const progressText = serviceProgressText(status);
    progress.textContent = progressText;
    progress.hidden = !progressText;
    progress.classList.toggle("failed", status === SERVICE_RESTART_FAILED);

    const btn = document.getElementById("serviceRestartBtn");
    const props = serviceButtonProps(status, alert.state);
    btn.textContent = props.label;
    btn.disabled = props.disabled;
    btn.onclick = clickServiceRestart;
  }

  // How hard the page chases the ledger after a restart was ACCEPTED. The
  // ordinary 30s poll is too slow to watch a restart with, and this is not a
  // substitute for it: it only re-reads /v1/ledger, so whatever it finds is
  // the daemon's own answer. It never concludes anything itself, and it stops
  // whether or not the service came back — the banner then simply keeps
  // saying what the ledger last said.
  const SERVICE_POLL_INTERVAL_MS = 2000;
  const SERVICE_POLL_MAX_ATTEMPTS = 20;

  async function watchServiceBack() {
    for (let attempt = 0; attempt < SERVICE_POLL_MAX_ATTEMPTS; attempt++) {
      await new Promise((resolve) => setTimeout(resolve, SERVICE_POLL_INTERVAL_MS));
      await loadAll();
      route();
      if (state.serviceRestart.status === SERVICE_RESTART_IDLE) return; // reconciled away
    }
    // Waited the whole window and the ledger never said anything different.
    // Give the press back rather than sitting on "Restart requested" forever
    // — the banner still says what is wrong, and a second attempt is now the
    // only thing left to try.
    if (state.serviceRestart.status === SERVICE_RESTART_ACCEPTED) {
      state.serviceRestart = { status: nextServiceRestart(state.serviceRestart.status, "gave_up"), forState: "" };
      route();
    }
  }

  /**
   * ⚠️ **A 202 IS "ACCEPTED", NOT "FIXED", AND THIS FUNCTION MUST NEVER SAY
   * OTHERWISE.** It moves the button to ACCEPTED and stops. The alarm is
   * retired by `renderServiceBanner` finding no alarm in a later ledger, and
   * by nothing else — optimistically rendering success here is exactly the
   * failure this whole piece of work exists to remove.
   */
  async function clickServiceRestart() {
    const alert = serviceAlert(state.ledger, { offline: state.offline });
    if (!alert) return;
    state.serviceRestart = {
      status: nextServiceRestart(state.serviceRestart.status, "clicked"),
      forState: alert.state,
    };
    route();
    const res = await sendJSON("/v1/service/restart", "POST", {});
    state.serviceRestart = {
      ...state.serviceRestart,
      status: nextServiceRestart(state.serviceRestart.status, res.ok ? "accepted" : "refused"),
    };
    route();
    if (res.ok) watchServiceBack();
  }

  function route() {
    const pane = paneFromHash();
    state.pane = pane;
    setActiveNav(pane);
    renderEnvPill();
    const alert = serviceAlert(state.ledger, { offline: state.offline });
    renderServiceBanner(alert);
    renderNavHealth(alert);
    renderNavVersion();
    document.getElementById("offlineBanner").hidden = !state.offline;
    // ⚠️ **SCOPED TO TODAY BY A BODY CLASS, DELIBERATELY.** The fixed-height
    // layout below only makes sense for a pane with one long list in the
    // middle. Projects and Settings are ordinary documents that should scroll
    // as a whole, and applying a viewport-height grid to them would trap their
    // content in a box. A class here is what keeps that impossible rather than
    // careful.
    document.body.classList.toggle("pane-today", pane === "today");
    const root = document.getElementById("paneRoot");
    if (pane === "today") renderToday(root);
    else if (pane === "projects") renderProjects(root);
    else renderSettings(root);
  }

  window.addEventListener("hashchange", route);

  (async function boot() {
    await loadAll();
    route();
    // Light polling so the Today pane stays current while open; a fixture
    // server answers the same file every time, so this is inert there.
    setInterval(async () => {
      await loadAll();
      route();
    }, 30000);
  })();
}
