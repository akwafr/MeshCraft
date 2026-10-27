package ios

// WHICH SVI CARRIES ADMINISTRATION — the read that was decided by the ORDER of
// the running configuration until 2026-10-25.
//
// The incident was measured on an Arista core (core01-arena, DCS-7050SX-64-R):
// six addressed SVIs, printed Vlan42, Vlan97, Vlan98, Vlan99, Vlan100, Vlan3000,
// the last of them a /31 point-to-point transit link. The parser assigned the
// management fields on every one of them in turn, so Vlan3000 won by being last
// and meshcraft filed 10.255.255.1/31 as the switch's management address — while
// the address it had itself dialed to read all of that was 192.0.2.1/16, on
// Vlan99.
//
// This connector carried the same shape, line for line, and two aggravations of
// its own:
//
//   - the "shutdown" guard never fired. It was a single boolean set only when
//     the line was seen BEFORE "ip address", and IOS prints "shutdown" near
//     the end of the block, after the address. An SVI both addressed and
//     administratively down was taken for the management path every time;
//   - the management SVI does not merely feed a read here. ReadMgmtAddressing
//     hands Mgmt.Interface to ApplyMgmtAddressing, which re-addresses that
//     interface by name — so the wrong SVI aims a WRITE at a transit link.
//
// Cf. resolveMgmtSVI for the rule that replaced "the last block wins", and for
// what each of these tests is holding it to.

import (
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// distShape is a distribution switch in the shape IOS prints: the physical
// interfaces first, then the SVIs, and a transit /31 LAST — which is the whole
// point. A fixture whose transit link came first would pass against the bug.
//
// The SVI set is core01-arena's, transposed to IOS's dotted-mask syntax, because
// that is the configuration the defect was measured on. Only one physical block
// is kept: enough for ApplyPortAdmin's membership table to exist (cf.
// portadmin.go — zero blocks is read as a truncated capture), and nothing here
// reads the ports.
const distShape = `version 15.2
hostname dist01-lab
!
interface GigabitEthernet1/0/1
 switchport mode trunk
!
interface Vlan42
 ip address 10.42.0.1 255.255.255.0
!
interface Vlan97
 ip address 10.97.0.1 255.255.255.0
!
interface Vlan98
 ip address 10.98.0.1 255.255.255.0
!
interface Vlan99
 ip address 192.0.2.1 255.255.0.0
!
interface Vlan100
 ip address 10.100.0.1 255.255.255.0
!
interface Vlan3000
 ip address 10.255.255.1 255.255.255.254
!
end
`

// THE REGRESSION. The management SVI is the one the session is riding, and the
// transit /31 that happens to be printed last is not it.
func TestManagementSVIIsTheOneTheSessionRidesNotTheLastBlock(t *testing.T) {
	var rc = parseRunningConfig(distShape, "192.0.2.1")

	if rc.MgmtVLAN != 99 {
		t.Errorf("MgmtVLAN = %d, want 99 — 192.0.2.1 is the address meshcraft reaches "+
			"this switch at, and Vlan99 is the SVI that carries it", rc.MgmtVLAN)
	}
	if rc.MgmtVLAN == 3000 {
		t.Error("the LAST SVI of the configuration won: that is the bug, and Vlan3000 is a " +
			"/31 point-to-point transit link, not a management path")
	}
	if rc.Mgmt.Address != "192.0.2.1/16" {
		t.Errorf("address = %q, want 192.0.2.1/16", rc.Mgmt.Address)
	}
	// LOWER CASE, because ApplyMgmtAddressing sends "interface" + this string.
	// A capital here would change the command on the wire.
	if rc.Mgmt.Interface != "vlan99" {
		t.Errorf("interface = %q, want vlan99 — and lower-case, because "+
			"ApplyMgmtAddressing concatenates it into the command it sends",
			rc.Mgmt.Interface)
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
}

// THE WRITE THE OLD READ WOULD HAVE AIMED AT A TRANSIT LINK. This is what EOS
// did not have at stake: ReadMgmtAddressing's answer IS the interface
// ApplyMgmtAddressing reconfigures, by name. Pinned on the composition rather
// than on a transcript so it holds whatever the command renderer becomes.
func TestManagementSVIIsTheInterfaceAReAddressingWouldWriteTo(t *testing.T) {
	var rc = parseRunningConfig(distShape, "192.0.2.1")
	var sent = "interface " + strings.TrimSpace(rc.Mgmt.Interface)
	if sent != "interface vlan99" {
		t.Fatalf("ApplyMgmtAddressing would send %q — a re-addressing gesture aimed at "+
			"anything but Vlan99 lands on an interface the operator never named, and "+
			"\"interface vlan3000\" lands on a point-to-point transit link", sent)
	}
}

// NO MATCH IS NOT A LICENCE TO GUESS. Falling back to the last SVI is the bug;
// falling back to the first is the same bug read backwards. Zero is what every
// reader of MgmtVLAN already treats as "not known", and it fails safe there.
func TestManagementSVIIsUnknownWhenNoneCarriesTheDialedAddress(t *testing.T) {
	// 198.51.100.7 is in none of the fixture's subnets: the session arrived through
	// something this configuration does not describe.
	var rc = parseRunningConfig(distShape, "198.51.100.7")

	if rc.MgmtVLAN != 0 {
		t.Errorf("MgmtVLAN = %d, want 0: six SVIs carry an address, none carries the one "+
			"meshcraft dialed, and picking any of them would be an invention", rc.MgmtVLAN)
	}
	if rc.Mgmt.Address != "" || rc.Mgmt.Interface != "" || rc.Mgmt.Mode != "" {
		t.Errorf("mgmt = %+v, want empty — an unknown management SVI has no address "+
			"either, and ApplyMgmtAddressing must have no interface to aim at", rc.Mgmt)
	}

	// The operator must be TOLD, and told what was seen — otherwise "management
	// VLAN unknown" on a switch with six SVIs reads as a failed read.
	var said = strings.Join(rc.MgmtSVINotes, " ")
	if !strings.Contains(said, "NOT DETERMINED") {
		t.Errorf("the notes do not say the VLAN was not determined: %q", said)
	}
	if !strings.Contains(said, "Vlan99") || !strings.Contains(said, "Vlan3000") {
		t.Errorf("the notes must name the candidates so an operator can settle it: %q", said)
	}
	// Twice over, because the two screens are different: the state read, and
	// ReadMgmtAddressing.
	if !strings.Contains(strings.Join(rc.Mgmt.Notes, " "), "NOT DETERMINED") {
		t.Error("Mgmt.Notes does not carry the sentence: ReadMgmtAddressing shows those " +
			"and nothing else")
	}

	// Zero must not be mistaken for a VLAN by the guardrail that consumes it.
	if carriesMgmtVLAN("GE1", rc, rc.MgmtVLAN) {
		t.Error("VLAN 0 is not a VLAN: the membership test must not invent a match")
	}
}

// ZERO REACHES THE SCREEN, not just the struct. ReadState is the only path that
// carries MgmtSVINotes to an operator, and a read that silently drops them turns
// "management VLAN unknown" into an unexplained blank on a switch that has six
// perfectly good SVIs.
func TestManagementSVINotesReachTheStateRead(t *testing.T) {
	var c, _ = newPortAdminFakeClient(distShape, labStatus)
	// The fake client dials 192.0.2.240, which this fixture's Vlan99 does not
	// carry — but 192.0.0.0/16 contains it, so the subnet test answers and the
	// note says so. Exactly the HSRP-shaped read, and exactly when an operator
	// needs to be told how the choice was made.
	var st, err = c.ReadState(t.Context())
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if st.MgmtVLAN != 99 {
		t.Fatalf("MgmtVLAN = %d, want 99", st.MgmtVLAN)
	}
	if !strings.Contains(strings.Join(st.Notes, " "), "HSRP") {
		t.Errorf("the state read must carry the sentence explaining how the management SVI "+
			"was chosen: %v", st.Notes)
	}
}

// AN HSRP VIRTUAL ADDRESS IN FRONT OF THE SVI. On a distribution pair the
// address meshcraft dials is routinely the HSRP one, which no SVI carries — and
// this connector reads no "standby" line anywhere, so there is nothing else
// that could resolve it. Exactly one subnet contains it, which is a comparison
// and not a guess. Most specific first, the way a routing table answers the same
// question.
func TestManagementSVIFallsBackToTheSubnetThatContainsTheDialedAddress(t *testing.T) {
	// The switch holds 192.0.2.2; 192.0.2.1 is the HSRP virtual address.
	var shape = strings.Replace(distShape,
		"ip address 192.0.2.1 255.255.0.0", "ip address 192.0.2.2 255.255.0.0", 1)
	if strings.Contains(shape, "192.0.2.1 255.255.0.0") {
		t.Fatal("the fixture edit did not apply: this test would prove the opposite of itself")
	}
	var rc = parseRunningConfig(shape, "192.0.2.1")

	if rc.MgmtVLAN != 99 {
		t.Errorf("MgmtVLAN = %d, want 99: no SVI carries 192.0.2.1, and Vlan99's /16 is the "+
			"only subnet that contains it", rc.MgmtVLAN)
	}
	if rc.Mgmt.Address != "192.0.2.2/16" {
		t.Errorf("address = %q, want 192.0.2.2/16 — what the SVI carries is reported, never "+
			"the virtual address meshcraft dialed", rc.Mgmt.Address)
	}
	var said = strings.Join(rc.MgmtSVINotes, " ")
	if !strings.Contains(said, "HSRP") || !strings.Contains(said, "192.0.2.1") {
		t.Errorf("a match by SUBNET rather than by address must say so, name HSRP — the "+
			"ordinary reason on IOS — and name the dialed address: %q", said)
	}
}

// LONGEST PREFIX, not first match. Two subnets both contain the dialed address;
// the specific one is the SVI administration is on, and the supernet is the
// summary route's interface.
func TestManagementSVIPrefersTheLongestPrefixThatContainsTheDialedAddress(t *testing.T) {
	var shape = "interface Vlan10\n ip address 10.0.0.1 255.0.0.0\n!\n" +
		"interface Vlan99\n ip address 192.0.2.2 255.255.0.0\n!\nend\n"
	var rc = parseRunningConfig(shape, "192.0.2.1")
	if rc.MgmtVLAN != 99 {
		t.Errorf("MgmtVLAN = %d, want 99: 10.0.0.0/8 contains 192.0.2.1 too, and the /16 "+
			"is the more specific answer — the way a routing table resolves it", rc.MgmtVLAN)
	}
}

// SECONDARY PREFIXES. IOS prints the primary address first and every
// "secondary" after it, in one block — and reRunIPAddr is not anchored at the
// end of the line, so "ip address A M secondary" matched and overwrote the
// primary. The address reported is the one administration actually rides, and
// the PRIMARY when nothing singles one out.
func TestManagementSVIReportsTheAddressAdministrationRidesNotTheLastSecondary(t *testing.T) {
	var twoPrefixes = `interface Vlan99
 ip address 192.0.2.1 255.255.0.0
 ip address 172.16.0.1 255.255.255.0 secondary
!
end
`
	var rc = parseRunningConfig(twoPrefixes, "192.0.2.1")
	if rc.MgmtVLAN != 99 || rc.Mgmt.Address != "192.0.2.1/16" {
		t.Errorf("mgmt = %d %q, want 99 192.0.2.1/16 — the secondary prefix printed after "+
			"the primary must not overwrite it", rc.MgmtVLAN, rc.Mgmt.Address)
	}

	// Reached ON the secondary, the secondary is what administration rides and
	// what the operator must be shown.
	rc = parseRunningConfig(twoPrefixes, "172.16.0.1")
	if rc.Mgmt.Address != "172.16.0.1/24" {
		t.Errorf("address = %q, want 172.16.0.1/24: that is the address this session "+
			"arrived on", rc.Mgmt.Address)
	}

	// With nothing to compare, the PRIMARY — the first line of the block.
	rc = parseRunningConfig(twoPrefixes, "")
	if rc.Mgmt.Address != "192.0.2.1/16" {
		t.Errorf("address = %q, want the primary 192.0.2.1/16", rc.Mgmt.Address)
	}
}

// THE FLEET'S ORDINARY SHAPE, unchanged: one addressed SVI and no ambiguity to
// resolve, so it is the management one whether or not the caller knows which
// address it dialed. Said out loud when the dialed address is NOT the one it
// carries — administration is then arriving by another door and the VLAN is a
// deduction. This connector models no out-of-band port at all (FastEthernet0 is
// refused by StandardPortName), so that door is never described here.
func TestManagementSVIKeepsTheOnlyCandidateThereIs(t *testing.T) {
	var one = "interface Vlan99\n ip address 192.0.2.5 255.255.255.0\n!\nend\n"

	var rc = parseRunningConfig(one, "")
	if rc.MgmtVLAN != 99 || rc.Mgmt.Address != "192.0.2.5/24" {
		t.Errorf("mgmt = %d %q: with one candidate there is nothing to disambiguate, and a "+
			"caller that does not know its own dialed address must not lose the read",
			rc.MgmtVLAN, rc.Mgmt.Address)
	}
	if len(rc.MgmtSVINotes) != 0 {
		t.Errorf("one candidate and no dialed address is not news: %v", rc.MgmtSVINotes)
	}

	// Reached at an address this SVI neither carries nor contains — a console
	// server, an address translation.
	rc = parseRunningConfig(one, "192.168.1.10")
	if rc.MgmtVLAN != 99 {
		t.Errorf("MgmtVLAN = %d, want 99 — still the only SVI that could carry "+
			"administration", rc.MgmtVLAN)
	}
	if !strings.Contains(strings.Join(rc.MgmtSVINotes, " "), "deduction") {
		t.Errorf("the SVI is reported on no evidence at all and the note must say so: %v",
			rc.MgmtSVINotes)
	}
}

// A DHCP SVI has no address in the running configuration — the intent is there,
// the lease is not — so it can never win by comparison. It still wins as the
// only candidate, which is how a DHCP-addressed access switch reads.
func TestManagementSVIUnderDHCP(t *testing.T) {
	var rc = parseRunningConfig("interface Vlan99\n ip address dhcp\n!\nend\n", "192.0.2.5")
	if rc.MgmtVLAN != 99 || rc.Mgmt.Mode != driver.MgmtAddrModeDHCP {
		t.Errorf("mgmt = %d %q, want 99 dhcp", rc.MgmtVLAN, rc.Mgmt.Mode)
	}
	if rc.Mgmt.Address != "" {
		t.Errorf("address = %q: the configuration carries the intent, never the lease",
			rc.Mgmt.Address)
	}

	// Beside a static SVI that DOES carry the dialed address, the comparison
	// decides and the DHCP block is not a contender.
	rc = parseRunningConfig("interface Vlan99\n ip address dhcp\n!\n"+
		"interface Vlan3000\n ip address 10.255.255.1 255.255.255.254\n!\nend\n",
		"10.255.255.1")
	if rc.MgmtVLAN != 3000 || rc.Mgmt.Mode != driver.MgmtAddrModeStatic {
		t.Errorf("mgmt = %d %q, want 3000 static: 10.255.255.1 is where this session "+
			"arrived, whatever the other block intends", rc.MgmtVLAN, rc.Mgmt.Mode)
	}
}

// "shutdown" AFTER the address, WHICH IS WHERE IOS PRINTS IT. This is the
// aggravation this connector had over EOS: the guard was a single boolean set
// only when the line was seen first, so on real IOS output it was still false
// when the address went past and never disqualified anything. The two lines
// describe the same dead interface in either order.
func TestManagementSVIIgnoresAShutSVIWhateverTheLineOrder(t *testing.T) {
	var after = parseRunningConfig(
		"interface Vlan99\n ip address 192.0.2.1 255.255.0.0\n shutdown\n!\nend\n",
		"192.0.2.1")
	if after.MgmtVLAN != 0 {
		t.Errorf("MgmtVLAN = %d, want 0: the SVI is shut down, its address answers nothing, "+
			"and IOS prints \"shutdown\" AFTER \"ip address\" — a guard that only looked "+
			"backwards never fired at all", after.MgmtVLAN)
	}
	if after.Mgmt.Interface != "" {
		t.Errorf("interface = %q: ApplyMgmtAddressing would re-address an interface that is "+
			"administratively down", after.Mgmt.Interface)
	}

	// And in the other order, which EOS prints and IOS does not: same verdict.
	var before = parseRunningConfig(
		"interface Vlan99\n shutdown\n ip address 192.0.2.1 255.255.0.0\n!\nend\n",
		"192.0.2.1")
	if before.MgmtVLAN != 0 {
		t.Errorf("MgmtVLAN = %d, want 0 whichever line comes first", before.MgmtVLAN)
	}

	// The live SVI beside a shut one that would otherwise have matched exactly.
	var mixed = parseRunningConfig(
		"interface Vlan99\n ip address 192.0.2.1 255.255.0.0\n shutdown\n!\n"+
			"interface Vlan100\n ip address 10.100.0.1 255.255.255.0\n!\nend\n",
		"192.0.2.1")
	if mixed.MgmtVLAN != 100 {
		t.Errorf("MgmtVLAN = %d, want 100 — the only SVI left standing", mixed.MgmtVLAN)
	}
}

// AN UNREADABLE MASK still compares for EQUALITY, which is the test that
// matters; only containment is lost. The address is reported as the device
// stated it, with the note saying the prefix was not assumed.
func TestManagementSVISurvivesAnUnreadableMask(t *testing.T) {
	var rc = parseRunningConfig(
		"interface Vlan42\n ip address 10.42.0.1 255.255.255.0\n!\n"+
			"interface Vlan99\n ip address 192.0.2.1 0.0.0.0\n!\nend\n", "192.0.2.1")
	if rc.MgmtVLAN != 99 {
		t.Fatalf("MgmtVLAN = %d, want 99: the prefix is unreadable, the ADDRESS is not, "+
			"and equality is what level 1 compares", rc.MgmtVLAN)
	}
	if rc.Mgmt.Address != "192.0.2.1" {
		t.Errorf("address = %q, want the bare 192.0.2.1 — never a prefix this parser "+
			"invented", rc.Mgmt.Address)
	}
	if !strings.Contains(strings.Join(rc.Mgmt.Notes, " "), "unreadable network mask") {
		t.Errorf("the winning SVI's own note must travel with it: %v", rc.Mgmt.Notes)
	}
}
