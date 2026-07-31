package siteconfig

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRemapConserveLesChampsInconnus : a file exported by a newer version must
// not return stripped of what it knew how to do. Translation works on raw JSON
// precisely for that — a round trip through a struct would drop every undeclared
// field, silently.
func TestRemapConserveLesChampsInconnus(t *testing.T) {
	var spec = json.RawMessage(`{
      "description":"floor",
      "champ_du_futur":{"a":1},
      "rules":[{"label":"uplink","port_profile_id":"u-1",
                "select":{"media":"sfp_plus"},"option_inconnue":true}]}`)
	var out = RemapSwitchRules(spec, RefByID, RefByName, map[string]string{"u-1": "Uplink"})
	var s = string(out)
	if !strings.Contains(s, "champ_du_futur") || !strings.Contains(s, "option_inconnue") {
		t.Errorf("unknown fields lost: %s", s)
	}
	if !strings.Contains(s, `"port_profile":"Uplink"`) {
		t.Errorf("reference not translated: %s", s)
	}
	if strings.Contains(s, "port_profile_id") {
		t.Errorf("old key remains: %s", s)
	}
}

// TestRemapEstReversible : export then import must return the starting spec. If
// forward and back did not match, configuration would take a trip through a file
// and not return identical.
func TestRemapEstReversible(t *testing.T) {
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
	var _, resta = rule[RefByName]
	if resta {
		t.Errorf("name should have disappeared: %v", rule)
	}
}

// TestRemapLaisseUneReferenceInconnueEnPlace : erasing a reference we cannot
// translate would produce a rule with no configuration, therefore a port that
// silently receives what the next rule gives it. Leaving it flags it on screen as
// a faulty rule, which is fixable.
func TestRemapLaisseUneReferenceInconnueEnPlace(t *testing.T) {
	var spec = json.RawMessage(`{"rules":[{"label":"u","port_profile_id":"disparu"}]}`)
	var out = RemapSwitchRules(spec, RefByID, RefByName, map[string]string{})
	if !strings.Contains(string(out), `"port_profile_id":"disparu"`) {
		t.Errorf("unknown reference erased: %s", out)
	}
}

// TestRemapToleUneSpecQuiNEnEstPasUne : an empty, unreadable, or rule-less spec
// passes through unharmed. Import must not fail on a profile whose rules nobody
// has written yet.
func TestRemapToleUneSpecQuiNEnEstPasUne(t *testing.T) {
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
