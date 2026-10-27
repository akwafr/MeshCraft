package eos

import (
	"strings"
	"testing"
)

// The command builder and the mismatch explanation, tested on states — the house
// rule for this package (cf. apply_test.go: apply logic is proven on states, not
// on hardware).
//
// This file was ABSENT until 2026-08-08, alone among the connectors that carry a
// system-name write: cisco-ios, hpe-procurve, fs-s3400, fs-s3900, instanton,
// mikrotik-swos, mikrotik-routeros and ubiquiti-edgeswitch all had one. The gap
// mattered because arista-eos is declared writable in
// api.hostnameWritableConnectors on a DEDUCED form, so the reconciliation loop
// will be what sends it first, unattended, on a ZTP fleet where every switch
// still answering to `localhost` reads `drift` at once.

func TestHostnameCmdsIsOneUnquotedLine(t *testing.T) {
	// One line, and no quoting: driver.ValidHostname has already ruled out
	// everything that would need quoting. A form that quoted anyway would put the
	// quotes INTO the name on this CLI, and the re-read would then disagree with
	// the inventory for ever — one push and one event per cooldown, on a switch
	// that did exactly what it was told.
	var cmds = hostnameCmds("sw-lab-01")
	if len(cmds) != 1 {
		t.Fatalf("cmds=%v, want exactly one configuration line", cmds)
	}
	if cmds[0] != "hostname sw-lab-01" {
		t.Errorf("cmds[0]=%q, want %q", cmds[0], "hostname sw-lab-01")
	}
}

func TestHostnameCmdsCarryNoConfigModeOfTheirOwn(t *testing.T) {
	// runConfigBlock sends "configure" and "end" itself (apply.go). A builder
	// that repeated them would leave the session one level deeper than the next
	// gesture expects.
	var cmds = hostnameCmds("sw1")
	var c string
	for _, c = range cmds {
		if strings.HasPrefix(c, "conf") || c == "end" || c == "exit" {
			t.Errorf("builder must not carry mode changes, got %q", c)
		}
	}
}

func TestHostnameMismatchNamesTheThreeFaults(t *testing.T) {
	// Absent line: the CLI accepted the command and nothing changed.
	var absent = hostnameMismatch("sw-lab-01", "")
	if !strings.Contains(absent, "no hostname line") {
		t.Errorf("an absent hostname must be reported as such, got %q", absent)
	}
	// Truncation: the device kept a PREFIX. The message must carry the observed
	// ceiling, because no capture in this project establishes this range's limit.
	var cut = hostnameMismatch("sw-lab-01-very-long", "sw-lab-01-very")
	if !strings.Contains(cut, "14 bytes") || !strings.Contains(cut, "cut at") {
		t.Errorf("a truncation must name the observed ceiling, got %q", cut)
	}
	// Anything else: report both strings and claim nothing about why.
	var other = hostnameMismatch("sw-lab-01", "localhost")
	if !strings.Contains(other, `"localhost"`) || !strings.Contains(other, `"sw-lab-01"`) {
		t.Errorf("a divergence must show both names, got %q", other)
	}
}

func TestHostnameReadBackParsesWhatWeWrite(t *testing.T) {
	// The write form and the read form must agree, or SetHostname's verification
	// would fail on a device that did exactly what it was told — and on this
	// connector that failure is unattended: it becomes one
	// `device.hostname_push_failed` per cooldown, for ever.
	var rc = parseRunningConfig("!\n"+hostnameCmds("sw-lab-01")[0]+"\n!\n", "")
	if rc.Hostname != "sw-lab-01" {
		t.Errorf("Hostname=%q: the command this connector sends must be the line "+
			"its own parser reads back", rc.Hostname)
	}
}

func TestHostnameReadBackSurvivesLocalhost(t *testing.T) {
	// `localhost` is what a switch out of ZTP answers to, and it is the value the
	// first real push will be replacing. The parser must read it like any other
	// name: reading it as "no hostname line" would make the verdict `not_observed`
	// on the very fleet this gesture exists for.
	var rc = parseRunningConfig("!\nhostname localhost\n!\n", "")
	if rc.Hostname != "localhost" {
		t.Errorf("Hostname=%q, want %q — the ZTP default is a name like any other",
			rc.Hostname, "localhost")
	}
}

// --- the SERIAL-PROVISIONING half, which used to follow the opposite doctrine ---
//
// [MgmtPushCmds] built its `hostname` line from a private `sanitizeHostname`
// until 2026-08-09. That one was the LEAST wrong of the four connector spellings
// — it mapped `_` to `-` and it trimmed — and it was still wrong twice over: it
// DROPPED silently, so "Switch étage 1" reached the console banner as
// `Switch-tage-1` with nothing to trace the deleted letter back to, and it fell
// back to the literal `switch`, which on a ZTP fleet means replacing one
// fleet-wide name (`localhost`) with another. Both paths now go through
// driver.HostnameFromDeviceName.
//
// The builder has no Go caller — it is the written specification of
// web/src/pages/add/aristaEosSoftReset.ts (see MgmtPushCmds's own header) — so
// these tests pin a SPECIFICATION, not a live outage. They matter for the day
// someone wires a name through.

func TestMgmtPushRefusesTheSystemNameRatherThanManglingIt(t *testing.T) {
	var cmds, err = MgmtPushCmds(MgmtOpts{
		VlanID: 99, IP: "192.0.2.5", PrefixLen: 16, Gateway: "192.0.2.1",
		Username: "mcadmin", Password: "s3cret",
		TrunkPorts: []string{"Ethernet1"},
		Hostname:   "Switch étage 1",
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
		t.Error("the `switch` fallback is back: on a ZTP fleet that is one " +
			"fleet-wide name traded for another")
	}
	// THE REST OF THE PUSH SURVIVES THE REFUSAL: a switch left factory-reset and
	// unreachable over a display name is a worse answer than a reachable switch
	// still answering to `localhost`.
	if !strings.Contains(joined, "ip address 192.0.2.5/16") {
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
