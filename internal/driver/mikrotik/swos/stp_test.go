package swos

import (
	"testing"

	"meshcraft/internal/driver"
)

func TestSwosRoleName(t *testing.T) {
	var cases = []struct {
		in   uint32
		want string
	}{
		{0, driver.STPRoleDisabled},
		{1, driver.STPRoleAlternate},
		{2, driver.STPRoleRoot},
		{3, driver.STPRoleDesignated},
		{4, driver.STPRoleBackup},
		{99, ""},
	}
	var i int
	for i = 0; i < len(cases); i++ {
		var got = swosRoleName(cases[i].in)
		if got != cases[i].want {
			t.Fatalf("swosRoleName(%d) = %q, want %q", cases[i].in, got, cases[i].want)
		}
	}
}

func TestBareMAC(t *testing.T) {
	var got = bareMAC("8000.aa:bb:cc:dd:ee:ff")
	if got != "aabbccddeeff" {
		t.Fatalf("bareMAC priority.id = %q", got)
	}
	got = bareMAC("AA:BB:CC:DD:EE:FF")
	if got != "aabbccddeeff" {
		t.Fatalf("bareMAC plain = %q", got)
	}
}
