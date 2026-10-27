package api

import (
	"encoding/json"
	"reflect"
	"testing"

	"meshcraft/internal/model"
)

// dhcpSnoopVLANsFor decides what the device is told to inspect, so these pin the
// difference between "protects the port that was clicked" and "switches
// inspection on across a site nobody asked about".
//
// The stakes are not symmetric. Too FEW VLANs means a trusted port sits outside
// every inspected VLAN, which on at least one platform does not even appear in
// the trust table — a write that reads back as success and protects nothing. Too
// MANY means inspection turned on for VLANs the gesture never mentioned.

func snoopDevice(t *testing.T, ports map[string]any) model.Device {
	t.Helper()
	var raw, err = json.Marshal(ports)
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return model.Device{Ports: raw}
}

func TestSnoopVLANsComeFromTheNamedPortsNotTheSite(t *testing.T) {
	var d = snoopDevice(t, map[string]any{
		"GE1": map[string]any{"mode": "access", "pvid": 100},
		"GE2": map[string]any{"mode": "access", "pvid": 200},
	})
	// The site declares far more than the gesture touches.
	var got, why = dhcpSnoopVLANsFor(d, []string{"GE1"}, []int{10, 20, 100, 200, 300})
	if !reflect.DeepEqual(got, []int{100}) {
		t.Fatalf("VLANs = %v, want [100]: only the VLAN the named port carries", got)
	}
	if why != "" {
		t.Errorf("a fully derivable plan should need no explanation, got %q", why)
	}
}

func TestSnoopVLANsIncludeTaggedMembership(t *testing.T) {
	// The uplink case: trusting a trunk is how DHCP replies are let in, and it is
	// worthless on the VLANs the trunk carries if those are not inspected.
	var d = snoopDevice(t, map[string]any{
		"GE24": map[string]any{"mode": "trunk", "pvid": 1, "tagged": []int{42, 98}},
	})
	var got, _ = dhcpSnoopVLANsFor(d, []string{"GE24"}, nil)
	if !reflect.DeepEqual(got, []int{1, 42, 98}) {
		t.Fatalf("VLANs = %v, want [1 42 98]: untagged and tagged both count", got)
	}
}

func TestSnoopVLANsFallBackToTheSiteWhenThePortIsUnknown(t *testing.T) {
	// An empty list would mean "inspect nothing", i.e. a device left with the
	// feature on and zero coverage. The site list is the honest fallback.
	var d = snoopDevice(t, map[string]any{})
	var got, why = dhcpSnoopVLANsFor(d, []string{"GE7"}, []int{10, 20})
	if !reflect.DeepEqual(got, []int{10, 20}) {
		t.Fatalf("VLANs = %v, want the site list [10 20]", got)
	}
	if why == "" {
		t.Error("fell back to the site and said nothing about it")
	}
}

func TestSnoopVLANsFallBackOnTaggedAllButKeepWhatWasDerivable(t *testing.T) {
	// "all" is not an enumeration this layer may invent, so the site list joins
	// in — but a port that WAS derivable must not be dropped on its account.
	var d = snoopDevice(t, map[string]any{
		"GE1":  map[string]any{"mode": "access", "pvid": 100},
		"GE24": map[string]any{"mode": "trunk", "pvid": 1, "tagged": "all"},
	})
	var got, why = dhcpSnoopVLANsFor(d, []string{"GE1", "GE24"}, []int{10, 20})
	var want = []int{1, 10, 20, 100}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("VLANs = %v, want %v", got, want)
	}
	if why == "" {
		t.Error("\"all\" forced a site-wide fallback and nothing said so")
	}
}

func TestSnoopVLANsNeverReturnZeroOrDuplicates(t *testing.T) {
	// A pvid of 0 is "unset", not VLAN 0, and two ports on one VLAN must not ask
	// the connector to enable it twice — connectors write the list as given.
	var d = snoopDevice(t, map[string]any{
		"GE1": map[string]any{"mode": "access", "pvid": 100},
		"GE2": map[string]any{"mode": "access", "pvid": 100},
		"GE3": map[string]any{"mode": "access", "pvid": 0},
	})
	var got, _ = dhcpSnoopVLANsFor(d, []string{"GE1", "GE2", "GE3"}, nil)
	if !reflect.DeepEqual(got, []int{100}) {
		t.Fatalf("VLANs = %v, want [100]: deduplicated, and 0 is not a VLAN", got)
	}
}
