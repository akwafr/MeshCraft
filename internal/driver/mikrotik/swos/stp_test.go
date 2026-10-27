package swos

import (
	"fmt"
	"os"
	"path/filepath"
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

// mac/rmac must use DecodeMAC, not Text(): Text hex-decodes ASCII and turns a
// CSS326 root MAC into binary junk (replacement chars / garbage in the panel).
func TestSysBridgeIDsUseDecodeMAC(t *testing.T) {
	var body, err = os.ReadFile(filepath.Join("testdata", "sys.b"))
	if err != nil {
		t.Fatal(err)
	}
	var sys *Page
	sys, err = ParseObject(string(body))
	if err != nil {
		t.Fatal(err)
	}

	var rootPriority, bridgePriority int
	var rpr, rprOK = sys.Num("rpr")
	if rprOK {
		rootPriority = int(rpr)
	}
	var prio, prioOK = sys.Num("prio")
	if prioOK {
		bridgePriority = int(prio)
	}

	var rootID, bridgeID string
	var rmacV, rmacOK = sys.Value("rmac")
	if rmacOK && rmacV.Kind == KindStr {
		var mac, macOK = DecodeMAC(rmacV.Raw)
		if macOK {
			rootID = mac
			if rootPriority > 0 {
				rootID = fmt.Sprintf("%04x.%s", rootPriority, rootID)
			}
		}
	}
	var macV, macValOK = sys.Value(FieldMAC)
	if macValOK && macV.Kind == KindStr {
		var mac, macOK = DecodeMAC(macV.Raw)
		if macOK {
			bridgeID = mac
			if bridgePriority > 0 {
				bridgeID = fmt.Sprintf("%04x.%s", bridgePriority, bridgeID)
			}
		}
	}

	if rootID != "1000.04:f4:1c:1d:74:58" {
		t.Fatalf("RootBridgeID = %q, want 1000.04:f4:1c:1d:74:58", rootID)
	}
	if bridgeID != "8000.b8:69:f4:5f:14:a8" {
		t.Fatalf("BridgeID = %q, want 8000.b8:69:f4:5f:14:a8", bridgeID)
	}

	// Prove the old bug: Text() yields a 6-byte binary blob containing 't'+'X'
	// (0x74 0x58) — what showed up as garbage / ".tsx"-like text in the panel.
	var bad, ok = sys.Text("rmac")
	if !ok || len(bad) != 6 {
		t.Fatalf("expected Text(rmac) binary len 6, got %q ok=%v", bad, ok)
	}
	if bad[4] != 't' || bad[5] != 'X' {
		t.Fatalf("fixture rmac Text() suffix = %q, want tX", bad[4:])
	}
}
