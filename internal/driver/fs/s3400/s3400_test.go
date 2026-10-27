package s3400

import (
	"strings"
	"testing"

	"meshcraft/internal/driver"
)

func TestMgmtPushCmdsDocumentedForms(t *testing.T) {
	var cmds, _ = MgmtPushCmds(MgmtOpts{
		VlanID:         99,
		VlanName:       "LAN99_Management",
		IP:             "192.0.2.241",
		Mask:           "255.255.0.0",
		Gateway:        "192.0.2.1",
		Username:       "mcadmin",
		Password:       "Mc8e03481e067aZz9",
		EnablePassword: "Mc8e03481e067aZz9",
		Hostname:       "test-fs-s3400",
		RemoveVlan1:    true,
		TrunkPorts:     "range tg0/1-4",
	})
	var want = []string{
		"config",
		"hostname test-fs-s3400",
		"vlan 99",
		"name LAN99_Management",
		"exit",
		"interface vlan 99",
		"ip address 192.0.2.241 255.255.0.0",
		"exit",
		// Captured 2026-08-03: "ip route default" accepted in config mode.
		"ip route default 192.0.2.1",
		"ip sshd enable",
		// Both were briefly (and wrongly) removed as "refused": an SSH session
		// into the provisioned switch found them in its running configuration.
		"ip http server",
		// LLDP ships OFF on this platform; provisioning turns it on by default
		// or the topology view stays empty on a healthy switch.
		"lldp run",
		"interface range tg0/1-4",
		"switchport mode trunk",
		"switchport trunk vlan-allowed 1-4094",
		"exit",
		"no interface vlan 1",
		// Credentials LAST — a session dropped by the password change must find
		// everything else already pushed (S3900 deploy log 2026-08-01).
		"username mcadmin password Mc8e03481e067aZz9",
		"aaa authentication login default local",
		"enable password 0 Mc8e03481e067aZz9",
		// "write" is documented as valid from global config mode.
		"write",
	}
	if len(cmds) != len(want) {
		t.Fatalf("len=%d want %d\ncmds=%v", len(cmds), len(want), cmds)
	}
	var i int
	for i = 0; i < len(want); i++ {
		if cmds[i] != want[i] {
			t.Errorf("[%d] got %q want %q", i, cmds[i], want[i])
		}
	}
}

func TestMgmtPushCmdsVlan1KeepsItSimple(t *testing.T) {
	var cmds, _ = MgmtPushCmds(MgmtOpts{
		VlanID: 1,
		IP:     "192.168.1.10",
		Mask:   "255.255.255.0",
	})
	var joined = strings.Join(cmds, "\n")
	if strings.Contains(joined, "vlan 1\nname") {
		t.Error("VLAN 1 must not be re-created")
	}
	if strings.Contains(joined, "no interface vlan 1") {
		t.Error("the management SVI itself must never be removed")
	}
	if strings.Contains(joined, "switchport mode trunk") {
		t.Error("no trunk block without TrunkPorts")
	}
	if !strings.Contains(joined, "ip address 192.168.1.10 255.255.255.0") {
		t.Errorf("dotted-mask address form missing:\n%s", joined)
	}
}

func TestFactoryResetCmdsAreTheResetGuideOnes(t *testing.T) {
	var cmds = FactoryResetCmds()
	if len(cmds) != 2 || cmds[0] != "delete startup-config" || cmds[1] != "reboot" {
		t.Fatalf("cmds=%v", cmds)
	}
}

func TestParseUpPorts(t *testing.T) {
	// SFP+ cages are TGigaEthernet0/1-4 (captured 2026-08-03), copper is
	// GigaEthernet0/1-48 — the uplinks of a 48T4SP are the tg ports.
	var show = `
GigaEthernet0/1 is down, line protocol is down
GigaEthernet0/2 is up, line protocol is down
TGigaEthernet0/3 is up, line protocol is up
TGigaEthernet0/4 is up, line protocol is up
VLAN1 is up, line protocol is down
`
	var ports = ParseUpPorts(show)
	if len(ports) != 2 || ports[0] != "tg0/3" || ports[1] != "tg0/4" {
		t.Fatalf("ports=%v", ports)
	}
	if CompactPortRange(ports) != "range tg0/3-4" {
		t.Fatalf("compact=%q", CompactPortRange(ports))
	}
	if CompactPortRange([]string{"tg0/1"}) != "tg0/1" {
		t.Fatalf("single compact=%q", CompactPortRange([]string{"tg0/1"}))
	}
	if CompactPortRange([]string{"g0/48", "tg0/1"}) != "" {
		t.Fatal("mixed port types must not compact into one range")
	}
	if CompactPortRange([]string{"g0/1", "g0/3"}) != "" {
		t.Fatal("non-contiguous ports must not compact")
	}
}

func TestFingerprint(t *testing.T) {
	var d = &Driver{}
	if d.Fingerprint(driver.Evidence{ShellOut: "FS S3400-48T4SP Software, Version 2.2.0C"}) != 88 {
		t.Error("model string must score high")
	}
	if d.Fingerprint(driver.Evidence{SysDescr: "BDCOM(tm) Software"}) != 60 {
		t.Error("BDCOM software vendor is a medium clue")
	}
	// A bare FS.com clue must stay BELOW the S3900's weak score (50): on that
	// clue alone the S3900 — the range with an actual connector — must win.
	var weak = d.Fingerprint(driver.Evidence{HTTPTitle: "FS.com switch"})
	if weak <= 0 || weak >= 50 {
		t.Errorf("weak FS score must be in ]0,50[, got %d", weak)
	}
	if d.Fingerprint(driver.Evidence{ShellOut: "Cisco IOS Software"}) != 0 {
		t.Error("foreign platforms must not match")
	}
}

func TestMonitoringDeclarationIsCoherent(t *testing.T) {
	var caps = (&Driver{}).Capabilities("S3400-48T4SP")
	if len(caps.Monitoring.Contradictions()) != 0 {
		t.Fatalf("contradictory declaration: %v", caps.Monitoring.Contradictions())
	}
	var _, ok = caps.Monitoring.BestOfKind("snmp", "if_counters")
	if !ok {
		t.Error("if_counters must be served by an snmp channel")
	}
	// LLDP is declared, and its note must keep saying the platform ships it
	// OFF: a switch with no "lldp run" is invisible to every neighbour, and
	// the empty topology view is otherwise unexplainable.
	var lldp, hasLLDP = caps.Monitoring.BestOfKind("snmp", "lldp")
	if !hasLLDP {
		t.Fatal("the snmp lldp channel must be declared")
	}
	if !strings.Contains(strings.ToLower(lldp.Note), "off") {
		t.Errorf("the lldp note must say the platform ships LLDP off: %q", lldp.Note)
	}
	// optical_power stays UNDECLARED even though the CLI serves a DDM block:
	// the collector speaks only SNMP, and promising a channel that would
	// collect nothing is worse than naming the gap out loud.
	if _, ok = caps.Monitoring.BestOfKind("snmp", "optical_power"); ok {
		t.Error("optical_power must not be declared: the collector cannot read the CLI's DDM block")
	}
	var un driver.MetricUnavailable
	var named bool
	for _, un = range caps.Monitoring.Unavailable {
		if un.Metric == "optical_power" {
			named = true
		}
	}
	if !named {
		t.Error("optical_power must be declared ABSENT with its reason: the data exists on the device")
	}
}

func TestResetProcedureIsHonestAboutItsStatus(t *testing.T) {
	var p = (&Driver{}).ResetProcedure("S3400-48T4SP")
	if !p.Confirmed {
		t.Fatal("the software reset ran end to end on a 48T4SP (2026-08-03): Confirmed must be true")
	}
	if !strings.Contains(p.Software, "delete startup-config") {
		t.Errorf("software reset must be the reset-guide sequence: %q", p.Software)
	}
	var joined = strings.Join(p.Pitfalls, " ")
	if !strings.Contains(joined, "SFP+") {
		t.Error("the undocumented SFP+ naming must be flagged as a pitfall")
	}
	// Factory login is admin / admin (operator-confirmed), not admin / blank —
	// a refused blank login costs ~40 s per attempt on the serial console.
	var after = strings.Join(p.AfterReset, " ")
	if !strings.Contains(after, "admin / admin") {
		t.Errorf("AfterReset must state the admin / admin factory login: %q", after)
	}
}
