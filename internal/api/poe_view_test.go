package api

import (
	"encoding/json"
	"testing"

	"meshcraft/internal/model"
)

// poeObservedOf must answer nil for every "nothing to show" shape — absent
// key, empty blob, bare {} — and a parsed state only when the observation
// actually carries something. A bare {} shown as "no PoE anywhere on this
// device" would be the exact lie the guard exists to prevent (same contract
// as stpObservedOf, one feature over).
func TestPoEObservedOfGuards(t *testing.T) {
	var cases = map[string]json.RawMessage{
		"no extra":     nil,
		"no key":       json.RawMessage(`{"driver":"ubiquiti-edgeswitch"}`),
		"null value":   json.RawMessage(`{"poe_observed":null}`),
		"empty object": json.RawMessage(`{"poe_observed":{}}`),
		"unreadable":   json.RawMessage(`{"poe_observed":"not an object"}`),
	}
	var name string
	var extra json.RawMessage
	for name, extra = range cases {
		var got = poeObservedOf(model.Device{Extra: extra})
		if got != nil {
			t.Errorf("%s: expected nil, got %+v", name, got)
		}
	}
}

func TestPoEObservedOfParses(t *testing.T) {
	var d = model.Device{Extra: json.RawMessage(`{"poe_observed":{
		"ports":[
			{"port":"GE1","capable":false,"delivering":false},
			{"port":"GE5","capable":true,"role":"out","class":"54v",
			 "delivering":true,"power_w":6.5,"negotiated_class":"2"}
		],
		"notes":["read on 2026-08-05"]}}`)}
	var st = poeObservedOf(d)
	if st == nil {
		t.Fatal("expected a parsed PoEState, got nil")
	}
	if len(st.Ports) != 2 {
		t.Fatalf("expected 2 ports, got %d", len(st.Ports))
	}
	if st.Ports[0].Capable || !st.Ports[1].Capable {
		t.Errorf("capability flags lost: %+v", st.Ports)
	}
	if st.Ports[1].Class != "54v" || st.Ports[1].PowerW != 6.5 {
		t.Errorf("class/draw lost on GE5: %+v", st.Ports[1])
	}
}

// The apply endpoint accepts EXPLICIT classes only: the empty class is the
// automatic profile-driven path's private vocabulary (cf. driver.PoEPortIntent)
// and must be refused at the HTTP boundary, along with anything unknown.
func TestValidPoEClass(t *testing.T) {
	var accepted = []string{"none", "24v", "54v"}
	var c string
	for _, c = range accepted {
		if !validPoEClass(c) {
			t.Errorf("class %q should be accepted", c)
		}
	}
	var refused = []string{"", "auto", "passive24v", "54V", "on", "off"}
	for _, c = range refused {
		if validPoEClass(c) {
			t.Errorf("class %q should be refused", c)
		}
	}
}

// The event line is read months later by somebody hunting the moment a device
// went dark, so the two directions must not look alike in a history: cutting
// power is a warning, offering it is ordinary. Same rule, and the same reason,
// as the shut / un-shut event next door (portadmin_apply.go).
func TestPoEApplySeverityFollowsTheDirection(t *testing.T) {
	var cases = map[string]struct {
		ports map[string]string
		want  int
	}{
		"a single cut": {map[string]string{"0/5": "none"}, model.SevWarning},
		"all on":       {map[string]string{"0/5": "54v", "0/6": "24v"}, model.SevInfo},
		"one cut among many": {
			map[string]string{"0/5": "54v", "0/6": "none", "0/7": "24v"},
			model.SevWarning,
		},
		"nothing asked": {map[string]string{}, model.SevInfo},
	}
	var name string
	for name = range cases {
		var got = poeApplySeverity(cases[name].ports)
		if got != cases[name].want {
			t.Errorf("%s: severity = %d, want %d", name, got, cases[name].want)
		}
	}
}

// Sorted and spelled out: two identical gestures must produce two identical
// lines, and the class must be readable without opening the code.
func TestPoEApplySummaryIsSortedAndNamesEveryClass(t *testing.T) {
	var got = poeApplySummary(map[string]string{
		"0/7": "24v", "0/5": "54v", "0/6": "none",
	})
	var want = "0/5 → 54v, 0/6 → none, 0/7 → 24v"
	if got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
	if poeApplySummary(map[string]string{}) != "" {
		t.Errorf("an empty request must summarize to an empty string")
	}
}
