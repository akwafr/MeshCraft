package s3900

// WHICH SVI CARRIES ADMINISTRATION — the read that was decided by the ORDER of
// the running configuration until 2026-10-25.
//
// The defect was MEASURED on an Arista core (core01-arena, DCS-7050SX-64-R, EOS
// 4.28.13M) whose parser had the line-for-line same shape as this one: six
// addressed SVIs, printed Vlan42, Vlan97, Vlan98, Vlan99, Vlan100, Vlan3000, and
// the last of them a /31 point-to-point transit link to a MikroTik. The parser
// assigned the management fields on every one in turn, so Vlan3000 won by being
// last and meshcraft filed 10.255.255.1/31 as the switch's management address —
// while the address it had itself dialed to read all of that was 192.0.2.1/16,
// on Vlan99. This connector carried the identical "last block wins" assignment
// and would have produced the identical answer on an FSOS switch shaped that
// way.
//
// Cf. resolveMgmtSVI for the rule that replaced it, and for the ONE guardrail
// arista/eos has that this file does not pin: there is no "shutdown" filter
// here, because the word FSOS 1.7 prints inside a disabled "interface vlan N"
// block has never been captured. TestAShutSVIIsStillAContenderForWantOfACapture
// pins the GAP rather than pretending otherwise.

import (
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// transitLast is an FSOS 1.7 running configuration shaped the way the Arista
// core was: several addressed SVIs in ascending order — which is the order FSOS
// prints them — with a point-to-point transit link LAST. A fixture whose transit
// link came first would pass against the bug, which is why the order is the
// whole point of it.
//
// It mixes the two spellings this CLI uses on purpose: "ip add A.B.C.D M.M.M.M"
// on the lab switch's own SVIs, "ip add A.B.C.D/nn" on the ones written by
// hand. Both must normalise before the comparison can read either.
const transitLast = `!
hostname agg01-fs
!
vlan database
 VLAN 1 name DefaultVlan media ethernet
 VLAN 42 name Voice media ethernet
 VLAN 99 name LAN99_Management media ethernet
 VLAN 100 name Cameras media ethernet
 VLAN 3000 name Transit media ethernet
!
interface ethernet 1/1
 switchport mode trunk
 switchport trunk allowed vlan add 42,99,100
!
interface vlan 42
 ip add 10.42.0.1 255.255.255.0
!
interface vlan 99
 ip add 192.0.2.240 255.255.0.0
!
interface vlan 100
 ip add 10.100.0.1 255.255.255.0
!
interface vlan 3000
 ip add 10.255.255.1/31
!
ip route 0.0.0.0 0.0.0.0 192.0.2.1
!
end
`

// THE REGRESSION. The management SVI is the one the session is riding, and the
// transit /31 that happens to be printed last is not it.
//
// Proven to fail against the old shape: restoring "last addressed SVI wins"
// in the parse drops FOUR assertions of this function — MgmtVLAN (3000 instead
// of 99), the explicit anti-3000 check, Mgmt.Address (10.255.255.1/31) and
// Mgmt.Interface (vlan3000).
func TestManagementSVIIsTheOneTheSessionRidesNotTheLastBlock(t *testing.T) {
	var rc = parseRunningConfig(transitLast, "192.0.2.240")

	if rc.MgmtVLAN != 99 {
		t.Errorf("MgmtVLAN = %d, want 99 — 192.0.2.240 is the address meshcraft reaches "+
			"this switch at, and vlan 99 is the SVI that carries it", rc.MgmtVLAN)
	}
	if rc.MgmtVLAN == 3000 {
		t.Error("the LAST SVI of the configuration won: that is the bug, and vlan 3000 is " +
			"a /31 point-to-point transit link, not a management path")
	}
	if rc.Mgmt.Address != "192.0.2.240/16" {
		t.Errorf("address = %q, want 192.0.2.240/16 — and the dotted mask must have "+
			"normalised before the comparison could read it", rc.Mgmt.Address)
	}
	if rc.Mgmt.Interface != "vlan99" {
		t.Errorf("interface = %q, want vlan99", rc.Mgmt.Interface)
	}
	if rc.Mgmt.Mode != driver.MgmtAddrModeStatic {
		t.Errorf("mode = %q, want %q", rc.Mgmt.Mode, driver.MgmtAddrModeStatic)
	}
	// An unambiguous read says nothing: a sentence on every healthy switch would
	// bury the one that matters.
	if len(rc.MgmtSVINotes) != 0 {
		t.Errorf("an SVI matched the dialed address exactly and still produced notes: %v",
			rc.MgmtSVINotes)
	}

	// Reached on the transit link itself — a technician on the other end of it —
	// and that IS where administration arrives for this session. The rule is a
	// comparison, not a preference for low VLAN numbers.
	rc = parseRunningConfig(transitLast, "10.255.255.1")
	if rc.MgmtVLAN != 3000 {
		t.Errorf("MgmtVLAN = %d, want 3000: this session really did arrive there", rc.MgmtVLAN)
	}
}

// THE TEST THAT THREATENED TO REGRESS. The lab switch carries vlan 1 on DHCP and
// vlan 99 static, and no caller of the parser in a test knows a dialed address —
// so with EOS's four levels verbatim this read would fall through to "several
// candidates, nothing to choose between them" and report 0.
//
// It does not, because "a static SVI beats a DHCP one" is a RULE of the
// resolver and not a consequence of vlan 1 printing before vlan 99. Cf. the
// narrowing in resolveMgmtSVI.
func TestAStaticSVIBeatsADHCPOneWhateverTheOrderAndWithNoDialedAddress(t *testing.T) {
	var rc = parseRunningConfig(labRun, "")
	if rc.MgmtVLAN != 99 || rc.Mgmt.Mode != driver.MgmtAddrModeStatic {
		t.Fatalf("mgmt = %d %q, want 99 static: the static SVI is the one meshcraft wrote, "+
			"\"ip add dhcp\" on vlan 1 is what the switch left the factory with",
			rc.MgmtVLAN, rc.Mgmt.Mode)
	}
	if len(rc.MgmtSVINotes) != 0 {
		t.Errorf("the ordinary provisioned shape of this platform is not news: %v",
			rc.MgmtSVINotes)
	}

	// AND IT MUST NOT DEPEND ON THE ORDER, which is the entire reason it is a rule
	// here rather than an "if MgmtVLAN == 0" in the DHCP branch: the same two
	// blocks the other way round.
	var reversed = "!\ninterface vlan 99\n ip add 192.0.2.240 255.255.0.0\n!\n" +
		"interface vlan 1\n ip add dhcp\n!\n"
	rc = parseRunningConfig(reversed, "")
	if rc.MgmtVLAN != 99 {
		t.Errorf("MgmtVLAN = %d, want 99 — swapping the two blocks must change nothing",
			rc.MgmtVLAN)
	}

	// A switch whose every SVI is on DHCP is read as what it is: the narrowing
	// fires only when a static exists to narrow TO.
	rc = parseRunningConfig("!\ninterface vlan 1\n ip add dhcp\n!\n", "")
	if rc.MgmtVLAN != 1 || rc.Mgmt.Mode != driver.MgmtAddrModeDHCP {
		t.Errorf("mgmt = %d %q, want 1 dhcp: an unprovisioned switch addressed by lease "+
			"must not read as having no management path", rc.MgmtVLAN, rc.Mgmt.Mode)
	}
	if rc.Mgmt.Address != "" {
		t.Errorf("address = %q: the configuration carries the intent, never the lease",
			rc.Mgmt.Address)
	}

	// Two statics and a DHCP: the narrowing leaves two, and two is still
	// ambiguous. It is a tie-break between modes, never a tie-break between
	// statics.
	rc = parseRunningConfig("!\ninterface vlan 1\n ip add dhcp\n!\n"+
		"interface vlan 99\n ip add 192.0.2.240 255.255.0.0\n!\n"+
		"interface vlan 3000\n ip add 10.255.255.1/31\n!\n", "")
	if rc.MgmtVLAN != 0 {
		t.Errorf("MgmtVLAN = %d, want 0: two static SVIs and no dialed address is exactly "+
			"the ambiguity this resolver refuses to invent its way out of", rc.MgmtVLAN)
	}
}

// NO MATCH IS NOT A LICENCE TO GUESS. Falling back to the last SVI is the bug;
// falling back to the first is the same bug read backwards. Zero is what both
// readers of MgmtVLAN already treat as "not known", and they fail safe on it.
func TestManagementSVIIsUnknownWhenNoneCarriesTheDialedAddress(t *testing.T) {
	// 198.51.100.7 is in none of the fixture's subnets: the session arrived through
	// something this configuration does not describe.
	var rc = parseRunningConfig(transitLast, "198.51.100.7")

	if rc.MgmtVLAN != 0 {
		t.Errorf("MgmtVLAN = %d, want 0: four SVIs carry an address, none carries the one "+
			"meshcraft dialed, and picking any of them would be an invention", rc.MgmtVLAN)
	}
	if rc.Mgmt.Address != "" || rc.Mgmt.Interface != "" || rc.Mgmt.Mode != "" {
		t.Errorf("mgmt = %+v, want empty — an unknown management SVI has no address either",
			rc.Mgmt)
	}

	// The operator must be TOLD, and told what was seen — otherwise "management
	// VLAN unknown" on a switch with four SVIs reads as a failed read.
	var said = strings.Join(rc.MgmtSVINotes, " ")
	if !strings.Contains(said, "NOT DETERMINED") {
		t.Errorf("the notes do not say the VLAN was not determined: %q", said)
	}
	if !strings.Contains(said, "vlan 99") || !strings.Contains(said, "vlan 3000") {
		t.Errorf("the notes must name the candidates so an operator can settle it: %q", said)
	}
	// Carried on all three screens, which do not show each other.
	if !strings.Contains(strings.Join(rc.Mgmt.Notes, " "), "NOT DETERMINED") {
		t.Error("Mgmt.Notes does not carry the sentence: ReadMgmtAddressing shows those " +
			"and nothing else")
	}

	// Zero must not be mistaken for a VLAN by the guardrail that consumes it.
	if carriesVLAN(rc.Ports["GE1"], rc.MgmtVLAN) {
		t.Error("VLAN 0 is not a VLAN: the membership test must not invent a match")
	}
	var block, why = portAdminGuardBlocks(rc.Ports["GE1"], rc.MgmtVLAN, true, false)
	if !block {
		t.Errorf("an unknown management VLAN must still spare a port with a LINK: %q", why)
	}
}

// A VIRTUAL ADDRESS IN FRONT OF THE SVI. The address meshcraft dials in front of
// a redundant pair is routinely the virtual one, which no SVI carries — but
// exactly one subnet contains it, and that is a comparison, not a guess. Most
// specific first, the way a routing table answers the same question.
func TestManagementSVIFallsBackToTheSubnetThatContainsTheDialedAddress(t *testing.T) {
	// The switch holds 192.0.2.240; 192.0.2.1 is the virtual address. Note it is
	// also inside vlan 99's /16 and inside nothing else — the /24s and the /31
	// exclude it.
	var rc = parseRunningConfig(transitLast, "192.0.2.1")

	if rc.MgmtVLAN != 99 {
		t.Errorf("MgmtVLAN = %d, want 99: no SVI carries 192.0.2.1, and vlan 99's /16 is "+
			"the only subnet that contains it", rc.MgmtVLAN)
	}
	if rc.Mgmt.Address != "192.0.2.240/16" {
		t.Errorf("address = %q, want 192.0.2.240/16 — what the SVI carries is reported, "+
			"never the virtual address meshcraft dialed", rc.Mgmt.Address)
	}
	var said = strings.Join(rc.MgmtSVINotes, " ")
	if !strings.Contains(said, "VRRP") || !strings.Contains(said, "192.0.2.1") {
		t.Errorf("a match by SUBNET rather than by address must say so, and name the "+
			"dialed address: %q", said)
	}

	// LONGEST PREFIX, and not "the first subnet that contains it": a /24 carved
	// out of the same /16 is the more specific answer, and it is printed after.
	var carved = parseRunningConfig(transitLast+
		"!\ninterface vlan 200\n ip add 192.0.2.2 255.255.255.0\n!\n", "192.0.2.1")
	if carved.MgmtVLAN != 200 {
		t.Errorf("MgmtVLAN = %d, want 200: 192.0.2.0/24 is more specific than 192.0.0.0/16, "+
			"and a longest-prefix match that depended on print order would answer 99",
			carved.MgmtVLAN)
	}
}

// THE FLEET'S ORDINARY SHAPE, unchanged: one addressed SVI and no ambiguity to
// resolve, so it is the management one whether or not the caller knows which
// address it dialed. Said out loud when the dialed address is NOT the one it
// carries — administration is then arriving by another door and the VLAN is a
// deduction.
func TestManagementSVIKeepsTheOnlyCandidateThereIs(t *testing.T) {
	var one = "!\ninterface vlan 99\n ip add 192.0.2.240 255.255.0.0\n!\n"

	var rc = parseRunningConfig(one, "")
	if rc.MgmtVLAN != 99 || rc.Mgmt.Address != "192.0.2.240/16" {
		t.Errorf("mgmt = %d %q: with one candidate there is nothing to disambiguate, and a "+
			"caller that does not know its own dialed address must not lose the read",
			rc.MgmtVLAN, rc.Mgmt.Address)
	}
	if len(rc.MgmtSVINotes) != 0 {
		t.Errorf("one candidate and no dialed address is not news: %v", rc.MgmtSVINotes)
	}

	// Reached at an address this SVI does not carry — the craft port, a console,
	// an address translation.
	rc = parseRunningConfig(one+"interface craft\n ip add 192.168.1.1 255.255.255.0\n!\n",
		"192.168.1.1")
	if rc.MgmtVLAN != 99 {
		t.Errorf("MgmtVLAN = %d, want 99 — still the only SVI that could carry "+
			"administration", rc.MgmtVLAN)
	}
	if !strings.Contains(strings.Join(rc.MgmtSVINotes, " "), "deduction") {
		t.Errorf("the SVI is reported on no evidence at all and the note must say so: %v",
			rc.MgmtSVINotes)
	}
}

// THE CRAFT PORT IS NOT A MANAGEMENT CANDIDATE, and the lab capture is the proof
// that this matters: labRun's "interface craft" carries 192.168.1.1/24, which
// is an address, on a block this parser must never offer to the resolver. It
// sits outside the switching fabric and this connector neither reads nor writes
// administration through it.
//
// It is kept for one purpose only — telling an operator what the switch DOES
// have when no SVI could be named.
func TestTheCraftPortIsRecordedButNeverACandidate(t *testing.T) {
	var rc = parseRunningConfig(labRun, "")
	if rc.MgmtOOB != "192.168.1.1/24" {
		t.Errorf("MgmtOOB = %q, want 192.168.1.1/24 off \"interface craft\"", rc.MgmtOOB)
	}
	if rc.MgmtVLAN != 99 {
		t.Errorf("MgmtVLAN = %d: the craft block must not have entered the resolution",
			rc.MgmtVLAN)
	}

	// Dialed AT the craft address, with two SVIs so no "only candidate" rescue:
	// the craft port is still not a candidate and the answer is still unknown.
	var twoSVIs = parseRunningConfig("!\ninterface vlan 99\n ip add 192.0.2.240 255.255.0.0\n!\n"+
		"interface vlan 3000\n ip add 10.255.255.1/31\n!\n"+
		"interface craft\n ip add 192.168.1.1 255.255.255.0\n!\n", "192.168.1.1")
	if twoSVIs.MgmtVLAN != 0 {
		t.Errorf("MgmtVLAN = %d, want 0: arriving on the out-of-band port says nothing "+
			"about which SVI carries in-band administration", twoSVIs.MgmtVLAN)
	}
	if twoSVIs.MgmtOOB != "192.168.1.1/24" {
		t.Errorf("MgmtOOB = %q: the sentence that explains the 0 needs it", twoSVIs.MgmtOOB)
	}
}

// A DUPLICATE ADDRESS cannot be, and a configuration that holds one must still
// read the same twice. The lowest VLAN is taken, and the contradiction is named.
func TestADuplicatedDialedAddressResolvesStablyAndSaysSo(t *testing.T) {
	var dup = "!\ninterface vlan 3000\n ip add 192.0.2.240 255.255.0.0\n!\n" +
		"interface vlan 99\n ip add 192.0.2.240 255.255.0.0\n!\n"
	var rc = parseRunningConfig(dup, "192.0.2.240")
	if rc.MgmtVLAN != 99 {
		t.Errorf("MgmtVLAN = %d, want 99 — the lowest, so two reads of this device agree "+
			"with each other rather than with the print order", rc.MgmtVLAN)
	}
	if !strings.Contains(strings.Join(rc.MgmtSVINotes, " "), "cannot be") {
		t.Errorf("a duplicated address must be reported, not quietly resolved: %v",
			rc.MgmtSVINotes)
	}
}

// THE KNOWN GAP, PINNED AS A GAP. arista/eos disqualifies a shut SVI whatever
// the position of its "shutdown" line; this connector cannot, because no
// capture in this repository shows an FSOS 1.7 "interface vlan N" block with
// the interface disabled, so the word it prints is unknown.
//
// This test asserts the CURRENT, WRONG behaviour on the only spelling one might
// guess at — deliberately. A regexp written on an unobserved form is worse than
// its absence: it looks like a guardrail and fails silently on the day the real
// word turns out to be another one. When the capture exists, the filter goes
// into the candidate loop at the end of parseRunningConfig and THIS TEST FLIPS:
// want 0, not 99. That is the signal that the gap closed.
func TestAShutSVIIsStillAContenderForWantOfACapture(t *testing.T) {
	var shut = "!\ninterface vlan 99\n ip add 192.0.2.240 255.255.0.0\n shutdown\n!\n"
	var rc = parseRunningConfig(shut, "")

	if rc.MgmtVLAN != 99 {
		t.Fatalf("MgmtVLAN = %d. If this is now 0, a \"shutdown\" filter was added: good — "+
			"update this test to pin the filter, and say which capture established the "+
			"spelling", rc.MgmtVLAN)
	}
	t.Log("KNOWN GAP: a disabled SVI carrying an address still claims administration on " +
		"this platform. Closing it needs a lab capture of \"show run\" with an SVI shut — " +
		"the FSOS 1.7 spelling has never been observed, and guessing it would put an " +
		"unverifiable regexp where a guardrail belongs. Cf. sviCandidate.")

	// What bounds the damage: at level 1 and level 2 the dialed address is the
	// address meshcraft is talking to RIGHT NOW, so the SVI carrying it is up by
	// observation whatever the configuration prints. The gap can only mislead
	// where there was no evidence anyway.
	var mixed = parseRunningConfig(shut+
		"interface vlan 100\n ip add 10.100.0.1 255.255.255.0\n!\n", "10.100.0.1")
	if mixed.MgmtVLAN != 100 {
		t.Errorf("MgmtVLAN = %d, want 100: the dialed address is evidence the shut-SVI gap "+
			"cannot override", mixed.MgmtVLAN)
	}
}
