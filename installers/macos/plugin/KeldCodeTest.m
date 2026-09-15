// Unit tests for the clipboard pairing-code predicate.
//
// ⚠️ THIS PREDICATE DECIDES WHETHER THE WIZARD AUTO-SUBMITS WHATEVER IS ON A
// PERSON'S CLIPBOARD. Too loose and the pane fires a login attempt at arbitrary
// copied text (and shows them an error about it); too tight and the feature
// never triggers and they are back to typing. Both failures are silent in the
// sense that nothing crashes — only a human clicking through would notice — so
// the shape rules are pinned here rather than trusted.
//
// Run by `make pkg-plugin-check`.
#import <Foundation/Foundation.h>
#import "KeldCode.h"

static int failures = 0;

static void expect(BOOL got, BOOL want, const char *input) {
    if (got != want) {
        failures++;
        printf("FAIL: %-46s got %s, want %s\n", input, got ? "accept" : "reject", want ? "accept" : "reject");
    }
}

static void accepts(NSString *s) { expect(KeldLooksLikePairingCode(s), YES, s.UTF8String); }
static void rejects(NSString *s) { expect(KeldLooksLikePairingCode(s), NO, s.UTF8String); }

int main(void) {
    @autoreleasepool {
        // The shape Atlas mints, bare and host-qualified — the two things a
        // person can actually have on their clipboard after clicking Copy.
        accepts(@"ABCD-EFGH");
        accepts(@"atlas.keld.co/ABCD-EFGH");
        accepts(@"atlas-dev.keld.co/WXYZ-1234");
        accepts(@"https://atlas.keld.co/ABCD-EFGH");
        // Lowercase is normalised Go-side by auth.ParsePairingCode, so accepting
        // it here costs nothing and helps anyone who retyped it from an email.
        accepts(@"abcd-efgh");
        // Surrounding whitespace survives a sloppy copy.
        accepts(@"  ABCD-EFGH\n");

        // Ordinary clipboard contents must never trigger a login attempt.
        rejects(@"");
        rejects(@"   ");
        rejects(@"here is your setup code");
        rejects(@"https://keld.co/download");
        rejects(@"keld signal setup --yes");
        rejects(@"ABCDEFGH");                       // no separator: not the minted shape
        rejects(@"ABCD-EFGH\nsecond line");          // multi-line paste
        rejects(@"-EFGH");                           // empty first group
        rejects(@"ABCD-");                           // empty second group
        rejects(@"/ABCD-EFGH/extra");                // path-like, not a code
        rejects([@"" stringByPaddingToLength:400 withString:@"A-B" startingAtIndex:0]);

        if (failures == 0) printf("KeldCodeTest: OK\n");
        return failures == 0 ? 0 : 1;
    }
}
