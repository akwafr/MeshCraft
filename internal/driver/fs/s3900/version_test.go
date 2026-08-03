package s3900

import (
	"strings"
	"testing"
)

// These tests exist because one keyword moved between two firmware revisions and
// nothing in the product would have noticed: a refused VLAN creation surfaces as
// every port that needed that VLAN landing in Skipped, with the real cause one
// line further up.
//
// The two captures they encode are BOTH from lab hardware:
//
//	OpCode 1.7.0 — "vlan N name X"                 → % Incomplete command
//	               "vlan N name X media ethernet"  → accepted
//	OpCode 1.7.7 — "vlan N name X"                 → accepted (VLAN created)
//	               "vlan N name X media ethernet"  → % Invalid input at '^' (on "media")

func TestVlanCreateFormForEachFirmware(t *testing.T) {
	var missing = []VLAN{{ID: 100, Name: "Servers"}}

	var bare = vlanCreateCmds(missing, false)
	if strings.Join(bare, "|") != "vlan database|vlan 100 name Servers|exit" {
		t.Errorf("bare form = %v", bare)
	}
	var media = vlanCreateCmds(missing, true)
	if !strings.Contains(strings.Join(media, "|"), "media ethernet") {
		t.Errorf("media form = %v, want the keyword the 1.7.0 CLI requires", media)
	}
}

// THE REGRESSION THIS PREVENTS: 1.7.7 must not be sent "media ethernet" first.
func TestNewFirmwareTriesTheBareFormFirst(t *testing.T) {
	var alts = vlanCreateCandidates([]VLAN{{ID: 42}}, versionFacts{OpCode: "1.7.7"})
	if len(alts) != 2 {
		t.Fatalf("got %d candidate forms, want both", len(alts))
	}
	if strings.Contains(strings.Join(alts[0], " "), "media ethernet") {
		t.Error("1.7.7 is offered « media ethernet » first, the form it rejects")
	}
	// The other form must still be there: the gate orders, it does not exclude.
	if !strings.Contains(strings.Join(alts[1], " "), "media ethernet") {
		t.Error("the 1.7.0 form was dropped entirely — a mixed fleet needs it")
	}
}

func TestOldFirmwareTriesTheMediaFormFirst(t *testing.T) {
	var alts = vlanCreateCandidates([]VLAN{{ID: 42}}, versionFacts{OpCode: "1.7.0"})
	if !strings.Contains(strings.Join(alts[0], " "), "media ethernet") {
		t.Error("1.7.0 is not offered the keyword it requires first")
	}
	if strings.Contains(strings.Join(alts[1], " "), "media ethernet") {
		t.Error("the bare fallback is missing for 1.7.0")
	}
}

// An unreadable version leans NEW, because the survey that produced the old form
// is itself the older fact — and the fallback covers being wrong.
func TestUnknownVersionLeansNew(t *testing.T) {
	if !wantsBareVlanCreate(versionFacts{}) {
		t.Error("a device with no readable version should try the newer form first")
	}
	if !wantsBareVlanCreate(versionFacts{OpCode: "wat"}) {
		t.Error("an unparseable version should try the newer form first")
	}
}

// NUMERIC, not lexicographic. "1.7.10" is newer than "1.7.7" and sorts BEFORE it
// as text: a string compare would silently regress the newest switches in a
// fleet back onto the removed keyword.
func TestOpCodeComparisonIsNumeric(t *testing.T) {
	if compareOpCode("1.7.10", "1.7.7") <= 0 {
		t.Error("1.7.10 compared as older than 1.7.7 — lexicographic, not numeric")
	}
	if !wantsBareVlanCreate(versionFacts{OpCode: "1.7.10"}) {
		t.Error("1.7.10 must use the new form")
	}
	if !wantsBareVlanCreate(versionFacts{OpCode: "1.8.0"}) {
		t.Error("1.8.0 must use the new form")
	}
	if wantsBareVlanCreate(versionFacts{OpCode: "1.7.6"}) {
		t.Error("1.7.6 is below the captured boundary and must use the old form")
	}
	if wantsBareVlanCreate(versionFacts{OpCode: "1.6.9"}) {
		t.Error("1.6.9 must use the old form")
	}
	// Decorated revision strings must not defeat the comparison.
	if !wantsBareVlanCreate(versionFacts{OpCode: "1.7.7-rc2"}) {
		t.Error("a decorated 1.7.7 was not recognised")
	}
	if compareOpCode("1.7.7", "1.7.7") != 0 {
		t.Error("equal versions did not compare equal")
	}
}

// The version actually running in the lab, read from the device today.
func TestLabFirmwareIsRecognised(t *testing.T) {
	var facts = parseShowVersion(showVersion177)
	if facts.OpCode != "1.7.7" {
		t.Fatalf("OpCode = %q, want 1.7.7 from the live capture", facts.OpCode)
	}
	if facts.Serial != "CG2002249511N0273" {
		t.Errorf("serial = %q", facts.Serial)
	}
	if !wantsBareVlanCreate(facts) {
		t.Error("the lab device would be sent the keyword its CLI rejects")
	}
}

// Captured from the lab S3900-24T4S on 2026-08-02, after the upgrade.
const showVersion177 = `
 Unit 1
  Serial Number                    : CG2002249511N0273
  Hardware Version                 : R01A
  Number of Ports                  : 28
  Loader Version                   : 1.0.0.7
  Operation Code Version           : 1.7.7
`
