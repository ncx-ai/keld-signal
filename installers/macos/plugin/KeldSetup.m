// Keld's Installer.app wizard pane — the whole of macOS onboarding, with no
// Terminal, no browser and no second app.
//
// ⚠️ **IT RENDERS; IT DECIDES NOTHING.** Every step runs the `keld` binary
// carried in this bundle's Resources and draws the NDJSON it emits
// (internal/cli's --json seam). Auth, tool detection, download verification and
// path resolution stay in Go, where they are tested. An ObjC reimplementation
// of any of them is the defect this design exists to avoid.
//
// ⚠️ **THIS PANE RUNS BEFORE THE PAYLOAD IS INSTALLED, and that is forced by
// measurement, not preference** (2026-09-14, macOS 26.5.2): a section ordered
// after Install.bundle enters with installStarted=0 and the plugin's host
// process stops the moment installation completes. There is no post-install UI
// in a pkg. So this pane does only what is safe to abandon — redeem a code
// (auth.json) and stage a download — and postinstall does everything that
// rewrites a user's files.
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#import <InstallerPlugins/InstallerPlugins.h>
#import "KeldCode.h"


// ⚠️ THIS PANE LAYS ITSELF OUT, AND THAT IS FORCED BY MEASUREMENT.
//
// An Installer pane's view is hosted OUT OF PROCESS: its superview is an
// `NSNextStepFrame.ViewBridge.jail`. Inside that jail the Auto Layout this code
// originally relied on does not run. Measured on a real installer 2026-09-15,
// with an NSStackView whose alignment was NSLayoutAttributeWidth and whose
// edgeInsets were 20/24:
//
//     container frame = {446, 338}   superview (the pane) = {418, 330}
//     children: x=303 w=121 | x=0 w=446 | x=134 w=290 | x=2 w=420
//
// Three things are wrong there and none are fixable by adding more constraints:
// the view is 28pt WIDER than the pane containing it (so its right-hand content
// is clipped), the children have unrelated widths (the width alignment never
// applied), and none sit at the 24pt inset (edgeInsets never applied either).
//
// Explicit frames are deterministic, need no nib — which would need Xcode — and
// cannot be quietly ignored by a host we do not control.
@interface KeldPaneView : NSView
@property (nonatomic, strong) NSArray<NSView *> *rows;    // laid out top to bottom
@property (nonatomic, strong) NSView *trailingControl;    // shares a line with trailingPartner
@property (nonatomic, strong) NSView *trailingPartner;
// ⚠️ ONLY THE PANE'S OWN CONTENT VIEW CLAMPS ITSELF TO ITS SUPERVIEW. That rule
// exists because the ViewBridge jail hands the content view a frame wider than
// the pane; applied to a NESTED instance it means "grow to fill my parent",
// which made the tool list resize to the whole pane and draw its checkboxes
// over the account section at the top.
@property (nonatomic) BOOL clampsToHost;
// Nested instances sit inside their parent's inset already, so they carry their
// own (usually zero).
@property (nonatomic) CGFloat insetX;
@property (nonatomic) CGFloat insetTop;
/// Height this view needs for `width`, so a parent can lay it out as one row.
- (CGFloat)contentHeightForWidth:(CGFloat)width;
@end

@implementation KeldPaneView

// Top-down coordinates, so the arithmetic below reads in the same order as the
// pane does.
- (BOOL)isFlipped { return YES; }

- (void)layout {
    [super layout];

    // ⚠️ Clamp to the host's bounds FIRST: the jail hands this view a frame
    // larger than the pane it is displayed in (446 against 418, measured), and
    // anything laid out past that edge is silently clipped.
    if (self.clampsToHost && self.superview &&
        !NSEqualSizes(self.frame.size, self.superview.bounds.size)) {
        self.frame = self.superview.bounds;
    }

    const CGFloat spacing = 9, gap = 8;
    const CGFloat insetX = self.insetX, insetTop = self.insetTop;
    CGFloat width = self.bounds.size.width - insetX * 2;
    if (width <= 0) return;
    CGFloat y = insetTop;

    for (NSView *row in self.rows) {
        if (row.hidden) continue;
        CGFloat rowWidth = width;
        // The code field shares its line with the Connect button and takes what
        // the button leaves, so neither is pushed off the edge however narrow
        // the pane turns out to be.
        if (row == self.trailingPartner && self.trailingControl && !self.trailingControl.hidden) {
            NSSize t = self.trailingControl.fittingSize;
            CGFloat tw = MIN(MAX(t.width, 70), width * 0.45);
            rowWidth = width - tw - gap;
            self.trailingControl.frame = NSMakeRect(insetX + rowWidth + gap, y, tw, MAX(t.height, 22));
        }
        CGFloat height = [self heightFor:row width:rowWidth];
        row.frame = NSMakeRect(insetX, y, rowWidth, height);
        y += height + spacing;
    }
}

- (CGFloat)heightFor:(NSView *)v width:(CGFloat)width {
    if ([v isKindOfClass:[WKWebView class]]) {
        // The page takes whatever is left, so it never exceeds the pane and
        // never needs a scrollbar someone would have to go looking for.
        return MAX(140, self.bounds.size.height - v.frame.origin.y - 14);
    }
    if ([v isKindOfClass:[NSProgressIndicator class]]) return 16;
    if ([v isKindOfClass:[NSTextField class]]) {
        NSTextField *tf = (NSTextField *)v;
        NSSize fit = [tf.cell cellSizeForBounds:NSMakeRect(0, 0, width, CGFLOAT_MAX)];
        return MAX(16, ceil(fit.height));
    }
    if ([v isKindOfClass:[KeldPaneView class]]) {
        return [(KeldPaneView *)v contentHeightForWidth:width];
    }
    if ([v isKindOfClass:[NSButton class]]) return MAX(22, v.fittingSize.height);
    return MAX(20, v.fittingSize.height);
}

- (CGFloat)contentHeightForWidth:(CGFloat)width {
    const CGFloat spacing = 9;
    CGFloat inner = width - self.insetX * 2;
    if (inner <= 0) return 0;
    CGFloat total = self.insetTop;
    for (NSView *row in self.rows) {
        if (row.hidden) continue;
        total += [self heightFor:row width:inner] + spacing;
    }
    return MAX(0, total - spacing);
}

@end

@interface KeldSetupPane : InstallerPane <WKNavigationDelegate>
@end

@implementation KeldSetupPane {
    NSView *_view;
    KeldPaneView *_root;
    NSTextField *_codeField;
    NSButton *_connectButton;
    NSTextField *_codeStatus;
    NSProgressIndicator *_engineBar;
    NSTextField *_engineStatus;
    KeldPaneView *_toolsPane;
    NSMutableArray<NSButton *> *_toolChecks;
    NSButton *_retryButton;
    BOOL _paired;
    NSString *_apiURL;
    NSString *_stagedSidecar;
    // Keeps an in-flight NSTask/NSPipe pair alive for the life of the spawn —
    // neither block below is captured BY anything else, so without this ARC
    // could reclaim both moments after launchAndReturnError: returns and no
    // event, and no `done`, would ever arrive.
    NSMutableArray *_activeTasks;
    // Guards startSidecarDownload so re-entering the pane (Back, then
    // Continue) can't pile up a second concurrent ~190 MB download.
    BOOL _sidecarDownloadStarted;
    // Same guard for the browser sign-in: re-entering the pane must not open a
    // second browser window or start a second polling child. Cleared when a
    // sign-in ends without pairing, so Try again can start a fresh one.
    BOOL _signInStarted;
    // Atlas's own approval page, embedded. Hidden until a device_code arrives
    // and again once approval lands.
    WKWebView *_approvalWeb;
    // The wait's own state: the bar that shows it is happening, the URL to
    // re-request if it fails, and how many times we have tried.
    NSProgressIndicator *_approvalBar;
    NSString *_approvalURL;
    int _approvalAttempts;
    // The sections hidden while the approval page is up: an Installer pane has a
    // fixed height, so the web view has to borrow their space rather than push
    // the pane taller (which would simply clip).
    NSMutableArray<NSView *> *_stowedWhileSigningIn;
}

- (NSString *)title { return @"Set Up Keld"; }

// keldPath is the CLI copy build-pkg.sh places in this bundle. The payload is
// not installed yet, so /usr/local/keld/keld does not exist at this point.
- (NSString *)keldPath {
    return [[NSBundle bundleForClass:[self class]] pathForResource:@"keld" ofType:nil];
}

- (NSString *)bundleVersion {
    NSString *v = [[NSBundle bundleForClass:[self class]] objectForInfoDictionaryKey:@"CFBundleShortVersionString"];
    return v ?: @"";
}


// configuredAPIURL is the Atlas this build targets, or nil for the default.
//
// ⚠️ IT IS BAKED IN AT BUILD TIME BECAUSE AN ENVIRONMENT VARIABLE CANNOT REACH
// HERE. Installer.app does inherit the environment of whatever launched it — a
// launch carrying KELD_API_URL appears in /var/log/install.log — but this pane
// runs inside InstallerRemotePluginService, an XPC service that starts with a
// CLEAN environment. Measured 2026-09-14: the pane read `KELD_API_URL=(unset)`
// in the same run where Installer's own env dump showed it set, so `keld`
// defaulted to production and loaded production's page.
//
// build-plugin.sh stamps this key when KELD_API_URL is set AT BUILD TIME; a
// release build stamps nothing and the CLI uses its compiled-in default.
- (NSString *)configuredAPIURL {
    NSString *v = [[NSBundle bundleForClass:[self class]] objectForInfoDictionaryKey:@"KeldAPIURL"];
    return v.length > 0 ? v : nil;
}

// apiArgs returns the --api-url pair for a build that targets a non-default
// Atlas, or nothing at all. Every `keld` invocation that talks to Atlas takes
// it; the sidecar download does not, because it fetches from the release host.
- (NSArray<NSString *> *)apiArgs {
    NSString *api = [self configuredAPIURL];
    return api ? @[@"--api-url", api] : @[];
}

#pragma mark - Running keld

// runKeld spawns the embedded CLI and delivers one parsed NDJSON object per
// line on the MAIN queue. `done` fires exactly once, after BOTH the process
// has exited AND its stdout pipe has been fully drained to EOF.
//
// ⚠️ THOSE ARE TWO SEPARATE SIGNALS ON TWO SEPARATE QUEUES, and `done` used to
// fire on termination alone. `readabilityHandler` runs off a GCD queue that is
// not synchronized with `terminationHandler`'s, so a chunk the OS pipe still
// held when the process exited could be delivered to the readability queue
// AFTER the termination queue had already cleared the handler and called
// `done` — dropping it. `authorized` is the LAST line `keld login --json`
// writes and `_paired` is set only from it (the `staged` event has the same
// shape), so the observable failure was: the machine IS paired, auth.json IS
// written, and the pane still says "That code was not accepted" with Continue
// disabled. Waiting for BOTH signals — in whichever order they arrive — is
// what removes the race rather than narrowing it.
- (void)runKeld:(NSArray<NSString *> *)args
         onEvent:(void (^)(NSDictionary *event))onEvent
            done:(void (^)(int status))done {
    NSString *keld = [self keldPath];
    if (!keld) {
        dispatch_async(dispatch_get_main_queue(), ^{ done(-1); });
        return;
    }
    NSTask *task = [NSTask new];
    task.executableURL = [NSURL fileURLWithPath:keld];
    task.arguments = args;
    NSPipe *out = [NSPipe pipe];
    task.standardOutput = out;
    task.standardError = [NSPipe pipe];   // keep stderr off the console log

    // ⚠️ Hold the pair explicitly. `task`/`out` are locals; the blocks below
    // reference them by their own block-parameter names (`t`/`fh`) or, for
    // `out`, only inside the terminationHandler — neither block is retained
    // by anything outside `task` itself, so nothing keeps `task` (and
    // therefore its blocks) alive once this method returns. Removed again in
    // the done path below, whichever way it's reached.
    if (!_activeTasks) _activeTasks = [NSMutableArray array];
    NSArray *handle = @[task, out];
    [_activeTasks addObject:handle];

    // Every read below, every mutation of `buffer`, and both completion flags
    // are only ever touched on the MAIN queue — the readability handler's own
    // queue is used solely for the blocking `fh.availableData` call, whose
    // result is immediately handed to the main queue. That is what makes two
    // independently-scheduled signals safe to combine without a lock.
    NSMutableData *buffer = [NSMutableData data];
    __block BOOL sawEOF = NO;
    __block BOOL sawExit = NO;
    __block int exitStatus = -1;
    __weak typeof(self) weakSelf = self;
    void (^finishIfReady)(void) = ^{
        if (!sawEOF || !sawExit) return;   // only ever fires once: both flip exactly once
        typeof(self) s = weakSelf; if (!s) return;
        [s->_activeTasks removeObject:handle];
        done(exitStatus);
    };
    void (^drainLines)(void) = ^{
        while (YES) {
            NSRange nl = [buffer rangeOfData:[@"\n" dataUsingEncoding:NSUTF8StringEncoding]
                                     options:0 range:NSMakeRange(0, buffer.length)];
            if (nl.location == NSNotFound) break;
            NSData *line = [buffer subdataWithRange:NSMakeRange(0, nl.location)];
            [buffer replaceBytesInRange:NSMakeRange(0, nl.location + 1) withBytes:NULL length:0];
            NSDictionary *obj = [NSJSONSerialization JSONObjectWithData:line options:0 error:nil];
            if ([obj isKindOfClass:[NSDictionary class]]) onEvent(obj);
        }
    };
    // A weak reference for the handler to nil itself out through, so it does
    // not strongly capture the very pipe whose property retains it — the same
    // block -> pipe -> block cycle `t.terminationHandler = nil` below avoids
    // by using its block parameter instead of a captured variable.
    __weak NSPipe *weakOut = out;
    out.fileHandleForReading.readabilityHandler = ^(NSFileHandle *fh) {
        NSData *chunk = fh.availableData;   // the only part that must run off-main
        dispatch_async(dispatch_get_main_queue(), ^{
            if (chunk.length == 0) {
                // EOF: the pipe's write end closed (the process exited or
                // closed its stdout). Parse whatever is left in the buffer —
                // a final line with no trailing newline must not be silently
                // dropped either — then stop the handler and signal.
                if (buffer.length > 0) {
                    NSDictionary *obj = [NSJSONSerialization JSONObjectWithData:buffer options:0 error:nil];
                    if ([obj isKindOfClass:[NSDictionary class]]) onEvent(obj);
                    [buffer setLength:0];
                }
                weakOut.fileHandleForReading.readabilityHandler = nil;
                sawEOF = YES;
                finishIfReady();
                return;
            }
            [buffer appendData:chunk];
            drainLines();
        });
    };
    // ⚠️ `terminationHandler` retains its block, which captures `handle` (and
    // so `task`) and, if `self` is captured strongly, `self` too — closing
    // task -> block -> handle -> task (and task -> block -> self). Both
    // edges are broken below: `t.terminationHandler = nil` once the handler
    // has what it needs from `t`, on every path that assigns the handler, and
    // a WEAK self capture matching the idiom already used in connect:,
    // loadTools and startSidecarDownload.
    task.terminationHandler = ^(NSTask *t) {
        // ⚠️ DO NOT clear `t.standardOutput` (or any other stream/launch
        // property) here. NSTask raises NSInvalidArgumentException
        // ("task already launched") on those setters once the task has been
        // launched, an ObjC throw inside a dispatch block is uncaught, and the
        // result is SIGTRAP: the plugin process dies and Installer.app puts up
        // "the installer encountered an error, install anyway?". That is
        // exactly what a real install did on 2026-09-14 — the first time this
        // async path ever ran, since the design probe used a synchronous
        // readDataToEndOfFile/waitUntilExit and no automated check can drive a
        // wizard pane. `terminationHandler` is the one property that tolerates
        // a post-launch write (measured, standalone), and clearing it is all
        // the cycle break needs; the pipe is released when this block is.
        t.terminationHandler = nil;   // breaks the task -> block edge
        dispatch_async(dispatch_get_main_queue(), ^{
            sawExit = YES;
            exitStatus = t.terminationStatus;
            finishIfReady();
        });
    };
    NSError *err = nil;
    if (![task launchAndReturnError:&err]) {
        task.terminationHandler = nil;   // same edge, on the launch-failure path
        out.fileHandleForReading.readabilityHandler = nil;
        [_activeTasks removeObject:handle];
        dispatch_async(dispatch_get_main_queue(), ^{ done(-1); });
    }
}

#pragma mark - View

- (NSTextField *)labelWithText:(NSString *)s bold:(BOOL)bold {
    NSTextField *l = [NSTextField labelWithString:s];
    if (bold) l.font = [NSFont boldSystemFontOfSize:[NSFont systemFontSize]];
    // ⚠️ STATE THE ALIGNMENT. Once the stack stretches a label to the full pane
    // width, an unstated alignment is not "left" — it is whatever the cell
    // decides, which put the section headers hard right and the status lines in
    // the middle while the checkboxes stayed left.
    l.alignment = NSTextAlignmentLeft;
    return l;
}

- (NSView *)contentView {
    if (_view) return _view;
    _toolChecks = [NSMutableArray array];

    // Plain subviews in a view that lays them out itself. No NSStackView and no
    // constraints: see KeldPaneView's header for the measurements showing that
    // neither survives the pane's out-of-process host.
    KeldPaneView *pane = [[KeldPaneView alloc] initWithFrame:NSMakeRect(0, 0, 418, 330)];
    pane.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
    pane.clampsToHost = YES;   // the content view, and only it — see the header
    pane.insetX = 20;
    pane.insetTop = 14;

    NSTextField *accountHeader = [self labelWithText:@"Your Keld account" bold:YES];

    _codeField = [[NSTextField alloc] initWithFrame:NSZeroRect];
    _codeField.placeholderString = @"atlas.keld.co/ABCD-EFGH";
    _connectButton = [NSButton buttonWithTitle:@"Connect" target:self action:@selector(connect:)];

    _codeStatus = [self labelWithText:@"Checking this device…" bold:NO];
    _codeStatus.textColor = [NSColor secondaryLabelColor];
    // Long states ("Sign-in didn't finish (…)") wrap within the pane's insets
    // rather than running past them, which reads as missing padding.
    _codeStatus.lineBreakMode = NSLineBreakByWordWrapping;
    _codeStatus.maximumNumberOfLines = 3;

    // Shown only when Atlas could not be reached. The install is all-or-nothing,
    // so this is the one way forward from that state: fix the network, try
    // again. There is deliberately no button that proceeds unverified.
    _retryButton = [NSButton buttonWithTitle:@"Try again" target:self action:@selector(retryIdentity:)];
    _retryButton.hidden = YES;

    NSTextField *engineHeader = [self labelWithText:@"Analysis engine" bold:YES];
    _engineBar = [[NSProgressIndicator alloc] initWithFrame:NSZeroRect];
    _engineBar.style = NSProgressIndicatorStyleBar;
    _engineBar.indeterminate = YES;
    _engineBar.minValue = 0;
    _engineBar.maxValue = 100;
    [_engineBar startAnimation:nil];
    _engineStatus = [self labelWithText:@"Preparing…" bold:NO];
    _engineStatus.textColor = [NSColor secondaryLabelColor];

    NSTextField *toolsHeader = [self labelWithText:@"Your AI tools" bold:YES];
    // The tool rows are their own laid-out view for the same reason as the pane.
    _toolsPane = [[KeldPaneView alloc] initWithFrame:NSZeroRect];
    _toolsPane.rows = @[];
    // Nested: no clamp (it is a row, not the host's view) and no inset of its
    // own, because the pane has already inset it.
    _toolsPane.clampsToHost = NO;
    _toolsPane.insetX = 0;
    _toolsPane.insetTop = 0;

    for (NSView *v in @[accountHeader, _codeField, _connectButton, _codeStatus, _retryButton,
                        engineHeader, _engineBar, _engineStatus, toolsHeader, _toolsPane]) {
        [pane addSubview:v];
    }
    // _connectButton shares the code field's line rather than taking one of its
    // own, and is therefore not in `rows`.
    pane.rows = @[accountHeader, _codeField, _codeStatus, _retryButton,
                  engineHeader, _engineBar, _engineStatus, toolsHeader, _toolsPane];
    pane.trailingPartner = _codeField;
    pane.trailingControl = _connectButton;

    _root = pane;
    _view = pane;
    return _view;
}

// The code field is the one control a person actually needs to type into the
// moment this pane appears; without an explicit initialKeyView, InstallerPane
// falls back to whatever the view hierarchy happens to make first responder,
// which is not guaranteed to be it.
- (NSView *)initialKeyView {
    // ⚠️ NEVER NAME A HIDDEN CONTROL HERE. Installer.app applies
    // initialKeyView on EVERY entry, so this is re-entered on Back-then-Continue
    // — and by then the code field is hidden behind the approval page.
    // Measured with a standalone harness (focustest.m): `makeFirstResponder:` on
    // a HIDDEN NSTextField returns YES and installs its field editor, so the
    // window's first responder becomes an NSTextView nobody can see and every
    // keystroke disappears into it. On screen that reads as the embedded page's
    // email and password fields being disabled — they render, they just never
    // receive a key. First entry hid the bug completely, because the field IS
    // visible then.
    if (_approvalWeb && !_approvalWeb.hidden) return _approvalWeb;
    if (!_codeField.hidden) return _codeField;
    return nil;
}

// klog writes to the unified log, which is the ONLY way to see inside this
// process: the pane runs in InstallerRemotePluginService, out of process, with
// no console and no stderr anyone will ever read. Two bugs here (the fatal
// NSTask property write, and focus going to a hidden field) were each diagnosed
// by other means because this did not exist. Read it with:
//   log show --last 10m --predicate 'process == "InstallerRemotePluginService"'
static void klog(NSString *fmt, ...) {
    va_list ap; va_start(ap, fmt);
    NSString *m = [[NSString alloc] initWithFormat:fmt arguments:ap];
    va_end(ap);
    NSLog(@"keld-pane: %@", m);
}

#pragma mark - Pane lifecycle

- (void)didEnterPane:(InstallerSectionDirection)dir {
    (void)[self contentView];
    klog(@"didEnterPane dir=%ld paired=%d signInStarted=%d web=%@ hidden=%d url=%@",
         (long)dir, (int)_paired, (int)_signInStarted,
         _approvalWeb ? @"yes" : @"no", (int)_approvalWeb.hidden, _approvalURL ?: @"(none)");
    // ⚠️ THE INSTALL IS ALL-OR-NOTHING. Continue is enabled by exactly one
    // thing — a VERIFIED connection to Atlas, either a setup code it accepted or
    // `whoami --verify` confirming the stored credential still works. There is
    // no deferral: a machine that installs unconnected collects nothing, and
    // (the app being unreleased and the CLI being a terminal) has no way to
    // finish that this wizard was built to replace. Catching that HERE is the
    // only option, because no pane can run after the install.
    self.nextEnabled = _paired;
    if (!_paired) [self checkIdentity];
    // Guarded: the pane can be re-entered (Back, then Continue again), and
    // without this a second entry would start a second concurrent ~190 MB
    // download rather than reusing the first.
    if (!_sidecarDownloadStarted) {
        _sidecarDownloadStarted = YES;
        [self startSidecarDownload];
    }

    // ⚠️ THE APPROVAL PAGE IS REBUILT ON RE-ENTRY RATHER THAN REUSED. Coming
    // back to this pane left the embedded page's fields unable to receive a
    // keystroke. Two mechanisms were ruled out by measurement rather than
    // argument: a hidden control CAN take first responder (focustest.m), which
    // was real and is fixed, and was not this; and plain AppKit re-parenting —
    // removing the container from the window and re-adding it, which is what
    // Back-then-Continue does — leaves a WKWebView fully typeable
    // (reparent.m: "VERDICT: typing after re-parent WORKS").
    //
    // What neither harness can reproduce is this pane's actual host: the plugin
    // runs in InstallerRemotePluginService, and the page is therefore a remote
    // view inside a remote view. Rather than guess at that boundary a third
    // time, the view is discarded and rebuilt — which is correct whichever half
    // is at fault, because a brand-new WKWebView has no stale responder link and
    // no suspended content process.
    //
    // The cost is one reload: anything half-typed into the form is lost. That is
    // strictly better than a page that cannot be typed into at all, and the
    // device code survives (the login child keeps polling across the Back), so
    // the flow itself is not restarted.
    // Runs LAST, and only on a real re-entry: on first entry there is no page
    // yet, and an early return here would skip the identity check and the
    // sidecar download above.
    if (dir == InstallerDirectionForward && _approvalWeb && !_approvalWeb.hidden
        && _approvalURL.length > 0) {
        klog(@"rebuilding approval web view for re-entry");
        NSString *url = _approvalURL;
        [self discardApprovalWebView];
        [self showApprovalPage:url];
    }
}

// shouldExitPane writes the handoff postinstall consumes. Returning YES is safe
// because reaching here at all means Continue was enabled, and Continue is
// enabled only for a verified connection — so the handoff can only ever say
// paired.
- (BOOL)shouldExitPane:(InstallerSectionDirection)dir {
    if (dir == InstallerDirectionForward) [self writeHandoff];
    return YES;
}

#pragma mark - Steps

- (void)connect:(id)sender {
    NSString *code = [_codeField.stringValue stringByTrimmingCharactersInSet:
                      [NSCharacterSet whitespaceAndNewlineCharacterSet]];
    if (code.length == 0) {
        _codeStatus.stringValue = @"Enter the setup code from your Keld download page.";
        return;
    }
    _connectButton.enabled = NO;
    _codeStatus.stringValue = @"Connecting…";
    __weak typeof(self) weakSelf = self;
    __block NSString *failure = nil;
    [self runKeld:[@[@"login", @"--code", code, @"--json"] arrayByAddingObjectsFromArray:[self apiArgs]]
          onEvent:^(NSDictionary *e) {
        typeof(self) s = weakSelf; if (!s) return;
        NSString *kind = e[@"event"];
        if ([kind isEqualToString:@"authorized"]) {
            s->_paired = YES;
            s->_apiURL = e[@"api_url"] ?: @"";
            s->_codeStatus.stringValue = [NSString stringWithFormat:@"Connected — %@ · %@",
                                          e[@"principal"] ?: @"", e[@"org"] ?: @""];
            s.nextEnabled = YES;
            [s loadTools];
        } else if ([kind isEqualToString:@"error"]) {
            failure = e[@"message"] ?: @"that code was not accepted";
        }
    } done:^(int status) {
        typeof(self) s = weakSelf; if (!s) return;
        s->_connectButton.enabled = YES;
        if (!s->_paired) {
            s->_codeStatus.stringValue = failure ?: @"That code was not accepted. Check it and try again.";
        }
    }];
}

// loadTools enumerates what is installed WITHOUT writing anything: --dry-run
// emits one `tool` event per detected tool and returns before any write.
- (void)loadTools {
    __weak typeof(self) weakSelf = self;
    NSMutableArray<NSDictionary *> *found = [NSMutableArray array];
    NSMutableArray<NSString *> *args = [@[@"signal", @"setup", @"--dry-run", @"--json"] mutableCopy];
    if (_apiURL.length) { [args addObjectsFromArray:@[@"--api-url", _apiURL]]; }
    [self runKeld:args onEvent:^(NSDictionary *e) {
        if ([e[@"event"] isEqualToString:@"tool"]) [found addObject:e];
    } done:^(int status) {
        typeof(self) s = weakSelf; if (!s) return;
        [s renderTools:found];
    }];
}

- (void)renderTools:(NSArray<NSDictionary *> *)tools {
    for (NSView *v in [_toolsPane.subviews copy]) [v removeFromSuperview];
    _toolsPane.rows = @[];
    [_toolChecks removeAllObjects];
    NSMutableArray<NSView *> *rows = [NSMutableArray array];
    if (tools.count == 0) {
        NSTextField *none = [self labelWithText:@"No supported AI tools found on this device." bold:NO];
        [_toolsPane addSubview:none];
        _toolsPane.rows = @[none];
        [_toolsPane setNeedsLayout:YES];
        return;
    }
    // `action` is one of configured | already_configured | skipped_conflict |
    // will_configure — the last is `signal setup --dry-run --json`'s event for
    // a detected, unconflicted tool that WOULD be written on a real run (see
    // runSetup). Everything except skipped_conflict is tickable and ticked by
    // default, will_configure included: its plain display-name label is
    // correct as-is, since there is nothing more specific to say about a tool
    // that simply hasn't been configured yet.
    for (NSDictionary *t in tools) {
        NSString *display = t[@"display"] ?: t[@"name"];
        NSString *action = t[@"action"] ?: @"";
        NSString *title = [action isEqualToString:@"skipped_conflict"]
            ? [NSString stringWithFormat:@"%@ — has its own telemetry settings; leave it alone", display]
            : display;
        NSButton *check = [NSButton checkboxWithTitle:title target:nil action:nil];
        check.identifier = t[@"name"];
        check.state = [action isEqualToString:@"skipped_conflict"] ? NSControlStateValueOff : NSControlStateValueOn;
        [_toolsPane addSubview:check];
        [rows addObject:check];
        [_toolChecks addObject:check];
    }
    NSTextField *restart = [self labelWithText:
        @"Restart these apps after setup — they read their settings once, at startup." bold:NO];
    restart.lineBreakMode = NSLineBreakByWordWrapping;
    restart.maximumNumberOfLines = 2;
    [_toolsPane addSubview:restart];
    [rows addObject:restart];
    _toolsPane.rows = rows;
    [_toolsPane setNeedsLayout:YES];
    // The tool list changes the pane's height, so the pane relays out too.
    [_root setNeedsLayout:YES];
}

// The download starts on its own and NEVER gates Continue: a late sidecar costs
// nothing (jobs spool until it lands), while a blocked wizard costs a person
// several minutes of staring on a slow connection.
- (void)startSidecarDownload {
    NSString *version = [self bundleVersion];
    NSMutableArray<NSString *> *args = [@[@"signal", @"install-sidecar", @"--json", @"--stage-only"] mutableCopy];
    // A dry-run build carries no real release tag; the Go side then resolves the
    // latest release, which is what onboard.command already does.
    if (version.length && [version rangeOfString:@"dryrun"].location == NSNotFound) {
        // ⚠️ CFBundleShortVersionString is stamped verbatim from build-pkg.sh's
        // own $VERSION, which CI sets from the release tag itself
        // (installers.yml: VER="$TAG", e.g. "v3.0.0-rc.5") — it ALREADY carries
        // the leading "v". Blindly prefixing another one here asked GitHub for
        // "vv3.0.0-rc.5" on every tagged build, which 404s outright; it was
        // invisible on a dry-run build only because "0.0.0-dryrun" takes the
        // branch above instead of reaching this line. Strip any leading "v"
        // first — so this is correct whether or not the bundle version happens
        // to carry one — then add exactly one back.
        NSString *bare = [version hasPrefix:@"v"] ? [version substringFromIndex:1] : version;
        [args addObjectsFromArray:@[@"--tag", [@"v" stringByAppendingString:bare]]];
    }
    __weak typeof(self) weakSelf = self;
    __block NSString *failure = nil;
    // The missing-published-hash warning (installsidecar.go, --json path):
    // `console.Print` writes to the same stream as the NDJSON events, and the
    // pane drops any line it can't parse as one, so under --json that warning
    // reached nobody. The spec justifies the degraded policy on "a human is
    // watching a progress bar" — this is what makes that true.
    __block NSString *warning = nil;
    [self runKeld:args onEvent:^(NSDictionary *e) {
        typeof(self) s = weakSelf; if (!s) return;
        NSString *kind = e[@"event"];
        if ([kind isEqualToString:@"progress"]) {
            long long got = [e[@"received"] longLongValue], total = [e[@"total"] longLongValue];
            if (total > 0) {
                s->_engineBar.indeterminate = NO;
                s->_engineBar.doubleValue = (double)got * 100.0 / (double)total;
                s->_engineStatus.stringValue = [NSString stringWithFormat:@"Downloading… %lld MB of %lld MB",
                                                got / 1048576, total / 1048576];
            }
        } else if ([kind isEqualToString:@"staged"]) {
            s->_stagedSidecar = e[@"path"];
        } else if ([kind isEqualToString:@"error"]) {
            failure = e[@"message"];
        } else if ([kind isEqualToString:@"warning"]) {
            warning = e[@"message"];
            s->_engineStatus.stringValue = warning ?: s->_engineStatus.stringValue;
        }
    } done:^(int status) {
        typeof(self) s = weakSelf; if (!s) return;
        [s->_engineBar stopAnimation:nil];
        s->_engineBar.indeterminate = NO;
        if (s->_stagedSidecar.length) {
            s->_engineBar.doubleValue = 100;
            s->_engineStatus.stringValue = warning.length
                ? [NSString stringWithFormat:@"Ready (%@).", warning]
                : @"Ready. Nothing multi-gigabyte is downloaded, now or later.";
        } else {
            s->_engineBar.doubleValue = 0;
            s->_engineStatus.stringValue = failure
                ? [NSString stringWithFormat:@"Could not download it (%@). Keld will retry in the background.", failure]
                : @"Could not download it. Keld will retry in the background.";
        }
    }];
}

#pragma mark - Identity

// checkIdentity asks whether this machine is ALREADY connected, and does it by
// asking Atlas rather than by looking for auth.json.
//
// ⚠️ `keld whoami` on its own never contacts Atlas — it prints what a local file
// says — so a revoked token and a live one are indistinguishable to it. Enabling
// Continue on that basis would let someone install with a dead credential and
// collect nothing, which is the confused state this pane exists to prevent.
// `--verify` performs the same `Onboarding()` call postinstall will make minutes
// later, so a verified answer predicts that step rather than merely correlating
// with it.
//
// The three failure states are kept apart deliberately: `unauthorized` means a
// code is required, `unreachable` means we learned nothing and the install must
// not proceed, `none` means a fresh machine.
- (void)checkIdentity {
    _codeStatus.stringValue = @"Checking this Mac…";
    _retryButton.hidden = YES;
    __weak typeof(self) weakSelf = self;
    __block NSDictionary *identity = nil;
    [self runKeld:@[@"whoami", @"--verify", @"--json"] onEvent:^(NSDictionary *e) {
        if ([e[@"event"] isEqualToString:@"identity"]) identity = e;
    } done:^(int status) {
        typeof(self) s = weakSelf; if (!s) return;
        NSString *state = identity[@"status"] ?: @"none";
        if ([state isEqualToString:@"verified"]) {
            s->_paired = YES;
            s->_codeField.hidden = YES;
            s->_connectButton.hidden = YES;
            s->_codeStatus.stringValue =
                [NSString stringWithFormat:@"Already connected — %@ · %@",
                 identity[@"principal"] ?: @"", identity[@"org"] ?: @""];
            s.nextEnabled = YES;
            [s loadTools];
            return;
        }
        if ([state isEqualToString:@"unreachable"]) {
            // All-or-nothing: we cannot confirm this machine can report to
            // Atlas, so the install does not proceed. Retry is the only way on.
            s->_codeStatus.stringValue =
                @"Can't reach Atlas — Keld can't be set up right now.";
            s->_retryButton.hidden = NO;
            s.nextEnabled = NO;
            return;
        }
        // `none` or `unauthorized`: this machine needs to be connected. An
        // expired credential is not a reason to nag about the old one, so both
        // read the same to the person.
        //
        // The clipboard is tried first only because it is INSTANT when someone
        // came straight from the download page; with nothing there, the pane
        // fetches a code itself rather than asking anyone to go and find one.
        if ([s prefillFromClipboard]) return;
        [s beginBrowserSignIn];
    }];
}

// beginBrowserSignIn runs the OAuth device-authorization flow: `keld login
// --json` with no --code asks Atlas for a code, opens the browser itself, and
// polls until the person approves.
//
// ⚠️ THE USER CODE IS DISPLAYED ON PURPOSE. Device flow's protection against
// being phished into approving someone else's sign-in is that the code on this
// screen must match the code in the browser; showing only a spinner would throw
// that away. The verification URL is shown for the same reason it is returned —
// if the browser does not open (no default handler, a locked-down Mac), the
// person still has somewhere to go instead of a dead end.
- (void)beginBrowserSignIn {
    if (_signInStarted) return;
    _signInStarted = YES;
    _codeStatus.stringValue = @"Signing in to Keld…";
    _retryButton.hidden = YES;
    __weak typeof(self) weakSelf = self;
    __block NSString *failure = nil;
    // ⚠️ `--no-browser` is load-bearing: the CLI opens the verification URL
    // itself by default, and with the page also embedded here that would put the
    // same approval in two places at once — one of which is the app this wizard
    // exists to avoid sending people to.
    [self runKeld:[@[@"login", @"--json", @"--no-browser"] arrayByAddingObjectsFromArray:[self apiArgs]] onEvent:^(NSDictionary *e) {
        typeof(self) s = weakSelf; if (!s) return;
        NSString *kind = e[@"event"];
        if ([kind isEqualToString:@"device_code"]) {
            // ⚠️ THE USER CODE IS DELIBERATELY NOT SHOWN. Displaying it was the
            // device flow's anti-phishing step, and that step assumed a SECOND
            // surface — a browser the person could compare against. With the
            // approval page embedded here, the pane supplies the code, loads
            // the page, and reads the result: there is nothing to compare it
            // with, so printing it only asks someone to check a number against
            // itself.
            //
            // It comes back if the approval ever moves out of the pane again
            // (a system browser, or a platform that cannot embed a web view),
            // because then the comparison is real.
            // ⚠️ Prefer Atlas's compact route. `verification_url` is the page
            // built for a real browser window: unauthenticated it redirects to
            // the full login, and every state centres itself with min-h-screen,
            // so inside this panel it is a page to scroll around rather than a
            // form to fill in. `installer_url` is the same approval, sized for
            // an embedded view. It is absent on an Atlas that predates it, and
            // that case must still work — hence the fallback rather than a
            // requirement.
            NSString *url = e[@"installer_url"] ?: @"";
            if (url.length == 0) url = e[@"verification_url"] ?: @"";
            // ⚠️ THE PROMPT IS NOT SET HERE ANY MORE. This event marks the
            // moment the page starts LOADING, not the moment it can be used,
            // and the gap between the two is seconds (longer against an Atlas
            // compiling the route on demand). Saying "sign in" over a blank
            // rectangle names an action with nothing to act on, which reads as
            // a form that failed to render — so people retry a page that was
            // still on its way. showApprovalPage: says it is loading; the
            // navigation delegate says it has loaded.
            [s showApprovalPage:url];
        } else if ([kind isEqualToString:@"authorized"]) {
            [s hideApprovalPage];
            s->_paired = YES;
            s->_apiURL = e[@"api_url"] ?: @"";
            s->_codeField.hidden = YES;
            s->_connectButton.hidden = YES;
            s->_codeStatus.stringValue =
                [NSString stringWithFormat:@"Connected — %@ · %@",
                 e[@"principal"] ?: @"", e[@"org"] ?: @""];
            s.nextEnabled = YES;
            [s loadTools];
        } else if ([kind isEqualToString:@"error"]) {
            failure = e[@"message"];
        }
    } done:^(int status) {
        typeof(self) s = weakSelf; if (!s) return;
        if (s->_paired) return;
        [s hideApprovalPage];
        // Sign-in did not complete — expired, declined, or the browser never
        // opened. All-or-nothing: Continue stays disabled and the only way on is
        // to try again, or to paste a code by hand into the field below.
        s->_signInStarted = NO;
        s->_codeStatus.stringValue = failure
            ? [NSString stringWithFormat:@"Sign-in didn't finish (%@). Try again, or paste a setup code.", failure]
            : @"Sign-in didn't finish. Try again, or paste a setup code.";
        s->_retryButton.hidden = NO;
    }];
}

- (void)retryIdentity:(id)sender {
    [self checkIdentity];
}

#pragma mark - Embedded approval

// showApprovalPage puts ATLAS'S OWN sign-in and approval page inside the pane.
//
// ⚠️ THE FIELDS ARE ATLAS'S, NOT OURS, AND THAT IS THE ENTIRE POINT. A native
// email/password form here would work — `POST /auth/login` then
// `POST /cli/device/approve` needs no server change — but it would make this
// installer an auth client handling somebody's org password, dead-end the day
// Atlas gains SSO or 2FA, break password-manager autofill, and teach people that
// typing credentials into a pkg wizard is normal (anyone can build a lookalike
// pkg; only a real page can prove its own origin). Rendering Atlas's page costs
// none of that, and the wizard still never sends anyone to another app.
//
// The trade we accept: a WKWebView has its own cookie store and cannot see
// Safari's session, so someone already signed in to Atlas in their browser signs
// in again here.
- (void)showApprovalPage:(NSString *)urlString {
    NSURL *url = [NSURL URLWithString:urlString];
    if (!url) return;
    KeldPaneView *root = _root;
    if (!_approvalWeb) {
        _approvalWeb = [[WKWebView alloc] initWithFrame:NSMakeRect(0, 0, 560, 300)
                                          configuration:[WKWebViewConfiguration new]];
        // ⚠️ THE PAGE'S TRANSPARENCY IS WASTED UNLESS THE WEB VIEW IS ALSO
        // NON-OPAQUE. Atlas's route drops its canvas so the approval form can
        // sit on the installer's own panel, but a WKWebView paints an opaque
        // white backdrop of its own underneath — so the seam just moves from
        // the page to the view. `underPageBackgroundColor` is the public way to
        // say "paint nothing": the page then composites onto the pane, and the
        // form reads as part of the wizard rather than as a website embedded
        // in one.
        _approvalWeb.underPageBackgroundColor = [NSColor clearColor];
        // ⚠️ `underPageBackgroundColor` alone may not be enough: it colours what
        // is drawn UNDER the page (overscroll), while the view's own opacity is
        // governed by `drawsBackground`, which WebKit exposes only through KVC.
        // Both are set, because the visible result of getting this half wrong is
        // a white rectangle where the seam was supposed to disappear.
        //
        // Wrapped, because an unknown key RAISES — and an uncaught ObjC
        // exception in this process is not an error message, it is SIGTRAP and
        // a dead wizard (measured 2026-09-14, from `t.standardOutput = nil`).
        // If a future WebKit drops the key, the page merely stays opaque.
        @try {
            [_approvalWeb setValue:@NO forKey:@"drawsBackground"];
        } @catch (NSException *e) {
            // Cosmetic only; nothing about the flow depends on it.
        }
        // Knowing when the page is READY is what separates a wait from a
        // failure, and without a delegate the pane cannot tell them apart.
        _approvalWeb.navigationDelegate = self;
        // No size constraints: KeldPaneView gives the page the pane's width and
        // whatever height is left below the status line.
    }
    if (!_approvalBar) {
        // Indeterminate: the load reports no progress fraction, and a bar that
        // invents one is a worse lie than no bar. KeldPaneView gives any
        // NSProgressIndicator a fixed 16pt row, so this needs no sizing.
        _approvalBar = [[NSProgressIndicator alloc] initWithFrame:NSZeroRect];
        _approvalBar.style = NSProgressIndicatorStyleBar;
        _approvalBar.indeterminate = YES;
        _approvalBar.controlSize = NSControlSizeSmall;
        _approvalBar.usesThreadedAnimation = YES;
    }
    // An Installer pane cannot grow, so the approval page borrows the space of
    // the sections below it rather than pushing them off the bottom.
    if (!_stowedWhileSigningIn) _stowedWhileSigningIn = [NSMutableArray array];
    if (_stowedWhileSigningIn.count == 0) {
        for (NSView *v in root.rows) {
            if (v == _codeStatus) continue;   // the code line stays visible above the page
            if (v.hidden) continue;
            [_stowedWhileSigningIn addObject:v];
        }
        for (NSView *v in _stowedWhileSigningIn) v.hidden = YES;
        // The Connect button shares the code field's line and is not in `rows`,
        // so it has to be hidden explicitly or it floats over the page.
        if (!_connectButton.hidden) {
            [_stowedWhileSigningIn addObject:_connectButton];
            _connectButton.hidden = YES;
        }
    }
    // The bar is added BEFORE the page so it sits above it: the web view's row
    // height is "whatever is left", so anything appended after it would be laid
    // out past the bottom edge and silently clipped.
    if (_approvalBar.superview == nil) {
        [root addSubview:_approvalBar];
        root.rows = [root.rows arrayByAddingObject:_approvalBar];
    }
    if (_approvalWeb.superview == nil) {
        [root addSubview:_approvalWeb];
        root.rows = [root.rows arrayByAddingObject:_approvalWeb];
    }
    _approvalWeb.hidden = NO;
    _approvalURL = urlString;
    _approvalAttempts = 0;
    [self beginApprovalLoad:url];
}

// beginApprovalLoad starts a load and puts the pane into its WAITING state: the
// bar runs, and the status line describes what is happening rather than what
// the person should do about it.
- (void)beginApprovalLoad:(NSURL *)url {
    _approvalAttempts++;
    _approvalBar.hidden = NO;
    [_approvalBar startAnimation:nil];
    _codeStatus.stringValue = @"Loading the sign-in page\u2026";
    [_root setNeedsLayout:YES];
    [_approvalWeb loadRequest:[NSURLRequest requestWithURL:url]];
}

- (void)stopApprovalBar {
    [_approvalBar stopAnimation:nil];
    _approvalBar.hidden = YES;
    [_root setNeedsLayout:YES];
}

- (void)webView:(WKWebView *)webView didFinishNavigation:(WKNavigation *)navigation {
    klog(@"approval page loaded; firstResponder=%@",
         _root.window.firstResponder.className ?: @"(none)");
    [self stopApprovalBar];
    // NOW there is a form to sign in to.
    _codeStatus.stringValue = @"Sign in to connect this device.";
}

// A failed load has to SAY so. Without a delegate this state was
// indistinguishable from a slow one: a blank rectangle under a prompt, for as
// long as the person was willing to wait.
- (void)approvalLoadFailed:(NSError *)error {
    // A load replaced by another load reports NSURLErrorCancelled. That is
    // bookkeeping, not a failure, and reporting it would fire on an ordinary
    // in-page navigation.
    if ([error.domain isEqualToString:NSURLErrorDomain] && error.code == NSURLErrorCancelled) return;
    NSString *why = error.localizedDescription ?: @"unknown error";
    // Retry twice before giving up: the usual causes are transient (a route
    // being compiled, a network still coming up after a restart), and a wizard
    // that surrenders to one blip sends the person back to a Terminal. Bounded,
    // because retrying forever leaves the same blank rectangle with a spinner
    // over it -- the exact failure this method exists to end.
    if (_approvalAttempts < 3) {
        _codeStatus.stringValue =
            [NSString stringWithFormat:@"Couldn't load the sign-in page (%@). Retrying\u2026", why];
        NSURL *url = [NSURL URLWithString:_approvalURL ?: @""];
        if (!url) { [self stopApprovalBar]; return; }
        __weak typeof(self) weakSelf = self;
        dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(3 * NSEC_PER_SEC)),
                       dispatch_get_main_queue(), ^{
            typeof(self) s = weakSelf; if (!s) return;
            if (s->_approvalWeb.hidden) return;   // sign-in finished, or was abandoned
            [s beginApprovalLoad:url];
        });
        return;
    }
    [self stopApprovalBar];
    // No "Try again" button here: `_signInStarted` gates re-entry and is cleared
    // only when the login child exits, so the button would do nothing. The child
    // is still polling, so the honest statement is what is wrong -- not an
    // action that would not work.
    _codeStatus.stringValue =
        [NSString stringWithFormat:@"Couldn't load the sign-in page (%@). Check the connection to Atlas.", why];
}

- (void)webView:(WKWebView *)webView
        didFailNavigation:(WKNavigation *)navigation withError:(NSError *)error {
    [self approvalLoadFailed:error];
}

// A terminated web content process renders as a page that is visibly THERE and
// completely inert — the exact shape of "the fields are disabled". It is worth
// naming in the log, and worth recovering from, because the recovery is the same
// rebuild re-entry performs.
- (void)webViewWebContentProcessDidTerminate:(WKWebView *)webView {
    klog(@"web content process TERMINATED; rebuilding");
    NSString *url = _approvalURL;
    [self discardApprovalWebView];
    if (url.length > 0) [self showApprovalPage:url];
}

- (void)webView:(WKWebView *)webView
        didFailProvisionalNavigation:(WKNavigation *)navigation withError:(NSError *)error {
    [self approvalLoadFailed:error];
}

// discardApprovalWebView tears the page down completely: out of the layout, out
// of the view hierarchy, and off the delegate, so nothing retained points at it.
// showApprovalPage: builds a fresh one on the next call.
- (void)discardApprovalWebView {
    if (!_approvalWeb) return;
    NSMutableArray *rows = [_root.rows mutableCopy];
    [rows removeObject:_approvalWeb];
    _root.rows = rows;
    [_approvalWeb stopLoading];
    _approvalWeb.navigationDelegate = nil;
    [_approvalWeb removeFromSuperview];
    _approvalWeb = nil;
}

- (void)hideApprovalPage {
    [self stopApprovalBar];
    _approvalWeb.hidden = YES;
    for (NSView *v in _stowedWhileSigningIn) v.hidden = NO;
    [_stowedWhileSigningIn removeAllObjects];
    [_root setNeedsLayout:YES];
}

// prefillFromClipboard fills the field from the clipboard and submits it.
//
// The Atlas download page's Copy button is what puts the code there, so in the
// normal flow it is already on the clipboard at the moment this pane appears and
// nobody should have to retype it. `KeldLooksLikePairingCode` (unit-tested in
// KeldCodeTest.m) is what keeps this from firing a login attempt at arbitrary
// copied text; the value stays visible and editable either way.
- (BOOL)prefillFromClipboard {
    NSString *pasted = [[NSPasteboard generalPasteboard] stringForType:NSPasteboardTypeString];
    if (!KeldLooksLikePairingCode(pasted)) return NO;
    _codeField.stringValue = [pasted stringByTrimmingCharactersInSet:
                              [NSCharacterSet whitespaceAndNewlineCharacterSet]];
    [self connect:nil];
    return YES;
}

#pragma mark - Handoff

// ⚠️ THE SETUP CODE IS NEVER WRITTEN HERE. It was redeemed already; auth.json
// (written by `keld login`) is the one credential artifact, exactly as today.
- (void)writeHandoff {
    NSMutableArray<NSString *> *tools = [NSMutableArray array];
    for (NSButton *c in _toolChecks) {
        if (c.state == NSControlStateValueOn && c.identifier) [tools addObject:(NSString *)c.identifier];
    }
    NSDictionary *payload = @{
        @"version": [self bundleVersion] ?: @"",
        @"paired": @(_paired),
        @"api_url": _apiURL ?: @"",
        @"tools": tools,
        @"sidecar_staged": _stagedSidecar ?: @"",
    };
    NSString *dir = [NSHomeDirectory() stringByAppendingPathComponent:@".keld/state"];
    [[NSFileManager defaultManager] createDirectoryAtPath:dir
                             withIntermediateDirectories:YES
                                              attributes:@{NSFilePosixPermissions: @(0700)}
                                                   error:nil];
    NSData *d = [NSJSONSerialization dataWithJSONObject:payload options:0 error:nil];
    NSString *path = [dir stringByAppendingPathComponent:@"installer-handoff.json"];
    [d writeToFile:path atomically:YES];
    [[NSFileManager defaultManager] setAttributes:@{NSFilePosixPermissions: @(0600)}
                                     ofItemAtPath:path error:nil];
}

@end

@interface KeldSetupSection : InstallerSection
@end

@implementation KeldSetupSection {
    KeldSetupPane *_pane;
}
- (NSString *)title { return @"Set Up Keld"; }
// No nib: InstallerSection.h sanctions a subclass supplying its own pane when no
// NSMainNibFile is declared. A nib would need Xcode and buy nothing here.
- (InstallerPane *)firstPane {
    if (!_pane) _pane = [[KeldSetupPane alloc] initWithSection:self];
    return _pane;
}
@end
