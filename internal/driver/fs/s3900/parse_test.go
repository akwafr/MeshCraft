package s3900

// Parsers are tested against the EXACT outputs of the 2026-08-01/02 serial
// session (OpCode 1.7.0). A firmware that changes the format breaks here, and
// that is intended: the post-upgrade dialect is a different command set.

import (
	"testing"

	"meshcraft/internal/driver"
)

const labBrief = `Interface Type         Admin  Link-Status  Negotiation  Speed/Duplex   Group
------------------------------------------------------------------------- -----
Eth 1/ 1  1000BASE-T   Up      Down          Auto                       None
Eth 1/ 2  1000BASE-T   Up      Down          Auto                       None
Eth 1/24  1000BASE-T   Up      Down          Auto                       None
Eth 1/25  10GBASE SFP+ Up      Down          Disable       10Gfull      None
Eth 1/26  10GBASE SFP+ Up      Down          Disable       10Gfull      None
Eth 1/27  10GBASE SFP+ Up      Down          Disable       10Gfull      None
Eth 1/28  10GBASE SFP+ Up      Up            Disable       10Gfull      None
`

const labRun = `Building running configuration. Please wait...
!<Version>0.0.1</Version>
!<stackingDB>000000000000</stackingDB>
!
snmp-server community public ro
snmp-server community private rw
!
enable password 7 79e20c4d5f10d980316c6163dea26765
!
hostname test-fs-s3900
!
vlan database
 VLAN 1 name DefaultVlan media ethernet
 VLAN 99 name LAN99_Management media ethernet
!
interface ethernet 1/1

!
interface ethernet 1/25
 no negotiation
 switchport mode trunk
 switchport trunk allowed vlan add 99,100
!
interface ethernet 1/28
 no negotiation
!
interface vlan 1
 ip add dhcp
!
interface vlan 99
 ip add 192.0.2.240 255.255.0.0
!
interface craft
 ip add 192.168.1.1 255.255.255.0
!
dns name-server 10.100.0.1
dns domain-lookup
!
ip route 0.0.0.0 0.0.0.0 192.0.2.1
!
ip ssh server enable
!
username admin privilege 15
username admin password 7 aabbccddeeff00112233445566778899
!
end
`

const labVersion = `Unit 1
 Serial Number:          S3900ABC12345
 Hardware Version:       R01
 Number of Ports:        28
 Main Power Status:      Up
Agent (Master)
 Unit 1
 Loader Version:         1.0.0.2
 Boot ROM Version:       1.0.0.5
 Operation Code Version: 1.7.0.20
`

func TestParseShowVersion(t *testing.T) {
	var f = parseShowVersion(labVersion)
	if f.Serial != "S3900ABC12345" {
		t.Errorf("serial=%q", f.Serial)
	}
	if f.OpCode != "1.7.0.20" {
		t.Errorf("opcode=%q", f.OpCode)
	}
	if f.Ports != 28 {
		t.Errorf("ports=%d", f.Ports)
	}
}

func TestParseInterfacesBrief(t *testing.T) {
	var ports = parseInterfacesBrief(labBrief)
	if len(ports) != 7 {
		t.Fatalf("ports=%d (%v)", len(ports), ports)
	}
	// "Eth 1/ 1" (right-padded number) must normalize to the standard-model name.
	if ports[0].Name != "GE1" || ports[0].Num != 1 || ports[0].LinkUp {
		t.Errorf("port[0]=%+v", ports[0])
	}
	var last = ports[len(ports)-1]
	if last.Name != "GE28" || last.Num != 28 || !last.LinkUp || last.SpeedMbps != 10000 {
		t.Errorf("port 28=%+v", last)
	}
}

func TestStandardPortName(t *testing.T) {
	var d = &Driver{}
	var cases = []struct {
		in   string
		want string
		ok   bool
	}{
		{"ethernet 1/11", "GE11", true},
		{"Eth 1/11", "GE11", true},
		{"eth1/1", "GE1", true},
		// SNMP ifDescr and LLDP local-port spellings: this is what makes port
		// link state and LLDP neighbors resolvable on a numbering connector
		// (cf. facade.resolve step 4).
		{"Ethernet1/11", "GE11", true},
		{"1/11", "GE11", true},
		// The SNMP agent's ifDescr, captured on OpCode 1.7.0 (2026-08-02).
		// Without this form every port reads as down and every LLDP neighbor
		// is dropped.
		{"Ethernet Port on unit 1, port 11", "GE11", true},
		{"Ethernet Port on unit 1, port 28", "GE28", true},
		{"Ethernet Port on unit 2, port 3", "", false}, // stacking unit: no standard name
		// Other interfaces the same walk returns: never faceplate ports.
		{"Console port", "", false},
		{"Loopback Interface 0", "", false},
		{"VLAN ID 0001", "", false},
		{"ethernet 2/5", "", false}, // stacking unit 2: outside coverage, never guessed
		{"vlan 99", "", false},
		{"Vlan1/11", "", false}, // not an ethernet port: must never be renamed to one
		{"craft", "", false},
	}
	var c struct {
		in   string
		want string
		ok   bool
	}
	for _, c = range cases {
		var got, ok = d.StandardPortName(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("StandardPortName(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestCLIPortNameRoundTrip(t *testing.T) {
	var d = &Driver{}
	var i int
	for i = 1; i <= 28; i++ {
		var std, _ = d.StandardPortName(cliPortName(standardPortName(1, i)))
		if std != standardPortName(1, i) {
			t.Errorf("round trip broke for unit 1 port %d: got %q", i, std)
		}
	}
	if cliPortName("not-a-standard-name") != "" {
		t.Error("a name outside the GEn scheme must translate to \"\", never a guess")
	}
}

func TestParseRunningConfig(t *testing.T) {
	var rc = parseRunningConfig(labRun, "")
	if rc.Hostname != "test-fs-s3900" {
		t.Errorf("hostname=%q", rc.Hostname)
	}
	if len(rc.VLANs) != 2 || rc.VLANs[1].ID != 99 || rc.VLANs[1].Name != "LAN99_Management" {
		t.Fatalf("vlans=%v", rc.VLANs)
	}

	var trunk, ok = rc.Ports["GE25"]
	if !ok || trunk.Mode != "trunk" {
		t.Fatalf("GE25=%+v", trunk)
	}
	if len(trunk.Tagged) != 2 || trunk.Tagged[0] != 99 || trunk.Tagged[1] != 100 {
		t.Errorf("tagged=%v (must be sorted)", trunk.Tagged)
	}
	if trunk.PVID != 1 {
		t.Errorf("trunk PVID=%d (default 1 expected)", trunk.PVID)
	}

	var plain = rc.Ports["GE1"]
	if plain.Mode != "access" || plain.PVID != 1 {
		t.Errorf("factory-default port=%+v", plain)
	}

	// The static SVI wins over the DHCP one: it is the one meshcraft pushed.
	if rc.MgmtVLAN != 99 {
		t.Errorf("mgmt vlan=%d", rc.MgmtVLAN)
	}
	if rc.Mgmt.Mode != "static" || rc.Mgmt.Address != "192.0.2.240/16" {
		t.Errorf("mgmt=%+v", rc.Mgmt)
	}
	if rc.Mgmt.Gateway != "192.0.2.1" {
		t.Errorf("gateway=%q", rc.Mgmt.Gateway)
	}
	if len(rc.Mgmt.DNS) != 1 || rc.Mgmt.DNS[0] != "10.100.0.1" {
		t.Errorf("dns=%v", rc.Mgmt.DNS)
	}

	if rc.Communities["public"] != "ro" || rc.Communities["private"] != "rw" {
		t.Errorf("communities=%v", rc.Communities)
	}
}

// TestAccountsMergeTheTwoLinesThisPlatformWritesPerAccount.
//
// The lab capture (OpCode 1.7.0, pinned in labRun) spreads ONE account over TWO
// "username" lines — which is also the shape EnsureAccount writes:
//
//	username admin privilege 15
//	username admin password 7 aabbccddeeff00112233445566778899
//
// An enumeration that kept the first row per name and moved on would report
// `admin` as having no stated privilege while the configuration states it
// plainly one line away. They are merged, and the read stays the single
// "show run" it always was.
func TestAccountsMergeTheTwoLinesThisPlatformWritesPerAccount(t *testing.T) {
	var got = parseRunAccounts(labRun)
	if len(got) != 1 {
		t.Fatalf("two lines are ONE account: %+v", got)
	}
	if got[0].Name != "admin" || got[0].Privilege != 15 ||
		got[0].Authority != driver.AuthorityFull {
		t.Errorf("account = %+v, want admin at privilege 15", got[0])
	}
	// The merge must work in the order the device prints, and in the other one:
	// a firmware that emitted the password line first must not lose the rank.
	var swapped = parseRunAccounts("username bob password 7 aabb\nusername bob privilege 15\n")
	if len(swapped) != 1 || swapped[0].Privilege != 15 {
		t.Errorf("swapped = %+v — the row that states the rank wins whichever "+
			"order it arrives in", swapped)
	}
}

// An account the configuration never ranks reads UNKNOWN — not 0, which is a
// real level on this CLI, and not the documented default either.
func TestAccountWithoutAPrivilegeLineReadsUnknown(t *testing.T) {
	var got = parseRunAccounts("username lecteur password 7 aabbcc\n")
	if len(got) != 1 {
		t.Fatalf("accounts = %+v", got)
	}
	if got[0].Privilege != driver.PrivilegeUnknown ||
		got[0].Authority != driver.AuthorityUnknown {
		t.Errorf("%+v: no \"privilege\" line anywhere, so NOT STATED", got[0])
	}
}

func TestPortGroupsFromBrief(t *testing.T) {
	// The faceplate as read from the device: port-count discovery. labBrief is
	// an excerpt (a gap between 1/2 and 1/24): contiguity must SPLIT groups
	// rather than draw sockets that do not exist.
	var groups = portGroupsOf(parseInterfacesBrief(labBrief))
	if len(groups) != 3 {
		t.Fatalf("groups=%v", groups)
	}
	if groups[2]["from"] != 25 || groups[2]["count"] != 4 ||
		groups[2]["speed"] != "10G" || groups[2]["label"] != "SFP+" {
		t.Errorf("SFP+ group=%v", groups[2])
	}
	// "prefix" is the STANDARD-MODEL name the backend manipulates; "silk" is
	// the hardware silkscreen the screen displays.
	if groups[0]["prefix"] != "GE" {
		t.Errorf("prefix=%v", groups[0]["prefix"])
	}
	if groups[0]["silk"] != "1/" {
		t.Errorf("silk=%v", groups[0]["silk"])
	}
}
