package eos

// The gNMI-transport-versus-management-VRF read, pinned on the exact shape
// core01.lab held on 2026-08-14: Vlan99 attached to VRF-LAN, the transport
// still in VRF-MANAGEMENT — an instance with no interfaces, where it bound
// successfully and served nobody for two days while ping, SSH and SNMP all
// answered. Cf. gnmiVRFFact and runConfig.GNMITransportVRF.

import (
	"strings"
	"testing"
)

// core01Shape is the running-config excerpt of the incident, reduced to the
// blocks the read consults. The "vrf" line precedes "ip address" in the SVI
// block because that is the order EOS prints — and the order that forces the
// membership to be resolved after the block, not inline.
const core01Shape = `vrf instance VRF-LAN
!
vrf instance VRF-MANAGEMENT
!
interface Vlan99
   vrf VRF-LAN
   ip address 192.0.2.5/16
!
management ssh
   vrf VRF-LAN
!
management api gnmi
   transport grpc default
      ssl profile meshcraft
      vrf VRF-MANAGEMENT
!
end
`

func TestRunningConfigReadsBothVRFsOfTheIncident(t *testing.T) {
	var rc = parseRunningConfig(core01Shape, "192.0.2.5")
	if rc.MgmtVLAN != 99 || rc.Mgmt.Address != "192.0.2.5/16" {
		t.Fatalf("management SVI misread: vlan=%d addr=%q", rc.MgmtVLAN, rc.Mgmt.Address)
	}
	if rc.MgmtVRF != "VRF-LAN" {
		t.Errorf("management VRF = %q, want VRF-LAN (the \"vrf\" line precedes the address)", rc.MgmtVRF)
	}
	if !rc.GNMIConfigured {
		t.Error("the \"management api gnmi\" block was not seen")
	}
	if rc.GNMITransportVRF != "VRF-MANAGEMENT" {
		t.Errorf("gNMI transport VRF = %q, want VRF-MANAGEMENT", rc.GNMITransportVRF)
	}
	// The "vrf VRF-LAN" under "management ssh" must not be read as anything —
	// neither the SVI's membership nor the transport's.
	var facts = strings.Join(gnmiVRFFact(rc), " ")
	if !strings.Contains(facts, "VRF-MANAGEMENT") || !strings.Contains(facts, "VRF-LAN") {
		t.Errorf("the mismatch fact does not name both instances: %q", facts)
	}
	if !strings.Contains(facts, "vrf VRF-LAN") {
		t.Errorf("the fact does not spell the repair (\"vrf VRF-LAN\"): %q", facts)
	}
}

// A transport in the SAME instance as management — the healthy shape, named
// (VRF-LAN twice) or defaulted (no vrf lines at all) — must say nothing: this
// fact exists for the disagreement, and a sentence on every healthy switch
// would bury the one that matters.
func TestGNMIVRFFactIsSilentWhenTheInstancesAgree(t *testing.T) {
	var agreeing = `interface Vlan99
   vrf VRF-LAN
   ip address 192.0.2.5/16
!
management api gnmi
   transport grpc default
      vrf VRF-LAN
!
end
`
	var rc = parseRunningConfig(agreeing, "192.0.2.5")
	if len(gnmiVRFFact(rc)) != 0 {
		t.Errorf("agreeing VRFs produced a fact: %v", gnmiVRFFact(rc))
	}

	var bothDefault = `interface Vlan99
   ip address 192.0.2.5/16
!
management api gnmi
   transport grpc default
      port 6030
!
end
`
	rc = parseRunningConfig(bothDefault, "192.0.2.5")
	if len(gnmiVRFFact(rc)) != 0 {
		t.Errorf("default-instance-on-both-sides produced a fact: %v", gnmiVRFFact(rc))
	}
	// And a device with NO gNMI block has nothing to compare: silence again.
	rc = parseRunningConfig("interface Vlan99\n   vrf VRF-LAN\n   ip address 192.0.2.5/16\n!\nend\n", "192.0.2.5")
	if len(gnmiVRFFact(rc)) != 0 {
		t.Errorf("a device without gNMI produced a fact: %v", gnmiVRFFact(rc))
	}
}

// The transport in the DEFAULT instance while management moved into a named one
// — the direction MgmtPushCmds would produce against a VRF site (GNMIOpts.VRF
// warns about exactly this) — is a disagreement like any other.
func TestGNMIVRFFactNamesTheDefaultInstanceToo(t *testing.T) {
	var rc = parseRunningConfig(`interface Vlan99
   vrf VRF-MANAGEMENT
   ip address 192.0.2.5/16
!
management api gnmi
   transport grpc default
      port 6030
!
end
`, "192.0.2.5")
	var facts = gnmiVRFFact(rc)
	if len(facts) != 1 {
		t.Fatalf("want one fact, got %v", facts)
	}
	if !strings.Contains(facts[0], "VRF default") || !strings.Contains(facts[0], "VRF-MANAGEMENT") {
		t.Errorf("the fact does not name the default instance against the named one: %q", facts[0])
	}
}
