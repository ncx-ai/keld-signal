#import "KeldCode.h"

// A clipboard string is a candidate only if it is a single whitespace-free
// token whose last "/"-separated segment is two alphanumeric groups joined by
// one dash. That last rule is what separates a real code from ordinary copied
// text and from a plain download URL, whose final segment ("download") carries
// no separator.
BOOL KeldLooksLikePairingCode(NSString *s) {
    if (s == nil) return NO;
    NSCharacterSet *ws = [NSCharacterSet whitespaceAndNewlineCharacterSet];
    NSString *t = [s stringByTrimmingCharactersInSet:ws];
    if (t.length == 0 || t.length > 128) return NO;
    // Any surviving whitespace means this is prose or a multi-line paste, not a
    // code someone clicked Copy for.
    if ([t rangeOfCharacterFromSet:ws].location != NSNotFound) return NO;

    NSString *code = t;
    NSRange slash = [t rangeOfString:@"/" options:NSBackwardsSearch];
    if (slash.location != NSNotFound) code = [t substringFromIndex:NSMaxRange(slash)];

    NSArray<NSString *> *parts = [code componentsSeparatedByString:@"-"];
    if (parts.count != 2) return NO;
    NSCharacterSet *notAlnum = [[NSCharacterSet alphanumericCharacterSet] invertedSet];
    for (NSString *part in parts) {
        if (part.length == 0) return NO;
        if ([part rangeOfCharacterFromSet:notAlnum].location != NSNotFound) return NO;
    }
    return YES;
}
