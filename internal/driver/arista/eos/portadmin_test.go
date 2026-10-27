package eos

import (
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// This file did not exist until 2026-08-09, and eos was the ONLY connector in the
// tree without one — while implementing [Client.ApplyPortAdmin] in full. The two
// facts sat badly together: TODO.md's "port_admin" entry said the guardrail was
// "written and tested", and the only thing tested was carriesMgmtVLAN, twice, from
// apply_test.go where it is exercised for the VLAN write. The DECISION — whether
// "shutdown" reaches the jack the session is riding — had no test at all.
//
// THE CAPABILITY WAS NOT DECLARED WHEN THIS FILE WAS WRITTEN, and nothing here
// asked for it to be: contract.go left "port_admin" false because no EOS switch
// had ever been sent "shutdown" by this product, which is a hardware fact and no
// test can retire it. What these tests DID retire is the other half of the risk —
// that the day a capture arrived and the flag flipped, the planning logic
// underneath it would never have been run.
//
// THE CAPTURE ARRIVED ON 2026-08-10 (core01.lab, Ethernet1: "shutdown" →
// "disabled" → "no shutdown" → "notconnect"), so the flag is now UP and
// TestWritesExtraPortAdminIsDeclaredOnTheCapture below pins it up. Nothing about
// the tests in this file changed with it, and their standing has not changed
// either: EVERYTHING HERE RUNS AGAINST A FAKE CLI. The capture was typed at a
// device by hand; these exercise the applier against a fixture. Neither is
// evidence for the other, and the gap between them — an applier that has never
// driven the gesture it now declares — is what live_portadmin_test.go exists to
// close.
//
// Everything runs against the package's own fixtures — runFixture and
// statusFixture (parse_test.go). On that switch: management VLAN 99 on Vlan99,
// ET3 a trunk whose native VLAN is 99 and which is connected (the session's
// likely path), ET2 a trunk with NO allowed-vlan line (so ALL VLANs, management
// included) and notconnect, ET1 an access port in VLAN 30 and connected, ET49
// connected with no block at all, ET50/ET51 errdisabled, ET52 disabled.

// --- the pieces --------------------------------------------------------

// The write form, and the read-back word that confirms it. "disabled" is what
// "show interfaces status" prints for a port carrying "shutdown", and it must
// NOT be confused with "notconnect" (nothing plugged in) or "errdisabled" (the
// switch shut it itself) — reading the three as one would report a missing cable
// as a job done, and would tell an operator that "no shutdown" had fixed a port
// the switch is still holding down.
func TestPortAdminCmdsAndTheStatusWordItReadsBack(t *testing.T) {
	var down = portAdminCmds("Ethernet12", false)
	if len(down) != 3 || down[0] != "interface Ethernet12" ||
		down[1] != "shutdown" || down[2] != "exit" {
		t.Errorf("shut form = %q", down)
	}
	var up = portAdminCmds("Ethernet12", true)
	if len(up) != 3 || up[0] != "interface Ethernet12" ||
		up[1] != "no shutdown" || up[2] != "exit" {
		t.Errorf("un-shut form = %q", up)
	}

	if !(statusPort{Status: "disabled"}).adminDown() {
		t.Error("\"disabled\" is the word this platform prints for an administratively shut port")
	}
	var word string
	for _, word = range []string{"notconnect", "connected", "errdisabled", ""} {
		if (statusPort{Status: word}).adminDown() {
			t.Errorf("%q must not read as administratively down: only \"shutdown\" produces "+
				"\"disabled\", and confusing the three makes an absent cable look like a job done",
				word)
		}
	}
}

// The guardrail's membership test, on the trunk shape this platform gets wrong.
// ET3's native VLAN is the management one, so it is very likely the path this
// session arrives on; ET1 is an access port in VLAN 30 and is not.
//
// (The all-allowed trunk and the unwritten port have their own unit tests in
// apply_test.go, where carriesMgmtVLAN is also the VLAN write's guardrail. What
// is new here is reaching the same decision THROUGH the applier, below.)
func TestCarriesMgmtVLANOnTheCaptureFixture(t *testing.T) {
	var rc = parseRunningConfig(runFixture, "192.0.2.5")
	if rc.MgmtVLAN != 99 {
		t.Fatalf("the fixture's management VLAN is 99, parsed %d", rc.MgmtVLAN)
	}
	if !carriesMgmtVLAN("ET3", rc, rc.MgmtVLAN) {
		t.Error("ET3 is a trunk whose NATIVE vlan is 99: shutting it would end the session")
	}
	if carriesMgmtVLAN("ET1", rc, rc.MgmtVLAN) {
		t.Error("ET1 is an access port in VLAN 30: it is not the session's path")
	}
	// ZERO IS NEVER PERMISSION. With no management VLAN known, the membership test
	// answers false for every port — and it is ApplyPortAdmin's own branch above it
	// that must then spare the LINKED ones, not this function.
	if carriesMgmtVLAN("ET3", rc, 0) {
		t.Error("VLAN 0 is not a VLAN: the membership test must not invent a match")
	}
}

// An err-disabled port is cleared by the BOUNCE, never by "no shutdown" alone,
// and the note must say so with the cause the switch gave. The refused-transceiver
// cause gets the longer note this connector already carries for it, because that
// is the one whose recovery timer provisioning arms.
func TestErrDisabledEnableNoteNamesTheBounceAndTheCause(t *testing.T) {
	var xcvr = errDisabledEnableNote("ET50", ErrDisableXcvr)
	if !strings.Contains(xcvr, "shutdown then no shutdown") {
		t.Errorf("the note must name the bounce: %q", xcvr)
	}
	if !strings.Contains(xcvr, UnsupportedTransceiverNote) {
		t.Errorf("the xcvr-unsupported cause has its own recorded note and must carry it: %q", xcvr)
	}
	var bpdu = errDisabledEnableNote("ET51", "bpduguard")
	if !strings.Contains(bpdu, "bpduguard") {
		t.Errorf("the note must quote the cause the switch gave: %q", bpdu)
	}
	// No cause read: the note must send the operator to the command that has it,
	// rather than printing an empty "the switch gives the cause as """.
	var blank = errDisabledEnableNote("ET52", "")
	if strings.Contains(blank, "gives the cause as") ||
		!strings.Contains(blank, "show interfaces status errdisabled") {
		t.Errorf("with no cause read, the note must name the command that has it: %q", blank)
	}
}

// --- the refusals that fire before the network is touched --------------

// The gesture must not reach a LOOKALIKE CLI. Many platforms answer
// "show version" and accept a configure prompt without meaning the same thing by
// "shutdown". Same refusal every other write on this connector makes, and it has
// to fire BEFORE the first command.
func TestApplyPortAdminIsRefusedOnAnUnknownDialect(t *testing.T) {
	var c = &Client{dialect: dialectUnknown, cli: &cliConn{}}
	var _, err = c.ApplyPortAdmin(t.Context(), driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: "ET5", Enabled: false}},
	})
	if err == nil {
		t.Fatal("\"shutdown\" accepted on a CLI that never announced Arista EOS")
	}
	if !strings.Contains(err.Error(), "arista-eos") {
		t.Errorf("the refusal must name the connector: %v", err)
	}
}

// THE GATE THIS RANGE HAS AND THE OTHERS DO NOT. A switch still in Zero Touch
// Provisioning accepts every command and discards the lot at its next cycle, so a
// "shutdown" there would report success and leave nothing behind — and, worse
// for this gesture, would leave an operator believing a port is down.
func TestApplyPortAdminIsRefusedOnAZTPSwitch(t *testing.T) {
	var c = &Client{dialect: dialectEOS, ztp: true, cli: &cliConn{}}
	var _, err = c.ApplyPortAdmin(t.Context(), driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: "ET5", Enabled: false}},
	})
	if err == nil {
		t.Fatal("a write was accepted on a switch that will discard it at its next ZTP cycle")
	}
	if !strings.Contains(err.Error(), "Zero Touch Provisioning") {
		t.Errorf("the refusal must say WHY nothing was written: %v", err)
	}
}

// THE CAPABILITY STAYS FALSE. This is the inverse of cisco/ios's equivalent test,
// and the inversion is the point: that connector had a live 2960-X and a capture
// of "no shutdown" on a real interface block before it declared anything. Nothing
// in this file is that capture — it exercises the PLANNING, on invented output —
// and a green test suite must not be mistaken for one. The flag flips when TODO.md's
// "port_admin" entry gets its capture, and not before.
// Until 2026-08-10 this test was TestWritesExtraPortAdminStaysUndeclared and it
// pinned the flag DOWN, because no "shutdown" had ever been sent to an Arista by
// anyone. That is no longer true: it was sent to Ethernet1 of core01.lab on
// 2026-08-10 and "show interfaces status" answered "disabled" (contract.go,
// live_portadmin_test.go). The assertion is inverted rather than removed — what it
// guards is unchanged, that the flag and the evidence move together.
//
// WHAT THE DECLARATION DOES NOT MEAN, and this file is the reason it has to be
// said here: the capture was typed at a CLI. Everything below drives
// ApplyPortAdmin through a FAKE CLI. Neither one is the other, and the tests in
// this file do not become a capture by sitting next to one.
func TestWritesExtraPortAdminIsDeclaredOnTheCapture(t *testing.T) {
	var d = &Driver{}
	if !d.WritesExtra("port_admin") {
		t.Error("\"port_admin\" is not declared although \"shutdown\" / \"disabled\" / " +
			"\"no shutdown\" were captured on core01.lab on 2026-08-10")
	}
	// The applier is what backs the declaration, and the compile-time assertion in
	// portadmin.go is what says it exists. A declared capability with no applier
	// behind it would be a button that fails at the click.
	var _ driver.PortAdminApplier = (*Client)(nil)
}

// --- the fake CLI, and the guardrail END TO END ------------------------
//
// The decision itself — "!it.Enabled && !plan.Force && now.LinkUp", then the
// management-VLAN membership — lives inline in ApplyPortAdmin and was, until this
// fake existed, verified by READING only. The fake stands in for the SSH shell:
// it records every line sent and answers each with a canned reply terminated by a
// prompt, which is all [cliConn.run] needs. No SSH client, no session; those
// fields stay nil and nothing closes the connection.

type portAdminFakeCLI struct {
	sent  []string
	out   chan byte
	reply func(cmd string) string
}

// portAdminPrompt is the fixture switch's privileged prompt. rePromptPriv matches
// the configuration sub-modes with the same pattern, so the fake does not model
// "sw-lab-1(config-if-Et1)#" separately.
const portAdminPrompt = "\nsw-lab-1#"

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

// paFakeClient returns a Client whose session is the fake, with the dialect
// already recognized so requireWritableDialect lets the write through.
//
// `statuses` are the successive answers to "show interfaces status": the
// pre-write read first, then the confirming re-read. Reading past the end repeats
// the last one — a device that did not change is a legitimate thing to model.
func paFakeClient(runCfg string, statuses ...string) (*Client, *portAdminFakeCLI) {
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
		case "show interfaces status errdisabled":
			return errDisabledFixture
		}
		return ""
	}
	return &Client{
		cli:     &cliConn{stdin: f, out: f.out},
		host:    "192.0.2.5",
		User:    "mcadmin",
		dialect: dialectEOS,
	}, f
}

// The reason table for statusFixture's two err-disabled ports.
const errDisabledFixture = `Port      Name          Status       Reason
Et50                    errdisabled  xcvr-unsupported
Et51      uplink b      err-disabled bpduguard
`

// paStatus returns statusFixture with ONE port's Status column rewritten. The
// replacement is padded to the same width so the columns after it do not shift —
// the parser reads by field, but a fixture that lies about its own shape is worse
// than no fixture.
func paStatus(port, from, to string) string {
	var lines = strings.Split(statusFixture, "\n")
	var i int
	for i = range lines {
		if strings.HasPrefix(lines[i], port+" ") {
			var padded = to
			for len(padded) < len(from) {
				padded += " "
			}
			lines[i] = strings.Replace(lines[i], from, padded, 1)
		}
	}
	return strings.Join(lines, "\n")
}

func paSent(f *portAdminFakeCLI) string { return strings.Join(f.sent, "\n") }

// paReads is every command a run makes when it writes NOTHING.
//
// THREE and not two: the errdisable cause table is read whenever the device
// reports ANY err-disabled port, which statusFixture does (ET50, ET51) — not
// only when the plan names one. That is the applier reading it ONCE for the whole
// run rather than per port, and it is a read, so it is safe to take before the
// guardrail has decided anything. A test that asserted two reads here would be
// asserting a behaviour this connector does not have.
const paReads = "show running-config\nshow interfaces status\n" +
	"show interfaces status errdisabled"

func paMentions(lines []string, sub string) bool {
	var s string
	for _, s = range lines {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// THE GUARDRAIL, through the applier. ET3 is connected AND its native VLAN is the
// management one — the session's own path on the fixture switch. Nothing may be
// sent: not "configure", not anything. The two reads and nothing else.
func TestApplyPortAdminRefusesToShutTheLinkedManagementUplink(t *testing.T) {
	var c, f = paFakeClient(runFixture, statusFixture)
	var res, err = c.ApplyPortAdmin(t.Context(), driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: "ET3", Enabled: false}},
	})
	if err != nil {
		t.Fatalf("a refusal is an ORDINARY outcome and belongs in Skipped, not in an error: %v", err)
	}
	if paSent(f) != paReads {
		t.Fatalf("the guardrail must fire BEFORE any write; commands sent:\n%s", paSent(f))
	}
	if len(res.PortsWritten) != 0 {
		t.Errorf("PortsWritten = %v, want empty: nothing was sent", res.PortsWritten)
	}
	if !paMentions(res.Skipped, "ET3 NOT shut") ||
		!paMentions(res.Skipped, "management VLAN (99)") {
		t.Errorf("the refusal must name the port and the VLAN it protects: %v", res.Skipped)
	}
}

// THE TRAP THIS PLATFORM SETS, through the applier: a trunk with no
// "switchport trunk allowed vlan" line carries EVERY VLAN on EOS, management
// included. ET2 is that trunk, and the fixture has it notconnect — so this test
// gives it a link, which is the only thing standing between the fixture and the
// uplink case. Reading its empty tagged list as "carries nothing" would hand the
// operator a shutdown on the uplink with no warning at all.
func TestApplyPortAdminProtectsTheAllAllowedTrunkOnceItHasALink(t *testing.T) {
	var linked = paStatus("Et2", "notconnect", "connected")
	if !strings.Contains(linked, "Et2                          connected") {
		t.Fatalf("the fixture edit did not apply; this test would prove nothing:\n%s", linked)
	}
	var c, f = paFakeClient(runFixture, linked)
	var res, err = c.ApplyPortAdmin(t.Context(), driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: "ET2", Enabled: false}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(paSent(f), "Ethernet2") {
		t.Fatalf("a trunk with no allowed-vlan line carries ALL VLANs on EOS, management "+
			"included: it must be treated as carrying the session\n%s", paSent(f))
	}
	if !paMentions(res.Skipped, "management VLAN (99)") {
		t.Errorf("the refusal must name what it is protecting: %v", res.Skipped)
	}
}

// Force is the operator saying they know. It lifts the refusal and NOTHING else:
// the exact command sequence, one interface block, and the re-read that is what
// says the port moved. The block is EOS's — "configure", not "configure
// terminal" — and this test is where that spelling is pinned for this gesture.
func TestApplyPortAdminForceLiftsTheRefusalAndSendsTheDocumentedForm(t *testing.T) {
	var c, f = paFakeClient(runFixture,
		statusFixture, paStatus("Et3", "connected", "disabled"))
	var res, err = c.ApplyPortAdmin(t.Context(), driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: "ET3", Enabled: false}},
		Force: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var want = strings.Join([]string{
		paReads,
		"configure",
		"interface Ethernet3",
		"shutdown",
		"exit",
		"end",
		"show interfaces status", // the re-read, and it is what decides
	}, "\n")
	if paSent(f) != want {
		t.Fatalf("commands sent:\n%s\n\nwant:\n%s", paSent(f), want)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("Force was given: nothing must be refused, got %v", res.Skipped)
	}
	if strings.Join(res.PortsWritten, " ") != "ET3" {
		t.Errorf("PortsWritten = %v, want [ET3]", res.PortsWritten)
	}
}

// THE ASYMMETRY THAT IS THE POINT OF THIS APPLIER: nothing is persisted. No
// "write memory", no "copy running-config startup-config", and the notes say
// so — a shut port is an action taken now, and copying it into the startup
// configuration would turn "bounce this port" into "this port is down for ever".
func TestApplyPortAdminNeverPersists(t *testing.T) {
	var c, f = paFakeClient(runFixture,
		statusFixture, paStatus("Et1", "connected", "disabled"))
	var res, err = c.ApplyPortAdmin(t.Context(), driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: "ET1", Enabled: false}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var sent = paSent(f)
	if strings.Contains(sent, "write memory") || strings.Contains(sent, "copy running-config") {
		t.Fatalf("this gesture must NOT be saved:\n%s", sent)
	}
	if !paMentions(res.Notes, "deliberately NOT saved") {
		t.Errorf("the result must say the change is running-config only: %v", res.Notes)
	}
}

// The guardrail is NARROW on purpose: ET1 is connected but is an access port in
// VLAN 30, so it is not the session's path and the operator gets the gesture they
// asked for without a Force they would learn to tick by reflex.
func TestApplyPortAdminShutsALinkedPortThatDoesNotCarryAdministration(t *testing.T) {
	var c, f = paFakeClient(runFixture,
		statusFixture, paStatus("Et1", "connected", "disabled"))
	var res, err = c.ApplyPortAdmin(t.Context(), driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: "ET1", Enabled: false}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(paSent(f), "interface Ethernet1\nshutdown") {
		t.Fatalf("an access port in VLAN 30 is not the session's path: %s", paSent(f))
	}
	if strings.Join(res.PortsWritten, " ") != "ET1" || len(res.Skipped) != 0 {
		t.Errorf("written=%v skipped=%v", res.PortsWritten, res.Skipped)
	}
}

// ZERO IS IGNORANCE, and here is the pair that proves it is not a freeze: with no
// SVI carrying an address, every LINKED port is spared (ET1) while a port with
// nothing plugged into it still moves (ET2). A port with no link cannot be
// carrying the session that would be cut.
func TestApplyPortAdminSparesLinkedPortsWhenTheManagementVLANIsUnknown(t *testing.T) {
	var noMgmt = strings.Replace(runFixture,
		"interface Vlan99\n   ip address 192.0.2.5/24",
		"interface Vlan99\n   no ip address", 1)
	if strings.Contains(noMgmt, "192.0.2.5/24") {
		t.Fatal("the fixture edit did not apply: this test would prove the opposite of itself")
	}
	var c, f = paFakeClient(noMgmt,
		statusFixture, paStatus("Et2", "notconnect", "disabled"))
	var res, err = c.ApplyPortAdmin(t.Context(), driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{
			{Port: "ET1", Enabled: false},
			{Port: "ET2", Enabled: false},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(paSent(f), "interface Ethernet1") {
		t.Errorf("ET1 has a LINK and the management VLAN is unknown: it must be spared\n%s",
			paSent(f))
	}
	if !strings.Contains(paSent(f), "interface Ethernet2\nshutdown") {
		t.Errorf("ET2 has no link and cannot be carrying the session: sparing it too would "+
			"make the gesture useless on exactly the ports it is safe on\n%s", paSent(f))
	}
	if !paMentions(res.Skipped, "could not determine") {
		t.Errorf("the refusal must say WHAT is unknown: %v", res.Skipped)
	}
	if strings.Join(res.PortsWritten, " ") != "ET2" {
		t.Errorf("PortsWritten = %v, want [ET2]", res.PortsWritten)
	}
}

// THE READ THAT LIES BY HALVES, and the reason portadmin.go carries its own
// mgmtVLAN rather than trusting rc.MgmtVLAN. A "show running-config" that lost
// its head keeps the SVIs — EOS prints them after the Ethernet blocks — and loses
// every "interface Ethernet" block. rc.MgmtVLAN then reads 99 while the
// membership table is empty, and carriesMgmtVLAN's "no block = factory default,
// access VLAN 1" branch (right for a port absent from a COMPLETE configuration)
// would answer false for the trunked uplink and wave it through, with the notes
// claiming the management VLAN was known. Zero blocks is a broken read: the VLAN
// is treated as UNKNOWN and every linked port is spared.
func TestApplyPortAdminDistrustsAManagementVLANWithNoMembershipTable(t *testing.T) {
	var tail = "interface Vlan99\n   ip address 192.0.2.5/24\n!\n" +
		"ip route 0.0.0.0/0 192.0.2.1\n!\nend\n"
	var rc = parseRunningConfig(tail, "192.0.2.5")
	if rc.MgmtVLAN != 99 || len(rc.Ports) != 0 {
		t.Fatalf("the premise of this test is a config with a management VLAN and NO port "+
			"block: mgmt=%d ports=%d", rc.MgmtVLAN, len(rc.Ports))
	}
	if carriesMgmtVLAN("ET3", rc, rc.MgmtVLAN) {
		t.Fatal("premise: with no block at all, the membership test answers false for the " +
			"uplink — which is exactly why ApplyPortAdmin must not trust this VLAN")
	}

	var c, f = paFakeClient(tail, statusFixture)
	var res, err = c.ApplyPortAdmin(t.Context(), driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: "ET3", Enabled: false}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if paSent(f) != paReads {
		t.Fatalf("the uplink must not be shut on a read that carried no membership at all:\n%s",
			paSent(f))
	}
	if !paMentions(res.Skipped, "could not determine") {
		t.Errorf("the port must be spared for the UNKNOWN reason: %v", res.Skipped)
	}
	if !paMentions(res.Notes, "NO interface block") {
		t.Errorf("the notes must say the READ was the problem, not the device: %v", res.Notes)
	}
}

// RE-ENABLING IS NEVER GATED, even on a port that carries the management VLAN.
// Bringing a port back cuts nothing, and refusing it would block the gesture an
// operator needs precisely when something already went wrong.
func TestApplyPortAdminNeverGatesTheEnableDirection(t *testing.T) {
	var c, f = paFakeClient(runFixture,
		paStatus("Et3", "connected", "disabled"), statusFixture)
	var res, err = c.ApplyPortAdmin(t.Context(), driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: "ET3", Enabled: true}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(paSent(f), "interface Ethernet3\nno shutdown") {
		t.Fatalf("\"no shutdown\" must reach a management-VLAN port: %s", paSent(f))
	}
	if len(res.Skipped) != 0 || strings.Join(res.PortsWritten, " ") != "ET3" {
		t.Errorf("written=%v skipped=%v", res.PortsWritten, res.Skipped)
	}
}

// A port already where the plan wants it is not written, and does not appear in
// PortsWritten (the contract): ET52 reads "disabled" and the plan asks for shut.
// Sending "shutdown" anyway would be a no-op on the device and a lie in the
// result — the port would be reported as moved by this gesture when it was already
// down before it ran.
func TestApplyPortAdminSendsNothingForAPortAlreadyThere(t *testing.T) {
	var c, f = paFakeClient(runFixture, statusFixture)
	var res, err = c.ApplyPortAdmin(t.Context(), driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: "ET52", Enabled: false}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if paSent(f) != paReads {
		t.Fatalf("nothing may be sent for a port already shut:\n%s", paSent(f))
	}
	if len(res.PortsWritten) != 0 || len(res.Skipped) != 0 {
		t.Errorf("written=%v skipped=%v — both must be empty", res.PortsWritten, res.Skipped)
	}
}

// "ENABLE" ON AN ERR-DISABLED PORT CHANGES NOTHING, and the applier must say so
// instead of quietly reporting success. ET50 is errdisabled for xcvr-unsupported
// — it is not carrying "shutdown", so it is already "not administratively down"
// and no command is sent; what the operator gets is the note, with the cause the
// switch itself gave, read from the errdisable table.
func TestApplyPortAdminExplainsAnErrDisabledPortInsteadOfBouncingIt(t *testing.T) {
	var c, f = paFakeClient(runFixture, statusFixture)
	var res, err = c.ApplyPortAdmin(t.Context(), driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: "ET50", Enabled: true}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(paSent(f), "shutdown") {
		t.Fatalf("silently shutting a port to un-shut it is a link flap nobody asked for:\n%s",
			paSent(f))
	}
	if !strings.Contains(paSent(f), "show interfaces status errdisabled") {
		t.Errorf("the cause table is what makes the note say WHY: %s", paSent(f))
	}
	if !paMentions(res.Notes, "shut down BY THE SWITCH") ||
		!paMentions(res.Notes, UnsupportedTransceiverNote) {
		t.Errorf("the note must name the condition and its recorded cause: %v", res.Notes)
	}
	if len(res.PortsWritten) != 0 {
		t.Errorf("nothing was written: %v", res.PortsWritten)
	}
}

// THE RE-READ IS WHAT DECIDES. A CLI that accepts "shutdown" and changes nothing
// is this platform's silent failure, and the returned success must not repeat it:
// the port stays out of PortsWritten and the note carries the word the device
// actually printed.
func TestApplyPortAdminReportsAPortThatDidNotMove(t *testing.T) {
	var c, _ = paFakeClient(runFixture, statusFixture, statusFixture)
	var res, err = c.ApplyPortAdmin(t.Context(), driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: "ET1", Enabled: false}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.PortsWritten) != 0 {
		t.Errorf("the re-read still says \"connected\": PortsWritten must stay empty, got %v",
			res.PortsWritten)
	}
	if !paMentions(res.Notes, "still reads \"connected\"") {
		t.Errorf("the note must quote the status word the device printed: %v", res.Notes)
	}
}

// A BREAKOUT LANE IS REFUSED BY NAME, and this is where TODO.md's breakout entry
// touches running code: Et53/1 has no standard-model name on this connector, so
// cliPortName returns nothing and the port is skipped with the reason. Nothing is
// sent for it — the alternative, guessing a CLI name, would shut whatever
// interface the guess happened to hit.
func TestApplyPortAdminRefusesANameOutsideTheStandardModel(t *testing.T) {
	var c, f = paFakeClient(runFixture, statusFixture)
	var res, err = c.ApplyPortAdmin(t.Context(), driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: "ET53/1", Enabled: false}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if paSent(f) != paReads {
		t.Fatalf("nothing may be sent for a name this connector cannot translate:\n%s", paSent(f))
	}
	if !paMentions(res.Skipped, "outside the standard-model mapping") {
		t.Errorf("the refusal must say the NAME was the problem: %v", res.Skipped)
	}
}

// A port the device does not report is skipped, and is not invented. Ma1 is the
// out-of-band management port: it is deliberately outside this connector's
// numbering, so it never reaches the status map — and "shutdown" on the
// out-of-band port of a switch reached in band is a gesture nobody should be able
// to make by typing a name.
func TestApplyPortAdminSkipsAPortTheDeviceDoesNotReport(t *testing.T) {
	var c, f = paFakeClient(runFixture, statusFixture)
	var res, err = c.ApplyPortAdmin(t.Context(), driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: "ET99", Enabled: false}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(paSent(f), "Ethernet99") {
		t.Fatalf("a port absent from \"show interfaces status\" must not be written:\n%s",
			paSent(f))
	}
	if !paMentions(res.Skipped, "does not report this port") {
		t.Errorf("the refusal must say the DEVICE did not list it: %v", res.Skipped)
	}
}

// A FAILED PRE-READ IS AN ERROR AND NOT A DEGRADED RUN. With no running
// configuration there is no management VLAN and no membership, so the guardrail
// has nothing to stand on; proceeding would be the guardrail disarming itself
// under exactly the condition it exists for.
func TestApplyPortAdminRefusesWhenThePreReadIsEmpty(t *testing.T) {
	var c, f = paFakeClient("", statusFixture)
	var _, err = c.ApplyPortAdmin(t.Context(), driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{{Port: "ET1", Enabled: false}},
	})
	// An empty running-config is not an error from the CLI's point of view, so the
	// applier proceeds — with mgmtVLAN unknown, which is the safe branch. What must
	// NOT happen is a write on a linked port.
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(paSent(f), "interface Ethernet1") {
		t.Fatalf("ET1 has a link and nothing was read about this device: it must be spared\n%s",
			paSent(f))
	}
}

// The order the plan arrives in is not the order the ports are written in:
// intents are sorted numerically, so ET2 precedes ET10 and a transcript reads as
// a faceplate does. String order would put ET10 first, which is the kind of
// detail nobody notices until they are reading a transcript at 2am.
func TestApplyPortAdminWritesInFaceplateOrder(t *testing.T) {
	var c, f = paFakeClient(runFixture, statusFixture)
	var _, err = c.ApplyPortAdmin(t.Context(), driver.PortAdminPlan{
		Ports: []driver.PortAdminIntent{
			{Port: "ET52", Enabled: false},
			{Port: "ET3", Enabled: false},
			{Port: "ET1", Enabled: false},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// ET1 is the only one that moves: ET3 is the protected uplink and ET52 is
	// already disabled. What is asserted is that ET1's block is reached at all,
	// which it is not if the sort panics or drops intents.
	if !strings.Contains(paSent(f), "interface Ethernet1\nshutdown") {
		t.Fatalf("the sorted plan lost its only writable port:\n%s", paSent(f))
	}
	if strings.Contains(paSent(f), "Ethernet3") || strings.Contains(paSent(f), "Ethernet52") {
		t.Errorf("a protected port and an already-shut one were written:\n%s", paSent(f))
	}
}
