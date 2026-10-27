package siteconfig

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRemapKeepsUnknownFields : a file exported by a newer version must
// not return stripped of what it knew how to do. Translation works on raw JSON
// precisely for that — a round trip through a struct would drop every undeclared
// field, silently.
func TestRemapKeepsUnknownFields(t *testing.T) {
	var spec = json.RawMessage(`{
      "description":"floor",
      "future_field":{"a":1},
      "rules":[{"label":"uplink","port_profile_id":"u-1",
                "select":{"media":"sfp_plus"},"unknown_option":true}]}`)
	var out = RemapSwitchRules(spec, RefByID, RefByName, map[string]string{"u-1": "Uplink"})
	var s = string(out)
	if !strings.Contains(s, "future_field") || !strings.Contains(s, "unknown_option") {
		t.Errorf("unknown fields lost: %s", s)
	}
	if !strings.Contains(s, `"port_profile":"Uplink"`) {
		t.Errorf("reference not translated: %s", s)
	}
	if strings.Contains(s, "port_profile_id") {
		t.Errorf("old key remains: %s", s)
	}
}

// TestRemapIsReversible : export then import must return the starting spec. If
// forward and back did not match, configuration would take a trip through a file
// and not return identical.
func TestRemapIsReversible(t *testing.T) {
	var spec = json.RawMessage(`{"rules":[{"label":"u","port_profile_id":"abc"}]}`)
	var exported = RemapSwitchRules(spec, RefByID, RefByName, map[string]string{"abc": "Uplink"})
	var imported = RemapSwitchRules(exported, RefByName, RefByID, map[string]string{"Uplink": "zzz"})
	var root map[string]any
	if json.Unmarshal(imported, &root) != nil {
		t.Fatalf("broken JSON: %s", imported)
	}
	var rule = root["rules"].([]any)[0].(map[string]any)
	if rule[RefByID] != "zzz" {
		t.Errorf("destination instance id was not set: %v", rule)
	}
	var _, remaining = rule[RefByName]
	if remaining {
		t.Errorf("name should have disappeared: %v", rule)
	}
}

// TestRemapLeavesAnUnknownReferenceInPlace : erasing a reference we cannot
// translate would produce a rule with no configuration, therefore a port that
// silently receives what the next rule gives it. Leaving it flags it on screen as
// a faulty rule, which is fixable.
func TestRemapLeavesAnUnknownReferenceInPlace(t *testing.T) {
	var spec = json.RawMessage(`{"rules":[{"label":"u","port_profile_id":"gone"}]}`)
	var out = RemapSwitchRules(spec, RefByID, RefByName, map[string]string{})
	if !strings.Contains(string(out), `"port_profile_id":"gone"`) {
		t.Errorf("unknown reference erased: %s", out)
	}
}

// TestRemapPortFollowsKeepsTheRest : a port's follow-link becomes a
// name on export, and everything ELSE the port carries — label, telemetry,
// fields a newer version wrote — passes through untouched. Same raw-JSON rule
// as switch profile rules, same reason.
func TestRemapPortFollowsKeepsTheRest(t *testing.T) {
	var ports = json.RawMessage(`{
	  "GE1":{"label":"PC room 2","mode":"access","pvid":10,
	         "profile_id":"u-1","rx_mbps":3.25,"future_field":true},
	  "GE2":{"mode":"trunk","tagged":"all"}}`)
	var out = RemapPortFollows(ports, PortRefByID, PortRefByName,
		map[string]string{"u-1": "Bureaux"})
	var s = string(out)
	if !strings.Contains(s, `"profile":"Bureaux"`) {
		t.Errorf("follow-link not translated: %s", s)
	}
	if strings.Contains(s, "profile_id") {
		t.Errorf("old key remains: %s", s)
	}
	if !strings.Contains(s, "PC room 2") || !strings.Contains(s, "future_field") ||
		!strings.Contains(s, "3.25") {
		t.Errorf("port fields lost in translation: %s", s)
	}
	if !strings.Contains(s, `"tagged":"all"`) {
		t.Errorf("a port without a follow must pass through intact: %s", s)
	}
}

// TestRemapFollowsIsReversible : export then import lands on the
// destination instance's id. The trip through the file must not lose which
// group a port lives by.
func TestRemapFollowsIsReversible(t *testing.T) {
	var ports = json.RawMessage(`{"GE1":{"mode":"access","profile_id":"abc"}}`)
	var exported = RemapPortFollows(ports, PortRefByID, PortRefByName,
		map[string]string{"abc": "Bureaux"})
	var imported = RemapPortFollows(exported, PortRefByName, PortRefByID,
		map[string]string{"Bureaux": "zzz"})
	var root map[string]map[string]any
	if json.Unmarshal(imported, &root) != nil {
		t.Fatalf("broken JSON: %s", imported)
	}
	if root["GE1"][PortRefByID] != "zzz" {
		t.Errorf("destination instance id was not set: %v", root["GE1"])
	}
	var _, remaining = root["GE1"][PortRefByName]
	if remaining {
		t.Errorf("name should have disappeared: %v", root["GE1"])
	}
}

// TestUnknownPortFollowIsDropped : the deliberate OPPOSITE of the rule
// remap. A dangling reference in a rule is flagged on screen and blocks that
// profile — leaving it is how it gets fixed. A dangling follow on a port has
// no such screen: the panel would read it as "this port's group was deleted",
// which on a fresh site is a lie. So the reference is dropped, on both
// directions, and a stale id from an older-format export is purged the same
// way on import.
func TestUnknownPortFollowIsDropped(t *testing.T) {
	// Export: the port followed a profile deleted since — no name to carry.
	var out = RemapPortFollows(
		json.RawMessage(`{"GE1":{"mode":"access","profile_id":"gone"}}`),
		PortRefByID, PortRefByName, map[string]string{})
	if strings.Contains(string(out), "gone") || strings.Contains(string(out), `"profile"`) {
		t.Errorf("unknown follow must be dropped on export: %s", out)
	}
	if !strings.Contains(string(out), `"mode":"access"`) {
		t.Errorf("the port itself must survive: %s", out)
	}
	// Import: name absent on the destination site.
	out = RemapPortFollows(
		json.RawMessage(`{"GE1":{"profile":"Unknown"}}`),
		PortRefByName, PortRefByID, map[string]string{})
	if strings.Contains(string(out), "Unknown") || strings.Contains(string(out), "profile_id") {
		t.Errorf("unknown follow must be dropped on import: %s", out)
	}
	// Import of an OLDER export that still ships raw uuids: purged, not kept
	// dangling.
	out = RemapPortFollows(
		json.RawMessage(`{"GE1":{"mode":"trunk","profile_id":"old-uuid"}}`),
		PortRefByName, PortRefByID, map[string]string{"Bureaux": "zzz"})
	if strings.Contains(string(out), "old-uuid") {
		t.Errorf("stale id must be purged on import: %s", out)
	}
}

// TestRemapFollowsToleratesPortsThatAreNot : empty, unreadable, or
// non-object entries pass through unharmed — import must not fail on a device
// whose ports nobody has configured yet.
func TestRemapFollowsToleratesPortsThatAreNot(t *testing.T) {
	var cas []json.RawMessage
	cas = append(cas, nil, json.RawMessage(`{}`), json.RawMessage(`not json`),
		json.RawMessage(`{"GE1":5}`), json.RawMessage(`{"GE1":{"mode":"access"}}`))
	var raw json.RawMessage
	for _, raw = range cas {
		var out = RemapPortFollows(raw, PortRefByID, PortRefByName, map[string]string{"a": "b"})
		if string(out) != string(raw) {
			t.Errorf("ports %q modified to %q", raw, out)
		}
	}
	// A null follow is an older writer's noise: cleared, without inventing a
	// reference.
	var out = RemapPortFollows(
		json.RawMessage(`{"GE1":{"mode":"access","profile_id":null}}`),
		PortRefByID, PortRefByName, map[string]string{"a": "b"})
	if strings.Contains(string(out), "profile") {
		t.Errorf("null follow must be cleared: %s", out)
	}
}

// TestRemapToleratesASpecThatIsNotOne : an empty, unreadable, or rule-less spec
// passes through unharmed. Import must not fail on a profile whose rules nobody
// has written yet.
func TestRemapToleratesASpecThatIsNotOne(t *testing.T) {
	var cas []json.RawMessage
	cas = append(cas, nil, json.RawMessage(`{}`), json.RawMessage(`not json`),
		json.RawMessage(`{"rules":"anything"}`))
	var raw json.RawMessage
	for _, raw = range cas {
		var out = RemapSwitchRules(raw, RefByID, RefByName, map[string]string{"a": "b"})
		if string(out) != string(raw) {
			t.Errorf("spec %q modified to %q", raw, out)
		}
	}
}
