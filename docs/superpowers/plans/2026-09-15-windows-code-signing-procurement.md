# Windows code signing — procurement task

**Owner:** whoever manages the Microsoft/Azure account and can complete company
verification. **Not an engineering task** — engineering cannot start until a
certificate exists.
**Raised:** 2026-09-15, from `docs/superpowers/specs/2026-09-15-windows-wizard-native-onboarding-design.md` §11.

## 1. What is broken

Windows 11 ships with **Smart App Control** on by default on clean installs. It
refuses to run software that is not signed by a recognised certificate. **None of
Keld's Windows binaries are signed**, so on those machines Keld does not run.

Measured on a real Windows 11 machine, 2026-09-15 — this is the *released*
product, not a development build:

```
C:\Users\<user>\AppData\Local\Programs\keld\keld.exe
Status : NotSigned
run    : BLOCKED — An Application Control policy has blocked this file
```

⚠️ **THIS IS NOT ONLY ABOUT THE NEW INSTALLER.** The command-line tool, the
background agent and the existing console setup script all run unsigned
binaries. Any Windows 11 user with Smart App Control on is affected.

⚠️ **AND IT IS NOT A STEADY FAILURE, WHICH IS WHY IT WENT UNNOTICED.** Unsigned
software is allowed or refused based on a *reputation guess* from Microsoft's
cloud, and that guess changes. On the test machine the same file ran at 2:05pm
and was blocked at 2:44pm with nothing changed in between. Microsoft's own
developer documentation says reputation "might not be available for newly
published binaries and can change over time", and that signing is the only
reliable answer.

## 2. What we need

A **code signing certificate**, plus a way to use it from our automated build
(GitHub Actions). Both halves matter — see §4.

## 3. The cheap option we CANNOT use — do not spend time on it

**Azure Trusted Signing** (now "Azure Artifact Signing") is Microsoft's own
service at **$9.99/month**, needs no hardware, and is built for automated builds.
It would be the obvious answer.

**We are not eligible.** Since April 2025 it requires the organisation to have
**at least three years of verifiable operating history**. Keld is a few months
old. The individual-developer path that previously existed was closed at the same
time, so there is no way in through a personal account either.

Microsoft has said it intends to extend the service to younger companies. **Add a
reminder to re-check this at our three-year mark**, or sooner if Microsoft
announces a change — it would cut this cost by roughly 95%.

## 4. What to buy instead

**An OV ("Organisation Validation") code signing certificate, from a vendor that
offers CLOUD SIGNING.**

⚠️ **THE CLOUD PART IS NOT OPTIONAL AND IS EASY TO BUY WRONG.** Since June 2023
the certificate's private key must live on certified hardware. Most vendors
default to shipping a **physical USB token** in the post. A USB token is useless
to us: our software is built on GitHub's servers, and nobody can plug a USB stick
into those. **If the order page offers "hardware token" or "cloud/HSM signing",
choose cloud.** Ordering a token means waiting for the post and then buying the
service again.

Vendors offering this (approximate, confirm at purchase):

| Vendor | Cloud service | Approx. cost |
|---|---|---|
| DigiCert | KeyLocker | higher, enterprise-oriented |
| SSL.com | eSigner | ~$215–290/yr OV |
| Sectigo | cloud signing | ~$215–290/yr OV |

**OV is sufficient.** There is a more expensive "EV" tier (~$290–500/yr). Smart
App Control requires a signature from a recognised authority — it does not
require EV. EV mainly buys faster reputation in a *different* Windows warning
(SmartScreen, the "unrecognised app" dialog on download). Worth considering later
if download warnings become a complaint; not needed to fix this.

**Timeline once documents are submitted:** OV typically **1–3 business days**.
SSL.com offers expedited EV at roughly 24 hours for about +$500 if this becomes
urgent.

**Renewal:** certificates issued from 1 March 2026 are capped at **460 days**, so
this becomes a yearly task. Put the expiry in a calendar — an expired certificate
reproduces the outage above.

## 5. What validation will ask for — gather this first

This is where the time goes. The certificate authority verifies that Keld is a
real company and that the person ordering may act for it. Expect to supply:

- **Exact registered legal entity name** and registered address — must match
  official records character for character.
- **Business registration / incorporation number**, and probably the
  incorporation documents themselves.
- **A D-U-N-S number.** Free from Dun & Bradstreet but **can take several
  business days to issue** — ⚠️ if we do not have one, request it TODAY, before
  ordering anything else. It is the most common cause of delay.
- **A phone number for the company listed in an independent public directory**,
  and someone available to answer a verification call.
- **Control of keld.co** (an email at the domain, or a DNS record).

⚠️ **BEING A YOUNG COMPANY IS THE MAIN RISK TO THE TIMELINE.** There is no
three-year rule for a commercial certificate the way there is for Microsoft's
service, but authorities lean on third-party records that a months-old company
may not appear in yet. If they cannot verify us from public data they will ask
for more — commonly a bank letter, a utility bill in the company name, or a
signed letter from a lawyer or accountant confirming the company exists.
**Ask the vendor up front what they accept for a recently-incorporated company**
rather than discovering it three days in.

## 6. What engineering needs back from you

Once the certificate is issued, we need **credentials for the cloud signing
service** so the build can sign automatically. The exact names differ per vendor,
but expect something like an API key or client ID and secret, the name of the
certificate to sign with, and possibly a one-time-password seed.

Hand those over however we normally pass secrets — **they must not go in a
ticket, a chat message or email**. They go into the repository's GitHub Actions
secrets. Anyone holding them can sign software as Keld.

Engineering then does the rest, with no further input needed:

- sign our four Windows programs and the installer;
- sign the ~45 third-party components inside the installer that arrive unsigned
  (measured: 118 binaries in the payload, most already signed by their own
  vendors);
- add the signing step to the automated build and remove the obsolete
  placeholder that assumed the pre-2023 approach.

## 7. Scope

**In:** Windows only.
**Out:** macOS, which already has an Apple Developer ID certificate and is
unaffected. This is a separate purchase from a different authority; the Apple one
cannot be used for Windows.

## 8. Decision needed

1. Confirm the exact legal entity to certify, and that we hold a D-U-N-S number
   (or start that today).
2. Pick a vendor and buy an **OV certificate with cloud signing** — not a USB
   token.
3. Ask the vendor what documentation they require for a company incorporated this
   year, before ordering.
4. Set a calendar reminder for renewal, and one to re-check Azure Trusted Signing
   eligibility.

Until this lands, Windows 11 machines with Smart App Control on cannot run Keld.
Developers can work around it locally by putting Smart App Control into
Evaluation mode; that is not something customers can be asked to do.
