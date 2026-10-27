package s3400

import (
	"strings"
	"testing"
)

func TestHostnameCmdsIsTheBareConfigLine(t *testing.T) {
	// configBlock sends "config" and the trailing "exit": a builder that
	// repeated them would leave the session one level off, and the next gesture
	// would run its first command in a context it does not expect.
	var cmds = hostnameCmds("sw-lab-01")
	if len(cmds) != 1 || cmds[0] != "hostname sw-lab-01" {
		t.Fatalf("cmds=%v, want [\"hostname sw-lab-01\"]", cmds)
	}
	var c string
	for _, c = range cmds {
		if c == "config" || c == "exit" {
			t.Errorf("builder must not carry mode changes, got %q", c)
		}
	}
}

func TestHostnameLineMatchesTheProvisioningForm(t *testing.T) {
	// MgmtPushCmds is the ONE hostname line this project has ever put on an
	// S3400 (pushed 2026-08-03 as part of the provisioning sequence). If the two
	// forms ever diverge, one of them is wrong and only the field would tell us
	// which.
	var pushed, _ = MgmtPushCmds(MgmtOpts{VlanID: 99, Hostname: "sw-lab-01"})
	var want = hostnameCmds("sw-lab-01")[0]
	var line string
	var found = false
	for _, line = range pushed {
		if line == want {
			found = true
		}
	}
	if !found {
		t.Errorf("provisioning emits no %q; MgmtPushCmds=%v", want, pushed)
	}
}

func TestHostnameWrittenFormIsReadBackByTheConfigParser(t *testing.T) {
	// FIXTURE CONSTRUCTED from the builder's own output: no capture in this
	// package carries a hostname line (cf. hostname.go), so this test pins the
	// AGREEMENT between the write form and the parser, not the device's
	// rendering — which stays to be captured live.
	var facts = ParseRunningConfig("!\n" + hostnameCmds("sw-lab-01")[0] + "\n!\n")
	if facts.Hostname != "sw-lab-01" {
		t.Errorf("Hostname=%q: the command this connector sends must be the line "+
			"its own parser reads back", facts.Hostname)
	}
}

func TestHostnameMismatchNamesTheThreeFaults(t *testing.T) {
	var absent = hostnameMismatch("sw-lab-01", "")
	if !strings.Contains(absent, "no hostname line") {
		t.Errorf("an absent hostname must be reported as such, got %q", absent)
	}
	var cut = hostnameMismatch("sw-lab-01-very-long", "sw-lab-01-very")
	if !strings.Contains(cut, "14 bytes") {
		t.Errorf("a truncation must name the observed ceiling, got %q", cut)
	}
	var other = hostnameMismatch("sw-lab-01", "Switch")
	if !strings.Contains(other, `"Switch"`) {
		t.Errorf("a divergence must show what the device carries, got %q", other)
	}
}

// --- the SERIAL-PROVISIONING half, which used to follow the opposite doctrine ---
//
// [MgmtPushCmds] built its `hostname` line from a private `sanitizeHostname`
// until 2026-08-09: it KEPT `_` (which driver.ValidHostname forbids, so the name
// it produced could never be the name hostnameCmds pushes), DROPPED every other
// character it did not recognise, never trimmed, and fell back to the literal
// `switch`. Both paths now go through driver.HostnameFromDeviceName.
//
// The builder has no Go caller — it is the written specification of
// web/src/pages/add/fsS3400SoftReset.ts — so these tests pin a SPECIFICATION,
// not a live outage. They matter for the day someone wires a name through.

func TestMgmtPushRefusesTheSystemNameRatherThanManglingIt(t *testing.T) {
	var cmds, err = MgmtPushCmds(MgmtOpts{
		VlanID: 99, IP: "192.0.2.241", Mask: "255.255.0.0",
		Username: "mcadmin", Password: "s3cret",
		Hostname: "Switch étage 1",
	})
	if err == nil {
		t.Fatal("an accented meshcraft name produced no refusal")
	}
	if !strings.Contains(err.Error(), "é") {
		t.Errorf("the refusal does not name the offending character: %s", err)
	}
	var joined = strings.Join(cmds, "\n")
	if strings.Contains(joined, "hostname ") {
		t.Errorf("a refused name still produced a hostname line: %s", joined)
	}
	if strings.Contains(joined, "Switch-tage-1") {
		t.Error("the dropped-character doctrine is back")
	}
	if strings.Contains(joined, "hostname switch") {
		t.Error("the `switch` fallback is back: a fleet of identically-named devices")
	}
	// THE REST OF THE PUSH SURVIVES THE REFUSAL: a switch left factory-reset and
	// unreachable over a display name is a worse answer than a reachable switch
	// still carrying its factory name.
	if !strings.Contains(joined, "ip address 192.0.2.241 255.255.0.0") {
		t.Errorf("the management address is missing from a push whose only fault "+
			"was its system name: %s", joined)
	}
	if !strings.Contains(joined, "username mcadmin") {
		t.Error("the credentials are missing from a push that only refused a name")
	}
}

func TestMgmtPushNormalizesWithTheSharedRule(t *testing.T) {
	var cmds, err = MgmtPushCmds(MgmtOpts{
		VlanID: 99, IP: "192.0.2.241", Mask: "255.255.0.0",
		Hostname: " sw_lab 01 ",
	})
	if err != nil {
		t.Fatalf("a normalizable name was refused: %s", err)
	}
	var joined = strings.Join(cmds, "\n")
	if !strings.Contains(joined, hostnameCmds("sw-lab-01")[0]) {
		t.Errorf("provisioning and reconciliation disagree on the same name: %s", joined)
	}
	if strings.Contains(joined, "sw_lab") {
		t.Error("the underscore survived: it is the one character " +
			"driver.ValidHostname forbids, so the healer could never emit this name")
	}
}
