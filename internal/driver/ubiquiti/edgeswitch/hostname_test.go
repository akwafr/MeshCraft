package edgeswitch

import (
	"context"
	"strings"
	"testing"
)

// capturedLLDPLocalBefore / capturedLLDPLocalAfter are EXCERPTS of the
// 2026-08-10 console capture on the lab EdgePoint S16 (unifimoule1, 192.0.2.4,
// FASTPATH 1.9.3) — the two rows of "show lldp local-device detail 0/18" that
// the system-name question turns on, before and after "snmp-server sysname
// mctest1". They live here rather than in testdata/ precisely because they are
// excerpts: the package's testdata files are WHOLE command outputs, and filing a
// two-row fragment among them would misrepresent it as one.
const (
	capturedLLDPLocalBefore = "System Name: UBNT EdgeSwitch\n" +
		"System Description: EdgePoint S16, 1.9.3, Linux 3.6.5-03329b4a, 1.2.0.5192732\n"
	capturedLLDPLocalAfter = "System Name: mctest1\n" +
		"System Description: EdgePoint S16, 1.9.3, Linux 3.6.5-03329b4a, 1.2.0.5192732\n"
)

func TestHostnameCmdsCarryTheirOwnConfigMode(t *testing.T) {
	// runBlock sends a FLAT list and waits one prompt per line: the mode changes
	// have to be in the slice or they never happen. This is the package idiom
	// (cf. accounts.go), and the 2026-08-10 capture typed the verb from
	// "(Config)#", not from privileged EXEC.
	var cmds = hostnameCmds("sw-lab-01")
	var want = []string{"configure", "snmp-server sysname sw-lab-01", "exit"}
	if len(cmds) != len(want) {
		t.Fatalf("cmds=%v, want %v", cmds, want)
	}
	var i int
	for i = range want {
		if cmds[i] != want[i] {
			t.Errorf("[%d] %q != %q", i, cmds[i], want[i])
		}
	}
}

// TestHostnameCmdsEmitTheNameBare pins the QUOTING DECISION, which is a decision
// and not an accident.
//
// "snmp-server sysname mctest1" is the form the device was captured accepting,
// and driver.ValidHostname has already stripped the name of anything this CLI
// could misread — so nothing is quoted. The quoted form is only needed for a
// value carrying a space (the factory ""UBNT EdgeSwitch""), which meshcraft can
// never produce. If someone adds quotes here "for safety", the emitted line stops
// being the captured one and the device would store the quotes on some builds.
func TestHostnameCmdsEmitTheNameBare(t *testing.T) {
	var joined = strings.Join(hostnameCmds("sw-lab-01"), "\n")
	if strings.Contains(joined, `"`) {
		t.Errorf("the name is emitted quoted:\n%s\n\nthe captured accepted form is bare "+
			"(\"snmp-server sysname mctest1\", 2026-08-10) and ValidHostname guarantees "+
			"there is nothing left to quote", joined)
	}
}

// TestHostnameCmdsNeverUtterTheRefutedVerb pins an ABSENCE, and the absence is a
// fact this connector paid for.
//
// On 2026-08-05 the reconciliation loop sent "hostname unifimoule1" to the lab
// EdgePoint S16 and got "% Invalid input detected at '^' marker." — this
// FASTPATH CLI has no `hostname` command. Learning the RIGHT verb on 2026-08-10
// does not resurrect the wrong one, and a command sent in order to be refused
// puts a red herring in the operator's transcript where it makes a REAL refusal
// harder to see. Sibling case: TestMgmtPushCmdsSendsNoHostname.
func TestHostnameCmdsNeverUtterTheRefutedVerb(t *testing.T) {
	var c string
	for _, c = range hostnameCmds("sw-lab-01") {
		if strings.Contains(c, "hostname") {
			t.Errorf("the connector emits %q — this CLI refused \"hostname <name>\" on "+
				"2026-08-05 (\"%% Invalid input detected at '^' marker.\"). The system "+
				"name is \"snmp-server sysname\" here; see hostname.go", c)
		}
	}
}

// TestLLDPLocalSysNameReadsTheCapturedTranscript replays the capture that settled
// the whole question: the same command, before and after, must yield the two
// names the console showed. This is the value api/hostnameheal.go compares.
func TestLLDPLocalSysNameReadsTheCapturedTranscript(t *testing.T) {
	var got = lldpLocalSysName(capturedLLDPLocalBefore)
	// The factory name has a SPACE. Reading it as "UBNT" would misdescribe the
	// exact state this gesture exists to repair, which is why the row is taken
	// whole rather than as a first field.
	if got != "UBNT EdgeSwitch" {
		t.Errorf("before the write: got %q, want %q", got, "UBNT EdgeSwitch")
	}
	if got = lldpLocalSysName(capturedLLDPLocalAfter); got != "mctest1" {
		t.Errorf("after the write: got %q, want mctest1", got)
	}
	// "System Description:" must not be mistaken for the name — the two rows are
	// adjacent in the real output and both start with "System".
	if strings.Contains(lldpLocalSysName(capturedLLDPLocalAfter), "EdgePoint") {
		t.Error("the description row was read as the system name")
	}
	// No row at all: LLDP off, or a command the device refused. Not an error here
	// — readSysname falls back on it.
	if got = lldpLocalSysName("% Invalid input detected at '^' marker.\n"); got != "" {
		t.Errorf("a refusal must read as no observation, got %q", got)
	}
}

func TestRunningConfigSysnameAcceptsBothRenderings(t *testing.T) {
	// FIXTURE CONSTRUCTED, not captured: the baseline capture
	// (testdata/show_running_config.txt) is a factory-default unit and carries no
	// sysname line, so neither rendering is proven on this range. The nearby
	// "snmp-server community "…" ro" IS captured quoted, which is a hint about
	// the family's habit with string arguments and not proof about this command.
	// Accepting both is what keeps verification from failing on a device that did
	// exactly what it was told.
	var quoted = "!\n!Current Configuration:\nsnmp-server sysname \"sw-lab-01\"\nvlan database\n"
	var got = runningConfigSysname(quoted)
	if got != "sw-lab-01" {
		t.Errorf("quoted form: got %q, want sw-lab-01", got)
	}
	var bare = "!\nsnmp-server sysname sw-lab-01\nnetwork protocol none\n"
	if got = runningConfigSysname(bare); got != "sw-lab-01" {
		t.Errorf("bare form: got %q, want sw-lab-01", got)
	}
	// A quoted value WITH A SPACE: meshcraft can never write one (ValidHostname),
	// but a unit still carrying the factory name has one, and the mismatch message
	// has to be able to show it whole.
	var factory = "!\nsnmp-server sysname \"UBNT EdgeSwitch\"\n!\n"
	if got = runningConfigSysname(factory); got != "UBNT EdgeSwitch" {
		t.Errorf("factory value: got %q, want %q", got, "UBNT EdgeSwitch")
	}
	// The refuted verb must not be picked up by the new parser either.
	if got = runningConfigSysname("!\nhostname sw-lab-01\n"); got != "" {
		t.Errorf("\"hostname\" is not a line this CLI can carry, got %q", got)
	}
}

func TestRunningConfigSysnameIsEmptyOnTheRealBaselineCapture(t *testing.T) {
	// The REAL capture, and the assertion is the interesting one: a
	// factory-default unit carries no sysname line, and that must read as "no name
	// configured" rather than as a parse failure — otherwise this write could
	// never confirm the very first name it sets. It is also why the LLDP TLV is
	// the primary re-read: the TLV said "UBNT EdgeSwitch" while the
	// configuration said nothing at all.
	var got = runningConfigSysname(mustRead(t, "testdata/show_running_config.txt"))
	if got != "" {
		t.Errorf("baseline capture yields %q: the fixture gained a sysname line, which "+
			"makes it a CAPTURE of the read form — update hostname.go's comment, it "+
			"currently says the rendering was never observed", got)
	}
}

func TestHostnameMismatchNamesTheThreeFaults(t *testing.T) {
	var absent = hostnameMismatch("sw-lab-01", "", sysnameFromTLV)
	if !strings.Contains(absent, "no system name") {
		t.Errorf("an absent name must be reported as such, got %q", absent)
	}
	if !strings.Contains(absent, "snmp-server sysname sw-lab-01") {
		t.Errorf("the message must quote the command actually sent, got %q", absent)
	}
	var cut = hostnameMismatch("sw-lab-01-very-long", "sw-lab-01-very-", sysnameFromTLV)
	if !strings.Contains(cut, "15 bytes") {
		t.Errorf("a truncation must name the observed ceiling, got %q", cut)
	}
	var other = hostnameMismatch("sw-lab-01", "UBNT EdgeSwitch", sysnameFromRun)
	if !strings.Contains(other, `"UBNT EdgeSwitch"`) {
		t.Errorf("a divergence must show what the device carries, got %q", other)
	}
	// Which source spoke is part of the message: the TLV is the value the healer
	// compares, the configuration is a fallback, and they are not equal evidence.
	if !strings.Contains(other, sysnameFromRun) || strings.Contains(other, sysnameFromTLV) {
		t.Errorf("the message must name its source, got %q", other)
	}
}

// --- Refusals, all of which must happen before a single byte is sent ----------
//
// These run against a Client with NO open session: c.cli is nil, so any code
// path that reached the device would panic rather than pass. That is the point —
// the assertion is that the refusal comes first.

func TestSetHostnameRefusesWithoutPrivilege(t *testing.T) {
	var c = &Client{host: "192.0.2.4"}
	var err = c.SetHostname(context.Background(), "sw-lab-01")
	if err == nil {
		t.Fatal("a session that never reached privileged mode must not attempt the write")
	}
	if !strings.Contains(err.Error(), "ubiquiti-edgeswitch") {
		t.Errorf("the refusal must name the connector, got %q", err)
	}
}

func TestSetHostnameRefusesANameItCouldNotSendBare(t *testing.T) {
	// known+privileged, so requireWritable passes; the name is what must be
	// refused, and it is refused BEFORE the transport check — which is also the
	// guarantee behind the bare emission: a space can never reach hostnameCmds.
	var c = &Client{host: "192.0.2.4", known: true, privileged: true}
	var bad = []string{
		"UBNT EdgeSwitch", // the factory value: a SPACE, the one that needs quotes
		"sw lab",
		"sw\nusername attacker password x level 15", // the injection this floor closes
		"sw\"lab",
		"-sw-lab",
		"",
		strings.Repeat("a", 64),
	}
	var name string
	for _, name = range bad {
		var err = c.SetHostname(context.Background(), name)
		if err == nil {
			t.Errorf("SetHostname(%q) was accepted", name)
			continue
		}
		if strings.Contains(err.Error(), "no open session") {
			t.Errorf("SetHostname(%q) reached the transport check before refusing the "+
				"name: the argument floor must come first, got %q", name, err)
		}
	}

	// And a VALID name on the same sessionless access stops at the transport
	// check — proving the case above was refused for the name and not for the
	// missing session.
	var err = c.SetHostname(context.Background(), "sw-lab-01")
	if err == nil || !strings.Contains(err.Error(), "no open session") {
		t.Errorf("a valid name with no session must stop at the transport check, got %v", err)
	}
}
