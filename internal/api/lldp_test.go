package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"meshcraft/internal/model"
	"meshcraft/internal/snmppoll"
)

// LLDP neighborhood decisions, exercised WITHOUT a database — the package convention
// (cf. proxyscope_test.go, trapdest_test.go). All values come from the July 29, 2026
// survey (docs/en/01-Architecture/LLDP neighbours.md).
//
// Device identifiers are from the reference fleet, truncated for readability but
// recognizable: that is what lets you reread a test failure next to a real walk.

const (
	idCRS  = "ec1057ea-d2d4-4ca0-ad16-cef54803e99d" // lab-crs326, RouterOS
	idCSS  = "7cf5eee4-d904-4f68-a206-7219b248d23a" // lab-css326, SwOS — NO LLDP
	id193  = "ef79223c-f0f4-4b75-9816-647c7d20216b" // sw-labo-1930, Instant On
	idSite = "7f0d238d-f180-440d-be56-cd1a4bcbb225"
)

// deviceCRS / device1930 / deviceCSS reproduce the REAL facades from the fleet, as
// `/api/sites/{id}/devices` returns them. The facade is what decides remote port attachment:
// inventing it would make the `resolvePeerPort` test worthless.
func deviceCRS() model.Device {
	return model.Device{
		ID: idCRS, SiteID: idSite, Name: "lab-crs326", Model: "CRS326-24G-2S+",
		PortGroups: json.RawMessage(`[
			{"prefix":"ether","from":1,"count":24,"speed":"1G"},
			{"prefix":"sfp-sfpplus","from":1,"count":2,"speed":"10G"}]`),
	}
}

func device1930() model.Device {
	// Two groups with the SAME "GE" prefix: 1..48 copper, 49..52 SFP+. That is why "50",
	// the label the CRS326 read, resolves to "GE50" by the third `resolve` scale (facade
	// prefix + label).
	return model.Device{
		ID: id193, SiteID: idSite, Name: "sw-labo-1930", Model: "JL685A",
		PortGroups: json.RawMessage(`[
			{"prefix":"GE","from":1,"count":48,"speed":"1G"},
			{"prefix":"GE","from":49,"count":4,"speed":"10G"}]`),
	}
}

func deviceCSS() model.Device {
	return model.Device{
		ID: idCSS, SiteID: idSite, Name: "lab-css326", Model: "CSS326-24G-2S+",
		PortGroups: json.RawMessage(`[
			{"prefix":"Port","from":1,"count":24,"speed":"1G"},
			{"prefix":"SFP","from":1,"count":2,"speed":"10G"}]`),
	}
}

// TestChassisNormalization: the measured fact that makes this function indispensable is
// the first case — the SAME chassis served as text by one and as raw bytes by the other.
func TestChassisNormalization(t *testing.T) {
	var cas = []struct {
		name     string
		input    string
		expected string
	}{
		{"MikroTik text (what the CRS326 announces about itself)",
			"B8:69:F4:5B:A0:D2", "b869f45ba0d2"},
		{"raw bytes (what the 1930 read from the SAME chassis)",
			"0xb869f45ba0d2", "b869f45ba0d2"},
		{"hyphen separator", "b8-69-f4-5b-a0-d2", "b869f45ba0d2"},
		{"Cisco dotted notation", "b869.f45b.a0d2", "b869f45ba0d2"},
		{"surrounding spaces", "  B8:69:F4:5B:A0:D2  ", "b869f45ba0d2"},
		{
			// Chassis is not always a MAC: the MIB enumerates seven subtypes, including
			// `local(7)`, a free string. Blindly reducing to hex digits would have turned
			// this name into "bab12" — and two devices whose names share only a-f letters
			// would have been declared identical.
			"local(7) subtype: a name, not a MAC — must NOT be reduced",
			"room-b-rack-12", "room-b-rack-12",
		},
		{"twelve hex characters but with a letter outside a-f",
			"deadbeefcafz", "deadbeefcafz"},
		{"eleven hex digits: not a MAC, kept as-is", "b869f45ba0d", "b869f45ba0d"},
		{"empty", "", ""},
	}
	var i int
	for i = range cas {
		var got = normalizeChassisID(cas[i].input)
		if got != cas[i].expected {
			t.Errorf("%s: %q → %q, expected %q", cas[i].name, cas[i].input, got,
				cas[i].expected)
		}
	}
	// The property that really matters, stated as a property: the two writings of the same
	// chassis match. That is what decides whether the fleet's only bilateral link is
	// recognized.
	if normalizeChassisID("B8:69:F4:5B:A0:D2") != normalizeChassisID("0xb869f45ba0d2") {
		t.Fatal("the two encodings read from the same chassis must match")
	}
}

// selfCRS / self1930: the identity each device ANNOUNCES, as the survey gives it.
func selfCRS() model.LLDPSelf {
	return model.LLDPSelf{
		DeviceID: idCRS, SiteID: idSite,
		ChassisID: "B8:69:F4:5B:A0:D2", SysName: "lan-r4",
		PortIDs: []string{"sfp-sfpplus2", "vlan99"},
		Served:  true,
	}
}

func self1930() model.LLDPSelf {
	return model.LLDPSelf{
		DeviceID: id193, SiteID: idSite,
		// DECLARED chassis ends with "a7"…
		ChassisID: "0x02005e1061a7", SysName: "CN0EXAMPL1",
		// …but frames emitted on port 50 carry "d9", which is here.
		PortIDs: []string{"0x02005e1061d9", "0x02005e1061db", "51"},
		Served:  true,
	}
}

// TestRecognitionByChassis: the strong path, on the surveyed case.
func TestRecognitionByChassis(t *testing.T) {
	var idx = buildLLDPIdentity([]model.LLDPSelf{selfCRS(), self1930()},
		[]model.Device{deviceCRS(), device1930()})

	// What the 1930 read on port 50: a chassis in raw bytes.
	var seen = model.LLDPNeighbor{
		DeviceID: id193, LocalPort: "GE50",
		ChassisID: "0xb869f45ba0d2", PortID: "sfp-sfpplus2", SysName: "lan-r4",
	}
	var peer, via = identifyPeer(idx, seen)
	if peer != idCRS {
		t.Fatalf("recognized device %q, expected CRS326: text chassis it announces must "+
			"match raw bytes the 1930 read", peer)
	}
	if via != lldpViaChassis {
		t.Errorf("path %q, expected %q", via, lldpViaChassis)
	}
	// And its PORT, resolved in the CRS326 facade by exact match.
	var port = resolvePeerPort(idx, peer, seen)
	if port != "sfp-sfpplus2" {
		t.Errorf("resolved remote port %q, expected \"sfp-sfpplus2\"", port)
	}
}

// TestRecognitionByPortID: the path that saves the fleet's only bilateral link, without
// which it would appear unilateral.
//
// The CRS326 recorded a neighbor with chassis "02:00:5e:10:61:D9". That value appears
// NOWHERE in the 1930's declared identity (`0x02005e1061a7`): it is port 50's MAC. Only
// `lldp_self.port_ids` enables the match.
func TestRecognitionByPortID(t *testing.T) {
	var idx = buildLLDPIdentity([]model.LLDPSelf{selfCRS(), self1930()},
		[]model.Device{deviceCRS(), device1930()})

	var seen = model.LLDPNeighbor{
		DeviceID: idCRS, LocalPort: "sfp-sfpplus2",
		ChassisID: "02:00:5e:10:61:D9", PortID: "50", SysName: "CN0EXAMPL1",
	}
	var peer, via = identifyPeer(idx, seen)
	if peer != id193 {
		t.Fatalf("recognized device %q, expected 1930: read chassis is port 50 MAC, which "+
			"appears in its lldpLocPortId values", peer)
	}
	if via != lldpViaPortID {
		t.Errorf("path %q, expected %q — the operator must be able to read they are looking "+
			"at oddly announcing firmware", via, lldpViaPortID)
	}
	// AND ITS PORT IS DELIBERATELY NOT RESOLVED, since 2026-08-10.
	//
	// "50" used to become GE50 by `resolve`'s third scale, facade prefix + label.
	// That scale now belongs to the metrics caller alone (cf.
	// facade.resolveNeighborPort): what a NEIGHBOUR puts in lldpRemPortId is not
	// a port number by any rule — the same fleet walk found a Juniper announcing
	// "521" for a port really called ge-0/0/9.0 — and refutation being port-exact,
	// a confidently wrong far port can retire a cable that exists.
	//
	// So the far end of this cable is now UNNAMED rather than named on the
	// strength of the 1930's facade being big enough. Recognition of the DEVICE,
	// which is what this test is named for, is untouched.
	var port = resolvePeerPort(idx, peer, seen)
	if port != "" {
		t.Errorf("resolved remote port %q, expected \"\": a bare ifIndex from a "+
			"neighbour must no longer be concatenated onto the facade prefix", port)
	}
}

// TestRecognitionBySysName: last resort, and it compares the ANNOUNCED name, not the
// meshcraft name.
func TestRecognitionBySysName(t *testing.T) {
	var idx = buildLLDPIdentity([]model.LLDPSelf{selfCRS()},
		[]model.Device{deviceCRS()})

	// A chassis in a form we cannot match, but the right announced name.
	var seen = model.LLDPNeighbor{
		DeviceID: id193, LocalPort: "GE50",
		ChassisID: "something-unknown", SysName: "lan-r4",
	}
	var peer, via = identifyPeer(idx, seen)
	if peer != idCRS || via != lldpViaSysName {
		t.Fatalf("recognized %q by %q, expected CRS326 by system name", peer, via)
	}

	// And the reverse, which is the trap: the MESHCRAFT name must recognize nothing. The
	// survey is unambiguous — `lab-crs326` announces "lan-r4", `sw-labo-1930` announces
	// by serial. Comparing to the meshcraft name would have matched no fleet device.
	var byMeshcraftName = model.LLDPNeighbor{SysName: "lab-crs326"}
	if peer, _ = identifyPeer(idx, byMeshcraftName); peer != "" {
		t.Errorf("meshcraft name %q must recognize nothing: that is not what the device "+
			"announces", "lab-crs326")
	}
}

// TestAmbiguousTokenDiscarded: two devices announcing the same system name must not have
// each other's neighbors attributed. Matching at random is worse than not matching (same
// doctrine as `facade.norm`).
func TestAmbiguousTokenDiscarded(t *testing.T) {
	var a = model.LLDPSelf{DeviceID: idCRS, SysName: "switch", Served: true}
	var b = model.LLDPSelf{DeviceID: id193, SysName: "switch", Served: true}
	var idx = buildLLDPIdentity([]model.LLDPSelf{a, b},
		[]model.Device{deviceCRS(), device1930()})

	var peer, _ = identifyPeer(idx, model.LLDPNeighbor{SysName: "switch"})
	if peer != "" {
		t.Errorf("shared system name was attributed to %q: it should have been NEUTRALIZED",
			peer)
	}
	// And a third device declaring the same name must not recover it: neutralization is
	// permanent, not deletion.
	var c = model.LLDPSelf{DeviceID: idCSS, SysName: "switch", Served: true}
	idx = buildLLDPIdentity([]model.LLDPSelf{a, b, c},
		[]model.Device{deviceCRS(), device1930(), deviceCSS()})
	if peer, _ = identifyPeer(idx, model.LLDPNeighbor{SysName: "switch"}); peer != "" {
		t.Errorf("neutralized token was taken by %q", peer)
	}
}

// TestNonMACPortIDNotIndexed: an `lldpLocPortId` of "45" or "ether3" identifies nothing
// beyond its device. Indexing it would recognize as the 1930 any neighbor whose chassis is
// called "45".
func TestNonMACPortIDNotIndexed(t *testing.T) {
	var idx = buildLLDPIdentity([]model.LLDPSelf{self1930()},
		[]model.Device{device1930()})
	var peer, _ = identifyPeer(idx, model.LLDPNeighbor{ChassisID: "51"})
	if peer != "" {
		t.Errorf("1930 local port \"51\" was used to recognize a neighbor (%q): only MAC-"+
			"shaped port identifiers are indexed", peer)
	}
	// And "sfp-sfpplus2", a CRS326 lldpLocPortId, must not identify anything either.
	idx = buildLLDPIdentity([]model.LLDPSelf{selfCRS()}, []model.Device{deviceCRS()})
	if peer, _ = identifyPeer(idx, model.LLDPNeighbor{ChassisID: "sfp-sfpplus2"}); peer != "" {
		t.Errorf("\"sfp-sfpplus2\" identified %q", peer)
	}
}

// TestUnresolvableRemotePort: a label the neighbor's facade does not contain leaves the
// port empty, and the link will stay "seen from one side". Assumed UNDER-ASSERTION:
// inventing a name would draw a cable on the wrong port of the right facade.
func TestUnresolvableRemotePort(t *testing.T) {
	var idx = buildLLDPIdentity([]model.LLDPSelf{selfCRS()},
		[]model.Device{deviceCRS()})
	var seen = model.LLDPNeighbor{
		ChassisID: "0xb869f45ba0d2",
		// What the 1930 serves in column 8 for this neighbor: a bridge membership name,
		// which is NOT a MikroTik facade port.
		PortID:   "",
		PortDesc: "bridge/sfp-sfpplus2",
	}
	var port = resolvePeerPort(idx, idCRS, seen)
	if port != "" {
		t.Errorf("resolved port %q: \"bridge/…\" is not a facade port, it should have been "+
			"dropped", port)
	}
}

// --- Link derivation ---

var tLLDP = time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)

// TestLinkSeenFromBothSides replays the reference fleet's ONLY bilateral link:
// lab-crs326:sfp-sfpplus2 ↔ sw-labo-1930:GE50, each seeing the other.
func TestLinkSeenFromBothSides(t *testing.T) {
	var seen = []model.LLDPNeighbor{
		{
			DeviceID: idCRS, LocalPort: "sfp-sfpplus2",
			ChassisID: "02:00:5e:10:61:D9", PortID: "50", SysName: "CN0EXAMPL1",
			PeerDevice: id193, PeerPort: "GE50", PeerVia: lldpViaPortID,
			FirstSeen: tLLDP, LastSeen: tLLDP.Add(2 * time.Minute),
		},
		{
			DeviceID: id193, LocalPort: "GE50",
			ChassisID: "0xb869f45ba0d2", PortID: "sfp-sfpplus2", SysName: "lan-r4",
			PeerDevice: idCRS, PeerPort: "sfp-sfpplus2", PeerVia: lldpViaChassis,
			// This side saw the link EARLIER and saw it again LATER.
			FirstSeen: tLLDP.Add(-time.Hour), LastSeen: tLLDP.Add(3 * time.Minute),
		},
	}
	var links = deriveLLDPLinks(seen)
	if len(links) != 1 {
		t.Fatalf("%d links, expected 1: two reciprocal observations make ONE link, not two",
			len(links))
	}
	if !links[0].BothSides {
		t.Error("both sides see each other: BothSides must be true")
	}
	// Dates merge: the link exists since the OLDEST of the two observations, and was
	// confirmed at the most RECENT.
	if !links[0].FirstSeen.Equal(tLLDP.Add(-time.Hour)) {
		t.Errorf("FirstSeen %v, expected oldest of the two (%v)",
			links[0].FirstSeen, tLLDP.Add(-time.Hour))
	}
	if !links[0].LastSeen.Equal(tLLDP.Add(3 * time.Minute)) {
		t.Errorf("LastSeen %v, expected most recent of the two", links[0].LastSeen)
	}
}

// TestOneSidedLinkIsNormal: this is the ORDINARY case, not an anomaly. An entire fleet
// platform does not speak 802.1AB, so every link touching it will only ever be seen from
// the other side — and it must still be DRAWN.
func TestOneSidedLinkIsNormal(t *testing.T) {
	var seen = []model.LLDPNeighbor{{
		DeviceID: idCRS, LocalPort: "sfp-sfpplus1",
		ChassisID: "04:F4:1C:1D:74:5F", PortID: "bridge/sfp-sfpplus1",
		SysName: "RB5009UG", FirstSeen: tLLDP, LastSeen: tLLDP,
	}}
	var links = deriveLLDPLinks(seen)
	if len(links) != 1 {
		t.Fatalf("%d links: an unrecognized neighbor is still TRACED, with the name it gave "+
			"itself", len(links))
	}
	if links[0].BothSides {
		t.Error("a single observation cannot be bilateral")
	}
	if links[0].BDevice != "" {
		t.Errorf("BDevice %q: this neighbor is in no fleet device", links[0].BDevice)
	}
	if links[0].BLabel != "RB5009UG" {
		t.Errorf("BLabel %q: announced name is the only thing left to display",
			links[0].BLabel)
	}
	if links[0].BChassisID != "04:F4:1C:1D:74:5F" {
		t.Errorf("BChassisID %q: kept so the operator can adopt the device",
			links[0].BChassisID)
	}
}

// TestMultiNeighborPortKeepsUniqueReciprocal: measured CRS326 case — one port hears
// several remotes through a transparent switch, but exactly one (the Instant On) also
// names that port back. Keep that bilateral edge; drop the rest (RB5009/CRS305-style).
func TestMultiNeighborPortKeepsUniqueReciprocal(t *testing.T) {
	var seen = []model.LLDPNeighbor{
		{
			DeviceID: idCRS, LocalPort: "sfp-sfpplus2", ChassisID: "02:00:5e:10:61:D9",
			SysName: "CN0EXAMPL1", PeerDevice: id193, PeerPort: "GE50",
			FirstSeen: tLLDP, LastSeen: tLLDP,
		},
		{
			DeviceID: idCRS, LocalPort: "sfp-sfpplus2", ChassisID: "d4:01:c3:ab:32:f6",
			SysName: "CRS305", PeerDevice: idCSS, PeerPort: "SFP2",
			FirstSeen: tLLDP, LastSeen: tLLDP,
		},
		{
			DeviceID: id193, LocalPort: "GE50", ChassisID: "0xb869f45ba0d2",
			PeerDevice: idCRS, PeerPort: "sfp-sfpplus2", FirstSeen: tLLDP, LastSeen: tLLDP,
		},
	}
	var links = deriveLLDPLinks(seen)
	if len(links) != 1 {
		t.Fatalf("%d links, expected 1: unique reciprocal peer on a flood port", len(links))
	}
	if !links[0].BothSides {
		t.Error("unique reciprocal flood peer must be bilateral")
	}
	if links[0].ADevice != idCRS || links[0].BDevice != id193 {
		t.Errorf("got %s→%s, expected CRS→1930", links[0].ADevice, links[0].BDevice)
	}
}

// TestMultiNeighborPortNoReciprocal: flood speakers with nobody answering back — no edge.
func TestMultiNeighborPortNoReciprocal(t *testing.T) {
	var seen = []model.LLDPNeighbor{
		{
			DeviceID: idCRS, LocalPort: "sfp-sfpplus2", ChassisID: "04:F4:1C:1D:74:5F",
			SysName: "RB5009UG", FirstSeen: tLLDP, LastSeen: tLLDP,
		},
		{
			DeviceID: idCRS, LocalPort: "sfp-sfpplus2", ChassisID: "d4:01:c3:ab:32:f6",
			SysName: "CRS305", FirstSeen: tLLDP, LastSeen: tLLDP,
		},
	}
	var links = deriveLLDPLinks(seen)
	if len(links) != 0 {
		t.Fatalf("%d links, expected 0: flood without reciprocity is not a cable", len(links))
	}
}

// TestExactReciprocity: two cables between the same two devices — every redundant uplink
// in the real world. Each pair must confirm with the RIGHT half.
func TestExactReciprocity(t *testing.T) {
	var seen = []model.LLDPNeighbor{
		{DeviceID: idCRS, LocalPort: "sfp-sfpplus1", PeerDevice: id193, PeerPort: "GE49",
			FirstSeen: tLLDP, LastSeen: tLLDP},
		{DeviceID: idCRS, LocalPort: "sfp-sfpplus2", PeerDevice: id193, PeerPort: "GE50",
			FirstSeen: tLLDP, LastSeen: tLLDP},
		{DeviceID: id193, LocalPort: "GE49", PeerDevice: idCRS, PeerPort: "sfp-sfpplus1",
			FirstSeen: tLLDP, LastSeen: tLLDP},
		{DeviceID: id193, LocalPort: "GE50", PeerDevice: idCRS, PeerPort: "sfp-sfpplus2",
			FirstSeen: tLLDP, LastSeen: tLLDP},
	}
	var links = deriveLLDPLinks(seen)
	if len(links) != 2 {
		t.Fatalf("%d links, expected 2: two distinct cables", len(links))
	}
	var i int
	for i = range links {
		if !links[i].BothSides {
			t.Errorf("link %s/%s ↔ %s/%s not confirmed", links[i].ADevice, links[i].APort,
				links[i].BDevice, links[i].BPort)
		}
	}

	// Counter-example: same devices, but ports do NOT correspond (each thinks it sees the
	// other on its other port). Nothing must confirm — exactly what the broader rule "A and
	// B see each other" would have missed.
	var crossed = []model.LLDPNeighbor{
		{DeviceID: idCRS, LocalPort: "sfp-sfpplus1", PeerDevice: id193, PeerPort: "GE50",
			FirstSeen: tLLDP, LastSeen: tLLDP},
		{DeviceID: id193, LocalPort: "GE49", PeerDevice: idCRS, PeerPort: "sfp-sfpplus2",
			FirstSeen: tLLDP, LastSeen: tLLDP},
	}
	links = deriveLLDPLinks(crossed)
	for i = range links {
		if links[i].BothSides {
			t.Error("two observations that do not designate each other must NOT confirm")
		}
	}
}

// TestLinkOrderIsStable: the weathermap must receive the same response for the same
// observations, otherwise it redraws a plan that did not move.
func TestLinkOrderIsStable(t *testing.T) {
	var seen = []model.LLDPNeighbor{
		{DeviceID: idCRS, LocalPort: "sfp-sfpplus2", ChassisID: "aaa", FirstSeen: tLLDP, LastSeen: tLLDP},
		{DeviceID: idCRS, LocalPort: "sfp-sfpplus1", ChassisID: "bbb", FirstSeen: tLLDP, LastSeen: tLLDP},
		{DeviceID: id193, LocalPort: "GE50", ChassisID: "ccc", FirstSeen: tLLDP, LastSeen: tLLDP},
	}
	var a = deriveLLDPLinks(seen)
	var b = deriveLLDPLinks(seen)
	var i int
	for i = range a {
		if a[i] != b[i] {
			t.Fatalf("position %d: unstable order", i)
		}
	}
	if a[0].APort != "sfp-sfpplus1" {
		t.Errorf("first link on %q: sort must be deterministic", a[0].APort)
	}
}

// TestRemotePortLabel: a MAC-shaped port identifier teaches nothing on a port cell; portDesc
// replaces it. Measured case on 1930 ports 50 and 52.
func TestRemotePortLabel(t *testing.T) {
	var got = peerPortLabel(model.LLDPNeighbor{PortID: "sfp-sfpplus2"})
	if got != "sfp-sfpplus2" {
		t.Errorf("readable portID: %q", got)
	}
	got = peerPortLabel(model.LLDPNeighbor{
		PortID: "0x02005e1061d9", PortDesc: "50"})
	if got != "50" {
		t.Errorf("MAC portID: %q, expected \"50\"", got)
	}
	got = peerPortLabel(model.LLDPNeighbor{PortDesc: "Eth 1/28"})
	if got != "Eth 1/28" {
		t.Errorf("absent portID: %q", got)
	}
	got = peerPortLabel(model.LLDPNeighbor{})
	if got != "" {
		t.Errorf("nothing at all: %q — do not invent a dash", got)
	}
}

// --- The view rendered on screen ---

// TestSilenceDistinguishesThreeCauses: "zero neighbors" has three causes repaired by
// OPPOSITE actions, and confusing them would send someone looking for a meshcraft defect
// where the firmware lacks the feature.
//
// The third — `platform_unsupported` — was added rereading the real path: collection turns
// on only if the connector declares an SNMP `lldp` channel, so SwOS is NEVER interrogated
// and would never have an `lldp_self` row. It would have presented as "a collector is
// missing", i.e. an action where there is nothing to do.
func TestSilenceDistinguishesThreeCauses(t *testing.T) {
	// The CSS326 carries its connector in `extra`, like the real fleet: that is how we find
	// the `MetricUnavailable{Metric: "lldp"}` declaration from `swos`.
	var css = deviceCSS()
	css.Extra = json.RawMessage(`{"driver":"mikrotik-swos"}`)
	// And a device whose connector can render LLDP.
	var crs = deviceCRS()
	crs.Extra = json.RawMessage(`{"driver":"mikrotik-routeros"}`)

	var devices = []model.Device{crs, css, device1930()}
	var selves = []model.LLDPSelf{selfCRS()}
	var view = buildLLDPView(nil, selves, devices)

	var reason = map[string]string{}
	var detail = map[string]string{}
	var i int
	for i = range view.Silent {
		reason[view.Silent[i].DeviceID] = view.Silent[i].Reason
		detail[view.Silent[i].DeviceID] = view.Silent[i].Detail
	}
	if reason[idCSS] != lldpSilencePlatform {
		t.Errorf("CSS326: reason %q, expected %q — its connector DECLARES SwOS does not "+
			"speak LLDP, so it is never interrogated and \"a collector is missing\" would be "+
			"a misconception", reason[idCSS], lldpSilencePlatform)
	}
	// Connector survey Source must come through as technical detail.
	if !strings.Contains(detail[idCSS], "SwOS") {
		t.Errorf("CSS326: detail %q — connector survey source must come through",
			detail[idCSS])
	}
	if reason[id193] != lldpSilenceNeverPoll {
		t.Errorf("1930: reason %q, expected %q — its platform can render LLDP, it lacks a "+
			"collector, and that is repairable", reason[id193], lldpSilenceNeverPoll)
	}
	var _, silent = reason[idCRS]
	if silent {
		t.Error("CRS326 serves the MIB and has a snapshot: it must not appear in silences")
	}

	// Third cause: interrogated, and nothing returned. Distinct from the first because for
	// US it is not the same thing — connector declaration to fix, or LLDP disabled on device.
	var polledButMute = buildLLDPView(nil,
		[]model.LLDPSelf{{DeviceID: id193, SiteID: idSite, Served: false, SeenAt: tLLDP}},
		[]model.Device{device1930()})
	if len(polledButMute.Silent) != 1 ||
		polledButMute.Silent[0].Reason != lldpSilenceNotServed {
		t.Fatalf("interrogated device returning nothing must be %q, got %+v",
			lldpSilenceNotServed, polledButMute.Silent)
	}
	// And it ALONE carries a date: the only of the three cases where a snapshot exists.
	if polledButMute.Silent[0].SeenAt == nil {
		t.Error("observed refusal must carry the snapshot date that established it")
	}
	for i = range view.Silent {
		if view.Silent[i].SeenAt != nil {
			t.Errorf("%s: date on never-interrogated device — we must not claim to know since "+
				"when it is mute", view.Silent[i].DeviceID)
		}
	}
}

// TestSilenceSeparatesDeviceRefusalFromUnservedMIB: the FOURTH cause, and the one
// that cost a whole switch.
//
// sw01-arena (FS S3400-48T4SP, measured 2026-10-25) carries a live 20 Gb/s uplink
// to the site core and was drawn on the plan with NO LINK AT ALL. Its snapshot was
// `source='cli', served=false`: the read-state CLI gesture asked, and the device
// answered "LLDP is not enabled". The screen reported `not_served`, whose
// published help sentence opens with "it was interrogated and returned nothing
// under 1.0.8802" — and this firmware serves nothing under 1.0.8802 BY DESIGN
// (cf. deviceKnownMACs), so that sentence sent the reader to an OID that was never
// queried while the actionable fact, the device's own refusal, went unsaid.
//
// Two devices, same Served=false, DIFFERENT source, and the reasons must diverge:
// one is repaired on the device in one command, the other is a connector
// declaration to re-survey.
func TestSilenceSeparatesDeviceRefusalFromUnservedMIB(t *testing.T) {
	var cliRefusal = buildLLDPView(nil,
		[]model.LLDPSelf{{DeviceID: id193, SiteID: idSite, Served: false,
			SeenAt: tLLDP, Source: lldpSourceCLI}},
		[]model.Device{device1930()})
	if len(cliRefusal.Silent) != 1 ||
		cliRefusal.Silent[0].Reason != lldpSilenceDisabled {
		t.Fatalf("a CLI snapshot with Served=false is the DEVICE saying LLDP is off: "+
			"expected %q, got %+v", lldpSilenceDisabled, cliRefusal.Silent)
	}
	// The date is what an operator who has just turned LLDP on compares against.
	if cliRefusal.Silent[0].SeenAt == nil {
		t.Error("the device's refusal must carry the snapshot date that recorded it")
	}

	// Same emptiness reached through SNMP keeps the old word: there, the empty
	// 1.0.8802 subtree really is all we know.
	var snmpMute = buildLLDPView(nil,
		[]model.LLDPSelf{{DeviceID: id193, SiteID: idSite, Served: false,
			SeenAt: tLLDP, Source: lldpSourceSNMP}},
		[]model.Device{device1930()})
	if len(snmpMute.Silent) != 1 ||
		snmpMute.Silent[0].Reason != lldpSilenceNotServed {
		t.Fatalf("an SNMP snapshot with Served=false must stay %q, got %+v",
			lldpSilenceNotServed, snmpMute.Silent)
	}

	// The help sentence travels with the data (cf. handleListSiteLLDP) and the
	// screen localizes from Reason — but the published vocabulary must name every
	// reason the backend can emit, or a support read hits a word the document does
	// not define.
	var reason string
	for _, reason = range []string{lldpSilencePlatform, lldpSilenceDisabled,
		lldpSilenceNotServed, lldpSilenceNeverPoll} {
		if !strings.Contains(lldpSilenceReasonHelp, reason) {
			t.Errorf("lldpSilenceReasonHelp does not define %q", reason)
		}
	}
}

// TestNoConnectorMeansNoConclusion: a manually entered device without declared connector
// must NOT be ranked as "the platform does not speak LLDP". We have no reason to believe
// that — same doctrine as `unavailableMetricsOf`, whose ignorance yields nil rather than an
// empty array.
func TestNoConnectorMeansNoConclusion(t *testing.T) {
	var bare = deviceCRS()
	bare.Extra = nil
	var view = buildLLDPView(nil, nil, []model.Device{bare})
	if len(view.Silent) != 1 || view.Silent[0].Reason != lldpSilenceNeverPoll {
		t.Fatalf("expected %q for device without connector, got %+v",
			lldpSilenceNeverPoll, view.Silent)
	}
}

// TestEndToEndChain is the end-to-end test of DECISIONS: it starts from the snapshot the
// proxy would actually produce (from the `snmppoll` decoder, from the survey) and verifies
// what each side ends up saying about the fleet's CRS326 ↔ 1930 cable.
//
// It locks the batch's most fragile junction: proxy, facade resolution, and derivation must
// agree on a port NAME, otherwise the two halves of a link do not recognize each other.
//
// # THE ROUND TRIP THIS ASSERTION HAS MADE, because both ends of it are load-bearing
//
// It asserted ONE bilateral link until 2026-08-10, then TWO one-sided ones, and it is back
// to ONE bilateral. The two changes are unrelated to each other and neither undoes the
// other, so both are written down.
//
// The 1930 announces its own port as a bare "50". Reciprocity is EXACT — both halves must
// name the same pair of endpoints — so it used to need the CRS326's row to turn that "50"
// into GE50, by `facade.resolve`'s prefix arithmetic. THAT ARITHMETIC IS GONE from this
// path and is not coming back (cf. facade.resolveNeighborPort): a neighbour's lldpRemPortId
// is whatever the far platform chose to put there, the same fleet walk found a Juniper
// announcing "521" for a port really called ge-0/0/9.0, and since [markRefutedObservations]
// is port-exact a confidently wrong far port is the one input that can RETIRE a cable that
// exists. The CRS326's half therefore still does NOT name the 1930's port on its own, and
// the assertion below on `PeerVia` is what proves this test still runs through that refusal
// rather than around it.
//
// What gives reciprocity back is [completeReciprocalPeerPorts], and it guesses nothing: the
// 1930's own row states the exact pair — "I am on GE50, facing lab-crs326:sfp-sfpplus2" —
// so the CRS326's row BORROWS that port name instead of computing one. Observation, not
// arithmetic. Measured on the live fleet the same day: this cable and
// `s3900lab:GE28 ↔ sw-labo-1930:GE51` were each drawn as two one-sided links, which is what
// an operator reported as a double link.
//
// WHAT THIS FIXTURE STILL CANNOT SHOW, and the reason it is said out loud here: it gives
// each port ONE neighbour. The real `lab-crs326:sfp-sfpplus2` hears four speakers and
// therefore FLOODS, and a one-neighbour fixture cannot exercise the flood branch at all —
// that is exactly how an earlier claim about this cable came to be wrong. The flood
// interaction is pinned by TestCompleteHalfSurvivesAnUnkeptFloodDomain and
// TestMultiNeighborPortDoesNotThrash, and the completion's own behaviour by
// TestFarPortCompletedFromTheFarDevicesOwnObservation.
func TestEndToEndChain(t *testing.T) {
	// What the proxy would push for the CRS326, having read only the neighbor that matters.
	var crsSide = snmppoll.LLDPReport{
		DeviceID: idCRS, ChassisID: "B8:69:F4:5B:A0:D2", SysName: "lan-r4",
		PortIDs: []string{"sfp-sfpplus2"}, Served: true,
		Neighbors: []snmppoll.Neighbor{{
			LocalPort: "sfp-sfpplus2", LocalPortNum: 26,
			ChassisID: "02:00:5e:10:61:D9", PortID: "50", SysName: "CN0EXAMPL1",
		}},
	}
	// And what the proxy would push for the 1930. Its local port is "50" — the
	// `lldpLocPortDesc`, since its `lldpLocPortId` is a MAC — and `resolve` must turn that
	// into "GE50".
	var side1930 = snmppoll.LLDPReport{
		DeviceID: id193, ChassisID: "0x02005e1061a7", SysName: "CN0EXAMPL1",
		PortIDs: []string{"0x02005e1061d9"}, Served: true,
		Neighbors: []snmppoll.Neighbor{{
			LocalPort: "50", LocalPortNum: 50,
			ChassisID: "0xb869f45ba0d2", PortID: "sfp-sfpplus2",
			PortDesc: "bridge/sfp-sfpplus2", SysName: "lan-r4",
		}},
	}

	var selfA, obsA, dropA = lldpRowsFor(deviceCRS(), crsSide)
	var selfB, obsB, dropB = lldpRowsFor(device1930(), side1930)
	if dropA != 0 || dropB != 0 {
		t.Fatalf("%d + %d observations dropped: both ports must attach to their facade",
			dropA, dropB)
	}
	if obsA[0].LocalPort != "sfp-sfpplus2" {
		t.Errorf("CRS326 local port: %q", obsA[0].LocalPort)
	}
	if obsB[0].LocalPort != "GE50" {
		t.Fatalf("1930 local port: %q, expected \"GE50\" — `resolve` must translate device "+
			"\"50\" to facade name", obsB[0].LocalPort)
	}

	var view = buildLLDPView(append(obsA, obsB...),
		[]model.LLDPSelf{selfA, selfB},
		[]model.Device{deviceCRS(), device1930()})
	// ONE cable, ONE link. Two one-sided halves for a cable both devices named is what an
	// operator reads as a double link, and it is the shape this must never go back to.
	if len(view.Links) != 1 {
		t.Fatalf("%d links, expected 1 (one cable, drawn once); links = %+v",
			len(view.Links), view.Links)
	}
	if !view.Links[0].BothSides {
		t.Errorf("one-sided: both devices name the pair — the CRS326 by borrowing the port " +
			"the 1930 states about itself — so this cable is seen from both sides")
	}
	// THE PAIR OF ENDPOINTS IS EXACT, whichever side the link is emitted from. The 1930's
	// own port resolves through the metrics-side rule (its `lldpLocPortDesc` "50" IS its
	// port number, said by the device about itself), and that half must not regress.
	var ends = map[lldpEndpoint]bool{
		{device: id193, port: "GE50"}:         true,
		{device: idCRS, port: "sfp-sfpplus2"}: true,
	}
	if !ends[lldpEndpoint{device: view.Links[0].ADevice, port: view.Links[0].APort}] ||
		!ends[lldpEndpoint{device: view.Links[0].BDevice, port: view.Links[0].BPort}] ||
		view.Links[0].ADevice == view.Links[0].BDevice {
		t.Errorf("link is %s:%s -> %s:%s, expected GE50 ↔ sfp-sfpplus2",
			view.Links[0].ADevice, view.Links[0].APort,
			view.Links[0].BDevice, view.Links[0].BPort)
	}
	// THE COMPLETION IS WHAT MADE IT RECIPROCAL, and this pins that it came from the far
	// device rather than from arithmetic creeping back into `resolveNeighborPort`: the
	// CRS326's row carries GE50 although the only thing the CRS326 itself announced is a
	// bare "50" that `resolvePeerPort` refuses to read as a port number.
	var i int
	for i = range view.Neighbors {
		if view.Neighbors[i].DeviceID != idCRS {
			continue
		}
		if view.Neighbors[i].PeerPort != "GE50" {
			t.Errorf("CRS326 far port %q, expected \"GE50\" borrowed from the 1930's own "+
				"observation", view.Neighbors[i].PeerPort)
		}
		if resolvePeerPort(buildLLDPIdentity([]model.LLDPSelf{selfA, selfB},
			[]model.Device{deviceCRS(), device1930()}), id193, view.Neighbors[i]) != "" {
			t.Error("`resolvePeerPort` named the 1930's port from the bare \"50\": the " +
				"prefix arithmetic is back on the LLDP path, and this test would then be " +
				"passing for the wrong reason")
		}
	}
	// And the path must be NAMED on both sides: chassis on one, port identifier on the
	// other, because the 1930 emits a chassis-id it does not declare.
	var paths = map[string]string{}
	for i = range view.Neighbors {
		paths[view.Neighbors[i].DeviceID] = view.Neighbors[i].PeerVia
	}
	if paths[id193] != lldpViaChassis {
		t.Errorf("1930 recognizes CRS326 by %q, expected chassis", paths[id193])
	}
	if paths[idCRS] != lldpViaPortID {
		t.Errorf("CRS326 recognizes 1930 by %q, expected port identifier — surveyed fact "+
			"justifies this path", paths[idCRS])
	}
}

// TestUnresolvedPortDropped: an observation whose local port attaches to no facade port is
// DROPPED and COUNTED, never placed at random.
func TestUnresolvedPortDropped(t *testing.T) {
	var report = snmppoll.LLDPReport{
		DeviceID: idCRS, Served: true,
		Neighbors: []snmppoll.Neighbor{
			{LocalPort: "sfp-sfpplus2", SysName: "good"},
			// "vlan99" is a VLAN interface, not a facade port. The survey shows the CRS326
			// runs LLDP there and sees two neighbors: they have no port cell to display on.
			{LocalPort: "vlan99", LocalPortNum: 28, SysName: "lan-m6"},
			{LocalPort: "", LocalPortNum: 99, SysName: "no-port"},
		},
	}
	var _, obs, dropped = lldpRowsFor(deviceCRS(), report)
	if len(obs) != 1 {
		t.Fatalf("%d observations kept, expected 1", len(obs))
	}
	if obs[0].SysName != "good" {
		t.Errorf("kept observation is %q", obs[0].SysName)
	}
	if dropped != 2 {
		t.Errorf("%d dropped, expected 2: count goes to proxy, otherwise a missing link "+
			"would look like a collection defect", dropped)
	}
}

// --- Recognition by a MAC observed OUTSIDE LLDP (the FS S3400 case) ---

// The August 5, 2026 survey, live on the fleet: poefs3400lab runs LLDP (`lldp run`
// configured, two devices record its frames) but serves NOTHING under `1.0.8802` —
// its `lldp_self` row carries an EMPTY chassis. Its base MAC is nonetheless known
// from the CLI spanning-tree read (`extra.stp_observed.bridge_id`).
const (
	idS34  = "a1b2bd06-74fb-4438-8b17-42157e571cf4" // poefs3400lab, FS S3400-48T4SP
	idEdge = "a349b07b-072d-499a-bb03-3f9af1d6735c" // the EdgeSwitch that sees it on GE18
	idFS39 = "4cc105d7-3243-4d6e-bb79-766d51207931" // the FS switch that sees it on GE25
)

// deviceS3400 reproduces the REAL device row: facade GE1..48 + XG1..4, and the
// spanning-tree observation read-state refreshes under `extra.stp_observed`.
func deviceS3400() model.Device {
	return model.Device{
		ID: idS34, SiteID: idSite, Name: "poefs3400lab", Model: "S3400-48T4SP",
		PortGroups: json.RawMessage(`[
			{"prefix":"GE","from":1,"count":48,"speed":"1G"},
			{"prefix":"XG","from":1,"count":4,"speed":"10G"}]`),
		Extra: json.RawMessage(`{"driver":"fs-s3400","stp_observed":{
			"mode":"rstp","enabled":true,
			"bridge_id":"64:9d:99:22:7b:95",
			"root_bridge_id":"d4:01:c3:ab:32:f4"}}`),
	}
}

// selfS3400 is the row production actually holds: SERVED — the walk read MIB-II —
// but with an EMPTY chassis, because the firmware implements no LLDP-MIB.
func selfS3400() model.LLDPSelf {
	return model.LLDPSelf{
		DeviceID: idS34, SiteID: idSite,
		ChassisID: "", SysName: "", PortIDs: []string{}, Served: true,
	}
}

// TestRecognitionByObservedMAC: a neighbor announcing the S3400's base MAC is
// recognized through the spanning-tree bridge identifier — the chassis path, because
// it is the same address by another witness — and its remote port resolves through
// the connector's own translation ("TGi0/1" is what the S3400 advertises, "XG1" is
// what its facade calls that cage).
func TestRecognitionByObservedMAC(t *testing.T) {
	var idx = buildLLDPIdentity([]model.LLDPSelf{selfS3400()},
		[]model.Device{deviceS3400()})

	// What the EdgeSwitch records on GE18, verbatim from lldp_neighbors.
	var seen = model.LLDPNeighbor{
		DeviceID: idEdge, LocalPort: "GE18",
		ChassisID: "0x649d99227b95", PortID: "TGi0/1", SysName: "Switch",
	}
	var peer, via = identifyPeer(idx, seen)
	if peer != idS34 {
		t.Fatalf("recognized device %q, expected poefs3400lab: its declared chassis is "+
			"empty, and only the observed bridge MAC can identify it", peer)
	}
	if via != lldpViaChassis {
		t.Errorf("path %q, expected %q: an observed base MAC carries chassis strength",
			via, lldpViaChassis)
	}
	var port = resolvePeerPort(idx, peer, seen)
	if port != "XG1" {
		t.Errorf("resolved remote port %q, expected \"XG1\": the connector translates "+
			"\"TGi0/1\" to the standard-model name", port)
	}
}

// TestRecognitionByAdoptionMAC: the other source, `extra.mac`, written at adoption
// from what the connector read at inspection.
func TestRecognitionByAdoptionMAC(t *testing.T) {
	var d = deviceCRS()
	d.Extra = json.RawMessage(`{"driver":"mikrotik-routeros","mac":"B8:69:F4:5B:A0:D2"}`)
	var idx = buildLLDPIdentity(nil, []model.Device{d})
	var peer, via = identifyPeer(idx, model.LLDPNeighbor{ChassisID: "0xb869f45ba0d2"})
	if peer != idCRS || via != lldpViaChassis {
		t.Fatalf("recognized %q by %q, expected CRS326 by chassis: the adoption-time MAC "+
			"must identify the device even with no lldp_self row at all", peer, via)
	}
}

// TestNonMACObservedIdentityNotIndexed: a textual bridge identifier or a
// number-shaped `extra.mac` identifies NOTHING. Same doctrine as `lldpLocPortId`
// values: only a hardware address is proof.
func TestNonMACObservedIdentityNotIndexed(t *testing.T) {
	var d = deviceS3400()
	d.Extra = json.RawMessage(`{"mac":"45","stp_observed":{"bridge_id":"room-b-rack-12"}}`)
	var idx = buildLLDPIdentity(nil, []model.Device{d})
	var peer, _ = identifyPeer(idx, model.LLDPNeighbor{ChassisID: "45"})
	if peer != "" {
		t.Errorf("number-shaped extra.mac identified %q: only MAC-shaped tokens are indexed",
			peer)
	}
	if peer, _ = identifyPeer(idx, model.LLDPNeighbor{ChassisID: "room-b-rack-12"}); peer != "" {
		t.Errorf("textual bridge identifier identified %q", peer)
	}
}

// TestObservedMACAmbiguityNeutralized: two devices somehow carrying the same
// observed MAC — a cloned configuration, a mis-parsed read — must identify
// NEITHER. Matching at random is worse than not matching.
func TestObservedMACAmbiguityNeutralized(t *testing.T) {
	var a = deviceCRS()
	a.Extra = json.RawMessage(`{"mac":"64:9d:99:22:7b:95"}`)
	var b = device1930()
	b.Extra = json.RawMessage(`{"stp_observed":{"bridge_id":"649d.9922.7b95"}}`)
	var idx = buildLLDPIdentity(nil, []model.Device{a, b})
	var peer, _ = identifyPeer(idx, model.LLDPNeighbor{ChassisID: "0x649d99227b95"})
	if peer != "" {
		t.Errorf("shared observed MAC was attributed to %q: it should have been NEUTRALIZED",
			peer)
	}
}

// TestS3400ResolvesInTopology is the production scenario end to end: the S3400's
// own snapshot has NO chassis and NO neighbors (its agent serves no LLDP-MIB), and
// the two devices that hear its frames must still resolve their links TO it.
//
// The links stay seen from ONE side — reciprocity needs an observation from the
// S3400 itself, which SNMP collection cannot produce on this firmware — and that
// is stated, not worked around: a one-sided link is drawn, and said as such.
func TestS3400ResolvesInTopology(t *testing.T) {
	var neighbors = []model.LLDPNeighbor{
		{DeviceID: idEdge, SiteID: idSite, LocalPort: "GE18",
			ChassisID: "0x649d99227b95", PortID: "TGi0/1", SysName: "Switch"},
		{DeviceID: idFS39, SiteID: idSite, LocalPort: "GE25",
			ChassisID: "0x649d99227b95", PortID: "TGi0/2", SysName: "Switch"},
	}
	var edge = model.Device{ID: idEdge, SiteID: idSite, Name: "sw-edge",
		PortGroups: json.RawMessage(`[{"prefix":"GE","from":1,"count":24}]`)}
	var fs39 = model.Device{ID: idFS39, SiteID: idSite, Name: "sw-fs39",
		PortGroups: json.RawMessage(`[{"prefix":"GE","from":1,"count":28}]`)}

	var view = buildLLDPView(neighbors, []model.LLDPSelf{selfS3400()},
		[]model.Device{deviceS3400(), edge, fs39})
	if len(view.Links) != 2 {
		t.Fatalf("%d links, expected 2", len(view.Links))
	}
	var wantPort = map[string]string{"GE18": "XG1", "GE25": "XG2"}
	var i int
	for i = range view.Links {
		var l = view.Links[i]
		if l.BDevice != idS34 {
			t.Fatalf("link from %s:%s resolves to %q, expected poefs3400lab: its MAC is "+
				"known from the spanning-tree read", l.ADevice, l.APort, l.BDevice)
		}
		if l.BPort != wantPort[l.APort] {
			t.Errorf("link from %s resolves remote port %q, expected %q",
				l.APort, l.BPort, wantPort[l.APort])
		}
		if l.BothSides {
			t.Errorf("link from %s claims BOTH SIDES: the S3400 produced no observation, "+
				"nothing can confirm reciprocity", l.APort)
		}
	}
}

// --- Discarding a link the far end refutes ---
//
// The rule under test is `markRefutedObservations`: an observation dies when the endpoint
// it claims is occupied by SOMEBODY ELSE in a strictly FRESHER observation made by that
// endpoint's own device. These tests are where a wrong rule shows itself, because both
// mistakes it can make are invisible on screen — an erased true link looks like a missing
// cable, a surviving false link looks like a cable that is there.
//
// Every row here sets LastSeen EXPLICITLY. The comparison is strict, so rows left at the
// zero value can never refute anything — which is exactly why the tests above are
// unaffected by this pass, and why forgetting a date here would silently test nothing.

// refutedRows lists the observations flagged, as "device/port→by", so a failure names the
// rows instead of a count.
func refutedRows(rows []model.LLDPNeighbor) []string {
	var out []string
	var i int
	for i = range rows {
		if rows[i].Refuted {
			out = append(out, rows[i].DeviceID+"/"+rows[i].LocalPort+"→"+rows[i].RefutedBy)
		}
	}
	return out
}

// TestRecabledPortDiscardsOldLink is the operator's report, reduced: the CRS326's uplink
// was moved from the 1930 to the CSS326. The CRS326 is polled and says so; the 1930 is not
// (unreachable device, or CLI-sourced, in both cases its rows are frozen — nothing ages
// them out) and keeps claiming the port it no longer holds.
//
// The gap between the two dates is SIX HOURS, and that is the fixture being honest rather
// than being generous: a claim can only survive into the next cycle if its claimant stopped
// being collected, and a device that stopped being collected is hours behind, not minutes.
// The five minutes this test used before `staleWitnessMargin` reached this rule described a
// device that was still being polled — which is to say, a device whose row would have been
// replaced instead of refuted.
func TestRecabledPortDiscardsOldLink(t *testing.T) {
	var seen = []model.LLDPNeighbor{
		{
			DeviceID: idCRS, LocalPort: "sfp-sfpplus2",
			PeerDevice: idCSS, PeerPort: "SFP2",
			FirstSeen: tLLDP.Add(6 * time.Hour), LastSeen: tLLDP.Add(6 * time.Hour),
		},
		{
			DeviceID: id193, LocalPort: "GE50",
			PeerDevice: idCRS, PeerPort: "sfp-sfpplus2",
			FirstSeen: tLLDP.Add(-time.Hour), LastSeen: tLLDP,
		},
	}
	var judged = markRefutedObservations(seen)
	if seen[1].Refuted {
		t.Fatal("the input slice was mutated: it comes from the store and must be left alone")
	}
	if judged[0].Refuted {
		t.Errorf("the FRESH observation was refuted (by %q): freshness is what arbitrates",
			judged[0].RefutedBy)
	}
	if !judged[1].Refuted {
		t.Fatal("the 1930 still claims a CRS326 port the CRS326 says is facing the CSS326, " +
			"and said it more recently: that claim must not draw a cable")
	}
	if judged[1].RefutedBy != idCSS {
		t.Errorf("RefutedBy %q, expected the device now found on that port (%q)",
			judged[1].RefutedBy, idCSS)
	}
	var links = deriveLLDPLinks(judged)
	if len(links) != 1 {
		t.Fatalf("%d links, expected 1: the old cable must disappear, not coexist — %v",
			len(links), links)
	}
	if links[0].ADevice != idCRS || links[0].BDevice != idCSS {
		t.Errorf("link %s→%s, expected CRS326→CSS326", links[0].ADevice, links[0].BDevice)
	}
	if links[0].BothSides {
		t.Error("the CSS326 speaks no LLDP: nothing can confirm that link from its side")
	}
}

// TestRepatchOnSameSwitchDiscardsOldPort: the cable stayed between the same two devices and
// only changed port on the far side. The device pair is unchanged, so any rule comparing
// devices rather than ENDPOINTS would keep drawing the old port — and a port is what an
// operator reads before pulling a cable out of a rack.
//
// Six hours between the two dates, for the reason given on TestRecabledPortDiscardsOldLink:
// the frozen claimant is the only kind of claimant either rule ever deletes.
func TestRepatchOnSameSwitchDiscardsOldPort(t *testing.T) {
	var seen = []model.LLDPNeighbor{
		{
			DeviceID: idCRS, LocalPort: "sfp-sfpplus2",
			PeerDevice: id193, PeerPort: "GE49",
			FirstSeen: tLLDP.Add(6 * time.Hour), LastSeen: tLLDP.Add(6 * time.Hour),
		},
		{
			DeviceID: id193, LocalPort: "GE50",
			PeerDevice: idCRS, PeerPort: "sfp-sfpplus2",
			FirstSeen: tLLDP, LastSeen: tLLDP,
		},
	}
	var judged = markRefutedObservations(seen)
	if !judged[1].Refuted || judged[1].RefutedBy != id193 {
		t.Fatalf("GE50's stale claim: Refuted=%v by %q — the CRS326 port it claims now "+
			"faces GE49 of that same switch", judged[1].Refuted, judged[1].RefutedBy)
	}
	var links = deriveLLDPLinks(judged)
	if len(links) != 1 {
		t.Fatalf("%d links, expected 1 (GE49 only): %v", len(links), links)
	}
	if links[0].BPort != "GE49" {
		t.Errorf("link drawn on remote port %q, expected GE49", links[0].BPort)
	}
}

// TestEqualFreshnessRefutesNothing answers "which side is authoritative when the two
// disagree": freshness, and on a TIE nobody. Two sides polled in the same cycle that
// contradict each other are a flood domain or a real ambiguity, and choosing one would be
// choosing at random — both stay drawn, one-sided, which says what we actually know.
func TestEqualFreshnessRefutesNothing(t *testing.T) {
	var seen = []model.LLDPNeighbor{
		{DeviceID: idCRS, LocalPort: "sfp-sfpplus2", PeerDevice: idCSS, PeerPort: "SFP2",
			FirstSeen: tLLDP, LastSeen: tLLDP},
		{DeviceID: id193, LocalPort: "GE50", PeerDevice: idCRS, PeerPort: "sfp-sfpplus2",
			FirstSeen: tLLDP, LastSeen: tLLDP},
	}
	var judged = markRefutedObservations(seen)
	var got = refutedRows(judged)
	if len(got) != 0 {
		t.Fatalf("refuted %v on equal dates: a tie is an ambiguity, not an arbitration", got)
	}
	var links = deriveLLDPLinks(judged)
	if len(links) != 2 {
		t.Fatalf("%d links, expected 2: both claims stay drawn, one-sided", len(links))
	}
}

// TestSilenceNeverRefutes replays the S3400's shape — a device with NO rows at all, because
// its firmware serves no LLDP-MIB — with explicit, unequal dates on the observing side.
//
// This is the refusal that carries the whole fleet: an entire platform (SwOS) never reports
// anybody, and treating "that endpoint says nothing" as refutation would erase every link
// the derivation calls the NORMAL case.
func TestSilenceNeverRefutes(t *testing.T) {
	var seen = []model.LLDPNeighbor{
		{DeviceID: idEdge, LocalPort: "GE18", PeerDevice: idS34, PeerPort: "XG1",
			FirstSeen: tLLDP, LastSeen: tLLDP.Add(5 * time.Minute)},
		{DeviceID: idFS39, LocalPort: "GE25", PeerDevice: idS34, PeerPort: "XG2",
			FirstSeen: tLLDP, LastSeen: tLLDP},
	}
	var judged = markRefutedObservations(seen)
	var got = refutedRows(judged)
	if len(got) != 0 {
		t.Fatalf("refuted %v: the S3400 has no rows, and an absence proves nothing", got)
	}
	var links = deriveLLDPLinks(judged)
	if len(links) != 2 {
		t.Fatalf("%d links, expected 2: both are true, and neither side can be confirmed",
			len(links))
	}

	// Same silent device, and this time the two observers claim the SAME far port — one of
	// them is certainly wrong. We still refuse: the arbitration is only ever made by the
	// device that OWNS the port, and this one says nothing.
	seen[1].PeerPort = "XG1"
	judged = markRefutedObservations(seen)
	got = refutedRows(judged)
	if len(got) != 0 {
		t.Fatalf("refuted %v: only XG1's own device could say who is on XG1", got)
	}
}

// TestUnrecognizedFarNeighborCannotRefute: the fresher row on the far port names a chassis
// we could not attach to any fleet device. It may BE the claimant under a spelling we
// failed to match — the 1930 announcing its port MAC instead of its chassis is exactly that
// shape, and it is the reason `normalizeChassisID` exists. An unidentified occupant
// therefore disables refutation on that endpoint entirely.
func TestUnrecognizedFarNeighborCannotRefute(t *testing.T) {
	var seen = []model.LLDPNeighbor{
		{DeviceID: idCRS, LocalPort: "sfp-sfpplus2", PeerDevice: id193, PeerPort: "GE50",
			FirstSeen: tLLDP, LastSeen: tLLDP},
		{DeviceID: id193, LocalPort: "GE50", ChassisID: "04:F4:1C:1D:74:5F",
			SysName: "RB5009UG", FirstSeen: tLLDP, LastSeen: tLLDP.Add(5 * time.Minute)},
	}
	var judged = markRefutedObservations(seen)
	var got = refutedRows(judged)
	if len(got) != 0 {
		t.Fatalf("refuted %v: an unrecognized occupant could be the claimant itself", got)
	}
}

// TestUnresolvedFarPortCannotRefute: the fresher row on the far port names a device but no
// port — `resolvePeerPort` returns "" often and by design (the CRS326 serves no
// `lldpRemPortDesc`, a facade may attach no label). Comparing at port granularity is
// meaningless then, and refuting would trade an under-asserted link for no link at all.
func TestUnresolvedFarPortCannotRefute(t *testing.T) {
	var seen = []model.LLDPNeighbor{
		{DeviceID: idCRS, LocalPort: "sfp-sfpplus2", PeerDevice: id193, PeerPort: "GE50",
			FirstSeen: tLLDP, LastSeen: tLLDP},
		{DeviceID: id193, LocalPort: "GE50", PeerDevice: idCSS, PeerPort: "",
			FirstSeen: tLLDP, LastSeen: tLLDP.Add(5 * time.Minute)},
	}
	var judged = markRefutedObservations(seen)
	var got = refutedRows(judged)
	if len(got) != 0 {
		t.Fatalf("refuted %v: a nameless far port cannot occupy anything", got)
	}
}

// TestReciprocalHalvesWithDifferentDatesSurvive is the regression guard that matters most,
// and it uses the EXACT data of TestLinkSeenFromBothSides: the fleet's only bilateral link,
// whose two halves are dated two and three minutes apart because the two devices are polled
// on their own cycles. Different dates are the ORDINARY state of a healthy link — if
// freshness alone refuted, every bilateral link in the product would lose a half.
func TestReciprocalHalvesWithDifferentDatesSurvive(t *testing.T) {
	var seen = []model.LLDPNeighbor{
		{
			DeviceID: idCRS, LocalPort: "sfp-sfpplus2",
			ChassisID: "02:00:5e:10:61:D9", PortID: "50", SysName: "CN0EXAMPL1",
			PeerDevice: id193, PeerPort: "GE50", PeerVia: lldpViaPortID,
			FirstSeen: tLLDP, LastSeen: tLLDP.Add(2 * time.Minute),
		},
		{
			DeviceID: id193, LocalPort: "GE50",
			ChassisID: "0xb869f45ba0d2", PortID: "sfp-sfpplus2", SysName: "lan-r4",
			PeerDevice: idCRS, PeerPort: "sfp-sfpplus2", PeerVia: lldpViaChassis,
			FirstSeen: tLLDP.Add(-time.Hour), LastSeen: tLLDP.Add(3 * time.Minute),
		},
	}
	var judged = markRefutedObservations(seen)
	var got = refutedRows(judged)
	if len(got) != 0 {
		t.Fatalf("refuted %v: the two halves NAME each other, which is confirmation", got)
	}
	var links = deriveLLDPLinks(judged)
	if len(links) != 1 || !links[0].BothSides {
		t.Fatalf("%d links, bilateral=%v: the reference fleet's only confirmed link",
			len(links), len(links) == 1 && links[0].BothSides)
	}
}

// TestMultiNeighborPortDoesNotThrash: a port that legitimately hears several speakers — a
// hub, a phone with a pass-through port, an intermediate switch that floods LLDP (measured
// on SwOS) — with all of them reciprocating and none of them dated alike.
//
// Nothing may be refuted here. Each speaker is named back by the shared port, and a flood
// domain is not a contradiction to arbitrate: resolving it by freshness would make the map
// flip between speakers at every poll, which is the one failure worse than drawing nothing.
// The pre-existing flood rule keeps deciding alone, and it draws no edge.
func TestMultiNeighborPortDoesNotThrash(t *testing.T) {
	var seen = []model.LLDPNeighbor{
		{DeviceID: idCRS, LocalPort: "sfp-sfpplus2", PeerDevice: id193, PeerPort: "GE50",
			FirstSeen: tLLDP, LastSeen: tLLDP.Add(time.Minute)},
		{DeviceID: idCRS, LocalPort: "sfp-sfpplus2", PeerDevice: idCSS, PeerPort: "SFP2",
			FirstSeen: tLLDP, LastSeen: tLLDP.Add(time.Minute)},
		{DeviceID: id193, LocalPort: "GE50", PeerDevice: idCRS, PeerPort: "sfp-sfpplus2",
			FirstSeen: tLLDP, LastSeen: tLLDP},
		{DeviceID: idCSS, LocalPort: "SFP2", PeerDevice: idCRS, PeerPort: "sfp-sfpplus2",
			FirstSeen: tLLDP, LastSeen: tLLDP.Add(2 * time.Minute)},
	}
	var judged = markRefutedObservations(seen)
	var got = refutedRows(judged)
	if len(got) != 0 {
		t.Fatalf("refuted %v: a shared port that names both speakers back contradicts "+
			"nobody", got)
	}
	var before = deriveLLDPLinks(seen)
	var after = deriveLLDPLinks(judged)
	if len(after) != len(before) {
		t.Fatalf("%d links after refutation, %d before: flood handling must be untouched",
			len(after), len(before))
	}
	if len(after) != 0 {
		t.Fatalf("%d links: two reciprocal remotes on one port stay ambiguous, and an "+
			"ambiguous flood domain draws nothing", len(after))
	}
}

// TestCompleteHalfSurvivesAnUnkeptFloodDomain: a COMPLETE observation — one that names
// both of its endpoints exactly — must still be drawn when the port it faces is a flood
// domain that keeps NOBODY.
//
// THE DEFECT THIS PINS, found live on 2026-08-10 with lab-crs326 missing from the
// topology entirely. `deriveLLDPLinks` skipped every observation whose far endpoint was
// flooded, on the reasoning that "only the floodKeep peer is drawn, and from that side".
// That reasoning holds ONLY when the far side really does draw this pair. `floodKeep`
// takes an entry for a flooded port with exactly ONE recognized remote that names it
// back; with none, the flooded side's own rows are all dropped by the flood branch and
// nothing whatsoever is drawn from there. The reverse half was then dropped too, and a
// cable that is physically plugged in left the map — the failure this file's header
// singles out as the expensive one.
//
// THE FIELD SHAPE, reproduced here rather than invented: `lab-crs326:sfp-sfpplus2` faces
// a transparent switch and hears four speakers, so it floods. Once `resolveNeighborPort`
// stopped reading `sw-labo-1930`'s bare "50" as a port number, the CRS326's own half
// no longer named the far port, so it stopped qualifying as the floodKeep candidate and
// the port kept nobody. The 1930's half names both ends exactly and is the only thing
// left that can draw this cable.
func TestCompleteHalfSurvivesAnUnkeptFloodDomain(t *testing.T) {
	var seen = []model.LLDPNeighbor{
		// The flooded port. NONE of these carries a far port: two are unrecognized
		// strangers, and the 1930's row is the one whose bare "50" no longer resolves.
		{DeviceID: idCRS, LocalPort: "sfp-sfpplus2", PeerDevice: "", PeerPort: "",
			SysName: "RB5009UG", FirstSeen: tLLDP, LastSeen: tLLDP.Add(time.Minute)},
		{DeviceID: idCRS, LocalPort: "sfp-sfpplus2", PeerDevice: id193, PeerPort: "",
			SysName: "CN0EXAMPL1", FirstSeen: tLLDP, LastSeen: tLLDP.Add(time.Minute)},
		{DeviceID: idCRS, LocalPort: "sfp-sfpplus2", PeerDevice: "", PeerPort: "",
			SysName: "CRS305", FirstSeen: tLLDP, LastSeen: tLLDP.Add(time.Minute)},
		// The complete half: the 1930 names its OWN port and the CRS326's exactly.
		{DeviceID: id193, LocalPort: "GE50", PeerDevice: idCRS, PeerPort: "sfp-sfpplus2",
			FirstSeen: tLLDP, LastSeen: tLLDP.Add(time.Minute)},
	}
	var links = deriveLLDPLinks(markRefutedObservations(seen))
	if len(links) != 1 {
		t.Fatalf("%d links, expected 1: the 1930's half names both endpoints, and the "+
			"flooded far port keeps nobody, so nothing draws this cable from that side",
			len(links))
	}
	if links[0].ADevice != id193 || links[0].APort != "GE50" ||
		links[0].BDevice != idCRS || links[0].BPort != "sfp-sfpplus2" {
		t.Fatalf("link is %s:%s -> %s:%s, expected the 1930's complete half",
			links[0].ADevice, links[0].APort, links[0].BDevice, links[0].BPort)
	}
	if links[0].BothSides {
		t.Error("bilateral: the CRS326's half does not name the far port, so this cable " +
			"is seen from one side only — claiming reciprocity would be the over-assertion")
	}

	// THE DUPLICATE THIS BRANCH EXISTS TO PREVENT MUST STILL BE PREVENTED. Give the
	// flooded port a unique remote that names it back and floodKeep takes an entry: the
	// pair is then drawn ONCE, from the flooded side, and the reverse half stays skipped.
	var kept = []model.LLDPNeighbor{
		{DeviceID: idCRS, LocalPort: "sfp-sfpplus2", PeerDevice: "", PeerPort: "",
			SysName: "RB5009UG", FirstSeen: tLLDP, LastSeen: tLLDP.Add(time.Minute)},
		{DeviceID: idCRS, LocalPort: "sfp-sfpplus2", PeerDevice: id193, PeerPort: "GE50",
			FirstSeen: tLLDP, LastSeen: tLLDP.Add(time.Minute)},
		{DeviceID: id193, LocalPort: "GE50", PeerDevice: idCRS, PeerPort: "sfp-sfpplus2",
			FirstSeen: tLLDP, LastSeen: tLLDP.Add(time.Minute)},
	}
	links = deriveLLDPLinks(markRefutedObservations(kept))
	if len(links) != 1 || !links[0].BothSides {
		t.Fatalf("%d links (bilateral=%v), expected 1 bilateral: a flood domain with a "+
			"unique reciprocating remote is drawn once, from the flooded side",
			len(links), len(links) == 1 && links[0].BothSides)
	}
	if links[0].ADevice != idCRS {
		t.Errorf("drawn from %s: the floodKeep pair is drawn from the FLOODED side, and "+
			"the reverse half must stay skipped or the cable is returned twice",
			links[0].ADevice)
	}
}

// TestFarPortCompletedFromTheFarDevicesOwnObservation pins the repair for the operator
// report of 2026-08-11: "still double link between s3900 and procurve — should be visible
// only one link in this position".
//
// THE FIELD SHAPE, taken off the live rows rather than invented. `s3900lab:GE28` announces
// its neighbour's port as a bare "51", which `resolveNeighborPort` refuses — a number in
// `lldpRemPortId` is whatever the far platform put there, and the same fleet walk found a
// Juniper announcing "521" for `ge-0/0/9.0`. `sw-labo-1930:GE51` names both endpoints
// exactly. Reciprocity being EXACT, the two halves did not recognize each other and ONE
// cable came out as TWO one-sided links — which on the map is a double link.
//
// The completion takes `local_port` off the far device's OWN row. It computes nothing, and
// the second half of this test is what keeps it that way: the moment two of the far
// device's ports claim our endpoint, nothing is completed at all.
func TestFarPortCompletedFromTheFarDevicesOwnObservation(t *testing.T) {
	const idS3900 = "4cc105d7-3243-4d6e-bb79-766d51207931"
	var seen = []model.LLDPNeighbor{
		// The S3900's half: recognizes the 1930, cannot name its port.
		{DeviceID: idS3900, LocalPort: "GE28", PeerDevice: id193, PeerPort: "",
			PortID: "51", PortDesc: "51", SysName: "CN0EXAMPL1",
			FirstSeen: tLLDP, LastSeen: tLLDP.Add(time.Minute)},
		// The 1930's half: names both ends.
		{DeviceID: id193, LocalPort: "GE51", PeerDevice: idS3900, PeerPort: "GE28",
			PortID: "Eth 1/28", FirstSeen: tLLDP, LastSeen: tLLDP.Add(time.Minute)},
	}
	var rows = append([]model.LLDPNeighbor{}, seen...)
	completeReciprocalPeerPorts(rows)
	if rows[0].PeerPort != "GE51" {
		t.Fatalf("S3900 far port %q, expected \"GE51\" — the 1930 states that port about "+
			"itself, so there is nothing to guess", rows[0].PeerPort)
	}
	var links = deriveLLDPLinks(rows)
	if len(links) != 1 {
		t.Fatalf("%d links, expected 1: one cable, and the operator saw it drawn twice; "+
			"links = %+v", len(links), links)
	}
	if !links[0].BothSides {
		t.Error("one-sided: both devices name the same pair of endpoints now")
	}

	// WITHOUT the completion the defect is exactly reproduced, which is what makes this
	// test about the repair rather than about the fixture.
	if len(deriveLLDPLinks(seen)) != 2 {
		t.Fatalf("%d links from the raw halves, expected the 2 the operator reported",
			len(deriveLLDPLinks(seen)))
	}

	// THE REFUSAL. Two of the far device's own ports claiming our endpoint is an ambiguity
	// — a loop, a flood domain — and this file does not arbitrate ambiguities. Nothing is
	// completed, and the cable stays drawn one-sided from the half that can name it.
	var ambiguous = []model.LLDPNeighbor{
		seen[0], seen[1],
		{DeviceID: id193, LocalPort: "GE47", PeerDevice: idS3900, PeerPort: "GE28",
			FirstSeen: tLLDP, LastSeen: tLLDP.Add(time.Minute)},
	}
	completeReciprocalPeerPorts(ambiguous)
	if ambiguous[0].PeerPort != "" {
		t.Errorf("completed to %q although two of the 1930's ports claim GE28: picking one "+
			"is the guess every refusal in this file exists to prevent", ambiguous[0].PeerPort)
	}

	// ONLY THE DEVICE WE SAY WE FACE may answer for its own ports. A third device naming
	// our endpoint says nothing about which port of the 1930 we are on.
	var stranger = []model.LLDPNeighbor{
		seen[0],
		{DeviceID: idCRS, LocalPort: "sfp-sfpplus2", PeerDevice: idS3900, PeerPort: "GE28",
			FirstSeen: tLLDP, LastSeen: tLLDP.Add(time.Minute)},
	}
	completeReciprocalPeerPorts(stranger)
	if stranger[0].PeerPort != "" {
		t.Errorf("completed to %q from a device that is not the recognized peer",
			stranger[0].PeerPort)
	}

	// A REFUTED ROW NEITHER COMPLETES NOR IS COMPLETED. `deriveLLDPLinks` drops those
	// first — a contradicted observation "only stopped being true" — so letting one supply
	// a port name would smuggle a retired claim back into the drawing.
	var stale = []model.LLDPNeighbor{seen[0], seen[1]}
	stale[1].Refuted = true
	completeReciprocalPeerPorts(stale)
	if stale[0].PeerPort != "" {
		t.Errorf("completed to %q from a REFUTED observation", stale[0].PeerPort)
	}
}

// TestCompletionCarriesNoRefutationTeeth: a completed far port must never DELETE anything.
//
// The value is second-hand — the far device's word about which of ITS ports faces us —
// and that is enough to draw a cable both sides already agree on, not to retire somebody
// else's. [markRefutedObservations] treats an occupant with no far port as a reason to
// disable refutation for that endpoint entirely ("this occupant could be the claimant
// itself under a spelling we failed to match"); completing before refutation would hand
// that veto away silently. `buildLLDPView` therefore completes AFTER both rules, and this
// pins the order rather than the comment that explains it.
func TestCompletionCarriesNoRefutationTeeth(t *testing.T) {
	const idS3900 = "4cc105d7-3243-4d6e-bb79-766d51207931"
	// A frozen third party claims `s3900lab:GE28` — hours behind, so it clears
	// [staleWitnessMargin] and nothing but the four refusals protects it. The occupant of
	// that endpoint is the S3900's own row, and it is UNRESOLVED: "an unrecognized or
	// unresolved occupant could be the claimant itself under a spelling we failed to
	// match", so it vetoes arbitration on GE28 entirely. Completing it first would revoke
	// that veto and delete this row.
	var seen = []model.LLDPNeighbor{
		{DeviceID: idCRS, LocalPort: "sfp-sfpplus2", PeerDevice: idS3900, PeerPort: "GE28",
			FirstSeen: tLLDP, LastSeen: tLLDP},
		{DeviceID: idS3900, LocalPort: "GE28", PeerDevice: id193, PeerPort: "",
			PortID: "51", FirstSeen: tLLDP, LastSeen: tLLDP.Add(6 * time.Hour)},
		{DeviceID: id193, LocalPort: "GE51", PeerDevice: idS3900, PeerPort: "GE28",
			FirstSeen: tLLDP, LastSeen: tLLDP.Add(6 * time.Hour)},
	}
	// The order buildLLDPView uses: refute on what the devices reported, complete after.
	var judged = append([]model.LLDPNeighbor{}, seen...)
	judged = markRefutedObservations(judged)
	completeReciprocalPeerPorts(judged)
	var got = refutedRows(judged)
	if len(got) != 0 {
		t.Fatalf("refuted %v: the S3900's row named no far port when refutation ran, so it "+
			"disabled arbitration on GE28 — a borrowed port name must not delete a "+
			"stranger's observation", got)
	}
	// And the completion still did its job on the same rows.
	if judged[1].PeerPort != "GE51" {
		t.Errorf("S3900 far port %q, expected \"GE51\"", judged[1].PeerPort)
	}

	// THE ORDER IS LOAD-BEARING, and this is what proves it rather than asserting it:
	// reversed, the same rows lose one. If this ever stops differing, the fixture has gone
	// stale and the assertion above is passing for free.
	var reversed = append([]model.LLDPNeighbor{}, seen...)
	completeReciprocalPeerPorts(reversed)
	if len(refutedRows(markRefutedObservations(reversed))) == 0 {
		t.Error("completing BEFORE refutation changed nothing here: this fixture no longer " +
			"exercises the veto it was built for, so the order it pins is untested")
	}
}

// TestRefutedObservationStaysListed runs the whole read path. The refuted row must remain in
// `neighbors` — the 1930 really did report that, the Ports panel must keep showing it, and a
// link vanishing with nothing on screen to explain it is how an operator concludes the tool
// is broken. Only the derived edge goes.
func TestRefutedObservationStaysListed(t *testing.T) {
	var css = deviceCSS()
	// Adoption-time MAC: the CSS326 speaks no LLDP and will never have an `lldp_self` row,
	// so this is the only thing that can recognize it behind a neighbor (cf.
	// TestRecognitionByAdoptionMAC).
	css.Extra = json.RawMessage(`{"driver":"mikrotik-swos","mac":"D4:01:C3:AB:32:F6"}`)
	var neighbors = []model.LLDPNeighbor{
		{
			DeviceID: idCRS, SiteID: idSite, LocalPort: "sfp-sfpplus2",
			ChassisID: "d4:01:c3:ab:32:f6", PortID: "SFP2", SysName: "lab-css326",
			FirstSeen: tLLDP.Add(6 * time.Hour), LastSeen: tLLDP.Add(6 * time.Hour),
		},
		{
			DeviceID: id193, SiteID: idSite, LocalPort: "GE50",
			ChassisID: "0xb869f45ba0d2", PortID: "sfp-sfpplus2", SysName: "lan-r4",
			FirstSeen: tLLDP.Add(-time.Hour), LastSeen: tLLDP,
		},
	}
	var view = buildLLDPView(neighbors, []model.LLDPSelf{selfCRS(), self1930()},
		[]model.Device{deviceCRS(), device1930(), css})
	if len(view.Neighbors) != 2 {
		t.Fatalf("%d observations listed, expected 2: a refuted row is still something the "+
			"device reported", len(view.Neighbors))
	}
	var i int
	var found bool
	for i = range view.Neighbors {
		if view.Neighbors[i].DeviceID != id193 {
			continue
		}
		found = true
		if !view.Neighbors[i].Refuted {
			t.Error("the 1930's stale claim is not flagged: the screen would present a " +
				"contradicted observation as current")
		}
		if view.Neighbors[i].RefutedBy != idCSS {
			t.Errorf("RefutedBy %q, expected %q", view.Neighbors[i].RefutedBy, idCSS)
		}
	}
	if !found {
		t.Fatal("the 1930's observation disappeared from the list entirely")
	}
	if len(view.Links) != 1 {
		t.Fatalf("%d links, expected 1: only the current cable is drawn — %v",
			len(view.Links), view.Links)
	}
	if view.Links[0].ADevice != idCRS || view.Links[0].BDevice != idCSS {
		t.Errorf("link %s→%s, expected CRS326→CSS326",
			view.Links[0].ADevice, view.Links[0].BDevice)
	}
}

// THE ERASED TRUE LINK. deriveLLDPLinks refuses to derive a claim from a port
// that hears several recognized neighbours; markRefutedObservations let exactly
// such a port ARBITRATE, picking whichever occupant was polled last and refuting
// with it. The measured shape: a CRS326 reaching a 1930 through a transparent
// switch, so the 1930 never hears the CRS back while hearing two other devices.
// The CRS's TRUE observation was flagged refuted by whichever of them was
// freshest, and the link vanished from the map.
func TestAmbiguousFarPortDoesNotRefute(t *testing.T) {
	var now = time.Now().UTC()
	var rows = []model.LLDPNeighbor{
		// The true link, seen only from the CRS side.
		{DeviceID: "crs", LocalPort: "XG2", PeerDevice: "i1930", PeerPort: "GE50",
			LastSeen: now.Add(-2 * time.Minute)},
		// The 1930's port hears TWO other devices and neither names the CRS back.
		{DeviceID: "i1930", LocalPort: "GE50", PeerDevice: "css", PeerPort: "SFP2",
			LastSeen: now.Add(-1 * time.Minute)},
		{DeviceID: "i1930", LocalPort: "GE50", PeerDevice: "edge", PeerPort: "GE17",
			LastSeen: now},
	}
	var got = markRefutedObservations(rows)
	if got[0].Refuted {
		t.Fatalf("the CRS observation was refuted by %q — an ambiguous port that cannot "+
			"say who it faces must not be trusted to say who somebody else does not face",
			got[0].RefutedBy)
	}
}

// And the guard must not neuter the feature: ONE fresher occupant naming
// somebody else is the re-cable this whole mechanism exists for.
func TestSingleFresherOccupantStillRefutes(t *testing.T) {
	var now = time.Now().UTC()
	var rows = []model.LLDPNeighbor{
		{DeviceID: "crs", LocalPort: "XG2", PeerDevice: "i1930", PeerPort: "GE50",
			LastSeen: now.Add(-6 * time.Hour)},
		{DeviceID: "i1930", LocalPort: "GE50", PeerDevice: "css", PeerPort: "SFP2",
			LastSeen: now},
	}
	var got = markRefutedObservations(rows)
	if !got[0].Refuted || got[0].RefutedBy != "css" {
		t.Fatalf("a single fresher occupant must refute: refuted=%v by=%q",
			got[0].Refuted, got[0].RefutedBy)
	}
}

// --- The frozen witness: a link that outlives its cable ---

// selfAt builds an lldp_self record for a device last heard from at `at`.
func selfAt(deviceID string, at time.Time) model.LLDPSelf {
	return model.LLDPSelf{DeviceID: deviceID, SeenAt: at}
}

// A FROZEN WITNESS STOPS HOLDING A CABLE UP.
//
// The operator-reported fault, and the one markRefutedObservations structurally
// cannot catch: it needs the far port to be occupied by SOMEBODY ELSE, and a port
// with nothing plugged into it is occupied by nobody. So when A stops being
// collected — its proxy went away, its rows came from a one-off CLI read — A's
// claim "I face B:Gi1/0/2" sits in the table for ever and the map keeps drawing a
// cable that was pulled months ago.
//
// B is still collected, keeps reporting, and its Gi1/0/2 lists nobody. That is
// not silence: it is a witness that went looking and came back empty.
func TestFrozenWitnessNoLongerHoldsALinkUp(t *testing.T) {
	var now = time.Now().UTC()
	var frozen = now.Add(-48 * time.Hour)
	var rows = []model.LLDPNeighbor{
		{
			DeviceID: "a", LocalPort: "Gi1/0/1", LastSeen: frozen,
			PeerDevice: "b", PeerPort: "Gi1/0/2",
		},
	}
	var selves = []model.LLDPSelf{
		selfAt("a", frozen), // A has not reported since
		selfAt("b", now),    // B has, many times, and names nobody on Gi1/0/2
	}
	refuteByFreshSilence(rows, selves, rows)
	if !rows[0].Refuted {
		t.Fatal("a claim two days stale, contradicted by a far end that has reported " +
			"since and lists nobody on that port, was not refuted")
	}
	if len(deriveLLDPLinks(rows)) != 0 {
		t.Error("the refuted claim still produced a link")
	}
}

// ASYMMETRIC LLDP MUST SURVIVE, and this is the case the whole margin exists for.
//
// A sees B; B transmits but never lists A back — the phenomenon the project's
// "silence never refutes" rule was founded on. B reports constantly and its port
// is empty, which looks exactly like the frozen-witness case above from the far
// end's point of view.
//
// The discriminator is that A is ALSO still being collected, so its claim keeps
// up with B's snapshots and neither pulls ahead. Refuting here would delete a true
// link and send somebody to the wrong rack.
func TestAsymmetricLLDPIsNotRefutedWhileBothSidesAreCollected(t *testing.T) {
	var now = time.Now().UTC()
	var rows = []model.LLDPNeighbor{
		{
			DeviceID: "a", LocalPort: "Gi1/0/1", LastSeen: now.Add(-20 * time.Second),
			PeerDevice: "b", PeerPort: "Gi1/0/2",
		},
	}
	var selves = []model.LLDPSelf{
		selfAt("a", now.Add(-20*time.Second)),
		selfAt("b", now),
	}
	refuteByFreshSilence(rows, selves, rows)
	if rows[0].Refuted {
		t.Fatal("a live one-sided link was refuted: asymmetric LLDP is the normal case " +
			"this rule must not touch")
	}
}

// A FLEET-WIDE COLLECTION OUTAGE MUST NOT EMPTY THE MAP.
//
// Every observation goes stale together, so no witness ever pulls ahead of
// another. The rule is RELATIVE by construction, and this asserts that property
// rather than trusting it: an absolute age test would erase an entire site's
// topology the first time a controller was down for an afternoon.
func TestAFleetWideOutageRefutesNothing(t *testing.T) {
	var old = time.Now().UTC().Add(-72 * time.Hour)
	var rows = []model.LLDPNeighbor{
		{DeviceID: "a", LocalPort: "1", LastSeen: old, PeerDevice: "b", PeerPort: "2"},
		{DeviceID: "b", LocalPort: "2", LastSeen: old, PeerDevice: "a", PeerPort: "1"},
	}
	var selves = []model.LLDPSelf{selfAt("a", old), selfAt("b", old)}
	refuteByFreshSilence(rows, selves, rows)
	var i int
	for i = range rows {
		if rows[i].Refuted {
			t.Fatal("a stopped fleet refuted its own topology")
		}
	}
}

// A DEVICE THAT NEVER REPORTS LLDP STILL REFUTES NOTHING.
//
// The commitment the file header makes and this rule must keep. A whole fleet
// platform serves no LLDP at all (SwOS, measured), so its absence from `selves`
// means "we know nothing", not "it saw nobody" — and treating the two the same
// would erase every link into every such device.
func TestADeviceWithNoSelfRecordRefutesNothing(t *testing.T) {
	var now = time.Now().UTC()
	var rows = []model.LLDPNeighbor{
		{
			DeviceID: "a", LocalPort: "1", LastSeen: now.Add(-48 * time.Hour),
			PeerDevice: "swos", PeerPort: "2",
		},
	}
	// `swos` is absent from selves entirely: never collected for LLDP.
	refuteByFreshSilence(rows, []model.LLDPSelf{selfAt("a", now.Add(-48*time.Hour))}, rows)
	if rows[0].Refuted {
		t.Fatal("silence from a platform that serves no LLDP was treated as refutation")
	}
}

// AN OCCUPIED FAR PORT IS LEFT TO THE OTHER RULE.
//
// markRefutedObservations arbitrates that contradiction with four refusals of its
// own. Deciding it here as well would settle one disagreement by two rules, which
// can disagree — and the two verdicts would then depend on call order.
func TestAnOccupiedFarPortIsLeftToTheOtherRule(t *testing.T) {
	var now = time.Now().UTC()
	var rows = []model.LLDPNeighbor{
		{
			DeviceID: "a", LocalPort: "1", LastSeen: now.Add(-48 * time.Hour),
			PeerDevice: "b", PeerPort: "2",
		},
		// Somebody else is on b:2 — that is the other rule's case.
		{
			DeviceID: "b", LocalPort: "2", LastSeen: now,
			PeerDevice: "c", PeerPort: "5",
		},
	}
	var selves = []model.LLDPSelf{
		selfAt("a", now.Add(-48*time.Hour)), selfAt("b", now), selfAt("c", now),
	}
	refuteByFreshSilence(rows, selves, rows)
	if rows[0].Refuted {
		t.Fatal("an occupied far port was arbitrated by the fresh-silence rule as well")
	}
}

// --- The verdict must not depend on who was polled last ---

// THE FLICKER, and it is the case TestMultiNeighborPortDoesNotThrash gave false
// assurance on: that one exercises a FULLY RECIPROCATING hub, where the
// far-end-names-us-back rule bails out before freshness is ever consulted. Nothing
// covered the shape that actually alternates — a far port with exactly ONE occupant
// that does NOT name us back.
//
// Both devices here are live and collected on their own sixty-second cycles, so
// which of the two is "the fresher" swaps every few seconds. Before the margin,
// three reads differing in NOTHING but poll order returned links=2, links=1,
// links=2 (measured by probe): a topology map and an event log churning against a
// fleet where no cable had moved. "Equal-or-older never refutes" prevents ties, not
// alternation.
//
// The fix is a MARGIN, not a tie-break and not hysteresis. A tie-break would still
// have to pick a winner from two live witnesses, which is the thing there is no
// honest basis for; hysteresis would need state, and this whole derivation is pure
// by design. The margin instead asks the only question with an answer: has the
// claimant genuinely stopped moving?
func TestVerdictDoesNotFlipWithPollOrder(t *testing.T) {
	var now = time.Now().UTC()
	// a:p1 says it faces b:q1. b:q1 says it faces c:r1 and does not name a back —
	// asymmetric LLDP, or a transparent hop flooding frames. Both are live.
	var cycle = func(aAge, bAge time.Duration) []model.LLDPNeighbor {
		return markRefutedObservations([]model.LLDPNeighbor{
			{DeviceID: "a", LocalPort: "p1", PeerDevice: "b", PeerPort: "q1",
				LastSeen: now.Add(-aAge)},
			{DeviceID: "b", LocalPort: "q1", PeerDevice: "c", PeerPort: "r1",
				LastSeen: now.Add(-bAge)},
		})
	}
	// Three reads, identical except for which side was polled most recently.
	var reads = [][]model.LLDPNeighbor{
		cycle(5*time.Second, 55*time.Second),  // a leads
		cycle(55*time.Second, 5*time.Second),  // b leads
		cycle(5*time.Second, 55*time.Second),  // a leads again
		cycle(60*time.Second, 60*time.Second), // dead heat
	}
	var i int
	for i = range reads {
		var got = refutedRows(reads[i])
		if len(got) != 0 {
			t.Fatalf("read %d refuted %v: two live collectors taking turns leading by a "+
				"cycle is not evidence that a cable moved", i+1, got)
		}
		var links = deriveLLDPLinks(reads[i])
		if len(links) != 2 {
			t.Fatalf("read %d drew %d links, expected 2 on every read: the map must not "+
				"depend on poll order", i+1, len(links))
		}
	}
}

// And the margin must not neuter the feature it guards. The claimant here FROZE —
// which is the only way a claim ever survives into a later cycle, since a device
// still being collected has its rows replaced wholesale — so it is hours behind,
// clears the margin without ambiguity, and the stale cable still leaves the map.
func TestAFrozenClaimantStillClearsTheMargin(t *testing.T) {
	var now = time.Now().UTC()
	var justOver = staleWitnessMargin + time.Minute
	var cases = []struct {
		name    string
		age     time.Duration
		refuted bool
	}{
		{"one cycle behind", 60 * time.Second, false},
		{"five minutes behind, a proxy hiccup", 5 * time.Minute, false},
		{"exactly at the margin", staleWitnessMargin, false},
		{"just past the margin", justOver, true},
		{"frozen for two days", 48 * time.Hour, true},
	}
	var i int
	for i = range cases {
		var rows = markRefutedObservations([]model.LLDPNeighbor{
			{DeviceID: "a", LocalPort: "p1", PeerDevice: "b", PeerPort: "q1",
				LastSeen: now.Add(-cases[i].age)},
			{DeviceID: "b", LocalPort: "q1", PeerDevice: "c", PeerPort: "r1",
				LastSeen: now},
		})
		if rows[0].Refuted != cases[i].refuted {
			t.Errorf("%s: refuted=%v, expected %v", cases[i].name,
				rows[0].Refuted, cases[i].refuted)
		}
	}
}

// --- Refutation that ADDS an edge says so ---

// THE COUNT RUNS BACKWARDS, and the behaviour is right — only the silence was wrong.
//
// a:P hears two remotes, so the flood rule suppresses it and draws nothing. One of
// those two is a frozen claim that b:q1 contradicts, so it is discarded; a:P is left
// with a single neighbour, is no longer a flood domain, and draws the edge that was
// being hidden. One link before, two after.
//
// `counts.refuted` is published so a support read can reconcile observations against
// edges. Hand somebody a count of DISCARDED rows and they will expect the edges to
// have gone down; when they have gone up, the response reads as a derivation bug.
// The note is what stops the next hour being spent looking for one.
func TestRefutationRaisingTheLinkCountSaysSo(t *testing.T) {
	var now = time.Now().UTC()
	var rows = []model.LLDPNeighbor{
		// The frozen claim: a:P → b:q1, six hours behind.
		{DeviceID: "a", LocalPort: "P", PeerDevice: "b", PeerPort: "q1",
			LastSeen: now.Add(-6 * time.Hour)},
		// The second occupant of a:P, which the flood rule was suppressing along with
		// the first.
		{DeviceID: "a", LocalPort: "P", PeerDevice: "c", PeerPort: "r1", LastSeen: now},
		// b:q1 is held by somebody else now, and says so.
		{DeviceID: "b", LocalPort: "q1", PeerDevice: "d", PeerPort: "s1", LastSeen: now},
	}
	var before = deriveLLDPLinks(rows)
	if len(before) != 1 {
		t.Fatalf("%d links before refutation, expected 1 (a:P is a flood domain and draws "+
			"nothing): %v", len(before), before)
	}
	// Identification is a no-op here — no devices are supplied, so the endpoints set on
	// the rows survive — and no `lldp_self` means the fresh-silence rule abstains. What
	// is under test is markRefutedObservations plus the note.
	var view = buildLLDPView(rows, nil, nil)
	if len(view.Links) != 2 {
		t.Fatalf("%d links after refutation, expected 2: dropping the stale row takes a:P "+
			"below the flood threshold — %v", len(view.Links), view.Links)
	}
	if len(view.Notes) != 1 {
		t.Fatalf("%d notes, expected 1: the link count rose while an observation was "+
			"discarded, and nothing on the response said so", len(view.Notes))
	}
	if !strings.Contains(view.Notes[0], "ROSE") ||
		!strings.Contains(view.Notes[0], "from 1 to 2") {
		t.Errorf("the note does not carry the two numbers a reader is reconciling: %q",
			view.Notes[0])
	}
}

// The note is EXCEPTIONAL, and that is what makes it worth reading. It must not fire
// on the ordinary shapes: nothing refuted at all, and — the common one — refutation
// that leaves the total untouched, which is what happens when one half of a bilateral
// link is discarded and the other half stays drawn one-sided.
func TestNoNoteWhenTheCountBehavesAsExpected(t *testing.T) {
	var now = time.Now().UTC()

	// Nothing refuted: the counterfactual derivation is not even run.
	var quiet = []model.LLDPNeighbor{
		{DeviceID: "a", LocalPort: "p1", PeerDevice: "b", PeerPort: "q1", LastSeen: now},
		{DeviceID: "b", LocalPort: "q1", PeerDevice: "a", PeerPort: "p1", LastSeen: now},
	}
	var note = refutationNote(markRefutedObservations(quiet), 1)
	if note != "" {
		t.Errorf("note on a read that refuted nothing: %q", note)
	}

	// Refutation that lowers the count is the expected direction, and needs no sentence.
	var recabled = []model.LLDPNeighbor{
		{DeviceID: "a", LocalPort: "p1", PeerDevice: "b", PeerPort: "q1",
			LastSeen: now.Add(-6 * time.Hour)},
		{DeviceID: "b", LocalPort: "q1", PeerDevice: "c", PeerPort: "r1", LastSeen: now},
	}
	var view = buildLLDPView(recabled, nil, nil)
	if len(view.Links) != 1 {
		t.Fatalf("%d links, expected 1: the stale cable is gone — %v",
			len(view.Links), view.Links)
	}
	if len(view.Notes) != 0 {
		t.Errorf("note on the ordinary direction: %v", view.Notes)
	}
}

// --- A CLI-sourced device is refuted and never refutes ---

// THE ASYMMETRY IS DELIBERATE, AND IT IS ENTIRELY A CONSEQUENCE OF FRESHNESS.
//
// Neither rule reads `lldp_self.source`; this test asserts the behaviour the two
// rules produce anyway, so that the choice documented on refuteByFreshSilence cannot
// drift away from the code without a failure.
//
// Half one: a CLI device's rows are stamped at read-state and never move until an
// operator runs another one, so against an SNMP-polled device it is always the older
// witness and can never carry an arbitration. This half is a pure under-assertion —
// the worst it does is leave a stale link drawn longer — and needs no guard.
func TestCLISourcedDeviceNeverRefutes(t *testing.T) {
	var now = time.Now().UTC()
	// The S3400 was read two days ago and reports somebody else on the EdgeSwitch port
	// that the EdgeSwitch itself, polled a minute ago, says faces the S3400.
	var rows = markRefutedObservations([]model.LLDPNeighbor{
		{DeviceID: "edge", LocalPort: "GE18", PeerDevice: "s3400", PeerPort: "XG1",
			LastSeen: now.Add(-60 * time.Second)},
		{DeviceID: "s3400", LocalPort: "XG1", PeerDevice: "crs", PeerPort: "XG2",
			LastSeen: now.Add(-48 * time.Hour)},
	})
	if rows[0].Refuted {
		t.Fatalf("the polled device's current observation was refuted by %q — a read-state "+
			"two days old is the older witness, and the older witness never arbitrates",
			rows[0].RefutedBy)
	}
}

// Half two, and this one HAS TEETH: a CLI device is always refutable by the
// fresh-silence rule. That rule's premise is "only a FROZEN witness falls behind",
// where frozen means broken — a proxy that went away, a device dropped from the
// watch. For a CLI-sourced device frozen is not the broken state, it is the only
// state, so its claims clear the margin fifteen minutes after every read-state and
// stay cleared until the next gesture.
//
// KEPT ON PURPOSE. Exempting `source='cli'` would restore the stale link for exactly
// the platform the rule was built for — this file's own account names the one-off CLI
// read as the canonical frozen witness. The test pins the cost so that it is a
// decision on the record rather than a surprise in the field.
func TestCLISourcedClaimIsRefutableByFreshSilence(t *testing.T) {
	var now = time.Now().UTC()
	var read = now.Add(-(staleWitnessMargin + 5*time.Minute))
	var rows = []model.LLDPNeighbor{
		{DeviceID: "s3400", LocalPort: "XG1", PeerDevice: "edge", PeerPort: "GE18",
			LastSeen: read},
	}
	var selves = []model.LLDPSelf{
		{DeviceID: "s3400", SeenAt: read, Source: lldpSourceCLI},
		{DeviceID: "edge", SeenAt: now, Source: lldpSourceSNMP},
	}
	refuteByFreshSilence(rows, selves, rows)
	if !rows[0].Refuted {
		t.Fatal("a CLI claim whose far end is polled and reports an EMPTY port survived: " +
			"that is the stale link the frozen-witness rule exists to retire")
	}
}

// AND THE EXPOSURE IS NARROW, which is why the paragraph above is a decision rather
// than a defect. A real S3400 link survives indefinitely: the S3400 transmits LLDP,
// so its neighbour SEES it, so the far port is occupied — and an occupied far port is
// condition 3, left to the other rule, which then finds the two halves naming each
// other. Only asymmetric LLDP on a CLI-sourced device is exposed, and asymmetric
// LLDP has never been observed on this fleet.
func TestARealCLILinkSurvivesForEver(t *testing.T) {
	var now = time.Now().UTC()
	var read = now.Add(-72 * time.Hour)
	var rows = []model.LLDPNeighbor{
		{DeviceID: "s3400", LocalPort: "XG1", PeerDevice: "edge", PeerPort: "GE18",
			LastSeen: read},
		// The EdgeSwitch is polled every sixty seconds and it does see the S3400.
		{DeviceID: "edge", LocalPort: "GE18", PeerDevice: "s3400", PeerPort: "XG1",
			LastSeen: now},
	}
	var selves = []model.LLDPSelf{
		{DeviceID: "s3400", SeenAt: read, Source: lldpSourceCLI},
		{DeviceID: "edge", SeenAt: now, Source: lldpSourceSNMP},
	}
	var judged = markRefutedObservations(rows)
	refuteByFreshSilence(judged, selves, judged)
	var got = refutedRows(judged)
	if len(got) != 0 {
		t.Fatalf("refuted %v: a three-day-old read-state whose neighbour still names it "+
			"back is a confirmed cable, not a stale one", got)
	}
	var links = deriveLLDPLinks(judged)
	if len(links) != 1 || !links[0].BothSides {
		t.Fatalf("%d links, bilateral=%v: the S3400's link must stay drawn and confirmed",
			len(links), len(links) == 1 && links[0].BothSides)
	}
}

// --- The margin's floor is the COLLECTION CADENCE, and it is not thirty seconds ---

// WHAT staleWitnessMargin IS ACTUALLY MEASURED AGAINST.
//
// The constant's own reasoning starts from "several LLDP collection cycles", and
// that number was written as 30 s — the floor every connector declares on its
// `lldp` channel. The floor is not the cadence. `effectiveInterval` only ever
// RAISES a wanted cadence to a floor; it can never lower one below it, so what the
// controller pushes is [wantedLLDPInterval] itself, sixty seconds, on both the SNMP
// and gNMI paths. The margin was being justified against half the real cycle.
//
// The value does not move — fifteen minutes is fifteen cycles at 60 s and still
// clears holdtime and a proxy restart by a wide margin — but the FLOOR is now
// asserted from the cadence constant instead of from a sentence, so measuring the
// fleet's holdtimes can lower this number without anybody having to rediscover that
// alternation between two live collectors puts a bound under it.
func TestStaleWitnessMarginClearsTheCollectionCadence(t *testing.T) {
	// First: the cycle length is knowable from the constant alone, whatever floor a
	// connector declares. This is the fact the margin's arithmetic rests on.
	var floors = []time.Duration{0, 5 * time.Second, 30 * time.Second, wantedLLDPInterval}
	var i int
	for i = range floors {
		var kept, _ = effectiveInterval(wantedLLDPInterval, floors[i])
		if kept < wantedLLDPInterval {
			t.Fatalf("a connector floor of %v lowered the LLDP cadence to %v: if a floor "+
				"could pull the cadence below the wanted value, the margin below would be "+
				"sized against a cycle length nobody can name", floors[i], kept)
		}
	}
	// The measured need, and the reason this constant cannot be deleted even if the
	// fleet's holdtimes turn out to be short: two devices on independent cycles take
	// turns being the fresher of the pair by up to one full cycle. A margin at or
	// below one cycle arbitrates that jitter, which is the flicker
	// TestVerdictDoesNotFlipWithPollOrder reproduces.
	if staleWitnessMargin <= wantedLLDPInterval {
		t.Fatalf("staleWitnessMargin is %v for a collection cadence of %v: a margin no "+
			"larger than one cycle lets poll order decide the verdict again",
			staleWitnessMargin, wantedLLDPInterval)
	}
	// And the design intent, stated at the constant: SEVERAL cycles, so a proxy that
	// missed a few of them is not read as a device that froze. Five is the floor this
	// test defends; the constant is currently at fifteen, and lowering it towards five
	// is a decision to be taken with holdtimes in hand — not by accident.
	var floor = 5 * wantedLLDPInterval
	if staleWitnessMargin < floor {
		t.Fatalf("staleWitnessMargin is %v, below %v — five collection cycles at %v. "+
			"\"Several cycles\" is the constant's own first requirement", staleWitnessMargin,
			floor, wantedLLDPInterval)
	}
}

// --- What a WRONG far-port resolution costs, in each of the two rules ---

// linksBetween counts the drawn edges joining two devices, whichever end is which.
func linksBetween(links []model.LLDPLink, a, b string) int {
	var n int
	var i int
	for i = range links {
		if links[i].ADevice == a && links[i].BDevice == b {
			n++
		}
		if links[i].ADevice == b && links[i].BDevice == a {
			n++
		}
	}
	return n
}

// A WRONG FAR PORT COSTS A LINK, and this is the shape where it does.
//
// `resolvePeerPort` returning "" is covered everywhere: an unresolved far port never
// refutes. Returning the WRONG port of the RIGHT device is not covered, and since
// refutation is port-exact the claim is then judged against a port the claimant is
// not on. Before refutation existed that mis-resolution only drew a slightly wrong
// cable; now it can delete the cable altogether.
//
// The mis-resolution itself is HYPOTHETICAL — no wrong far-port resolution has been
// observed on this fleet, and the wrong port is injected here rather than produced
// by `resolve`. What the test pins is the COST if one ever occurs, and the fact that
// the same rows with the RIGHT port keep the link. That difference is the entire
// exposure, and it is on the record instead of in a TODO line.
func TestAWrongFarPortResolutionCanDeleteTheLastEdge(t *testing.T) {
	var now = time.Now().UTC()
	// The cable runs a:p1 ↔ b:GE2. `b` does not name `a` back — asymmetric LLDP, or a
	// transparent hop — so a's own row is the ONLY witness of it, and a has frozen.
	var rows = func(farPort string) []model.LLDPNeighbor {
		return []model.LLDPNeighbor{
			{DeviceID: "a", LocalPort: "p1", PeerDevice: "b", PeerPort: farPort,
				LastSeen: now.Add(-6 * time.Hour)},
			// An unrelated live cable on b:GE1, naming somebody else.
			{DeviceID: "b", LocalPort: "GE1", PeerDevice: "c", PeerPort: "r1",
				LastSeen: now},
		}
	}
	// Right port: b:GE2 has no rows at all, silence never refutes, the cable stays.
	var right = markRefutedObservations(rows("GE2"))
	if len(refutedRows(right)) != 0 {
		t.Fatalf("refuted %v with a correctly resolved far port: b:GE2 lists nobody, and "+
			"silence never refutes", refutedRows(right))
	}
	if linksBetween(deriveLLDPLinks(right), "a", "b") != 1 {
		t.Fatal("the a↔b cable must stay drawn when the far port is resolved correctly")
	}
	// Wrong port: b:GE1 is held by exactly one stranger, fresher by six hours and not
	// naming a back — every refusal in markRefutedObservations passes, and the only
	// witness of a real cable is discarded.
	var wrong = markRefutedObservations(rows("GE1"))
	if len(refutedRows(wrong)) != 1 {
		t.Fatalf("refuted %v: the exposure being pinned is that a wrong far port DOES "+
			"refute here — if that has changed, this test and resolvePeerPort's comment "+
			"both need rewriting", refutedRows(wrong))
	}
	if linksBetween(deriveLLDPLinks(wrong), "a", "b") != 0 {
		t.Fatal("expected the a↔b edge to be gone: that is the cost of a wrong far-port " +
			"resolution, and pretending otherwise would hide it")
	}
}

// AND IN THE OTHER RULE THE SAME MISTAKE IS BENIGN, which is why the exposure is
// narrower than it reads.
//
// [refuteByFreshSilence]'s condition 3 — "the far port lists nobody" — cannot save a
// mis-resolution, because a port the claimant is not really on is empty by
// construction. So the wrong claim IS retired. But that rule's condition 1 requires
// the far device to be collected, and a collected far end that sees the cable keeps
// drawing it from its own side, on the right port. What leaves the map is the row
// that was drawn on the wrong port; the cable stays.
func TestAWrongFarPortInTheSilenceRuleRetiresOnlyTheWrongRow(t *testing.T) {
	var now = time.Now().UTC()
	var frozen = now.Add(-6 * time.Hour)
	var rows = func(farPort string) []model.LLDPNeighbor {
		return []model.LLDPNeighbor{
			// The frozen claimant, pointing at `farPort` on b.
			{DeviceID: "a", LocalPort: "p1", PeerDevice: "b", PeerPort: farPort,
				LastSeen: frozen},
			// b is polled and DOES see a — on GE2, where the cable actually is.
			{DeviceID: "b", LocalPort: "GE2", PeerDevice: "a", PeerPort: "p1",
				LastSeen: now},
		}
	}
	var selves = []model.LLDPSelf{selfAt("a", frozen), selfAt("b", now)}
	// Right port: b:GE2 is occupied, condition 3 sends the contradiction to the other
	// rule, nothing is refuted and the link is confirmed from both sides.
	var right = rows("GE2")
	refuteByFreshSilence(right, selves, right)
	if len(refutedRows(right)) != 0 {
		t.Fatalf("refuted %v: an occupied far port is condition 3, left to the other rule",
			refutedRows(right))
	}
	var rightLinks = deriveLLDPLinks(right)
	if len(rightLinks) != 1 || !rightLinks[0].BothSides {
		t.Fatalf("%d links: the correctly resolved cable must be drawn once and bilateral",
			len(rightLinks))
	}
	// Wrong port: b:GE1 lists nobody, so the frozen row is retired. The cable is NOT
	// lost — b still draws it, on the port it is really on.
	var wrong = rows("GE1")
	refuteByFreshSilence(wrong, selves, wrong)
	if !wrong[0].Refuted {
		t.Fatal("a claim on a far port that lists nobody survived: condition 3 cannot " +
			"protect a mis-resolution, and the comment on resolvePeerPort says so")
	}
	var wrongLinks = deriveLLDPLinks(wrong)
	if linksBetween(wrongLinks, "a", "b") != 1 {
		t.Fatalf("%d a↔b links after retiring the wrong-port row, expected 1: the far end "+
			"is collected by condition 1, so it keeps drawing the cable itself",
			linksBetween(wrongLinks, "a", "b"))
	}
	if wrongLinks[0].BPort != "p1" || wrongLinks[0].APort != "GE2" {
		t.Fatalf("the surviving edge is %s:%s → %s:%s, expected b:GE2 → a:p1 — the edge "+
			"that remains must be the one on the RIGHT port", wrongLinks[0].ADevice,
			wrongLinks[0].APort, wrongLinks[0].BDevice, wrongLinks[0].BPort)
	}
}
