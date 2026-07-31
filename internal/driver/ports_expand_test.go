package driver

import (
	"reflect"
	"testing"
)

func TestExpandAllTrunksMaterializesFloor(t *testing.T) {
	var desired = map[string]PortState{
		"sfp-sfpplus1": {Mode: "trunk", PVID: 1, Tagged: []int{99}, TaggedAtLeast: true},
		"ether1":       {Mode: "access", PVID: 20},
	}
	var site = []VLAN{{ID: 10}, {ID: 20}, {ID: 99}, {ID: 1}}
	var got = ExpandAllTrunks(desired, site)

	var up = got["sfp-sfpplus1"]
	if up.TaggedAtLeast {
		t.Fatal("TaggedAtLeast must clear when the site declares VLANs")
	}
	// PVID 1 excluded from tagged; site list becomes the exact set.
	if !reflect.DeepEqual(up.Tagged, []int{10, 20, 99}) {
		t.Fatalf("tagged = %v, want [10 20 99]", up.Tagged)
	}
	if got["ether1"].Mode != "access" || got["ether1"].PVID != 20 {
		t.Fatalf("unrelated port mutated: %+v", got["ether1"])
	}
}

func TestExpandAllTrunksKeepsFloorWhenSiteEmpty(t *testing.T) {
	var desired = map[string]PortState{
		"sfp-sfpplus1": {Mode: "trunk", PVID: 1, TaggedAtLeast: true},
	}
	var got = ExpandAllTrunks(desired, nil)
	if !got["sfp-sfpplus1"].TaggedAtLeast {
		t.Fatal("empty site plan must leave TaggedAtLeast set — stripping would guess")
	}
}
