package instanton

// DHCP snooping tests (cf. dhcpsnoop.go, and driver/dhcpsnoop.go for the
// contract).
//
// FIXTURE PROVENANCE — THE WHOLE POINT OF THIS FILE
//
// The three SECTION names were captured read-only on 2026-08-09, and the two
// list sections' WRITE shapes on 2026-08-11 out of the product's own UI code
// (dhcpsnoop.go's header carries both documents verbatim). What has still never
// been captured is a POPULATED list body, because the lab unit has the feature
// off. So the fixtures below are of three kinds and they must not be confused:
//
//   - the ENVELOPE is copied from a REAL capture: the MSTPInstanceList body in
//     stp_mst_test.go, taken live from sw-labo-1930 (JL685A,
//     InstantOn_1930_3.4.0.0) on 2026-08-05. `<ResponseData>` →
//     `<DeviceConfiguration>` → `<Name type="section">`, and
//     `<VLANList>2-41,43-49,…</VLANList>` for a compressed VLAN set. That much
//     is this framework's real grammar;
//   - the `<VLAN>` / `<Interface>` row WRAPPERS and the `VLANID` /
//     `interfaceName` fields inside them are CAPTURED — they are what the UI
//     writes, and what the write builders now emit;
//   - rows built with `<Entry>` and a TRUST FLAG are RETAINED DELIBERATELY as
//     refuted shapes. The firmware serves no such flag; these fixtures exist to
//     prove the READ still believes an explicit one if some firmware publishes
//     it, and they must never be read as evidence that a 1930 serves it.
//
// WritesExtra("dhcp_snooping") stays false (asserted below): the shapes are
// settled, the APPLIER has never run against a device, and these tests cannot
// settle that.

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// snoopBody wraps an inner body in the envelope the real captures carry.
func snoopBody(section, inner string) string {
	return `<?xml version="1.0" encoding="UTF-8" ?>
<ResponseData>
<DeviceConfiguration>
<version>1.0</version>
<` + section + ` type="section">
` + inner + `
</` + section + `>
</DeviceConfiguration>
</ResponseData>`
}

// CONSTRUCTED (real envelope, hypothetical names): the global section as it
// would look with the feature ON. _TRUE='1' is real — it comes from modules.js.
var snoopGlobalOn = snoopBody("DHCPSnoopingGlobalSetting",
	`<DHCPSnoopingEnable>1</DHCPSnoopingEnable>
<verifyMAC>2</verifyMAC>`)

var snoopGlobalOff = snoopBody("DHCPSnoopingGlobalSetting",
	`<DHCPSnoopingEnable>2</DHCPSnoopingEnable>
<verifyMAC>2</verifyMAC>`)

// CONSTRUCTED: a global section that answered but carries NONE of the names this
// connector knows. This is the shape that must produce a loud note rather than
// "snooping is off".
var snoopGlobalAlien = snoopBody("DHCPSnoopingGlobalSetting",
	`<snoopAdminState>1</snoopAdminState>
<verifyMAC>2</verifyMAC>`)

// CONSTRUCTED: the row-per-VLAN shape, as VLANList itself serves.
var snoopVLANRows = snoopBody("DHCPSnoopingVLANList",
	`<Entry><VLANID>10</VLANID><enabled>1</enabled></Entry>
<Entry><VLANID>99</VLANID><enabled>1</enabled></Entry>
<Entry><VLANID>50</VLANID><enabled>2</enabled></Entry>`)

// CONSTRUCTED, but the <VLANList>ranges</VLANList> form is REAL — that is
// exactly how the captured MSTPInstanceList serves a VLAN set.
var snoopVLANRanges = snoopBody("DHCPSnoopingVLANList",
	`<Entry><instanceID>0</instanceID><VLANList>10,30-32,99</VLANList></Entry>`)

// REFUTED SHAPE, KEPT ON PURPOSE: `<Entry>` rows carrying a `trustInterface`
// flag. No 1930 serves this — the real section carries `<Interface>` rows and no
// flag at all — and it is here to hold the READ's tolerance: a firmware that did
// publish an explicit flag must be believed rather than read as "trusted because
// the row exists". GE10 is deliberately listed BEFORE GE2 and GE1, because a
// device serving faceplate order is not what this fixture is for — it is here to
// catch a lexical sort.
var snoopPortRows = snoopBody("DHCPSnoopingInterfaceISList",
	`<Entry><interfaceName>10</interfaceName><trustInterface>2</trustInterface></Entry>
<Entry><interfaceName>2</interfaceName><trustInterface>1</trustInterface></Entry>
<Entry><interfaceName>1</interfaceName><trustInterface>2</trustInterface></Entry>`)

// THE CAPTURED SHAPE: `<Interface>` rows with an interfaceName and nothing else.
// Presence IS the trust flag, so this body means "GE2 and GE10 trusted, every
// other port untrusted".
var snoopPortRowsCaptured = snoopBody("DHCPSnoopingInterfaceList",
	`<Interface><interfaceName>10</interfaceName></Interface>
<Interface><interfaceName>2</interfaceName></Interface>`)

// THE CAPTURED VLAN SHAPE: `<VLAN>` rows with a VLANID and nothing else.
var snoopVLANRowsCaptured = snoopBody("DHCPSnoopingVLANList",
	`<VLAN><VLANID>99</VLANID></VLAN>
<VLAN><VLANID>10</VLANID></VLAN>`)

// --- the switch-wide flag: OFF and UNREAD are two different facts ---

func TestParseSnoopEnabledSeparatesOffFromUnread(t *testing.T) {
	var on, found = parseSnoopEnabled(snoopGlobalOn)
	if !on || !found {
		t.Fatalf("enable field _TRUE='1' read as on=%v found=%v", on, found)
	}
	var off, found2 = parseSnoopEnabled(snoopGlobalOff)
	if off || !found2 {
		t.Fatalf("_FALSE='2' read as on=%v found=%v; found must stay true — the device "+
			"answered, it just answered no", off, found2)
	}
	// The alien body: no known field. `found` false is the fact that matters —
	// reporting off here would be reporting a state we never read.
	var alien, found3 = parseSnoopEnabled(snoopGlobalAlien)
	if alien || found3 {
		t.Fatalf("a body with none of the known names gave on=%v found=%v", alien, found3)
	}
	var none, found4 = parseSnoopEnabled("")
	if none || found4 {
		t.Fatalf("an empty body gave on=%v found=%v", none, found4)
	}
}

func TestSnoopStateNamesAnUnreadableGlobalSection(t *testing.T) {
	var loc = snoopSections{global: "DHCPSnoopingGlobalSetting", globalBody: snoopGlobalAlien}
	var st = snoopStateOf(loc, "GE")
	if st.Enabled {
		t.Fatal("state says enabled from a body that never said so")
	}
	if !ionNoteHas(st.Notes, "carries none of the expected enable fields") {
		t.Fatalf("the section answered with unknown names and nothing says so: %v", st.Notes)
	}
	// And the note must PRINT what the device does serve: on a platform with no
	// documentation that message is how the shape gets learned.
	if !ionNoteHas(st.Notes, "snoopAdminState") {
		t.Fatalf("the note does not name the fields the firmware served: %v", st.Notes)
	}
}

func TestSnoopStateNamesAMissingGlobalSection(t *testing.T) {
	var loc = snoopSections{port: "DHCPSnoopingInterfaceISList", portBody: snoopPortRows}
	var st = snoopStateOf(loc, "GE")
	if !ionNoteHas(st.Notes, "may be inert") {
		t.Fatalf("trust flags reported with no global section and no warning: %v", st.Notes)
	}
}

// The contract's warning, in the direction this platform makes possible: an
// empty VLAN list here can mean UNREAD, and that is not "none".
func TestUnreadVLANListIsNotReportedAsNone(t *testing.T) {
	var loc = snoopSections{global: "DHCPSnoopingGlobalSetting", globalBody: snoopGlobalOn}
	var st = snoopStateOf(loc, "GE")
	if len(st.VLANs) != 0 {
		t.Fatalf("VLANs = %v from a body that carries none", st.VLANs)
	}
	if !ionNoteHas(st.Notes, "UNREAD, not \"none\"") {
		t.Fatalf("an unread VLAN list is not distinguished from an empty one: %v", st.Notes)
	}
}

// --- the VLAN list ---

func TestParseSnoopVLANsReadsBothShapes(t *testing.T) {
	// Row per VLAN, with the flag believed when present: 50 is off and must not
	// appear.
	var rows = parseSnoopVLANs(snoopVLANRows)
	if !reflect.DeepEqual(rows, []int{10, 99}) {
		t.Fatalf("row shape = %v, want [10 99] (VLAN 50 carries enabled=2)", rows)
	}
	// Compressed ranges, the form the real MSTPInstanceList capture uses.
	var ranges = parseSnoopVLANs(snoopVLANRanges)
	if !reflect.DeepEqual(ranges, []int{10, 30, 31, 32, 99}) {
		t.Fatalf("range shape = %v, want [10 30 31 32 99]", ranges)
	}
}

// On this UI's row-shaped tables presence IS membership, and a row with no
// enable field must count as covered rather than be dropped.
func TestVLANRowWithoutAnEnableFieldCountsAsCovered(t *testing.T) {
	var body = snoopBody("DHCPSnoopingVLANList",
		`<Entry><VLANID>77</VLANID></Entry>`)
	var got = parseSnoopVLANs(body)
	if !reflect.DeepEqual(got, []int{77}) {
		t.Fatalf("got %v, want [77]: presence is membership on these tables", got)
	}
}

func TestParseSnoopVLANsIsSortedAndDeduped(t *testing.T) {
	var body = snoopBody("DHCPSnoopingVLANList",
		`<Entry><VLANID>99</VLANID></Entry>
<Entry><VLANID>10</VLANID></Entry>
<Entry><VLANID>99</VLANID></Entry>
<Entry><VLANID>2</VLANID></Entry>`)
	var got = parseSnoopVLANs(body)
	if !reflect.DeepEqual(got, []int{2, 10, 99}) {
		t.Fatalf("got %v, want [2 10 99]", got)
	}
}

// --- the per-port trust flags ---

// THE CAPTURED READING: inside DHCPSnoopingInterfaceList a row IS the trust
// flag, so a row with no flag means TRUSTED and a port with no row means
// untrusted. The refuted shape used to make this connector drop the row instead.
func TestPresenceIsMembershipInsideThePortSection(t *testing.T) {
	var ports = parseSnoopPorts(snoopPortRowsCaptured, "GE", true)
	var names []string
	var p driver.DHCPSnoopPortState
	for _, p = range ports {
		if !p.Trusted {
			t.Fatalf("%s is listed in the section and reads untrusted: presence IS the "+
				"flag on this firmware", p.Port)
		}
		names = append(names, p.Port)
	}
	if !reflect.DeepEqual(names, []string{"GE2", "GE10"}) {
		t.Fatalf("ports = %v, want [GE2 GE10]", names)
	}
}

// THE DISTINCTION THE CONTRACT INSISTS ON, and it survives — but only OUTSIDE
// the captured section. In a child-list fallback nothing establishes that a row
// means trusted, so a row with no readable flag was NOT READ, and "untrusted" is
// a protection claim that must not be invented.
func TestPortRowWithNoTrustFieldIsLeftOutOfTheFallbackBody(t *testing.T) {
	var body = snoopBody("DHCPSnoopingInterfaceISList",
		`<Entry><interfaceName>1</interfaceName><trustInterface>2</trustInterface></Entry>
<Entry><interfaceName>2</interfaceName><rateLimit>0</rateLimit></Entry>`)
	var ports = parseSnoopPorts(body, "GE", false)
	if len(ports) != 1 {
		t.Fatalf("ports = %+v; GE2's row carries no trust field and must be absent", ports)
	}
	if ports[0].Port != "GE1" || ports[0].Trusted {
		t.Fatalf("GE1 = %+v, want untrusted", ports[0])
	}
	var p driver.DHCPSnoopPortState
	for _, p = range ports {
		if p.Port == "GE2" {
			t.Fatal("GE2 was not read and appears in the list: \"not read\" has become " +
				"\"untrusted\", which is the confusion driver/dhcpsnoop.go forbids")
		}
	}
	// AND AN EXPLICIT FLAG IS STILL BELIEVED inside the captured section: a
	// firmware that does publish one must not be overridden by presence.
	var explicit = parseSnoopPorts(body, "GE", true)
	if len(explicit) != 2 {
		t.Fatalf("ports = %+v; presence makes GE2 trusted and GE1 keeps its served flag",
			explicit)
	}
	if explicit[0].Port != "GE1" || explicit[0].Trusted {
		t.Fatalf("GE1 = %+v: the served trustInterface=2 was overridden by presence",
			explicit[0])
	}
}

// The captured VLAN rows are `<VLAN>` wrappers, not `<Entry>` ones.
func TestParseSnoopVLANsReadsTheCapturedWrapper(t *testing.T) {
	var got = parseSnoopVLANs(snoopVLANRowsCaptured)
	if !reflect.DeepEqual(got, []int{10, 99}) {
		t.Fatalf("got %v, want [10 99] from <VLAN><VLANID>…</VLANID></VLAN> rows", got)
	}
}

// REGRESSION: the sort here was `out[i].Port < out[j].Port`, which puts GE10
// between GE1 and GE2 and shuffles a list an operator reads against a panel.
func TestPortsComeOutInFaceplateOrderNotLexicalOrder(t *testing.T) {
	var ports = parseSnoopPorts(snoopPortRows, "GE", false)
	var names []string
	var p driver.DHCPSnoopPortState
	for _, p = range ports {
		names = append(names, p.Port)
	}
	if !reflect.DeepEqual(names, []string{"GE1", "GE2", "GE10"}) {
		t.Fatalf("order = %v, want [GE1 GE2 GE10]: GE10 must not sort between GE1 and GE2",
			names)
	}
	// And the flags must have travelled with their ports.
	if ports[0].Trusted || !ports[1].Trusted || ports[2].Trusted {
		t.Fatalf("flags followed the wrong ports: %+v", ports)
	}
}

func TestSnoopPortLessIsNumericWithinAPrefix(t *testing.T) {
	if !snoopPortLess("GE2", "GE10") {
		t.Fatal("GE2 must precede GE10")
	}
	if snoopPortLess("GE10", "GE2") {
		t.Fatal("not a strict order")
	}
	if !snoopPortLess("GE48", "XG1") {
		t.Fatal("the copper block must precede the cages")
	}
	// A name with no trailing number must not panic and must still order.
	if snoopPortLess("lag", "lag") {
		t.Fatal("a prefix-only name compared unequal to itself")
	}
	if !snoopPortLess("GE1", "lag") {
		t.Fatal("mixed shapes must still produce a total order")
	}
}

func TestFacadeNamesUseTheSessionsPrefix(t *testing.T) {
	// The prefix is the session's, and a wrong one writes to the wrong socket.
	var ports = parseSnoopPorts(snoopPortRows, "XG", false)
	if ports[0].Port != "XG1" {
		t.Fatalf("first port = %q with prefix XG", ports[0].Port)
	}
	// interfaceName "1" is socket 1, not socket 0 and not socket 2.
	if devicePortName("GE1", "GE") != "1" {
		t.Fatalf("GE1 → %q, want 1", devicePortName("GE1", "GE"))
	}
	if facadePortName("1", "GE") != "GE1" {
		t.Fatalf("1 → %q, want GE1", facadePortName("1", "GE"))
	}
	if devicePortName("GE48", "GE") != "48" {
		t.Fatalf("GE48 → %q, want 48", devicePortName("GE48", "GE"))
	}
}

// --- the field-name machinery, which is what keeps a write honest here ---

func TestIonPickFieldSpeaksTheDevicesVocabulary(t *testing.T) {
	var block = `<snoopingEnabled>1</snoopingEnabled><enabled>2</enabled>`
	// Specific must win over generic: `enabled` is this UI's name for a dozen
	// unrelated things, and it is last in the alias list for that reason.
	var specific = ionPickField(block, snoopEnableFields)
	if specific != "snoopingEnabled" {
		t.Fatalf("picked %q, want snoopingEnabled", specific)
	}
	// None of them present: "" — never a default. A default is what produces the
	// statusCode 0 with no change.
	var absent = ionPickField(`<rateLimit>0</rateLimit>`, snoopEnableFields)
	if absent != "" {
		t.Fatalf("picked %q from a block carrying none of the aliases", absent)
	}
	var trust = ionPickField(`<trustPort>1</trustPort>`, snoopTrustFields)
	if trust != "trustPort" {
		t.Fatalf("picked %q, want trustPort", trust)
	}
}

func TestIonFieldsPresentIsTheSurveyMessage(t *testing.T) {
	var got = ionFieldsPresent(`<zeta>1</zeta><alpha>2</alpha><zeta>3</zeta><beta>4</beta>`)
	if !reflect.DeepEqual(got, []string{"alpha", "beta", "zeta"}) {
		t.Fatalf("got %v, want [alpha beta zeta] — sorted and deduplicated, because this "+
			"string is pasted into a TODO by whoever runs the first live probe", got)
	}
	if len(ionFieldsPresent("")) != 0 {
		t.Fatal("an empty body served fields")
	}
}

// --- the write bodies ---

func TestSnoopWriteBodiesUseTheUIsBooleanCodes(t *testing.T) {
	// _TRUE='1', _FALSE='2' (modules.js). The GLOBAL enable is the only place a
	// boolean is still sent — the two lists express themselves by presence.
	if snoopGlobalInner("DHCPSnoopingEnable", true) !=
		"<DHCPSnoopingEnable>1</DHCPSnoopingEnable>" {
		t.Fatalf("global on = %q", snoopGlobalInner("DHCPSnoopingEnable", true))
	}
	if ionBoolCode(false) != "2" {
		t.Fatalf("_FALSE = %q, want 2 — '0' is another UI's code and would be accepted "+
			"and ignored here", ionBoolCode(false))
	}
}

// THE CAPTURED WRITE BODIES, and this test is the guard against the refuted ones
// coming back: {VLAN:{VLANID:n}} and {Interface:{interfaceName:n}}, no wrapper
// called Entry and no flag of any kind.
func TestSnoopWriteRowsAreTheCapturedShapes(t *testing.T) {
	if snoopVLANInner(99) != "<VLAN><VLANID>99</VLANID></VLAN>" {
		t.Fatalf("VLAN row = %q", snoopVLANInner(99))
	}
	if snoopPortInner("28") != "<Interface><interfaceName>28</interfaceName></Interface>" {
		t.Fatalf("port row = %q", snoopPortInner("28"))
	}
	var row string
	for _, row = range []string{snoopVLANInner(99), snoopPortInner("28")} {
		if strings.Contains(row, "<Entry>") {
			t.Fatalf("an <Entry> wrapper came back: %q — the firmware serves VLAN and "+
				"Interface, and Entry is the shape that answered code 0 and changed "+
				"nothing", row)
		}
		var flag string
		for _, flag = range snoopTrustFields {
			if strings.Contains(row, "<"+flag+">") {
				t.Fatalf("%q carries %q — that is a UI COLUMN name this firmware never "+
					"serves, and writing it is the refuted shape", row, flag)
			}
		}
	}
}

// UNTRUST IS A DELETE. Writing the row with a flag false is what this file used
// to do, on a firmware that has no such flag.
func TestUntrustIsADeleteOfTheRow(t *testing.T) {
	if snoopPortAction(true) != "set" {
		t.Fatalf("trust action = %q, want set", snoopPortAction(true))
	}
	if snoopPortAction(false) != "delete" {
		t.Fatalf("untrust action = %q, want delete — a `set` of the same row is what "+
			"TRUSTS the port, so an untrust written that way is the opposite gesture",
			snoopPortAction(false))
	}
}

// THE PROHIBITION. Nothing this file builds may carry a disable for the feature
// or for a VLAN: only the per-port row has an off position, and it is expressed
// by the ACTION, never by the body.
func TestNoWriteBodyCanTurnTheFeatureOff(t *testing.T) {
	// The global builder's `on` argument is the only channel to the switch-wide
	// flag, and the apply calls it with true and nothing else. The VLAN builder
	// has no off form at all — no action argument, and a body that is only an id.
	var v int
	for _, v = range []int{1, 99, 4094} {
		var row = snoopVLANInner(v)
		var want = "<VLAN><VLANID>" + strconv.Itoa(v) + "</VLANID></VLAN>"
		if row != want {
			t.Fatalf("VLAN row = %q, want %q: any extra element here is a field this "+
				"firmware does not serve, and one of them would be a disable", row, want)
		}
	}
}

// --- verification: the 2xx that changes nothing ---

// A TRUST that left no row is the silent failure this platform is built around:
// the device answered code 0 and created nothing.
func TestVerifySnoopPortsCatchesTheRowThatWasNotCreated(t *testing.T) {
	var intents = []driver.DHCPSnoopPortIntent{{Port: "GE2", Trusted: true}}
	var after = driver.DHCPSnoopState{Enabled: true}
	var written, skipped = verifySnoopPorts([]string{"GE2"}, intents, after, nil)
	if len(written) != 0 {
		t.Fatalf("PortsWritten = %v while the re-read lists no such row", written)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "code 0") {
		t.Fatalf("skipped = %v", skipped)
	}
	// The same must hold when the row IS served with an explicit flag that reads
	// false — a firmware that publishes one must not slip through.
	var flagged = driver.DHCPSnoopState{Enabled: true,
		Ports: []driver.DHCPSnoopPortState{{Port: "GE2", Trusted: false}}}
	written, skipped = verifySnoopPorts([]string{"GE2"}, intents, flagged, nil)
	if len(written) != 0 || len(skipped) != 1 {
		t.Fatalf("written = %v, skipped = %v", written, skipped)
	}
}

// AN UNTRUST is confirmed by the row being GONE — the `delete` really removed
// it. A row still listed after a delete is the same silent failure, in the
// direction that leaves a port trusted while the screen says it is not.
func TestVerifySnoopPortsCatchesTheRowThatWasNotDeleted(t *testing.T) {
	var intents = []driver.DHCPSnoopPortIntent{{Port: "GE2", Trusted: false}}
	var after = driver.DHCPSnoopState{Enabled: true,
		Ports: []driver.DHCPSnoopPortState{{Port: "GE2", Trusted: true}}}
	var written, skipped = verifySnoopPorts([]string{"GE2"}, intents, after, nil)
	if len(written) != 0 {
		t.Fatalf("PortsWritten = %v while the port is still listed as trusted", written)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "DELETE") {
		t.Fatalf("skipped = %v — the message must name the gesture that did not take",
			skipped)
	}
}

func TestVerifySnoopPortsConfirmsWhatReallyMoved(t *testing.T) {
	var intents = []driver.DHCPSnoopPortIntent{
		{Port: "GE2", Trusted: true}, {Port: "GE3", Trusted: false}}
	// GE2 is listed — trusted. GE3 is ABSENT, which on this firmware IS the
	// answer "untrusted": the row was deleted, so there is nothing to serve.
	var after = driver.DHCPSnoopState{Enabled: true, Ports: []driver.DHCPSnoopPortState{
		{Port: "GE2", Trusted: true}}}
	var written, skipped = verifySnoopPorts([]string{"GE2", "GE3"}, intents, after, nil)
	if !reflect.DeepEqual(written, []string{"GE2", "GE3"}) {
		t.Fatalf("written = %v", written)
	}
	if len(skipped) != 0 {
		t.Fatalf("skipped = %v on an apply the re-read confirms", skipped)
	}
}

// The port writes were verified and the VLAN writes were not: their "enabled on
// VLAN n" note was written on the strength of a statusCode 0, which is this
// framework's silent failure.
func TestVerifySnoopVLANsCatchesTheVLANThatWasNotRetained(t *testing.T) {
	var after = driver.DHCPSnoopState{Enabled: true, VLANs: []int{10}}
	var got = verifySnoopVLANs([]int{10, 99}, after, "DHCPSnoopingVLANList")
	if len(got) != 1 {
		t.Fatalf("got %v; VLAN 99 was claimed and is not in the re-read", got)
	}
	if !strings.Contains(got[0], "99") || strings.Contains(got[0], "10,99") {
		t.Fatalf("the message must name only the lost VLAN: %q", got[0])
	}
	if !strings.Contains(got[0], "code 0") {
		t.Fatalf("the message does not name the failure mode: %q", got[0])
	}
	// Everything retained: silence.
	if len(verifySnoopVLANs([]int{10, 99},
		driver.DHCPSnoopState{VLANs: []int{10, 99, 200}}, "X")) != 0 {
		t.Fatal("VLANs the re-read confirms were reported as lost")
	}
	if len(verifySnoopVLANs(nil, after, "X")) != 0 {
		t.Fatal("an apply that claimed no VLAN reported a loss")
	}
}

// Two identical gestures must produce one sequence of writes. `missing` keeps
// the caller's order, so {99,10} and {10,99} used to write in two different
// orders — two transcripts that cannot be compared on a platform where
// comparing transcripts is how a shape gets learned.
func TestSnoopVLANWriteOrderIsSortedAndTheCallersSliceIsLeftAlone(t *testing.T) {
	var plan = []int{99, 10, 99, 2}
	var got = snoopSortedVLANs(plan)
	if !reflect.DeepEqual(got, []int{2, 10, 99}) {
		t.Fatalf("got %v, want [2 10 99]", got)
	}
	if !reflect.DeepEqual(plan, []int{99, 10, 99, 2}) {
		t.Fatalf("the caller's plan was reordered in place: %v", plan)
	}
	if !reflect.DeepEqual(snoopSortedVLANs([]int{10, 99}), snoopSortedVLANs([]int{99, 10})) {
		t.Fatal("the same set in two orders produced two write sequences")
	}
	if len(snoopSortedVLANs(nil)) != 0 {
		t.Fatal("nil produced a list")
	}
}

// --- the hazard note, and the refusals ---

func TestMgmtVLANWarningFiresOnlyWhenNothingIsTrusted(t *testing.T) {
	var on = driver.DHCPSnoopState{Enabled: true}
	if len(snoopMgmtVLANWarning(on, driver.DHCPSnoopPlan{})) != 1 {
		t.Fatal("snooping on, no trusted port anywhere, and no warning: this device can " +
			"lose its own DHCP lease on a range with no console")
	}
	// A trusted port in the PLAN counts, even if the re-read has not caught up.
	var planned = driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE28", Trusted: true}}}
	if len(snoopMgmtVLANWarning(on, planned)) != 0 {
		t.Fatal("the plan trusts the uplink and the warning fired anyway")
	}
	// A trusted port already on the device counts too.
	var hasTrusted = driver.DHCPSnoopState{Enabled: true,
		Ports: []driver.DHCPSnoopPortState{{Port: "GE28", Trusted: true}}}
	if len(snoopMgmtVLANWarning(hasTrusted, driver.DHCPSnoopPlan{})) != 0 {
		t.Fatal("a device with a trusted uplink got the warning")
	}
	// Feature off: nothing is dropped, so nothing to warn about.
	if len(snoopMgmtVLANWarning(driver.DHCPSnoopState{}, driver.DHCPSnoopPlan{})) != 0 {
		t.Fatal("the warning fired on a device that inspects nothing")
	}
}

// The apply must refuse before touching the device while the APPLIER has never
// run against one. Nothing here opens a session: WritesExtra is consulted first.
//
// THE BAR HAS MOVED TWICE AND THIS TEST MOVED WITH IT. It first read "not one
// section body has been captured"; one had. It then read "the row shapes are
// hypotheses"; they are captured too, from the UI's own write documents. Left as
// it was either time, the next reader would have gone off to redo work that was
// done. What is left is the ordinary bar every other range in this feature
// carries: a live run of the applier.
func TestApplyRefusesUntilTheApplierHasRunOnHardware(t *testing.T) {
	if WritesExtra("dhcp_snooping") {
		t.Fatal("WritableExtras announces dhcp_snooping while ApplyDHCPSnooping has " +
			"never run against a 1930 — the forms are captured, the applier is not " +
			"exercised, and on this range the run is service-affecting")
	}
	var a = &Admin{}
	var res, err = a.ApplyDHCPSnooping(context.Background(), driver.DHCPSnoopPlan{
		Ports: []driver.DHCPSnoopPortIntent{{Port: "GE28", Trusted: true}},
		VLANs: []int{99},
	})
	if err != nil {
		t.Fatalf("the refusal must be an ordinary result, not a Go error: %v", err)
	}
	if len(res.PortsWritten) != 0 {
		t.Fatalf("PortsWritten = %v on an apply that sent nothing", res.PortsWritten)
	}
	if len(res.Notes) == 0 || !strings.Contains(res.Notes[0], "not declared writable") {
		t.Fatalf("Notes = %v — the reason must be legible", res.Notes)
	}
	if !ionNoteHas(res.Notes, "nothing was sent to the device") {
		t.Fatalf("Notes = %v — an operator must be able to tell this from a silent no-op",
			res.Notes)
	}
	// AND IT MUST NAME THE RIGHT REASON. The note has twice named a gap that had
	// already been closed, sending whoever read it to redo a capture. The
	// remaining gap is the APPLIER, and the sentence has to say so.
	if !ionNoteHas(res.Notes, "APPLIER") {
		t.Fatalf("Notes = %v — the deferral must name the unexercised APPLIER as what "+
			"is missing, not the section names (captured 2026-08-09) and not the row "+
			"shapes (captured 2026-08-11)", res.Notes)
	}
	// The refusal is still declared as steps: the transcript is what an operator
	// reads when the screen offered a control that did nothing.
	if len(a.Steps) == 0 {
		t.Fatal("the deferral left no trace in the transcript")
	}
}

func TestReadRefusesToReportTheFeatureOffWhenNoSectionAnswered(t *testing.T) {
	// snoopSections.found() is the guard, and it must be false for a zero value:
	// a zero DHCPSnoopState reads as "snooping is off", which is the one
	// sentence an operator acts on.
	var empty snoopSections
	if empty.found() {
		t.Fatal("a probe that found nothing reports found()")
	}
	var one = snoopSections{port: "DHCPSnoopingInterfaceISList"}
	if !one.found() {
		t.Fatal("a probe that found the port section reports nothing found")
	}
}

// Reading may fall back to the global body for the child lists; WRITING may not.
func TestChildListFallbackIsReadOnly(t *testing.T) {
	var loc = snoopSections{
		global: "DHCPSnooping",
		globalBody: snoopBody("DHCPSnooping",
			`<DHCPSnoopingEnable>1</DHCPSnoopingEnable>
<Entry><VLANID>99</VLANID></Entry>
<Entry><interfaceName>28</interfaceName><trustInterface>1</trustInterface></Entry>`),
	}
	var st = snoopStateOf(loc, "GE")
	if !st.Enabled {
		t.Fatal("the one-section shape was not read")
	}
	if !reflect.DeepEqual(st.VLANs, []int{99}) {
		t.Fatalf("VLANs = %v, want [99] from the child list", st.VLANs)
	}
	if len(st.Ports) != 1 || st.Ports[0].Port != "GE28" || !st.Ports[0].Trusted {
		t.Fatalf("Ports = %+v", st.Ports)
	}
	// But the write targets only a section the device served under its OWN name:
	// vlan and port are empty here, so the apply must refuse rather than post
	// into the global section.
	if loc.vlan != "" || loc.port != "" {
		t.Fatal("the fallback leaked into the write targets")
	}
}

func TestAdminIsSeenAsSnoopReaderAndApplier(t *testing.T) {
	var a = &Admin{}
	var _, okR = driver.AsDHCPSnoopReader(a)
	var _, okA = driver.AsDHCPSnoopApplier(a)
	if !okR || !okA {
		t.Fatalf("reader=%v applier=%v", okR, okA)
	}
}

func ionNoteHas(notes []string, needle string) bool {
	var n string
	for _, n = range notes {
		if strings.Contains(n, needle) {
			return true
		}
	}
	return false
}

// CAPTURED, verbatim, from the lab 1930 (JL685A) on 2026-08-09 — the body
// `DHCPSnoopingGlobalSetting` really served, not a constructed one.
//
// Everything above this line in the fixtures is CONSTRUCTED: a real envelope
// wrapped around hypothetical field names. This is the first of these sections
// whose contents came off a device, and it settles three things that were
// guesses:
//
//   - `DHCPSnoopingEnable` is the right name, and it was already this
//     connector's leading alias;
//   - the 1/2 encoding is right, and 2 is OFF. This is the lab unit's factory
//     state, so the connector reporting `enabled=false` from it is correct;
//   - three field names nobody had: `backupDatabaseEnable`,
//     `verifyMACAddressEnable` and `passThroughOption82Enable`. Note the last
//     two: the constructed fixtures above call it `verifyMAC`, which this
//     firmware does NOT serve.
//
// What it does NOT settle: the ENABLED shape. Both list sections came back with
// no children, so the per-VLAN and per-port entry shapes are still hypotheses.
const snoopGlobalCaptured = `<?xml version="1.0" encoding="UTF-8" ?>
<ResponseData>
<DeviceConfiguration>
<version>1.0</version>
<DHCPSnoopingGlobalSetting type="section">
<DHCPSnoopingEnable>2</DHCPSnoopingEnable>
<backupDatabaseEnable>2</backupDatabaseEnable>
<verifyMACAddressEnable>1</verifyMACAddressEnable>
<passThroughOption82Enable>2</passThroughOption82Enable>
</DHCPSnoopingGlobalSetting>
</DeviceConfiguration>
<ActionStatus>
<requestURL>DHCPSnoopingGlobalSetting</requestURL>
<statusCode></statusCode>
</ActionStatus>
</ResponseData>`

// The empty list sections, captured in the same pass. An EMPTY section is not a
// missing one, and the difference matters: `sectionMissing` must accept these,
// or a device with snooping switched off would report as unquestionable.
const snoopVLANListCapturedEmpty = `<?xml version="1.0" encoding="UTF-8" ?>
<ResponseData>
<DeviceConfiguration>
<version>1.0</version>
<DHCPSnoopingVLANList type="section">
</DHCPSnoopingVLANList>
</DeviceConfiguration>
<ActionStatus>
<requestURL>DHCPSnoopingVLANList</requestURL>
<statusCode></statusCode>
</ActionStatus>
</ResponseData>`

// TestCapturedGlobalSectionReadsAsOff pins the parse of the real body.
func TestCapturedGlobalSectionReadsAsOff(t *testing.T) {
	var loc = snoopSections{
		global: "DHCPSnoopingGlobalSetting", globalBody: snoopGlobalCaptured,
		vlan: "DHCPSnoopingVLANList", vlanBody: snoopVLANListCapturedEmpty,
	}
	var st = snoopStateOf(loc, "GE")
	if st.Enabled {
		t.Error("the captured body carries DHCPSnoopingEnable=2 and reads as ENABLED — " +
			"this is the lab unit's factory state and it is OFF")
	}
	if len(st.VLANs) != 0 {
		t.Errorf("VLANs = %v from an empty list section", st.VLANs)
	}
}

// TestCapturedEmptyListIsNotAMissingSection: an empty section ANSWERED. Treating
// it as missing would make ReadDHCPSnooping refuse on a healthy switch whose
// only fault is having the feature turned off.
func TestCapturedEmptyListIsNotAMissingSection(t *testing.T) {
	if sectionMissing(snoopVLANListCapturedEmpty, "DHCPSnoopingVLANList") {
		t.Error("the captured empty VLAN list is treated as a missing section: " +
			"a switch with snooping off would report as unquestionable")
	}
	if sectionMissing(snoopGlobalCaptured, "DHCPSnoopingGlobalSetting") {
		t.Error("the captured global section is treated as missing")
	}
}
