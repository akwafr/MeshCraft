package api

import (
	"encoding/json"
	"strings"
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
	var _, ok = m["zones"]
	if !ok {
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

// TestEmptiedPreferenceDoesNotResolveToTheDefault is the PER-SITE OFF SWITCH,
// end to end through the two functions that decide it.
//
// The bug it pins, live until 2026-08-09: stpPreferenceOf answered ABSENT and
// EMPTIED differently, exactly as its comment promised, and then
// driver.ResolveSTPMode substituted DefaultSTPPreference on `len == 0` and
// collapsed them again. An operator who cleared the site's Preferred list to
// stop meshcraft touching a mid-migration Catalyst's tree still got `rstp`
// resolved and pushed at the next apply.
//
// Both halves are asserted here, because either one alone is satisfiable
// wrongly: ABSENT must still give the product default (or every site that never
// opened the STP screen stops getting a tree), and EMPTIED must give nothing.
func TestEmptiedPreferenceDoesNotResolveToTheDefault(t *testing.T) {
	var caps = []string{"stp", "rstp", "mstp"}

	var emptied = stpPreferenceOf(json.RawMessage(`{"stp_mode_preference":[]}`))
	if len(emptied) != 0 {
		t.Fatalf("an explicitly empty list read back as %v", emptied)
	}
	var mode, disable, ok = driver.ResolveSTPMode(emptied, caps)
	if ok {
		t.Errorf("an EMPTIED site preference resolved to mode=%q disable=%v — the "+
			"off switch is gone and every apply will re-push a mode", mode, disable)
	}

	var absent = stpPreferenceOf(json.RawMessage(`{"zones":[]}`))
	mode, disable, ok = driver.ResolveSTPMode(absent, caps)
	if !ok || disable || mode != "rstp" {
		t.Errorf("an ABSENT preference gave mode=%q disable=%v ok=%v — a site that "+
			"never opened the STP screen must still get the product default",
			mode, disable, ok)
	}
}

// TestTheEmptyPreferenceNoteNamesEverythingItSuppresses guards the sentence an
// operator gets INSTEAD of a spanning tree.
//
// The off switch is one early return in applyDeviceSTP, and everything below it
// travels on one plan: the mode, the MST region, and SpanningTreePlan.Ports —
// portfast/edge, BPDU guard, path cost. Until 2026-08-11 the note said only
// "no mode, no MST region", so a site that emptied the list to protect one
// mid-migration Catalyst's global mode also, silently, stopped BPDU guard
// reaching its access ports and stopped its MST region being re-homogenised when
// the VLAN list moved. Neither shows up anywhere: a device evicted from its
// region keeps forwarding and keeps reading `ok`.
//
// The note is the ONLY place either fact is told, so it is asserted rather than
// trusted. Assertions are on the SUBSTANCE, not the wording — rephrase freely,
// but a rewrite that drops one of these has dropped the warning.
func TestTheEmptyPreferenceNoteNamesEverythingItSuppresses(t *testing.T) {
	var lower = strings.ToLower(stpPreferenceEmptyNote)
	var want = map[string]string{
		"bpdu guard":  "the per-port intents stop too, and BPDU guard is the one whose absence is dangerous and invisible",
		"path cost":   "per-port path cost rides the same plan",
		"mst region":  "the region stops being pushed at all",
		"re-homogen":  "and stops being RE-homogenised when the site VLAN list changes — the silent 802.1s eviction",
		"still green": "the eviction has no symptom: forwarding continues and the device reads ok",
	}
	var k, why string
	for k, why = range want {
		if !strings.Contains(lower, k) {
			t.Errorf("the empty-preference note no longer mentions %q — %s", k, why)
		}
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
