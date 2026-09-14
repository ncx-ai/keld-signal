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
#import <InstallerPlugins/InstallerPlugins.h>

@interface KeldSetupPane : InstallerPane
@end

@implementation KeldSetupPane {
    NSView *_view;
    NSTextField *_codeField;
    NSButton *_connectButton;
    NSTextField *_codeStatus;
    NSProgressIndicator *_engineBar;
    NSTextField *_engineStatus;
    NSStackView *_toolsStack;
    NSMutableArray<NSButton *> *_toolChecks;
    NSButton *_laterButton;
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
    return l;
}

- (NSView *)contentView {
    if (_view) return _view;
    _toolChecks = [NSMutableArray array];

    NSStackView *root = [[NSStackView alloc] initWithFrame:NSMakeRect(0, 0, 620, 340)];
    root.orientation = NSUserInterfaceLayoutOrientationVertical;
    root.alignment = NSLayoutAttributeLeading;
    root.spacing = 14;

    [root addArrangedSubview:[self labelWithText:@"Your setup code" bold:YES]];
    _codeField = [[NSTextField alloc] initWithFrame:NSMakeRect(0, 0, 320, 24)];
    _codeField.placeholderString = @"atlas.keld.co/ABCD-EFGH";
    // ⚠️ A frame set at init time is DISCARDED once a view is added to an
    // NSStackView's arranged subviews — the stack view lays it out with Auto
    // Layout from its intrinsic content size instead, which for a plain text
    // field is small and arbitrary. An explicit width constraint is what
    // actually sizes it; nothing else here can catch that but a real Mac.
    _codeField.translatesAutoresizingMaskIntoConstraints = NO;
    [_codeField.widthAnchor constraintEqualToConstant:320].active = YES;
    _connectButton = [NSButton buttonWithTitle:@"Connect" target:self action:@selector(connect:)];
    NSStackView *codeRow = [NSStackView stackViewWithViews:@[_codeField, _connectButton]];
    codeRow.orientation = NSUserInterfaceLayoutOrientationHorizontal;
    [root addArrangedSubview:codeRow];
    _codeStatus = [self labelWithText:@"Paste the code from your Keld download page." bold:NO];
    _codeStatus.textColor = [NSColor secondaryLabelColor];
    [root addArrangedSubview:_codeStatus];

    [root addArrangedSubview:[self labelWithText:@"Analysis engine" bold:YES]];
    _engineBar = [[NSProgressIndicator alloc] initWithFrame:NSMakeRect(0, 0, 420, 16)];
    _engineBar.style = NSProgressIndicatorStyleBar;
    _engineBar.indeterminate = YES;
    _engineBar.minValue = 0;
    _engineBar.maxValue = 100;
    // Same NSStackView frame-discarding issue as _codeField above.
    _engineBar.translatesAutoresizingMaskIntoConstraints = NO;
    [_engineBar.widthAnchor constraintEqualToConstant:420].active = YES;
    [_engineBar startAnimation:nil];
    [root addArrangedSubview:_engineBar];
    _engineStatus = [self labelWithText:@"Preparing…" bold:NO];
    _engineStatus.textColor = [NSColor secondaryLabelColor];
    [root addArrangedSubview:_engineStatus];

    [root addArrangedSubview:[self labelWithText:@"Your AI tools" bold:YES]];
    _toolsStack = [[NSStackView alloc] initWithFrame:NSZeroRect];
    _toolsStack.orientation = NSUserInterfaceLayoutOrientationVertical;
    _toolsStack.alignment = NSLayoutAttributeLeading;
    _toolsStack.spacing = 4;
    [root addArrangedSubview:_toolsStack];

    _laterButton = [NSButton buttonWithTitle:@"Set up later" target:self action:@selector(setUpLater:)];
    _laterButton.bezelStyle = NSBezelStyleInline;
    [root addArrangedSubview:_laterButton];

    _view = root;
    return _view;
}

// The code field is the one control a person actually needs to type into the
// moment this pane appears; without an explicit initialKeyView, InstallerPane
// falls back to whatever the view hierarchy happens to make first responder,
// which is not guaranteed to be it.
- (NSView *)initialKeyView {
    return _codeField;
}

#pragma mark - Pane lifecycle

- (void)didEnterPane:(InstallerSectionDirection)dir {
    (void)[self contentView];
    // ⚠️ Continue is disabled until the code is accepted (or "Set up later" is
    // clicked). Catching a bad code HERE is the point: there is no pane after
    // the install to catch it in.
    self.nextEnabled = _paired;
    // Guarded: the pane can be re-entered (Back, then Continue again), and
    // without this a second entry would start a second concurrent ~190 MB
    // download rather than reusing the first.
    if (!_sidecarDownloadStarted) {
        _sidecarDownloadStarted = YES;
        [self startSidecarDownload];
    }
}

// shouldExitPane writes the handoff postinstall consumes. Returning YES always:
// by this point either pairing succeeded or the person chose to finish later,
// and both are states postinstall knows how to complete.
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
    [self runKeld:@[@"login", @"--code", code, @"--json"]
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
    for (NSView *v in [_toolsStack.arrangedSubviews copy]) [_toolsStack removeArrangedSubview:v], [v removeFromSuperview];
    [_toolChecks removeAllObjects];
    if (tools.count == 0) {
        [_toolsStack addArrangedSubview:[self labelWithText:@"No supported AI tools found on this Mac." bold:NO]];
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
        [_toolsStack addArrangedSubview:check];
        [_toolChecks addObject:check];
    }
    [_toolsStack addArrangedSubview:[self labelWithText:
        @"Restart these apps after setup — they read their settings once, at startup." bold:NO]];
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

- (void)setUpLater:(id)sender {
    _codeStatus.stringValue = @"Skipping for now — nothing is collected until you add a code.";
    self.nextEnabled = YES;
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
