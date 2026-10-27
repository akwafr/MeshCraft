package eos

// WHICH SVI CARRIES ADMINISTRATION — the read that was decided by the ORDER of
// the running configuration until 2026-10-24.
//
// Measured on core01-arena (DCS-7050SX-64-R, EOS 4.28.13M): six addressed SVIs,
// printed Vlan42, Vlan97, Vlan98, Vlan99, Vlan100, Vlan3000, and the last of
// them a /31 point-to-point transit link to a MikroTik. The parser assigned the
// management fields on every one of them in turn, so Vlan3000 won by being last
// and meshcraft filed 10.255.255.1/31 as the switch's management address — while
// the address it had itself dialed to read all of that was 192.0.2.1/16, on
// Vlan99.
//
// Cf. resolveMgmtSVI for the rule that replaced "the last block wins", and for
// what each of these tests is holding it to.

import (
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// arenaShape is core01-arena's SVI set, in the ORDER THE DEVICE PRINTS IT, which
// is the whole point: a fixture whose transit link came first would pass against
// the bug. Addresses and prefix lengths are the measured ones; the port blocks
// are left out because nothing here reads them.
const arenaShape = `hostname core01-arena
!
vrf instance VRF-LAN
!
interface Vlan42
   vrf VRF-LAN
   ip address 10.42.0.1/24
!
interface Vlan97
   vrf VRF-LAN
   ip address 10.97.0.1/24
!
interface Vlan98
   vrf VRF-LAN
   ip address 10.98.0.1/24
!
interface Vlan99
   vrf VRF-LAN
   ip address 192.0.2.1/16
!
interface Vlan100
   vrf VRF-LAN
   ip address 10.100.0.1/24
!
interface Vlan3000
   vrf VRF-LAN
   ip address 10.255.255.1/31
!
end
`

// THE REGRESSION. The management SVI is the one the session is riding, and the
// transit /31 that happens to be printed last is not it.
func TestManagementSVIIsTheOneTheSessionRidesNotTheLastBlock(t *testing.T) {
	var rc = parseRunningConfig(arenaShape, "192.0.2.1")

	if rc.MgmtVLAN != 99 {
		t.Errorf("MgmtVLAN = %d, want 99 — 192.0.2.1 is the address meshcraft reaches "+
			"this switch at, and Vlan99 is the SVI that carries it", rc.MgmtVLAN)
	}
	if rc.MgmtVLAN == 3000 {
		t.Error("the LAST SVI of the configuration won: that is the bug, and Vlan3000 is a " +
			"/31 point-to-point transit link to a MikroTik, not a management path")
	}
	if rc.Mgmt.Address != "192.0.2.1/16" {
		t.Errorf("address = %q, want 192.0.2.1/16", rc.Mgmt.Address)
	}
	if rc.Mgmt.Interface != "Vlan99" {
		t.Errorf("interface = %q, want Vlan99", rc.Mgmt.Interface)
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

// THE GUARDRAIL THAT WAS RIGHT BY LUCK. gnmiVRFFact compares the gNMI
// transport's VRF against the VRF OF THE MANAGEMENT SVI. On the real
// core01-arena every SVI sits in VRF-LAN, so the wrong SVI gave the right
// answer and nothing was noticed for months. Spread the same SVIs over two
// instances — the transit link in its own, which is how a transit link is
// normally kept — and the old read invents a disagreement that does not exist.
func TestManagementSVIDecidesTheGNMIVRFComparison(t *testing.T) {
	var shape = strings.Replace(arenaShape,
		"interface Vlan3000\n   vrf VRF-LAN",
		"interface Vlan3000\n   vrf VRF-TRANSIT", 1) +
		"management api gnmi\n   transport grpc default\n      vrf VRF-LAN\n!\nend\n"

	var rc = parseRunningConfig(shape, "192.0.2.1")
	if rc.MgmtVRF != "VRF-LAN" {
		t.Fatalf("MgmtVRF = %q, want VRF-LAN — the management SVI is Vlan99, and reading "+
			"the VRF of Vlan3000 instead is how this fact gets its premise wrong", rc.MgmtVRF)
	}
	var facts = gnmiVRFFact(rc)
	if len(facts) != 0 {
		t.Errorf("the transport and the management SVI are BOTH in VRF-LAN: this fact must "+
			"stay silent. It reported a disagreement that does not exist: %v", facts)
	}

	// And the other direction: the transport left behind in the instance the
	// transit SVI lives in is a REAL disagreement, which must still be named.
	var moved = strings.Replace(shape,
		"transport grpc default\n      vrf VRF-LAN",
		"transport grpc default\n      vrf VRF-TRANSIT", 1)
	rc = parseRunningConfig(moved, "192.0.2.1")
	facts = gnmiVRFFact(rc)
	if len(facts) != 1 {
		t.Fatalf("want one fact, got %v — a transport in VRF-TRANSIT while management is "+
			"in VRF-LAN binds successfully and serves nobody", facts)
	}
	if !strings.Contains(facts[0], "Vlan99") {
		t.Errorf("the fact must name the SVI the verdict is built on: %q", facts[0])
	}
}

// NO MATCH IS NOT A LICENCE TO GUESS. Falling back to the last SVI is the bug;
// falling back to the first is the same bug read backwards. Zero is what every
// reader of MgmtVLAN already treats as "not known", and it fails safe there.
func TestManagementSVIIsUnknownWhenNoneCarriesTheDialedAddress(t *testing.T) {
	// 198.51.100.7 is in none of the fixture's subnets: the session arrived through
	// something this configuration does not describe.
	var rc = parseRunningConfig(arenaShape, "198.51.100.7")

	if rc.MgmtVLAN != 0 {
		t.Errorf("MgmtVLAN = %d, want 0: six SVIs carry an address, none carries the one "+
			"meshcraft dialed, and picking any of them would be an invention", rc.MgmtVLAN)
	}
	if rc.Mgmt.Address != "" || rc.Mgmt.Interface != "" || rc.Mgmt.Mode != "" {
		t.Errorf("mgmt = %+v, want empty — an unknown management SVI has no address either",
			rc.Mgmt)
	}
	if rc.MgmtVRF != "" {
		t.Errorf("MgmtVRF = %q: with no management SVI there is no VRF to compare against",
			rc.MgmtVRF)
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
	if !strings.Contains(strings.Join(configFacts(rc), " "), "NOT DETERMINED") {
		t.Error("the state read does not carry the sentence: configFacts must pass " +
			"MgmtSVINotes through")
	}
	if !strings.Contains(strings.Join(rc.Mgmt.Notes, " "), "NOT DETERMINED") {
		t.Error("Mgmt.Notes does not carry the sentence: ReadMgmtAddressing shows those " +
			"and nothing else")
	}

	// Zero must not be mistaken for a VLAN by the guardrail that consumes it.
	if carriesMgmtVLAN("ET3", rc, rc.MgmtVLAN) {
		t.Error("VLAN 0 is not a VLAN: the membership test must not invent a match")
	}
}

// A VIRTUAL ADDRESS IN FRONT OF THE SVI. On a core pair the address meshcraft
// dials is routinely the VRRP one, which no SVI carries — but exactly one
// subnet contains it, and that is a comparison, not a guess. Most specific
// first, the way a routing table answers the same question.
func TestManagementSVIFallsBackToTheSubnetThatContainsTheDialedAddress(t *testing.T) {
	// The switch holds 192.0.2.2; 192.0.2.1 is the virtual address.
	var shape = strings.Replace(arenaShape, "ip address 192.0.2.1/16", "ip address 192.0.2.2/16", 1)
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
	if !strings.Contains(said, "VRRP") || !strings.Contains(said, "192.0.2.1") {
		t.Errorf("a match by SUBNET rather than by address must say so, and name the "+
			"dialed address: %q", said)
	}
}

// SECONDARY PREFIXES. EOS prints the primary address first and every
// "secondary" after it, in one block — so the within-block read had the same
// last-wins shape as the between-block one. The address reported is the one
// administration actually rides, and the PRIMARY when nothing singles one out.
func TestManagementSVIReportsTheAddressAdministrationRidesNotTheLastSecondary(t *testing.T) {
	var twoPrefixes = `interface Vlan99
   ip address 192.0.2.1/16
   ip address 172.16.0.1/24 secondary
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
// deduction.
func TestManagementSVIKeepsTheOnlyCandidateThereIs(t *testing.T) {
	var one = "interface Vlan99\n   ip address 192.0.2.5/24\n!\nend\n"

	var rc = parseRunningConfig(one, "")
	if rc.MgmtVLAN != 99 || rc.Mgmt.Address != "192.0.2.5/24" {
		t.Errorf("mgmt = %d %q: with one candidate there is nothing to disambiguate, and a "+
			"caller that does not know its own dialed address must not lose the read",
			rc.MgmtVLAN, rc.Mgmt.Address)
	}
	if len(rc.MgmtSVINotes) != 0 {
		t.Errorf("one candidate and no dialed address is not news: %v", rc.MgmtSVINotes)
	}

	// Reached at an address this SVI does not carry — the out-of-band port, a
	// console, an address translation.
	rc = parseRunningConfig(one+"interface Management1\n   ip address 192.168.1.10/24\n!\nend\n",
		"192.168.1.10")
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
	var rc = parseRunningConfig("interface Vlan99\n   ip address dhcp\n!\nend\n", "192.0.2.5")
	if rc.MgmtVLAN != 99 || rc.Mgmt.Mode != driver.MgmtAddrModeDHCP {
		t.Errorf("mgmt = %d %q, want 99 dhcp", rc.MgmtVLAN, rc.Mgmt.Mode)
	}
	if rc.Mgmt.Address != "" {
		t.Errorf("address = %q: the configuration carries the intent, never the lease",
			rc.Mgmt.Address)
	}

	// Beside a static SVI that DOES carry the dialed address, the comparison
	// decides and the DHCP block is not a contender.
	rc = parseRunningConfig("interface Vlan99\n   ip address dhcp\n!\n"+
		"interface Vlan3000\n   ip address 10.255.255.1/31\n!\nend\n", "10.255.255.1")
	if rc.MgmtVLAN != 3000 || rc.Mgmt.Mode != driver.MgmtAddrModeStatic {
		t.Errorf("mgmt = %d %q, want 3000 static: 10.255.255.1 is where this session "+
			"arrived, whatever the other block intends", rc.MgmtVLAN, rc.Mgmt.Mode)
	}
}

// "shutdown" AFTER the address. EOS prints it before, but a configuration that
// carries the two lines the other way round describes the same dead interface —
// and a read that depended on the order would call it the management path.
func TestManagementSVIIgnoresAShutSVIWhateverTheLineOrder(t *testing.T) {
	var after = parseRunningConfig(
		"interface Vlan99\n   ip address 192.0.2.1/16\n   shutdown\n!\nend\n", "192.0.2.1")
	if after.MgmtVLAN != 0 {
		t.Errorf("MgmtVLAN = %d, want 0: the SVI is shut down, its address answers nothing, "+
			"and the position of the \"shutdown\" line does not change that", after.MgmtVLAN)
	}

	// The live SVI beside a shut one that would otherwise have matched.
	var mixed = parseRunningConfig(
		"interface Vlan99\n   ip address 192.0.2.1/16\n   shutdown\n!\n"+
			"interface Vlan100\n   ip address 10.100.0.1/24\n!\nend\n", "192.0.2.1")
	if mixed.MgmtVLAN != 100 {
		t.Errorf("MgmtVLAN = %d, want 100 — the only SVI left standing", mixed.MgmtVLAN)
	}
}
