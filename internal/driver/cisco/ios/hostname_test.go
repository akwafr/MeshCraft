package ios

import (
	"strings"
	"testing"
)

// The command builder and the mismatch explanation, tested on states — the
// house rule for this package (cf. apply_test.go: "Apply logic is proven on
// states, not on hardware").

func TestHostnameCmdsIsOneUnquotedLine(t *testing.T) {
	// One line, and no quoting: driver.ValidHostname has already ruled out
	// everything that would need quoting. A form that quoted anyway would put
	// the quotes INTO the name on this CLI, and the re-read would then disagree
	// with the plan for ever.
	var cmds = hostnameCmds("sw-lab-01")
	if len(cmds) != 1 {
		t.Fatalf("cmds=%v, want exactly one configuration line", cmds)
	}
	if cmds[0] != "hostname sw-lab-01" {
		t.Errorf("cmds[0]=%q, want %q", cmds[0], "hostname sw-lab-01")
	}
}

func TestHostnameCmdsCarryNoConfigModeOfTheirOwn(t *testing.T) {
	// runConfigBlock sends "configure terminal" and "end" itself. A builder that
	// repeated them would leave the session one level deeper than the next
	// gesture expects — the exact fault runConfigBlock's comment guards.
	var cmds = hostnameCmds("sw1")
	var c string
	for _, c = range cmds {
		if strings.HasPrefix(c, "conf") || c == "end" || c == "exit" {
			t.Errorf("builder must not carry mode changes, got %q", c)
		}
	}
}

func TestHostnameMismatchNamesTheThreeFaults(t *testing.T) {
	// Absent line: the write did nothing at all.
	var absent = hostnameMismatch("sw-lab-01", "")
	if !strings.Contains(absent, "no hostname line") {
		t.Errorf("an absent hostname must be reported as such, got %q", absent)
	}
	// Truncation: the device kept a PREFIX. The message must carry the observed
	// ceiling, because that number exists nowhere else in this project.
	var cut = hostnameMismatch("sw-lab-01-very-long", "sw-lab-01-very")
	if !strings.Contains(cut, "14 bytes") || !strings.Contains(cut, "cut at") {
		t.Errorf("a truncation must name the observed ceiling, got %q", cut)
	}
	// Anything else: report both strings and claim nothing about why.
	var other = hostnameMismatch("sw-lab-01", "Switch")
	if !strings.Contains(other, `"Switch"`) || !strings.Contains(other, `"sw-lab-01"`) {
		t.Errorf("a divergence must show both names, got %q", other)
	}
}

func TestHostnameReadBackParsesWhatWeWrite(t *testing.T) {
	// The write form and the read form must agree, or verification would fail on
	// a device that did exactly what it was told. Fixture CONSTRUCTED from the
	// builder's own output — the running-config line shape itself is captured
	// (cf. labRun in parse_test.go, which reRunHostname already reads).
	var rc = parseRunningConfig("!\n"+hostnameCmds("sw-lab-01")[0]+"\n!\n", "")
	if rc.Hostname != "sw-lab-01" {
		t.Errorf("Hostname=%q: the command this connector sends must be the line "+
			"its own parser reads back", rc.Hostname)
	}
}

// --- the SERIAL-PROVISIONING half, which used to follow the opposite doctrine ---
//
// [MgmtPushCmds] built its `hostname` line from a private `sanitizeHostname`
// until 2026-08-09. That function disagreed with everything above it in this
// file: it KEPT `_` (which driver.ValidHostname forbids, so the name it produced
// could never be the name hostnameCmds pushes), it DROPPED every other character
// it did not recognise, it never trimmed, and it fell back to the literal
// `switch`. Both paths now go through driver.HostnameFromDeviceName.
//
// The builder has no Go caller — it is the written specification of
// web/src/pages/add/ciscoIosSoftReset.ts (see its own header) — so these tests
// pin a SPECIFICATION, not a live outage. They matter for the day someone wires
// a name through: without them the four ranges would each mangle it differently
// again, and the first sign would be a console banner nobody can trace.

func TestMgmtPushRefusesTheSystemNameRatherThanManglingIt(t *testing.T) {
	var cmds, err = MgmtPushCmds(MgmtOpts{
		VlanID: 99, IP: "192.0.2.240", PrefixLen: 16, Gateway: "192.0.2.1",
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
	// The exact spelling this connector used to emit, named so it fails loudly.
	if strings.Contains(joined, "Switch-tage-1") {
		t.Error("the dropped-character doctrine is back")
	}
	if strings.Contains(joined, "hostname switch") {
		t.Error("the `switch` fallback is back: a fleet of identically-named devices")
	}
	// THE REST OF THE PUSH SURVIVES THE REFUSAL, and that is the contract: a
	// switch left factory-reset and unreachable over a display name is a worse
	// answer than a reachable switch that is still called `Switch`.
	if !strings.Contains(joined, "ip address 192.0.2.240 255.255.0.0") {
		t.Errorf("the management address is missing from a push whose only fault "+
			"was its system name: %s", joined)
	}
	if !strings.Contains(joined, "username mcadmin") {
		t.Error("the credentials are missing from a push that only refused a name")
	}
}

func TestMgmtPushNormalizesWithTheSharedRule(t *testing.T) {
	var cmds, err = MgmtPushCmds(MgmtOpts{
		VlanID: 99, IP: "192.0.2.240", PrefixLen: 16,
		Hostname: " sw_lab 01 ",
	})
	if err != nil {
		t.Fatalf("a normalizable name was refused: %s", err)
	}
	var joined = strings.Join(cmds, "\n")
	// The separators become hyphens, the ends are trimmed, and the line is the
	// SAME one hostnameCmds sends on the reconciled path.
	if !strings.Contains(joined, hostnameCmds("sw-lab-01")[0]) {
		t.Errorf("provisioning and reconciliation disagree on the same name: %s", joined)
	}
	if strings.Contains(joined, "sw_lab") {
		t.Error("the underscore survived: it is the one character " +
			"driver.ValidHostname forbids, so the healer could never emit this name")
	}
}
