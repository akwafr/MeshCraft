package api

import (
	"testing"
	"time"
)

// The heal loop must neither hammer a broken device nor forget it: backoff
// doubles per failure, caps at a day, and clears on success (or restart —
// the state is deliberately in-memory so a NEW BINARY retries at once).
func TestSNMPHealBackoff(t *testing.T) {
	var h snmpHealState
	var t0 = time.Now()

	if !h.due("d1", t0) {
		t.Fatal("a device never attempted must be due")
	}
	h.failed("d1", t0)
	if h.due("d1", t0.Add(30*time.Minute)) {
		t.Error("30 min after a first failure: still cooling down (1h)")
	}
	if !h.due("d1", t0.Add(61*time.Minute)) {
		t.Error("after the first cooldown, the device is due again")
	}

	// Doubling, capped.
	var i int
	for i = 0; i < 10; i++ {
		h.failed("d1", t0)
	}
	if h.due("d1", t0.Add(23*time.Hour)) {
		t.Error("backoff must cap at 24h, not below")
	}
	if !h.due("d1", t0.Add(25*time.Hour)) {
		t.Error("even a chronically failing device is retried daily")
	}

	h.succeeded("d1")
	if !h.due("d1", t0) {
		t.Error("success clears the backoff entirely")
	}

	// Independence between devices.
	h.failed("d1", t0)
	if !h.due("d2", t0) {
		t.Error("one device's backoff must not delay another")
	}
}

func TestFaceplateEmpty(t *testing.T) {
	var cases = []struct {
		in   string
		want bool
	}{
		{``, true}, {`null`, true}, {`[]`, true}, {`{}`, true},
		{`[{"prefix":"ethernet 1/","count":24}]`, false},
		{`not json`, false}, // illisible mais non vide : ne pas écraser
	}
	var c struct {
		in   string
		want bool
	}
	for _, c = range cases {
		if faceplateEmpty([]byte(c.in)) != c.want {
			t.Errorf("faceplateEmpty(%q)=%v, want %v", c.in, !c.want, c.want)
		}
	}
}
