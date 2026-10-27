package s3900

import (
	"testing"

	"meshcraft/internal/driver"
)

// The running-config shape after the LIVE create on s3900lab (2026-08-13):
// the group section plus the member's channel-group line.
const s3900RunWithLAG = `!
interface port-channel 1
 switchport mode trunk
 switchport trunk native vlan 99
 switchport trunk allowed vlan add 10
 switchport trunk allowed vlan add 42
!
interface ethernet 1/5
 channel-group 1
!
interface ethernet 1/6
 switchport mode access
!
interface vlan 99
 ip address 192.0.2.71 255.255.255.0
!`

func TestParseS3900LAGs(t *testing.T) {
	var lags = parseS3900LAGs(s3900RunWithLAG)
	if len(lags) != 1 {
		t.Fatalf("lags = %d, want 1", len(lags))
	}
	var l = lags[0]
	if l.Name != "LAG1" || l.Mode != driver.LAGModeStatic {
		t.Errorf("lag = %+v, want LAG1/static (the captured write's regime)", l)
	}
	if len(l.Members) != 1 || l.Members[0] != "GE5" {
		t.Errorf("members = %v, want [GE5]", l.Members)
	}
	if l.VLAN.Mode != "trunk" || l.VLAN.PVID != 99 || len(l.VLAN.Tagged) != 2 {
		t.Errorf("vlan half = %+v, want trunk/99/[10 42]", l.VLAN)
	}
}

// The empty config yields no aggregates — the fleet's ordinary state.
func TestParseS3900NoLAGs(t *testing.T) {
	var got = len(parseS3900LAGs("!\ninterface ethernet 1/1\n!\n"))
	if got != 0 {
		t.Errorf("lags = %d, want 0", got)
	}
}
