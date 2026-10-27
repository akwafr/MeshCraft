package api

import (
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

// The live case behind this test: a 1930 whose site runs MSTP. Its connector
// cannot write the MST instance-to-VLAN mapping (InstanceEntry shape unknown
// on this range) and says so on every apply. Routed into Result.Skipped, that
// phrase was counted by the UI as a skipped PORT: the banner "Configuration
// applied, but 1 port was skipped" never went away, and Force — which only
// lifts the management-port guardrail of ApplyPorts — could not clear it.
// STP refusals are not force-able (SpanningTreePlan has no Force) and must
// land in notes, never in the force-able bucket.
func TestMergeSTPApplySkippedGoesToNotes(t *testing.T) {
	var out applyOutcome
	var stp = driver.ApplyResult{
		Skipped: []string{"MST instance-to-VLAN mapping NOT written: shape unknown on this range"},
		// A note the connector actually still emits. It used to be
		// `MSTP region name="site-lab2" revision=1`, dropped by ADR-0021's
		// companion cleanup as an ECHO of the request rather than a reading of the
		// device; this one names what the device HAD, which no step carries.
		Notes: []string{"MST instance 1 VLANs set to 10-11,42 (device had 42)"},
	}
	mergeSTPApply(&out, stp)

	if len(out.Result.Skipped) != 0 {
		t.Fatalf("STP refusals must not enter the force-able Skipped bucket, got %v",
			out.Result.Skipped)
	}
	if !containsSub(out.Result.Notes, "MST instance-to-VLAN") {
		t.Fatalf("STP refusal lost: Result.Notes = %v", out.Result.Notes)
	}
	if !containsSub(out.Result.Notes, "device had 42") {
		t.Fatalf("STP note lost: Result.Notes = %v", out.Result.Notes)
	}
	// Outcome notes too: on the ApplyPorts-error path, runApply returns before
	// rebuilding out.Notes from Result.Notes, and rememberApply reads out.Notes.
	if !containsSub(out.Notes, "MST instance-to-VLAN") || !containsSub(out.Notes, "device had 42") {
		t.Fatalf("outcome notes incomplete: %v", out.Notes)
	}
}

// Port skips decided by ApplyPorts — the actual guardrail — stay exactly where
// they were: force-able, counted by the banner, untouched by the STP merge.
func TestMergeSTPApplyLeavesPortSkipsAlone(t *testing.T) {
	var out applyOutcome
	out.Result.Skipped = []string{
		"GE7 not modified: it has an ACTIVE link and is an untagged member of VLAN 99",
	}
	mergeSTPApply(&out, driver.ApplyResult{
		Skipped: []string{"unsupported STP mode foo"},
	})

	if len(out.Result.Skipped) != 1 || !strings.Contains(out.Result.Skipped[0], "GE7") {
		t.Fatalf("port guardrail skip must survive the merge intact, got %v", out.Result.Skipped)
	}
	if !containsSub(out.Result.Notes, "unsupported STP mode") {
		t.Fatalf("STP refusal must still be told, Result.Notes = %v", out.Result.Notes)
	}
}

// containsSub reports whether any element contains the substring.
func containsSub(list []string, sub string) bool {
	var s string
	for _, s = range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
