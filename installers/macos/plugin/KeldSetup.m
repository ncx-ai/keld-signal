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
// line on the MAIN queue. `done` fires once, after exit.
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

    NSMutableData *buffer = [NSMutableData data];
    out.fileHandleForReading.readabilityHandler = ^(NSFileHandle *fh) {
        [buffer appendData:fh.availableData];
        while (YES) {
            NSRange nl = [buffer rangeOfData:[@"\n" dataUsingEncoding:NSUTF8StringEncoding]
                                     options:0 range:NSMakeRange(0, buffer.length)];
            if (nl.location == NSNotFound) break;
            NSData *line = [buffer subdataWithRange:NSMakeRange(0, nl.location)];
            [buffer replaceBytesInRange:NSMakeRange(0, nl.location + 1) withBytes:NULL length:0];
            NSDictionary *obj = [NSJSONSerialization JSONObjectWithData:line options:0 error:nil];
            if ([obj isKindOfClass:[NSDictionary class]]) {
                dispatch_async(dispatch_get_main_queue(), ^{ onEvent(obj); });
            }
        }
    };
    task.terminationHandler = ^(NSTask *t) {
        t.standardOutput = nil;
        out.fileHandleForReading.readabilityHandler = nil;
        dispatch_async(dispatch_get_main_queue(), ^{
            [self->_activeTasks removeObject:handle];
            done(t.terminationStatus);
        });
    };
    NSError *err = nil;
    if (![task launchAndReturnError:&err]) {
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
        [args addObjectsFromArray:@[@"--tag", [@"v" stringByAppendingString:version]]];
    }
    __weak typeof(self) weakSelf = self;
    __block NSString *failure = nil;
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
        }
    } done:^(int status) {
        typeof(self) s = weakSelf; if (!s) return;
        [s->_engineBar stopAnimation:nil];
        s->_engineBar.indeterminate = NO;
        if (s->_stagedSidecar.length) {
            s->_engineBar.doubleValue = 100;
            s->_engineStatus.stringValue = @"Ready. Nothing multi-gigabyte is downloaded, now or later.";
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
