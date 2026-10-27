package instanton

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// newSwitchWithState serves a device that responds to the three ReadState reads,
// including its management VLAN, and accepts writes.
func newSwitchWithState(t *testing.T, seen *[]captured, mgmtVLAN int) *httptest.Server {
	t.Helper()
	var srv = httptest.NewTLSServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			*seen = append(*seen, captured{
				method: r.Method, path: r.URL.Path, rawQuery: r.URL.RawQuery,
				body: readAll(r),
			})
			if r.Method != http.MethodGet {
				_, _ = w.Write([]byte(okResponse))
				return
			}
			switch {
			case strings.Contains(r.URL.RawQuery, "ManagementInterfaceGlobalSetting"):
				_, _ = w.Write([]byte(`<ResponseData><DeviceConfiguration>` +
					`<ManagementInterfaceGlobalSetting type="section"><managementVLAN>` +
					itoa(mgmtVLAN) + `</managementVLAN></ManagementInterfaceGlobalSetting>` +
					`</DeviceConfiguration></ResponseData>`))
			case strings.Contains(r.URL.RawQuery, "VLANList"):
				_, _ = w.Write([]byte(`<ResponseData><DeviceConfiguration><VLANList type="section">` +
					`<VLAN><VLANID>1</VLANID><VLANName></VLANName></VLAN>` +
					`</VLANList></DeviceConfiguration></ResponseData>`))
			case strings.Contains(r.URL.RawQuery, "Standard802_3List"):
				// Only port 1 has a link: it alone must be spared.
				_, _ = w.Write([]byte(`<ResponseData><DeviceConfiguration>` +
					`<Standard802_3List type="section">` +
					`<Entry><interfaceName>1</interfaceName><linkState>1</linkState></Entry>` +
					`<Entry><interfaceName>12</interfaceName><linkState>2</linkState></Entry>` +
					`</Standard802_3List></DeviceConfiguration></ResponseData>`))
			default:
				_, _ = w.Write([]byte(membershipXML))
			}
		}))
	t.Cleanup(srv.Close)
	return srv
}

func itoa(n int) string {
	var digits = "0123456789"
	if n == 0 {
		return "0"
	}
	var out string
	for n > 0 {
		out = string(digits[n%10]) + out
		n /= 10
	}
	return out
}

// TestMembershipFor: the port → VLAN pivot that decides which ports stay in which VLAN.
// A mistake here only shows when a switch goes silent, hence the care in this test.
func TestMembershipFor(t *testing.T) {
	var desired = map[string]PortState{
		"GE1": {Mode: "access", PVID: 20},
		"GE2": {Mode: "access", PVID: 20},
		"GE3": {Mode: "trunk", PVID: 1, Tagged: []int{20, 30}},
		// "libre" = unmanaged: meshcraft does not claim this port; it must appear
		// in NO membership.
		"GE4": {Mode: "libre", PVID: 99},
	}
	var got = MembershipFor(desired)

	if len(got) != 3 {
		t.Fatalf("expected 3 VLANs (1, 20, 30), got %d: %+v", len(got), got)
	}
	// Ascending order: two runs must produce the same request, otherwise comparing
	// two transcripts means nothing.
	if got[0].VLAN != 1 || got[1].VLAN != 20 || got[2].VLAN != 30 {
		t.Errorf("VLANs out of order: %+v", got)
	}
	if !reflect.DeepEqual(got[1].Untagged, []string{"GE1", "GE2"}) {
		t.Errorf("VLAN 20 untagged = %v, expected [GE1 GE2]", got[1].Untagged)
	}
	if !reflect.DeepEqual(got[1].Tagged, []string{"GE3"}) {
		t.Errorf("VLAN 20 tagged = %v, expected [GE3]", got[1].Tagged)
	}
	// GE4 is free: it belongs to nothing.
	var m membership
	for _, m = range got {
		var n string
		for _, n = range append(append([]string{}, m.Tagged...), m.Untagged...) {
			if n == "GE4" {
				t.Errorf("a free port must belong to no VLAN: %+v", m)
			}
		}
	}
}

// TestMembershipForNativeWinsOverTagged: a VLAN both native and tagged on the same port
// makes no sense. Native wins, as the device UI does — sending it in both lists would
// make the write fail.
func TestMembershipForNativeWinsOverTagged(t *testing.T) {
	var got = MembershipFor(map[string]PortState{
		"GE1": {Mode: "trunk", PVID: 10, Tagged: []int{10, 20}},
	})
	var m membership
	for _, m = range got {
		if m.VLAN != 10 {
			continue
		}
		if len(m.Tagged) != 0 {
			t.Errorf("VLAN 10 must not be tagged on its native port: %+v", m)
		}
		if !reflect.DeepEqual(m.Untagged, []string{"GE1"}) {
			t.Errorf("VLAN 10 untagged = %v, expected [GE1]", m.Untagged)
		}
	}
}

// TestPortNumbers: a name that does not follow the convention is IGNORED, never guessed.
// Sending a wrong port number is writing to the wrong place.
func TestPortNumbers(t *testing.T) {
	var got = portNumbers([]string{"GE1", "GE48", "TRK1", "GE", "GEx", "other"}, "GE")
	if !reflect.DeepEqual(got, []string{"1", "48"}) {
		t.Errorf("portNumbers = %v, expected [1 48]", got)
	}
}

// TestApplyPortsRefusesWithoutManagementVLAN: not knowing which VLAN carries
// administration and writing anyway is exactly the gesture that costs a site visit.
// This product line has no serial port.
//
// The silent test device declares no management VLAN, and no plan provides one: the
// re-read still happens — it is harmless — but NO write must go out.
func TestApplyPortsRefusesWithoutManagementVLAN(t *testing.T) {
	var seen []captured
	var srv = newFakeSwitch(t, &seen)

	var _, err = adminOn(srv).ApplyPorts(context.Background(), PortPlan{
		Desired: map[string]PortState{"GE1": {Mode: "access", PVID: 20}},
	})
	if err == nil {
		t.Fatal("writing without knowing the management VLAN must be refused")
	}
	var w = writes(seen)
	if len(w) != 0 {
		t.Errorf("no write must be sent: %+v", w)
	}
}

// TestApplyPortsProtectsTheDevicesVLAN: the guardrail must protect the VLAN the
// DEVICE declares, not only the one the plan wants. They differ until a device has been
// migrated — observed on a 1930: plan at 99, device at 1. Protecting the wrong one
// would leave the session VLAN writable.
//
// It must spare ONLY ports that carry a link: GE1 is cabled, GE12 is not and must
// therefore move, although it is also untagged in the administration VLAN.
func TestApplyPortsProtectsTheDevicesVLAN(t *testing.T) {
	var seen []captured
	// Device declares VLAN 1 as management; plan says 99.
	var srv = newSwitchWithState(t, &seen, 1)

	var res, err = adminOn(srv).ApplyPorts(context.Background(), PortPlan{
		Desired: map[string]PortState{
			// GE1 is untagged in device VLAN 1: changing its native VLAN would cut access.
			"GE1":  {Mode: "access", PVID: 30},
			"GE12": {Mode: "access", PVID: 50},
		},
		MgmtVLAN: 99,
	})
	if err != nil {
		t.Fatalf("ApplyPorts: %v", err)
	}
	// GE1 is untagged in VLAN 1, through which the device is administered: its native
	// VLAN must never be rewritten.
	var w captured
	for _, w = range writes(seen) {
		if strings.Contains(w.body, "<interfaceName>1</interfaceName>") {
			t.Errorf("the port carrying administration was rewritten: %s", w.body)
		}
	}
	if len(res.Skipped) == 0 {
		t.Error("the mismatch must be reported, not passed silently")
	}
	var wrote12 bool
	for _, w = range writes(seen) {
		if strings.Contains(w.body, "<interfaceName>12</interfaceName>") {
			wrote12 = true
		}
	}
	if !wrote12 {
		t.Error("ordinary ports must still be written")
	}
}

// TestApplyPortsWithoutLinkSparesNothing: on a factory-fresh device, ALL ports are untagged
// in the administration VLAN. Sparing them all would mean configuring nothing — "adopt"
// would mean nothing. A port with no cable carries no session.
func TestApplyPortsWithoutLinkSparesNothing(t *testing.T) {
	var seen []captured
	var srv = newSwitchWithState(t, &seen, 1)

	var res, err = adminOn(srv).ApplyPorts(context.Background(), PortPlan{
		Desired:  map[string]PortState{"GE12": {Mode: "access", PVID: 100}},
		MgmtVLAN: 99,
	})
	if err != nil {
		t.Fatalf("ApplyPorts: %v", err)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("a port without link must not be spared: %+v", res.Skipped)
	}
	if len(res.PortsWritten) != 1 {
		t.Errorf("port should have been written: %+v", res.PortsWritten)
	}
}

// TestApplyPortsForceLiftsTheGuard: the operator knows their topology better than
// meshcraft. The guardrail must be liftable — explicitly, never by default, and never
// by automatic apply.
func TestApplyPortsForceLiftsTheGuard(t *testing.T) {
	var seen []captured
	var srv = newSwitchWithState(t, &seen, 1)

	var res, err = adminOn(srv).ApplyPorts(context.Background(), PortPlan{
		Desired:  map[string]PortState{"GE1": {Mode: "access", PVID: 30}},
		MgmtVLAN: 99,
		Force:    true,
	})
	if err != nil {
		t.Fatalf("ApplyPorts: %v", err)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("in force mode, nothing should be spared: %+v", res.Skipped)
	}
	var wrote bool
	var w captured
	for _, w = range writes(seen) {
		if strings.Contains(w.body, "<interfaceName>1</interfaceName>") {
			wrote = true
		}
	}
	if !wrote {
		t.Error("protected port must be written when forced")
	}
}

// TestApplyPortsUnresolvedAllRemovesNothing: unresolved "all" (`TaggedAtLeast`)
// must not strip tagged membership while the site plan is empty.
//
// Contrast: TestApplyPortsExplicitListRemoves — an exact list does remove.
func TestApplyPortsUnresolvedAllRemovesNothing(t *testing.T) {
	var seen []captured
	var srv = newSwitchWithState(t, &seen, 1)

	// GE3 already carries tagged VLANs 20 and 30 (cf. membershipXML). Unresolved
	// "all" asks for 20 and 99 with TaggedAtLeast: without the flag, 30 would go.
	var res, err = adminOn(srv).ApplyPorts(context.Background(), PortPlan{
		Desired: map[string]PortState{
			"GE3": {Mode: "trunk", PVID: 1, Tagged: []int{20, 99}, TaggedAtLeast: true},
		},
		MgmtVLAN: 99,
	})
	if err != nil {
		t.Fatalf("ApplyPorts: %v", err)
	}
	var w captured
	for _, w = range writes(seen) {
		if strings.Contains(w.body, `action="delete"`) {
			t.Errorf("unresolved all must remove no tagged VLAN: %s", w.body)
		}
	}
	var added bool
	for _, w = range writes(seen) {
		if strings.Contains(w.body, "<generalTaggedVLANs>99</generalTaggedVLANs>") {
			added = true
		}
	}
	if !added {
		t.Error("missing site VLAN must be added")
	}
	if len(res.PortsWritten) != 1 {
		t.Errorf("port should have been written: %+v", res.PortsWritten)
	}
}

// TestApplyPortsExplicitListRemoves: counterpart to the previous test. A TYPED list
// is an enumeration, and removal is legitimate there — that is the whole difference
// between "all" and a list, and it must stay visible.
func TestApplyPortsExplicitListRemoves(t *testing.T) {
	var seen []captured
	var srv = newSwitchWithState(t, &seen, 1)

	var _, err = adminOn(srv).ApplyPorts(context.Background(), PortPlan{
		Desired: map[string]PortState{
			"GE3": {Mode: "trunk", PVID: 1, Tagged: []int{20}},
		},
		MgmtVLAN: 99,
	})
	if err != nil {
		t.Fatalf("ApplyPorts: %v", err)
	}
	var dropped bool
	var w captured
	for _, w = range writes(seen) {
		if strings.Contains(w.body, `action="delete"`) &&
			strings.Contains(w.body, "<generalTaggedVLANs>30</generalTaggedVLANs>") {
			dropped = true
		}
	}
	if !dropped {
		t.Error("an explicit list must remove VLANs it no longer contains")
	}
}

// TestApplyPortsSetBeforeDeleteWhenNativeChanges reproduces the fault
// reported live on sw-labo-1930: "device refused change (code 3 port 7
// MUST belong to at least one vlan)". GE7 was a TAGGED-ONLY member of VLAN
// 99, no native VLAN of its own — a legal state on this range — moving to
// native access VLAN 100 with no tagged membership at all. Sending the
// DELETE (drop VLAN 99) before the SET (add VLAN 100) left the port
// momentarily claiming no VLAN whatsoever, and the device validates each
// POST synchronously: it refused the DELETE right there, before the SET that
// would have fixed it ever went out.
func TestApplyPortsSetBeforeDeleteWhenNativeChanges(t *testing.T) {
	var seen []captured
	// Custom fixture, not newSwitchWithState's shared one: GE7 needs to be
	// TAGGED-ONLY (no untaggedPorts entry anywhere), the exact shape that
	// triggers the fault — no port in the shared fixture is in that state.
	const membership = `<?xml version="1.0" encoding="UTF-8" ?><ResponseData><DeviceConfiguration>
<VLANInterfaceMembershipTable type="section">
<Entry><VLANID>1</VLANID><VLANName></VLANName>
<taggedPorts></taggedPorts><untaggedPorts>TRK1-16</untaggedPorts></Entry>
<Entry><VLANID>99</VLANID><VLANName>mgmt</VLANName>
<taggedPorts>7</taggedPorts><untaggedPorts></untaggedPorts></Entry>
<Entry><VLANID>100</VLANID><VLANName>user</VLANName>
<taggedPorts></taggedPorts><untaggedPorts></untaggedPorts></Entry>
</VLANInterfaceMembershipTable></DeviceConfiguration></ResponseData>`

	var srv = httptest.NewTLSServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			seen = append(seen, captured{
				method: r.Method, path: r.URL.Path, rawQuery: r.URL.RawQuery,
				body: readAll(r),
			})
			if r.Method != http.MethodGet {
				_, _ = w.Write([]byte(okResponse))
				return
			}
			switch {
			case strings.Contains(r.URL.RawQuery, "ManagementInterfaceGlobalSetting"):
				_, _ = w.Write([]byte(`<ResponseData><DeviceConfiguration>` +
					`<ManagementInterfaceGlobalSetting type="section"><managementVLAN>99</managementVLAN>` +
					`</ManagementInterfaceGlobalSetting></DeviceConfiguration></ResponseData>`))
			case strings.Contains(r.URL.RawQuery, "VLANList"):
				_, _ = w.Write([]byte(`<ResponseData><DeviceConfiguration><VLANList type="section">` +
					`<VLAN><VLANID>1</VLANID><VLANName></VLANName></VLAN>` +
					`<VLAN><VLANID>99</VLANID><VLANName>mgmt</VLANName></VLAN>` +
					`<VLAN><VLANID>100</VLANID><VLANName>user</VLANName></VLAN>` +
					`</VLANList></DeviceConfiguration></ResponseData>`))
			case strings.Contains(r.URL.RawQuery, "Standard802_3List"):
				_, _ = w.Write([]byte(`<ResponseData><DeviceConfiguration>` +
					`<Standard802_3List type="section"></Standard802_3List></DeviceConfiguration></ResponseData>`))
			default:
				_, _ = w.Write([]byte(membership))
			}
		}))
	t.Cleanup(srv.Close)

	var _, err = adminOn(srv).ApplyPorts(context.Background(), PortPlan{
		Desired:  map[string]PortState{"GE7": {Mode: "access", PVID: 100}},
		MgmtVLAN: 99,
	})
	if err != nil {
		t.Fatalf("ApplyPorts: %v", err)
	}

	var setIdx, delIdx = -1, -1
	var wr = writes(seen)
	var i int
	var w captured
	for i, w = range wr {
		if !strings.Contains(w.body, "<interfaceName>7</interfaceName>") {
			continue
		}
		if strings.Contains(w.body, `action="set"`) {
			setIdx = i
		}
		if strings.Contains(w.body, `action="delete"`) {
			delIdx = i
		}
	}
	if setIdx == -1 || delIdx == -1 {
		t.Fatalf("expected both a set and a delete for GE7, got: %+v", wr)
	}
	if setIdx > delIdx {
		t.Error("SET must reach the device before DELETE: dropping VLAN 99 first would " +
			"leave GE7 with zero VLAN membership before VLAN 100 lands — the exact fault " +
			"reported on sw-labo-1930")
	}
	if !strings.Contains(wr[setIdx].body, "<generalPVID>100</generalPVID>") {
		t.Errorf("SET must carry the new native VLAN: %s", wr[setIdx].body)
	}
	if !strings.Contains(wr[delIdx].body, "<generalTaggedVLANs>99</generalTaggedVLANs>") {
		t.Errorf("DELETE must drop the old tagged VLAN 99: %s", wr[delIdx].body)
	}
}
