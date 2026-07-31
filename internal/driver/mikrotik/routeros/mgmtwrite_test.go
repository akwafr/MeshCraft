package routeros

import "testing"

func TestUplinksFromMgmtPreferCurrentVLANMembers(t *testing.T) {
	var st = DeviceState{
		BridgeName: "bridge",
		MgmtVLAN:   99,
		VLANs: []BridgeVLAN{
			{ID: 99, TaggedMembers: []string{"bridge", "sfp-sfpplus2"}},
			{ID: 100, TaggedMembers: []string{"sfp-sfpplus1"}},
		},
		LinkUp: map[string]bool{"ether1": true, "sfp-sfpplus2": true},
	}
	var got = uplinksFromMgmt(st)
	if len(got) != 1 || got[0] != "sfp-sfpplus2" {
		t.Fatalf("uplinks = %v, expected [sfp-sfpplus2] (bridge CPU excluded)", got)
	}
}

func TestUplinksFromMgmtFallbackToLinkUp(t *testing.T) {
	var st = DeviceState{
		BridgeName: "bridge",
		MgmtVLAN:   1,
		VLANs:      nil,
		LinkUp:     map[string]bool{"bridge": true, "ether3": true, "ether4": false},
	}
	var got = uplinksFromMgmt(st)
	if len(got) != 1 || got[0] != "ether3" {
		t.Fatalf("uplinks = %v, expected [ether3]", got)
	}
}

func TestVLANIDsContain(t *testing.T) {
	if !vlanIDsContain("99", 99) || !vlanIDsContain("1,99,100", 99) {
		t.Fatal("expected match")
	}
	if vlanIDsContain("1,100", 99) || vlanIDsContain("", 99) {
		t.Fatal("expected miss")
	}
}
