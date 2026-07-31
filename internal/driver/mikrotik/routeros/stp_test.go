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
