// Does AppKit hand first-responder status to a HIDDEN NSTextField?
//
// This is the load-bearing assumption behind the Back-then-return bug: the pane's
// -initialKeyView returns _codeField, which is HIDDEN while the embedded approval
// page is showing. Installer.app makes initialKeyView the first responder on every
// pane entry. If a hidden field can take it, then on re-entry the keystrokes go to
// an invisible text field and the web page's inputs are dead — which is exactly
// what was reported.
#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>

int main(void) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];

        NSWindow *w = [[NSWindow alloc] initWithContentRect:NSMakeRect(0, 0, 400, 300)
                                                  styleMask:NSWindowStyleMaskTitled
                                                    backing:NSBackingStoreBuffered
                                                      defer:NO];
        NSView *root = w.contentView;

        NSTextField *field = [NSTextField textFieldWithString:@""];
        field.frame = NSMakeRect(10, 250, 200, 22);
        [root addSubview:field];

        WKWebView *web = [[WKWebView alloc] initWithFrame:NSMakeRect(10, 10, 380, 230)
                                            configuration:[WKWebViewConfiguration new]];
        [root addSubview:web];

        [w makeKeyAndOrderFront:nil];

        // 1. Visible field — the FIRST-entry case.
        BOOL okVisible = [w makeFirstResponder:field];
        NSResponder *r1 = w.firstResponder;
        printf("visible field: makeFirstResponder=%s firstResponder=%s\n",
               okVisible ? "YES" : "NO", r1.className.UTF8String);

        // 2. Now hide it and ask again — the RE-ENTRY case.
        [w makeFirstResponder:web];
        field.hidden = YES;
        BOOL okHidden = [w makeFirstResponder:field];
        NSResponder *r2 = w.firstResponder;
        printf("hidden field:  makeFirstResponder=%s firstResponder=%s\n",
               okHidden ? "YES" : "NO", r2.className.UTF8String);

        // 3. Does the web view still accept focus afterwards?
        BOOL backToWeb = [w makeFirstResponder:web];
        printf("web view after: makeFirstResponder=%s firstResponder=%s\n",
               backToWeb ? "YES" : "NO", w.firstResponder.className.UTF8String);

        printf("VERDICT: hidden field %s steal focus\n", okHidden ? "CAN" : "CANNOT");
    }
    return 0;
}
