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
// survey (docs/01-Architecture/Voisinage LLDP.md).
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
			"salle-b-baie-12", "salle-b-baie-12",
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
		ChassisID: "0x8825101761a7", SysName: "CN2AKPH86J",
		// …but frames emitted on port 50 carry "d9", which is here.
		PortIDs: []string{"0x8825101761d9", "0x8825101761db", "51"},
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
// The CRS326 recorded a neighbor with chassis "88:25:10:17:61:D9". That value appears
// NOWHERE in the 1930's declared identity (`0x8825101761a7`): it is port 50's MAC. Only
// `lldp_self.port_ids` enables the match.
func TestRecognitionByPortID(t *testing.T) {
	var idx = buildLLDPIdentity([]model.LLDPSelf{selfCRS(), self1930()},
		[]model.Device{deviceCRS(), device1930()})

	var seen = model.LLDPNeighbor{
		DeviceID: idCRS, LocalPort: "sfp-sfpplus2",
		ChassisID: "88:25:10:17:61:D9", PortID: "50", SysName: "CN2AKPH86J",
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
	// "50" resolves by the THIRD `resolve` scale: facade prefix + label.
	var port = resolvePeerPort(idx, peer, seen)
	if port != "GE50" {
		t.Errorf("resolved remote port %q, expected \"GE50\": the 1930 facade names "+
			"\"GE50\" what the device calls \"50\"", port)
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
	if port := resolvePeerPort(idx, idCRS, seen); port != "" {
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
			ChassisID: "88:25:10:17:61:D9", PortID: "50", SysName: "CN2AKPH86J",
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
			DeviceID: idCRS, LocalPort: "sfp-sfpplus2", ChassisID: "88:25:10:17:61:D9",
			SysName: "CN2AKPH86J", PeerDevice: id193, PeerPort: "GE50",
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
	if got := peerPortLabel(model.LLDPNeighbor{PortID: "sfp-sfpplus2"}); got != "sfp-sfpplus2" {
		t.Errorf("readable portID: %q", got)
	}
	if got := peerPortLabel(model.LLDPNeighbor{
		PortID: "0x8825101761d9", PortDesc: "50"}); got != "50" {
		t.Errorf("MAC portID: %q, expected \"50\"", got)
	}
	if got := peerPortLabel(model.LLDPNeighbor{PortDesc: "Eth 1/28"}); got != "Eth 1/28" {
		t.Errorf("absent portID: %q", got)
	}
	if got := peerPortLabel(model.LLDPNeighbor{}); got != "" {
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
	if _, silent := reason[idCRS]; silent {
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
// the fleet's bilateral link comes out recognized.
//
// It locks the batch's most fragile junction: proxy, facade resolution, and derivation must
// agree on a port NAME, otherwise the two halves of a link do not recognize each other.
func TestEndToEndChain(t *testing.T) {
	// What the proxy would push for the CRS326, having read only the neighbor that matters.
	var crsSide = snmppoll.LLDPReport{
		DeviceID: idCRS, ChassisID: "B8:69:F4:5B:A0:D2", SysName: "lan-r4",
		PortIDs: []string{"sfp-sfpplus2"}, Served: true,
		Neighbors: []snmppoll.Neighbor{{
			LocalPort: "sfp-sfpplus2", LocalPortNum: 26,
			ChassisID: "88:25:10:17:61:D9", PortID: "50", SysName: "CN2AKPH86J",
		}},
	}
	// And what the proxy would push for the 1930. Its local port is "50" — the
	// `lldpLocPortDesc`, since its `lldpLocPortId` is a MAC — and `resolve` must turn that
	// into "GE50".
	var side1930 = snmppoll.LLDPReport{
		DeviceID: id193, ChassisID: "0x8825101761a7", SysName: "CN2AKPH86J",
		PortIDs: []string{"0x8825101761d9"}, Served: true,
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
	if len(view.Links) != 1 {
		t.Fatalf("%d links, expected 1 (bilateral)", len(view.Links))
	}
	if !view.Links[0].BothSides {
		t.Fatal("link lab-crs326:sfp-sfpplus2 ↔ sw-labo-1930:GE50 must come out SEEN FROM " +
			"BOTH SIDES — the fleet's only one, and only thanks to port identifier " +
			"recognition path")
	}
	// And the path must be NAMED on both sides: chassis on one, port identifier on the
	// other, because the 1930 emits a chassis-id it does not declare.
	var paths = map[string]string{}
	var i int
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
