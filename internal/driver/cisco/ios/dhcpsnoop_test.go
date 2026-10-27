package ios

// DHCP snooping tests (cf. dhcpsnoop.go, and driver/dhcpsnoop.go for the
// contract these hold the code to).
//
// FIXTURE PROVENANCE — READ THIS BEFORE TRUSTING A PASS
//
// Two kinds of fixture live here, and the difference decides what a green run
// is worth.
//
// CAPTURED (testdata/show_ip_dhcp_snooping_{off,on}.txt): taken 2026-08-08 from
// the lab 2960-X, feature off and on, through meshcraft's own SOCKS route. These
// say what the device actually prints. They arrived after everything below was
// written, and they vindicated it — the parser needed no change — but they also
// carried two lines no constructed fixture had, which is the argument for
// capturing rather than reasoning: a "Switch DHCP gleaning is disabled" line
// directly under the one that matters, and an indented "Custom circuit-ids:"
// sub-line inside the trust table.
//
// CONSTRUCTED (the docSnoop* constants): written from the Catalyst 2960-X / 2960
// command reference, exactly like the fixtures in parse_test.go. They are KEPT,
// deliberately, because they cover shapes the lab unit cannot produce — the 12.2
// two-column layout with "Fa0/1" names, and a rate-limited untrusted port —
// and because they pin the connector's REASONING rather than one switch's
// output. What they cannot do is tell you the device prints what we think it
// prints; only the captured pair does that, and only for the train it came from.
//
// Driver.WritesExtra HAS announced "dhcp_snooping" since 2026-08-09 (asserted
// below). The captures here are still READS; what earned the declaration is
// live_snoop_test.go, which drove a plan through Client.ApplyDHCPSnooping on the
// lab 2960-X with the real uplink trusted, converged on a second apply, exercised
// a removal and both refusal branches, and left running AND startup configs as
// found.

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// CONSTRUCTED, not captured: the IOS 15.x layout, three columns in the trust
// table (Trusted / Allow option / Rate limit). VLAN 100 is configured and NOT
// operational — snooping asked for on a VLAN the switch does not have, which is
// the divergence the reader must name rather than hide.
const docSnoop15x = `Switch DHCP snooping is enabled
DHCP snooping is configured on following VLANs:
99,100
DHCP snooping is operational on following VLANs:
99
DHCP snooping is configured on the following L3 Interfaces:

Insertion of option 82 is enabled
   circuit-id default format: vlan-mod-port
   remote-id: 0c75.bd51.6d80 (MAC)
Option 82 on untrusted port is not allowed
Verification of hwaddr field is enabled
Verification of giaddr field is enabled
DHCP snooping trust/rate is configured on the following Interfaces:

Interface                  Trusted    Allow option    Rate limit (pps)
-----------------------    -------    ------------    ----------------
GigabitEthernet1/0/28      yes        yes             unlimited
GigabitEthernet1/0/2       no         no              20
`

// CONSTRUCTED, not captured: the 12.2 layout — two columns, abbreviated
// interface names, and the pre-stack slot/port shape "Fa0/1".
const docSnoop12x = `Switch DHCP snooping is enabled
DHCP snooping is configured on following VLANs:
10-12
DHCP snooping is operational on following VLANs:
10-12
Insertion of option 82 is enabled
Option 82 on untrusted port is not allowed
Verification of hwaddr field is enabled
Interface                    Trusted     Rate limit (pps)
------------------------     -------     ----------------
Fa0/24                       yes         unlimited
`

// CONSTRUCTED, not captured: the shape of a switch that has the feature
// compiled in and never configured. Both lists print "none", and "none" is
// not a VLAN.
const docSnoopOff = `Switch DHCP snooping is disabled
DHCP snooping is configured on following VLANs:
none
DHCP snooping is operational on following VLANs:
none
DHCP snooping is configured on the following L3 Interfaces:

Insertion of option 82 is disabled
Option 82 on untrusted port is not allowed
Verification of hwaddr field is enabled
`

// --- the three levels, read as three levels ---

func TestParseSnoopReadsTheThreeLevelsSeparately(t *testing.T) {
	var r = parseShowDHCPSnooping(docSnoop15x)
	if !r.Enabled {
		t.Fatal("global statement present and the reader says the feature is off")
	}
	if !reflect.DeepEqual(r.Configured, []int{99, 100}) {
		t.Fatalf("configured VLANs = %v, want [99 100]", r.Configured)
	}
	if !reflect.DeepEqual(r.Operational, []int{99}) {
		t.Fatalf("operational VLANs = %v, want [99]", r.Operational)
	}
	// The two lists must NOT be merged: a screen showing one of them would show
	// working protection on VLAN 100, where nothing is inspected.
	if reflect.DeepEqual(r.Configured, r.Operational) {
		t.Fatal("the configured and operational lists came out identical: the divergence " +
			"this parser exists to surface has been collapsed")
	}
}

func TestParseSnoopKeepsTrustPerPortInBothDirections(t *testing.T) {
	var r = parseShowDHCPSnooping(docSnoop15x)
	var trusted, ok = r.Trust["GE28"]
	if !ok || !trusted {
		t.Fatalf("GE28 printed \"yes\" and Trust says %v (present=%v)", trusted, ok)
	}
	var untrusted, ok2 = r.Trust["GE2"]
	if !ok2 {
		t.Fatal("GE2 has a rate-limit row and was not read at all")
	}
	if untrusted {
		t.Fatal("GE2 printed \"no\" and the reader made it trusted")
	}
}

// THE DISTINCTION THE CONTRACT INSISTS ON: a port the device did not print was
// NOT READ, which is not the same as untrusted. "show ip dhcp snooping" lists
// only the ports departing from the default, so most of a real switch's ports
// are absent — and drawing "untrusted" on them would be asserting protection
// from a line the device never printed.
func TestUnprintedPortIsAbsentNotReportedUntrusted(t *testing.T) {
	var st = snoopStateOf(parseShowDHCPSnooping(docSnoop15x))
	var seen = map[string]bool{}
	var p driver.DHCPSnoopPortState
	for _, p = range st.Ports {
		seen[p.Port] = true
	}
	if len(st.Ports) != 2 {
		t.Fatalf("state carries %d ports (%v); the device printed 2", len(st.Ports), st.Ports)
	}
	// GE1 exists on the faceplate, carries IOS's default (untrusted), and was
	// NOT printed. It must not appear.
	if seen["GE1"] {
		t.Fatal("GE1 was never printed by the device and appears in DHCPSnoopState.Ports: " +
			"\"not read\" has been turned into \"untrusted\", which is the one confusion " +
			"driver/dhcpsnoop.go forbids")
	}
	// And the absence must be EXPLAINED, not left for a caller to guess.
	if !notesMention(st.Notes, "absent") {
		t.Fatalf("nothing in Notes explains why most ports are missing: %v", st.Notes)
	}
}

func TestParseSnoopUnderstandsThe12_2TwoColumnLayout(t *testing.T) {
	var r = parseShowDHCPSnooping(docSnoop12x)
	if !reflect.DeepEqual(r.Configured, []int{10, 11, 12}) {
		t.Fatalf("configured = %v, want [10 11 12] (the range must be expanded)", r.Configured)
	}
	var trusted, ok = r.Trust["FE24"]
	if !ok || !trusted {
		t.Fatalf("Fa0/24 (slot/port, no stack member) not read as FE24 trusted: %v", r.Trust)
	}
}

func TestParseSnoopNoneIsNotAVLAN(t *testing.T) {
	var r = parseShowDHCPSnooping(docSnoopOff)
	if r.Enabled {
		t.Fatal("\"Switch DHCP snooping is disabled\" read as enabled")
	}
	if len(r.Configured) != 0 || len(r.Operational) != 0 {
		t.Fatalf("\"none\" became VLANs: configured=%v operational=%v",
			r.Configured, r.Operational)
	}
	if len(r.Printed) != 0 {
		t.Fatalf("no trust row was printed and the reader found %v", r.Printed)
	}
}

// The trust table's separator rule is all dashes, which is exactly the
// character class a VLAN continuation line is made of. If the collector is
// still open when it arrives, the ruler becomes a VLAN range.
func TestParseSnoopDoesNotSwallowTheTableRuler(t *testing.T) {
	var r = parseShowDHCPSnooping(docSnoop15x)
	var v int
	for _, v = range append(append([]int{}, r.Configured...), r.Operational...) {
		if v != 99 && v != 100 {
			t.Fatalf("VLAN %d appeared from nowhere — a ruler or a rate limit was collected "+
				"as a VLAN list (configured=%v operational=%v)", v, r.Configured, r.Operational)
		}
	}
}

func TestParseSnoopSurvivesCRLF(t *testing.T) {
	var lf = parseShowDHCPSnooping(docSnoop15x)
	var crlf = parseShowDHCPSnooping(strings.ReplaceAll(docSnoop15x, "\n", "\r\n"))
	if !reflect.DeepEqual(lf, crlf) {
		t.Fatalf("CRLF output parses differently from LF:\n LF  = %+v\n CRLF= %+v", lf, crlf)
	}
}

// Two reads of one device must produce one list. The trust table's order is the
// device's, not the faceplate's, and a map would add its own randomness on top.
func TestPrintedPortsComeOutInFaceplateOrder(t *testing.T) {
	var r = parseShowDHCPSnooping(docSnoop15x)
	if !reflect.DeepEqual(r.Printed, []string{"GE2", "GE28"}) {
		t.Fatalf("Printed = %v, want [GE2 GE28] — the device printed GE28 first and the "+
			"reader must sort", r.Printed)
	}
}

func TestStdPortLessIsNumericNotLexical(t *testing.T) {
	if !stdPortLess("GE2", "GE10") {
		t.Fatal("GE2 sorted after GE10: string order has leaked into a faceplate order")
	}
	if stdPortLess("GE10", "GE2") {
		t.Fatal("stdPortLess is not a strict order")
	}
	// Media prefix decides first: the copper block, then the cages.
	if !stdPortLess("GE28", "TG1") {
		t.Fatal("GE must sort before TG")
	}
	// A name outside the scheme must not panic, and must still give an order.
	if stdPortLess("Po1", "Po1") {
		t.Fatal("an unparsable name compared unequal to itself")
	}
}

// --- Notes: the three facts the struct's fields cannot carry ---

func TestSnoopStateNamesTheGlobalOffTrap(t *testing.T) {
	var st = snoopStateOf(parseShowDHCPSnooping(docSnoopOff))
	if st.Enabled {
		t.Fatal("state says enabled on a device whose global statement is absent")
	}
	if !notesMention(st.Notes, "OFF switch-wide") {
		t.Fatalf("a device inspecting nothing does not say so in Notes: %v", st.Notes)
	}
}

func TestSnoopStateNamesTheConfiguredButNotOperationalVLAN(t *testing.T) {
	var st = snoopStateOf(parseShowDHCPSnooping(docSnoop15x))
	if !notesMention(st.Notes, "NOT operational") {
		t.Fatalf("VLAN 100 is configured and not operational and Notes is silent: %v", st.Notes)
	}
	// And the field carries the CONFIGURED list — that is what an apply must not
	// remove and what the configuration says.
	if !reflect.DeepEqual(st.VLANs, []int{99, 100}) {
		t.Fatalf("DHCPSnoopState.VLANs = %v, want the configured list [99 100]", st.VLANs)
	}
}

func TestSnoopStateNamesGlobalOnWithNoVLAN(t *testing.T) {
	// CONSTRUCTED, not captured: the worst-of-both state — the feature on, no
	// VLAN under it, and a trusted port that reads on a panel like protection.
	var out = `Switch DHCP snooping is enabled
DHCP snooping is configured on following VLANs:
none
DHCP snooping is operational on following VLANs:
none
Interface                  Trusted    Allow option    Rate limit (pps)
-----------------------    -------    ------------    ----------------
GigabitEthernet1/0/28      yes        yes             unlimited
`
	var st = snoopStateOf(parseShowDHCPSnooping(out))
	if !st.Enabled || len(st.VLANs) != 0 {
		t.Fatalf("fixture misread: enabled=%v vlans=%v", st.Enabled, st.VLANs)
	}
	if !notesMention(st.Notes, "covers NO VLAN") {
		t.Fatalf("snooping on with no VLAN is not named in Notes: %v", st.Notes)
	}
}

// --- the write ---

func TestSnoopTrustCmdsAreExplicitInBothDirections(t *testing.T) {
	var on = snoopTrustCmds("Gi1/0/28", true)
	if !reflect.DeepEqual(on, []string{"interface Gi1/0/28", "ip dhcp snooping trust", "exit"}) {
		t.Fatalf("trust block = %v", on)
	}
	var off = snoopTrustCmds("Gi1/0/28", false)
	if !reflect.DeepEqual(off,
		[]string{"interface Gi1/0/28", "no ip dhcp snooping trust", "exit"}) {
		t.Fatalf("untrust block = %v", off)
	}
	// NEITHER direction may touch the feature itself. "no ip dhcp snooping"
	// on its own line would strip protection from the whole device on the
	// strength of one port's click (driver/dhcpsnoop.go: it never turns the
	// feature OFF).
	var cmd string
	for _, cmd = range append(on, off...) {
		if cmd == "no ip dhcp snooping" || cmd == "no ip dhcp snooping vlan" {
			t.Fatalf("a per-port write emitted the global disable form: %q", cmd)
		}
	}
}

// THE PROHIBITION AT THE HEART OF THE CONTRACT: a VLAN found enabled is
// protection this connector did not install, and it must survive an apply that
// does not mention it.
func TestSnoopVLANsToAddNeverRemovesWhatItFound(t *testing.T) {
	// The device snoops 99 and 200. The plan names 99 and 300.
	var toAdd = snoopVLANsToAdd([]int{99, 300}, []int{99, 200})
	if !reflect.DeepEqual(toAdd, []int{300}) {
		t.Fatalf("toAdd = %v, want [300]: 99 is already there and 200 is not ours to take away",
			toAdd)
	}
	var v int
	for _, v = range toAdd {
		if v == 200 {
			t.Fatal("VLAN 200 was found enabled on the device and appears in the write set")
		}
	}
	// A plan that names nothing removes nothing.
	if len(snoopVLANsToAdd(nil, []int{99, 200})) != 0 {
		t.Fatal("an empty plan produced VLAN commands")
	}
}

func TestSnoopVLANsToAddIsSortedAndDeduped(t *testing.T) {
	// Two identical gestures must produce one command, whatever order the
	// caller's list arrives in.
	var a = snoopVLANsToAdd([]int{300, 99, 300, 12}, nil)
	var b = snoopVLANsToAdd([]int{12, 300, 99, 99}, nil)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("the same set in two orders gave %v and %v", a, b)
	}
	if !reflect.DeepEqual(a, []int{12, 99, 300}) {
		t.Fatalf("toAdd = %v, want [12 99 300]", a)
	}
	if joinInts(a) != "12,99,300" {
		t.Fatalf("command argument = %q", joinInts(a))
	}
}

// The VLANs this connector refuses were being dropped inside snoopVLANsToAdd
// and reported nowhere: the operator read a successful apply while the VLAN
// they asked to protect carried no snooping. ApplyResult.Skipped is where an
// ordinary refusal belongs (driver/dhcpsnoop.go).
func TestRefusedVLANsAreReportedNotDroppedSilently(t *testing.T) {
	var want = []int{99, 0, 4095, 1003}
	var toAdd = snoopVLANsToAdd(want, nil)
	if !reflect.DeepEqual(toAdd, []int{99}) {
		t.Fatalf("toAdd = %v, want [99]", toAdd)
	}
	var refused = snoopVLANsRefused(want)
	if len(refused) != 3 {
		t.Fatalf("3 VLANs were refused and %d were reported: %v", len(refused), refused)
	}
	var needle string
	for _, needle = range []string{"0", "4095", "1003"} {
		if !notesMention(refused, "VLAN "+needle+" ") {
			t.Fatalf("VLAN %s was dropped without a word: %v", needle, refused)
		}
	}
	// A plan with nothing to refuse must not manufacture a refusal.
	if len(snoopVLANsRefused([]int{1, 99, 4094})) != 0 {
		t.Fatal("a legal plan produced Skipped entries")
	}
}

// "ip dhcp snooping vlan 99" on a VLAN the switch does not have is accepted
// and changes nothing. The trust flags then sit on a VLAN nobody inspects, and
// the return code says the apply worked.
func TestVLANsNotRetainedAfterTheReReadAreCaught(t *testing.T) {
	var lost = snoopVLANsNotRetained([]int{99, 100}, []int{99})
	if !reflect.DeepEqual(lost, []int{100}) {
		t.Fatalf("lost = %v, want [100]", lost)
	}
	if len(snoopVLANsNotRetained([]int{99, 100}, []int{99, 100, 200})) != 0 {
		t.Fatal("VLANs the re-read confirms were reported as lost")
	}
	if len(snoopVLANsNotRetained(nil, nil)) != 0 {
		t.Fatal("an apply that added no VLAN reported a loss")
	}
}

func TestAnyTrustedSeesTheUplinkOrItsAbsence(t *testing.T) {
	if anyTrusted(nil) {
		t.Fatal("an empty plan trusts a port")
	}
	if anyTrusted([]driver.DHCPSnoopPortIntent{{Port: "GE2", Trusted: false}}) {
		t.Fatal("an all-untrusted plan reported a trusted port: the warning about dropping " +
			"the site's own DHCP replies would never fire")
	}
	if !anyTrusted([]driver.DHCPSnoopPortIntent{
		{Port: "GE2", Trusted: false}, {Port: "GE28", Trusted: true}}) {
		t.Fatal("a plan trusting the uplink was reported as trusting nothing")
	}
}

// The command must never drift into two different questions between the read
// and the apply's verification re-read (dhcpsnoop.go keeps it as a constant).
func TestSnoopCommandIsTheReadNotAConfigurationDump(t *testing.T) {
	if showSnoopCmd != "show ip dhcp snooping" {
		t.Fatalf("showSnoopCmd = %q", showSnoopCmd)
	}
	if strings.Contains(showSnoopCmd, "binding") {
		t.Fatal("the binding table is telemetry and must not be read by a configuration read")
	}
}

// The READ is captured as of 2026-08-08 (see the provenance note at the top of
// this file); the WRITE is not proven, and the capability must stay undeclared
// until it is (driver/dhcpsnoop.go on DHCPSnoopApplier). That bar has been met:
// a plan driven through Client.ApplyDHCPSnooping against the REAL uplink, plus a
// removal and both refusal branches, on the lab 2960-X (live_snoop_test.go). This
// test now guards the other direction — that the earned declaration is not lost.
func TestDHCPSnoopingIsDeclaredOnLiveProof(t *testing.T) {
	var d = &Driver{}
	if !d.WritesExtra("dhcp_snooping") {
		t.Fatal("dhcp_snooping was proven on hardware on 2026-08-09 — a plan through " +
			"ApplyDHCPSnooping, uplink trusted, converged, removal and both refusals, " +
			"reverted in running AND startup config — and the declaration has been lost, " +
			"so the operator is told meshcraft cannot do something it can")
	}
	if !d.WritesExtra("stp") {
		t.Fatal("declaring dhcp_snooping is not the only thing this guard watches: stp " +
			"stopped being declared")
	}
}

// The compile-time assertions in dhcpsnoop.go say the Client is both; a caller
// gates the whole control on these (driver.AsDHCPSnoopReader).
func TestClientIsSeenAsBothReaderAndApplier(t *testing.T) {
	var c = &Client{}
	var _, okR = driver.AsDHCPSnoopReader(c)
	if !okR {
		t.Fatal("the Client is not seen as a DHCPSnoopReader")
	}
	var _, okA = driver.AsDHCPSnoopApplier(c)
	if !okA {
		t.Fatal("the Client is not seen as a DHCPSnoopApplier")
	}
}

func notesMention(notes []string, needle string) bool {
	var n string
	for _, n = range notes {
		if strings.Contains(n, needle) {
			return true
		}
	}
	return false
}

// --- THE CAPTURED FIXTURES: a real 2960-X, not the command reference ---
//
// Captured 2026-08-08 on the lab CiscoLabFirst1 (WS-C2960X-24TD-L, 192.0.2.2)
// through meshcraft's own SOCKS route. The ON state was produced deliberately —
// global enable, "ip dhcp snooping vlan 999" on a VLAN the switch does not
// have, and one trusted port — and reverted in the same gesture; the switch's
// running configuration carried no dhcp line before or after.
//
// VLAN 999 was chosen because it makes the capture safe AND useful at once:
// snooping polices only the VLANs it is enabled on, so a VLAN with no ports
// inspects nothing and no client could lose DHCP — and it is exactly the
// configured-but-NOT-operational divergence this parser exists to surface.
//
// Two lines here appear in NO constructed fixture, which is the whole argument
// for capturing:
//
//   - "Switch DHCP gleaning is disabled", a second global line immediately
//     under the one that matters. reSnoopGlobal keys on the word "snooping",
//     so gleaning cannot flip Enabled — but nothing had ever proven that,
//     because no fixture contained the line.
//   - "Custom circuit-ids:", an INDENTED sub-line inside the trust table,
//     after the interface row it belongs to. A trust-row pattern that keyed on
//     indentation alone would have read it as a port.
func loadSnoopFixture(t *testing.T, name string) string {
	t.Helper()
	var b, err = os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(b)
}

func TestParseSnoopOnCapturedHardwareOffState(t *testing.T) {
	var r = parseShowDHCPSnooping(loadSnoopFixture(t, "show_ip_dhcp_snooping_off.txt"))
	if r.Enabled {
		t.Fatal("the captured OFF state reads as enabled")
	}
	if len(r.Configured) != 0 || len(r.Operational) != 0 {
		t.Fatalf("\"none\" became VLANs: configured=%v operational=%v",
			r.Configured, r.Operational)
	}
	// The trust table prints its header and rule even with nothing in it. A
	// header read as a port would be a phantom trusted interface.
	if len(r.Trust) != 0 {
		t.Fatalf("empty trust table produced %d entries: %v", len(r.Trust), r.Trust)
	}
}

func TestParseSnoopOnCapturedHardwareOnState(t *testing.T) {
	var r = parseShowDHCPSnooping(loadSnoopFixture(t, "show_ip_dhcp_snooping_on.txt"))
	if !r.Enabled {
		t.Fatal("\"Switch DHCP snooping is enabled\" read as off — check that the " +
			"\"gleaning is disabled\" line below it is not being matched instead")
	}
	if !reflect.DeepEqual(r.Configured, []int{999}) {
		t.Fatalf("configured VLANs = %v, want [999]", r.Configured)
	}
	// THE POINT OF THE CAPTURE. The device really does print "none" under
	// "operational" while listing 999 under "configured", so the divergence
	// the contract insists on surfacing is a real shape and not a supposition.
	if len(r.Operational) != 0 {
		t.Fatalf("operational VLANs = %v, want none — 999 does not exist on this "+
			"switch, so snooping is configured on it and operational nowhere",
			r.Operational)
	}
	var trusted, ok = r.Trust["GE24"]
	if !ok || !trusted {
		t.Fatalf("Gi1/0/24 printed \"yes\"; Trust says %v (present=%v) — full map %v",
			trusted, ok, r.Trust)
	}
	// "Custom circuit-ids:" follows the interface row, indented. If it were
	// read as a row, the map would carry a second, invented port.
	if len(r.Trust) != 1 {
		t.Fatalf("one interface was printed and Trust holds %d: %v", len(r.Trust), r.Trust)
	}
}
