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

#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use std::fs;
use std::path::PathBuf;

use tauri::{WebviewUrl, WebviewWindowBuilder};

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

fn main() {
    tauri::Builder::default()
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
            WebviewWindowBuilder::new(app, "main", url)
                .title(title)
                .inner_size(1180.0, 820.0)
                .min_inner_size(820.0, 560.0)
                .build()?;
            Ok(())
        })
        .run(tauri::generate_context!())
        .expect("keld signal: failed to start the window");
}
