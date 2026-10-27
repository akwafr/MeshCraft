package ios

// Trap-destination logic is proven on states, not on hardware (house style —
// cf. apply_test.go). What the device DOES with the accepted forms was
// exercised live on the lab WS-C2960X-24TD-L (2026-08-05): pose, re-read,
// removal, re-read — the transcript lives with the TODO entry's history.

import (
	"testing"

	"meshcraft/internal/driver"
)

// A running-config excerpt with every recipient shape the parser must hold
// apart: ours, a stale one toward the same collector, a third party's, an
// informs line, and a non-default port.
const trapRunConfig = `!
hostname Switch
!
snmp-server community mc-poll RO
snmp-server enable traps snmp authentication linkdown linkup coldstart warmstart
snmp-server host 10.42.0.67 version 2c mc-poll
snmp-server host 10.42.0.67 version 2c old-rotated-community
snmp-server host 10.42.0.67 legacy-v1
snmp-server host 192.0.2.9 version 2c nagios udp-port 1620
snmp-server host 192.0.2.10 informs version 2c opsview
!
end
`

func TestParseTrapReceiversWholeLines(t *testing.T) {
	var rc = parseRunningConfig(trapRunConfig, "")
	if len(rc.TrapReceivers) != 5 {
		t.Fatalf("receivers=%v", rc.TrapReceivers)
	}
	var ours = rc.TrapReceivers[0]
	if ours.Host != "10.42.0.67" || ours.Version != "2c" || ours.Community != "mc-poll" ||
		ours.Port != 0 || ours.Informs {
		t.Errorf("our line misread: %+v", ours)
	}
	// The v1 line: version ABSENT in the config means v1, and it must stay
	// empty rather than be rounded — emptiness is what decides a rewrite.
	var v1 = rc.TrapReceivers[2]
	if v1.Version != "" || v1.Community != "legacy-v1" {
		t.Errorf("v1 line misread: %+v", v1)
	}
	// udp-port must be read: a recipient on another port is NOT the same
	// recipient, and treating it as one would rewrite a third party's line.
	var nagios = rc.TrapReceivers[3]
	if nagios.Port != 1620 || nagios.Community != "nagios" {
		t.Errorf("udp-port line misread: %+v", nagios)
	}
	if !rc.TrapReceivers[4].Informs {
		t.Errorf("informs line misread: %+v", rc.TrapReceivers[4])
	}
	// The event keywords, lower-cased, whole.
	if !trapLinkEventsCovered(rc.SNMPTrapEvents) {
		t.Errorf("events misread: %v", rc.SNMPTrapEvents)
	}
}

func TestParseTrapReceiverTolerablesShapes(t *testing.T) {
	// vrf clause skipped, "traps" keyword accepted, v3 security word absorbed.
	var r = parseTrapReceiver("10.0.0.1", "vrf mgmt traps version 3 priv netops udp-port 10162")
	if r.Host != "10.0.0.1" || r.Version != "3" || r.Community != "netops" || r.Port != 10162 {
		t.Errorf("misread: %+v", r)
	}
}

func TestTrapEntryConformance(t *testing.T) {
	var want = driver.TrapDestination{Address: "10.42.0.67", Community: "mc-poll"}
	var rc = parseRunningConfig(trapRunConfig, "")

	var conforming = 0
	var r trapReceiver
	for _, r = range rc.TrapReceivers {
		if trapEntryConforms(r, want) {
			conforming++
		}
	}
	if conforming != 1 {
		t.Fatalf("exactly the first line conforms, got %d", conforming)
	}

	// Port 0 in the intent and 162 written on the device are the SAME
	// recipient: IOS omits "udp-port" at the default, and reading that
	// omission as divergence would rewrite an exact device forever.
	if !trapEntryConforms(trapReceiver{Host: "h", Version: "2c", Community: "c", Port: 162},
		driver.TrapDestination{Address: "h", Community: "c", Port: 0}) {
		t.Error("default port must normalize")
	}
	// An empty version is v1: same address, same community, NOT conforming —
	// a v1 trap does not carry the fields a v2c collector reads.
	if trapEntryConforms(trapReceiver{Host: "h", Community: "c"},
		driver.TrapDestination{Address: "h", Community: "c"}) {
		t.Error("a version-less (v1) line must not read as conforming")
	}
	// An informs line retries into a void the collector never acknowledges.
	if trapEntryConforms(trapReceiver{Host: "h", Version: "2c", Community: "c", Informs: true},
		driver.TrapDestination{Address: "h", Community: "c"}) {
		t.Error("an informs line must not read as conforming")
	}
}

func TestStaleAndThirdPartySplit(t *testing.T) {
	var want = driver.TrapDestination{Address: "10.42.0.67", Community: "mc-poll"}
	var rc = parseRunningConfig(trapRunConfig, "")

	// Stale = OUR address, wrong identity: the rotated community and the v1
	// line. The third parties (192.0.2.x) must never appear here — removing
	// them would cut someone else's supervision.
	var stale = staleTrapEntries(rc.TrapReceivers, want)
	if len(stale) != 2 {
		t.Fatalf("stale=%+v", stale)
	}
	if stale[0].Community != "old-rotated-community" || stale[1].Community != "legacy-v1" {
		t.Errorf("stale picked the wrong lines: %+v", stale)
	}
	if thirdPartyTrapCount(rc.TrapReceivers, want) != 2 {
		t.Errorf("third-party count wrong")
	}
}

func TestTrapCommandForms(t *testing.T) {
	var want = driver.TrapDestination{Address: "10.42.0.67", Community: "mc-poll"}
	var forms = trapHostCandidates(want)
	if forms[0][0] != "snmp-server host 10.42.0.67 version 2c mc-poll" {
		t.Errorf("primary form: %q", forms[0][0])
	}
	if forms[1][0] != "snmp-server host 10.42.0.67 traps version 2c mc-poll" {
		t.Errorf("fallback form: %q", forms[1][0])
	}
	// A non-default port is WRITTEN, not refused: IOS has the field.
	want.Port = 10162
	if trapHostLine(want, false) != "snmp-server host 10.42.0.67 version 2c mc-poll udp-port 10162" {
		t.Errorf("udp-port form: %q", trapHostLine(want, false))
	}

	// Removal names the line as it stands — "no" on IOS wants the full
	// spelling first — and only then degrades toward the bare-address form,
	// which is safe solely because every entry given here names OUR collector.
	var rm = trapRemoveCandidates(trapReceiver{Host: "10.42.0.67", Version: "2c",
		Community: "old", Port: 1620})
	if rm[0][0] != "no snmp-server host 10.42.0.67 version 2c old udp-port 1620" {
		t.Errorf("full no-form: %q", rm[0][0])
	}
	if rm[2][0] != "no snmp-server host 10.42.0.67" {
		t.Errorf("last-resort no-form: %q", rm[2][0])
	}
	var rmV1 = trapRemoveCandidates(trapReceiver{Host: "10.42.0.67", Community: "legacy"})
	if rmV1[0][0] != "no snmp-server host 10.42.0.67 legacy" {
		t.Errorf("v1 no-form must not invent a version clause: %q", rmV1[0][0])
	}

	// The event switch: additive per-event keywords first, whole-class second.
	var ev = trapEnableCandidates()
	if ev[0][0] != "snmp-server enable traps snmp linkdown linkup coldstart warmstart" {
		t.Errorf("event form: %q", ev[0][0])
	}
	if ev[1][0] != "snmp-server enable traps snmp" {
		t.Errorf("event fallback: %q", ev[1][0])
	}
}

func TestTrapLinkEventsCovered(t *testing.T) {
	if trapLinkEventsCovered([]string{"authentication", "coldstart"}) {
		t.Error("coldstart alone must not read as covered")
	}
	if !trapLinkEventsCovered([]string{"linkdown", "linkup"}) {
		t.Error("linkdown+linkup is the floor and must suffice")
	}
	if trapLinkEventsCovered([]string{"linkup"}) {
		t.Error("half the pair is not coverage")
	}
}
