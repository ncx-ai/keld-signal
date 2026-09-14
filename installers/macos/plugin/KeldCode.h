// Shape test for a pairing code found on the clipboard.
//
// Deliberately its own file with NO InstallerPlugins dependency, so it can be
// compiled and unit-tested on its own (KeldCodeTest.m, run by
// `make pkg-plugin-check`). The pane itself cannot be executed by any automated
// check, so anything decidable in isolation belongs out here.
#import <Foundation/Foundation.h>

/// Reports whether `s` looks like a setup code the wizard may submit on the
/// person's behalf: optionally host-qualified ("atlas.keld.co/ABCD-EFGH" or
/// "https://atlas.keld.co/ABCD-EFGH"), with a code of exactly two alphanumeric
/// groups separated by one dash.
///
/// It is a SHAPE test, not a validity test — Atlas decides whether a
/// well-shaped code is real. Its only job is to keep the pane from firing a
/// login attempt at arbitrary clipboard contents.
BOOL KeldLooksLikePairingCode(NSString *s);
