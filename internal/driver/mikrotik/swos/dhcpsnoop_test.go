package swos

import (
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// DHCP snooping trust, verified against the REAL captures in testdata/ (cf.
// parse_test.go's header: these are the pages the reference CSS326-24G-2S+
// actually served, not hand-written examples).
//
// The pure parts are tested here — the mask arithmetic, the geometry, and the
// refusals — because the write path itself needs a device: it is a
// read-modify-rewrite POST of /sys.b, and what matters about it (that a partial
// page is REFUSED rather than posted) is already the rule writeSysMgmt is tested
// under.

// TestDtrpCaptured pins what the reference device actually carries, because the
// value is more interesting than a round-trip assertion would be.
//
// `dtrp:0x03ffffff` is 26 bits set: EVERY port of this 24+2 switch is TRUSTED.
// On a platform where the trust mask is the entire feature, that means DHCP
// snooping protects nothing at all on this unit — every jack may answer DHCP.
// That is the factory posture, and it is worth a test of its own: it is the
// baseline any future capture gets compared against, and if a firmware ever
// ships a different default this test is what notices.
func TestDtrpCaptured(t *testing.T) {
	var page, err = ParseObject(capture(t, "sys.b"))
	if err != nil {
		t.Fatalf("/sys.b refused: %v", err)
	}
	var mask, ok = page.Num(FieldDHCPTrusted)
	if !ok {
		t.Fatalf("no \"%s\" field in the captured /sys.b — the whole DHCP "+
			"snooping read rests on it", FieldDHCPTrusted)
	}
	if mask != 0x03ffffff {
		t.Errorf("%s = %#x, capture carries 0x03ffffff", FieldDHCPTrusted, mask)
	}
	// All 26 ports, bit by bit: the mask is read through Bit() in production, so
	// that is what gets exercised here rather than the raw number.
	var i int
	for i = 0; i < 26; i++ {
		var trusted, readOK = page.Bit(FieldDHCPTrusted, i)
		if !readOK {
			t.Fatalf("bit %d unreadable on %s", i, FieldDHCPTrusted)
		}
		if !trusted {
			t.Errorf("bit %d = untrusted, the captured mask has every port trusted", i)
		}
	}
}

// TestDhcpTrustedIsInWritableSet guards the read-modify-rewrite contract: a
// /sys.b POST carries the whole writable set, so `dtrp` must be IN that set or a
// trust write would be silently dropped from the body — and `ainf` must be there
// too, or posting would let the device zero option 82 as collateral damage.
func TestDhcpTrustedIsInWritableSet(t *testing.T) {
	var fields, ok = WritableFields(PageSys)
	if !ok {
		t.Fatal("no writable field set declared for " + PageSys)
	}
	var want = []string{FieldDHCPTrusted, FieldDHCPOption82}
	var w string
	for _, w = range want {
		var found bool
		var f string
		for _, f = range fields {
			if f == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("\"%s\" missing from the writable set of %s: a trust write "+
				"would post a body without it", w, PageSys)
		}
	}
}

// TestCapturedSysServesEveryWritableField is the precondition the applier
// refuses on. If the reference capture itself were missing a writable field, the
// refusal path would fire on a healthy device and no trust could ever be
// written — so this asserts the refusal is aimed at genuinely odd firmware, not
// at the platform as captured.
func TestCapturedSysServesEveryWritableField(t *testing.T) {
	var page, err = ParseObject(capture(t, "sys.b"))
	if err != nil {
		t.Fatalf("/sys.b refused: %v", err)
	}
	var fields, ok = WritableFields(PageSys)
	if !ok {
		t.Fatal("no writable field set declared for " + PageSys)
	}
	var _, missing = page.Only(fields)
	if len(missing) > 0 {
		t.Errorf("captured /sys.b does not serve %v — the applier would refuse "+
			"every write on a healthy device", missing)
	}
}

// TestSetBitOnDtrp exercises the exact painting the applier performs: clear one
// port's bit, leave every other alone. The "leave the others alone" half is
// the contract's "a port absent from the plan is NOT MANAGED" rule, and on this
// platform it holds by construction — which is only true if SetBit really is
// surgical, so that gets asserted rather than assumed.
func TestSetBitOnDtrp(t *testing.T) {
	var page, err = ParseObject(capture(t, "sys.b"))
	if err != nil {
		t.Fatalf("/sys.b refused: %v", err)
	}
	if !page.SetBit(FieldDHCPTrusted, 4, false) {
		t.Fatal("SetBit refused on a field the capture carries")
	}
	var mask, ok = page.Num(FieldDHCPTrusted)
	if !ok {
		t.Fatal("mask unreadable after SetBit")
	}
	if mask != 0x03ffffef {
		t.Errorf("mask = %#x, expected 0x03ffffef (bit 4 cleared, rest kept)", mask)
	}
	var trusted bool
	trusted, ok = page.Bit(FieldDHCPTrusted, 4)
	if !ok || trusted {
		t.Errorf("bit 4 still trusted after being cleared (ok=%v)", ok)
	}
	// A neighbour must be untouched: clearing one jack's trust must not open or
	// close another's.
	trusted, ok = page.Bit(FieldDHCPTrusted, 5)
	if !ok || !trusted {
		t.Errorf("bit 5 changed as collateral (trusted=%v, ok=%v)", trusted, ok)
	}
}

// TestIntsListSorted: the VLAN list travels into an operator-facing sentence, so
// two identical requests must produce two identical sentences — an unsorted list
// would make the same refusal read differently on every apply.
func TestIntsListSorted(t *testing.T) {
	var got = intsList([]int{30, 10, 20})
	if got != "10, 20, 30" {
		t.Errorf("intsList = %q, expected \"10, 20, 30\"", got)
	}
	if intsList(nil) != "" {
		t.Errorf("intsList(nil) = %q, expected empty", intsList(nil))
	}
}

// TestNoVLANDimensionNamesTheEvidence: the sentence returned to the caller is
// the whole deliverable of this platform's refusal, so it must carry WHY the
// absence is knowable — an enumeration, not a failed search. A future edit that
// shortens it to "not supported" would send an operator looking for a checkbox
// that does not exist.
func TestNoVLANDimensionNamesTheEvidence(t *testing.T) {
	var want = []string{"per-VLAN", "dtrp", "backup"}
	var w string
	for _, w = range want {
		if !contains(noVLANDimension, w) {
			t.Errorf("the refusal sentence no longer mentions %q: %s", w, noVLANDimension)
		}
	}
}

func contains(haystack, needle string) bool {
	var i int
	for i = 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// The same regression as [TestVerifyStormWritesAcceptsEitherNamingFamily], on the
// applier next door. Found by reading rather than by hardware: the storm exercise
// on the lab CSS326 (2026-08-08) exposed the pattern, and this applier had it too.
//
// It matters more here than there. A trust flag is what stops a rogue DHCP
// server; reporting an applied one as unconfirmed sends an operator to re-apply a
// bit that is already correct, on a page that also carries the management
// allow-list.
func TestVerifySnoopWritesAcceptsEitherNamingFamily(t *testing.T) {
	var facade = NewFacade(26, 2, 24, "GE")
	// The device always answers in its own family.
	var after = driver.DHCPSnoopState{Ports: []driver.DHCPSnoopPortState{
		{Port: "Port2", Trusted: true},
		{Port: "Port3", Trusted: false},
	}}

	var planned string
	for _, planned = range []string{"Port2", "GE2"} {
		t.Run(planned, func(t *testing.T) {
			var idx, ok = facade.Index(planned)
			if !ok {
				t.Fatalf("%s does not resolve", planned)
			}
			var confirmed, disagreed, unseen = verifySnoopWrites(after, facade,
				[]snoopWrite{{Name: planned, Idx: idx, Trusted: true}})
			if len(disagreed) > 0 || len(unseen) > 0 {
				t.Errorf("the device shows Port2 trusted and a plan naming it %q was told "+
					"otherwise: disagreed=%v unseen=%v", planned, disagreed, unseen)
			}
			if len(confirmed) != 1 || confirmed[0] != planned {
				t.Errorf("confirmed = %v, want [%s]", confirmed, planned)
			}
		})
	}
}

// The three outcomes are three different claims and must stay apart: a device
// that DECLINED the flag is not a device whose port vanished from the re-read.
func TestVerifySnoopWritesSeparatesRefusalFromAbsence(t *testing.T) {
	var facade = NewFacade(26, 2, 24, "GE")
	var after = driver.DHCPSnoopState{Ports: []driver.DHCPSnoopPortState{
		{Port: "Port2", Trusted: false}, // asked for trusted, read untrusted
		// Port5 is absent from the re-read entirely.
	}}
	var confirmed, disagreed, unseen = verifySnoopWrites(after, facade, []snoopWrite{
		{Name: "GE2", Idx: 1, Trusted: true},
		{Name: "GE5", Idx: 4, Trusted: true},
	})
	if len(confirmed) != 0 {
		t.Errorf("confirmed = %v, want none", confirmed)
	}
	if len(disagreed) != 1 || !strings.Contains(disagreed[0], "GE2") {
		t.Errorf("disagreed = %v, want one line naming GE2 as declined", disagreed)
	}
	if len(unseen) != 1 || !strings.Contains(unseen[0], "GE5") {
		t.Errorf("unseen = %v, want one line naming GE5 as absent from the re-read", unseen)
	}
}

// Declared 2026-08-09, on the one write on this platform that could end the
// session: /sys.b carries `ip`, `avln`, `allp` and `mgmt` beside `dtrp` and is
// posted whole. The exercise on the lab CSS326 moved `dtrp` and exactly one of
// the twenty-two posted fields, asserted the four access fields by name,
// converged on a second apply, and left the page equal. capabilities.go carries
// the evidence; live_stormcontrol_test.go carries the exercise.
func TestWritesExtraDHCPSnoopingIsDeclared(t *testing.T) {
	if !WritesExtra("dhcp_snooping") {
		t.Fatal("dhcp_snooping was proven on hardware on 2026-08-09 and the declaration " +
			"has been lost — the operator is being told meshcraft cannot do something it can")
	}
	// The feature cannot be turned OFF here, and that is structural: there is no
	// global enable on this platform, only the trusted-port mask.
	var name string
	for _, name = range []string{"poe", "port_security"} {
		if WritesExtra(name) {
			t.Errorf("WritesExtra(%q) = true without apply code", name)
		}
	}
}
