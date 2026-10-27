package instanton

import (
	"reflect"
	"testing"
)

func TestParsePortRange(t *testing.T) {
	var cases = []struct {
		name     string
		spec     string
		expected []string
	}{
		{"empty", "", nil},
		{"single port", "7", []string{"GE7"}},
		{"range", "1-3", []string{"GE1", "GE2", "GE3"}},
		{"multiple ranges", "1-2,5", []string{"GE1", "GE2", "GE5"}},
		// Aggregates are not physical ports: expanding them would
		// invent slots on the faceplate.
		{"aggregates ignored", "1-2,TRK1-16", []string{"GE1", "GE2"}},
		{"aggregate only", "TRK1-16", nil},
		// A response we cannot parse must not spawn ports.
		{"reversed range", "9-2", nil},
		{"unreadable token", "abc,1", []string{"GE1"}},
	}
	var c = cases[0]
	for _, c = range cases {
		t.Run(c.name, func(t *testing.T) {
			var got = ParsePortRange(c.spec, "GE")
			if !reflect.DeepEqual(got, c.expected) {
				t.Errorf("ParsePortRange(%q) = %v, expected %v", c.spec, got, c.expected)
			}
		})
	}
}

// Real response from a 1930 (48 ports + 4 SFP+), enriched with a tagged VLAN to
// cover the trunk case.
const membershipXML = `<?xml version="1.0" encoding="UTF-8" ?><ResponseData><DeviceConfiguration>
<VLANInterfaceMembershipTable type="section">
<Entry><VLANID>1</VLANID><VLANName></VLANName>
<taggedPorts></taggedPorts><untaggedPorts>1-3,TRK1-16</untaggedPorts></Entry>
<Entry><VLANID>20</VLANID><VLANName>audio</VLANName>
<taggedPorts>2-3</taggedPorts><untaggedPorts>4</untaggedPorts></Entry>
<Entry><VLANID>30</VLANID><VLANName>video</VLANName>
<taggedPorts>3</taggedPorts><untaggedPorts></untaggedPorts></Entry>
</VLANInterfaceMembershipTable></DeviceConfiguration></ResponseData>`

func TestPortsFromMembership(t *testing.T) {
	var got = portsFromMembership(membershipXML, "GE")

	// GE1: untagged member of VLAN 1 only → access.
	if got["GE1"].Mode != "access" || got["GE1"].PVID != 1 {
		t.Errorf("GE1 = %+v, expected access on VLAN 1", got["GE1"])
	}
	// GE4: untagged on 20, never tagged → access.
	if got["GE4"].Mode != "access" || got["GE4"].PVID != 20 {
		t.Errorf("GE4 = %+v, expected access on VLAN 20", got["GE4"])
	}
	// GE2: native on 1 AND tagged on 20 → trunk, PVID preserved.
	if got["GE2"].Mode != "trunk" || got["GE2"].PVID != 1 ||
		!reflect.DeepEqual(got["GE2"].Tagged, []int{20}) {
		t.Errorf("GE2 = %+v, expected trunk PVID 1 tagged [20]", got["GE2"])
	}
	// GE3: tagged in two VLANs, returned SORTED — unstable order would make
	// an unchanged port flicker.
	if !reflect.DeepEqual(got["GE3"].Tagged, []int{20, 30}) {
		t.Errorf("GE3 tagged = %v, expected [20 30] sorted", got["GE3"].Tagged)
	}
	// Aggregates do not create ports.
	var name string
	for name = range got {
		if name == "GETRK1" || name == "TRK1" {
			t.Errorf("an aggregate was taken for a port: %q", name)
		}
	}
}
