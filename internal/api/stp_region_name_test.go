package api

import (
	"encoding/json"
	"testing"
)

// The MST region name decides whether two switches are in one region, and 802.1s
// decides it on name + revision + a digest over the whole VLAN-to-instance table,
// all three exactly. A device whose name differs has not been relabelled, it has
// LEFT the region — silently, still forwarding, still green (cf. ADR-0021).
//
// So the tests below are about one thing: that nothing in this product can rename
// a region as a side effect of something else.

// THE NO-RENAME GUARANTEE, and the reason migration 0047 exists.
//
// Every site that predates ADR-0021 was pinned to its then-current slug. For such
// a site the name this code returns must be the slug, byte for byte — which is
// exactly what the slug-derived version returned the day before. If this test ever
// fails, deploying renames live regions.
func TestAPinnedNameWinsOverTheDerivedOne(t *testing.T) {
	var plan = json.RawMessage(`{"stp_mst_region":"site-exec","stp_mst_revision":1}`)
	var got = stpRegionNameOf(plan, "7f0d238d-f180-440d-be56-cd1a4bcbb225")
	if got != "site-exec" {
		t.Fatalf("name = %q, want the pin %q — a pinned site must never be "+
			"renamed by a code change", got, "site-exec")
	}
}

// A site with no pin has no hardware carrying a region (it was created after the
// migration), so it gets the generic name derived from its immutable identifier.
func TestAnUnpinnedSiteGetsTheGenericName(t *testing.T) {
	var got = stpRegionNameOf(json.RawMessage(`{}`), "7f0d238d-f180-440d-be56-cd1a4bcbb225")
	if got != "mst-7f0d238df180" {
		t.Errorf("name = %q, want mst-7f0d238df180", got)
	}
	// An absent plan is the same case as an empty one: no pin.
	if stpRegionNameOf(nil, "7f0d238d-f180-440d-be56-cd1a4bcbb225") != "mst-7f0d238df180" {
		t.Error("a site with no plan at all must still get a region name")
	}
}

// An empty or whitespace pin is NOT a name. Treating it as one would write a
// region called "" — which Configured() refuses, so the apply would silently stop
// managing spanning tree on that site instead of falling back.
func TestAnEmptyPinFallsBackRatherThanNamingNothing(t *testing.T) {
	var cases = []string{
		`{"stp_mst_region":""}`,
		`{"stp_mst_region":"   "}`,
	}
	var c string
	for _, c = range cases {
		var got = stpRegionNameOf(json.RawMessage(c), "7f0d238d-f180-440d-be56-cd1a4bcbb225")
		if got != "mst-7f0d238df180" {
			t.Errorf("plan %s gave %q, want the derived name", c, got)
		}
	}
}

// Malformed JSON must not be read as "no pin". It is read as "no pin" for the
// NAME here only because the caller refuses to build a region at all when the
// plan read fails; what this asserts is that a broken plan never crashes and
// never invents a pin.
func TestABrokenPlanYieldsNoPin(t *testing.T) {
	if stpRegionPinOf(json.RawMessage(`{"stp_mst_region":`)) != "" {
		t.Error("truncated JSON produced a pin")
	}
	if stpRegionPinOf(json.RawMessage(`{"stp_mst_region":42}`)) != "" {
		t.Error("a non-string pin produced a pin")
	}
}

// The display endpoint has to tell a pinned name from a derived one, because the
// two carry different warnings: a pin is a decision somebody can unmake, a
// derived name cannot drift. Same key, two questions, two helpers.
func TestThePinIsDistinguishableFromTheDerivedName(t *testing.T) {
	if stpRegionPinOf(json.RawMessage(`{"stp_mst_region":"arena"}`)) != "arena" {
		t.Error("a set pin must be reported as set")
	}
	if stpRegionPinOf(json.RawMessage(`{"stp_mst_revision":3}`)) != "" {
		t.Error("a plan with only a revision must report no pin")
	}
}

// The revision is the other third of the identity and it keeps its own default.
// Pinning a name must not disturb it: a site that pinned a name and said nothing
// about the revision still runs revision 1, not 0 — 0 is what an untouched device
// carries, and the difference is how a meshcraft-managed region is recognisable.
func TestPinningANameLeavesTheRevisionAlone(t *testing.T) {
	var plan = json.RawMessage(`{"stp_mst_region":"site-exec"}`)
	if stpRegionRevisionOf(plan) != 1 {
		t.Errorf("revision = %d, want 1", stpRegionRevisionOf(plan))
	}
}
