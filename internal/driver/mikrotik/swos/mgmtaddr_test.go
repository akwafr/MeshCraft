package swos

import (
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// Reading management addressing, verified on the reference device's REAL
// CAPTURE.
//
// This test is also the SURVEY that justifies the per-device addressing mode
// (migration 0027): it observes, on hardware, that a CSS326 adopted in the
// right management subnet is on a FIXED address there, while the site plan
// says "external DHCP". If `iptp` ever stopped being 1 in this capture, it is
// not the test that would need fixing, it is the justification that would
// need re-reading.

// TestRealMgmtAddr: /sys.b as the switch served it.
func TestRealMgmtAddr(t *testing.T) {
	var page, err = ParseObject(capture(t, "sys.b"))
	if err != nil {
		t.Fatalf("/sys.b refused: %v", err)
	}
	var got = MgmtAddrOf(page)

	if got.Mode != driver.MgmtAddrModeStatic {
		t.Errorf("mode = %q, \"static\" expected: the capture carries iptp:0x01, and that is "+
			"the whole point of the survey", got.Mode)
	}
	if got.Address != "192.0.2.36" {
		t.Errorf("address = %q, \"192.0.2.36\" expected (cip:0x240200c0 little-endian)",
			got.Address)
	}
	// The platform has NEITHER a gateway NOR DNS: both fields must stay empty,
	// and filling them with any value would be worse than emptiness.
	if got.Gateway != "" || len(got.DNS) != 0 {
		t.Errorf("gateway = %q, DNS = %v: SwOS carries none (field set of "+
			"/sys.b enumerated by backup.swb)", got.Gateway, got.DNS)
	}
	// Absence of gateway/DNS lives on MgmtCapabilities (Gateway/DNS/Routed/IPARP),
	// not in observation notes — so the UI can localize it.
	if hasNoteAbout(got.Notes, "gateway") {
		t.Errorf("platform capability leaked into observation notes: %v", got.Notes)
	}
	// No anomaly to flag: on this device `ip` and `cip` are equal, so the gap
	// note must NOT appear.
	if hasNoteAbout(got.Notes, "but answers on") {
		t.Errorf("configured / current address gap wrongly flagged: %v", got.Notes)
	}
}

// TestMgmtAddrModes: the selector's three modes, plus the two cases where no
// conclusion is drawn.
//
// The last two cases are the most important of the table: a firmware that
// returned a fourth value, or did not serve the field, must produce NO mode.
// Guessing "static" would keep a lease address as intentional, and it may
// change at any moment.
func TestMgmtAddrModes(t *testing.T) {
	var cases = []struct {
		name      string
		iptp      uint32
		absent    bool
		expected  string
		noteAbout string
	}{
		{name: "static", iptp: AddrStatic, expected: driver.MgmtAddrModeStatic},
		{name: "DHCP only", iptp: AddrDHCPOnly, expected: driver.MgmtAddrModeDHCP},
		{
			name: "DHCP with fallback", iptp: AddrDHCPFallback,
			expected: driver.MgmtAddrModeDHCP, noteAbout: "fallback",
		},
		{name: "unknown value", iptp: 7, expected: "", noteAbout: "unrecognised"},
		{name: "missing field", absent: true, expected: "", noteAbout: "does not serve the"},
	}

	var c = cases[0]
	for _, c = range cases {
		var page = NewPage()
		page.SetNum(FieldCurrentIP, 0x240200c0)
		page.SetNum(FieldIP, 0x240200c0)
		if !c.absent {
			page.SetNum(FieldAddrMode, c.iptp)
		}
		var got = MgmtAddrOf(page)
		if got.Mode != c.expected {
			t.Errorf("%s: mode = %q, %q expected", c.name, got.Mode, c.expected)
		}
		if c.noteAbout != "" && !hasNoteAbout(got.Notes, c.noteAbout) {
			t.Errorf("%s: no note containing %q — notes = %v",
				c.name, c.noteAbout, got.Notes)
		}
	}
}

// TestMgmtAddrCurrentWinsOverConfigured: on DHCP, the CURRENT address is the
// one displayed.
//
// The trap this test locks: returning `ip` would display the fallback address
// of a device on a lease, i.e. an address it does not answer on. It is the
// kind of value an operator copies into a browser before concluding there is
// an outage.
func TestMgmtAddrCurrentWinsOverConfigured(t *testing.T) {
	var page = NewPage()
	page.SetNum(FieldAddrMode, AddrDHCPFallback)
	page.SetNum(FieldIP, 0x0158a8c0)        // 192.168.88.1, the fallback
	page.SetNum(FieldCurrentIP, 0x2a0200c0) // 192.0.2.42, the lease
	var got = MgmtAddrOf(page)
	if got.Address != "192.0.2.42" {
		t.Errorf("address = %q, \"192.0.2.42\" expected (the lease, not the fallback)", got.Address)
	}
	// A gap between configured and current is NORMAL on DHCP: no anomaly to
	// flag, otherwise the whole range on leases would be shown as
	// inconsistent.
	if hasNoteAbout(got.Notes, "but answers on") {
		t.Errorf("gap wrongly flagged on a DHCP device: %v", got.Notes)
	}
}

// TestMgmtAddrStaticInconsistency: static AND two different addresses is a
// real anomaly, and it is said.
//
// It is the only case where the gap between `ip` and `cip` deserves to be
// flagged: one of the two fields was changed without the other following, and
// writing on this page without having seen it would set an address nobody
// asked for.
func TestMgmtAddrStaticInconsistency(t *testing.T) {
	var page = NewPage()
	page.SetNum(FieldAddrMode, AddrStatic)
	page.SetNum(FieldIP, 0x240200c0)        // 192.0.2.36, configured
	page.SetNum(FieldCurrentIP, 0x2a0200c0) // 192.0.2.42, current
	var got = MgmtAddrOf(page)
	if !hasNoteAbout(got.Notes, "but answers on") {
		t.Errorf("inconsistency not flagged on a static device: %v", got.Notes)
	}
}

// TestMgmtAddrWithoutAddress: a page without any address makes nothing up.
func TestMgmtAddrWithoutAddress(t *testing.T) {
	var page = NewPage()
	page.SetNum(FieldAddrMode, AddrStatic)
	var got = MgmtAddrOf(page)
	if got.Address != "" {
		t.Errorf("address = %q on a page that carries none", got.Address)
	}
}

func hasNoteAbout(notes []string, pattern string) bool {
	var n string
	for _, n = range notes {
		if strings.Contains(n, pattern) {
			return true
		}
	}
	return false
}
