package service

import (
	"encoding/xml"
	"fmt"
	"strings"
	"unicode/utf16"
)

// Registering the logon task WITHOUT ADMINISTRATOR RIGHTS.
//
// ⚠️ `schtasks /SC ONLOGON` CANNOT BE USED, and the reason is not obvious from
// its name. It is shorthand for a logon trigger with NO UserId — i.e. "when ANY
// user logs on" — which is a machine-wide trigger and therefore requires
// elevation. The installer is PrivilegesRequired=lowest and never has it, so the
// call failed with `ERROR: Access is denied.` on every Windows machine, the task
// was never created, and the daemon only ever ran as the installer's own child
// process, dying when the installer exited. Measured on a real machine: the
// failure reproduces for a user who IS a local Administrator, because an
// unelevated process holds no admin rights under UAC — so no user, of any kind,
// was getting a Windows autostart.
//
// A logon trigger SCOPED TO ONE USER is not machine-wide and registers fine
// unelevated. schtasks has no flag for that, but it accepts a full task document
// via `/XML`, which does. Verified on the affected machine: `/SC ONLOGON` denied,
// this same document accepted, unelevated, same session.
//
// MultipleInstancesPolicy=IgnoreNew is Task Scheduler's own single-instance
// guard. The daemon has none of its own — its ingress binds 127.0.0.1:0, an
// EPHEMERAL port, so a second instance takes a different port, overwrites
// agent.json and runs alongside the first (the telemetry proxy's fixed 14318
// would fail for it, and that is explicitly non-fatal). This keeps the scheduler
// from being the thing that causes that.
//
// ExecutionTimeLimit=PT0S means no limit: the default is 72 hours, which would
// otherwise terminate a healthy long-running daemon.
//
// ⚠️ `--hide-console` IS PART OF THE CONTRACT, NOT A CONVENIENCE. Task Scheduler
// gives a console binary a console, and without this the daemon opens a black
// window of logs at every logon. The daemon detaches ONLY when told to, so the
// flag has to be here — the previous version inferred it from the console's
// process count and got it wrong on a real machine. A human running
// `keld-agent run` passes no flag and keeps their terminal.
const taskXMLTemplate = `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <Triggers><LogonTrigger><Enabled>true</Enabled><UserId>%[1]s</UserId></LogonTrigger></Triggers>
  <Principals><Principal id="Author"><UserId>%[1]s</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
  <Settings><MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy><Enabled>true</Enabled><ExecutionTimeLimit>PT0S</ExecutionTimeLimit><DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries><StopIfGoingOnBatteries>false</StopIfGoingOnBatteries></Settings>
  <Actions Context="Author"><Exec><Command>%[2]s</Command><Arguments>run --hide-console</Arguments></Exec></Actions>
</Task>
`

// taskXMLFor builds the registration document for user running exe.
//
// Both values are XML-ESCAPED rather than interpolated raw. A Windows account
// name may contain `&`, and an install directory is user-controlled — an
// unescaped `&` makes the document malformed, and schtasks then rejects the whole
// registration with a parse error that names nothing useful.
func taskXMLFor(user, exe string) string {
	return fmt.Sprintf(taskXMLTemplate, xmlEscape(user), xmlEscape(exe))
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// utf16LEWithBOM encodes s the way schtasks expects the file to be.
//
// The document declares encoding="UTF-16", so the BYTES have to be UTF-16 —
// writing UTF-8 under that declaration is a document that lies about itself and
// schtasks rejects it. Encoded by hand rather than via golang.org/x/text: this is
// the whole of the need, `unicode/utf16` is stdlib, and the CLI ships with no
// runtime dependencies.
func utf16LEWithBOM(s string) []byte {
	units := utf16.Encode([]rune(s))
	out := make([]byte, 0, 2+2*len(units))
	out = append(out, 0xFF, 0xFE) // little-endian BOM
	for _, u := range units {
		out = append(out, byte(u), byte(u>>8))
	}
	return out
}
