// Does a WKWebView still accept typed input after its container is removed from
// the window and re-added?
//
// That is what Installer.app does when you press Back and then Continue: the
// pane's view is swapped out of the window and later swapped back. Two fixes
// aimed at focus did not help, so this isolates the WebKit half from the pane
// entirely — a plain window, a plain container, one text input, and a REAL key
// event posted through CGEvent.
//
// Reports, before and after the re-parent: the window's first responder, whether
// makeFirstResponder: succeeds, whether JS still runs (a suspended or dead web
// content process is the other candidate), and whether a posted keystroke
// actually lands in the input.
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>

static NSString *const kPage =
  @"<html><body style='font:14px -apple-system'>"
  @"<input id='x' style='width:200px'><script>document.getElementById('x').focus()</script>"
  @"</body></html>";

static NSWindow *gWin; static WKWebView *gWeb; static NSView *gBox;

static void js(WKWebView *web, NSString *src, void (^done)(NSString *)) {
    [web evaluateJavaScript:src completionHandler:^(id r, NSError *e) {
        done(e ? [NSString stringWithFormat:@"<error: %@>", e.localizedDescription]
               : [NSString stringWithFormat:@"%@", r]);
    }];
}

// Post a real 'a' keystroke to this process, the way a person's keyboard would.
static void typeA(void) {
    CGEventRef down = CGEventCreateKeyboardEvent(NULL, (CGKeyCode)0, true);
    CGEventRef up   = CGEventCreateKeyboardEvent(NULL, (CGKeyCode)0, false);
    CGEventPostToPid(getpid(), down);
    CGEventPostToPid(getpid(), up);
    CFRelease(down); CFRelease(up);
}

static void step(int n, void (^blk)(void)) {
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(n * NSEC_PER_SEC)),
                   dispatch_get_main_queue(), blk);
}

int main(void) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];

        gWin = [[NSWindow alloc] initWithContentRect:NSMakeRect(200, 200, 460, 300)
                                           styleMask:NSWindowStyleMaskTitled
                                             backing:NSBackingStoreBuffered defer:NO];
        gBox = [[NSView alloc] initWithFrame:gWin.contentView.bounds];
        gWeb = [[WKWebView alloc] initWithFrame:gBox.bounds configuration:[WKWebViewConfiguration new]];
        [gBox addSubview:gWeb];
        [gWin.contentView addSubview:gBox];
        [gWeb loadHTMLString:kPage baseURL:nil];
        [gWin makeKeyAndOrderFront:nil];
        [NSApp activateIgnoringOtherApps:YES];

        // 1. Baseline: type before any re-parenting.
        step(2, ^{
            [gWin makeFirstResponder:gWeb];
            typeA();
            step(1, ^{
                js(gWeb, @"document.getElementById('x').value", ^(NSString *v) {
                    printf("BEFORE reparent: value=%s firstResponder=%s\n",
                           v.UTF8String, gWin.firstResponder.className.UTF8String);

                    // 2. Simulate Back: the pane's view leaves the window.
                    [gBox removeFromSuperview];
                    printf("  (removed from window; web.window=%s)\n",
                           gWeb.window ? "set" : "nil");

                    // 3. Simulate Continue: it comes back.
                    step(1, ^{
                        [gWin.contentView addSubview:gBox];
                        BOOL ok = [gWin makeFirstResponder:gWeb];
                        printf("  (re-added; makeFirstResponder=%s firstResponder=%s)\n",
                               ok ? "YES" : "NO", gWin.firstResponder.className.UTF8String);

                        // Is the web content process still alive and running JS?
                        js(gWeb, @"document.readyState", ^(NSString *rs) {
                            printf("AFTER reparent: readyState=%s\n", rs.UTF8String);
                            js(gWeb, @"document.getElementById('x').focus(), document.activeElement.id", ^(NSString *ae) {
                                printf("AFTER reparent: activeElement=%s\n", ae.UTF8String);
                                typeA();
                                step(1, ^{
                                    js(gWeb, @"document.getElementById('x').value", ^(NSString *v2) {
                                        printf("AFTER reparent: value=%s\n", v2.UTF8String);
                                        printf("VERDICT: typing after re-parent %s\n",
                                               [v2 containsString:@"a"] ? "WORKS" : "IS DEAD");
                                        exit(0);
                                    });
                                });
                            });
                        });
                    });
                });
            });
        });

        [NSApp run];
    }
    return 0;
}
