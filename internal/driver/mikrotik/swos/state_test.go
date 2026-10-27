package swos

import (
	"reflect"
	"testing"
)

// Reading the applied state, verified on the device's REAL captures.
//
// These tests are what make the translation of the SwOS VLAN model verifiable
// by someone else: they replay /vlan.b, /fwd.b and /link.b as the reference
// CSS326-24G-2S+ served them, and verify that the rebuilt configuration is the
// one that explains the device's observed behaviour.

// referenceState reads the three pages of the capture and rebuilds the state,
// without the network.
func referenceState(t *testing.T) (map[string]PortState, VLANFiltering) {
	t.Helper()
	var vlanRows, err = ParseTable(capture(t, "vlan.b"))
	if err != nil {
		t.Fatalf("/vlan.b: %v", err)
	}
	var fwd *Page
	fwd, err = ParseObject(capture(t, "fwd.b"))
	if err != nil {
		t.Fatalf("/fwd.b: %v", err)
	}
	var st, filtering, buildErr = portsFrom(vlanRows, fwd, facadeCSS326())
	if buildErr != nil {
		t.Fatalf("reconstruction refused: %v", buildErr)
	}
	return st, filtering
}

// TestReferenceVLANMembership: the translation of the SwOS VLAN model.
//
// The reference device carries two VLANs (99 and 100), all ports members of
// both, a "Default VLAN ID" of 100 on the 24 copper ports and of 1 on the two
// SFP cages. The SwOS tagging rule — a frame leaves untagged if its VLAN is
// the port's native VLAN, tagged otherwise — therefore gives:
//
//	Port1…Port24 : native 100, tagged [99]        → trunk
//	SFP1 / SFP2  : native 1,   tagged [99, 100]   → trunk
//
// THE NAMES ARE THE DEVICE'S, and that is what the capture establishes:
// /link.b serves "Port1"… "Port24", "SFP1", "SFP2" in its `nm` field (cf.
// naming.go and TestPortNamesAreTheDevices).
//
// WHAT THIS READ PREDICTS, AND WHICH IS VERIFIED: cage SFP2 carries VLAN 99
// TAGGED. Now that is the only port of this device carrying a link, and the
// device is administered in VLAN 99 — and it answers. An inverted tagging
// rule would predict that VLAN 99 leaves this port untagged and VLAN 1 leaves
// it tagged, which would make the device mute. The translation is therefore
// corroborated by the only switching fact observable from the outside.
func TestReferenceVLANMembership(t *testing.T) {
	var st, _ = referenceState(t)

	if len(st) != 26 {
		t.Fatalf("%d port(s) rebuilt, expected 26", len(st))
	}

	// The 24 copper ports.
	var i int
	for i = 1; i <= 24; i++ {
		var name = CopperPortPrefix + itoa(i)
		var p = st[name]
		if p.Mode != "trunk" || p.PVID != 100 ||
			!reflect.DeepEqual(p.Tagged, []int{99}) {
			t.Errorf("%s = %+v; expected a trunk with native 100 tagged [99]", name, p)
		}
	}
	// The two SFP cages, whose native VLAN is 1: they therefore carry 99 AND
	// 100 tagged.
	for i = 1; i <= 2; i++ {
		var name = SFPPortPrefix + itoa(i)
		var p = st[name]
		if p.Mode != "trunk" || p.PVID != 1 ||
			!reflect.DeepEqual(p.Tagged, []int{99, 100}) {
			t.Errorf("%s = %+v; expected a trunk with native 1 tagged [99 100] — it is this "+
				"port that carries administration in VLAN 99 tagged", name, p)
		}
	}
}

// TestNativeVLANNeverTagged: the heart of the tagging rule.
//
// A port that is a member of a VLAN which is also its native VLAN must NEVER
// appear as tagged in that VLAN. Doing so would produce a port that looks both
// native and tagged in the same VLAN — which reconciliation would read as a
// permanent deviation, and which the write would try to "fix" on every pass.
func TestNativeVLANNeverTagged(t *testing.T) {
	// A single port, member of 10 and 20, whose native VLAN is 10.
	var rows, err = ParseTable(
		"[{nm:'',mbr:0x01,vid:0x000a},{nm:'',mbr:0x01,vid:0x0014}]")
	if err != nil {
		t.Fatalf("table refused: %v", err)
	}
	var fwd *Page
	fwd, err = ParseObject("{vlan:[0x02],dvid:[0x000a]}")
	if err != nil {
		t.Fatalf("page refused: %v", err)
	}
	var st, _, buildErr = portsFrom(rows, fwd, facadeCopper(1))
	if buildErr != nil {
		t.Fatalf("reconstruction refused: %v", buildErr)
	}
	var p = st["Port1"]
	if p.PVID != 10 {
		t.Errorf("PVID = %d, expected 10", p.PVID)
	}
	if !reflect.DeepEqual(p.Tagged, []int{20}) {
		t.Errorf("tagged = %v, expected [20]: the native VLAN must not appear there", p.Tagged)
	}
}

// TestPortWithoutTaggedVLANIsAccess: a port that is a member of its native
// VLAN only is an access port, not a trunk. Classifying it as a trunk would
// make the intent diverge from what the device will always report.
func TestPortWithoutTaggedVLANIsAccess(t *testing.T) {
	var rows, err = ParseTable("[{nm:'',mbr:0x01,vid:0x0014}]")
	if err != nil {
		t.Fatalf("table refused: %v", err)
	}
	var fwd *Page
	fwd, err = ParseObject("{vlan:[0x02],dvid:[0x0014]}")
	if err != nil {
		t.Fatalf("page refused: %v", err)
	}
	var st, _, buildErr = portsFrom(rows, fwd, facadeCopper(1))
	if buildErr != nil {
		t.Fatalf("reconstruction refused: %v", buildErr)
	}
	if st["Port1"].Mode != "access" || st["Port1"].PVID != 20 || st["Port1"].Tagged != nil {
		t.Errorf("Port1 = %+v, expected an access port in VLAN 20 with no tagged VLAN", st["Port1"])
	}
}

// TestVLANFilteringReported: "disabled" must be visible.
//
// A port whose VLAN filtering is off carries an INERT VLAN configuration: the
// PVID and membership are indeed there, but the port does not apply them.
// Keeping quiet about that distinction would present a configuration as
// active when it does nothing — and that is the kind of silence that sends
// someone hunting for a fault elsewhere.
func TestVLANFilteringReported(t *testing.T) {
	// Two ports: the first with filtering disabled, the second on "strict".
	var rows, err = ParseTable("[{nm:'',mbr:0x03,vid:0x000a}]")
	if err != nil {
		t.Fatalf("table refused: %v", err)
	}
	var fwd *Page
	fwd, err = ParseObject("{vlan:[0x00,0x03],dvid:[0x000a,0x000a]}")
	if err != nil {
		t.Fatalf("page refused: %v", err)
	}
	var _, filtering, buildErr = portsFrom(rows, fwd, facadeCopper(2))
	if buildErr != nil {
		t.Fatalf("reconstruction refused: %v", buildErr)
	}
	if filtering["Port1"] != "disabled" || filtering["Port2"] != "strict" {
		t.Errorf("filtering = %v, expected Port1 disabled and Port2 strict", filtering)
	}
	// And the per-port mode can be returned as a usable list: it is what
	// becomes the mandatory note of State.Notes (cf. stateOf). The per-port
	// VLAN mode is NOT a standard state field — there is nothing universal
	// about it — but what it implies must reach the operator.
	if !reflect.DeepEqual(filtering.Disabled(), []string{"Port1"}) {
		t.Errorf("Disabled = %v, expected [Port1]", filtering.Disabled())
	}
}

// TestReconstructionRefusesAnIncompletePage: better to refuse than to guess.
//
// On this platform, a write REWRITES the whole table. A read that guessed a
// port's membership would produce a write that sets it for real.
func TestReconstructionRefusesAnIncompletePage(t *testing.T) {
	var rows, _ = ParseTable("[{nm:'',mbr:0x01,vid:0x000a}]")

	var cases = []struct {
		name, fwd string
		ports     int
	}{
		{"no native VLANs", "{vlan:[0x01]}", 1},
		{"native VLANs too short", "{vlan:[0x01,0x01],dvid:[0x000a]}", 2},
	}
	var c = cases[0]
	for _, c = range cases {
		t.Run(c.name, func(t *testing.T) {
			var fwd, err = ParseObject(c.fwd)
			if err != nil {
				t.Fatalf("page refused: %v", err)
			}
			var _, _, buildErr = portsFrom(rows, fwd, facadeCopper(c.ports))
			if buildErr == nil {
				t.Error("reconstruction accepted on an incomplete page")
			}
		})
	}

	// And an unreadable member mask must stop the read: guessing a port's
	// membership means deciding for it.
	var bad, _ = ParseTable("[{nm:'',vid:0x000a}]")
	var minimal, _ = ParseObject("{vlan:[0x01],dvid:[0x000a]}")
	var _, _, err = portsFrom(bad, minimal, facadeCopper(1))
	if err == nil {
		t.Error("a VLAN without a member mask was accepted")
	}
}

// TestVLANTableWithoutIdentifierRefused: losing a row would mean losing a
// VLAN.
func TestVLANTableWithoutIdentifierRefused(t *testing.T) {
	var rows, err = ParseTable("[{nm:'',mbr:0x01,vid:0x000a},{nm:'',mbr:0x02}]")
	if err != nil {
		t.Fatalf("table refused by the parser: %v", err)
	}
	var _, vlanErr = vlansFrom(rows)
	if vlanErr == nil {
		t.Error("a row without an identifier was silently ignored: the rewrite " +
			"of the table would lose that VLAN")
	}
}

// TestReferenceLinks: link state, and why a missing mask is an error.
//
// Assuming "no link" would lift the write guard on ALL ports, i.e. the exact
// opposite of caution.
func TestReferenceLinks(t *testing.T) {
	var link, err = ParseObject(capture(t, "link.b"))
	if err != nil {
		t.Fatalf("/link.b: %v", err)
	}
	var up, linkErr = linksFrom(link, facadeCSS326())
	if linkErr != nil {
		t.Fatalf("link read refused: %v", linkErr)
	}
	if !reflect.DeepEqual(up, map[string]bool{"SFP2": true}) {
		t.Errorf("active links = %v; the capture carries only one, cage SFP2 — which "+
			"the device names \"SFP2\" and the old faceplate called \"SFP2\"", up)
	}

	// Missing mask: an error, not "no link".
	var without *Page
	without, err = ParseObject("{prt:0x1a}")
	if err != nil {
		t.Fatalf("page refused: %v", err)
	}
	var _, e = linksFrom(without, facadeCSS326())
	if e == nil {
		t.Error("a missing link mask was read as \"no link\": the write guard " +
			"would be lifted on every port")
	}
}

// TestReferenceSNMP: /snmp.b, and the requirement that its fields be present.
func TestReferenceSNMP(t *testing.T) {
	var page, err = ParseObject(capture(t, "snmp.b"))
	if err != nil {
		t.Fatalf("/snmp.b: %v", err)
	}
	var cfg, snmpErr = snmpFrom(page)
	if snmpErr != nil {
		t.Fatalf("SNMP read refused: %v", snmpErr)
	}
	if !cfg.Enabled {
		t.Error("the agent is enabled in the capture (`en:0x01`)")
	}
	// The capture's community is a placeholder: the production device's secret
	// is not in the repository. What is verified is the DECODING, not the
	// value.
	if cfg.Community != "REDACTED-SNMP-1" {
		t.Errorf("decoded community = %q", cfg.Community)
	}
	if cfg.Contact != "" || cfg.Location != "" {
		t.Errorf("contact and location are empty in the capture, got %q / %q",
			cfg.Contact, cfg.Location)
	}

	// A missing mandatory field must stop the read: a write that followed
	// would send back an empty community and silently cut monitoring.
	var truncated *Page
	truncated, err = ParseObject("{en:0x01}")
	if err != nil {
		t.Fatalf("page refused: %v", err)
	}
	var _, e = snmpFrom(truncated)
	if e == nil {
		t.Error("a /snmp.b page without a community was accepted")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var digits []byte
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}
