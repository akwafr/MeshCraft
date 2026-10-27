package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"meshcraft/internal/driver"
	"meshcraft/internal/model"
)

// The refusal that must now be IMPOSSIBLE TO FORGET.
//
// Context, because a test asserting a count is unreadable without it. On
// 2026-10-24 sw01-arena — an FS S3400-48T4SP, faceplate 52 (48×GE + 4×XG 10G) —
// was adopted from discovery with 48 ports imported. The four 10G uplinks were
// absent from the model, the 48 were ALL `access` on a seven-VLAN switch, and
// the device read `sync: in_sync`: intent and observation came out of the same
// truncated read, so drift compared two equally wrong halves and found nothing.
// An apply would have treated the uplinks as unconfigured.
//
// The refusal existed — in handleImportDeviceConfig, with a comment in the
// header of absorbObserved telling any new caller to repeat it. importFromDevice
// was that new caller and did not. ADR-0017 moved the check into
// absorbObserved; these tests pin it there, on the function no caller can go
// around.

// portsObserved builds an observation of n ports, all access, which is the shape
// the incident produced.
func portsObserved(n int) observedState {
	var ports = map[string]driver.PortState{}
	var i int
	for i = 1; i <= n; i++ {
		ports["GE"+itoa(i)] = driver.PortState{Mode: "access", PVID: 1}
	}
	return observedState{Ports: ports}
}

// s3400 is sw01-arena's faceplate: 48 copper plus 4 SFP+, 52 jacks.
func s3400Faceplate() model.Device {
	return model.Device{PortGroups: []byte(
		`[{"from":1,"silk":"g0/","count":48,"prefix":"GE"},` +
			`{"from":1,"silk":"tg0/","count":4,"label":"SFP+","speed":"10G","prefix":"XG"}]`)}
}

// THE INCIDENT ITSELF: 48 read back against a faceplate of 52 must be refused,
// and the sentence must name all three numbers — what was reached, what exists,
// and what would have been deleted. An operator reading "48 of 52" goes looking
// for four ports; one reading "import failed" goes looking for a bug.
func TestShortReadRefusalRefusesTheAdoptionIncident(t *testing.T) {
	var refusal = shortReadRefusal(s3400Faceplate(), portsObserved(48))
	if refusal == nil {
		t.Fatal("48 ports observed against a 52-port faceplate must be refused: " +
			"this is the sw01-arena incident, and absorbing it deletes XG1-XG4")
	}
	var msg = refusal.Error()
	var want string
	for _, want = range []string{"48", "52", "4"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal sentence %q must name %q (reached / faceplate / lost)", msg, want)
		}
	}
}

// THE HALF THAT COULD BREAK PRODUCTION. A guard that fired on a healthy read
// would block a legitimate adoption and teach people to distrust it. Measured
// on the fleet before the first refusal landed: eight devices of nine observe
// EXACTLY their faceplate, and the ninth observes MORE — so both shapes are
// asserted, not just the equal one.
func TestShortReadRefusalLetsCompleteAndOverCompleteReadsThrough(t *testing.T) {
	if shortReadRefusal(s3400Faceplate(), portsObserved(52)) != nil {
		t.Error("a read covering exactly the faceplate must import normally")
	}
	if shortReadRefusal(s3400Faceplate(), portsObserved(56)) != nil {
		t.Error("observing MORE ports than the faceplate declares must not be refused: " +
			"CiscoLabFirst1 reports 28 against a declared 26, and it is healthy")
	}
}

// AN UNKNOWN FACEPLATE MUST NOT BLOCK ANYTHING. Every platform that declares no
// port group would otherwise lose the ability to import at all — a far larger
// regression than the bug being fixed.
func TestShortReadRefusalDoesNotBlockAnUnknownFaceplate(t *testing.T) {
	var noFaceplate = model.Device{}
	if shortReadRefusal(noFaceplate, portsObserved(6)) != nil {
		t.Error("faceplate 0 means \"we do not know\", and an unknown yardstick " +
			"must not refuse: it would block every platform that declares none")
	}
	if shortReadRefusal(model.Device{PortGroups: []byte(`not json`)}, portsObserved(6)) != nil {
		t.Error("an unreadable faceplate totals 0 and must behave like an absent one")
	}
}

// THE EMPTY READ, which is where an unknown faceplate still has one yardstick
// left: the intent already stored. A read that reached NO port, against a model
// that carries some, is an import declaring every jack unconfigured.
func TestShortReadRefusalOnAnEmptyRead(t *testing.T) {
	// With a faceplate, the ordinary clause covers it.
	if shortReadRefusal(s3400Faceplate(), observedState{}) == nil {
		t.Error("zero ports against a 52-port faceplate must be refused")
	}
	// Without one, the stored intent answers.
	var modelled = model.Device{Ports: []byte(
		`{"GE1":{"mode":"access"},"GE2":{"mode":"trunk"}}`)}
	var refusal = shortReadRefusal(modelled, observedState{})
	if refusal == nil {
		t.Fatal("a read covering no port, against a model that carries two, must be " +
			"refused even with no faceplate to measure against")
	}
	if !strings.Contains(refusal.Error(), "no port at all") {
		t.Errorf("the empty-read sentence must say the read reached nothing, got %q",
			refusal.Error())
	}
	// Nothing to lose: no faceplate, no read, no stored intent. Refusing here
	// would block a connector that legitimately reads VLANs and no port.
	if shortReadRefusal(model.Device{}, observedState{}) != nil {
		t.Error("an empty read over an empty model asserts nothing and must not be refused")
	}
	if shortReadRefusal(model.Device{Ports: []byte(`{}`)}, observedState{}) != nil {
		t.Error("an explicitly empty port model must behave like an absent one")
	}
}

// THE PROPERTY THAT MAKES THE MOVE WORTH IT: the refusal happens inside
// absorbObserved, before it reads or writes ANYTHING.
//
// The Store is nil on purpose. Every other path through absorbObserved begins
// with vlansMissingForDevice, which queries it — so a build where the check sat
// in the callers, or sat after the VLAN decision, fails this test instead of
// passing it quietly. It also pins the operational half: a refusal costs no
// query and leaves nothing half-done.
func TestAbsorbObservedRefusesWithoutTouchingTheStore(t *testing.T) {
	var s = &Server{}
	var ports, created, declined, err = s.absorbObserved(
		context.Background(), s3400Faceplate(), portsObserved(48))
	if err == nil {
		t.Fatal("absorbObserved must make the short-read refusal itself: a caller that " +
			"forgets it is exactly how sw01-arena was adopted with 48 of 52 ports")
	}
	var short *shortRead
	if !errors.As(err, &short) {
		t.Fatalf("the refusal must be a *shortRead the callers can render, got %T: %v", err, err)
	}
	if short.Observed != 48 || short.Faceplate != 52 {
		t.Errorf("refusal carries %d/%d, want 48/52", short.Observed, short.Faceplate)
	}
	// Nothing claimed, so no caller can report a partial success.
	if ports != 0 || created != nil || declined != "" {
		t.Errorf("a refusal must report no work done, got ports=%d created=%v declined=%q",
			ports, created, declined)
	}
}

// A COMPLETE READ STILL REACHES THE STORE, which is the only proof the guard is
// a guard and not a wall. With a nil Store the call panics at the first query —
// so recovering from that panic is how this test says "it went past the check"
// without standing up a database.
func TestAbsorbObservedLetsACompleteReadReachTheStore(t *testing.T) {
	var s = &Server{}
	var reached bool
	func() {
		defer func() {
			if recover() != nil {
				reached = true
			}
		}()
		var _, _, _, err = s.absorbObserved(
			context.Background(), s3400Faceplate(), portsObserved(52))
		var short *shortRead
		if errors.As(err, &short) {
			t.Error("a complete read must NOT be refused: the guard would be a wall")
		}
	}()
	if !reached {
		t.Error("a complete read must proceed into the absorb (it reaches the store)")
	}
}

// THE SENTENCES THEMSELVES, pinned because they are what an operator acts on,
// and because both callers compose their own ending onto the same Error().
func TestShortReadSentencesAreActionable(t *testing.T) {
	var truncated = &shortRead{Observed: 48, Faceplate: 52}
	var msg = truncated.Error()
	if !strings.Contains(msg, "would delete the configuration") {
		t.Errorf("the sentence must say what importing would COST, got %q", msg)
	}
	// 52-48: the number of ports the operator should go looking for.
	if !strings.Contains(msg, "the 4 it did not reach") {
		t.Errorf("the sentence must name how many ports were missed, got %q", msg)
	}
	var empty = &shortRead{Intent: 24}
	if !strings.Contains(empty.Error(), "24") {
		t.Errorf("the empty-read sentence must name the model's port count, got %q",
			empty.Error())
	}
}

// The yardstick can only be read from the device, never supplied: the faceplate
// is derived from `d.PortGroups`, which absorbObserved already has. A caller
// cannot pass a 0 to silence the check, because it is never asked for one.
//
// This test is a REGRESSION FENCE on the signature, not on behaviour: if
// absorbObserved ever grows a faceplate parameter, this stops compiling and
// whoever changed it reads ADR-0017 before deciding again.
func TestFaceplateIsDerivedFromTheDeviceNotSupplied(t *testing.T) {
	var derive func(model.Device, observedState) *shortRead = shortReadRefusal
	if derive(s3400Faceplate(), portsObserved(48)) == nil {
		t.Error("the refusal must be derivable from the device alone")
	}
	// And it really is the PortGroups column it reads, so a device whose model
	// nobody filled in cannot be blocked by a faceplate it never declared.
	var noGroups model.Device
	if json.Valid(noGroups.PortGroups) {
		t.Fatal("precondition: an unset PortGroups is not valid JSON")
	}
	if derive(noGroups, portsObserved(1)) != nil {
		t.Error("an unset faceplate column must not refuse")
	}
}
