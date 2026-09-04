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

/** The glyph for one cell's status. "—" (em dash) is reserved for "unknown /
 *  never happened"; a real failure is always "✗", never the same mark. */
export function cellGlyph(cellRead) {
  if (!cellRead.present) return "—";
  switch (cellRead.status) {
    case "ok":
      return "✓";
    case "failed":
      return "✗";
    case "pending":
      return "…";
    case "n/a":
      return "n/a";
    default:
      return "—";
  }
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

export function reasonText(code) {
  return REASON_TEXT[code] || (code ? code : "");
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

  // Two page-only preferences docs/v3/contracts.md does not define a wire
  // field for: "Show details on cards" and "Start at login". Both stay
  // entirely client-side (this browser's localStorage) rather than invent a
  // settings key the daemon has never heard of. See the report for why.
  function loadLocalPrefs() {
    return readJSONStorage(LOCAL_PREFS_KEY, {
      showDetails: false,
      startAtLogin: false,
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
  };

  async function fetchJSON(path, opts) {
    const res = await fetch(path, { credentials: "same-origin", ...opts });
    if (!res.ok) throw new Error(`${path}: ${res.status}`);
    return res.json();
  }

  async function loadAll() {
    try {
      const [ledger, settings, projects] = await Promise.all([
        fetchJSON("/v1/ledger"),
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
    if (blocks.length) root.appendChild(el("div", { class: "wrap" }, table));

    const pending = ledger.pending || [];
    if (pending.length) {
      const list = el("div", { class: "wrap", style: "margin-top:8px" });
      for (const p of pending) {
        list.appendChild(
          el(
            "div",
            { class: "suggestion-row" },
            el("span", { class: "pill wait" }, "waiting"),
            el("span", { style: "margin-left:10px;color:var(--ink-2)" }, reasonText(p.reason) || "Signal hasn't been able to ask for this session's focus blocks yet.")
          )
        );
      }
      root.appendChild(el("div", { class: "section-label" }, `Waiting to be cut · ${pending.length}`));
      root.appendChild(list);
    }

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

  async function updateSettings(patch) {
    try {
      await fetchJSON("/v1/settings", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(patch) });
    } catch {
      // fixture/dev server or an unreachable daemon: keep the optimistic
      // local update below so the toggle still visibly responds.
    }
    // Optimistic only, deliberately not re-fetched here: DevServer's PUT
    // echoes and never persists (see embed.go), so an immediate reload would
    // overwrite this with the unchanged fixture and the toggle would look
    // like it did nothing. The next periodic poll re-syncs from whatever the
    // server actually holds — real on a live daemon, unchanged on a fixture.
    state.settings = { ...state.settings, ...patch };
    route();
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
            el(
              "div",
              { class: "row-actions" },
              el("button", { class: "btn secondary", onclick: () => placeSuggestion(s) }, "Same as…"),
              el("button", { class: "btn", onclick: () => bundleSuggestion(s, workstreams) }, "New project")
            )
          )
        );
      }
    }

    root.appendChild(el("div", { class: "section-label" }, "Your projects"));
    for (const w of workstreams) {
      const inThis = allProjects.filter((p) => p.workstream === w.key && !p.hidden);
      const card = el(
        "div",
        { class: "workstream-card" + (w.off ? " off" : "") },
        el(
          "div",
          { class: "workstream-head" },
          el("span", { class: "name" }, `${w.name}`),
          el("label", {}, "counts for my work ", switchEl({
            checked: !w.off,
            onChange: (v) => setWorkstreamOff(w.key, !v),
          }))
        )
      );
      if (w.off) {
        card.appendChild(el("div", { class: "workstream-off-note" }, "Your work never lands here. Turn on if you work in this area."));
      } else if (!inThis.length) {
        card.appendChild(el("div", { class: "workstream-off-note" }, "No projects yet."));
      } else {
        for (const p of inThis) {
          const conflictIds = conflicts[p.id] || [];
          card.appendChild(
            el(
              "div",
              { class: "project-row" },
              el(
                "div",
                { class: "row-title" },
                p.title,
                el("small", {}, matchedBySummary(p))
              ),
              conflictIds.length
                ? el("span", { class: "pill no" }, "conflict · pick one")
                : el("span", { class: "pill ok" }, p.origin === "atlas" ? "✓ in Atlas" : "local")
            )
          );
          if (conflictIds.length) {
            card.appendChild(el("div", { class: "conflict-note" }, `Also claimed by: ${conflictIds.join(", ")}`));
          }
        }
      }
      root.appendChild(card);
    }
  }

  function kindLabel(kind) {
    return kind === "repo" ? "matched by repository" : kind === "ticket" ? "ticket key from branch names" : "matched by workspace";
  }

  function matchedBySummary(p) {
    const bits = [];
    if (p.repos && p.repos.length) bits.push(`repo ${p.repos[0]}${p.repos.length > 1 ? ` +${p.repos.length - 1}` : ""}`);
    if (p.ticket_key) bits.push(`tickets ${p.ticket_key}-xxx`);
    return bits.join(" · ") || "no rules yet";
  }

  async function placeSuggestion(suggestion) {
    const target = prompt(`Same as which project id? (${(state.projects.projects || []).map((p) => p.id).join(", ")})`);
    if (!target) return;
    await fetchJSON("/v1/projects/place", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ suggestion: suggestion.id, same_as: target }) }).catch(() => {});
    await loadAll();
    route();
  }

  async function bundleSuggestion(suggestion, workstreams) {
    const title = prompt("New project title:", suggestion.value);
    if (!title) return;
    const workstream = (workstreams[0] && workstreams[0].key) || "development";
    await fetchJSON("/v1/projects/bundle", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ title, workstream, suggestions: [suggestion.id] }) }).catch(() => {});
    await loadAll();
    route();
  }

  async function setWorkstreamOff(key, off) {
    await fetchJSON(`/v1/workstreams/${encodeURIComponent(key)}/off`, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ off }) }).catch(() => {});
    await loadAll();
    route();
  }

  // ---- Settings ----

  function renderSettings(root) {
    const { settings } = state;
    root.innerHTML = "";
    if (!settings) {
      root.appendChild(el("p", { class: "loading" }, "Settings unavailable — Signal may not be running."));
      return;
    }
    const readonly = new Set(settings.readonly || []);
    const atlasOn = atlasEnabled(settings);

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
          el("div", { class: "settings-note", style: "color:var(--muted)" }, "Paste a setup code from any Atlas. Signal restarts and points there.")
        ),
        el(
          "div",
          { class: "tile" },
          el("div", { class: "l" }, "Atlas"),
          el(
            "div",
            { class: "settings-row" },
            el("span", {}, "Send to Atlas", el("div", { class: "desc" }, "Publish focus blocks, sync projects, take the org's workstreams. Off: nothing leaves this machine.")),
            switchEl({ checked: atlasOn, disabled: readonly.has("send_to_atlas"), onChange: (v) => updateSettings({ send_to_atlas: v }) })
          )
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
          el(
            "div",
            { class: "settings-row" },
            el("span", {}, "Vector attribution", el("div", { class: "desc" }, "Downloads a 1.2 GB text model and reads your messages on this device to name projects for non-coding work.")),
            switchEl({ checked: !!settings.attribution, disabled: readonly.has("attribution"), onChange: (v) => updateSettings({ attribution: v }) })
          ),
          el(
            "div",
            { class: "settings-row" },
            el("span", {}, "Start at login", el("div", { class: "desc" }, "This browser only, until the Keld Signal app manages startup.")),
            switchEl({ checked: !!state.local.startAtLogin, onChange: (v) => { state.local.startAtLogin = v; saveLocalPrefs(state.local); route(); } })
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

  function renderDevBlocksTile(settings, atlasOn, readonly) {
    const modes = [
      ["", "20 minutes", "default"],
      ["bin", "5 minutes", "one bin"],
      ["prompt", "per prompt", ""],
      ["minute", "1 minute", "dev store"],
    ];
    const current = settings.dev_blocks || "";
    const disabled = atlasOn || readonly.has("dev_blocks");
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
    return el(
      "div",
      { class: "tile", style: "background:var(--nested)" },
      el("div", { class: "l" }, "Developer"),
      el("div", { style: "margin-top:6px;color:var(--muted)" }, "Block granularity"),
      grid,
      disabled
        ? el("div", { class: "settings-note" }, "Available while Send to Atlas is off. Dev blocks never leave the machine.")
        : null
    );
  }

  async function submitCode() {
    const input = document.getElementById("codeInput");
    const code = input && input.value.trim();
    if (!code) return;
    try {
      await fetchJSON("/v1/config", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ code }) });
    } catch {
      // fixture/dev server: nothing to do besides refresh below.
    }
    await loadAll();
    route();
  }

  // ---- Router / boot ----

  function renderEnvPill() {
    const pill = document.getElementById("envPill");
    const settings = state.settings;
    if (!settings) {
      pill.hidden = true;
      return;
    }
    pill.hidden = false;
    pill.textContent = atlasEnabled(settings) ? "Send to Atlas: on" : "Local only";
  }

  function renderNavHealth() {
    const dot = document.getElementById("navHealthDot");
    const text = document.getElementById("navHealthText");
    dot.classList.remove("ok", "bad", "warn");
    if (state.offline) {
      dot.classList.add("bad");
      text.textContent = "not running";
      return;
    }
    const health = visibleHealth(state.ledger ? state.ledger.health : [], state.settings);
    const bad = health.some((h) => h.status === "failed");
    const warn = health.some((h) => h.status === "pending");
    dot.classList.add(bad ? "bad" : warn ? "warn" : "ok");
    text.textContent = bad ? "needs attention" : warn ? "catching up" : "all good";
  }

  function route() {
    const pane = paneFromHash();
    state.pane = pane;
    setActiveNav(pane);
    renderEnvPill();
    renderNavHealth();
    document.getElementById("offlineBanner").hidden = !state.offline;
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
