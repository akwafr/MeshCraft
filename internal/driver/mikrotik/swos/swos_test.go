package swos

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// The declared capabilities, and above all what SwOS CANNOT DO.
//
// These tests do not paraphrase a structure: each one locks a declaration
// whose reversal would change an operational decision. A wrong `Commit` makes
// one believe in a rollback that does not exist; a `SerialPort` set to true
// promises a recovery console to someone who will have to travel; a reset
// procedure presented as verified when it is inferred is paid for at the worst
// moment, the one when access is already lost.

// TestCommitImmediateWithoutSafetyNet: the heaviest constraint of this
// connector.
//
// The device's UI has no "Save Configuration": its only "Discard Changes"
// RE-READS the page from the device, i.e. undoes nothing that has gone out.
// What is posted is written, and a reboot will not undo it.
//
// Flipping this declaration to `apply_then_save` would suggest a free rollback
// that does not exist, and the write would stop taking the backup that is the
// only real safety net.
func TestCommitImmediateWithoutSafetyNet(t *testing.T) {
	var caps = (&Driver{}).Capabilities("CSS326-24G-2S+")
	if caps.Commit != driver.CommitImmediate {
		t.Errorf("Commit = %q; SwOS persists in a single gesture, there is no "+
			"separate startup configuration and therefore no free way back",
			caps.Commit)
	}
	// A single session: SwOS has no notion of a session, and parallelism has
	// not been verified on hardware in service.
	if caps.MaxSessions != 1 {
		t.Errorf("MaxSessions = %d; parallelism could not be tested, so the "+
			"connector must serialise its accesses", caps.MaxSessions)
	}
}

// TestNoCLINorConsole: what makes every guard necessary.
//
// On the reference device, a single TCP port answers: 80. Adoption over a
// serial console — one of discovery's three entry points — is therefore
// IMPOSSIBLE, and a management addressing mistake is fixed on site.
func TestNoCLINorConsole(t *testing.T) {
	var caps = (&Driver{}).Capabilities("CSS326-24G-2S+")
	var f string
	for _, f = range []string{"SSHMgmt", "TelnetMgmt", "SerialCLI", "SerialPort"} {
		if caps.Has(f) {
			t.Errorf("%s declared available: on the reference device, neither 22, "+
				"nor 23, nor a serial port", f)
		}
	}
	if !caps.Has("HTTPMgmt") {
		t.Error("the web UI is the ONLY administration path")
	}
	// The declared transport: "web" AND "snmp", and nothing else.
	//
	// THIS ASSERTION CHANGED SHAPE, DELIBERATELY. It used to require `[web]`
	// alone, on the grounds that "advertising SNMP without having seen the
	// agent answer would schedule a collection that would never arrive". The
	// reasoning was right and its condition is now MET: the agent answered, in
	// v2c, on the reference device (cf. the survey in SNMPVersions). Keeping the
	// old assertion would have failed the test on a verified fact.
	//
	// What this test really protects was NOT the absence of SNMP — it is the
	// absence of CLI and console, which is this platform's hard fact and the
	// reason for all its write caution. The list is therefore checked by what
	// it must NOT contain, which is more faithful to the intent and will
	// survive the next read channel to be surveyed.
	var transport = (&Driver{}).Info().Transport
	var forbidden = map[string]string{
		"ssh":    "no 22 open on the reference device",
		"telnet": "no 23 open",
		"serial": "this model has no serial connector",
		"api":    "SwOS has no API: its *.b pages ARE its web UI",
	}
	var tr string
	for _, tr = range transport {
		var why, isForbidden = forbidden[tr]
		if isForbidden {
			t.Errorf("Transport advertises %q: %s", tr, why)
		}
	}
	if !slices.Contains(transport, "web") {
		t.Error("Transport must advertise \"web\": it is the only WRITE path")
	}
	if !slices.Contains(transport, "snmp") {
		t.Error("Transport must advertise \"snmp\": the v2c agent was observed on " +
			"the reference device, and collection goes through it")
	}
}

// TestNoRoutingNorClock: what SwOS cannot do, and which must be visible.
func TestNoRoutingNorClock(t *testing.T) {
	var caps = (&Driver{}).Capabilities("CSS326-24G-2S+")
	var absent = []string{
		"StaticRouting", // SwOS does not route at all: that is the fundamental difference
		"SNTP",          // no clock, hence no timestamp possible
		"LLDP",          // no standard neighbour discovery
		"Dot1X", "RADIUS", "VoiceVLAN", "DHCPRelay", "JumboFrames",
		"LoopProtection", "EEE", "RMON", "ARPProtection", "DoSProtection",
	}
	var f string
	for _, f = range absent {
		if caps.Has(f) {
			t.Errorf("%s declared available although no device field "+
				"establishes it", f)
		}
	}
	var present = []string{
		"Trunk8021Q", "PVID", "IGMPSnooping", "LACP", "DHCPSnooping",
		"PortSecurity", "ProtectedPorts", "PortMirroring", "IngressRate",
		"StormControl", "StormControlPort",
	}
	for _, f = range present {
		if !caps.Has(f) {
			t.Errorf("%s is established by a device field and should be "+
				"declared", f)
		}
	}
	// Storm control is PER PORT on SwOS (`srt` is an array), where Instant On
	// can only do it globally. It is a strength of this platform and must not
	// be lost.
	if !caps.Has("StormControlPort") {
		t.Error("`srt` is an array of 26 values: storm control is per port")
	}
	// No routes, no MSTP instances.
	if caps.MaxStaticRoutes != 0 || caps.MSTInstances != 0 {
		t.Errorf("routes = %d, MSTP instances = %d; SwOS has neither",
			caps.MaxStaticRoutes, caps.MSTInstances)
	}
	// RSTP only: the field that displays "STP" is read-only, it is an
	// automatic fallback and not a mode one chooses.
	if !reflect.DeepEqual(caps.STPModes, []string{"rstp"}) {
		t.Errorf("STPModes = %v; the UI offers no protocol selector",
			caps.STPModes)
	}
}

// TestHonestResetProcedure: the factory reset procedure, and how certain it
// is.
//
// `Confirmed` must be FALSE: the software path is observed in the code of the
// device's UI, but the HARDWARE gesture comes from documentation and has not
// been exercised. Since it is precisely the gesture an operator will attempt
// once access is lost — at the worst moment — presenting it as verified would
// be the very mistake this field exists to prevent.
func TestHonestResetProcedure(t *testing.T) {
	var p = (&Driver{}).ResetProcedure("CSS326-24G-2S+")
	if p.Confirmed {
		t.Error("the hardware gesture has not been exercised on this model: Confirmed must " +
			"stay false, and the source must say which of the two halves is established")
	}
	if p.Hardware == "" || p.Software == "" {
		t.Error("both paths must be described")
	}
	// MikroTik's hardware gesture is not a long press on a powered-on
	// device: it requires cutting then restoring power. Saying so avoids a
	// pointless site visit.
	if !strings.Contains(strings.ToLower(p.Hardware), "power") {
		t.Errorf("the hardware gesture must say it goes through the power supply; got: %q",
			p.Hardware)
	}
	// The source must distinguish what is observed from what is inferred.
	var expected string
	for _, expected = range []string{"software", "documentation"} {
		if !strings.Contains(strings.ToLower(p.Source), expected) {
			t.Errorf("the source must mention %q; got: %q", expected, p.Source)
		}
	}
	// And the trap that matters most: no recovery console.
	var joined = strings.ToLower(strings.Join(p.Pitfalls, " "))
	if !strings.Contains(joined, "serial") {
		t.Errorf("the pitfalls must say there is no serial port: %v", p.Pitfalls)
	}
}

// TestManagementNotesCarryTheOrder: the most dangerous capability to assume.
//
// Getting it wrong makes the device unreachable, and this model has no serial
// port. The notes must carry the ORDER of operations and the fields that cut
// access — that is the difference between an apply that works and a site
// visit.
func TestManagementNotesCarryTheOrder(t *testing.T) {
	var mgmt = (&Driver{}).Capabilities("CSS326-24G-2S+").Mgmt
	if !mgmt.TaggedOK {
		t.Error("tagged management is OBSERVED on the reference device: it is " +
			"administered in VLAN 99 while its only port with a link has native VLAN 1")
	}
	if mgmt.VlanIDs != driver.VlanAny {
		t.Errorf("VlanIDs = %q; \"Allow From VLAN\" is free", mgmt.VlanIDs)
	}
	// The upper bound is the VLAN TABLE's (4094), not the `avln` field's
	// (4095): a management VLAN must exist in the table.
	if mgmt.VlanRange != [2]int{1, 4094} {
		t.Errorf("VlanRange = %v, expected [1 4094] — the lower of the two bounds",
			mgmt.VlanRange)
	}
	var joined = strings.ToLower(strings.Join(mgmt.Notes, " "))
	var expected = []string{
		"before", // the order of operations
		"allp",   // the fields that cut access
		"serial", // no recovery console
		"strict", // the VLAN mode that drops frames
		"endian", // the byte order of the address
	}
	var a string
	for _, a = range expected {
		if !strings.Contains(joined, a) {
			t.Errorf("the management notes must talk about %q: it is a documented "+
				"way of locking yourself out", a)
		}
	}
}

// TestSNMPv2cSurveyed: the v2c agent, and the fact that it is NOT a disguised
// v3.
//
// This test replaces one that locked the OPPOSITE — `SNMPVersions` had to be
// empty, "for lack of having seen the agent answer". The reasoning was good and
// its condition has been lifted: the proxy's walk, which speaks hard-coded
// v2c, obtained sysDescr, sysObjectID, sysName and ifNumber from the reference
// device on 29 July 2026. What is locked here is therefore the survey, and its
// ACCURACY — advertising more than what answered would fall back into the
// mistake the old test avoided.
func TestSNMPv2cSurveyed(t *testing.T) {
	var caps = (&Driver{}).Capabilities("CSS326-24G-2S+")
	if !reflect.DeepEqual(caps.SNMPVersions, []string{"v2c"}) {
		t.Errorf("SNMPVersions = %v; only v2c was OBSERVED. v1 is probable but has "+
			"never answered anyone — the project's only query channel is v2c — "+
			"and v3 does not exist on this platform.", caps.SNMPVersions)
	}
	// v3 is an ABSENCE ESTABLISHED by enumeration: /snmp.b fits in four fields,
	// and there is no user, no algorithm, no encryption, in any tab or in the
	// configuration backup. Declaring an algorithm would generate a v3
	// identity nothing could set.
	if len(caps.SNMPv3Auth) != 0 || len(caps.SNMPv3Priv) != 0 {
		t.Errorf("SNMPv3Auth = %v, SNMPv3Priv = %v; SwOS has NO v3",
			caps.SNMPv3Auth, caps.SNMPv3Priv)
	}
}
