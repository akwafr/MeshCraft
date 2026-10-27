package routeros

// DHCP snooping tests (cf. dhcpsnoop.go, dhcpsnoop_apply.go, and
// driver/dhcpsnoop.go for the contract).
//
// FIXTURE PROVENANCE
//
// The read tests and the planner tests run on REAL CAPTURES: testdata/
// interface_bridge.json and interface_bridge_port.json, collected from the
// reference CRS326-24G-2S+ on RouterOS 7.21beta3 (cf. fixtures_test.go). Those
// two files carry the exact fields this connector claims —
// `"dhcp-snooping":"false"` on the bridge, `"trusted":"false"` on all 26 bridge
// ports — so these tests verify the reading against what the device answered,
// not against what we hoped it would answer.
//
// Where a state the reference device is NOT in has to be exercised — snooping
// already on, a port already trusted, a second bridge, a write the device did
// not retain — the capture is MUTATED in place by the helper below, or a small
// Row is CONSTRUCTED, and each such spot says so. The mutation starts from the
// real row, so every field but the one under test remains the device's.
//
// No WRITE appears in any capture: the channel to the reference device is
// restricted to GET/HEAD by project decision. The apply tests therefore prove
// apply LOGIC on real states, exactly as apply_test.go does.

import (
	"reflect"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// snoopFixtures loads the two real captures.
func snoopFixtures(t *testing.T) (Row, []Row) {
	t.Helper()
	return fixtureOne(t, "interface_bridge.json"), fixture(t, "interface_bridge_port.json")
}

// withField returns a copy of a CAPTURED row with one field changed, so a state
// the reference device is not in can be exercised without inventing the other
// forty fields.
func withField(r Row, field, value string) Row {
	var out = Row{}
	var k, v string
	for k, v = range r {
		out[k] = v
	}
	out[field] = value
	return out
}

// --- reading ---

func TestReadSnoopingOnTheRealCapture(t *testing.T) {
	var br, ports = snoopFixtures(t)
	var st = foldDHCPSnoop(br, ports, 1)

	// The capture says "dhcp-snooping":"false" — the connector must not soften it.
	if st.Enabled {
		t.Fatalf("the reference bridge carries dhcp-snooping=false and the reader says on")
	}
	if len(st.Ports) == 0 {
		t.Fatal("26 bridge ports were captured and none was read")
	}
	var p driver.DHCPSnoopPortState
	for _, p = range st.Ports {
		if p.Trusted {
			t.Fatalf("%s reads trusted; every captured row says trusted=false", p.Port)
		}
	}
	if st.Ports[0].Port != "ether1" {
		t.Fatalf("first port = %q, want ether1: the device's own faceplate order was lost",
			st.Ports[0].Port)
	}
}

// THE MISREADING THE CONTRACT WARNS ABOUT, INVERTED ON THIS PLATFORM. An empty
// VLANs list here means "RouterOS has no such level", not "no VLAN is
// protected". Drop the Note and a fully protected bridge reads as covering
// nothing.
func TestEmptyVLANListIsExplainedNotLeftToBeMisread(t *testing.T) {
	var br, ports = snoopFixtures(t)
	var st = foldDHCPSnoop(withField(br, "dhcp-snooping", "true"), ports, 1)
	if len(st.VLANs) != 0 {
		t.Fatalf("VLANs = %v; RouterOS has no per-VLAN snooping and this connector must "+
			"invent nothing", st.VLANs)
	}
	if !snoopNoteHas(st.Notes, "NO per-VLAN DHCP snooping") {
		t.Fatalf("the empty VLAN list is not explained: %v", st.Notes)
	}
	if !snoopNoteHas(st.Notes, "not that no VLAN is protected") {
		t.Fatalf("the Note does not invert the misreading, which is its only job: %v", st.Notes)
	}
}

// Enabled is a property OF A BRIDGE here, and a single boolean read as a
// property of the chassis would be wrong on any device with two bridges.
func TestEnabledIsNamedAsABridgePropertyNotAChassisOne(t *testing.T) {
	var br, ports = snoopFixtures(t)
	var st = foldDHCPSnoop(br, ports, 1)
	if !snoopNoteHas(st.Notes, "per BRIDGE") || !snoopNoteHas(st.Notes, `"bridge"`) {
		t.Fatalf("nothing says WHICH bridge the boolean belongs to: %v", st.Notes)
	}
	// Two bridges: the ports of the other one are absent, and that absence must be
	// stated rather than left to look like a short faceplate.
	var st2 = foldDHCPSnoop(br, ports, 2)
	if !snoopNoteHas(st2.Notes, "not the same as untrusted") {
		t.Fatalf("a second bridge's ports go missing without a word: %v", st2.Notes)
	}
}

// A port that is not a bridge member has no `trusted` field to read. It must be
// ABSENT from Ports, not reported untrusted (driver/dhcpsnoop.go).
func TestNonMemberPortIsAbsentNotUntrusted(t *testing.T) {
	var br, ports = snoopFixtures(t)
	// CONSTRUCTED row: a bridge-port record belonging to ANOTHER bridge. The
	// reference device has a single bridge, so this state had to be built.
	var foreign = Row{".id": "*99", "bridge": "bridge-guest",
		"interface": "ether24", "trusted": "true"}
	var st = foldDHCPSnoop(br, append(append([]Row{}, ports...), foreign), 2)

	var p driver.DHCPSnoopPortState
	for _, p = range st.Ports {
		if p.Port == "ether24" && p.Trusted {
			t.Fatal("a bridge-port row belonging to another bridge leaked into this " +
				"bridge's trust list: the flags on screen would belong to two switching tables")
		}
	}
	if !snoopNoteHas(st.Notes, "absent from this list rather than reported untrusted") {
		t.Fatalf("the absence rule is not stated: %v", st.Notes)
	}
}

func TestSnoopOffSaysTheTrustFlagsAreInert(t *testing.T) {
	var br, ports = snoopFixtures(t)
	var off = foldDHCPSnoop(br, ports, 1)
	if !snoopNoteHas(off.Notes, "INERT") {
		t.Fatalf("feature off and nothing says the flags do not act: %v", off.Notes)
	}
	var on = foldDHCPSnoop(withField(br, "dhcp-snooping", "true"), ports, 1)
	if snoopNoteHas(on.Notes, "INERT") {
		t.Fatalf("feature on and the connector still calls the flags inert: %v", on.Notes)
	}
}

func TestPickBridgePrefersTheFilteringOne(t *testing.T) {
	// CONSTRUCTED: the reference device has one bridge, so a two-bridge device
	// had to be built to pin the choice.
	var plain = Row{".id": "*1", "name": "bridge-flat", "vlan-filtering": "false"}
	var filtering = Row{".id": "*2", "name": "bridge-seg", "vlan-filtering": "true"}
	if pickBridge([]Row{plain, filtering}).Get("name") != "bridge-seg" {
		t.Fatal("the filtering bridge is the one carrying segmentation and must be chosen")
	}
	if pickBridge([]Row{plain}).Get("name") != "bridge-flat" {
		t.Fatal("with no filtering bridge the first is the answer")
	}
}

// --- the planner ---

func TestPlanTurnsTheFeatureOnAndSaysSoLoudly(t *testing.T) {
	var br, ports = snoopFixtures(t)
	var plan = driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "ether1", Trusted: true}},
		VLANs: []int{100},
	}
	var enable, writes, out = planDHCPSnoop(plan, br, ports)
	if !enable {
		t.Fatal("the captured bridge has dhcp-snooping=false and the plan does not turn it on: " +
			"the caller would get an exact trust flag and zero protection")
	}
	if len(writes) != 1 || writes[0].Port != "ether1" || !writes[0].Trusted {
		t.Fatalf("writes = %+v", writes)
	}
	// The .id must come from the read, never from a constant.
	if writes[0].RowID != "*0" {
		t.Fatalf("RowID = %q, want the *0 the capture carries for ether1", writes[0].RowID)
	}
	if !snoopNoteHas(out.Notes, "TURNED IT ON") {
		t.Fatalf("a bridge-wide change is not reported: %v", out.Notes)
	}
	if !snoopNoteHas(out.Notes, "never asked about") {
		t.Fatalf("the note does not say the change reaches ports outside the plan: %v", out.Notes)
	}
}

// THE PROHIBITION: nothing in this planner may ever produce a disable.
func TestPlanNeverTurnsTheFeatureOff(t *testing.T) {
	var br, ports = snoopFixtures(t)
	var on = withField(br, "dhcp-snooping", "true")
	// A plan that untrusts every port — the shape that would tempt a connector to
	// "clean up" the bridge flag once the last trusted port went away.
	var plan driver.DHCPSnoopPlan
	var p Row
	for _, p = range ports {
		plan.Ports = append(plan.Ports,
			driver.DHCPSnoopPortIntent{Port: p.Get("interface"), Trusted: false})
	}
	var enable, writes, out = planDHCPSnoop(plan, on, ports)
	if enable {
		t.Fatal("the feature was already on and the planner asked to write it again")
	}
	// enableBridge is the ONLY channel to the bridge flag, and it is a bool that
	// can only mean "turn on". There is no representation of a disable, which is
	// the structural guarantee — assert the writes touch ports only.
	var w trustWrite
	for _, w = range writes {
		if w.Port == "" || w.RowID == "" {
			t.Fatalf("a write with no port/row: %+v", w)
		}
	}
	if !snoopNoteHas(out.Notes, "NO port on this bridge is trusted") {
		t.Fatalf("a bridge with snooping on and not one trusted port is an outage waiting "+
			"for a lease timer, and nothing says so: %v", out.Notes)
	}
}

// A port outside the bridge has no flag to set. That is an ORDINARY REFUSAL and
// belongs in Skipped — never a Go error, never dropped.
func TestNonMemberPortLandsInSkipped(t *testing.T) {
	var br, ports = snoopFixtures(t)
	var plan = driver.DHCPSnoopPlan{Ports: []driver.DHCPSnoopPortIntent{
		{Port: "ether1", Trusted: true},
		{Port: "sfp-sfpplus9", Trusted: true}, // does not exist on this chassis
	}}
	var _, writes, out = planDHCPSnoop(plan, br, ports)
	if len(writes) != 1 {
		t.Fatalf("the unknown port produced a write: %+v", writes)
	}
	if len(out.Skipped) != 1 || !strings.Contains(out.Skipped[0], "sfp-sfpplus9") {
		t.Fatalf("Skipped = %v — the refusal was swallowed", out.Skipped)
	}
	if !strings.Contains(out.Skipped[0], "not a member of bridge") {
		t.Fatalf("the reason does not say where the flag would have gone: %q", out.Skipped[0])
	}
}

func TestBridgePortWithoutAnIDIsRefusedNotGuessed(t *testing.T) {
	var br, _ = snoopFixtures(t)
	// CONSTRUCTED: a row with no `.id`. RouterOS always serves one; this is the
	// defensive path, and a `set` without an identifier writes to another row.
	var noID = Row{"bridge": "bridge", "interface": "ether7", "trusted": "false"}
	var plan = driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "ether7", Trusted: true}}}
	var _, writes, out = planDHCPSnoop(plan, br, []Row{noID})
	if len(writes) != 0 {
		t.Fatalf("a write was planned without an .id: %+v", writes)
	}
	if len(out.Skipped) != 1 {
		t.Fatalf("Skipped = %v", out.Skipped)
	}
}

func TestPortAlreadyAtTheIntentIsNotRewritten(t *testing.T) {
	var br, ports = snoopFixtures(t)
	// The capture says every port is trusted=false, so an untrust intent is a no-op.
	var plan = driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "ether3", Trusted: false}}}
	var _, writes, _ = planDHCPSnoop(plan, br, ports)
	if len(writes) != 0 {
		t.Fatalf("a round trip that changes nothing was planned: %+v", writes)
	}
}

// The site's VLANs are not written here, and the reason must be the TRUE one:
// saying "snooping covers nothing yet" on this platform would be backwards.
func TestVLANsAreExplainedNotSilentlyIgnored(t *testing.T) {
	var br, ports = snoopFixtures(t)
	var withVLANs, _, out = planDHCPSnoop(
		driver.DHCPSnoopPlan{VLANs: []int{10, 20}}, br, ports)
	_ = withVLANs
	if !snoopNoteHas(out.Notes, "no per-VLAN DHCP snooping") {
		t.Fatalf("two declared VLANs went nowhere without a word: %v", out.Notes)
	}
	if snoopNoteHas(out.Notes, "covers nothing") {
		t.Fatal("the contract's empty-VLAN sentence was copied verbatim onto a platform " +
			"where it is false: bridge-level snooping covers every VLAN it switches")
	}
	var _, _, none = planDHCPSnoop(driver.DHCPSnoopPlan{}, br, ports)
	if !snoopNoteHas(none.Notes, "declares no VLAN") {
		t.Fatalf("an empty VLAN list is not addressed at all: %v", none.Notes)
	}
}

// Two identical gestures must produce two identical write lists — the planner
// walks the plan, and the plan's order is the caller's, but the SET of writes
// and their row ids must not depend on map iteration.
func TestPlanIsDeterministic(t *testing.T) {
	var br, ports = snoopFixtures(t)
	var plan = driver.DHCPSnoopPlan{Ports: []driver.DHCPSnoopPortIntent{
		{Port: "ether24", Trusted: true},
		{Port: "ether2", Trusted: true},
		{Port: "sfp-sfpplus1", Trusted: true},
	}}
	var _, first, firstOut = planDHCPSnoop(plan, br, ports)
	var i int
	for i = 0; i < 20; i++ {
		var _, again, againOut = planDHCPSnoop(plan, br, ports)
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("run %d planned different writes:\n %+v\n %+v", i, first, again)
		}
		if !reflect.DeepEqual(firstOut.Notes, againOut.Notes) {
			t.Fatalf("run %d produced different notes", i)
		}
	}
}

// --- the verification re-read ---
//
// This is the defect these tests were written to close: the apply used to report
// every accepted `set` as a written port. A 2xx that changes nothing is the
// characteristic silent failure, and PortsWritten was carrying it.

func TestVerifyConfirmsOnlyWhatTheReReadShows(t *testing.T) {
	var _, ports = snoopFixtures(t)
	// CONSTRUCTED end-state: the captured rows with ether1 now trusted, which is
	// what a bridge looks like after the write we asked for landed.
	var after = make([]Row, 0, len(ports))
	var p Row
	for _, p = range ports {
		if p.Get("interface") == "ether1" {
			after = append(after, withField(p, "trusted", "true"))
			continue
		}
		after = append(after, p)
	}
	var snap = verifySnoopSnapshot{
		Bridge: Row{".id": "*1B", "name": "bridge", "dhcp-snooping": "true"},
		Ports:  after,
	}
	var writes = []trustWrite{{Port: "ether1", RowID: "*0", Trusted: true}}
	var confirmed, notes = verifySnoopWrites(true, writes, snap)
	if !reflect.DeepEqual(confirmed, []string{"ether1"}) {
		t.Fatalf("confirmed = %v, want [ether1]", confirmed)
	}
	if len(notes) != 0 {
		t.Fatalf("a write the re-read confirms produced complaints: %v", notes)
	}
}

func TestVerifyCatchesTheAcceptedWriteThatChangedNothing(t *testing.T) {
	var _, ports = snoopFixtures(t)
	// The re-read is the CAPTURE UNCHANGED: every port still trusted=false. That
	// is exactly the 2xx-that-did-nothing case.
	var snap = verifySnoopSnapshot{
		Bridge: Row{".id": "*1B", "name": "bridge", "dhcp-snooping": "true"},
		Ports:  ports,
	}
	var writes = []trustWrite{{Port: "ether1", RowID: "*0", Trusted: true}}
	var confirmed, notes = verifySnoopWrites(true, writes, snap)
	if len(confirmed) != 0 {
		t.Fatalf("ether1 is still trusted=false in the re-read and was reported written: %v",
			confirmed)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "did not stick") {
		t.Fatalf("notes = %v", notes)
	}
	if !strings.Contains(notes[0], "NOT trusted") {
		t.Fatalf("the note does not name the state the port is really in: %q", notes[0])
	}
}

func TestVerifyCatchesTheEnableThatChangedNothing(t *testing.T) {
	var _, ports = snoopFixtures(t)
	// Bridge still dhcp-snooping=false after we asked to turn it on: the trust
	// flags are in place and nothing is inspected.
	var snap = verifySnoopSnapshot{
		Bridge: Row{".id": "*1B", "name": "bridge", "dhcp-snooping": "false"},
		Ports:  ports,
	}
	var _, notes = verifySnoopWrites(true, nil, snap)
	if len(notes) != 1 || !strings.Contains(notes[0], "still reports it off") {
		t.Fatalf("notes = %v", notes)
	}
	// And an apply that was not asked to enable must not complain about it.
	var _, quiet = verifySnoopWrites(false, nil, snap)
	if len(quiet) != 0 {
		t.Fatalf("an apply that never touched the bridge flag complained about it: %v", quiet)
	}
}

func TestVerifyRefusesToConfirmAVanishedRow(t *testing.T) {
	var snap = verifySnoopSnapshot{
		Bridge: Row{"name": "bridge", "dhcp-snooping": "true"},
		Ports:  []Row{{".id": "*0", "bridge": "bridge", "interface": "ether1", "trusted": "true"}},
	}
	var writes = []trustWrite{{Port: "ether9", RowID: "*8", Trusted: true}}
	var confirmed, notes = verifySnoopWrites(false, writes, snap)
	if len(confirmed) != 0 {
		t.Fatalf("a port absent from the re-read was confirmed: %v", confirmed)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "absent from") {
		t.Fatalf("notes = %v", notes)
	}
}

// --- the shape of the contract ---

func TestClientIsSeenAsSnoopReaderAndApplier(t *testing.T) {
	var c = &Client{}
	var _, okR = driver.AsDHCPSnoopReader(c)
	var _, okA = driver.AsDHCPSnoopApplier(c)
	if !okR || !okA {
		t.Fatalf("reader=%v applier=%v", okR, okA)
	}
}

// On RouterOS meshcraft's port names ARE the device's names: ether3 is ether3.
// A prefix applied here would write to a port that does not exist.
func TestNoPortPrefixIsAppliedOnThisPlatform(t *testing.T) {
	var br, ports = snoopFixtures(t)
	var st = foldDHCPSnoop(br, ports, 1)
	var p driver.DHCPSnoopPortState
	for _, p = range st.Ports {
		if !strings.HasPrefix(p.Port, "ether") && !strings.HasPrefix(p.Port, "sfp") {
			t.Fatalf("port %q is not a RouterOS interface name: something rewrote it", p.Port)
		}
	}
	// And the planner matches the plan's names against the device's, unchanged.
	var _, writes, _ = planDHCPSnoop(driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "ether5", Trusted: true}}}, br, ports)
	if len(writes) != 1 || writes[0].Port != "ether5" {
		t.Fatalf("writes = %+v: the plan's name must be the device's name here", writes)
	}
}

func snoopNoteHas(notes []string, needle string) bool {
	var n string
	for _, n = range notes {
		if strings.Contains(n, needle) {
			return true
		}
	}
	return false
}
