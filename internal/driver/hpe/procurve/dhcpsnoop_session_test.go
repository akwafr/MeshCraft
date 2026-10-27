package procurve

// ApplyDHCPSnooping DRIVEN END TO END, through the connector's own shell.
//
// # What this closes, and why the premise it was filed under was wrong
//
// TODO.md said the session-bound branches — the verifying re-read, a refusal
// landing in Skipped, the "written but NOT saved" note — had no unit coverage
// because "Client.cli is a concrete *cliConn", and asked for a cli seam. The
// seam was already there: `cliConn` reaches the device through an
// `io.WriteCloser` and a `chan byte`, both plain fields, and four sibling
// connectors already drive their appliers that way (fs/s3400's snoopDevice,
// fs/s3900, cisco/ios, ubiquiti/edgeswitch). Nothing needed to be added to the
// production type. What was missing was a stand-in that speaks this CLI.
//
// # THE FIXTURE IS CONSTRUCTED, and the distinction matters on this range
//
// `snoopShell` below is an in-process stand-in, not a capture. It reproduces the
// two protocol facts this connector's reader is built around — the CLI echoes
// what it is sent, and output is terminated by a prompt (cf. cli.go's
// waitEchoThenPrompt, and the repainted-prompt bug it documents) — and it serves
// a running configuration assembled from what it was told.
//
// Its value is NOT that it behaves like a 2610. It is that the apply's ORDER,
// its idempotence, its refusals, its verifying re-read and its save can be
// asserted at all. Every claim about what the HARDWARE does still rests on
// testdata/show_running_config.txt and on the hand survey; nothing here changes
// [Driver.WritesExtra], which still answers false for "dhcp_snooping" because
// no 2610 has printed a "dhcp-snooping vlan" line back (cf.
// TestDriverDeclaresOnlyTheExtrasItWrites).

import (
	"context"
	"io"
	"strconv"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// --- the shell stand-in ---------------------------------------------------

// snoopShell speaks the ProCurve CLI well enough for a session to run against
// it: it echoes, it prompts, it interprets the snooping grammar dhcpsnoop.go's
// header documents, and it serves a "show running-config" built from what it
// interpreted.
//
// The knobs are the failure modes the contract cares about, and each is a
// SEPARATE field rather than a mode enum: an apply can meet more than one at
// once, and the interesting cases are exactly the combinations (a refused port
// AND a failed re-read, a write that took AND a save that did not).
type snoopShell struct {
	out chan byte

	// sent is every line the connector wrote, in order — the transcript the
	// order assertions are made against.
	sent []string

	// what the device currently has configured.
	globalOn bool
	vlans    []int
	trusted  map[int]bool

	// refusePorts: port numbers whose "dhcp-snooping trust" is answered with
	// this CLI's refusal shape. The device says "%" and carries on.
	refusePorts map[int]bool
	// swallowTrust: the trust command is ACCEPTED and configures nothing — the
	// silent failure the verifying re-read exists to catch.
	swallowTrust bool
	// dieOnReadBack: the SECOND "show running-config" (the verifying re-read)
	// comes back empty and the session reports it closed. The pre-read still
	// works, so the apply gets as far as writing.
	dieOnReadBack bool
	// shortReadBack: the verifying re-read returns a handful of characters —
	// the CLI desynchronisation snoopConfig refuses to read as a device.
	shortReadBack bool
	// refuseSave: "write memory" is refused. The failure mode that must NOT
	// fail the apply.
	refuseSave bool

	reads int // how many "show running-config" have been answered
	saved int // how many "write memory" were accepted
	// dead: the channel has been closed and the shell answers nothing more.
	// A closed session stays closed — every later command returns "SSH session
	// closed by the device" the moment cli.go reads from it, which is what the
	// applier's remaining steps then have to cope with.
	dead bool

	line  strings.Builder
	iface int
}

const snoopShellPrompt = "SW-TEST# "

func newSnoopShell() (*Client, *snoopShell) {
	var d = &snoopShell{
		out:         make(chan byte, 1<<20),
		trusted:     map[int]bool{},
		refusePorts: map[int]bool{},
	}
	var c = &Client{
		cli: &cliConn{out: d.out, stdin: d},
		tgt: driver.Target{Host: "192.0.2.9"},
	}
	return c, d
}

// Write is the device's side of the wire: the connector's sendLine lands here.
func (d *snoopShell) Write(p []byte) (int, error) {
	var i int
	for i = 0; i < len(p); i++ {
		if p[i] == '\n' || p[i] == '\r' {
			var l = d.line.String()
			d.line.Reset()
			d.handle(l)
			continue
		}
		d.line.WriteByte(p[i])
	}
	return len(p), nil
}

func (d *snoopShell) Close() error { return nil }

// emit puts bytes on the wire the way the device does: the ECHO of the command
// first — which is what cli.go synchronises on — then the output, then a prompt
// on a line of its own.
func (d *snoopShell) emit(cmd, body string) {
	var s = cmd
	if body != "" {
		s += "\n" + body
	}
	s += "\n" + snoopShellPrompt
	var i int
	for i = 0; i < len(s); i++ {
		d.out <- s[i]
	}
}

func (d *snoopShell) handle(raw string) {
	var cmd = strings.TrimSpace(raw)
	d.sent = append(d.sent, cmd)
	if d.dead {
		return
	}
	switch {
	case cmd == "":
		d.emit("", "")
	case cmd == "show running-config":
		d.reads++
		if d.dieOnReadBack && d.reads > 1 {
			// The session goes away mid-read: cli.go returns what it had — nothing
			// — plus "SSH session closed by the device".
			d.dead = true
			close(d.out)
			return
		}
		d.emit(cmd, d.runningConfig())
	case cmd == "configure terminal":
		d.emit(cmd, "")
	case cmd == "exit":
		d.iface = 0
		d.emit(cmd, "")
	case cmd == "write memory":
		if d.refuseSave {
			d.emit(cmd, "% Configuration file could not be written")
			return
		}
		d.saved++
		d.emit(cmd, "")
	case cmd == "dhcp-snooping":
		d.globalOn = true
		d.emit(cmd, "")
	case strings.HasPrefix(cmd, "dhcp-snooping vlan "):
		d.vlans = parseNumberList(strings.TrimPrefix(cmd, "dhcp-snooping vlan "))
		d.emit(cmd, "")
	case strings.HasPrefix(cmd, "interface "):
		var n, err = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(cmd, "interface ")))
		if err != nil {
			d.emit(cmd, "% Invalid input: "+cmd)
			return
		}
		d.iface = n
		d.emit(cmd, "")
	case cmd == "dhcp-snooping trust" || cmd == "no dhcp-snooping trust":
		if d.refusePorts[d.iface] {
			// This CLI reports a refusal in the OUTPUT, never through a status.
			d.emit(cmd, "% Command cannot be performed on port "+strconv.Itoa(d.iface))
			return
		}
		if !d.swallowTrust {
			d.trusted[d.iface] = cmd == "dhcp-snooping trust"
		}
		d.emit(cmd, "")
	default:
		d.emit(cmd, "")
	}
}

// runningConfig serves what the device would print. The header padding is not
// decoration: snoopConfig refuses a read shorter than minRunningConfig as a CLI
// desynchronisation, which is a rule worth honouring rather than working around.
func (d *snoopShell) runningConfig() string {
	if d.shortReadBack && d.reads > 1 {
		return "hostname \"SW-TEST\""
	}
	var b strings.Builder
	b.WriteString("Running configuration:\n\n")
	b.WriteString("; J9088A Configuration Editor; Created on release #R.11.121\n")
	b.WriteString("; Ver #03:10.00.06.01\n\n")
	b.WriteString("hostname \"SW-TEST\"\n")
	b.WriteString("module 1 type J90SSA\n")
	b.WriteString("ip default-gateway 192.0.2.1\n")
	if d.globalOn {
		b.WriteString("dhcp-snooping\n")
	}
	if len(d.vlans) > 0 {
		b.WriteString("dhcp-snooping vlan " + formatPortRange(d.vlans) + "\n")
	}
	var n int
	for n = 1; n <= 52; n++ {
		var t, known = d.trusted[n]
		if !known {
			continue
		}
		b.WriteString("interface " + strconv.Itoa(n) + "\n")
		if t {
			b.WriteString("   dhcp-snooping trust\n")
		} else {
			b.WriteString("   no dhcp-snooping trust\n")
		}
		b.WriteString("   exit\n")
	}
	b.WriteString("vlan 1\n   name \"DEFAULT_VLAN\"\n   untagged 1-52\n   ip address dhcp-bootp\n   exit\n")
	b.WriteString("snmp-server community \"public\" operator\n")
	return b.String()
}

// The stand-in has to be a real io.WriteCloser or cliConn would not take it.
var _ io.WriteCloser = (*snoopShell)(nil)

// The harness is only worth what its protocol is: if the connector cannot read
// this shell at all, every assertion below would pass by never reaching the
// device. Asserted first, and separately.
func TestTheSnoopShellSpeaksThisCLI(t *testing.T) {
	var c, d = newSnoopShell()
	d.globalOn = true
	d.trusted[45] = true
	var before, _, err = c.snoopConfig(context.Background())
	if err != nil {
		t.Fatalf("the connector could not read its own stand-in: %v", err)
	}
	if !before.Enabled || !before.Trusted[45] {
		t.Fatalf("the stand-in served something the parser did not recognise: %+v", before)
	}
}

// --- the verifying re-read ------------------------------------------------

// A trust flag the CLI accepted and the device did not keep must NOT be
// reported as written. This is the branch the whole applier is built around,
// and until now nothing exercised it on this range.
func TestApplySnoopDoesNotReportATrustFlagTheDeviceSwallowed(t *testing.T) {
	var c, d = newSnoopShell()
	d.globalOn = true
	d.swallowTrust = true

	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE45", Trusted: true}},
	})
	if err != nil {
		t.Fatalf("a swallowed write is a result, not a Go error: %v", err)
	}
	if len(res.PortsWritten) != 0 {
		t.Fatalf("PortsWritten = %v: the CLI accepted the command and the configuration "+
			"does not carry it — a return code that changes nothing is exactly what the "+
			"re-read exists to catch", res.PortsWritten)
	}
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0], "GE45") {
		t.Fatalf("Skipped = %v: the disagreement must name the jack", res.Skipped)
	}
	if !strings.Contains(strings.Join(res.Skipped, " "), "absent from the configuration") {
		t.Fatalf("Skipped = %v: the operator has to be told what the device says", res.Skipped)
	}
}

// A flag that DID land is reported, and the save comes after the verification
// and not before it — "write memory" sent first would bake into flash a
// configuration nothing had confirmed.
func TestApplySnoopConfirmsAndThenSaves(t *testing.T) {
	var c, d = newSnoopShell()
	d.globalOn = true

	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE45", Trusted: true}},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(res.PortsWritten) != 1 || res.PortsWritten[0] != "GE45" {
		t.Fatalf("PortsWritten = %v", res.PortsWritten)
	}
	if len(res.Skipped) != 0 {
		t.Fatalf("Skipped = %v on a write the device kept", res.Skipped)
	}
	if d.saved != 1 {
		t.Fatalf("\"write memory\" was sent %d time(s): protection a reboot forgets is "+
			"protection nobody has", d.saved)
	}
	var order = strings.Join(d.sent, "\n")
	var readBack = strings.LastIndex(order, "show running-config")
	var save = strings.Index(order, "write memory")
	if readBack < 0 || save < 0 || save < readBack {
		t.Fatalf("the save did not follow the verifying re-read:\n%s", order)
	}
}

// THE ITEM THIS FILE WAS FILED UNDER, and the one the PortsWritten sweep of
// 2026-08-09 changed: a re-read that could not be TAKEN confirms nothing.
// Before the sweep this branch left every CLI-accepted port in PortsWritten
// under a note saying the opposite.
func TestApplySnoopRetractsEverythingWhenTheReReadFails(t *testing.T) {
	var c, d = newSnoopShell()
	d.globalOn = true
	d.dieOnReadBack = true

	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE45", Trusted: true}},
	})
	if err != nil {
		t.Fatalf("a failed verification is not a failed apply: %v", err)
	}
	if len(res.PortsWritten) != 0 {
		t.Fatalf("PortsWritten = %v after a re-read that never landed — the field means "+
			"CONFIRMED (cf. driver.ApplyResult.PortsWritten)", res.PortsWritten)
	}
	var notes = strings.Join(res.Notes, " | ")
	if !strings.Contains(notes, "GE45") {
		t.Fatalf("Notes = %v: retracting the port without NAMING it loses the only "+
			"record of what went out", res.Notes)
	}
	if !strings.Contains(notes, "re-read the device before sending again") {
		t.Fatalf("Notes = %v: the operator is given no next step", res.Notes)
	}
}

// A SHORT read is the same event as a dead one and must be treated the same
// way: snoopConfig refuses it as a desynchronisation rather than reading it as
// "this switch has no snooping", and the apply must not then report the write
// as confirmed by a configuration it never saw.
func TestApplySnoopTreatsADesynchronisedReadBackAsNoConfirmation(t *testing.T) {
	var c, d = newSnoopShell()
	d.globalOn = true
	d.shortReadBack = true

	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE45", Trusted: true}},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(res.PortsWritten) != 0 {
		t.Fatalf("PortsWritten = %v on a read the connector itself refused to believe",
			res.PortsWritten)
	}
	if !strings.Contains(strings.Join(res.Notes, " "), "desynchronised") {
		t.Fatalf("Notes = %v: the reason has to survive into the result", res.Notes)
	}
}

// --- the refusal path -----------------------------------------------------

// A jack the CLI refuses lands in Skipped for that jack, and the rest of the
// plan still goes out. It must NOT then also appear as "accepted by the CLI
// and absent afterwards": two entries making opposite claims about one socket
// is what the applier's `delete(wanted, …)` exists to prevent.
func TestApplySnoopKeepsARefusedPortOutOfTheVerificationSet(t *testing.T) {
	var c, d = newSnoopShell()
	d.globalOn = true
	d.refusePorts[46] = true

	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{
			{Port: "GE45", Trusted: true},
			{Port: "GE46", Trusted: true},
		},
	})
	if err != nil {
		t.Fatalf("one refused jack must not cost the whole gesture: %v", err)
	}
	if len(res.PortsWritten) != 1 || res.PortsWritten[0] != "GE45" {
		t.Fatalf("PortsWritten = %v: the port the device kept is the one that counts",
			res.PortsWritten)
	}
	var about46 int
	var s string
	for _, s = range res.Skipped {
		if strings.Contains(s, "46") {
			about46++
		}
	}
	if about46 != 1 {
		t.Fatalf("GE46 appears in Skipped %d times: %v — a refusal and an \"accepted then "+
			"absent\" about the same jack are contradictory claims", about46, res.Skipped)
	}
}

// Every change refused is NOT "already carries the intent". A green step under
// a gesture that changed nothing it was asked to change is the report this
// branch exists to avoid.
func TestApplySnoopSaysSoWhenEveryChangeWasRefused(t *testing.T) {
	var c, d = newSnoopShell()
	d.globalOn = true // no global command to send, so only the ports move
	d.refusePorts[45] = true

	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE45", Trusted: true}},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(res.PortsWritten) != 0 {
		t.Fatalf("PortsWritten = %v", res.PortsWritten)
	}
	if d.saved != 0 {
		t.Fatal("flash was written for a gesture that changed nothing")
	}
	var steps = c.Steps
	var last = steps[len(steps)-1]
	if last.OK || !strings.Contains(last.What, "every change was refused") {
		t.Fatalf("last step = %+v: a refused gesture must not read as an idempotent no-op",
			last)
	}
}

// --- the "written but NOT saved" note -------------------------------------

// A failed save does NOT fail the apply: the write really is in the running
// configuration and returning an error would claim otherwise. What it owes is a
// note naming what the operator loses.
func TestApplySnoopSurvivesARefusedSaveAndSaysWhatItCosts(t *testing.T) {
	var c, d = newSnoopShell()
	d.globalOn = true
	d.refuseSave = true

	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE45", Trusted: true}},
	})
	if err != nil {
		t.Fatalf("a refused save must not fail an apply that landed: %v", err)
	}
	if len(res.PortsWritten) != 1 {
		t.Fatalf("PortsWritten = %v: the write DID land in the running configuration",
			res.PortsWritten)
	}
	var notes = strings.Join(res.Notes, " | ")
	if !strings.Contains(notes, "NOT saved") || !strings.Contains(notes, "next reboot") {
		t.Fatalf("Notes = %v: the operator must be told this is lost at the next reboot",
			res.Notes)
	}
}

// --- the switch-wide half -------------------------------------------------

// Turning the feature on affects every port on the box, including ones this
// plan never named, and the applier says so. A snooping enable nobody was
// warned about is how an untrusted uplink starts dropping a site's leases.
func TestApplySnoopWarnsWhenItTurnsTheFeatureOnForTheWholeSwitch(t *testing.T) {
	var c, d = newSnoopShell()
	d.globalOn = false

	var res, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE45", Trusted: true}},
		VLANs: []int{99},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !d.globalOn {
		t.Fatal("the feature was never turned on")
	}
	var notes = strings.Join(res.Notes, " | ")
	if !strings.Contains(notes, "WHOLE") {
		t.Fatalf("Notes = %v: a switch-wide change must not arrive silently", res.Notes)
	}
	// And the VLAN list is the UNION, never the plan on its own — asserted here
	// against what the DEVICE ended up with, not against the command list, which
	// snoopGlobalCmds' own tests already hold.
	if len(d.vlans) != 1 || d.vlans[0] != 99 {
		t.Fatalf("device VLANs = %v", d.vlans)
	}
}

// A global enable the CLI accepted and the configuration does not carry is the
// one failure this contract exists to prevent: trust flags on a device that
// inspects nothing, reported as protection. It is an ERROR, not a note.
func TestApplySnoopFailsLoudlyWhenTheFeatureDidNotComeOn(t *testing.T) {
	// The control: a device that DOES turn it on is not an error, or the
	// assertion below would be measuring the harness.
	var c, _ = newSnoopShell()
	var _, err = c.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE45", Trusted: true}},
	})
	if err != nil {
		t.Fatalf("the stand-in accepted the enable, so this must succeed: %v", err)
	}

	// And a device that accepts "dhcp-snooping" and stays off.
	var stubborn, d = newSnoopShell()
	stubborn.cli = &cliConn{out: d.out, stdin: &stuckGlobalShell{snoopShell: d}}
	var _, offErr = stubborn.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE45", Trusted: true}},
	})
	if offErr == nil {
		t.Fatal("an apply that left the feature OFF was reported as a success: every " +
			"trust flag it just wrote is inert")
	}
	if !strings.Contains(offErr.Error(), "the feature is OFF") {
		t.Fatalf("err = %v — the sentence has to say what is wrong", offErr)
	}
}

// stuckGlobalShell accepts "dhcp-snooping" and never turns it on.
type stuckGlobalShell struct{ *snoopShell }

func (s *stuckGlobalShell) Write(p []byte) (int, error) {
	var n, err = s.snoopShell.Write(p)
	s.snoopShell.globalOn = false
	return n, err
}
