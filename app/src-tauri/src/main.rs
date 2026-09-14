// Keld Signal — the desktop shell.
//
// ⚠️ **IT SHOWS THE DAEMON'S PAGE AND DOES NOTHING ELSE.** Everything a person
// sees here is served by keld-agent at 127.0.0.1 from a `go:embed`, which is the
// same page `keld signal open` opens in a browser. One page, two frames: the two
// can never disagree about what a person is looking at, and a fix to either
// reaches both.
//
// ⚠️ **IT HOLDS NO CREDENTIAL AND MAKES NO OUTBOUND REQUEST.** The daemon owns
// every token and every connection to Atlas. This process reads one local file
// to learn a port and a loopback secret, and opens a window. If that ever stops
// being true, the privacy story has to be re-argued from the top.
//
// The window is the SYSTEM web view — WKWebView on macOS, WebView2 on Windows,
// WebKitGTK on Linux — rather than a bundled browser, so the app is one small
// binary. That matters concretely on macOS: the installer already sits behind
// Apple's notary service, which scans every file in a submission, and the pkg
// payload was cut to four files precisely because a 15,000-file sidecar put a
// real submission four hours into an unbounded queue.
//
// ⚠️ **CLOSING THE WINDOW HIDES IT; IT DOES NOT QUIT.** This is a status-item
// app (macOS convention: Bartender, Dropbox, 1Password all behave this way) —
// the daemon collects regardless of whether anyone has this window open, so
// treating a close click as "stop everything" would teach a false lesson. The
// tray's "Quit" is the only path that actually exits the process; "Open Keld
// Signal" re-shows the same window rather than building a new one, so state
// (scroll position, in-flight fetches) survives a hide/show cycle.
//
// ⚠️ **THERE IS EXACTLY ONE WINDOW, AND THAT IS A CORRECTNESS RULE RATHER THAN
// A TIDINESS ONE.** A window resolves the daemon's address once, when it is
// built, and the daemon binds an ephemeral port that moves on every restart —
// which is the whole reason `follow_agent` exists. So a second window opened
// after a restart would point at a different origin from the first: one live,
// one dead, both titled "Keld Signal", disagreeing about the same machine.
// `tauri-plugin-single-instance` makes a second launch hand its arguments to
// this process and exit. There are THREE ways a person asks for this window —
// the tray's "Open Keld Signal", a second launch of the binary, and a Dock
// click on macOS (`RunEvent::Reopen`, which is the common one and which the
// plugin does not see, because macOS activates the running process rather than
// starting a second) — and all three call `show_main_window` and nothing else,
// so they cannot drift into three different ideas of what "open" means.

#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use std::fs;
use std::path::PathBuf;
use std::thread;
use std::time::Duration;

use tauri::{
    menu::{Menu, MenuItem},
    tray::TrayIconBuilder,
    image::Image,
    Manager, WebviewUrl, WebviewWindowBuilder, WindowEvent,
};
use tauri_plugin_autostart::{MacosLauncher, ManagerExt};

/// What the daemon writes to `~/.keld/agent.json` at startup. Only two fields
/// matter here; the rest of that file is the daemon's business.
#[derive(serde::Deserialize)]
struct AgentInfo {
    port: u16,
    secret: String,
}

/// keld_home resolves the same directory the Go side does, honouring KELD_HOME
/// so a developer running an isolated daemon can point the shell at it without
/// touching their real install.
fn keld_home() -> PathBuf {
    if let Ok(h) = std::env::var("KELD_HOME") {
        if !h.trim().is_empty() {
            return PathBuf::from(h);
        }
    }
    let home = std::env::var("HOME").unwrap_or_default();
    PathBuf::from(home).join(".keld")
}

/// read_agent returns the running daemon's port and loopback secret, or None.
///
/// ⚠️ **None means "not running", and that is a real state the app must show
/// rather than fail on.** The daemon is installed as a login item and can be
/// stopped, upgraded, or simply not installed yet — an app that showed a blank
/// window or an error dialog in that case would teach a person nothing, while
/// the page has a state that names it and gives the command to fix it.
fn read_agent() -> Option<AgentInfo> {
    let raw = fs::read_to_string(keld_home().join("agent.json")).ok()?;
    let info: AgentInfo = serde_json::from_str(&raw).ok()?;
    if info.port == 0 || info.secret.is_empty() {
        return None;
    }
    Some(info)
}

/// page_url is the daemon's page, carrying the loopback secret ONCE.
///
/// ⚠️ **The secret rides a query parameter because a web view cannot set a
/// header on a top-level navigation**, exactly as the browser path does. The
/// page copies it into a cookie and calls `history.replaceState`, so it does not
/// remain in the address bar, the back stack, or a screenshot someone sends us
/// while debugging. Keeping the two frames on the identical mechanism is
/// deliberate: a second way in would be a second thing to get wrong.
fn page_url(info: &AgentInfo) -> String {
    format!(
        "http://127.0.0.1:{}/?secret={}",
        info.port,
        urlencode(&info.secret)
    )
}

/// How often the shell re-reads `agent.json`. Two seconds is well inside the
/// time a service restart takes and is a stat of one small local file.
const FOLLOW_INTERVAL: Duration = Duration::from_secs(2);

/// follow_agent keeps the window pointed at wherever the daemon currently is.
///
/// ⚠️ **WITHOUT THIS, EVERY DAEMON RESTART STRANDS THE OPEN WINDOW, AND THE
/// PAGE REPORTS THE OPPOSITE OF WHAT IS TRUE.** The daemon binds
/// `127.0.0.1:0` — a fresh ephemeral port each start, which is why `agent.json`
/// exists at all — and this shell used to resolve that file exactly once, in
/// `setup`, baking the port into the window's own origin. So a restart moved
/// the daemon and left the window talking to a closed port.
///
/// That is not a cosmetic staleness. Restarting is the documented outcome of
/// two things a person does in Settings: pasting a setup code to switch Atlas,
/// and toggling Send to Atlas. Both answer `restart_required`, both raise the
/// restart bar, and the bar then polls `/v1/ledger` on the dead origin to
/// decide when Signal is back. Measured on a real machine: the daemon moved
/// 50953 → 51255 and came back healthy in about two seconds, while the window
/// sat on "Restarting…" and then rendered "Signal is not running on this
/// machine" — a confident negative about a daemon that was running perfectly.
///
/// `daemon/onboarding.go` states this rule one level down, for a swap inside a
/// single process: "pair on one port and continue on another and the window can
/// never come back." The same rule has to hold ACROSS a restart, and only the
/// shell can enforce it, because a page whose origin has died cannot navigate
/// itself.
///
/// The secret is part of the comparison, not just the port: `agentcfg.NewSecret`
/// generates a fresh one per daemon start, so a restart that happened to land on
/// the same port still needs the window reloaded with the new credential.
fn follow_agent(window: tauri::WebviewWindow) {
    thread::spawn(move || {
        let mut current = read_agent().map(|i| (i.port, i.secret));
        loop {
            thread::sleep(FOLLOW_INTERVAL);
            let latest = read_agent().map(|i| (i.port, i.secret));
            if latest == current {
                continue;
            }
            let url = match &latest {
                // A daemon that has moved: follow it.
                Some((port, secret)) => page_url(&AgentInfo {
                    port: *port,
                    secret: secret.clone(),
                }),
                // agent.json gone or unreadable. This is deliberately NOT
                // treated as "the daemon stopped": the file is rewritten on
                // every start, so a restart has a window in which it is absent
                // or half-written, and navigating to the not-running screen
                // there would flash it during a routine restart. The page's own
                // banner already says when the daemon is unreachable, and it
                // says so from an actual failed request rather than from a
                // missing file.
                None => {
                    current = latest;
                    continue;
                }
            };
            if let Ok(parsed) = url.parse() {
                if window.navigate(parsed).is_ok() {
                    current = latest;
                }
            }
        }
    });
}

fn urlencode(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for b in s.bytes() {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => {
                out.push(b as char)
            }
            _ => out.push_str(&format!("%{:02X}", b)),
        }
    }
    out
}

/// NOT_RUNNING is shown when no daemon answers.
///
/// It is inline rather than a bundled asset for one reason: this is the ONE
/// screen that must render when nothing else on the machine is working, and an
/// asset pipeline is one more thing that can be missing at exactly that moment.
/// The copy names the command, because "something went wrong" is not an
/// instruction. Colours are the same Institutional Light tokens the real page
/// uses, so the failure state does not look like a different product.
const NOT_RUNNING: &str = r#"data:text/html,
<meta charset="utf-8">
<title>Keld Signal</title>
<style>
body{margin:0;background:%23FEFCF6;color:%230E1A12;
  font:16px/1.6 -apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;
  display:flex;align-items:center;justify-content:center;height:100vh}
main{max-width:34rem;padding:2rem}
h1{font-size:1.35rem;font-weight:600;margin:0 0 .5rem}
p{color:rgba(14,26,18,.66);margin:0 0 1rem}
code{font:13px ui-monospace,SFMono-Regular,monospace;background:%23F6F4EE;
  border:1px solid rgba(24,28,22,.15);border-radius:6px;padding:.35rem .6rem;display:inline-block}
</style>
<main>
<h1>Keld Signal is not running on this machine</h1>
<p>The app shows what the local agent collected. Start it, then reopen this window.</p>
<p><code>keld-agent run</code></p>
<p style="font-size:.9rem">If you have never set it up: <code>keld-agent install</code></p>
</main>"#;

/// show_main_window brings the one window this app has back in front of the
/// person, from whatever state it is currently in.
///
/// ⚠️ **`show()` IS THE STEP A NAIVE VERSION LEAVES OUT, AND IT IS THE ONE THAT
/// MATTERS HERE.** Closing this window hides it rather than destroying it (see
/// the header), so on the ordinary path — someone closed it to the tray this
/// morning and is now reopening it — the window is not merely behind another
/// one, it is not on screen at all. `set_focus` alone would focus a hidden
/// window: nothing appears, and the second launch has just been swallowed with
/// no visible effect, which reads as the app being broken.
///
/// `unminimize` covers the other half of the same question. A minimised window
/// is shown but not readable, and `show` does not restore it, so both calls are
/// needed to cover the states a window can be left in.
///
/// ⚠️ **BOTH CALLERS GO THROUGH HERE ON PURPOSE.** The tray's "Open Keld
/// Signal" and the single-instance callback are answering the identical
/// question — "make the existing window visible and focused" — and if they were
/// written twice they would be fixed once. Nothing here unwraps: the window is
/// looked up by label and can legitimately be absent (during teardown after
/// "Quit", or if `setup` failed to build it), and a panic on a background
/// activation path would take the whole app down for a state it can simply do
/// nothing about.
fn show_main_window(app: &tauri::AppHandle) {
    let Some(window) = app.get_webview_window("main") else {
        return;
    };
    if window.is_minimized().unwrap_or(false) {
        let _ = window.unminimize();
    }
    let _ = window.show();
    let _ = window.set_focus();
}

/// set_autostart enables or disables launching Keld Signal at login, via the
/// official `tauri-plugin-autostart` (a LaunchAgent on macOS — the same
/// mechanism class the daemon's own service registration uses, just a
/// separate registration, since this process and the daemon start
/// independently).
///
/// ⚠️ **Exposed for the PAGE to call, not wired to any UI here.** The Keld
/// Signal page's "Start at login" toggle (`internal/agent/ui/app.js`) is D2's
/// lane, out of scope for this one, and today renders permanently disabled
/// with "This browser only, until the Keld Signal app manages startup."
/// `set_autostart`/`get_autostart` are the exact command names D2 calls via
/// `window.__TAURI__.core.invoke(...)` once that page learns to detect it is
/// running inside this shell — see app/README.md.
#[tauri::command]
fn set_autostart(app: tauri::AppHandle, enabled: bool) -> Result<(), String> {
    let manager = app.autolaunch();
    let result = if enabled {
        manager.enable()
    } else {
        manager.disable()
    };
    result.map_err(|e| e.to_string())
}

/// get_autostart reports whether Keld Signal is currently registered to start
/// at login, read live from the OS's own record (the LaunchAgent plist, the
/// registry Run key, or the XDG autostart entry — whichever this platform
/// uses) rather than a cached flag that could drift from it.
#[tauri::command]
fn get_autostart(app: tauri::AppHandle) -> Result<bool, String> {
    app.autolaunch().is_enabled().map_err(|e| e.to_string())
}

fn main() {
    tauri::Builder::default()
        // ⚠️ **THIS MUST BE THE FIRST PLUGIN IN THE CHAIN.** It is the plugin's
        // own documented requirement, and getting it wrong fails SILENTLY —
        // registered after another plugin it simply never takes effect, the
        // second launch opens its own window, and nothing anywhere reports a
        // problem. Do not reorder this line to keep the list alphabetical.
        //
        // The callback runs in the ALREADY-RUNNING process when someone launches
        // Keld Signal a second time; the second process exits without building a
        // window. `args`/`cwd` are the second launch's argv and working
        // directory, and this app takes no arguments and does no work relative
        // to a directory, so both are deliberately ignored rather than parsed —
        // an unauthenticated local process can invoke this path, and the less it
        // can influence, the less there is to get wrong.
        .plugin(tauri_plugin_single_instance::init(|app, _args, _cwd| {
            show_main_window(app);
        }))
        .plugin(tauri_plugin_autostart::init(MacosLauncher::LaunchAgent, None))
        .invoke_handler(tauri::generate_handler![set_autostart, get_autostart])
        .setup(|app| {
            let (url, title) = match read_agent() {
                Some(info) => (
                    WebviewUrl::External(page_url(&info).parse().expect("loopback url")),
                    "Keld Signal",
                ),
                None => (
                    WebviewUrl::External(NOT_RUNNING.parse().expect("inline url")),
                    "Keld Signal — not running",
                ),
            };
            let window = WebviewWindowBuilder::new(app, "main", url)
                .title(title)
                .inner_size(1180.0, 820.0)
                .min_inner_size(820.0, 560.0)
                .build()?;

            // Follow the daemon across restarts. Started for the not-running
            // frame too, which is what makes the app recover on its own when
            // the daemon starts after it — the ordinary case on a fresh
            // install, where the service is registered before anyone pairs it.
            follow_agent(window.clone());

            // See the header comment: closing the window hides it, it does
            // not quit. `prevent_close` stops the webview from tearing down
            // so re-showing it is instant and loses no state.
            let window_to_hide = window.clone();
            window.on_window_event(move |event| {
                if let WindowEvent::CloseRequested { api, .. } = event {
                    let _ = window_to_hide.hide();
                    api.prevent_close();
                }
            });

            // Tray icon: a MONOCHROME template mark, not the coloured app
            // icon. macOS recolors a "template" image's alpha shape per
            // menu-bar theme (light/dark/highlighted); the coloured 3-tone
            // logo (`icons/icon.png`) would render as flat, wrong-looking
            // color swatches instead. `icons/tray-icon.png` is generated from
            // it: the logo's flat green background dropped, the gold+white
            // mark kept as solid black with the original antialiasing as
            // alpha, cropped to the mark's own bounding box (27x44 — narrow
            // and tall, not padded to a square) rather than shrinking the
            // full square canvas, which at menu-bar size read as a
            // featureless block.
            let tray_icon = Image::from_bytes(include_bytes!("../icons/tray-icon.png"))
                .expect("tray-icon.png must decode");

            let open_item = MenuItem::with_id(app, "open", "Open Keld Signal", true, None::<&str>)?;
            let quit_item = MenuItem::with_id(app, "quit", "Quit", true, None::<&str>)?;
            let tray_menu = Menu::with_items(app, &[&open_item, &quit_item])?;

            TrayIconBuilder::new()
                .icon(tray_icon)
                .icon_as_template(true)
                .tooltip("Keld Signal")
                .menu(&tray_menu)
                .on_menu_event(|app, event| match event.id().as_ref() {
                    // The same call the single-instance callback makes — see
                    // `show_main_window`. "Open" here is never "build a window";
                    // the window always exists, it is just hidden.
                    "open" => show_main_window(app),
                    "quit" => app.exit(0),
                    _ => {}
                })
                .build(app)?;

            Ok(())
        })
        // ⚠️ **`build(...).expect(...)` IS THE SAME FAILURE PATH `run(...)` HAD,
        // NOT A NEW ONE.** `Builder::run` is `self.build(context)?` followed by
        // `App::run`, and `App::run` cannot fail — so `build` was always the
        // only source of the error the old `.expect` was catching, and the
        // message is kept verbatim so a startup failure still aborts the process
        // saying exactly what it said before. The split exists only to get at
        // the event loop's callback; a restructure that quietly moved a startup
        // failure onto a different exit path would be a regression hiding
        // inside a refactor.
        .build(tauri::generate_context!())
        .expect("keld signal: failed to start the window")
        .run(|_app, _event| {
            // ⚠️ **macOS DOES NOT LAUNCH A SECOND PROCESS, SO THE
            // SINGLE-INSTANCE PLUGIN NEVER FIRES ON THE COMMON PATH.** Clicking
            // a running app's Dock icon — which is how a person actually
            // "opens Signal again" on the platform this ships to as a .pkg —
            // makes the OS activate the process that is already running and
            // deliver it a reopen event. No second binary is started, so
            // nothing hands anything to `tauri_plugin_single_instance`. Without
            // this arm the guard would cover only duplicate CLI/binary launches
            // and the story would be half-delivered on its main platform: with
            // the window hidden to the tray, a Dock click would do nothing at
            // all.
            //
            // The `#[cfg]` is REQUIRED, not stylistic: `RunEvent::Reopen` is
            // itself declared macOS-only in Tauri, so naming the variant on
            // Linux or Windows does not compile. `_app`/`_event` are
            // underscore-prefixed for the same reason — on every other platform
            // this closure body is empty and they are genuinely unused.
            //
            // `has_visible_windows` is deliberately ignored. It reports whether
            // anything is on screen, and the answer does not change what to do:
            // hidden means show it, visible means bring it forward, and
            // `show_main_window` is correct for both.
            #[cfg(target_os = "macos")]
            if let tauri::RunEvent::Reopen { .. } = _event {
                show_main_window(_app);
            }
        });
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Covers read_agent/page_url/urlencode together, in one test function
    /// rather than several: all three read/write the process-global
    /// KELD_HOME env var (via keld_home()), and Rust runs #[test] fns on
    /// separate threads by default, so splitting this across tests that each
    /// set KELD_HOME independently would race.
    #[test]
    fn read_agent_page_url_and_urlencode_round_trip() {
        let dir = std::env::temp_dir().join(format!(
            "keld-signal-shell-test-{}-{:?}",
            std::process::id(),
            std::thread::current().id()
        ));
        let _ = fs::remove_dir_all(&dir);
        fs::create_dir_all(&dir).expect("create temp KELD_HOME");
        // SAFETY: this test owns `dir` exclusively and no other test in this
        // binary touches KELD_HOME, so there is no concurrent mutator.
        unsafe {
            std::env::set_var("KELD_HOME", &dir);
        }

        // No agent.json at all: "not running", not an error.
        assert!(
            read_agent().is_none(),
            "no agent.json must read as not-running"
        );

        // A secret containing BOTH `&` and `=` — the two characters that
        // would smuggle extra query parameters if urlencode missed them.
        let secret = "a&b=c d/e+f";
        let body = format!(r#"{{"port": 63946, "secret": "{}"}}"#, secret);
        fs::write(dir.join("agent.json"), body).expect("write agent.json");

        let info = read_agent().expect("a valid agent.json must parse");
        assert_eq!(info.port, 63946);
        assert_eq!(info.secret, secret, "the secret must round-trip exactly");

        let url = page_url(&info);
        assert_eq!(
            url,
            format!("http://127.0.0.1:63946/?secret={}", urlencode(secret))
        );
        // The raw `&` and `=` must not survive into the URL unescaped, or
        // they would be read as additional query parameters rather than
        // part of the secret's value.
        assert!(
            !url["http://127.0.0.1:63946/?secret=".len()..].contains(['&', '=']),
            "raw & or = leaked into the URL: {url}"
        );
        assert!(url.contains("%26"), "'&' must be percent-encoded");
        assert!(url.contains("%3D"), "'=' must be percent-encoded");
        assert!(url.contains("%20"), "' ' must be percent-encoded");

        // Port 0 and an empty secret are both "not running", never a
        // half-valid state the caller might act on.
        fs::write(dir.join("agent.json"), r#"{"port": 0, "secret": "x"}"#)
            .expect("write agent.json");
        assert!(read_agent().is_none(), "port 0 must read as not-running");

        fs::write(dir.join("agent.json"), r#"{"port": 111, "secret": ""}"#)
            .expect("write agent.json");
        assert!(
            read_agent().is_none(),
            "an empty secret must read as not-running"
        );

        // Malformed JSON must not panic.
        fs::write(dir.join("agent.json"), "not json").expect("write agent.json");
        assert!(read_agent().is_none(), "malformed JSON must not panic");

        // SAFETY: same justification as the `set_var` above.
        unsafe {
            std::env::remove_var("KELD_HOME");
        }
        let _ = fs::remove_dir_all(&dir);
    }

    #[test]
    fn urlencode_leaves_unreserved_characters_alone() {
        assert_eq!(urlencode("abc-._~123"), "abc-._~123");
    }

    #[test]
    fn urlencode_escapes_everything_else() {
        assert_eq!(urlencode("a b"), "a%20b");
        assert_eq!(urlencode("a&b=c"), "a%26b%3Dc");
        assert_eq!(urlencode("+/"), "%2B%2F");
    }
}
