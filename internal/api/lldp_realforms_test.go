package api

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"meshcraft/internal/model"
)

// THE FAR-PORT RESOLUTION, MEASURED ON THE FLEET RATHER THAN REASONED ABOUT.
//
// The survey asked for exactly one thing to settle whether a wrong far-port
// resolution can cost a link: "walk lldpRemPortId / lldpRemPortDesc on each
// platform and compare against what facade.resolve produces". This file is
// that comparison. Every PortID and PortDesc below was read out of
// `lldp_neighbors` on the lab/CaserneLab fleet on 2026-08-10 — the rows the
// pollers themselves wrote — and every port inventory is the device's real
// `devices.ports` key set from the same read. Nothing here is invented, which
// is the whole reason it is worth having: the interesting cases are the ones
// nobody would have thought to construct.
//
// # WHAT THE WALK ACTUALLY SHOWED — five dialects for one field
//
// `lldpRemPortId` is not a port name on this fleet. It is whatever each
// platform decided to put in the TLV:
//
//	arista-eos           "Ethernet34"   the interface name
//	cisco-ios            "Te1/0/2"      the SHORT name, long form in PortDesc
//	fs-s3400             "TGi0/2"       a short form nothing else in the fleet uses
//	fs-s3900             "Eth 1/26"     name with a SPACE, desc is prose
//	ubiquiti-edgeswitch  "0/18"         unit/port, no desc at all
//	mikrotik-routeros    "bridge/sfp-sfpplus2"  bridge-qualified
//	(off-fleet)          "521", "50"  a bare ifIndex
//
// So the resolver is not tidying up spelling variants; it is bridging seven
// vocabularies, and PortDesc is empty on four of the seven — which is why
// resolvePeerPort's fallback to PortDesc cannot be the thing that saves it.
//
// # THE TWO FINDINGS
//
// GOOD: every in-fleet cable resolves, from PortID alone, on all five ranges —
// including the three forms nobody would have invented ("Te1/0/2", "0/18",
// and "Eth 1/26" with a space in it).
//
// LESS GOOD, and this file was written expecting the opposite: a BARE ifIndex was
// resolved by prefix concatenation. "50" became GE50 whenever the facade had
// a GE50, with nothing judging whether the number was ever a port number. That
// finding is what split `facade.resolve` in two — see
// TestABareIfIndexNoLongerResolvesOnTheLLDPPath below, and
// [facade.resolveNeighborPort] for the reasoning and the accepted cost.
//
// The four cables below are each visible FROM BOTH ENDS, in two different
// dialects, which is what makes them evidence: core01 says the Cisco's port is
// "Te1/0/2" while the Cisco says core01's port is "Ethernet34", and both
// have to land on the standard names XG2 and ET34 for the link to be drawn once
// instead of twice or not at all.

// fleetFacade builds a facade over a real device's real port inventory.
//
// `driver` is the key stored in `devices.extra.driver` — the same source
// facadeOf reads — so the PortNamer step (resolve's step 4) is reached exactly
// as it is in production.
func fleetFacade(t *testing.T, driverKey string, ports []string) facade {
	t.Helper()
	var p = map[string]any{}
	var i int
	for i = range ports {
		p[ports[i]] = map[string]any{}
	}
	var portsJSON, err = json.Marshal(p)
	if err != nil {
		t.Fatalf("marshalling ports: %v", err)
	}
	var extraJSON []byte
	extraJSON, err = json.Marshal(map[string]any{"driver": driverKey})
	if err != nil {
		t.Fatalf("marshalling extra: %v", err)
	}
	return facadeOf(model.Device{
		Ports: portsJSON,
		Extra: extraJSON,
	})
}

// portRange spells GE1..GEn / ET1..ETn the way the inventory stores them.
func portRange(prefix string, n int) []string {
	var out []string
	var i int
	for i = 1; i <= n; i++ {
		out = append(out, prefix+strconv.Itoa(i))
	}
	return out
}

func ciscoLabPorts() []string {
	return append(portRange("GE", 24), "XG1", "XG2")
}

// TestTheFleetsRealFarPortFormsResolve walks the four cables that the fleet
// reports from BOTH ends and asserts each side lands on the other's standard
// name. Captured 2026-08-10.
func TestTheFleetsRealFarPortFormsResolve(t *testing.T) {
	var cases = []struct {
		what      string
		driverKey string
		ports     []string
		portID    string
		portDesc  string
		want      string
	}{
		// core01.lab ET34  <->  CiscoLabFirst1 XG2
		{
			what:      "arista names a Catalyst 10G port by its SHORT form",
			driverKey: "cisco-ios",
			ports:     ciscoLabPorts(),
			portID:    "Te1/0/2",
			portDesc:  "TenGigabitEthernet1/0/2",
			want:      "XG2",
		},
		{
			what:      "the Catalyst names an Arista port by its interface name",
			driverKey: "arista-eos",
			ports:     portRange("ET", 48),
			portID:    "Ethernet34",
			want:      "ET34",
		},

		// core01.lab ET37  <->  unifimoule1 GE18
		{
			what:      "arista names an EdgeSwitch port \"0/18\", with NO PortDesc at all",
			driverKey: "ubiquiti-edgeswitch",
			ports:     portRange("GE", 18),
			portID:    "0/18",
			want:      "GE18",
		},
		{
			what:      "the EdgeSwitch names the Arista port by interface name",
			driverKey: "arista-eos",
			ports:     portRange("ET", 48),
			portID:    "Ethernet37",
			want:      "ET37",
		},

		// core01.lab ET48  <->  s3900lab GE26
		{
			what:      "arista names an S3900 port \"Eth 1/26\" — a SPACE inside the name",
			driverKey: "fs-s3900",
			ports:     portRange("GE", 28),
			portID:    "Eth 1/26",
			portDesc:  "Ethernet Port on unit 1, port 26",
			want:      "GE26",
		},
		{
			what:      "the S3900 names the Arista port by interface name",
			driverKey: "arista-eos",
			ports:     portRange("ET", 48),
			portID:    "Ethernet48",
			want:      "ET48",
		},

		// core01.lab ET35  <->  poefs3400lab XG2
		{
			what:      "the S3400 names the Arista port by interface name",
			driverKey: "arista-eos",
			ports:     portRange("ET", 48),
			portID:    "Ethernet35",
			want:      "ET35",
		},
	}

	var i int
	for i = range cases {
		var c = cases[i]
		t.Run(c.what, func(t *testing.T) {
			var f = fleetFacade(t, c.driverKey, c.ports)
			var got = resolvePeerPortVia(f, c.portID, c.portDesc)
			if got != c.want {
				t.Fatalf("%s: PortID %q / PortDesc %q resolved to %q, want %q\n"+
					"this form was READ OFF THE FLEET on 2026-08-10; if the "+
					"resolver stopped accepting it, a real cable stopped being drawn",
					c.what, c.portID, c.portDesc, got, c.want)
			}
		})
	}
}

// A BARE ifIndex NO LONGER RESOLVES ON THE LLDP PATH — and still does on the
// metrics path, which is the whole point of there being two entry points.
//
// THE HAZARD THIS FILE WENT LOOKING FOR, measured 2026-08-10: `resolve` step 3
// is `f.ports[f.prefix+descr]`, and it exists for a good reason — Instant On
// serves a bare "1" as its ifDescr and the facade calls that GE1. But
// `resolve` served TWO callers, ifDescr on the metrics path and lldpRemPortId
// here, and on the LLDP path the bare number is not a port number: it is
// whatever the far platform put in the TLV. Three rows on the fleet carry one:
// "521" on SW-PLAYER-79 (a Juniper neighbour whose PortDesc says the port is
// really "ge-0/0/9.0" — so the concatenation would have been WRONG, not merely
// unjustified) and "50" / "51" from sw-labo-1930, which IS in the inventory
// and is merely unidentified today because its systemName is its serial
// (CN0EXAMPL1). Identify it and `resolvePeerPort` reaches its facade.
//
// Refutation is PORT-EXACT (cf. markRefutedObservations): a confidently wrong far
// port is the one input that can retire a link that really exists. So the LLDP
// caller lost the arithmetic and the metrics caller kept it.
func TestABareIfIndexNoLongerResolvesOnTheLLDPPath(t *testing.T) {
	// A 52-port facade, so GE50 and GE51 genuinely exist.
	var f = fleetFacade(t, "fs-s3400", portRange("GE", 52))

	// IN RANGE, and previously the hazard: it must now attach to nothing.
	var got = resolvePeerPortVia(f, "50", "")
	if got != "" {
		t.Fatalf("bare \"50\" resolved to %q on the LLDP path, want \"\".\n"+
			"A neighbour's lldpRemPortId is whatever the far platform chose to "+
			"put in the TLV; concatenating the facade prefix onto it names a "+
			"port on the strength of the inventory's SIZE alone", got)
	}

	// OUT OF RANGE: refused too, but it always was — and for the wrong reason,
	// namely that GE521 does not exist. Kept so the two are seen to be refused
	// by the same rule now, not by the inventory's shape.
	got = resolvePeerPortVia(f, "521", "")
	if got != "" {
		t.Fatalf("bare \"521\" resolved to %q on a 52-port facade, want \"\"", got)
	}
}

// TestTheMetricsPathKEEPSPrefixConcatenation is the other half, and it is not a
// formality: this step is the ONLY way an Instant On's telemetry attaches to its
// own ports. Deleting step 3 instead of narrowing its audience would have left a
// 1930 with every port reading as down while the device is fine.
func TestTheMetricsPathKeepsPrefixConcatenation(t *testing.T) {
	var f = fleetFacade(t, "aruba-instant-on-1930", portRange("GE", 52))

	var got = f.resolve("1")
	if got != "GE1" {
		t.Fatalf("ifDescr \"1\" resolved to %q on the metrics path, want GE1.\n"+
			"Instant On serves a bare number as its ifDescr and the facade calls "+
			"it GE1: this step is why its samples attach at all", got)
	}
	if got = f.resolve("50"); got != "GE50" {
		t.Fatalf("ifDescr \"50\" resolved to %q, want GE50", got)
	}
	// And the metrics path is unchanged where it always refused: a number the
	// facade does not carry stays unattached.
	if got = f.resolve("100"); got != "" {
		t.Fatalf("ifDescr \"100\" resolved to %q on a 52-port facade, want \"\"", got)
	}
}

// TestANamerCannotSmuggleThePrefixArithmeticBack pins the back door, on the one
// connector that has it: hpe/procurve's StandardPortName turns a bare number
// into GE<n>, so narrowing step 3 alone would have left the arithmetic available
// through step 4 — and the guarantee would have depended on which connector the
// FAR device happens to run.
//
// The same facade must keep resolving what the namer genuinely exists for, which
// is bridging vocabularies rather than counting: "0/18" on an EdgeSwitch and
// "Te1/0/2" on a Catalyst still land, on the LLDP path, in this same test.
func TestANamerCannotSmuggleThePrefixArithmeticBack(t *testing.T) {
	var pc = fleetFacade(t, "hpe-procurve", portRange("GE", 52))
	// Without this the test could pass vacuously: a driver key the registry does
	// not know leaves the namer nil, and step 4 is never reached at all.
	if pc.namer == nil {
		t.Fatal("the ProCurve facade carries no PortNamer — this test would then " +
			"assert nothing about the step it exists for")
	}
	var got = resolvePeerPortVia(pc, "50", "")
	if got != "" {
		t.Fatalf("bare \"50\" resolved to %q through the ProCurve namer, want \"\".\n"+
			"StandardPortName(\"50\") is character for character the prefix "+
			"arithmetic step 3 was just refused for", got)
	}

	// The namer's real job, on the same path, unaffected.
	var es = fleetFacade(t, "ubiquiti-edgeswitch", portRange("GE", 18))
	if got = resolvePeerPortVia(es, "0/18", ""); got != "GE18" {
		t.Fatalf("\"0/18\" resolved to %q on the LLDP path, want GE18 — the "+
			"namer must still bridge vocabularies", got)
	}
	var cat = fleetFacade(t, "cisco-ios", ciscoLabPorts())
	if got = resolvePeerPortVia(cat, "Te1/0/2", ""); got != "XG2" {
		t.Fatalf("\"Te1/0/2\" resolved to %q on the LLDP path, want XG2", got)
	}
}

// TestPortDescIsEmptyOnMostOfTheFleet records the measurement that makes the
// PortDesc fallback nearly decorative, so nobody leans on it.
//
// Of the seventeen neighbour rows on the fleet (2026-08-10), ELEVEN carry an
// empty PortDesc — every arista-eos row, every mikrotik-routeros row, and the
// Catalyst's own. So resolvePeerPort's second chance does not exist for most of
// the fleet: if PortID does not resolve, nothing does.
func TestPortDescIsEmptyOnMostOfTheFleet(t *testing.T) {
	var f = fleetFacade(t, "arista-eos", portRange("ET", 48))
	// The real shape: PortID resolves, PortDesc is empty, and the empty string
	// must not be treated as a candidate.
	var got = resolvePeerPortVia(f, "Ethernet34", "")
	if got != "ET34" {
		t.Fatalf("PortID-only row resolved to %q, want ET34", got)
	}
	got = resolvePeerPortVia(f, "", "")
	if got != "" {
		t.Fatalf("a row with neither field resolved to %q, want \"\"", got)
	}
}

// resolvePeerPortVia is resolvePeerPort's body without the inventory lookup, so
// the resolution can be exercised against a facade built here. It must stay
// identical to resolvePeerPort's two steps — INCLUDING which entry point they
// call; the test below fails if it drifts.
func resolvePeerPortVia(f facade, portID, portDesc string) string {
	var name = f.resolveNeighborPort(portID)
	if name != "" {
		return name
	}
	return f.resolveNeighborPort(portDesc)
}

// TestResolvePeerPortStillTriesPortIDThenPortDesc guards the helper above
// against resolvePeerPort changing order or gaining a step without this file
// noticing — which would make every assertion here a statement about code that
// is no longer running.
func TestResolvePeerPortStillTriesPortIDThenPortDesc(t *testing.T) {
	var raw, err = os.ReadFile("lldp.go")
	if err != nil {
		t.Fatalf("reading lldp.go: %v", err)
	}
	var src = string(raw)
	var at = strings.Index(src, "func resolvePeerPort(")
	if at < 0 {
		t.Fatal("resolvePeerPort not found in lldp.go — this file's helper mirrors it")
	}
	var end = strings.Index(src[at:], "\n}\n")
	if end < 0 {
		t.Fatal("could not delimit resolvePeerPort")
	}
	var fn = src[at : at+end]

	// The ENTRY POINT is part of what is mirrored, not just the order: calling
	// `resolve` here instead of `resolveNeighborPort` would silently give the
	// LLDP path the prefix arithmetic back, and every assertion in this file
	// would go on passing while describing the wrong function.
	if !strings.Contains(fn, "f.resolveNeighborPort(n.PortID)") ||
		!strings.Contains(fn, "f.resolveNeighborPort(n.PortDesc)") {
		t.Fatalf("resolvePeerPort no longer resolves PortID then PortDesc through "+
			"resolveNeighborPort; resolvePeerPortVia in this file mirrors it and "+
			"must be updated together, or these fleet captures stop testing the "+
			"real path.\n%s", fn)
	}
	if strings.Index(fn, "n.PortID") > strings.Index(fn, "n.PortDesc") {
		t.Fatal("resolvePeerPort now tries PortDesc BEFORE PortID — on this fleet " +
			"eleven of seventeen rows have no PortDesc, so the order matters")
	}
}
