package api

import (
	"encoding/json"
	"testing"

	"meshcraft/internal/driver"
)

func TestSTPPreferenceOfDefault(t *testing.T) {
	var got = stpPreferenceOf(nil)
	if len(got) != len(driver.DefaultSTPPreference) {
		t.Fatalf("default len=%d", len(got))
	}
}

func TestMergeSTPPreferenceKeepsZones(t *testing.T) {
	var before = json.RawMessage(`{"zones":[{"label":"A"}],"stp_mode_preference":["stp"]}`)
	var merged, err = mergeSTPPreference(before, []string{"mstp", "rstp-pvst", "none"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(merged, &m) != nil {
		t.Fatal("unmarshal")
	}
	if _, ok := m["zones"]; !ok {
		t.Fatal("zones dropped")
	}
	var pref = stpPreferenceOf(merged)
	if len(pref) != 3 || pref[0] != "mstp" || pref[1] != "rstp-pvst" || pref[2] != "none" {
		t.Fatalf("pref=%v", pref)
	}
}

func TestMergeSTPPreferenceAllowsEmpty(t *testing.T) {
	// Empty Preferred must persist so the operator can re-add STP from Available.
	var before = json.RawMessage(`{"stp_mode_preference":["stp","none"]}`)
	var merged, err = mergeSTPPreference(before, nil)
	if err != nil {
		t.Fatal(err)
	}
	var pref = stpPreferenceOf(merged)
	if len(pref) != 0 {
		t.Fatalf("empty pref coerced to %v", pref)
	}
}

func TestSTPPreferenceOfAbsentUsesDefault(t *testing.T) {
	var got = stpPreferenceOf(json.RawMessage(`{"zones":[]}`))
	if len(got) != len(driver.DefaultSTPPreference) {
		t.Fatalf("absent key: %v", got)
	}
}

func TestPortSTPIntents(t *testing.T) {
	var raw = json.RawMessage(`{
		"GE1":{"mode":"access","pvid":100,"stp":{"edge":true,"bpdu_guard":true}},
		"GE2":{"mode":"libre","stp":{"edge":true}},
		"GE3":{"mode":"trunk","stp":{"enabled":false,"edge":"auto"}},
		"GE4":{"mode":"access","stp":{"edge":"yes"}}
	}`)
	var got = portSTPIntents(raw)
	if len(got) != 3 {
		t.Fatalf("len=%d want 3 (libre skipped): %+v", len(got), got)
	}
	var by = map[string]driver.SpanningTreePortIntent{}
	var p driver.SpanningTreePortIntent
	for _, p = range got {
		by[p.Port] = p
	}
	if by["GE1"].Edge != driver.STPEdgeYes {
		t.Errorf("GE1 edge bool→yes = %q", by["GE1"].Edge)
	}
	if by["GE1"].BPDUGuard == nil || !*by["GE1"].BPDUGuard {
		t.Errorf("GE1 bpdu_guard = %+v", by["GE1"].BPDUGuard)
	}
	if by["GE3"].Edge != driver.STPEdgeAuto || by["GE3"].Enabled == nil || *by["GE3"].Enabled {
		t.Errorf("GE3 = %+v", by["GE3"])
	}
	if by["GE4"].Edge != driver.STPEdgeYes {
		t.Errorf("GE4 = %+v", by["GE4"])
	}
}
