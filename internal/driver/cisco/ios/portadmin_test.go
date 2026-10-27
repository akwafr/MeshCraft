package ios

import (
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// This file did not exist until "port_admin" was declared (contract.go,
// 2026-08-06), and its absence was the real gap: portadmin.go's guardrail — the
// thing that decides whether "shutdown" reaches the jack the session is riding —
// had NO test at all while every other connector's did. A declaration turns that
// code into something a screen can reach, so the guardrail is pinned here first and
// the command form second.
//
// Everything below runs against labRun (parse_test.go), the lab 2960-X's own
// running configuration: management VLAN 99 on Vlan99, GE28 a trunk with an
// explicit allowed list 1,99,100, GE3 a trunk with NO allowed-vlan line, GE2 an
// access port in VLAN 10, GE1 an empty block at its factory default.

// THE UPLINK CASE, and it is the one an IOS-shaped guardrail gets wrong: GE28 is a
// trunk whose allowed list names the management VLAN, so it is very likely the path
// this session arrives on.
func TestCarriesMgmtVLANProtectsTheTrunkedUplink(t *testing.T) {
	var rc = parseRunningConfig(labRun, "192.0.2.240")
	if rc.MgmtVLAN != 99 {
		t.Fatalf("the capture's management VLAN is 99, parsed %d", rc.MgmtVLAN)
	}
	if !carriesMgmtVLAN("GE28", rc, rc.MgmtVLAN) {
		t.Error("GE28 is a trunk carrying VLAN 99 (native, and in its allowed list): shutting " +
			"it would end the session")
	}
	if carriesMgmtVLAN("GE2", rc, rc.MgmtVLAN) {
		t.Error("GE2 is an access port in VLAN 10: it is not the session's path")
	}
}

// THE TRAP THIS PLATFORM SETS: a trunk with no "switchport trunk allowed vlan"
// line carries EVERY VLAN on IOS, which includes the management one. Reading its
// empty tagged list as "carries nothing" would hand the operator a shutdown on the
// uplink with no warning at all — the failure apply.go was built around, and the
// reason AllowedAll exists.
func TestCarriesMgmtVLANReadsAllowedAllAsEveryVLAN(t *testing.T) {
	var rc = parseRunningConfig(labRun, "192.0.2.240")
	if !rc.AllowedAll["GE3"] {
		t.Fatal("GE3 has no allowed-vlan line: the parser must mark it AllowedAll")
	}
	if !carriesMgmtVLAN("GE3", rc, rc.MgmtVLAN) {
		t.Error("a trunk with no allowed-vlan line carries ALL VLANs on IOS, management " +
			"included: it must be treated as carrying the session")
	}
}

// A port with NO block in the running configuration is at its factory default —
// access VLAN 1 — and not "unknown". The distinction matters on a switch whose
// management VLAN IS 1, which is the factory state of the whole range: reading an
// absent block as "carries nothing" would wave every untouched port through.
func TestCarriesMgmtVLANTreatsAnAbsentBlockAsAccessVLAN1(t *testing.T) {
	var rc = parseRunningConfig(labRun, "192.0.2.240")
	var _, hasBlock = rc.Ports["GE47"]
	if hasBlock {
		t.Fatal("GE47 has no block in this capture: the premise of this test is gone")
	}
	if !carriesMgmtVLAN("GE47", rc, 1) {
		t.Error("a port with no block is at access VLAN 1: on a switch managed in VLAN 1 it " +
			"carries the session")
	}
	if carriesMgmtVLAN("GE47", rc, 99) {
		t.Error("a factory-default port is in VLAN 1, not in VLAN 99")
	}
}

// ZERO IS NEVER PERMISSION. When no SVI carries an address that is administratively
// up, carriesMgmtVLAN answers false for every port — and ApplyPortAdmin must then
// spare every LINKED port instead of falling through to the write. The membership
// test is the wrong place to encode that, so this asserts the contract of the pair:
// false here, and the applier's own mgmt <= 0 branch above it.
func TestCarriesMgmtVLANAnswersFalseForAnUnknownManagementVLAN(t *testing.T) {
	var rc = parseRunningConfig(labRun, "192.0.2.240")
	if carriesMgmtVLAN("GE28", rc, 0) {
		t.Error("VLAN 0 is not a VLAN: the membership test must not invent a match, and " +
			"ApplyPortAdmin's own mgmt <= 0 branch is what spares the linked ports")
	}
}

// The write form, and the read-back word that confirms it. "disabled" is what
// "show interfaces status" prints for a port carrying "shutdown", and it must
// NOT be confused with "notconnect" (nothing plugged in) or "err-disabled" (the
// switch shut it itself) — reading the three as one would report a missing cable as
// a job done.
func TestPortAdminCmdsAndTheStatusWordItReadsBack(t *testing.T) {
	var down = portAdminCmds("GigabitEthernet1/0/12", false)
	if len(down) != 3 || down[0] != "interface GigabitEthernet1/0/12" ||
		down[1] != "shutdown" || down[2] != "exit" {
		t.Errorf("shut form = %q", down)
	}
	var up = portAdminCmds("GigabitEthernet1/0/12", true)
	if len(up) != 3 || up[1] != "no shutdown" {
		t.Errorf("un-shut form = %q", up)
	}

	if !(statusPort{Status: "disabled"}).adminDown() {
		t.Error("\"disabled\" is the word this platform prints for an administratively shut port")
	}
	var word string
	for _, word = range []string{"notconnect", "connected", "err-disabled", ""} {
		if (statusPort{Status: word}).adminDown() {
			t.Errorf("%q must not read as administratively down: only \"shutdown\" produces "+
				"\"disabled\", and confusing the three makes an absent cable look like a job done",
				word)
		}
	}
}

// The connector must DECLARE the capability, or the API layer never reaches
// ApplyPortAdmin at all (api/portadmin_apply.go gates on the name). "stp" stays
// declared; "dhcp_snooping" must stay false — its forms have never been accepted
// on this range, and dhcpsnoop_test.go guards that separately. Both live in ONE
// expression now, which is why this second lock is worth having.
func TestDriverDeclaresPortAdmin(t *testing.T) {
	var d = &Driver{}
	if !d.WritesExtra("port_admin") {
		t.Error("ApplyPortAdmin exists, \"shutdown\" / \"no shutdown\" is the form the operator " +
			"named, and an undeclared capability is a button that leads nowhere")
	}
	if !d.WritesExtra("stp") {
		t.Error("ApplySpanningTree exists and \"stp\" must stay declared")
	}
	var name string
	// dhcp_snooping left this list on 2026-08-09, proven on the lab 2960-X.
	for _, name = range []string{"poe", "port_security", "port_power_cycle"} {
		if d.WritesExtra(name) {
			t.Errorf("declares %q writable, and nothing in this package writes it", name)
		}
	}
}

// The gesture must not reach a LOOKALIKE CLI. Many platforms answer
// "show version" and accept "conf t" without meaning the same thing by
// "shutdown", and now that the capability is declared a screen will offer this
// button on anything this connector adopted. Same refusal ApplyPorts makes, and it
// has to fire BEFORE the first command: nothing here touches the network.
func TestApplyPortAdminIsRefusedOnAnUnknownDialect(t *testing.T) {
	var c = &Client{dialect: dialectUnknown, cli: &cliConn{}}
	var _, err = c.ApplyPortAdmin(t.Context(), driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: "GE5", Enabled: false}},
	})
	if err == nil {
		t.Fatal("\"shutdown\" accepted on a CLI that never announced Cisco IOS")
	}
	if !strings.Contains(err.Error(), "cisco-ios") {
		t.Errorf("the refusal must name the connector: %v", err)
	}
}

// --- the fake CLI, and the guardrail read END TO END --------------------
//
// Everything above tests the PIECES. The decision itself —
// "!it.Enabled && !plan.Force && now.LinkUp", then the management-VLAN
// membership — lives inline in ApplyPortAdmin and was, until this fake existed,
// verified by READING only (TODO.md said so). It is the branch that decides
// whether "shutdown" reaches the jack the session is riding, so it is now
// exercised through the real applier: same reads, same command sequence, same
// re-read.
//
// The fake is the EdgeSwitch connector's snoopFakeCLI in this package's dialect:
// it stands in for the SSH shell, records every line sent, and answers each with a
// canned reply terminated by a prompt — which is all [cliConn.run] needs. No SSH
// client, no session; those fields stay nil and nothing closes the connection.

type portAdminFakeCLI struct {
	sent  []string
	out   chan byte
	reply func(cmd string) string
}

// portAdminPrompt is the privileged prompt of the fixture switch. rePromptPriv
// matches the configuration sub-modes with the same pattern, so the fake does not
// model "lab-cat2960x(config-if)#" separately.
const portAdminPrompt = "\nlab-cat2960x#"

func (f *portAdminFakeCLI) Write(p []byte) (int, error) {
	var line = strings.TrimRight(string(p), " \t\r\n")
	if line == "" {
		return len(p), nil
	}
	f.sent = append(f.sent, line)
	var b byte
	for _, b = range []byte(f.reply(line) + portAdminPrompt) {
		f.out <- b
	}
	return len(p), nil
}

func (f *portAdminFakeCLI) Close() error { return nil }

// newPortAdminFakeClient returns a Client whose session is the fake, with the
// dialect already recognized so requireWritableDialect lets the write through.
//
// `statuses` are the successive answers to "show interfaces status": the
// pre-write read first, then the confirming re-read. Reading past the end repeats
// the last one — a device that did not change is a legitimate thing to model.
func newPortAdminFakeClient(runCfg string, statuses ...string) (*Client, *portAdminFakeCLI) {
	var n int
	var f = &portAdminFakeCLI{out: make(chan byte, 1<<20)}
	f.reply = func(cmd string) string {
		switch cmd {
		case "show running-config":
			return runCfg
		case "show interfaces status":
			if len(statuses) == 0 {
				return ""
			}
			if n < len(statuses) {
				n++
			}
			return statuses[n-1]
		}
		return ""
	}
	return &Client{
		cli:     &cliConn{stdin: f, out: f.out},
		host:    "192.0.2.240",
		User:    "mcadmin",
		dialect: dialectIOS,
	}, f
}

// statusWord returns labStatus with ONE port's status column replaced. Used for
// the re-read: "disabled" is what the device prints once "shutdown" landed.
func statusWord(port, from, to string) string {
	var lines = strings.Split(labStatus, "\n")
	var i int
	for i = range lines {
		if strings.HasPrefix(lines[i], port+" ") {
			lines[i] = strings.Replace(lines[i], from, to, 1)
		}
	}
	return strings.Join(lines, "\n")
}

func sentLines(f *portAdminFakeCLI) string { return strings.Join(f.sent, "\n") }

func skippedMentions(skipped []string, sub string) bool {
	var s string
	for _, s = range skipped {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// THE GUARDRAIL, through the applier. GE28 is connected AND is the trunk carrying
// VLAN 99 — the session's own path on the fixture switch. Nothing may be sent: not
// "configure terminal", not anything. The two reads and nothing else.
func TestApplyPortAdminRefusesToShutTheLinkedManagementUplink(t *testing.T) {
	var c, f = newPortAdminFakeClient(labRun, labStatus)
	var res, err = c.ApplyPortAdmin(t.Context(), driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: "GE28", Enabled: false}},
	})
	if err != nil {
		t.Fatalf("a refusal is an ORDINARY outcome and belongs in Skipped, not in an error: %v", err)
	}
	if sentLines(f) != "show running-config\nshow interfaces status" {
		t.Fatalf("the guardrail must fire BEFORE any write; commands sent:\n%s", sentLines(f))
	}
	if len(res.PortsWritten) != 0 {
		t.Errorf("PortsWritten = %v, want empty: nothing was sent", res.PortsWritten)
	}
	if !skippedMentions(res.Skipped, "GE28 NOT shut") ||
		!skippedMentions(res.Skipped, "management VLAN (99)") {
		t.Errorf("the refusal must name the port and the VLAN it protects: %v", res.Skipped)
	}
}

// Force is the operator saying they know. It lifts the refusal and NOTHING else:
// the exact command sequence, one interface block, and the re-read that is what
// says the port moved.
func TestApplyPortAdminForceLiftsTheRefusalAndSendsTheDocumentedForm(t *testing.T) {
	var c, f = newPortAdminFakeClient(labRun,
		labStatus, statusWord("Gi1/0/28", "connected", "disabled "))
	var res, err = c.ApplyPortAdmin(t.Context(), driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: "GE28", Enabled: false}},
		Force: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var want = strings.Join([]string{
		"show running-config",
		"show interfaces status",
		"configure terminal",
		"interface GigabitEthernet1/0/28",
		"shutdown",
		"exit",
		"end",
		"show interfaces status", // the re-read, and it is what decides
	}, "\n")
	if sentLines(f) != want {
		t.Fatalf("commands sent:\n%s\n\nwant:\n%s", sentLines(f), want)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("Force was given: nothing must be refused, got %v", res.Skipped)
	}
	if strings.Join(res.PortsWritten, " ") != "GE28" {
		t.Errorf("PortsWritten = %v, want [GE28]", res.PortsWritten)
	}
	if !notesMention(res.Notes, "deliberately NOT saved") {
		t.Errorf("the gesture is an action and must say it was not persisted: %v", res.Notes)
	}
}

// The guardrail is NARROW on purpose: GE2 is connected but is an access port in
// VLAN 10, so it is not the session's path and the operator gets the gesture they
// asked for without a Force they would learn to tick by reflex.
func TestApplyPortAdminShutsALinkedPortThatDoesNotCarryAdministration(t *testing.T) {
	var c, f = newPortAdminFakeClient(labRun,
		labStatus, statusWord("Gi1/0/2", "connected", "disabled "))
	var res, err = c.ApplyPortAdmin(t.Context(), driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: "GE2", Enabled: false}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(sentLines(f), "interface GigabitEthernet1/0/2\nshutdown") {
		t.Fatalf("an access port in VLAN 10 is not the session's path: %s", sentLines(f))
	}
	if strings.Join(res.PortsWritten, " ") != "GE2" || len(res.Skipped) != 0 {
		t.Errorf("written=%v skipped=%v", res.PortsWritten, res.Skipped)
	}
}

// ZERO IS IGNORANCE, and here is the pair that proves it is not a freeze: with no
// SVI carrying an address, every LINKED port is spared (GE2) while a port with
// nothing plugged into it still moves (GE1). A port with no link cannot be
// carrying the session that would be cut.
func TestApplyPortAdminSparesLinkedPortsWhenTheManagementVLANIsUnknown(t *testing.T) {
	var noMgmt = strings.Replace(labRun,
		"interface Vlan99\n ip address 192.0.2.240 255.255.0.0",
		"interface Vlan99\n no ip address", 1)
	if strings.Contains(noMgmt, "192.0.2.240") {
		t.Fatal("the fixture edit did not apply: this test would prove the opposite of itself")
	}
	var c, f = newPortAdminFakeClient(noMgmt,
		labStatus, statusWord("Gi1/0/1", "notconnect", "disabled  "))
	var res, err = c.ApplyPortAdmin(t.Context(), driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{
			{Port: "GE1", Enabled: false},
			{Port: "GE2", Enabled: false},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(sentLines(f), "GigabitEthernet1/0/2") {
		t.Errorf("GE2 has a LINK and the management VLAN is unknown: it must be spared\n%s",
			sentLines(f))
	}
	if !strings.Contains(sentLines(f), "interface GigabitEthernet1/0/1\nshutdown") {
		t.Errorf("GE1 has no link and cannot be carrying the session: sparing it too would "+
			"make the gesture useless on exactly the ports it is safe on\n%s", sentLines(f))
	}
	if !skippedMentions(res.Skipped, "could not determine") {
		t.Errorf("the refusal must say WHAT is unknown: %v", res.Skipped)
	}
	if strings.Join(res.PortsWritten, " ") != "GE1" {
		t.Errorf("PortsWritten = %v, want [GE1]", res.PortsWritten)
	}
}

// THE READ THAT LIES BY HALVES. A "show running-config" that lost its head keeps
// the SVIs — IOS prints them last — and loses every "interface GigabitEthernet"
// block. rc.MgmtVLAN then reads 99 while the membership table is empty, and
// carriesMgmtVLAN's "no block = factory default, access VLAN 1" branch (right for a
// port absent from a COMPLETE configuration) would answer false for the trunked
// uplink and wave it through. A real running-config always prints one block per
// interface, empty body included, so zero blocks is a broken read: the management
// VLAN is treated as UNKNOWN and every linked port is spared.
func TestApplyPortAdminDistrustsAManagementVLANWithNoMembershipTable(t *testing.T) {
	var tail = "interface Vlan99\n ip address 192.0.2.240 255.255.0.0\n!\n" +
		"ip default-gateway 192.0.2.1\nline vty 0 15\n login local\n!\nend\n"
	var rc = parseRunningConfig(tail, "192.0.2.240")
	if rc.MgmtVLAN != 99 || len(rc.Ports) != 0 {
		t.Fatalf("the premise of this test is a config with a management VLAN and NO port "+
			"block: mgmt=%d ports=%d", rc.MgmtVLAN, len(rc.Ports))
	}
	if carriesMgmtVLAN("GE28", rc, rc.MgmtVLAN) {
		t.Fatal("premise: with no block at all, the membership test answers false for the " +
			"uplink — which is exactly why ApplyPortAdmin must not trust this VLAN")
	}

	var c, f = newPortAdminFakeClient(tail, labStatus)
	var res, err = c.ApplyPortAdmin(t.Context(), driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: "GE28", Enabled: false}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sentLines(f) != "show running-config\nshow interfaces status" {
		t.Fatalf("the uplink must not be shut on a read that carried no membership at all:\n%s",
			sentLines(f))
	}
	if !skippedMentions(res.Skipped, "could not determine") {
		t.Errorf("the port must be spared for the UNKNOWN reason: %v", res.Skipped)
	}
	if !notesMention(res.Notes, "NO interface block") {
		t.Errorf("the notes must say the read was the problem, not the device: %v", res.Notes)
	}
}

// RE-ENABLING IS NEVER GATED, even on a port that carries the management VLAN.
// GE3 is a trunk with no allowed-vlan line — it carries every VLAN, 99 included —
// and is "disabled" in the capture. Bringing it back cuts nothing, and refusing
// it would block the gesture an operator needs precisely when something already
// went wrong.
func TestApplyPortAdminNeverGatesTheEnableDirection(t *testing.T) {
	var c, f = newPortAdminFakeClient(labRun,
		labStatus, statusWord("Gi1/0/3", "disabled", "connected"))
	var res, err = c.ApplyPortAdmin(t.Context(), driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: "GE3", Enabled: true}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(sentLines(f), "interface GigabitEthernet1/0/3\nno shutdown") {
		t.Fatalf("\"no shutdown\" must reach a management-VLAN port: %s", sentLines(f))
	}
	if len(res.Skipped) != 0 || strings.Join(res.PortsWritten, " ") != "GE3" {
		t.Errorf("written=%v skipped=%v", res.PortsWritten, res.Skipped)
	}
}

// A port already where the plan wants it is not written, and does not appear in
// PortsWritten (the contract): GE3 reads "disabled" and the plan asks for shut.
// Sending "shutdown" anyway would be a no-op on the device and a lie in the
// result — and the port would be reported as moved by this gesture when it was
// already down before it ran.
func TestApplyPortAdminSendsNothingForAPortAlreadyThere(t *testing.T) {
	var c, f = newPortAdminFakeClient(labRun, labStatus)
	var res, err = c.ApplyPortAdmin(t.Context(), driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: "GE3", Enabled: false}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sentLines(f) != "show running-config\nshow interfaces status" {
		t.Fatalf("nothing may be sent for a port already shut:\n%s", sentLines(f))
	}
	if len(res.PortsWritten) != 0 || len(res.Skipped) != 0 {
		t.Errorf("written=%v skipped=%v — both must be empty", res.PortsWritten, res.Skipped)
	}
}

// THE RE-READ IS WHAT DECIDES. A CLI that accepts "shutdown" and changes nothing
// is this platform's silent failure, and the returned success must not repeat it:
// the port stays out of PortsWritten and the note carries the word the device
// actually printed.
func TestApplyPortAdminReportsAPortThatDidNotMove(t *testing.T) {
	var c, _ = newPortAdminFakeClient(labRun, labStatus, labStatus)
	var res, err = c.ApplyPortAdmin(t.Context(), driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: "GE2", Enabled: false}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.PortsWritten) != 0 {
		t.Errorf("the re-read still says \"connected\": PortsWritten must stay empty, got %v",
			res.PortsWritten)
	}
	if !notesMention(res.Notes, "still reads \"connected\"") {
		t.Errorf("the note must quote the status word the device printed: %v", res.Notes)
	}
}

// The refusal an operator reads when the CLI rejects the verb must NAME the release
// this command set was written against — that is the whole reason a documented form
// is declarable: the failure says what to survey rather than going silent.
func TestErrDisabledNoteAndDocumentedReleaseAreNamedInRefusals(t *testing.T) {
	if DocumentedRelease == "" {
		t.Fatal("the refusals quote DocumentedRelease: an empty one makes them useless")
	}
	var note = errDisabledEnableNote("GE5", statusPort{Status: ErrDisableGBIC})
	if !strings.Contains(note, "shutdown then no shutdown") {
		t.Errorf("an err-disabled port is cleared by the BOUNCE, and \"no shutdown\" alone "+
			"changes nothing: the note must say so — %q", note)
	}
}
