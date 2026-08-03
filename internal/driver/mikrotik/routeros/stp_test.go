package routeros

import (
	"testing"

	"meshcraft/internal/driver"
)

func TestMapROSRole(t *testing.T) {
	var cases = []struct {
		in   string
		want string
	}{
		{"root-port", driver.STPRoleRoot},
		{"designated-port", driver.STPRoleDesignated},
		{"alternate-port", driver.STPRoleAlternate},
		{"backup-port", driver.STPRoleBackup},
		{"disabled-port", driver.STPRoleDisabled},
		{"master-port", driver.STPRoleMaster},
		{"", ""},
		{"weird", ""},
	}
	var i int
	for i = 0; i < len(cases); i++ {
		var got = mapROSRole(cases[i].in)
		if got != cases[i].want {
			t.Fatalf("mapROSRole(%q) = %q, want %q", cases[i].in, got, cases[i].want)
		}
	}
}

func TestParseHexOrDec(t *testing.T) {
	var got = parseHexOrDec("0x8000")
	if got != 0x8000 {
		t.Fatalf("hex = %d", got)
	}
	got = parseHexOrDec("32768")
	if got != 32768 {
		t.Fatalf("dec = %d", got)
	}
	got = parseHexOrDec("")
	if got != 0 {
		t.Fatalf("empty = %d", got)
	}
}

func TestBridgeSTPFields(t *testing.T) {
	var prio = 32768
	var fields, skip = bridgeSTPFields(driver.SpanningTreePlan{Enabled: true, Mode: "rstp", BridgePriority: &prio})
	if skip != "" || fields["protocol-mode"] != "rstp" || fields["priority"] != "0x8000" {
		t.Fatalf("rstp: fields=%v skip=%q", fields, skip)
	}
	fields, skip = bridgeSTPFields(driver.SpanningTreePlan{Enabled: false})
	if skip != "" || fields["protocol-mode"] != "none" {
		t.Fatalf("none: fields=%v skip=%q", fields, skip)
	}
	fields, skip = bridgeSTPFields(driver.SpanningTreePlan{Enabled: true, Mode: "bogus"})
	if skip == "" || fields != nil {
		t.Fatalf("bogus should skip: fields=%v skip=%q", fields, skip)
	}
	var modes = []string{"stp", "mstp", "rstp-pvst"}
	var i int
	for i = range modes {
		fields, skip = bridgeSTPFields(driver.SpanningTreePlan{Enabled: true, Mode: modes[i]})
		if skip != "" {
			t.Fatalf("mode %q skip=%q", modes[i], skip)
		}
		var want = driver.NormalizeSTPMode(modes[i])
		if fields["protocol-mode"] != want {
			t.Fatalf("mode %q → %q, want %q", modes[i], fields["protocol-mode"], want)
		}
	}
}

func TestSTPModesCapability(t *testing.T) {
	var caps = (&Driver{}).Capabilities("CRS326-24G-2S+")
	var want = []string{"none", "stp", "rstp", "mstp"}
	if len(caps.STPModes) != len(want) {
		t.Fatalf("STPModes = %v, want %v", caps.STPModes, want)
	}
	var i int
	for i = range want {
		if caps.STPModes[i] != want[i] {
			t.Fatalf("STPModes = %v, want %v", caps.STPModes, want)
		}
	}
}
