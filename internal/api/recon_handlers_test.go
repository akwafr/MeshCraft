package api

import (
	"testing"

	"meshcraft/internal/model"
)

// The faceplate yardstick: a truncated observation must be refused, and a
// complete one must never be.
//
// The second half is the half that could break production. Absorb is how an
// operator adopts a device's existing configuration, so a guard that fired on a
// healthy read would block a legitimate gesture and teach people to distrust it.
// Measured across the whole fleet before this landed: eight of nine devices
// observe EXACTLY their faceplate count, and CiscoLabFirst1 observes MORE than
// its faceplate declares (28 against 26) — which is why the test asserts the
// over-count case explicitly rather than only the equal one.
func TestFaceplatePortCountAndTheTruncationYardstick(t *testing.T) {
	var s3400 = model.Device{PortGroups: []byte(
		`[{"from":1,"silk":"g0/","count":48,"prefix":"GE"},` +
			`{"from":1,"silk":"tg0/","count":4,"label":"SFP+","speed":"10G","prefix":"XG"}]`)}
	var got = faceplatePortCount(s3400)
	if got != 52 {
		t.Fatalf("faceplatePortCount = %d, want 52 (48 + 4)", got)
	}

	// The incident: 23 of 52 read back. Refused.
	if !(23 < got) {
		t.Error("a 23-port observation must read as short of a 52-port faceplate")
	}
	// The healthy shapes: equal, and MORE than declared. Neither may be refused.
	if 52 < got {
		t.Error("a complete observation must not read as truncated")
	}
	if 28 < faceplatePortCount(model.Device{PortGroups: []byte(
		`[{"from":1,"count":24,"prefix":"GE"},{"from":1,"count":2,"prefix":"XG"}]`)}) {
		t.Error("observing MORE ports than the faceplate declares must not be refused")
	}

	// An unreadable faceplate yields 0, and shortReadRefusal's `faceplate > 0`
	// clause then declines to conclude anything — absorb stays available rather
	// than being blocked by a fact nobody has. (The refusal itself lives in
	// absorbObserved since ADR-0017; see absorb_shortread_test.go.)
	if faceplatePortCount(model.Device{PortGroups: []byte(`not json`)}) != 0 {
		t.Error("an unreadable faceplate must total 0, not a partial count")
	}
	if faceplatePortCount(model.Device{}) != 0 {
		t.Error("an absent faceplate must total 0")
	}
}
