package s3900

import (
	"strings"
	"testing"
)

func TestHostnameCmdsIsTheBareConfigLine(t *testing.T) {
	// runConfigBlock supplies "conf t" and "end". A builder repeating them
	// would leave the session in a sub-mode the next gesture does not know about
	// — the exact fault runConfigBlock's comment guards against.
	var cmds = hostnameCmds("sw-lab-01")
	if len(cmds) != 1 || cmds[0] != "hostname sw-lab-01" {
		t.Fatalf("cmds=%v, want [\"hostname sw-lab-01\"]", cmds)
	}
	var c string
	for _, c = range cmds {
		if strings.HasPrefix(c, "conf") || c == "end" {
			t.Errorf("builder must not carry mode changes, got %q", c)
		}
	}
}

func TestHostnameReadBackFromTheRealCapture(t *testing.T) {
	// labRun is the 2026-08-01/02 serial capture (OpCode 1.7.0) and it carries a
	// NON-default "hostname test-fs-s3900". This is the one piece of hardware
	// evidence behind this write: the line the parser reads is the line the
	// builder produces.
	var rc = parseRunningConfig(labRun, "")
	if rc.Hostname != "test-fs-s3900" {
		t.Fatalf("Hostname=%q: the capture that justifies this write form moved", rc.Hostname)
	}
	var round = parseRunningConfig("!\n"+hostnameCmds("test-fs-s3900")[0]+"\n!\n", "")
	if round.Hostname != rc.Hostname {
		t.Errorf("the builder emits %q but the capture reads %q — the write form and "+
			"the read form must agree, or verification fails on a device that obeyed",
			round.Hostname, rc.Hostname)
	}
}

func TestHostnameMismatchNamesTheThreeFaults(t *testing.T) {
	var absent = hostnameMismatch("sw-lab-01", "")
	if !strings.Contains(absent, "no hostname line") {
		t.Errorf("an absent hostname must be reported as such, got %q", absent)
	}
	// The ceiling is learned from the device, not carried as a constant: on a
	// platform whose forms move between firmware builds, a stale constant is
	// worse than no constant.
	var cut = hostnameMismatch("sw-lab-01-very-long", "sw-lab-01-very")
	if !strings.Contains(cut, "14 bytes") {
		t.Errorf("a truncation must name the observed ceiling, got %q", cut)
	}
	var other = hostnameMismatch("sw-lab-01", "Switch-6")
	if !strings.Contains(other, `"Switch-6"`) {
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
// web/src/pages/add/fsS3900SoftReset.ts — so these tests pin a SPECIFICATION,
// not a live outage. They matter for the day someone wires a name through.

func TestMgmtPushRefusesTheSystemNameRatherThanManglingIt(t *testing.T) {
	var cmds, err = MgmtPushCmds(MgmtOpts{
		VlanID: 99, IP: "192.0.2.5", PrefixLen: 16,
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
	if !strings.Contains(joined, "ip add 192.0.2.5/16") {
		t.Errorf("the management address is missing from a push whose only fault "+
			"was its system name: %s", joined)
	}
	if !strings.Contains(joined, "username mcadmin") {
		t.Error("the credentials are missing from a push that only refused a name")
	}
}

func TestMgmtPushNormalizesWithTheSharedRule(t *testing.T) {
	var cmds, err = MgmtPushCmds(MgmtOpts{
		VlanID: 99, IP: "192.0.2.5", PrefixLen: 16,
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
